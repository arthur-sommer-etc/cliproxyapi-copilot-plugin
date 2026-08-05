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

type copilotTokenEntry struct {
	Token       string
	APIBaseURL  string
	ExpiresAt   time.Time
	Fingerprint string
}

type tokenFlight struct {
	done  chan struct{}
	entry copilotTokenEntry
	err   error
}

func (s *Service) copilotToken(ctx context.Context, callbackID, authID string, storage authStorage) (copilotTokenEntry, error) {
	fingerprint := tokenFingerprint(storage.GitHubAccessToken)
	key := strings.TrimSpace(authID)
	if key == "" {
		key = fingerprint
	}
	cfg := s.Config()
	now := s.now()

	s.tokenMu.Lock()
	if cached, ok := s.tokenEntries[key]; ok &&
		cached.Fingerprint == fingerprint &&
		cached.ExpiresAt.After(now.Add(cfg.tokenExpiryBuffer())) {
		s.tokenMu.Unlock()
		return cached, nil
	}
	if flight := s.tokenInflight[key]; flight != nil {
		done := flight.done
		s.tokenMu.Unlock()
		select {
		case <-ctx.Done():
			return copilotTokenEntry{}, ctx.Err()
		case <-done:
			return flight.entry, flight.err
		}
	}
	flight := &tokenFlight{done: make(chan struct{})}
	s.tokenInflight[key] = flight
	s.tokenMu.Unlock()

	entry, errExchange := s.exchangeCopilotToken(ctx, callbackID, fingerprint, storage.GitHubAccessToken)

	s.tokenMu.Lock()
	flight.entry = entry
	flight.err = errExchange
	if errExchange == nil {
		s.tokenEntries[key] = entry
	}
	delete(s.tokenInflight, key)
	close(flight.done)
	s.tokenMu.Unlock()
	return entry, errExchange
}

func (s *Service) exchangeCopilotToken(ctx context.Context, callbackID, fingerprint, githubToken string) (copilotTokenEntry, error) {
	cfg := s.Config()
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
		ExpiresAt:   expiresAt,
		Fingerprint: fingerprint,
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
	parsed, errParse := url.Parse(raw)
	if errParse != nil || parsed.Hostname() == "" {
		return "", fmt.Errorf("Copilot token returned an invalid API endpoint")
	}
	if parsed.Scheme != "https" && !(cfg.AllowInsecureBaseURLs && parsed.Scheme == "http") {
		return "", fmt.Errorf("Copilot API endpoint must use HTTPS")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("Copilot API endpoint contains query or fragment")
	}
	return raw, nil
}

func (s *Service) invalidateAuth(authID string) {
	authID = strings.TrimSpace(authID)
	s.tokenMu.Lock()
	if authID != "" {
		delete(s.tokenEntries, authID)
	}
	s.tokenMu.Unlock()
	s.modelMu.Lock()
	if authID != "" {
		delete(s.modelEntries, authID)
	}
	s.modelMu.Unlock()
}
