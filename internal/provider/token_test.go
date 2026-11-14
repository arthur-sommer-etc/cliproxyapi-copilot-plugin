package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
)

type tokenExchangeHost struct {
	entered   chan string
	blocking  string
	release   chan struct{}
	emptyAPI  bool
	expiresAt int64
	mu        sync.Mutex
	count     int
}

func (h *tokenExchangeHost) Do(ctx context.Context, _ string, request transport.Request) (transport.Response, error) {
	h.mu.Lock()
	h.count++
	emptyAPI := h.emptyAPI
	h.mu.Unlock()
	credential := strings.TrimPrefix(request.Headers.Get("Authorization"), "token ")
	if h.entered != nil {
		h.entered <- credential
	}
	if credential == h.blocking && h.release != nil {
		select {
		case <-ctx.Done():
			return transport.Response{}, ctx.Err()
		case <-h.release:
		}
	}
	apiURL := ""
	if !emptyAPI && credential == "github-a" {
		apiURL = "https://api-a.example"
	}
	if !emptyAPI && credential == "github-b" {
		apiURL = "https://api-b.example"
	}
	expiresAt := h.expiresAt
	if expiresAt == 0 {
		expiresAt = time.Now().Add(time.Hour).Unix()
	}
	response, err := json.Marshal(map[string]any{
		"token":      "copilot-" + credential,
		"expires_at": expiresAt,
		"endpoints":  map[string]string{"api": apiURL},
	})
	if err != nil {
		return transport.Response{}, err
	}
	return transport.Response{StatusCode: http.StatusOK, Body: response}, nil
}

func (h *tokenExchangeHost) OpenStream(context.Context, string, transport.Request) (transport.Stream, error) {
	return transport.Stream{}, fmt.Errorf("unexpected stream")
}

func (h *tokenExchangeHost) ReadStream(context.Context, string) (transport.StreamChunk, error) {
	return transport.StreamChunk{}, fmt.Errorf("unexpected stream")
}

func (h *tokenExchangeHost) CloseStream(context.Context, string) error { return nil }

func (h *tokenExchangeHost) Emit(context.Context, string, []byte) error { return nil }

func (h *tokenExchangeHost) CloseOutput(context.Context, string, string) {}

func TestCopilotTokenCacheRefreshesAtExpiryBufferBoundary(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	host := &tokenExchangeHost{expiresAt: now.Add(5 * time.Minute).Unix()}
	service := New(host)
	service.now = func() time.Time { return now }
	storage := authStorage{GitHubAccessToken: "github-a"}
	if _, err := service.copilotToken(context.Background(), "callback", "auth", storage); err != nil {
		t.Fatalf("first token exchange: %v", err)
	}
	if _, err := service.copilotToken(context.Background(), "callback", "auth", storage); err != nil {
		t.Fatalf("token exchange at expiry buffer boundary: %v", err)
	}
	if host.count != 2 {
		t.Fatalf("token exchange count at exact five-minute buffer = %d, want 2", host.count)
	}
}

func TestTokenExpiryPrecedence(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	for _, test := range []struct {
		name  string
		token copilotTokenResponse
		want  time.Time
	}{
		{name: "explicit expiry first", token: copilotTokenResponse{ExpiresAt: now.Add(15 * time.Minute).Unix(), Token: "opaque;exp=1700000300", RefreshIn: 30}, want: now.Add(15 * time.Minute)},
		{name: "embedded expiry second", token: copilotTokenResponse{Token: "opaque;exp=1700000600", RefreshIn: 30}, want: now.Add(10 * time.Minute)},
		{name: "refresh interval third", token: copilotTokenResponse{Token: "opaque", RefreshIn: 90}, want: now.Add(90 * time.Second)},
		{name: "twenty minute fallback", token: copilotTokenResponse{Token: "opaque"}, want: now.Add(20 * time.Minute)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := tokenExpiry(test.token, now); !got.Equal(test.want) {
				t.Fatalf("tokenExpiry() = %s, want %s", got, test.want)
			}
		})
	}
}

func TestCopilotTokenFlightsAreCredentialScoped(t *testing.T) {
	host := &tokenExchangeHost{entered: make(chan string, 3), blocking: "github-a", release: make(chan struct{})}
	service := New(host)
	results := make(chan struct {
		entry copilotTokenEntry
		err   error
	}, 2)
	go func() {
		entry, err := service.copilotToken(context.Background(), "callback", "same-auth", authStorage{GitHubAccessToken: "github-a"})
		results <- struct {
			entry copilotTokenEntry
			err   error
		}{entry, err}
	}()
	if credential := <-host.entered; credential != "github-a" {
		t.Fatalf("first exchange credential = %q", credential)
	}
	go func() {
		entry, err := service.copilotToken(context.Background(), "callback", "same-auth", authStorage{GitHubAccessToken: "github-b"})
		results <- struct {
			entry copilotTokenEntry
			err   error
		}{entry, err}
	}()
	if credential := <-host.entered; credential != "github-b" {
		t.Fatalf("second exchange credential = %q", credential)
	}
	resultB := <-results
	if resultB.err != nil || resultB.entry.Fingerprint != tokenFingerprint("github-b") || resultB.entry.APIBaseURL != "https://api-b.example" {
		t.Fatalf("new credential received wrong token entry: entry=%#v err=%v", resultB.entry, resultB.err)
	}
	close(host.release)
	resultA := <-results
	if resultA.err != nil || resultA.entry.Fingerprint != tokenFingerprint("github-a") || resultA.entry.APIBaseURL != "https://api-a.example" {
		t.Fatalf("old credential received wrong token entry: entry=%#v err=%v", resultA.entry, resultA.err)
	}
	entryB, errB := service.copilotToken(context.Background(), "callback", "same-auth", authStorage{GitHubAccessToken: "github-b"})
	if errB != nil || entryB.Fingerprint != tokenFingerprint("github-b") || entryB.APIBaseURL != "https://api-b.example" {
		t.Fatalf("cached token crossed credentials: entry=%#v err=%v", entryB, errB)
	}
}

func TestConfigureInvalidatesCachedAndInFlightTokens(t *testing.T) {
	host := &tokenExchangeHost{entered: make(chan string, 2), blocking: "github-a", release: make(chan struct{})}
	service := New(host)
	result := make(chan struct {
		entry copilotTokenEntry
		err   error
	}, 1)
	go func() {
		entry, err := service.copilotToken(context.Background(), "callback", "auth", authStorage{GitHubAccessToken: "github-a"})
		result <- struct {
			entry copilotTokenEntry
			err   error
		}{entry, err}
	}()
	<-host.entered
	if err := service.Configure([]byte("copilot_api_url: https://new-api.example\n")); err != nil {
		t.Fatalf("configure: %v", err)
	}
	host.mu.Lock()
	host.emptyAPI = true
	host.mu.Unlock()
	close(host.release)
	old := <-result
	if old.err == nil || !strings.Contains(old.err.Error(), "configuration changed") {
		t.Fatalf("old in-flight exchange error = %v", old.err)
	}
	entry, err := service.copilotToken(context.Background(), "callback", "auth", authStorage{GitHubAccessToken: "github-a"})
	if err != nil {
		t.Fatalf("new configuration token exchange: %v", err)
	}
	if entry.APIBaseURL != "https://new-api.example" || entry.ConfigGeneration != 1 {
		t.Fatalf("token entry after reconfigure = %#v", entry)
	}
	if host.count != 2 {
		t.Fatalf("token exchange count = %d, want 2", host.count)
	}
}
