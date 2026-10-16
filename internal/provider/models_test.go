package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

func TestEndpointForModelWithPayloadSelectsAdvertisedLosslessImageRoute(t *testing.T) {
	t.Parallel()

	model := capturedCopilotModel(t, "claude-haiku-5.5")
	if !model.Capabilities.Supports.Vision || !contains(model.SupportedEndpoints, translate.EndpointMessages) || !contains(model.SupportedEndpoints, translate.EndpointChatCompletions) {
		t.Fatalf("captured Claude catalog capabilities changed: %#v", model)
	}
	for _, detail := range []string{"high", "low"} {
		t.Run(detail, func(t *testing.T) {
			service, storage, _ := serviceWithCachedModels(t, []upstreamModel{model})
			body := capturedResponsesImageRequest(t, detail)
			endpoint, selected, _, err := service.endpointForModelWithPayload(context.Background(), "callback", "auth", storage, model.ID, "openai-response", body)
			if err != nil {
				t.Fatalf("select attachment-compatible endpoint: %v", err)
			}
			if selected.ID != model.ID || endpoint != translate.EndpointChatCompletions {
				t.Fatalf("selected endpoint = (%q, %q), want same model via advertised Chat Completions", endpoint, selected.ID)
			}
			if err := translate.ValidateRequestAttachmentsForEndpoint("openai-response", endpoint, body); err != nil {
				t.Fatalf("selected endpoint does not preserve captured image detail: %v", err)
			}
		})
	}
}

func TestEndpointForModelWithPayloadPreservesDefaultAndOverrideRoutes(t *testing.T) {
	t.Parallel()

	model := capturedCopilotModel(t, "claude-haiku-5.5")
	service, storage, _ := serviceWithCachedModels(t, []upstreamModel{model})
	plain := []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)
	endpoint, _, _, err := service.endpointForModelWithPayload(context.Background(), "callback", "auth", storage, model.ID, "openai-response", plain)
	if err != nil || endpoint != translate.EndpointMessages {
		t.Fatalf("plain Responses request endpoint = %q, error=%v; want unchanged Messages preference", endpoint, err)
	}

	service.config.ModelEndpointOverrides[model.ID] = translate.EndpointMessages
	body := capturedResponsesImageRequest(t, "high")
	endpoint, _, _, err = service.endpointForModelWithPayload(context.Background(), "callback", "auth", storage, model.ID, "openai-response", body)
	if err != nil || endpoint != translate.EndpointMessages {
		t.Fatalf("explicit Messages override endpoint = %q, error=%v", endpoint, err)
	}
	translationErr := translate.ValidateRequestAttachmentsForEndpoint("openai-response", endpoint, body)
	var detailErr *translate.UnsupportedImageDetailError
	if !errors.As(translationErr, &detailErr) || detailErr.Endpoint != translate.EndpointMessages {
		t.Fatalf("explicit override incompatibility = %v, want typed Messages image-detail error", translationErr)
	}
	statusErr, ok := translationStatusError(translationErr).(*StatusError)
	if !ok || statusErr.Code != "unsupported_image_detail" || statusErr.HTTPStatus != http.StatusUnprocessableEntity {
		t.Fatalf("explicit override status = %#v, want 422 unsupported_image_detail", statusErr)
	}
}

func TestEndpointForModelWithPayloadRequiresAdvertisedVisionAndLosslessRoute(t *testing.T) {
	t.Parallel()

	body := capturedResponsesImageRequest(t, "high")
	base := capturedCopilotModel(t, "claude-haiku-5.5")
	for _, test := range []struct {
		name  string
		model upstreamModel
	}{
		{
			name: "Chat endpoint is not advertised",
			model: func() upstreamModel {
				model := base
				model.SupportedEndpoints = []string{translate.EndpointMessages}
				return model
			}(),
		},
		{
			name: "vision capability is absent",
			model: func() upstreamModel {
				model := base
				model.Capabilities.Supports.Vision = false
				return model
			}(),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, storage, _ := serviceWithCachedModels(t, []upstreamModel{test.model})
			endpoint, _, _, err := service.endpointForModelWithPayload(context.Background(), "callback", "auth", storage, base.ID, "openai-response", body)
			if err != nil || endpoint != translate.EndpointMessages {
				t.Fatalf("endpoint = %q, error=%v; want original Messages route", endpoint, err)
			}
			translationErr := translate.ValidateRequestAttachmentsForEndpoint("openai-response", endpoint, body)
			var detailErr *translate.UnsupportedImageDetailError
			if !errors.As(translationErr, &detailErr) || detailErr.Endpoint != translate.EndpointMessages {
				t.Fatalf("Messages validation error = %v, want typed image-detail incompatibility", translationErr)
			}
		})
	}
}

func TestEndpointForModelWithPayloadDoesNotFallbackForClaudeFileReferences(t *testing.T) {
	t.Parallel()

	model := capturedCopilotModel(t, "claude-haiku-5.5")
	service, storage, _ := serviceWithCachedModels(t, []upstreamModel{model})
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"document","source":{"type":"file","file_id":"file_uploaded"}}]}]}`)
	endpoint, _, _, err := service.endpointForModelWithPayload(context.Background(), "callback", "auth", storage, model.ID, "claude", body)
	if err != nil || endpoint != translate.EndpointMessages {
		t.Fatalf("Claude file-reference route = %q, error=%v; want unchanged native Messages", endpoint, err)
	}
	if err := translate.ValidateRequestAttachmentsForEndpoint("claude", translate.EndpointResponses, body); err == nil {
		t.Fatal("Claude uploaded file reference was accepted by a cross-format endpoint")
	}
}

func TestEndpointForModelWithPayloadLeavesMalformedAndUnknownSourceRequestsForValidation(t *testing.T) {
	t.Parallel()

	model := capturedCopilotModel(t, "claude-haiku-5.5")
	for _, test := range []struct {
		name         string
		sourceFormat string
		model        upstreamModel
		payload      []byte
		wantEndpoint string
	}{
		{
			name:         "malformed attachment stays on default endpoint",
			sourceFormat: "openai-response",
			model:        model,
			payload:      []byte(`{"input":[{"role":"user","content":[{"type":"input_image","detail":"high"}]}]}`),
			wantEndpoint: translate.EndpointMessages,
		},
		{
			name:         "unknown source format gets no compatibility fallback",
			sourceFormat: "custom-source",
			model: func() upstreamModel {
				model := model
				model.SupportedEndpoints = []string{translate.EndpointResponses, translate.EndpointMessages}
				return model
			}(),
			payload:      capturedResponsesImageRequest(t, "high"),
			wantEndpoint: translate.EndpointResponses,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, storage, _ := serviceWithCachedModels(t, []upstreamModel{test.model})
			endpoint, _, _, err := service.endpointForModelWithPayload(context.Background(), "callback", "auth", storage, model.ID, test.sourceFormat, test.payload)
			if err != nil || endpoint != test.wantEndpoint {
				t.Fatalf("endpoint = %q, error=%v; want original endpoint %q", endpoint, err, test.wantEndpoint)
			}
			validationErr := translate.ValidateRequestAttachmentsForEndpoint(test.sourceFormat, endpoint, test.payload)
			var detailErr *translate.UnsupportedImageDetailError
			if errors.As(validationErr, &detailErr) {
				t.Fatalf("non-image-detail input was misclassified for fallback: %v", validationErr)
			}
			if test.sourceFormat == "openai-response" && validationErr == nil {
				t.Fatal("malformed attachment unexpectedly passed validation")
			}
		})
	}
}

func TestEndpointForModelWithPayloadKeepsOriginalDetailUnrepresentable(t *testing.T) {
	t.Parallel()

	model := capturedCopilotModel(t, "claude-haiku-5.5")
	service, storage, _ := serviceWithCachedModels(t, []upstreamModel{model})
	body := capturedResponsesImageRequest(t, "original")
	endpoint, _, _, err := service.endpointForModelWithPayload(context.Background(), "callback", "auth", storage, model.ID, "openai-response", body)
	if err != nil || endpoint != translate.EndpointMessages {
		t.Fatalf("original-detail endpoint = %q, error=%v; want no fabricated compatible route", endpoint, err)
	}
	for _, unsupported := range []string{translate.EndpointMessages, translate.EndpointChatCompletions} {
		translationErr := translate.ValidateRequestAttachmentsForEndpoint("openai-response", unsupported, body)
		var detailErr *translate.UnsupportedImageDetailError
		if !errors.As(translationErr, &detailErr) || detailErr.Endpoint != unsupported || detailErr.Detail != "original" {
			t.Fatalf("endpoint %s original-detail validation = %v, want typed incompatibility", unsupported, translationErr)
		}
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
		{ID: "claude-sonnet-5.5", Vendor: "Anthropic", ModelPickerEnabled: boolPointer(true), Policy: &modelPolicy{State: "enabled"}, Capabilities: modelCapabilities{Type: "chat"}, SupportedEndpoints: []string{"/v1/messages"}},
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
	if len(response.Models) != 2 || response.Models[0].ID != "claude-sonnet-5.5" || response.Models[1].ID != "visible" {
		t.Fatalf("exposed models = %#v, want all eligible models", response.Models)
	}
	if storage.GitHubAccessToken == "" {
		t.Fatal("test storage lost its synthetic credential")
	}
}

func TestModelsForAuthRegistersConfiguredOverridesWithoutInventingUnknownMetadata(t *testing.T) {
	t.Parallel()
	models := []upstreamModel{
		{ID: "visible", ModelPickerEnabled: boolPointer(true), Policy: &modelPolicy{State: "enabled"}, Capabilities: modelCapabilities{Type: "chat"}, SupportedEndpoints: []string{translate.EndpointResponses}},
		{
			ID: "Picker.Hidden", Vendor: "Anthropic", Name: "Picker label", Version: "5.5",
			ModelPickerEnabled: boolPointer(false), Policy: &modelPolicy{State: "enabled"},
			Capabilities: modelCapabilities{
				Type: "chat", Limits: modelLimits{MaxPromptTokens: 128, MaxOutputTokens: 32},
				Supports: modelSupports{AdaptiveThinking: true, ReasoningEffort: []string{"low", "high"}},
			},
			SupportedEndpoints: []string{translate.EndpointMessages},
		},
		{ID: "policy-disabled", Policy: &modelPolicy{State: "disabled"}, SupportedEndpoints: []string{translate.EndpointResponses}},
		{ID: "policy-preview", Policy: &modelPolicy{State: "preview"}, SupportedEndpoints: []string{translate.EndpointResponses}},
		{ID: "no-endpoint", Policy: &modelPolicy{State: "enabled"}, Capabilities: modelCapabilities{Type: "chat"}},
	}
	service, _, rawStorage := serviceWithCachedModels(t, models)
	service.config.ModelEndpointOverrides = map[string]string{
		"absent-zed":      translate.EndpointResponses,
		"absent-alpha":    translate.EndpointMessages,
		"visible":         translate.EndpointResponses,
		"picker.hidden":   translate.EndpointMessages,
		"policy-disabled": translate.EndpointResponses,
		"policy-preview":  translate.EndpointResponses,
		"no-endpoint":     translate.EndpointResponses,
	}
	cachedInventory, err := json.Marshal(service.modelEntries["auth"].Models)
	if err != nil {
		t.Fatalf("marshal cached inventory before registration: %v", err)
	}
	response, err := service.ModelsForAuth(context.Background(), "callback", pluginapi.AuthModelRequest{AuthID: "auth", StorageJSON: rawStorage})
	if err != nil {
		t.Fatalf("discover models with explicit overrides: %v", err)
	}
	if response.Provider != providerID {
		t.Fatalf("provider = %q, want %q", response.Provider, providerID)
	}
	wantIDs := []string{"absent-alpha", "absent-zed", "no-endpoint", "Picker.Hidden", "visible"}
	if len(response.Models) != len(wantIDs) {
		t.Fatalf("registered models = %#v, want IDs %#v", response.Models, wantIDs)
	}
	infos := make(map[string]pluginapi.ModelInfo, len(response.Models))
	for index, model := range response.Models {
		if model.ID != wantIDs[index] {
			t.Fatalf("registered model %d = %q, want %q", index, model.ID, wantIDs[index])
		}
		infos[model.ID] = model
	}
	unknown := infos["absent-alpha"]
	if !unknown.UserDefined || unknown.Description != "Configured endpoint override: "+translate.EndpointMessages+". Copilot availability and capabilities are unverified." {
		t.Fatalf("configured absent model info = %#v", unknown)
	}
	if unknown.Object != "" || unknown.Created != 0 || unknown.OwnedBy != "" || unknown.Type != "" || unknown.DisplayName != "" || unknown.Name != "" || unknown.Version != "" || unknown.InputTokenLimit != 0 || unknown.OutputTokenLimit != 0 || unknown.ContextLength != 0 || unknown.MaxCompletionTokens != 0 || unknown.SupportedGenerationMethods != nil || unknown.SupportedParameters != nil || unknown.SupportedInputModalities != nil || unknown.SupportedOutputModalities != nil || unknown.Thinking != nil {
		t.Fatalf("configured absent model invented catalog metadata: %#v", unknown)
	}
	hidden := infos["Picker.Hidden"]
	if hidden.UserDefined || hidden.Name != "Picker.Hidden" || hidden.DisplayName != "Picker label" || hidden.OwnedBy != "Anthropic" || hidden.Version != "5.5" || len(hidden.SupportedGenerationMethods) != 1 || hidden.SupportedGenerationMethods[0] != translate.EndpointMessages || hidden.Thinking == nil || !hidden.Thinking.DynamicAllowed {
		t.Fatalf("explicit override did not preserve hidden catalog metadata: %#v", hidden)
	}
	for _, id := range []string{"policy-disabled", "policy-preview"} {
		if _, exists := infos[id]; exists {
			t.Fatalf("non-enabled model %q was registered despite its override", id)
		}
	}
	updatedInventory, err := json.Marshal(service.modelEntries["auth"].Models)
	if err != nil {
		t.Fatalf("marshal cached inventory after registration: %v", err)
	}
	if string(updatedInventory) != string(cachedInventory) {
		t.Fatalf("model registration changed the cached upstream inventory: %#v", service.modelEntries["auth"].Models)
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

func capturedCopilotModel(t *testing.T, modelID string) upstreamModel {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "current-copilot-catalog", "response.json"))
	if err != nil {
		t.Fatal(err)
	}
	var list modelListResponse
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode captured Copilot catalog: %v", err)
	}
	for _, model := range list.Data {
		if strings.EqualFold(model.ID, modelID) {
			return normalizeModels([]upstreamModel{model})[0]
		}
	}
	t.Fatalf("captured Copilot catalog has no model %q", modelID)
	return upstreamModel{}
}

func capturedResponsesImageRequest(t *testing.T, detail string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "translate", "testdata", "native-attachment-captures", "gpt-png", "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	oldDetail := []byte(`"detail": "auto"`)
	if count := bytes.Count(body, oldDetail); count != 1 {
		t.Fatalf("captured Responses request has %d image detail fields with auto value, want 1", count)
	}
	return bytes.Replace(body, oldDetail, []byte(`"detail": "`+detail+`"`), 1)
}

func boolPointer(value bool) *bool {
	return &value
}
