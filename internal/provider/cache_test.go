package provider

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestForcedModelRefreshPrunesExpiredAccountsOnFailure(t *testing.T) {
	service, storage, _ := serviceWithCachedModels(t, []upstreamModel{{ID: "valid-model"}})
	host := newDirectOAuthTestHost()
	host.catalogStatus = http.StatusServiceUnavailable
	service.host = host
	service.modelEntries["expired-account"] = modelCacheEntry{
		ExpiresAt: service.now().Add(-time.Second),
		Models:    []upstreamModel{{ID: "stale-model"}},
	}
	if _, _, err := service.models(t.Context(), "synthetic-callback", "auth", storage, true); err == nil {
		t.Fatal("forced model discovery unexpectedly succeeded")
	}
	if _, retained := service.modelEntries["expired-account"]; retained {
		t.Fatal("failed forced refresh retained an expired account catalogue")
	}
	if _, retained := service.modelEntries["auth"]; !retained {
		t.Fatal("failed forced refresh removed a still-valid account catalogue")
	}
}

func TestPruneTokenCacheRemovesExpiredEntriesAndBoundsChurn(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cfg := DefaultConfig()
	entries := map[string]copilotTokenEntry{
		"expired": {Token: "expired-secret", ExpiresAt: now.Add(-time.Second), Fingerprint: "expired"},
		"valid":   {Token: "valid-secret", ExpiresAt: now.Add(time.Hour), Fingerprint: "valid"},
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
		entries[key] = copilotTokenEntry{
			Token:       "secret-" + key,
			ExpiresAt:   now.Add(time.Duration(i+1) * time.Hour),
			Fingerprint: key,
		}
		pruneTokenCache(entries, cfg, now)
	}
	if len(entries) > maxAccountCacheEntries {
		t.Fatalf("token cache grew to %d entries, limit is %d", len(entries), maxAccountCacheEntries)
	}
}

func TestPruneModelCacheRemovesExpiredEntriesAndBoundsChurn(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	entries := map[string]modelCacheEntry{
		"expired": {Fingerprint: "expired", ExpiresAt: now, Models: []upstreamModel{{ID: "stale-model"}}},
		"valid":   {Fingerprint: "valid", ExpiresAt: now.Add(time.Hour), Models: []upstreamModel{{ID: "valid-model"}}},
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
		entries[key] = modelCacheEntry{
			Fingerprint: key,
			ExpiresAt:   now.Add(time.Duration(i+1) * time.Hour),
			Models:      []upstreamModel{{ID: key}},
		}
		pruneModelCache(entries, now)
	}
	if len(entries) > maxAccountCacheEntries {
		t.Fatalf("model cache grew to %d entries, limit is %d", len(entries), maxAccountCacheEntries)
	}
}

func TestPruneTokenCachePreservesDirectOAuthDeadlines(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cfg := DefaultConfig()
	cfg.AuthMode = authModeDirectOAuth
	entries := map[string]copilotTokenEntry{
		"context-expired": {
			Token:            "expired-credential",
			Mode:             authModeDirectOAuth,
			ExpiresAt:        now.Add(time.Hour),
			ContextExpiresAt: now,
		},
		"credential-expired": {
			Token:            "expired-credential",
			Mode:             authModeDirectOAuth,
			ExpiresAt:        now,
			ContextExpiresAt: now.Add(time.Hour),
		},
		"valid": {
			Token:            "valid-credential",
			Mode:             authModeDirectOAuth,
			ExpiresAt:        now.Add(time.Hour),
			ContextExpiresAt: now.Add(time.Minute),
		},
	}
	pruneTokenCache(entries, cfg, now)
	if _, ok := entries["context-expired"]; ok {
		t.Fatal("expired direct-OAuth context remained in cache")
	}
	if _, ok := entries["credential-expired"]; ok {
		t.Fatal("expired direct-OAuth credential remained in cache")
	}
	if _, ok := entries["valid"]; !ok {
		t.Fatal("valid direct-OAuth entry was pruned")
	}
}

func TestCopilotTokenCacheStaysBoundedDuringAccountChurn(t *testing.T) {
	host := &tokenExchangeHost{}
	service := New(host)
	service.tokenEntries["expired-account"] = copilotTokenEntry{
		Token:       "expired-secret",
		ExpiresAt:   time.Now().Add(-time.Second),
		Fingerprint: "expired-account",
	}
	for i := 0; i < maxAccountCacheEntries+20; i++ {
		credential := fmt.Sprintf("github-%d", i)
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
