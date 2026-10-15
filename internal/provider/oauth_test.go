package provider

import (
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
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

func TestValidateGitHubVerificationURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		url           string
		base          string
		allowInsecure bool
		wantError     bool
	}{
		{name: "same HTTPS origin", url: "https://github.com/login/device?user_code=ABCD", base: "https://github.com"},
		{name: "external host", url: "https://attacker.example/device", base: "https://github.com", wantError: true},
		{name: "userinfo", url: "https://github.com@attacker.example/device", base: "https://github.com", wantError: true},
		{name: "HTTP denied", url: "http://github.com/device", base: "http://github.com", wantError: true},
		{name: "HTTP explicit test mode", url: "http://github.com/device", base: "http://github.com", allowInsecure: true},
		{name: "fragment denied", url: "https://github.com/device#token", base: "https://github.com", wantError: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := validateGitHubVerificationURL(test.url, test.base, test.allowInsecure)
			if (err != nil) != test.wantError {
				t.Fatalf("validation error = %v, want error=%v", err, test.wantError)
			}
		})
	}
}

func TestCopilotAPIBaseRejectsUntrustedURLParts(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	if _, err := copilotAPIBase(map[string]string{"api": "https://token@api.example"}, cfg); err == nil {
		t.Fatal("API endpoint with userinfo was accepted")
	}
	if _, err := copilotAPIBase(map[string]string{"api": "http://api.example"}, cfg); err == nil {
		t.Fatal("HTTP API endpoint was accepted without insecure test mode")
	}
	cfg.AllowInsecureBaseURLs = true
	if endpoint, err := copilotAPIBase(map[string]string{"api": "http://api.example"}, cfg); err != nil || endpoint != "http://api.example" {
		t.Fatalf("explicit insecure test endpoint = %q, error=%v", endpoint, err)
	}
}
