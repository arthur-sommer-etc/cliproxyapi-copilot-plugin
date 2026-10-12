package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
)

func TestPruneTokenCacheRemovesExpiredEntriesAndBoundsChurn(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cfg := DefaultConfig()
	entries := map[string]copilotTokenEntry{
		"expired": {Token: "expired-secret", ExpiresAt: now.Add(-time.Second)},
		"valid":   {Token: "valid-secret", ExpiresAt: now.Add(time.Hour)},
	}
	pruneTokenCache(entries, cfg, now)
	if _, ok := entries["expired"]; ok {
		t.Fatal("expired token remained in cache")
	}
	if entry, ok := entries["valid"]; !ok || entry.Token != "valid-secret" {
		t.Fatalf("valid token cache entry was not preserved: %#v", entry)
	}
	for i := 0; i < maxAccountCacheEntries+20; i++ {
		key := fmt.Sprintf("account-%d", i)
		entries[key] = copilotTokenEntry{Token: "secret-" + key, ExpiresAt: now.Add(time.Duration(i+1) * time.Hour)}
		pruneTokenCache(entries, cfg, now)
	}
	if len(entries) > maxAccountCacheEntries {
		t.Fatalf("token cache grew to %d entries, limit is %d", len(entries), maxAccountCacheEntries)
	}
}

func TestPruneModelCacheRemovesExpiredEntriesAndBoundsChurn(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	entries := map[string]modelCacheEntry{
		"expired": {ExpiresAt: now, Models: []upstreamModel{{ID: "stale-model"}}},
		"valid":   {ExpiresAt: now.Add(time.Hour), Models: []upstreamModel{{ID: "valid-model"}}},
	}
	pruneModelCache(entries, now)
	if _, ok := entries["expired"]; ok {
		t.Fatal("expired model catalogue remained in cache")
	}
	if entry, ok := entries["valid"]; !ok || len(entry.Models) != 1 || entry.Models[0].ID != "valid-model" {
		t.Fatalf("valid model catalogue was not preserved: %#v", entry)
	}
	for i := 0; i < maxAccountCacheEntries+20; i++ {
		key := fmt.Sprintf("account-%d", i)
		entries[key] = modelCacheEntry{ExpiresAt: now.Add(time.Duration(i+1) * time.Hour), Models: []upstreamModel{{ID: key}}}
		pruneModelCache(entries, now)
	}
	if len(entries) > maxAccountCacheEntries {
		t.Fatalf("model cache grew to %d entries, limit is %d", len(entries), maxAccountCacheEntries)
	}
}

func TestCopilotTokenCacheStaysBoundedDuringAccountChurn(t *testing.T) {
	host := &cacheTokenHost{}
	service := New(host)
	service.tokenEntries["expired-account"] = copilotTokenEntry{Token: "expired-secret", ExpiresAt: time.Now().Add(-time.Second)}
	for i := 0; i < maxAccountCacheEntries+20; i++ {
		credential := fmt.Sprintf("synthetic-credential-%d", i)
		if _, err := service.copilotToken(t.Context(), "callback", credential, authStorage{GitHubAccessToken: credential}); err != nil {
			t.Fatalf("exchange token for account %d: %v", i, err)
		}
	}
	service.tokenMu.Lock()
	count := len(service.tokenEntries)
	_, expiredRetained := service.tokenEntries["expired-account"]
	service.tokenMu.Unlock()
	if expiredRetained {
		t.Fatal("expired account token remained after cache activity")
	}
	if count != maxAccountCacheEntries {
		t.Fatalf("token cache retained %d entries after churn, want %d", count, maxAccountCacheEntries)
	}
}

func TestModelCacheStaysBoundedDuringAccountChurn(t *testing.T) {
	host := &cacheTokenHost{}
	service := New(host)
	service.modelEntries["expired-account"] = modelCacheEntry{ExpiresAt: time.Now().Add(-time.Second), Models: []upstreamModel{{ID: "stale-model"}}}
	for i := 0; i < maxAccountCacheEntries+20; i++ {
		credential := fmt.Sprintf("synthetic-credential-%d", i)
		if _, _, err := service.models(t.Context(), "callback", credential, authStorage{GitHubAccessToken: credential}, false); err != nil {
			t.Fatalf("discover models for account %d: %v", i, err)
		}
	}
	service.modelMu.Lock()
	count := len(service.modelEntries)
	_, expiredRetained := service.modelEntries["expired-account"]
	service.modelMu.Unlock()
	if expiredRetained {
		t.Fatal("expired account catalogue remained after cache activity")
	}
	if count != maxAccountCacheEntries {
		t.Fatalf("model cache retained %d entries after churn, want %d", count, maxAccountCacheEntries)
	}
}

func TestCopilotTokenCacheKeepsInFlightRequestsShared(t *testing.T) {
	host := &cacheTokenHost{entered: make(chan struct{}, 1), release: make(chan struct{})}
	service := New(host)
	const callers = 16
	results := make(chan error, callers)
	var ready sync.WaitGroup
	ready.Add(callers)
	start := make(chan struct{})
	for range callers {
		go func() {
			ready.Done()
			<-start
			_, err := service.copilotToken(t.Context(), "callback", "shared-account", authStorage{GitHubAccessToken: "synthetic-credential"})
			results <- err
		}()
	}
	ready.Wait()
	close(start)
	<-host.entered
	close(host.release)
	for range callers {
		if err := <-results; err != nil {
			t.Fatalf("shared token exchange: %v", err)
		}
	}
	host.mu.Lock()
	requests := host.requests
	host.mu.Unlock()
	if requests != 1 {
		t.Fatalf("token endpoint received %d requests, want 1", requests)
	}
}

type cacheTokenHost struct {
	mu       sync.Mutex
	requests int
	entered  chan struct{}
	release  chan struct{}
}

func (h *cacheTokenHost) Do(ctx context.Context, _ string, request transport.Request) (transport.Response, error) {
	h.mu.Lock()
	h.requests++
	entered, release := h.entered, h.release
	h.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return transport.Response{}, ctx.Err()
		}
	}
	if strings.HasSuffix(request.URL, "/models") {
		return transport.Response{StatusCode: http.StatusOK, Body: []byte(`{"data":[{"id":"synthetic-model","supported_endpoints":["/chat/completions"]}]}`)}, nil
	}
	expiresAt := time.Now().Add(time.Hour).Unix()
	return transport.Response{
		StatusCode: http.StatusOK,
		Body:       []byte(fmt.Sprintf(`{"token":"synthetic-copilot-token","expires_at":%d,"endpoints":{"api":"https://api.githubcopilot.com"}}`, expiresAt)),
	}, nil
}

func (*cacheTokenHost) OpenStream(context.Context, string, transport.Request) (transport.Stream, error) {
	return transport.Stream{}, fmt.Errorf("unexpected stream request")
}

func (*cacheTokenHost) ReadStream(context.Context, string) (transport.StreamChunk, error) {
	return transport.StreamChunk{}, fmt.Errorf("unexpected stream read")
}

func (*cacheTokenHost) CloseStream(context.Context, string) error {
	return nil
}

func (*cacheTokenHost) Emit(context.Context, string, []byte) error {
	return nil
}

func (*cacheTokenHost) CloseOutput(context.Context, string, string) {}
