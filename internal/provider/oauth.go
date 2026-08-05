package provider

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/redact"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

type deviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type oauthTokenResponse struct {
	AccessToken           string `json:"access_token"`
	RefreshToken          string `json:"refresh_token"`
	TokenType             string `json:"token_type"`
	Scope                 string `json:"scope"`
	ExpiresIn             int64  `json:"expires_in"`
	RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
	Error                 string `json:"error"`
	ErrorDescription      string `json:"error_description"`
	Interval              int    `json:"interval"`
}

type githubUser struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
}

type deviceSession struct {
	DeviceCode string
	ClientID   string
	UserCode   string
	ExpiresAt  time.Time
	Interval   time.Duration
	NextPoll   time.Time
	Polling    bool
}

type pollDecision struct {
	Status       pluginapi.AuthLoginStatus
	Message      string
	NextInterval time.Duration
	Token        *oauthTokenResponse
	Terminal     bool
}

func (s *Service) ParseAuth(req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
	if req.Provider != "" && !strings.EqualFold(req.Provider, providerID) {
		return pluginapi.AuthParseResponse{Handled: false}, nil
	}
	storage, errParse := parseStorage(req.RawJSON)
	if errParse != nil {
		var rawType struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(req.RawJSON, &rawType) != nil || !strings.EqualFold(strings.TrimSpace(rawType.Type), providerID) {
			return pluginapi.AuthParseResponse{Handled: false}, nil
		}
		return pluginapi.AuthParseResponse{}, errParse
	}
	data, errData := authData(storage, req.FileName, req.FileName, "", "", false, nil, nil)
	if errData != nil {
		return pluginapi.AuthParseResponse{}, errData
	}
	return pluginapi.AuthParseResponse{Handled: true, Auth: data}, nil
}

func (s *Service) StartLogin(ctx context.Context, callbackID string) (pluginapi.AuthLoginStartResponse, error) {
	cfg := s.Config()
	if !cfg.Enabled {
		return pluginapi.AuthLoginStartResponse{}, statusError("plugin_disabled", "Copilot plugin is disabled", http.StatusServiceUnavailable)
	}
	form := url.Values{"client_id": {cfg.GitHubClientID}}
	if cfg.GitHubScope != "" {
		form.Set("scope", cfg.GitHubScope)
	}
	resp, errDo := s.host.Do(ctx, callbackID, transport.Request{
		Method: http.MethodPost,
		URL:    cfg.GitHubBaseURL + "/login/device/code",
		Headers: http.Header{
			"Accept":       []string{"application/json"},
			"Content-Type": []string{"application/x-www-form-urlencoded"},
			"User-Agent":   []string{userAgent()},
		},
		Body: []byte(form.Encode()),
	})
	if errDo != nil {
		return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("start GitHub device flow: %w", errDo)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return pluginapi.AuthLoginStartResponse{}, upstreamStatusError(resp.StatusCode, redact.ErrorBody(resp.Body))
	}
	var device deviceCodeResponse
	if errUnmarshal := json.Unmarshal(resp.Body, &device); errUnmarshal != nil {
		return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("decode GitHub device flow response: %w", errUnmarshal)
	}
	device.DeviceCode = strings.TrimSpace(device.DeviceCode)
	device.UserCode = strings.TrimSpace(device.UserCode)
	device.VerificationURI = strings.TrimSpace(device.VerificationURI)
	device.VerificationURIComplete = strings.TrimSpace(device.VerificationURIComplete)
	if device.DeviceCode == "" || device.UserCode == "" || device.VerificationURI == "" {
		return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("GitHub device flow response is incomplete")
	}
	if device.ExpiresIn <= 0 {
		device.ExpiresIn = int(cfg.oauthTimeout().Seconds())
	}
	if max := int(cfg.oauthTimeout().Seconds()); device.ExpiresIn > max {
		device.ExpiresIn = max
	}
	if device.Interval < 5 {
		device.Interval = 5
	}
	state, errState := randomIdentifier(24)
	if errState != nil {
		return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("generate OAuth state: %w", errState)
	}
	now := s.now()
	session := &deviceSession{
		DeviceCode: device.DeviceCode,
		ClientID:   cfg.GitHubClientID,
		UserCode:   device.UserCode,
		ExpiresAt:  now.Add(time.Duration(device.ExpiresIn) * time.Second),
		Interval:   time.Duration(device.Interval) * time.Second,
		NextPoll:   now,
	}
	s.oauthMu.Lock()
	s.oauthSession[state] = session
	s.oauthMu.Unlock()

	loginURL := device.VerificationURIComplete
	if loginURL == "" {
		parsed, errParse := url.Parse(device.VerificationURI)
		if errParse != nil {
			return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("parse GitHub verification URL: %w", errParse)
		}
		query := parsed.Query()
		query.Set("user_code", device.UserCode)
		parsed.RawQuery = query.Encode()
		loginURL = parsed.String()
	}
	return pluginapi.AuthLoginStartResponse{
		Provider:  providerID,
		URL:       loginURL,
		State:     state,
		ExpiresAt: session.ExpiresAt.UTC(),
		Metadata: map[string]any{
			"user_code": device.UserCode,
			"interval":  device.Interval,
		},
	}, nil
}

func (s *Service) PollLogin(ctx context.Context, callbackID, state string) (pluginapi.AuthLoginPollResponse, error) {
	state = strings.TrimSpace(state)
	now := s.now()
	s.oauthMu.Lock()
	session := s.oauthSession[state]
	if session == nil {
		s.oauthMu.Unlock()
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "unknown or expired GitHub device flow"}, nil
	}
	if !now.Before(session.ExpiresAt) {
		delete(s.oauthSession, state)
		s.oauthMu.Unlock()
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "GitHub device code expired"}, nil
	}
	if session.Polling || now.Before(session.NextPoll) {
		s.oauthMu.Unlock()
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusPending, Message: "waiting for GitHub authorization"}, nil
	}
	session.Polling = true
	deviceCode := session.DeviceCode
	clientID := session.ClientID
	interval := session.Interval
	s.oauthMu.Unlock()

	form := url.Values{
		"client_id":   {clientID},
		"device_code": {deviceCode},
		"grant_type":  {deviceGrantType},
	}
	cfg := s.Config()
	resp, errDo := s.host.Do(ctx, callbackID, transport.Request{
		Method: http.MethodPost,
		URL:    cfg.GitHubBaseURL + "/login/oauth/access_token",
		Headers: http.Header{
			"Accept":       []string{"application/json"},
			"Content-Type": []string{"application/x-www-form-urlencoded"},
			"User-Agent":   []string{userAgent()},
		},
		Body: []byte(form.Encode()),
	})
	if errDo != nil {
		s.finishPoll(state, interval, false)
		return pluginapi.AuthLoginPollResponse{}, fmt.Errorf("poll GitHub device flow: %w", errDo)
	}
	var token oauthTokenResponse
	if errUnmarshal := json.Unmarshal(resp.Body, &token); errUnmarshal != nil {
		s.finishPoll(state, interval, false)
		return pluginapi.AuthLoginPollResponse{}, fmt.Errorf("decode GitHub device token response: %w", errUnmarshal)
	}
	decision := classifyDeviceToken(resp.StatusCode, token, interval)
	if decision.Status == pluginapi.AuthLoginStatusPending {
		s.finishPoll(state, decision.NextInterval, false)
		return pluginapi.AuthLoginPollResponse{Status: decision.Status, Message: decision.Message}, nil
	}
	if decision.Status == pluginapi.AuthLoginStatusError {
		s.finishPoll(state, interval, decision.Terminal)
		return pluginapi.AuthLoginPollResponse{Status: decision.Status, Message: decision.Message}, nil
	}

	user, errUser := s.fetchGitHubUser(ctx, callbackID, token.AccessToken)
	if errUser != nil {
		s.finishPoll(state, interval, true)
		return pluginapi.AuthLoginPollResponse{}, errUser
	}
	createdAt := s.now()
	storage := authStorage{
		Type:               providerID,
		GitHubAccessToken:  strings.TrimSpace(token.AccessToken),
		GitHubRefreshToken: strings.TrimSpace(token.RefreshToken),
		TokenType:          strings.TrimSpace(token.TokenType),
		Scope:              strings.TrimSpace(token.Scope),
		GitHubLogin:        strings.TrimSpace(user.Login),
		GitHubUserID:       user.ID,
		OAuthClientID:      clientID,
		UpdatedAt:          createdAt.UTC().Format(time.RFC3339),
	}
	if token.ExpiresIn > 0 {
		storage.ExpiresAt = createdAt.Add(time.Duration(token.ExpiresIn) * time.Second).Unix()
	}
	if token.RefreshTokenExpiresIn > 0 {
		storage.RefreshTokenExpiresAt = createdAt.Add(time.Duration(token.RefreshTokenExpiresIn) * time.Second).Unix()
	}
	data, errData := authData(storage, "", "", "", "", false, nil, nil)
	if errData != nil {
		s.finishPoll(state, interval, true)
		return pluginapi.AuthLoginPollResponse{}, errData
	}
	s.finishPoll(state, interval, true)
	return pluginapi.AuthLoginPollResponse{
		Status:  pluginapi.AuthLoginStatusSuccess,
		Message: "GitHub Copilot authentication succeeded",
		Auth:    data,
	}, nil
}

func (s *Service) finishPoll(state string, interval time.Duration, terminal bool) {
	s.oauthMu.Lock()
	defer s.oauthMu.Unlock()
	session := s.oauthSession[state]
	if session == nil {
		return
	}
	if terminal {
		delete(s.oauthSession, state)
		return
	}
	session.Polling = false
	session.Interval = interval
	session.NextPoll = s.now().Add(interval)
}

func classifyDeviceToken(statusCode int, token oauthTokenResponse, currentInterval time.Duration) pollDecision {
	if strings.TrimSpace(token.AccessToken) != "" && statusCode >= 200 && statusCode < 300 {
		token.AccessToken = strings.TrimSpace(token.AccessToken)
		return pollDecision{Status: pluginapi.AuthLoginStatusSuccess, Token: &token, Terminal: true}
	}
	code := strings.ToLower(strings.TrimSpace(token.Error))
	detail := strings.TrimSpace(token.ErrorDescription)
	switch code {
	case "authorization_pending":
		return pollDecision{
			Status:       pluginapi.AuthLoginStatusPending,
			Message:      "waiting for GitHub authorization",
			NextInterval: currentInterval,
		}
	case "slow_down":
		next := currentInterval + 5*time.Second
		if token.Interval > 0 && time.Duration(token.Interval)*time.Second > next {
			next = time.Duration(token.Interval) * time.Second
		}
		return pollDecision{
			Status:       pluginapi.AuthLoginStatusPending,
			Message:      "GitHub requested slower device polling",
			NextInterval: next,
		}
	case "expired_token", "incorrect_device_code", "access_denied", "device_flow_disabled", "incorrect_client_credentials", "unsupported_grant_type":
		if detail == "" {
			detail = strings.ReplaceAll(code, "_", " ")
		}
		return pollDecision{Status: pluginapi.AuthLoginStatusError, Message: "GitHub device flow failed: " + detail, Terminal: true}
	default:
		if detail == "" {
			if code != "" {
				detail = strings.ReplaceAll(code, "_", " ")
			} else {
				detail = "unexpected HTTP " + strconv.Itoa(statusCode)
			}
		}
		return pollDecision{Status: pluginapi.AuthLoginStatusError, Message: "GitHub device flow failed: " + detail, Terminal: statusCode >= 400 && statusCode < 500}
	}
}

func (s *Service) fetchGitHubUser(ctx context.Context, callbackID, accessToken string) (githubUser, error) {
	cfg := s.Config()
	resp, errDo := s.host.Do(ctx, callbackID, transport.Request{
		Method: http.MethodGet,
		URL:    cfg.GitHubAPIURL + "/user",
		Headers: http.Header{
			"Accept":               []string{"application/vnd.github+json"},
			"Authorization":        []string{"Bearer " + accessToken},
			"User-Agent":           []string{userAgent()},
			"X-GitHub-Api-Version": []string{"2022-11-28"},
		},
	})
	if errDo != nil {
		return githubUser{}, fmt.Errorf("fetch GitHub user: %w", errDo)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return githubUser{}, upstreamStatusError(resp.StatusCode, redact.ErrorBody(resp.Body, accessToken))
	}
	var user githubUser
	if errUnmarshal := json.Unmarshal(resp.Body, &user); errUnmarshal != nil {
		return githubUser{}, fmt.Errorf("decode GitHub user: %w", errUnmarshal)
	}
	user.Login = strings.TrimSpace(user.Login)
	if user.Login == "" {
		return githubUser{}, fmt.Errorf("GitHub user response has no login")
	}
	return user, nil
}

func (s *Service) RefreshAuth(ctx context.Context, callbackID string, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
	storage, errParse := parseStorage(req.StorageJSON)
	if errParse != nil {
		return pluginapi.AuthRefreshResponse{}, errParse
	}
	now := s.now()
	if storage.ExpiresAt == 0 || time.Unix(storage.ExpiresAt, 0).After(now.Add(10*time.Minute)) {
		data, errData := authData(storage, req.AuthID, "", "", "", false, req.Metadata, req.Attributes)
		if errData != nil {
			return pluginapi.AuthRefreshResponse{}, errData
		}
		return pluginapi.AuthRefreshResponse{Auth: data, NextRefreshAfter: data.NextRefreshAfter}, nil
	}
	if storage.GitHubRefreshToken == "" {
		return pluginapi.AuthRefreshResponse{}, statusError("auth_expired", "GitHub OAuth token expired and has no refresh token", http.StatusUnauthorized)
	}
	if storage.RefreshTokenExpiresAt > 0 && !time.Unix(storage.RefreshTokenExpiresAt, 0).After(now) {
		return pluginapi.AuthRefreshResponse{}, statusError("auth_expired", "GitHub OAuth refresh token expired", http.StatusUnauthorized)
	}
	clientID := storage.OAuthClientID
	if clientID == "" {
		clientID = s.Config().GitHubClientID
	}
	form := url.Values{
		"client_id":     {clientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {storage.GitHubRefreshToken},
	}
	cfg := s.Config()
	resp, errDo := s.host.Do(ctx, callbackID, transport.Request{
		Method: http.MethodPost,
		URL:    cfg.GitHubBaseURL + "/login/oauth/access_token",
		Headers: http.Header{
			"Accept":       []string{"application/json"},
			"Content-Type": []string{"application/x-www-form-urlencoded"},
			"User-Agent":   []string{userAgent()},
		},
		Body: []byte(form.Encode()),
	})
	if errDo != nil {
		return pluginapi.AuthRefreshResponse{}, fmt.Errorf("refresh GitHub OAuth token: %w", errDo)
	}
	var token oauthTokenResponse
	if errUnmarshal := json.Unmarshal(resp.Body, &token); errUnmarshal != nil {
		return pluginapi.AuthRefreshResponse{}, fmt.Errorf("decode GitHub OAuth refresh response: %w", errUnmarshal)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || strings.TrimSpace(token.AccessToken) == "" {
		return pluginapi.AuthRefreshResponse{}, upstreamStatusError(resp.StatusCode, redact.ErrorBody(resp.Body, storage.GitHubAccessToken, storage.GitHubRefreshToken))
	}
	storage.GitHubAccessToken = strings.TrimSpace(token.AccessToken)
	if strings.TrimSpace(token.RefreshToken) != "" {
		storage.GitHubRefreshToken = strings.TrimSpace(token.RefreshToken)
	}
	storage.TokenType = strings.TrimSpace(token.TokenType)
	storage.Scope = strings.TrimSpace(token.Scope)
	storage.UpdatedAt = now.UTC().Format(time.RFC3339)
	if token.ExpiresIn > 0 {
		storage.ExpiresAt = now.Add(time.Duration(token.ExpiresIn) * time.Second).Unix()
	}
	if token.RefreshTokenExpiresIn > 0 {
		storage.RefreshTokenExpiresAt = now.Add(time.Duration(token.RefreshTokenExpiresIn) * time.Second).Unix()
	}
	s.invalidateAuth(req.AuthID)
	data, errData := authData(storage, req.AuthID, "", "", "", false, req.Metadata, req.Attributes)
	if errData != nil {
		return pluginapi.AuthRefreshResponse{}, errData
	}
	return pluginapi.AuthRefreshResponse{Auth: data, NextRefreshAfter: data.NextRefreshAfter}, nil
}

func randomIdentifier(bytesCount int) (string, error) {
	raw := make([]byte, bytesCount)
	if _, errRead := rand.Read(raw); errRead != nil {
		return "", errRead
	}
	return hex.EncodeToString(raw), nil
}

func userAgent() string {
	return "CLIProxyAPI-Copilot-Plugin/0.1.0"
}
