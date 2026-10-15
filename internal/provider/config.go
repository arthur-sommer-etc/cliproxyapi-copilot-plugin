package provider

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	authModeTokenExchange = "token_exchange"
	authModeDirectOAuth   = "direct_oauth"

	DefaultGitHubClientID = "Iv1.b507a08c87ecfe98"
	defaultGitHubBaseURL  = "https://github.com"
	defaultGitHubAPIURL   = "https://api.github.com"
	defaultCopilotAPIURL  = "https://api.githubcopilot.com"
)

type Config struct {
	Enabled                  bool              `yaml:"enabled"`
	AuthMode                 string            `yaml:"auth_mode"`
	GitHubClientID           string            `yaml:"github_client_id"`
	GitHubScope              string            `yaml:"github_scope"`
	GitHubBaseURL            string            `yaml:"github_base_url"`
	GitHubAPIURL             string            `yaml:"github_api_url"`
	CopilotAPIURL            string            `yaml:"copilot_api_url"`
	AllowInsecureBaseURLs    bool              `yaml:"allow_insecure_base_urls"`
	OAuthTimeoutSeconds      int               `yaml:"oauth_timeout_seconds"`
	ModelCacheTTLSeconds     int               `yaml:"model_cache_ttl_seconds"`
	TokenExpiryBufferSeconds int               `yaml:"token_expiry_buffer_seconds"`
	ModelEndpointOverrides   map[string]string `yaml:"model_endpoint_overrides"`
	CompactionModels         []string          `yaml:"compaction_models"`
	PromptCacheKey           bool              `yaml:"support-prompt-cache-key"`
	ReasoningReplay          bool              `yaml:"reasoning_replay"`
}

func DefaultConfig() Config {
	return Config{
		Enabled:                  true,
		AuthMode:                 authModeTokenExchange,
		GitHubClientID:           DefaultGitHubClientID,
		GitHubScope:              "read:user",
		GitHubBaseURL:            defaultGitHubBaseURL,
		GitHubAPIURL:             defaultGitHubAPIURL,
		CopilotAPIURL:            defaultCopilotAPIURL,
		OAuthTimeoutSeconds:      900,
		ModelCacheTTLSeconds:     600,
		TokenExpiryBufferSeconds: 300,
		ModelEndpointOverrides:   map[string]string{},
		PromptCacheKey:           true,
		ReasoningReplay:          true,
	}
}

func ParseConfig(raw []byte) (Config, error) {
	cfg := DefaultConfig()
	if len(raw) > 0 {
		if errUnmarshal := yaml.Unmarshal(raw, &cfg); errUnmarshal != nil {
			return Config{}, fmt.Errorf("decode plugin config: %w", errUnmarshal)
		}
	}
	cfg.GitHubClientID = strings.TrimSpace(cfg.GitHubClientID)
	cfg.AuthMode = strings.ToLower(strings.TrimSpace(cfg.AuthMode))
	if cfg.AuthMode == "" {
		cfg.AuthMode = authModeTokenExchange
	}
	cfg.GitHubScope = strings.TrimSpace(cfg.GitHubScope)
	cfg.GitHubBaseURL = strings.TrimRight(strings.TrimSpace(cfg.GitHubBaseURL), "/")
	cfg.GitHubAPIURL = strings.TrimRight(strings.TrimSpace(cfg.GitHubAPIURL), "/")
	cfg.CopilotAPIURL = strings.TrimRight(strings.TrimSpace(cfg.CopilotAPIURL), "/")
	cfg.CompactionModels = normalizeModelIDs(cfg.CompactionModels)
	var errOverrides error
	cfg.ModelEndpointOverrides, errOverrides = normalizeEndpointOverrides(cfg.ModelEndpointOverrides)
	if errOverrides != nil {
		return Config{}, errOverrides
	}
	if cfg.GitHubClientID == "" {
		return Config{}, fmt.Errorf("github_client_id is required")
	}
	if cfg.AuthMode != authModeTokenExchange && cfg.AuthMode != authModeDirectOAuth {
		return Config{}, fmt.Errorf("auth_mode must be %q or %q", authModeTokenExchange, authModeDirectOAuth)
	}
	for name, value := range map[string]string{
		"github_base_url": cfg.GitHubBaseURL,
		"github_api_url":  cfg.GitHubAPIURL,
		"copilot_api_url": cfg.CopilotAPIURL,
	} {
		if errURL := validateBaseURL(value, cfg.AllowInsecureBaseURLs); errURL != nil {
			return Config{}, fmt.Errorf("%s: %w", name, errURL)
		}
	}
	if cfg.OAuthTimeoutSeconds < 60 || cfg.OAuthTimeoutSeconds > 1800 {
		return Config{}, fmt.Errorf("oauth_timeout_seconds must be between 60 and 1800")
	}
	if cfg.ModelCacheTTLSeconds < 30 || cfg.ModelCacheTTLSeconds > 3600 {
		return Config{}, fmt.Errorf("model_cache_ttl_seconds must be between 30 and 3600")
	}
	if cfg.TokenExpiryBufferSeconds < 30 || cfg.TokenExpiryBufferSeconds > 900 {
		return Config{}, fmt.Errorf("token_expiry_buffer_seconds must be between 30 and 900")
	}
	return cfg, nil
}

func normalizeModelIDs(modelIDs []string) []string {
	seen := make(map[string]struct{}, len(modelIDs))
	out := make([]string, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		modelID = strings.ToLower(strings.TrimSpace(modelID))
		if modelID == "" {
			continue
		}
		if _, exists := seen[modelID]; exists {
			continue
		}
		seen[modelID] = struct{}{}
		out = append(out, modelID)
	}
	return out
}

func normalizeEndpointOverrides(overrides map[string]string) (map[string]string, error) {
	normalized := make(map[string]string, len(overrides))
	for modelID, endpoint := range overrides {
		modelID = strings.ToLower(strings.TrimSpace(modelID))
		if modelID == "" {
			return nil, fmt.Errorf("model_endpoint_overrides contains an empty model ID")
		}
		switch strings.ToLower(strings.TrimSpace(endpoint)) {
		case "/responses", "responses", "openai-responses":
			endpoint = "/responses"
		case "/chat/completions", "chat", "chat-completions", "openai":
			endpoint = "/chat/completions"
		case "/v1/messages", "messages", "anthropic":
			endpoint = "/v1/messages"
		default:
			return nil, fmt.Errorf("model_endpoint_overrides[%q] must select /responses, /chat/completions, or /v1/messages", modelID)
		}
		if prior, exists := normalized[modelID]; exists && prior != endpoint {
			return nil, fmt.Errorf("model_endpoint_overrides has conflicting entries for %q", modelID)
		}
		normalized[modelID] = endpoint
	}
	return normalized, nil
}

func validateBaseURL(raw string, allowInsecure bool) error {
	parsed, errParse := url.Parse(raw)
	if errParse != nil || parsed.Hostname() == "" || parsed.User != nil {
		return fmt.Errorf("invalid absolute URL")
	}
	if parsed.Scheme != "https" && !(allowInsecure && parsed.Scheme == "http") {
		return fmt.Errorf("URL must use HTTPS")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("base URL must not contain query or fragment")
	}
	return nil
}

func (c Config) oauthTimeout() time.Duration {
	return time.Duration(c.OAuthTimeoutSeconds) * time.Second
}

func (c Config) modelCacheTTL() time.Duration {
	return time.Duration(c.ModelCacheTTLSeconds) * time.Second
}

func (c Config) tokenExpiryBuffer() time.Duration {
	return time.Duration(c.TokenExpiryBufferSeconds) * time.Second
}
