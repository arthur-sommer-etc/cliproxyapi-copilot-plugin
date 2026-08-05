package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type authStorage struct {
	Type                  string `json:"type"`
	GitHubAccessToken     string `json:"github_access_token"`
	GitHubRefreshToken    string `json:"github_refresh_token,omitempty"`
	TokenType             string `json:"token_type,omitempty"`
	Scope                 string `json:"scope,omitempty"`
	ExpiresAt             int64  `json:"expires_at,omitempty"`
	RefreshTokenExpiresAt int64  `json:"refresh_token_expires_at,omitempty"`
	GitHubLogin           string `json:"github_login"`
	GitHubUserID          int64  `json:"github_user_id,omitempty"`
	OAuthClientID         string `json:"oauth_client_id,omitempty"`
	UpdatedAt             string `json:"updated_at"`
}

func parseStorage(raw []byte) (authStorage, error) {
	var storage authStorage
	if len(raw) == 0 {
		return storage, fmt.Errorf("Copilot auth storage is empty")
	}
	if errUnmarshal := json.Unmarshal(raw, &storage); errUnmarshal != nil {
		return storage, fmt.Errorf("decode Copilot auth storage: %w", errUnmarshal)
	}
	if !strings.EqualFold(strings.TrimSpace(storage.Type), providerID) {
		return storage, fmt.Errorf("auth type is not %s", providerID)
	}
	storage.GitHubAccessToken = strings.TrimSpace(storage.GitHubAccessToken)
	storage.GitHubRefreshToken = strings.TrimSpace(storage.GitHubRefreshToken)
	storage.GitHubLogin = strings.TrimSpace(storage.GitHubLogin)
	storage.OAuthClientID = strings.TrimSpace(storage.OAuthClientID)
	if storage.GitHubAccessToken == "" {
		return storage, fmt.Errorf("Copilot auth storage has no GitHub access token")
	}
	return storage, nil
}

func marshalStorage(storage authStorage) ([]byte, error) {
	return json.Marshal(storage)
}

func authData(storage authStorage, id, fileName, prefix, proxyURL string, disabled bool, metadata map[string]any, attributes map[string]string) (pluginapi.AuthData, error) {
	raw, errMarshal := marshalStorage(storage)
	if errMarshal != nil {
		return pluginapi.AuthData{}, fmt.Errorf("encode Copilot auth storage: %w", errMarshal)
	}
	if fileName == "" {
		fileName = credentialFileName(storage.GitHubLogin)
	}
	if id == "" {
		id = fileName
	}
	if len(metadata) == 0 {
		metadata = map[string]any{
			"type":         providerID,
			"github_login": storage.GitHubLogin,
		}
	}
	if len(attributes) == 0 {
		attributes = map[string]string{"auth_kind": "oauth"}
	}
	nextRefresh := nextGitHubRefresh(storage, time.Now())
	return pluginapi.AuthData{
		Provider:         providerID,
		ID:               id,
		FileName:         fileName,
		Label:            storage.GitHubLogin,
		Prefix:           prefix,
		ProxyURL:         proxyURL,
		Disabled:         disabled,
		StorageJSON:      raw,
		Metadata:         metadata,
		Attributes:       attributes,
		NextRefreshAfter: nextRefresh,
	}, nil
}

func nextGitHubRefresh(storage authStorage, now time.Time) time.Time {
	if storage.ExpiresAt > 0 {
		refreshAt := time.Unix(storage.ExpiresAt, 0).Add(-10 * time.Minute)
		if refreshAt.Before(now.Add(time.Minute)) {
			return now.Add(time.Minute)
		}
		return refreshAt
	}
	return now.Add(24 * time.Hour)
}

func credentialFileName(login string) string {
	login = strings.ToLower(strings.TrimSpace(login))
	var clean strings.Builder
	for _, r := range login {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			clean.WriteRune(r)
		case r == '-', r == '_', r == '.':
			clean.WriteRune(r)
		default:
			clean.WriteByte('-')
		}
	}
	name := strings.Trim(clean.String(), "-.")
	if name == "" {
		name = "account"
	}
	return "copilot-" + name + ".json"
}

func tokenFingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
