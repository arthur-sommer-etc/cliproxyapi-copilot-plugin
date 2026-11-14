package provider

import "time"

const maxAccountCacheEntries = 128

func clearTokenCacheEntry(entries map[string]copilotTokenEntry, key string) {
	entries[key] = copilotTokenEntry{}
	delete(entries, key)
}

func pruneTokenCache(entries map[string]copilotTokenEntry, cfg Config, now time.Time) {
	for key, entry := range entries {
		if !copilotTokenEntryValid(entry, cfg, now) {
			clearTokenCacheEntry(entries, key)
		}
	}
	for len(entries) > maxAccountCacheEntries {
		var oldestKey string
		var oldestExpiry time.Time
		first := true
		for key, entry := range entries {
			expiresAt := tokenCacheExpiry(entry)
			if first || expiresAt.Before(oldestExpiry) {
				oldestKey = key
				oldestExpiry = expiresAt
				first = false
			}
		}
		clearTokenCacheEntry(entries, oldestKey)
	}
}

func tokenCacheExpiry(entry copilotTokenEntry) time.Time {
	expiresAt := entry.ExpiresAt
	if expiresAt.IsZero() || (!entry.ContextExpiresAt.IsZero() && entry.ContextExpiresAt.Before(expiresAt)) {
		return entry.ContextExpiresAt
	}
	return expiresAt
}

func clearModelCacheEntry(entries map[string]modelCacheEntry, key string) {
	entries[key] = modelCacheEntry{}
	delete(entries, key)
}

func pruneModelCache(entries map[string]modelCacheEntry, now time.Time) {
	for key, entry := range entries {
		if !entry.ExpiresAt.After(now) {
			clearModelCacheEntry(entries, key)
		}
	}
	for len(entries) > maxAccountCacheEntries {
		var oldestKey string
		var oldestExpiry time.Time
		first := true
		for key, entry := range entries {
			if first || entry.ExpiresAt.Before(oldestExpiry) {
				oldestKey = key
				oldestExpiry = entry.ExpiresAt
				first = false
			}
		}
		clearModelCacheEntry(entries, oldestKey)
	}
}

func clearTokenCache(entries map[string]copilotTokenEntry) {
	for key := range entries {
		clearTokenCacheEntry(entries, key)
	}
}

func clearModelCache(entries map[string]modelCacheEntry) {
	for key := range entries {
		clearModelCacheEntry(entries, key)
	}
}
