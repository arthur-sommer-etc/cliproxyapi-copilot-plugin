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
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
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
	ModelPickerEnabled  *bool             `json:"model_picker_enabled"`
	Policy              *modelPolicy      `json:"policy"`
	Preview             bool              `json:"preview"`
	SupportedEndpoints  []string          `json:"supported_endpoints"`
	WarningMessages     []modelMessage    `json:"warning_messages"`
	InformationMessages []modelMessage    `json:"info_messages"`
	Capabilities        modelCapabilities `json:"capabilities"`
}

type modelPolicy struct {
	State string `json:"state"`
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
	Fingerprint      string
	APIBaseURL       string
	ConfigGeneration uint64
	ExpiresAt        time.Time
	Models           []upstreamModel
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
	cfg := s.Config()
	available := filterModels(availableModels(models), cfg.ExcludedModelPrefixes)
	return pluginapi.ModelResponse{Provider: providerID, Models: modelInfos(available)}, nil
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
		if ok && cached.Fingerprint == fingerprint && cached.APIBaseURL == token.APIBaseURL && cached.ConfigGeneration == token.ConfigGeneration && cached.ExpiresAt.After(now) {
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
	cfg, generation := s.configSnapshot()
	if generation != token.ConfigGeneration {
		return nil, copilotTokenEntry{}, fmt.Errorf("Copilot configuration changed during model discovery")
	}
	inventory := normalizeModels(list.Data)

	s.configMu.RLock()
	if s.configGeneration != token.ConfigGeneration {
		s.configMu.RUnlock()
		return nil, copilotTokenEntry{}, fmt.Errorf("Copilot configuration changed during model discovery")
	}
	s.modelMu.Lock()
	s.modelEntries[key] = modelCacheEntry{
		Fingerprint:      fingerprint,
		APIBaseURL:       token.APIBaseURL,
		ConfigGeneration: token.ConfigGeneration,
		ExpiresAt:        now.Add(cfg.modelCacheTTL()),
		Models:           cloneUpstreamModels(inventory),
	}
	s.modelMu.Unlock()
	s.configMu.RUnlock()
	return inventory, token, nil
}

func (s *Service) endpointForModel(ctx context.Context, callbackID, authID string, storage authStorage, modelID, sourceFormat string) (string, upstreamModel, copilotTokenEntry, error) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return "", upstreamModel{}, copilotTokenEntry{}, statusError("invalid_request", "model is required", http.StatusBadRequest)
	}
	if endpoint := s.endpointOverride(modelID); endpoint != "" {
		models, token, errModels := s.models(ctx, callbackID, authID, storage, false)
		if errModels != nil {
			return "", upstreamModel{}, copilotTokenEntry{}, errModels
		}
		for _, model := range models {
			if strings.EqualFold(model.ID, modelID) {
				if !modelPolicyAllowsUse(model.Policy) {
					return "", upstreamModel{}, token, statusError("model_not_found", "Copilot model is not present in the authenticated model catalog", http.StatusNotFound)
				}
				return endpoint, model, token, nil
			}
		}
		return endpoint, upstreamModel{}, token, nil
	}
	models, token, errModels := s.models(ctx, callbackID, authID, storage, false)
	if errModels != nil {
		return "", upstreamModel{}, copilotTokenEntry{}, errModels
	}
	cfg := s.Config()
	for _, model := range filterModels(availableModels(models), cfg.ExcludedModelPrefixes) {
		if strings.EqualFold(model.ID, modelID) {
			endpoint, errEndpoint := selectEndpoint(model, sourceFormat)
			return endpoint, model, token, errEndpoint
		}
	}
	return "", upstreamModel{}, token, statusError("model_not_found", "Copilot model is not present in the authenticated model catalog", http.StatusNotFound)
}

func availableModels(models []upstreamModel) []upstreamModel {
	out := make([]upstreamModel, 0, len(models))
	for _, model := range models {
		if !modelAvailable(model) {
			continue
		}
		out = append(out, model)
	}
	return out
}

func modelAvailable(model upstreamModel) bool {
	if !modelPolicyAllowsUse(model.Policy) || model.ModelPickerEnabled != nil && !*model.ModelPickerEnabled {
		return false
	}
	if modelType := strings.TrimSpace(model.Capabilities.Type); modelType != "" && !strings.EqualFold(modelType, "chat") {
		return false
	}
	return len(normalizeEndpoints(model.SupportedEndpoints)) > 0
}

func modelPolicyAllowsUse(policy *modelPolicy) bool {
	return policy == nil || strings.EqualFold(strings.TrimSpace(policy.State), "enabled")
}

func (s *Service) endpointOverride(modelID string) string {
	return s.Config().ModelEndpointOverrides[strings.ToLower(strings.TrimSpace(modelID))]
}

func selectEndpoint(model upstreamModel, sourceFormat string) (string, error) {
	endpoints := normalizeEndpoints(model.SupportedEndpoints)
	for _, preferred := range endpointPreferences(model, sourceFormat) {
		for _, endpoint := range endpoints {
			if endpoint == preferred {
				return preferred, nil
			}
		}
	}
	return "", statusError("unsupported_model_endpoint", "Copilot model exposes no supported chat endpoint", http.StatusUnprocessableEntity)
}

func endpointPreferences(model upstreamModel, sourceFormat string) []string {
	switch normalizeRequestFormat(sourceFormat) {
	case "claude":
		return []string{translate.EndpointMessages, translate.EndpointResponses, translate.EndpointChatCompletions}
	case "openai":
		return []string{translate.EndpointChatCompletions, translate.EndpointResponses, translate.EndpointMessages}
	case "openai-response":
		if isClaudeFamilyModel(model) {
			return []string{translate.EndpointResponses, translate.EndpointMessages, translate.EndpointChatCompletions}
		}
		return []string{translate.EndpointResponses, translate.EndpointChatCompletions, translate.EndpointMessages}
	default:
		return []string{translate.EndpointResponses, translate.EndpointChatCompletions, translate.EndpointMessages}
	}
}

func isClaudeFamilyModel(model upstreamModel) bool {
	vendor := strings.ToLower(strings.TrimSpace(model.Vendor))
	family := strings.ToLower(strings.TrimSpace(model.Capabilities.Family))
	id := strings.ToLower(strings.TrimSpace(model.ID))
	return vendor == "anthropic" || vendor == "claude" || strings.HasPrefix(family, "claude") || strings.HasPrefix(id, "claude-")
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
		if in[i].ModelPickerEnabled != nil {
			value := *in[i].ModelPickerEnabled
			out[i].ModelPickerEnabled = &value
		}
		if in[i].Policy != nil {
			policy := *in[i].Policy
			out[i].Policy = &policy
		}
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
