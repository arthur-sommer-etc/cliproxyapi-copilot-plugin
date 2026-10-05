package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestSelectEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		model        upstreamModel
		sourceFormat string
		want         string
		wantError    bool
	}{
		{
			name:         "responses source preserves native endpoint",
			model:        upstreamModel{ID: "model-a", SupportedEndpoints: []string{"/chat/completions", "/responses"}},
			sourceFormat: "openai-response",
			want:         translate.EndpointResponses,
		},
		{
			name:         "Claude prefers native Messages endpoint",
			model:        upstreamModel{ID: "model-b", SupportedEndpoints: []string{"/responses", "messages"}},
			sourceFormat: "claude",
			want:         translate.EndpointMessages,
		},
		{
			name:         "chat source prefers native Chat endpoint",
			model:        upstreamModel{ID: "model-c", SupportedEndpoints: []string{"/responses", "/chat/completions"}},
			sourceFormat: "openai",
			want:         translate.EndpointChatCompletions,
		},
		{
			name:         "Claude falls back to Responses",
			model:        upstreamModel{ID: "model-d", SupportedEndpoints: []string{"/chat/completions", "/responses"}},
			sourceFormat: "claude",
			want:         translate.EndpointResponses,
		},
		{
			name: "Claude client routes to Gemini Chat",
			model: upstreamModel{
				ID: "gemini-3.8-flash", Vendor: "Google",
				SupportedEndpoints: []string{"/chat/completions"},
			},
			sourceFormat: "claude",
			want:         translate.EndpointChatCompletions,
		},
		{
			name: "Claude client routes to native GPT Responses",
			model: upstreamModel{
				ID: "gpt-6-luna", Vendor: "OpenAI",
				SupportedEndpoints: []string{"/responses"},
			},
			sourceFormat: "claude",
			want:         translate.EndpointResponses,
		},
		{
			name: "Claude client routes to native Claude Messages",
			model: upstreamModel{
				ID: "claude-sonnet-5.5", Vendor: "Anthropic",
				SupportedEndpoints: []string{"/v1/messages"},
			},
			sourceFormat: "claude",
			want:         translate.EndpointMessages,
		},
		{
			name: "Chat client keeps native Chat endpoint for Claude model",
			model: upstreamModel{
				ID: "claude-sonnet-5.5", Vendor: "Anthropic",
				SupportedEndpoints: []string{"/v1/messages", "/chat/completions"},
			},
			sourceFormat: "openai",
			want:         translate.EndpointChatCompletions,
		},
		{
			name: "Responses client routes Gemini fallback to Chat",
			model: upstreamModel{
				ID: "gemini-3.8-flash", Vendor: "Google",
				Capabilities:       modelCapabilities{Family: "gemini"},
				SupportedEndpoints: []string{"/v1/messages", "/chat/completions"},
			},
			sourceFormat: "openai-response",
			want:         translate.EndpointChatCompletions,
		},
		{
			name: "Responses client uses native GPT endpoint",
			model: upstreamModel{
				ID: "gpt-6-luna", Vendor: "OpenAI",
				SupportedEndpoints: []string{"/chat/completions", "/responses"},
			},
			sourceFormat: "openai-response",
			want:         translate.EndpointResponses,
		},
		{
			name: "Responses client prefers Claude Messages over Chat fallback",
			model: upstreamModel{
				ID: "claude-sonnet-5.5", Vendor: "Anthropic",
				Capabilities:       modelCapabilities{Family: "Claude"},
				SupportedEndpoints: []string{"/chat/completions", "/v1/messages"},
			},
			sourceFormat: "openai-response",
			want:         translate.EndpointMessages,
		},
		{
			name: "Claude family metadata selects Messages fallback",
			model: upstreamModel{
				ID: "model-family-only", Vendor: "Other",
				Capabilities:       modelCapabilities{Family: "claude-sonnet"},
				SupportedEndpoints: []string{"/chat/completions", "/v1/messages"},
			},
			sourceFormat: "openai-response",
			want:         translate.EndpointMessages,
		},
		{
			name: "Claude model ID selects Messages fallback",
			model: upstreamModel{
				ID: "claude-sonnet-5.5", Vendor: "Other",
				SupportedEndpoints: []string{"/chat/completions", "/v1/messages"},
			},
			sourceFormat: "openai-response",
			want:         translate.EndpointMessages,
		},
		{
			name: "Responses source preserves native endpoint before Claude fallback",
			model: upstreamModel{
				ID: "claude-sonnet-5.5", Vendor: "Anthropic",
				SupportedEndpoints: []string{"/chat/completions", "/v1/messages", "/responses"},
			},
			sourceFormat: "openai-response",
			want:         translate.EndpointResponses,
		},
		{
			name: "unknown vendor keeps existing Responses fallback order",
			model: upstreamModel{
				ID: "model-unknown", Vendor: "Other",
				SupportedEndpoints: []string{"/chat/completions", "/v1/messages"},
			},
			sourceFormat: "openai-response",
			want:         translate.EndpointChatCompletions,
		},
		{
			name: "unrecognized source keeps existing endpoint order",
			model: upstreamModel{
				ID: "claude-sonnet-5.5", Vendor: "Anthropic",
				SupportedEndpoints: []string{"/chat/completions", "/v1/messages"},
			},
			sourceFormat: "unknown",
			want:         translate.EndpointChatCompletions,
		},
		{
			name:         "unsupported",
			model:        upstreamModel{ID: "embedding-model", SupportedEndpoints: []string{"/embeddings"}},
			sourceFormat: "openai-response",
			wantError:    true,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := selectEndpoint(test.model, test.sourceFormat)
			if test.wantError {
				if err == nil {
					t.Fatal("expected an endpoint selection error")
				}
				return
			}
			if err != nil {
				t.Fatalf("select endpoint: %v", err)
			}
			if got != test.want {
				t.Fatalf("endpoint = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNormalizeModelsDoesNotInventUnsupportedEndpoints(t *testing.T) {
	t.Parallel()

	models := normalizeModels([]upstreamModel{
		{
			ID:                 "model-a",
			SupportedEndpoints: []string{"/chat/completions"},
			Capabilities: modelCapabilities{
				Supports: modelSupports{Streaming: true, ToolCalls: true, Vision: true},
				Limits:   modelLimits{MaxPromptTokens: 100, MaxOutputTokens: 20},
			},
		},
	})
	if len(models) != 1 || contains(models[0].SupportedEndpoints, translate.EndpointResponses) {
		t.Fatalf("unsupported responses endpoint was invented: %#v", models)
	}
	info := modelInfos(models)[0]
	if contains(info.SupportedGenerationMethods, translate.EndpointResponses) {
		t.Fatalf("model metadata invented responses endpoint: %#v", info.SupportedGenerationMethods)
	}
	if !contains(info.SupportedInputModalities, "IMAGE") {
		t.Fatalf("model metadata omits image support: %#v", info.SupportedInputModalities)
	}
}

func TestModelInfosPreservesAdaptiveThinkingMetadata(t *testing.T) {
	t.Parallel()
	model := upstreamModel{
		ID:     "claude-sonnet-5.5",
		Vendor: "Anthropic",
		Capabilities: modelCapabilities{
			Family: "claude-sonnet-5.5",
			Supports: modelSupports{
				AdaptiveThinking: true,
				ReasoningEffort:  []string{"low", "medium", "high", "xhigh", "max"},
			},
		},
	}
	info := modelInfos([]upstreamModel{model})[0]
	if info.Thinking == nil || !info.Thinking.DynamicAllowed {
		t.Fatalf("adaptive thinking capability was lost: %#v", info.Thinking)
	}
	if len(info.Thinking.Levels) != len(model.Capabilities.Supports.ReasoningEffort) {
		t.Fatalf("reasoning effort levels = %#v, want %#v", info.Thinking.Levels, model.Capabilities.Supports.ReasoningEffort)
	}
	model.Capabilities.Supports.ReasoningEffort[0] = "changed"
	if info.Thinking.Levels[0] != "low" {
		t.Fatalf("model metadata aliases source reasoning effort levels: %#v", info.Thinking.Levels)
	}
}

func TestFilterModelsExcludesConfiguredPrefixes(t *testing.T) {
	t.Parallel()

	models := filterModels([]upstreamModel{
		{ID: "gpt-5.6-sol"},
		{ID: "claude-sonnet-5"},
		{ID: "Claude-Haiku-4.5"},
	}, []string{"claude-"})
	if len(models) != 1 || models[0].ID != "gpt-5.6-sol" {
		t.Fatalf("filtered models = %#v", models)
	}
}

func TestAvailableModelsUsesExplicitAvailabilityMetadata(t *testing.T) {
	t.Parallel()
	var list modelListResponse
	if err := json.Unmarshal([]byte(`{"data":[
		{"id":"picker-and-policy-enabled","model_picker_enabled":true,"policy":{"state":"enabled"},"capabilities":{"type":"chat"},"supported_endpoints":["/responses"]},
		{"id":"policy-absent-picker-enabled","model_picker_enabled":true,"capabilities":{"type":"chat"},"supported_endpoints":["/chat/completions"]},
		{"id":"legacy-metadata-absent","supported_endpoints":["/v1/messages"]},
		{"id":"picker-disabled","model_picker_enabled":false,"policy":{"state":"enabled"},"capabilities":{"type":"chat"},"supported_endpoints":["/responses"]},
		{"id":"policy-disabled","model_picker_enabled":true,"policy":{"state":"disabled"},"capabilities":{"type":"chat"},"supported_endpoints":["/responses"]},
		{"id":"policy-non-enabled","policy":{"state":"preview"},"capabilities":{"type":"chat"},"supported_endpoints":["/responses"]},
		{"id":"completion-type","capabilities":{"type":"completion"},"supported_endpoints":["/chat/completions"]},
		{"id":"embedding-type","capabilities":{"type":"embeddings"},"supported_endpoints":["/chat/completions"]},
		{"id":"no-chat-endpoint","capabilities":{"type":"chat"},"supported_endpoints":["/embeddings"]}
	]}`), &list); err != nil {
		t.Fatalf("decode synthetic model inventory: %v", err)
	}
	got := availableModels(normalizeModels(list.Data))
	want := []string{"legacy-metadata-absent", "picker-and-policy-enabled", "policy-absent-picker-enabled"}
	if len(got) != len(want) {
		t.Fatalf("available models = %#v, want IDs %#v", got, want)
	}
	for index, id := range want {
		if got[index].ID != id {
			t.Fatalf("available model %d = %q, want %q", index, got[index].ID, id)
		}
	}
}

func TestModelsForAuthReturnsOnlyAvailableInventoryEntries(t *testing.T) {
	t.Parallel()
	models := []upstreamModel{
		{ID: "visible", ModelPickerEnabled: boolPointer(true), Policy: &modelPolicy{State: "enabled"}, Capabilities: modelCapabilities{Type: "chat"}, SupportedEndpoints: []string{"/responses"}},
		{ID: "disabled", ModelPickerEnabled: boolPointer(true), Policy: &modelPolicy{State: "disabled"}, Capabilities: modelCapabilities{Type: "chat"}, SupportedEndpoints: []string{"/responses"}},
		{ID: "internal", ModelPickerEnabled: boolPointer(false), Capabilities: modelCapabilities{Type: "chat"}, SupportedEndpoints: []string{"/responses"}},
		{ID: "embedding", Capabilities: modelCapabilities{Type: "embeddings"}, SupportedEndpoints: []string{"/responses"}},
		{ID: "no-endpoint", Capabilities: modelCapabilities{Type: "chat"}},
	}
	service, storage, rawStorage := serviceWithCachedModels(t, models)
	response, err := service.ModelsForAuth(context.Background(), "callback", pluginapi.AuthModelRequest{AuthID: "auth", StorageJSON: rawStorage})
	if err != nil {
		t.Fatalf("discover available models: %v", err)
	}
	if len(response.Models) != 1 || response.Models[0].ID != "visible" {
		t.Fatalf("exposed models = %#v, want only visible", response.Models)
	}
	if storage.GitHubAccessToken == "" {
		t.Fatal("test storage lost its synthetic credential")
	}
}

func TestModelsForAuthReturnsEmptyWhenInventoryHasNoAvailableEntries(t *testing.T) {
	t.Parallel()
	service, _, rawStorage := serviceWithCachedModels(t, []upstreamModel{
		{ID: "disabled-a", Policy: &modelPolicy{State: "disabled"}, SupportedEndpoints: []string{"/responses"}},
		{ID: "disabled-b", Policy: &modelPolicy{State: "restricted"}, SupportedEndpoints: []string{"/chat/completions"}},
	})
	response, err := service.ModelsForAuth(context.Background(), "callback", pluginapi.AuthModelRequest{AuthID: "auth", StorageJSON: rawStorage})
	if err != nil {
		t.Fatalf("discover empty available model set: %v", err)
	}
	if response.Provider != providerID || len(response.Models) != 0 {
		t.Fatalf("empty discovery response = %#v, want provider %q and no models", response, providerID)
	}
}

func TestEndpointOverrideCannotBypassNonEnabledPolicy(t *testing.T) {
	t.Parallel()
	service, storage, _ := serviceWithCachedModels(t, []upstreamModel{
		{ID: "policy-disabled", Policy: &modelPolicy{State: "disabled"}, SupportedEndpoints: []string{"/responses"}},
		{ID: "picker-hidden", ModelPickerEnabled: boolPointer(false)},
	})
	service.config.ModelEndpointOverrides["policy-disabled"] = translate.EndpointResponses
	service.config.ModelEndpointOverrides["picker-hidden"] = translate.EndpointResponses
	_, _, _, err := service.endpointForModel(context.Background(), "callback", "auth", storage, "policy-disabled", "openai-response")
	statusErr, ok := err.(*StatusError)
	if !ok || statusErr.HTTPStatus != http.StatusNotFound {
		t.Fatalf("disabled model override error = %#v, want 404", err)
	}
	endpoint, model, _, err := service.endpointForModel(context.Background(), "callback", "auth", storage, "picker-hidden", "openai-response")
	if err != nil {
		t.Fatalf("explicit override for picker-hidden model: %v", err)
	}
	if endpoint != translate.EndpointResponses || model.ID != "picker-hidden" {
		t.Fatalf("override selection = (%q, %#v), want Responses and raw inventory model", endpoint, model)
	}
}

func TestCloneUpstreamModelsCopiesAvailabilityMetadata(t *testing.T) {
	t.Parallel()
	models := []upstreamModel{{ModelPickerEnabled: boolPointer(true), Policy: &modelPolicy{State: "enabled"}}}
	cloned := cloneUpstreamModels(models)
	*cloned[0].ModelPickerEnabled = false
	cloned[0].Policy.State = "disabled"
	if !*models[0].ModelPickerEnabled || models[0].Policy.State != "enabled" {
		t.Fatalf("availability metadata shares cache-owned values: %#v", models[0])
	}
}

func serviceWithCachedModels(t *testing.T, models []upstreamModel) (*Service, authStorage, []byte) {
	t.Helper()
	now := time.Now().UTC()
	storage := authStorage{Type: providerID, GitHubAccessToken: "synthetic-access-token"}
	rawStorage, err := marshalStorage(storage)
	if err != nil {
		t.Fatalf("marshal synthetic storage: %v", err)
	}
	fingerprint := tokenFingerprint(storage.GitHubAccessToken)
	service := New(&tokenExchangeHost{})
	service.now = func() time.Time { return now }
	service.tokenEntries["auth"] = copilotTokenEntry{
		Token:       "synthetic-copilot-token",
		APIBaseURL:  "https://copilot.example",
		ExpiresAt:   now.Add(time.Hour),
		Fingerprint: fingerprint,
	}
	service.modelEntries["auth"] = modelCacheEntry{
		Fingerprint: fingerprint,
		APIBaseURL:  "https://copilot.example",
		ExpiresAt:   now.Add(time.Hour),
		Models:      cloneUpstreamModels(models),
	}
	return service, storage, rawStorage
}

func boolPointer(value bool) *bool {
	return &value
}

func TestNormalizeModelPrefixes(t *testing.T) {
	t.Parallel()

	got := normalizeModelPrefixes([]string{" Claude- ", "claude-", "", "GPT-"})
	if len(got) != 2 || got[0] != "claude-" || got[1] != "gpt-" {
		t.Fatalf("normalized prefixes = %#v", got)
	}
}
