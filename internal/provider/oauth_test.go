package provider

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestParseAuthPreservesPrefix(t *testing.T) {
	t.Parallel()

	raw, errMarshal := json.Marshal(map[string]any{
		"type":                providerID,
		"prefix":              " /copilot/ ",
		"github_access_token": "github-token",
		"github_login":        "example-user",
		"updated_at":          "2026-08-24T00:00:00Z",
	})
	if errMarshal != nil {
		t.Fatalf("marshal auth: %v", errMarshal)
	}

	service := New(nil)
	resp, errParse := service.ParseAuth(pluginapi.AuthParseRequest{
		Provider: providerID,
		FileName: "copilot-example-user.json",
		RawJSON:  raw,
	})
	if errParse != nil {
		t.Fatalf("ParseAuth() error = %v", errParse)
	}
	if !resp.Handled {
		t.Fatal("ParseAuth() did not handle Copilot auth")
	}
	if resp.Auth.Prefix != "copilot" {
		t.Fatalf("ParseAuth() prefix = %q, want %q", resp.Auth.Prefix, "copilot")
	}
}

func TestClassifyDeviceToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		status        int
		token         oauthTokenResponse
		wantStatus    pluginapi.AuthLoginStatus
		wantTerminal  bool
		wantInterval  time.Duration
		messageNeedle string
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
			name:          "access denied",
			status:        400,
			token:         oauthTokenResponse{Error: "access_denied", ErrorDescription: "the user declined"},
			wantStatus:    pluginapi.AuthLoginStatusError,
			wantTerminal:  true,
			messageNeedle: "user declined",
		},
		{
			name:          "server error remains retryable",
			status:        503,
			token:         oauthTokenResponse{},
			wantStatus:    pluginapi.AuthLoginStatusError,
			wantTerminal:  false,
			messageNeedle: "503",
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
			if test.messageNeedle != "" && !strings.Contains(got.Message, test.messageNeedle) {
				t.Fatalf("message %q does not contain %q", got.Message, test.messageNeedle)
			}
			if test.wantStatus == pluginapi.AuthLoginStatusSuccess {
				if got.Token == nil || got.Token.AccessToken != "token-value" {
					t.Fatalf("success token was not normalized")
				}
			}
		})
	}
}
