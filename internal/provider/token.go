package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/redact"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
)

type copilotTokenResponse struct {
	Token      string            `json:"token"`
	ExpiresAt  int64             `json:"expires_at"`
	RefreshIn  int64             `json:"refresh_in"`
	Endpoints  map[string]string `json:"endpoints"`
	TokenError string            `json:"error"`
}

type copilotUserEndpointsResponse struct {
	Endpoints map[string]string `json:"endpoints"`
}

type copilotTokenEntry struct {
	Token            string
	APIBaseURL       string
	Mode             string
	ExpiresAt        time.Time
	ContextExpiresAt time.Time
	Fingerprint      string
	ConfigGeneration uint64
}

type tokenFlight struct {
	done        chan struct{}
	entry       copilotTokenEntry
	err         error
	fingerprint string
	generation  uint64
}

func (s *Service) copilotToken(ctx context.Context, callbackID, authID string, storage authStorage) (copilotTokenEntry, error) {
	fingerprint := tokenFingerprint(storage.GitHubAccessToken)
	key := strings.TrimSpace(authID)
	if key == "" {
		key = fingerprint
	}
	cfg, generation := s.configSnapshot()
	now := s.now()
	s.tokenMu.Lock()
	if cached, ok := s.tokenEntries[key]; ok && cached.Fingerprint == fingerprint && effectiveAuthMode(cached.Mode) == authModeDirectOAuth && cfg.AuthMode == authModeDirectOAuth {
		cached.ExpiresAt = storageCredentialExpiry(storage)
		s.tokenEntries[key] = cached
	}
	pruneTokenCache(s.tokenEntries, cfg, now)
	if cached, ok := s.tokenEntries[key]; ok && cached.Fingerprint == fingerprint && cached.ConfigGeneration == generation && effectiveAuthMode(cached.Mode) == cfg.AuthMode {
		if copilotTokenEntryValid(cached, cfg, now) {
			s.tokenEntries[key] = cached
			s.tokenMu.Unlock()
			return cached, nil
		}
	}
	flightKey := tokenFlightKey(key, fingerprint, generation)
	if flight := s.tokenInflight[flightKey]; flight != nil {
		done := flight.done
		s.tokenMu.Unlock()
		select {
		case <-ctx.Done():
			return copilotTokenEntry{}, ctx.Err()
		case <-done:
			_, currentGeneration := s.configSnapshot()
			if currentGeneration != generation {
				return copilotTokenEntry{}, fmt.Errorf("Copilot configuration changed during token exchange")
			}
			if flight.err != nil {
				return flight.entry, flight.err
			}
			if flight.fingerprint != fingerprint || flight.entry.Fingerprint != fingerprint || effectiveAuthMode(flight.entry.Mode) != cfg.AuthMode {
				return copilotTokenEntry{}, fmt.Errorf("Copilot token credential changed during token exchange")
			}
			entry := flight.entry
			if cfg.AuthMode == authModeDirectOAuth {
				entry.ExpiresAt = storageCredentialExpiry(storage)
				if !directOAuthEntryUsable(entry, s.now()) {
					return copilotTokenEntry{}, statusError("auth_state_refresh_required", "Refresh Copilot authentication before making this request", http.StatusConflict)
				}
			}
			return entry, nil
		}
	}
	if cfg.AuthMode == authModeDirectOAuth && !cachedCredentialExpiryValid(storage, now) {
		s.tokenMu.Unlock()
		return copilotTokenEntry{}, statusError("auth_state_refresh_required", "Refresh Copilot authentication before making this request", http.StatusConflict)
	}
	flight := &tokenFlight{done: make(chan struct{}), fingerprint: fingerprint, generation: generation}
	s.tokenInflight[flightKey] = flight
	s.tokenMu.Unlock()

	var entry copilotTokenEntry
	var errExchange error
	if cfg.AuthMode == authModeDirectOAuth {
		entry, errExchange = s.discoverCopilotAPI(ctx, callbackID, fingerprint, storage, cfg)
	} else {
		entry, errExchange = s.exchangeCopilotToken(ctx, callbackID, fingerprint, storage.GitHubAccessToken, cfg)
	}
	entry.ConfigGeneration = generation
	if errExchange == nil && cfg.AuthMode == authModeDirectOAuth {
		entry.ExpiresAt = storageCredentialExpiry(storage)
		if !directOAuthEntryUsable(entry, s.now()) {
			errExchange = statusError("auth_state_refresh_required", "Refresh Copilot authentication before making this request", http.StatusConflict)
		}
	}

	s.configMu.RLock()
	if s.configGeneration != generation {
		errExchange = fmt.Errorf("Copilot configuration changed during token exchange")
	}
	s.tokenMu.Lock()
	flight.entry = entry
	flight.err = errExchange
	if errExchange == nil && entry.Fingerprint == fingerprint {
		s.tokenEntries[key] = entry
		pruneTokenCache(s.tokenEntries, cfg, s.now())
	}
	delete(s.tokenInflight, flightKey)
	close(flight.done)
	s.tokenMu.Unlock()
	s.configMu.RUnlock()
	return entry, errExchange
}

func effectiveAuthMode(mode string) string {
	if mode == "" {
		return authModeTokenExchange
	}
	return mode
}

func copilotTokenEntryValid(entry copilotTokenEntry, cfg Config, now time.Time) bool {
	if effectiveAuthMode(entry.Mode) == authModeDirectOAuth {
		return entry.ContextExpiresAt.After(now) && (entry.ExpiresAt.IsZero() || entry.ExpiresAt.After(now.Add(cfg.tokenExpiryBuffer())))
	}
	return entry.ExpiresAt.After(now.Add(cfg.tokenExpiryBuffer()))
}

func storageCredentialExpiry(storage authStorage) time.Time {
	if storage.ExpiresAt <= 0 {
		return time.Time{}
	}
	return time.Unix(storage.ExpiresAt, 0)
}

func cachedCredentialExpiryValid(storage authStorage, now time.Time) bool {
	expiresAt := storageCredentialExpiry(storage)
	return expiresAt.IsZero() || expiresAt.After(now)
}

func directOAuthEntryUsable(entry copilotTokenEntry, now time.Time) bool {
	return entry.ContextExpiresAt.After(now) && (entry.ExpiresAt.IsZero() || entry.ExpiresAt.After(now))
}

func tokenFlightKey(authID, fingerprint string, generation uint64) string {
	return strings.Join([]string{authID, fingerprint, strconv.FormatUint(generation, 10)}, "\x00")
}

func (s *Service) exchangeCopilotToken(ctx context.Context, callbackID, fingerprint, githubToken string, cfg Config) (copilotTokenEntry, error) {
	resp, errDo := s.host.Do(ctx, callbackID, transport.Request{
		Method: http.MethodGet,
		URL:    cfg.GitHubAPIURL + "/copilot_internal/v2/token",
		Headers: http.Header{
			"Accept":               []string{"application/vnd.github+json"},
			"Authorization":        []string{"token " + githubToken},
			"User-Agent":           []string{userAgent()},
			"X-GitHub-Api-Version": []string{"2025-04-01"},
		},
	})
	if errDo != nil {
		return copilotTokenEntry{}, fmt.Errorf("exchange GitHub OAuth token for Copilot token: %w", errDo)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return copilotTokenEntry{}, upstreamStatusError(resp.StatusCode, redact.ErrorBody(resp.Body, githubToken))
	}
	var token copilotTokenResponse
	if errUnmarshal := json.Unmarshal(resp.Body, &token); errUnmarshal != nil {
		return copilotTokenEntry{}, fmt.Errorf("decode Copilot token response: %w", errUnmarshal)
	}
	token.Token = strings.TrimSpace(token.Token)
	if token.Token == "" {
		return copilotTokenEntry{}, fmt.Errorf("Copilot token response has no token")
	}
	expiresAt := tokenExpiry(token, s.now())
	apiBase, errBase := copilotAPIBase(token.Endpoints, cfg)
	if errBase != nil {
		return copilotTokenEntry{}, errBase
	}
	return copilotTokenEntry{
		Token:       token.Token,
		APIBaseURL:  apiBase,
		Mode:        authModeTokenExchange,
		ExpiresAt:   expiresAt,
		Fingerprint: fingerprint,
	}, nil
}

func (s *Service) discoverCopilotAPI(ctx context.Context, callbackID, fingerprint string, storage authStorage, cfg Config) (copilotTokenEntry, error) {
	headers := http.Header{}
	headers.Set("Accept", "application/json")
	headers.Set("Authorization", "Bearer "+storage.GitHubAccessToken)
	headers.Set("User-Agent", userAgent())
	headers.Set("X-GitHub-Api-Version", copilotAPIVersion)
	resp, errDo := s.host.Do(ctx, callbackID, transport.Request{
		Method:  http.MethodGet,
		URL:     cfg.GitHubAPIURL + "/copilot_internal/user",
		Headers: headers,
	})
	if errDo != nil {
		return copilotTokenEntry{}, fmt.Errorf("discover Copilot API from GitHub account: %w", errDo)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return copilotTokenEntry{}, upstreamStatusError(resp.StatusCode, redact.ErrorBody(resp.Body, storage.GitHubAccessToken))
	}
	var account copilotUserEndpointsResponse
	if errUnmarshal := json.Unmarshal(resp.Body, &account); errUnmarshal != nil {
		return copilotTokenEntry{}, fmt.Errorf("decode GitHub Copilot account response: %w", errUnmarshal)
	}
	apiBase, errBase := copilotAPIBaseFromDirectOAuth(account.Endpoints, cfg)
	if errBase != nil {
		return copilotTokenEntry{}, errBase
	}
	return copilotTokenEntry{
		Token:            storage.GitHubAccessToken,
		APIBaseURL:       apiBase,
		Mode:             authModeDirectOAuth,
		ExpiresAt:        storageCredentialExpiry(storage),
		ContextExpiresAt: s.now().Add(cfg.modelCacheTTL()),
		Fingerprint:      fingerprint,
	}, nil
}

func tokenExpiry(token copilotTokenResponse, now time.Time) time.Time {
	if token.ExpiresAt > 0 {
		return time.Unix(token.ExpiresAt, 0)
	}
	for _, part := range strings.Split(token.Token, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || key != "exp" {
			continue
		}
		seconds, errParse := strconv.ParseInt(value, 10, 64)
		if errParse == nil && seconds > 0 {
			return time.Unix(seconds, 0)
		}
	}
	if token.RefreshIn > 0 {
		return now.Add(time.Duration(token.RefreshIn) * time.Second)
	}
	return now.Add(20 * time.Minute)
}

func copilotAPIBase(endpoints map[string]string, cfg Config) (string, error) {
	raw := strings.TrimRight(strings.TrimSpace(endpoints["api"]), "/")
	if raw == "" {
		raw = cfg.CopilotAPIURL
	}
	return validateCopilotAPIBase(raw, cfg, "Copilot token")
}

func copilotAPIBaseFromDirectOAuth(endpoints map[string]string, cfg Config) (string, error) {
	raw := strings.TrimRight(strings.TrimSpace(endpoints["api"]), "/")
	if raw == "" {
		return "", fmt.Errorf("GitHub Copilot account response has no API endpoint")
	}
	return validateCopilotAPIBase(raw, cfg, "GitHub Copilot account response")
}

func validateCopilotAPIBase(raw string, cfg Config, source string) (string, error) {
	parsed, errParse := url.Parse(raw)
	if errParse != nil || parsed.Hostname() == "" || parsed.User != nil {
		return "", fmt.Errorf("%s returned an invalid API endpoint", source)
	}
	if parsed.Scheme != "https" && !(cfg.AllowInsecureBaseURLs && parsed.Scheme == "http") {
		return "", fmt.Errorf("Copilot API endpoint must use HTTPS")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("Copilot API endpoint contains query or fragment")
	}
	return raw, nil
}

func (s *Service) invalidateAuthForCredential(authID, fingerprint string) {
	key := strings.TrimSpace(authID)
	if key == "" {
		key = fingerprint
	}
	s.tokenMu.Lock()
	if entry, ok := s.tokenEntries[key]; ok && entry.Fingerprint == fingerprint {
		clearTokenCacheEntry(s.tokenEntries, key)
	}
	s.tokenMu.Unlock()
	s.modelMu.Lock()
	if entry, ok := s.modelEntries[key]; ok && entry.Fingerprint == fingerprint {
		clearModelCacheEntry(s.modelEntries, key)
	}
	s.modelMu.Unlock()
}

func (s *Service) invalidateAuth(authID string) {
	authID = strings.TrimSpace(authID)
	s.tokenMu.Lock()
	if authID != "" {
		clearTokenCacheEntry(s.tokenEntries, authID)
	}
	s.tokenMu.Unlock()
	s.modelMu.Lock()
	if authID != "" {
		clearModelCacheEntry(s.modelEntries, authID)
	}
	s.modelMu.Unlock()
}
