package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/redact"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type modelListResponse struct {
	Data []upstreamModel `json:"data"`
}

type upstreamModel struct {
	ID                  string            `json:"id"`
	Vendor              string            `json:"vendor"`
	Name                string            `json:"name"`
	Version             string            `json:"version"`
	Object              string            `json:"object"`
	ModelPickerEnabled  bool              `json:"model_picker_enabled"`
	Preview             bool              `json:"preview"`
	SupportedEndpoints  []string          `json:"supported_endpoints"`
	WarningMessages     []modelMessage    `json:"warning_messages"`
	InformationMessages []modelMessage    `json:"info_messages"`
	Capabilities        modelCapabilities `json:"capabilities"`
}

type modelMessage struct {
	Message string `json:"message"`
}

type modelCapabilities struct {
	Type      string        `json:"type"`
	Tokenizer string        `json:"tokenizer"`
	Family    string        `json:"family"`
	Object    string        `json:"object"`
	Supports  modelSupports `json:"supports"`
	Limits    modelLimits   `json:"limits"`
}

type modelSupports struct {
	ToolCalls         bool     `json:"tool_calls"`
	ParallelToolCalls bool     `json:"parallel_tool_calls"`
	Streaming         bool     `json:"streaming"`
	Vision            bool     `json:"vision"`
	AdaptiveThinking  bool     `json:"adaptive_thinking"`
	ReasoningEffort   []string `json:"reasoning_effort"`
}

type modelLimits struct {
	MaxInputs                   int64 `json:"max_inputs"`
	MaxPromptTokens             int64 `json:"max_prompt_tokens"`
	MaxOutputTokens             int64 `json:"max_output_tokens"`
	MaxNonStreamingOutputTokens int64 `json:"max_non_streaming_output_tokens"`
	MaxContextWindowTokens      int64 `json:"max_context_window_tokens"`
}

type modelCacheEntry struct {
	Fingerprint string
	APIBaseURL  string
	ExpiresAt   time.Time
	Models      []upstreamModel
	ByID        map[string]upstreamModel
}

func (s *Service) StaticModels() pluginapi.ModelResponse {
	return pluginapi.ModelResponse{Provider: providerID, Models: []pluginapi.ModelInfo{}}
}

func (s *Service) ModelsForAuth(ctx context.Context, callbackID string, req pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
	storage, errParse := parseStorage(req.StorageJSON)
	if errParse != nil {
		return pluginapi.ModelResponse{}, errParse
	}
	models, _, errModels := s.models(ctx, callbackID, req.AuthID, storage, false)
	if errModels != nil {
		return pluginapi.ModelResponse{}, errModels
	}
	return pluginapi.ModelResponse{Provider: providerID, Models: modelInfos(models)}, nil
}

func (s *Service) models(ctx context.Context, callbackID, authID string, storage authStorage, force bool) ([]upstreamModel, copilotTokenEntry, error) {
	token, errToken := s.copilotToken(ctx, callbackID, authID, storage)
	if errToken != nil {
		return nil, copilotTokenEntry{}, errToken
	}
	fingerprint := tokenFingerprint(storage.GitHubAccessToken)
	key := strings.TrimSpace(authID)
	if key == "" {
		key = fingerprint
	}
	now := s.now()
	if !force {
		s.modelMu.Lock()
		cached, ok := s.modelEntries[key]
		s.modelMu.Unlock()
		if ok && cached.Fingerprint == fingerprint && cached.APIBaseURL == token.APIBaseURL && cached.ExpiresAt.After(now) {
			return cloneUpstreamModels(cached.Models), token, nil
		}
	}

	resp, errDo := s.host.Do(ctx, callbackID, transport.Request{
		Method:  http.MethodGet,
		URL:     token.APIBaseURL + "/models",
		Headers: copilotHeaders(token.Token, false),
	})
	if errDo != nil {
		return nil, copilotTokenEntry{}, fmt.Errorf("discover Copilot models: %w", errDo)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		s.invalidateAuth(authID)
		token, errToken = s.copilotToken(ctx, callbackID, authID, storage)
		if errToken != nil {
			return nil, copilotTokenEntry{}, errToken
		}
		resp, errDo = s.host.Do(ctx, callbackID, transport.Request{
			Method:  http.MethodGet,
			URL:     token.APIBaseURL + "/models",
			Headers: copilotHeaders(token.Token, false),
		})
		if errDo != nil {
			return nil, copilotTokenEntry{}, fmt.Errorf("discover Copilot models after token refresh: %w", errDo)
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, copilotTokenEntry{}, upstreamStatusError(resp.StatusCode, redact.ErrorBody(resp.Body, token.Token, storage.GitHubAccessToken))
	}
	var list modelListResponse
	if errUnmarshal := json.Unmarshal(resp.Body, &list); errUnmarshal != nil {
		return nil, copilotTokenEntry{}, fmt.Errorf("decode Copilot models response: %w", errUnmarshal)
	}
	cleaned := filterModels(normalizeModels(list.Data), s.Config().ExcludedModelPrefixes)
	if len(cleaned) == 0 {
		return nil, copilotTokenEntry{}, fmt.Errorf("Copilot models endpoint returned no usable models")
	}

	byID := make(map[string]upstreamModel, len(cleaned))
	for _, model := range cleaned {
		byID[strings.ToLower(model.ID)] = model
	}
	s.modelMu.Lock()
	s.modelEntries[key] = modelCacheEntry{
		Fingerprint: fingerprint,
		APIBaseURL:  token.APIBaseURL,
		ExpiresAt:   now.Add(s.Config().modelCacheTTL()),
		Models:      cloneUpstreamModels(cleaned),
		ByID:        byID,
	}
	s.modelMu.Unlock()
	return cleaned, token, nil
}

func (s *Service) endpointForModel(ctx context.Context, callbackID, authID string, storage authStorage, modelID string) (string, copilotTokenEntry, error) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return "", copilotTokenEntry{}, statusError("invalid_request", "model is required", http.StatusBadRequest)
	}
	models, token, errModels := s.models(ctx, callbackID, authID, storage, false)
	if errModels != nil {
		if endpoint, ok := specialResponsesModel(modelID); ok {
			token, errToken := s.copilotToken(ctx, callbackID, authID, storage)
			return endpoint, token, errToken
		}
		return "", copilotTokenEntry{}, errModels
	}
	for _, model := range models {
		if strings.EqualFold(model.ID, modelID) {
			endpoint, errEndpoint := selectEndpoint(model)
			return endpoint, token, errEndpoint
		}
	}
	if endpoint, ok := specialResponsesModel(modelID); ok {
		return endpoint, token, nil
	}
	return "", token, statusError("model_not_found", "Copilot model is not present in the authenticated model catalog", http.StatusNotFound)
}

func selectEndpoint(model upstreamModel) (string, error) {
	if endpoint, ok := specialResponsesModel(model.ID); ok {
		return endpoint, nil
	}
	endpoints := normalizeEndpoints(model.SupportedEndpoints)
	for _, preferred := range []string{translate.EndpointResponses, translate.EndpointChatCompletions, translate.EndpointMessages} {
		for _, endpoint := range endpoints {
			if endpoint == preferred {
				return preferred, nil
			}
		}
	}
	return "", statusError("unsupported_model_endpoint", "Copilot model exposes no supported chat endpoint", http.StatusUnprocessableEntity)
}

func specialResponsesModel(modelID string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(modelID)) {
	case "gpt-5.6-sol", "gpt-5.6-terra":
		return translate.EndpointResponses, true
	default:
		return "", false
	}
}

func normalizeModels(models []upstreamModel) []upstreamModel {
	seen := make(map[string]struct{}, len(models))
	out := make([]upstreamModel, 0, len(models))
	for _, model := range models {
		model.ID = strings.TrimSpace(model.ID)
		if model.ID == "" {
			continue
		}
		key := strings.ToLower(model.ID)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		model.Name = strings.TrimSpace(model.Name)
		model.Vendor = strings.TrimSpace(model.Vendor)
		model.Version = strings.TrimSpace(model.Version)
		model.Object = strings.TrimSpace(model.Object)
		model.SupportedEndpoints = normalizeEndpoints(model.SupportedEndpoints)
		if endpoint, ok := specialResponsesModel(model.ID); ok && !contains(model.SupportedEndpoints, endpoint) {
			model.SupportedEndpoints = append(model.SupportedEndpoints, endpoint)
		}
		out = append(out, model)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].ID) < strings.ToLower(out[j].ID)
	})
	return out
}

func filterModels(models []upstreamModel, excludedPrefixes []string) []upstreamModel {
	if len(excludedPrefixes) == 0 {
		return models
	}
	out := make([]upstreamModel, 0, len(models))
	for _, model := range models {
		modelID := strings.ToLower(model.ID)
		excluded := false
		for _, prefix := range excludedPrefixes {
			if strings.HasPrefix(modelID, prefix) {
				excluded = true
				break
			}
		}
		if !excluded {
			out = append(out, model)
		}
	}
	return out
}

func normalizeEndpoints(endpoints []string) []string {
	seen := make(map[string]struct{}, len(endpoints))
	out := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		switch strings.ToLower(strings.TrimSpace(endpoint)) {
		case "/responses", "responses", "openai-responses":
			endpoint = translate.EndpointResponses
		case "/chat/completions", "chat", "chat-completions":
			endpoint = translate.EndpointChatCompletions
		case "/v1/messages", "messages", "anthropic":
			endpoint = translate.EndpointMessages
		default:
			continue
		}
		if _, exists := seen[endpoint]; exists {
			continue
		}
		seen[endpoint] = struct{}{}
		out = append(out, endpoint)
	}
	return out
}

func modelInfos(models []upstreamModel) []pluginapi.ModelInfo {
	out := make([]pluginapi.ModelInfo, 0, len(models))
	for _, model := range models {
		owner := model.Vendor
		if owner == "" {
			owner = "github-copilot"
		}
		displayName := model.Name
		if displayName == "" {
			displayName = model.ID
		}
		contextLength := model.Capabilities.Limits.MaxContextWindowTokens
		if contextLength == 0 {
			contextLength = model.Capabilities.Limits.MaxPromptTokens + model.Capabilities.Limits.MaxOutputTokens
		}
		parameters := []string{}
		if model.Capabilities.Supports.Streaming {
			parameters = append(parameters, "stream")
		}
		if model.Capabilities.Supports.ToolCalls {
			parameters = append(parameters, "tools", "tool_choice")
		}
		if model.Capabilities.Supports.ParallelToolCalls {
			parameters = append(parameters, "parallel_tool_calls")
		}
		if len(model.Capabilities.Supports.ReasoningEffort) > 0 {
			parameters = append(parameters, "reasoning_effort")
		}
		inputModalities := []string{"TEXT"}
		if model.Capabilities.Supports.Vision {
			inputModalities = append(inputModalities, "IMAGE")
		}
		var thinking *pluginapi.ThinkingSupport
		if model.Capabilities.Supports.AdaptiveThinking || len(model.Capabilities.Supports.ReasoningEffort) > 0 {
			thinking = &pluginapi.ThinkingSupport{
				DynamicAllowed: model.Capabilities.Supports.AdaptiveThinking,
				Levels:         append([]string(nil), model.Capabilities.Supports.ReasoningEffort...),
			}
		}
		out = append(out, pluginapi.ModelInfo{
			ID:                         model.ID,
			Object:                     firstNonEmpty(model.Object, "model"),
			OwnedBy:                    owner,
			Type:                       firstNonEmpty(model.Capabilities.Type, "chat"),
			DisplayName:                displayName,
			Name:                       model.ID,
			Version:                    model.Version,
			Description:                modelDescription(model),
			InputTokenLimit:            model.Capabilities.Limits.MaxPromptTokens,
			OutputTokenLimit:           model.Capabilities.Limits.MaxOutputTokens,
			SupportedGenerationMethods: append([]string(nil), model.SupportedEndpoints...),
			ContextLength:              contextLength,
			MaxCompletionTokens:        model.Capabilities.Limits.MaxOutputTokens,
			SupportedParameters:        parameters,
			SupportedInputModalities:   inputModalities,
			SupportedOutputModalities:  []string{"TEXT"},
			Thinking:                   thinking,
		})
	}
	return out
}

func modelDescription(model upstreamModel) string {
	for _, item := range append(model.WarningMessages, model.InformationMessages...) {
		if message := strings.TrimSpace(item.Message); message != "" {
			return message
		}
	}
	parts := []string{"GitHub Copilot subscription model"}
	if family := strings.TrimSpace(model.Capabilities.Family); family != "" {
		parts = append(parts, "family "+family)
	}
	if len(model.SupportedEndpoints) > 0 {
		parts = append(parts, "endpoints "+strings.Join(model.SupportedEndpoints, ", "))
	}
	return strings.Join(parts, "; ")
}

func cloneUpstreamModels(in []upstreamModel) []upstreamModel {
	out := make([]upstreamModel, len(in))
	copy(out, in)
	for i := range out {
		out[i].SupportedEndpoints = append([]string(nil), in[i].SupportedEndpoints...)
		out[i].Capabilities.Supports.ReasoningEffort = append([]string(nil), in[i].Capabilities.Supports.ReasoningEffort...)
		out[i].WarningMessages = append([]modelMessage(nil), in[i].WarningMessages...)
		out[i].InformationMessages = append([]modelMessage(nil), in[i].InformationMessages...)
	}
	return out
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
