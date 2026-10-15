package provider

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestClassifyDeviceToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		status       int
		token        oauthTokenResponse
		wantStatus   pluginapi.AuthLoginStatus
		wantTerminal bool
		wantInterval time.Duration
	}{
		{
			name:         "success",
			status:       200,
			token:        oauthTokenResponse{AccessToken: " token-value "},
			wantStatus:   pluginapi.AuthLoginStatusSuccess,
			wantTerminal: true,
		},
		{
			name:         "authorization pending",
			status:       200,
			token:        oauthTokenResponse{Error: "authorization_pending"},
			wantStatus:   pluginapi.AuthLoginStatusPending,
			wantInterval: 5 * time.Second,
		},
		{
			name:         "slow down",
			status:       200,
			token:        oauthTokenResponse{Error: "slow_down", Interval: 12},
			wantStatus:   pluginapi.AuthLoginStatusPending,
			wantInterval: 12 * time.Second,
		},
		{
			name:         "access denied",
			status:       400,
			token:        oauthTokenResponse{Error: "access_denied", ErrorDescription: "the user declined"},
			wantStatus:   pluginapi.AuthLoginStatusError,
			wantTerminal: true,
		},
		{
			name:         "server error remains retryable",
			status:       503,
			token:        oauthTokenResponse{},
			wantStatus:   pluginapi.AuthLoginStatusError,
			wantTerminal: false,
		},
		{
			name:         "access token on server error is rejected",
			status:       503,
			token:        oauthTokenResponse{AccessToken: "token-value"},
			wantStatus:   pluginapi.AuthLoginStatusError,
			wantTerminal: false,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := classifyDeviceToken(test.status, test.token, 5*time.Second)
			if got.Status != test.wantStatus {
				t.Fatalf("status = %q, want %q", got.Status, test.wantStatus)
			}
			if got.Terminal != test.wantTerminal {
				t.Fatalf("terminal = %v, want %v", got.Terminal, test.wantTerminal)
			}
			if got.NextInterval != test.wantInterval {
				t.Fatalf("next interval = %s, want %s", got.NextInterval, test.wantInterval)
			}
			if test.wantStatus == pluginapi.AuthLoginStatusSuccess {
				if got.Token == nil || got.Token.AccessToken != "token-value" {
					t.Fatalf("success token was not normalized")
				}
			} else if got.Token != nil {
				t.Fatalf("non-success decision admitted a token: %#v", got)
			}
		})
	}
}

func TestRefreshAuthPreservesAdmissionAndInvalidation(t *testing.T) {
	t.Parallel()
	now := time.Unix(100000, 0)
	for _, test := range []struct {
		name        string
		expiresAt   int64
		refresh     string
		wantCalls   int
		wantError   bool
		wantCleared bool
	}{
		{name: "no expiry", refresh: "synthetic-refresh"},
		{name: "valid beyond buffer", expiresAt: now.Add(11 * time.Minute).Unix(), refresh: "synthetic-refresh"},
		{name: "buffer boundary refresh", expiresAt: now.Add(10 * time.Minute).Unix(), refresh: "synthetic-refresh", wantCalls: 1, wantCleared: true},
		{name: "expired without refresh token", expiresAt: now.Add(-time.Minute).Unix(), wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := &oauthDeviceLifecycleHost{}
			service := New(host)
			service.now = func() time.Time { return now }
			service.tokenEntries["account"] = copilotTokenEntry{}
			service.modelEntries["account"] = modelCacheEntry{}
			raw, err := marshalStorage(authStorage{Type: providerID, GitHubAccessToken: "synthetic-original", GitHubRefreshToken: test.refresh, ExpiresAt: test.expiresAt})
			if err != nil {
				t.Fatalf("marshal fixture: %v", err)
			}
			response, err := service.RefreshAuth(context.Background(), "callback", pluginapi.AuthRefreshRequest{AuthID: "account", StorageJSON: raw})
			if test.wantError {
				if err == nil {
					t.Error("expired credential was admitted without a refresh token")
				}
			} else if err != nil {
				t.Errorf("refresh: %v", err)
			} else {
				storage, errParse := parseStorage(response.Auth.StorageJSON)
				if errParse != nil {
					t.Fatalf("parse refreshed storage: %v", errParse)
				}
				wantAccess := "synthetic-original"
				if test.wantCalls > 0 {
					wantAccess = "synthetic-access-token"
				}
				if storage.GitHubAccessToken != wantAccess || response.Auth.ID != "account" {
					t.Errorf("access token or auth identity changed unexpectedly")
				}
			}
			if host.tokenRequests != test.wantCalls {
				t.Errorf("refresh requests = %d, want %d", host.tokenRequests, test.wantCalls)
			}
			_, tokenCached := service.tokenEntries["account"]
			_, modelCached := service.modelEntries["account"]
			if tokenCached != !test.wantCleared || modelCached != !test.wantCleared {
				t.Errorf("token and model cache invalidation did not match refresh admission")
			}
		})
	}
}
