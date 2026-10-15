package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/compact"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type oauthPersistenceTestHost struct {
	refreshTestHost
	userResponses   []transport.Response
	userErrors      []error
	discoveryError  error
	oauthCalls      int
	userCredentials []string
	oauthResponses  []transport.Response
	oauthForms      []url.Values
}

func (h *oauthPersistenceTestHost) Do(ctx context.Context, callbackID string, request transport.Request) (transport.Response, error) {
	if request.URL == "https://api.github.com/user" {
		h.userCredentials = append(h.userCredentials, request.Headers.Get("Authorization"))
		if len(h.userErrors) > 0 {
			err := h.userErrors[0]
			h.userErrors = h.userErrors[1:]
			if err != nil {
				return transport.Response{}, err
			}
		}
		if len(h.userResponses) > 0 {
			response := h.userResponses[0]
			h.userResponses = h.userResponses[1:]
			return response, nil
		}
	}
	if strings.HasSuffix(request.URL, "/copilot_internal/v2/token") || strings.HasSuffix(request.URL, "/copilot_internal/user") {
		if h.discoveryError != nil {
			return transport.Response{}, h.discoveryError
		}
		if strings.HasSuffix(request.URL, "/copilot_internal/user") {
			return transport.Response{StatusCode: http.StatusOK, Body: []byte(`{"endpoints":{"api":"https://api.example"}}`)}, nil
		}
	}
	if strings.HasSuffix(request.URL, "/login/oauth/access_token") {
		h.oauthCalls++
		form, err := url.ParseQuery(string(request.Body))
		if err != nil {
			return transport.Response{}, err
		}
		h.oauthForms = append(h.oauthForms, form)
		if len(h.oauthResponses) > 0 {
			response := h.oauthResponses[0]
			h.oauthResponses = h.oauthResponses[1:]
			return response, nil
		}
		if h.oauthCalls > 1 {
			return transport.Response{StatusCode: http.StatusBadRequest, Body: []byte(`{"error":"invalid_grant"}`)}, nil
		}
	}
	return h.refreshTestHost.Do(ctx, callbackID, request)
}

func oauthPersistenceStorage(t *testing.T, now time.Time, legacy bool) []byte {
	t.Helper()
	storage := authStorage{Type: providerID, GitHubAccessToken: "synthetic-old-access", GitHubRefreshToken: "synthetic-old-refresh", GitHubLogin: "fixture-user", GitHubUserID: 4242, OAuthClientID: "stored-client", ExpiresAt: now.Add(5 * time.Minute).Unix(), RefreshTokenExpiresAt: now.Add(24 * time.Hour).Unix()}
	if !legacy {
		keyring, err := newContinuityKeyring(4242, storage.GitHubAccessToken)
		if err != nil {
			t.Fatal(err)
		}
		storage.ContinuityKeyring = keyring
	}
	raw, err := marshalStorage(storage)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func oauthPersistenceHost() *oauthPersistenceTestHost {
	return &oauthPersistenceTestHost{refreshTestHost: refreshTestHost{oauthResponse: transport.Response{StatusCode: http.StatusOK, Body: []byte(`{"access_token":"synthetic-rotated-access","refresh_token":"synthetic-rotated-refresh","token_type":"bearer","scope":"read:user","expires_in":3600,"refresh_token_expires_in":86400}`)}}}
}

func TestRefreshAuthPersistsRotationUntilAccountVerificationRecovers(t *testing.T) {
	for _, failure := range []struct {
		name     string
		response transport.Response
		err      error
	}{
		{name: "transport", err: errors.New("synthetic temporary network failure")},
		{name: "HTTP 503", response: transport.Response{StatusCode: http.StatusServiceUnavailable, Body: []byte(`{"error":"temporarily unavailable"}`)}},
		{name: "invalid user body", response: transport.Response{StatusCode: http.StatusOK, Body: []byte(`{"login":"fixture-user"}`)}},
	} {
		t.Run(failure.name, func(t *testing.T) {
			now := time.Unix(1_900_000_000, 0).UTC()
			host := oauthPersistenceHost()
			host.userErrors = []error{failure.err}
			if failure.err == nil {
				host.userResponses = []transport.Response{failure.response}
			}
			service := New(host)
			service.now = func() time.Time { return now }
			raw := oauthPersistenceStorage(t, now, false)
			original, err := parseStorage(raw)
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.RefreshAuth(context.Background(), "callback", pluginapi.AuthRefreshRequest{AuthID: "auth-id", StorageJSON: raw, Metadata: map[string]any{"fixture": "kept"}, Attributes: map[string]string{"fixture": "kept"}})
			if err != nil {
				t.Fatalf("rotated credentials were discarded after user lookup failure: %v", err)
			}
			if !strings.Contains(string(result.Auth.StorageJSON), "synthetic-rotated-access") || !strings.Contains(string(result.Auth.StorageJSON), "synthetic-rotated-refresh") {
				t.Fatal("host persistence response lost rotated credentials")
			}
			active, err := parseStorage(result.Auth.StorageJSON)
			if err != nil {
				t.Fatal(err)
			}
			if active.GitHubAccessToken != original.GitHubAccessToken || active.GitHubRefreshToken != original.GitHubRefreshToken {
				t.Fatal("unverified rotation became active")
			}
			if active.ContinuityKeyring.RootKey != original.ContinuityKeyring.RootKey || active.GitHubUserID != 4242 {
				t.Fatal("pending rotation changed identity or continuity")
			}
			if result.Auth.Metadata["refresh_interval_seconds"] != 60 {
				t.Fatal("pending verification lost the host scheduler retry interval")
			}
			if !result.NextRefreshAfter.Equal(now.Add(time.Minute)) || !result.Auth.NextRefreshAfter.Equal(result.NextRefreshAfter) || result.Auth.Disabled {
				t.Fatal("pending account verification was not scheduled for retry")
			}
			parsed, err := service.ParseAuth(pluginapi.AuthParseRequest{Provider: providerID, FileName: "auth-id", RawJSON: result.Auth.StorageJSON})
			if err != nil || !strings.Contains(string(parsed.Auth.StorageJSON), "synthetic-rotated-refresh") {
				t.Fatalf("restart parser lost pending rotation: %v", err)
			}
			now = now.Add(2 * time.Minute)
			restarted := New(host)
			restarted.now = func() time.Time { return now }
			recovered, err := restarted.RefreshAuth(context.Background(), "callback", pluginapi.AuthRefreshRequest{AuthID: "auth-id", StorageJSON: parsed.Auth.StorageJSON, Metadata: result.Auth.Metadata, Attributes: result.Auth.Attributes})
			if err != nil {
				t.Fatalf("pending verification retry: %v", err)
			}
			rotated, err := parseStorage(recovered.Auth.StorageJSON)
			if err != nil {
				t.Fatal(err)
			}
			if rotated.GitHubAccessToken != "synthetic-rotated-access" || rotated.GitHubRefreshToken != "synthetic-rotated-refresh" || rotated.ExpiresAt != now.Add(58*time.Minute).Unix() || rotated.RefreshTokenExpiresAt != now.Add(24*time.Hour-2*time.Minute).Unix() {
				t.Fatal("verified rotation lost credentials or extended token lifetimes on retry")
			}
			if rotated.ContinuityKeyring.RootKey != original.ContinuityKeyring.RootKey || rotated.ContinuityKeyring.CredentialFingerprint != tokenFingerprint(rotated.GitHubAccessToken) {
				t.Fatal("verified rotation did not preserve continuity")
			}
			if _, err := validateContinuityKeyring(rotated, "auth-id"); err != nil {
				t.Fatal(err)
			}
			if host.oauthCalls != 1 || len(host.userCredentials) != 2 || host.userCredentials[0] != "Bearer synthetic-rotated-access" || host.userCredentials[1] != host.userCredentials[0] {
				t.Fatal("retry reused the revoked old refresh token or checked the wrong access token")
			}
			form, err := url.ParseQuery(string(host.oauthRequest.Body))
			if err != nil || form.Get("client_id") != "stored-client" || form.Get("refresh_token") != "synthetic-old-refresh" {
				t.Fatal("rotation request lost stored OAuth binding")
			}
			if strings.Contains(string(recovered.Auth.StorageJSON), "pending_oauth_refresh") {
				t.Fatal("verified rotation retained obsolete pending state")
			}
			if _, ok := recovered.Auth.Metadata["refresh_interval_seconds"]; ok {
				t.Fatal("verified rotation retained the temporary retry interval")
			}
			if recovered.Auth.Metadata["fixture"] != "kept" || recovered.Auth.Attributes["fixture"] != "kept" {
				t.Fatal("pending persistence lost host metadata")
			}
		})
	}
}

func TestRefreshAuthPendingRotationRejectsChangedAccount(t *testing.T) {
	now := time.Unix(1_900_000_000, 0).UTC()
	host := oauthPersistenceHost()
	host.userErrors = []error{errors.New("synthetic user lookup outage")}
	service := New(host)
	service.now = func() time.Time { return now }
	pending, err := service.RefreshAuth(context.Background(), "callback", pluginapi.AuthRefreshRequest{AuthID: "auth-id", StorageJSON: oauthPersistenceStorage(t, now, false)})
	if err != nil {
		t.Fatalf("persist pending rotation: %v", err)
	}
	host.githubUserID = 9191
	result, err := service.RefreshAuth(context.Background(), "callback", pluginapi.AuthRefreshRequest{AuthID: "auth-id", StorageJSON: pending.Auth.StorageJSON})
	status, ok := err.(*StatusError)
	if !ok || status.Code != "account_mismatch" || len(result.Auth.StorageJSON) != 0 || host.oauthCalls != 1 {
		t.Fatalf("pending rotation accepted changed account: %v", err)
	}
}

func TestRefreshAuthLegacyDiscoveryFailureRemainsRetryable(t *testing.T) {
	for _, due := range []bool{false, true} {
		t.Run(map[bool]string{false: "initialization", true: "before rotation"}[due], func(t *testing.T) {
			now := time.Unix(1_900_000_000, 0).UTC()
			host := oauthPersistenceHost()
			host.discoveryError = errors.New("synthetic temporary endpoint discovery failure")
			service := New(host)
			service.now = func() time.Time { return now }
			raw := oauthPersistenceStorage(t, now, true)
			if !due {
				var fields map[string]any
				if err := json.Unmarshal(raw, &fields); err != nil {
					t.Fatal(err)
				}
				delete(fields, "expires_at")
				var err error
				raw, err = json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
			}
			result, err := service.RefreshAuth(context.Background(), "callback", pluginapi.AuthRefreshRequest{AuthID: "auth-id", StorageJSON: raw})
			if due {
				if err != nil {
					t.Fatalf("legacy discovery failure prevented token refresh: %v", err)
				}
				active, err := parseStorage(result.Auth.StorageJSON)
				if err != nil || active.ContinuityKeyring.LegacyV1 == nil || !active.ContinuityKeyring.LegacyV1.EndpointPending || active.ContinuityKeyring.LegacyV1.APIBaseURL != "" {
					t.Fatal("legacy discovery failure lost old seed or guessed endpoint")
				}
				raw = result.Auth.StorageJSON
			} else if err == nil || len(result.Auth.StorageJSON) != 0 || host.oauthCalls != 0 {
				t.Fatal("failed legacy initialization persisted an incomplete keyring")
			}
			host.discoveryError = nil
			recovered, err := service.RefreshAuth(context.Background(), "callback", pluginapi.AuthRefreshRequest{AuthID: "auth-id", StorageJSON: raw})
			if err != nil {
				t.Fatalf("retry legacy migration: %v", err)
			}
			migrated, err := parseStorage(recovered.Auth.StorageJSON)
			if err != nil {
				t.Fatal(err)
			}
			materials, err := continuityKeyMaterialsFor(migrated, "auth-id", "gpt-6-luna", "/responses", "https://api.example")
			if err != nil || len(materials.Legacy) != 1 {
				t.Fatalf("retry lost legacy replay material: %v", err)
			}
			wantScope, wantSecret := compactionKeyMaterial("auth-id", "synthetic-old-access", "gpt-6-luna", "/responses", "https://api.example")
			if materials.Legacy[0].Scope != wantScope || string(materials.Legacy[0].Secret) != string(wantSecret) {
				t.Fatal("migration did not preserve original credential and endpoint binding")
			}
			if _, err := validateContinuityKeyring(migrated, "other-auth-id"); err == nil {
				t.Fatal("migration accepted a different auth ID")
			}
			otherEndpoint, err := continuityKeyMaterialsFor(migrated, "auth-id", "gpt-6-luna", "/responses", "https://other.example")
			if err != nil {
				t.Fatal(err)
			}
			if !due && len(otherEndpoint.Legacy) != 0 {
				t.Fatal("legacy replay escaped its discovered endpoint binding")
			}
			assertOAuthLegacyCapsuleBinding(t, migrated)
			reloaded, err := New(host).RefreshAuth(context.Background(), "callback", pluginapi.AuthRefreshRequest{AuthID: "auth-id", StorageJSON: recovered.Auth.StorageJSON})
			if err != nil {
				t.Fatalf("reload migrated auth: %v", err)
			}
			persistent, err := parseStorage(reloaded.Auth.StorageJSON)
			if err != nil || persistent.ContinuityKeyring.LegacyV1.CredentialFingerprint != tokenFingerprint("synthetic-old-access") {
				t.Fatal("later refresh dropped original legacy key")
			}
		})
	}
}

func TestRefreshAuthRestoresHostRefreshPreferenceAfterPendingRetry(t *testing.T) {
	now := time.Unix(1_900_000_000, 0).UTC()
	host := oauthPersistenceHost()
	host.userErrors = []error{errors.New("synthetic temporary user failure")}
	service := New(host)
	service.now = func() time.Time { return now }
	metadata := map[string]any{"refresh_interval_seconds": "900", "fixture": "kept"}
	pending, err := service.RefreshAuth(context.Background(), "callback", pluginapi.AuthRefreshRequest{AuthID: "auth-id", StorageJSON: oauthPersistenceStorage(t, now, false), Metadata: metadata})
	if err != nil {
		t.Fatal(err)
	}
	if metadata["refresh_interval_seconds"] != "900" {
		t.Fatal("pending response mutated original host metadata")
	}
	rotated, err := service.RefreshAuth(context.Background(), "callback", pluginapi.AuthRefreshRequest{AuthID: "auth-id", StorageJSON: pending.Auth.StorageJSON, Metadata: pending.Auth.Metadata})
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Auth.Metadata["refresh_interval_seconds"] != "900" || rotated.Auth.Metadata["fixture"] != "kept" {
		t.Fatal("verified rotation lost original host refresh preference")
	}
}

func TestRefreshAuthPendingLegacyRotationPreservesOriginalReplayKey(t *testing.T) {
	for _, mode := range []string{authModeTokenExchange, authModeDirectOAuth} {
		t.Run(mode, func(t *testing.T) {
			now := time.Unix(1_900_000_000, 0).UTC()
			host := oauthPersistenceHost()
			host.userErrors = []error{nil, errors.New("synthetic rotated user lookup outage")}
			service := New(host)
			if err := service.Configure([]byte("auth_mode: " + mode + "\n")); err != nil {
				t.Fatal(err)
			}
			service.now = func() time.Time { return now }
			pending, err := service.RefreshAuth(context.Background(), "callback", pluginapi.AuthRefreshRequest{AuthID: "auth-id", StorageJSON: oauthPersistenceStorage(t, now, true)})
			if err != nil {
				t.Fatal(err)
			}
			staged, err := parseStorage(pending.Auth.StorageJSON)
			if err != nil {
				t.Fatal(err)
			}
			var pendingState oauthRefreshStorage
			if err := json.Unmarshal(pending.Auth.StorageJSON, &pendingState); err != nil {
				t.Fatal(err)
			}
			if staged.ContinuityKeyring != nil || pendingState.PendingRefresh.LegacyMigration == nil {
				t.Fatal("pending legacy rotation activated unverified identity or lost original replay seed")
			}
			rotated, err := New(host).RefreshAuth(context.Background(), "callback", pluginapi.AuthRefreshRequest{AuthID: "auth-id", StorageJSON: pending.Auth.StorageJSON, Metadata: pending.Auth.Metadata})
			if err != nil {
				t.Fatal(err)
			}
			active, err := parseStorage(rotated.Auth.StorageJSON)
			if err != nil {
				t.Fatal(err)
			}
			materials, err := continuityKeyMaterialsFor(active, "auth-id", "gpt-6-luna", "/responses", "https://api.example")
			if err != nil || len(materials.Legacy) != 1 {
				t.Fatalf("verified legacy replay missing: %v", err)
			}
			wantScope, wantSecret := compactionKeyMaterial("auth-id", "synthetic-old-access", "gpt-6-luna", "/responses", "https://api.example")
			if materials.Legacy[0].Scope != wantScope || string(materials.Legacy[0].Secret) != string(wantSecret) || host.oauthCalls != 1 {
				t.Fatal("pending retry changed legacy replay key or reused old OAuth refresh")
			}
		})
	}
}

func assertOAuthLegacyCapsuleBinding(t *testing.T, storage authStorage) {
	t.Helper()
	scope, secret := compactionKeyMaterial("auth-id", "synthetic-old-access", "gpt-6-luna", "/responses", "https://api.example")
	result, err := compact.Complete([]byte(`{"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Synthetic original summary"}]}]}`), compact.KeyMaterial{Scope: scope, Secret: secret}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Output []json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(map[string]any{"input": response.Output})
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"https://api.example", "https://different.example"} {
		materials, err := continuityKeyMaterialsFor(storage, "auth-id", "gpt-6-luna", "/responses", endpoint)
		if err != nil {
			t.Fatal(err)
		}
		prepared, _, err := compact.Prepare(request, materials.Active, materials.Legacy)
		if endpoint == "https://api.example" {
			if err != nil || !bytes.Contains(prepared, []byte("Synthetic original summary")) {
				t.Fatalf("original endpoint failed authenticated legacy replay: %v", err)
			}
		} else if !errors.Is(err, compact.ErrInvalidCapsule) {
			t.Fatalf("different endpoint accepted original capsule: %v", err)
		}
	}
	storage.GitHubUserID++
	if _, err := continuityKeyMaterialsFor(storage, "auth-id", "gpt-6-luna", "/responses", "https://api.example"); err == nil {
		t.Fatal("legacy replay accepted changed account")
	}
}

func TestRefreshAuthExpiredLegacyCredentialPreservesRefreshLivenessAndReplay(t *testing.T) {
	for _, knownAccount := range []bool{false, true} {
		t.Run(map[bool]string{false: "original account absent", true: "stored account"}[knownAccount], func(t *testing.T) {
			now := time.Unix(1_900_000_000, 0).UTC()
			host := oauthPersistenceHost()
			host.userResponses = []transport.Response{{StatusCode: http.StatusUnauthorized, Body: []byte(`{"message":"Bad credentials"}`)}}
			service := New(host)
			service.now = func() time.Time { return now }
			storage, err := parseStorage(oauthPersistenceStorage(t, now, true))
			if err != nil {
				t.Fatal(err)
			}
			storage.ExpiresAt = now.Add(-time.Minute).Unix()
			if !knownAccount {
				storage.GitHubUserID = 0
			}
			raw, err := marshalStorage(storage)
			if err != nil {
				t.Fatal(err)
			}
			rotated, err := service.RefreshAuth(context.Background(), "callback", pluginapi.AuthRefreshRequest{AuthID: "auth-id", StorageJSON: raw})
			if err != nil {
				t.Fatalf("expired legacy refresh was blocked: %v", err)
			}
			active, err := parseStorage(rotated.Auth.StorageJSON)
			if err != nil {
				t.Fatal(err)
			}
			legacy := active.ContinuityKeyring.LegacyV1
			if active.GitHubUserID != 4242 || active.GitHubAccessToken != "synthetic-rotated-access" || legacy == nil || legacy.AccountID != 4242 || !legacy.EndpointPending || legacy.APIBaseURL != "" || legacy.CredentialFingerprint != tokenFingerprint(storage.GitHubAccessToken) {
				t.Fatal("expired legacy rotation lost verified identity, old seed, or endpoint uncertainty")
			}
			assertOAuthLegacyCapsuleBinding(t, active)
			if _, err := validateContinuityKeyring(active, "other-auth-id"); err == nil {
				t.Fatal("expired legacy migration accepted changed auth ID")
			}
		})
	}
}

func TestRefreshAuthExpiredPendingCandidateUsesLatestRefreshPair(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, omitRefresh := range []bool{false, true} {
			t.Run(fmt.Sprintf("legacy=%v/refresh_omitted=%v", legacy, omitRefresh), func(t *testing.T) {
				now := time.Unix(1_900_000_000, 0).UTC()
				host := oauthPersistenceHost()
				host.userErrors = []error{errors.New("synthetic user lookup outage")}
				if legacy {
					host.userErrors = append([]error{nil}, host.userErrors...)
				}
				nextToken := oauthTokenResponse{AccessToken: "synthetic-second-access", ExpiresIn: 3600}
				wantRefresh := "synthetic-rotated-refresh"
				if !omitRefresh {
					nextToken.RefreshToken = "synthetic-second-refresh"
					nextToken.RefreshTokenExpiresIn = 86400
					wantRefresh = nextToken.RefreshToken
				}
				body, err := json.Marshal(nextToken)
				if err != nil {
					t.Fatal(err)
				}
				host.oauthResponses = []transport.Response{host.oauthResponse, {StatusCode: http.StatusOK, Body: body}}
				service := New(host)
				service.now = func() time.Time { return now }
				pending, err := service.RefreshAuth(context.Background(), "callback", pluginapi.AuthRefreshRequest{AuthID: "auth-id", StorageJSON: oauthPersistenceStorage(t, now, legacy)})
				if err != nil {
					t.Fatal(err)
				}
				now = now.Add(2 * time.Hour)
				rotated, err := service.RefreshAuth(context.Background(), "callback", pluginapi.AuthRefreshRequest{AuthID: "auth-id", StorageJSON: pending.Auth.StorageJSON, Metadata: pending.Auth.Metadata})
				if err != nil {
					t.Fatalf("expired pending candidate could not refresh: %v", err)
				}
				active, err := parseStorage(rotated.Auth.StorageJSON)
				if err != nil {
					t.Fatal(err)
				}
				if active.GitHubAccessToken != "synthetic-second-access" || active.GitHubRefreshToken != wantRefresh || active.ExpiresAt != now.Add(time.Hour).Unix() {
					t.Fatal("expired candidate did not persist latest pair")
				}
				if omitRefresh && active.RefreshTokenExpiresAt != now.Add(22*time.Hour).Unix() {
					t.Fatal("omitted expiry extended retained pending refresh lifetime")
				}
				if len(host.oauthForms) != 2 || host.oauthForms[0].Get("refresh_token") != "synthetic-old-refresh" || host.oauthForms[1].Get("refresh_token") != "synthetic-rotated-refresh" {
					t.Fatal("expired candidate reused revoked refresh token")
				}
				if legacy {
					assertOAuthLegacyCapsuleBinding(t, active)
				}
			})
		}
	}
}

func TestRefreshAuthLegacyAccountMismatchFailsClosed(t *testing.T) {
	for _, oldRejected := range []bool{false, true} {
		t.Run(fmt.Sprintf("old_rejected=%v", oldRejected), func(t *testing.T) {
			now := time.Unix(1_900_000_000, 0).UTC()
			host := oauthPersistenceHost()
			host.githubUserID = 9191
			if oldRejected {
				host.userResponses = []transport.Response{{StatusCode: http.StatusUnauthorized}}
			}
			service := New(host)
			service.now = func() time.Time { return now }
			result, err := service.RefreshAuth(context.Background(), "callback", pluginapi.AuthRefreshRequest{AuthID: "auth-id", StorageJSON: oauthPersistenceStorage(t, now, true)})
			status, ok := err.(*StatusError)
			wantRotations := 0
			if oldRejected {
				wantRotations = 1
			}
			if !ok || status.Code != "account_mismatch" || len(result.Auth.StorageJSON) != 0 || host.oauthCalls != wantRotations {
				t.Fatalf("legacy rotation accepted changed account or consumed token before validation: %v", err)
			}
		})
	}
}

func TestRefreshAuthExpiredLegacyPendingSeedSurvivesRestart(t *testing.T) {
	now := time.Unix(1_900_000_000, 0).UTC()
	host := oauthPersistenceHost()
	host.userResponses = []transport.Response{{StatusCode: http.StatusUnauthorized}, {StatusCode: http.StatusServiceUnavailable}}
	storage, err := parseStorage(oauthPersistenceStorage(t, now, true))
	if err != nil {
		t.Fatal(err)
	}
	storage.GitHubUserID = 0
	storage.ExpiresAt = now.Add(-time.Minute).Unix()
	raw, err := marshalStorage(storage)
	if err != nil {
		t.Fatal(err)
	}
	service := New(host)
	service.now = func() time.Time { return now }
	pending, err := service.RefreshAuth(context.Background(), "callback", pluginapi.AuthRefreshRequest{AuthID: "auth-id", StorageJSON: raw})
	if err != nil {
		t.Fatal(err)
	}
	var state oauthRefreshStorage
	if err := json.Unmarshal(pending.Auth.StorageJSON, &state); err != nil {
		t.Fatal(err)
	}
	if state.GitHubUserID != 0 || state.ContinuityKeyring != nil || state.PendingRefresh.LegacyMigration.AccountID != 0 || !state.PendingRefresh.LegacyMigration.EndpointPending {
		t.Fatal("pending expired legacy state acquired an unverified identity or lost endpoint uncertainty")
	}
	restarted := New(host)
	restarted.now = func() time.Time { return now }
	parsed, err := restarted.ParseAuth(pluginapi.AuthParseRequest{Provider: providerID, FileName: "auth-id", RawJSON: pending.Auth.StorageJSON})
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := restarted.RefreshAuth(context.Background(), "callback", pluginapi.AuthRefreshRequest{AuthID: "auth-id", StorageJSON: parsed.Auth.StorageJSON, Metadata: parsed.Auth.Metadata})
	if err != nil {
		t.Fatal(err)
	}
	active, err := parseStorage(rotated.Auth.StorageJSON)
	if err != nil {
		t.Fatal(err)
	}
	if active.GitHubUserID != 4242 || active.ContinuityKeyring.LegacyV1.AccountID != 4242 || host.oauthCalls != 1 {
		t.Fatal("restarted pending seed failed verified account binding or reused old refresh")
	}
	assertOAuthLegacyCapsuleBinding(t, active)
}
