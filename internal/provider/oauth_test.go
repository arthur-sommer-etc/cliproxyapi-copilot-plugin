package provider

import (
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

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
