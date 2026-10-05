package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeHostOAuthContinuityPersistsAcrossRestart(t *testing.T) {
	binary := os.Getenv("CPA_BINARY")
	if binary == "" {
		t.Skip("set CPA_BINARY to a built CLIProxyAPI v8 server for native host integration")
	}
	state := &fixture{canceled: make(chan struct{})}
	upstream := httptest.NewServer(state)
	t.Cleanup(upstream.Close)
	root := t.TempDir()
	base, stop := startProxyInRoot(t, binary, upstream.URL, root, legacyOAuthAuthFixtureJSON(t), "fixture-management-secret")
	forceRefreshAuthFile(t, base, "fixture-management-secret", "fixture.json")
	firstStorage := readPersistedOAuthStorage(t, root)
	assertMigratedOAuthStorage(t, firstStorage, upstream.URL)
	route := matrixRoute{name: "ResponsesToGeminiChat", clientFormat: "openai-response", model: matrixChatModel, upstreamPath: "/chat/completions"}
	firstRequest := matrixInitialRequest(route, false)
	firstResponse := callProxyWithSession(t, base+matrixClientPath(route.clientFormat), firstRequest, "oauth-root-persistence")
	firstCaptured, firstPath := lastUpstreamRequest(t, state)
	assertMatrixRoute(t, route, firstCaptured, firstPath, false)
	assertMatrixInitialRequest(t, route, firstCaptured)
	output := assertMatrixClientOutput(t, route, firstResponse, false)
	stop()
	base, _ = startProxyInRoot(t, binary, upstream.URL, root, nil, "fixture-management-secret")
	secondStorage := readPersistedOAuthStorage(t, root)
	if secondStorage.Continuity.RootKey != firstStorage.Continuity.RootKey || secondStorage.Continuity.KeyID != firstStorage.Continuity.KeyID {
		t.Fatal("host restart changed the persisted Copilot continuity root")
	}
	followup := matrixFollowupRequest(route, output)
	callProxyWithSession(t, base+matrixClientPath(route.clientFormat), followup, "oauth-root-persistence")
	followupCaptured, followupPath := lastUpstreamRequest(t, state)
	assertMatrixRoute(t, route, followupCaptured, followupPath, false)
	assertMatrixFollowupRequest(t, route, followupCaptured, output)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.oauthRefreshCount != 1 {
		t.Fatalf("OAuth refresh requests = %d, want one", state.oauthRefreshCount)
	}
	if len(state.githubUserAuth) != 2 || state.githubUserAuth[0] != "Bearer fixture-old-github-token" || state.githubUserAuth[1] != "Bearer fixture-new-github-token" {
		t.Fatalf("GitHub identity checks did not use old and refreshed synthetic credentials")
	}
}

type persistedOAuthStorage struct {
	GitHubAccessToken string `json:"github_access_token"`
	GitHubUserID      int64  `json:"github_user_id"`
	Continuity        struct {
		Version               int    `json:"version"`
		KeyID                 string `json:"key_id"`
		AccountID             int64  `json:"account_id"`
		RootKey               string `json:"root_key"`
		CredentialFingerprint string `json:"credential_fingerprint"`
		Legacy                *struct {
			AccountID             int64  `json:"account_id"`
			AuthID                string `json:"auth_id"`
			CredentialFingerprint string `json:"credential_fingerprint"`
			APIBaseURL            string `json:"api_base_url"`
		} `json:"legacy_v1"`
	} `json:"continuity_keyring"`
}

func legacyOAuthAuthFixtureJSON(t *testing.T) []byte {
	t.Helper()
	storage := map[string]any{
		"type":                     "copilot",
		"github_access_token":      "fixture-old-github-token",
		"github_refresh_token":     "fixture-refresh-token",
		"github_login":             "fixture",
		"github_user_id":           4242,
		"oauth_client_id":          "fixture-oauth-client-id",
		"expires_at":               time.Now().Add(-time.Hour).Unix(),
		"refresh_token_expires_at": time.Now().Add(24 * time.Hour).Unix(),
	}
	body, err := json.Marshal(storage)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func forceRefreshAuthFile(t *testing.T, base, secret, name string) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v0/management/auth-files/refresh", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+secret)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("force-refresh synthetic auth status = %d, want 200", response.StatusCode)
	}
}

func readPersistedOAuthStorage(t *testing.T, root string) persistedOAuthStorage {
	t.Helper()
	path := filepath.Join(root, "auths", "fixture.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted synthetic auth state: %v", err)
	}
	var storage persistedOAuthStorage
	if err := json.Unmarshal(body, &storage); err != nil {
		t.Fatalf("decode persisted synthetic auth state: %v", err)
	}
	rootKey, err := base64.RawURLEncoding.DecodeString(storage.Continuity.RootKey)
	if err != nil || len(rootKey) != sha256.Size {
		t.Fatal("persisted continuity root is not a 32-byte RawURL key")
	}
	if storage.Continuity.Version != 1 || storage.Continuity.AccountID != 4242 || storage.GitHubUserID != 4242 || len(storage.Continuity.KeyID) != 32 {
		t.Fatalf("persisted continuity identity is malformed: version=%d account=%d user=%d", storage.Continuity.Version, storage.Continuity.AccountID, storage.GitHubUserID)
	}
	return storage
}

func assertMigratedOAuthStorage(t *testing.T, storage persistedOAuthStorage, apiBaseURL string) {
	t.Helper()
	if storage.GitHubAccessToken != "fixture-new-github-token" {
		t.Fatal("OAuth refresh did not persist the new synthetic access token")
	}
	newFingerprint := sha256.Sum256([]byte(storage.GitHubAccessToken))
	if storage.Continuity.CredentialFingerprint != hex.EncodeToString(newFingerprint[:]) {
		t.Fatal("persisted continuity root is not bound to the refreshed credential")
	}
	legacy := storage.Continuity.Legacy
	if legacy == nil {
		t.Fatal("persisted legacy continuity binding is missing")
	}
	if legacy.AccountID != 4242 || legacy.AuthID == "" || legacy.APIBaseURL != strings.TrimRight(apiBaseURL, "/") {
		t.Fatalf("persisted legacy continuity binding is malformed: account=%d auth_id=%t api_base_match=%t", legacy.AccountID, legacy.AuthID != "", legacy.APIBaseURL == strings.TrimRight(apiBaseURL, "/"))
	}
	oldFingerprint := sha256.Sum256([]byte("fixture-old-github-token"))
	if legacy.CredentialFingerprint != hex.EncodeToString(oldFingerprint[:]) {
		t.Fatal("persisted legacy continuity binding is not scoped to the pre-refresh credential")
	}
}
