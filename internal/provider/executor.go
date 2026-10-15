package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/compact"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/redact"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/sse"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	copilotUserAgent     = "GitHubCopilotChat/0.35.0"
	copilotEditorVersion = "vscode/1.107.0"
	copilotPluginVersion = "copilot-chat/0.35.0"
	copilotIntegrationID = "vscode-chat"
	copilotAPIVersion    = "2025-04-01"
)

type ExecuteRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type HTTPRequest struct {
	pluginapi.ExecutorHTTPRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

func (s *Service) Execute(ctx context.Context, req ExecuteRequest) (pluginapi.ExecutorResponse, error) {
	sourceFormat := normalizeRequestFormat(firstNonEmpty(req.SourceFormat, req.Format))
	if sourceFormat == "" {
		return pluginapi.ExecutorResponse{}, statusError("unsupported_format", fmt.Sprintf("unsupported request format %q", firstNonEmpty(req.SourceFormat, req.Format)), http.StatusUnprocessableEntity)
	}
	translationPayload, errClaudeInput := normalizeClaudeSourceRequest(sourceFormat, req.Payload)
	if errClaudeInput != nil {
		return pluginapi.ExecutorResponse{}, claudeSourceNormalizationStatusError(errClaudeInput)
	}
	storage, errParse := parseStorage(req.StorageJSON)
	if errParse != nil {
		return pluginapi.ExecutorResponse{}, errParse
	}
	v1Compact := strings.EqualFold(strings.TrimSpace(req.Alt), "responses/compact")
	v2Compact := hasCompactionTrigger(req.Payload) || hasCompactionTrigger(req.OriginalRequest)
	if (v1Compact || v2Compact) && sourceFormat != "openai-response" {
		return pluginapi.ExecutorResponse{}, statusError("unsupported_compaction_format", "Responses compaction requires the OpenAI Responses format", http.StatusUnprocessableEntity)
	}
	if (v1Compact || v2Compact) && !s.compactionEnabled(req.Model) {
		return pluginapi.ExecutorResponse{}, statusError("compaction_not_enabled", "Responses compaction is not enabled for this model", http.StatusUnprocessableEntity)
	}
	endpointFormat := sourceFormat
	if v1Compact {
		endpointFormat = "openai-response"
	}
	endpoint, model, token, errEndpoint := s.endpointForModelWithPayload(ctx, req.HostCallbackID, req.AuthID, storage, req.Model, endpointFormat, translationPayload)
	if errEndpoint != nil {
		return pluginapi.ExecutorResponse{}, errEndpoint
	}
	keyMaterials, errKeyMaterial := continuityKeyMaterialsFor(storage, req.AuthID, req.Model, endpoint, token.APIBaseURL)
	if errKeyMaterial != nil {
		return pluginapi.ExecutorResponse{}, statusError("auth_state_refresh_required", "Refresh Copilot authentication before making this request", http.StatusConflict)
	}
	carrierScope := reasoningCarrierScopeFor(req.AuthID, req.Model, endpoint, token.APIBaseURL, keyMaterials)
	translationPayload, errClaudeInput = unwrapRequestReasoningCarriers(sourceFormat, translationPayload, carrierScope)
	if errClaudeInput != nil {
		return pluginapi.ExecutorResponse{}, statusError("reasoning_carrier_error", errClaudeInput.Error(), http.StatusUnprocessableEntity)
	}
	if errValidate := validateReasoningRequestForEndpoint(sourceFormat, translationPayload, endpoint); errValidate != nil {
		return pluginapi.ExecutorResponse{}, statusError("translation_error", errValidate.Error(), http.StatusUnprocessableEntity)
	}
	if (v1Compact || v2Compact) && endpoint != translate.EndpointResponses {
		return pluginapi.ExecutorResponse{}, statusError("unsupported_compaction_endpoint", "Responses compaction requires the Copilot Responses endpoint", http.StatusUnprocessableEntity)
	}
	translationPayload, exclusions, errNativeTools := filterUnrepresentableNativeTools(sourceFormat, endpoint, translationPayload)
	if errNativeTools != nil {
		return pluginapi.ExecutorResponse{}, errNativeTools
	}
	requestBody, errTranslate := translate.RequestForEndpointFrom(sourceFormat, req.Model, translationPayload, false, endpoint)
	if errTranslate != nil {
		return pluginapi.ExecutorResponse{}, translationStatusError(errTranslate)
	}
	if endpoint == translate.EndpointMessages {
		requestBody, errTranslate = normalizeClaudeMessagesRequest(model, sourceFormat, req.Payload, requestBody)
		if errTranslate != nil {
			return pluginapi.ExecutorResponse{}, statusError("translation_error", errTranslate.Error(), http.StatusUnprocessableEntity)
		}
	}
	sessionPayload := req.OriginalRequest
	if len(sessionPayload) == 0 {
		sessionPayload = req.Payload
	}
	sessionID, agentID := protocolSessionIdentity(sessionPayload, req.Headers, req.Metadata)
	scopeKey := protocolScopeKey(req.AuthID, storage, req.Model, token.APIBaseURL, endpoint, sessionID, agentID, token.ConfigGeneration)
	if endpoint == translate.EndpointResponses {
		if sourceFormat == "claude" {
			requestBody = s.restoreReasoningReplay(scopeKey, req.OriginalRequest, requestBody)
		} else if sourceFormat == "openai-response" {
			requestBody = s.restoreNativeResponsesReplay(scopeKey, requestBody)
		}
	}
	compactionRequested := false
	if s.compactionEnabled(req.Model) && endpoint == translate.EndpointResponses {
		if v1Compact {
			requestBody, errTranslate = addCompactionTrigger(requestBody)
			if errTranslate != nil {
				return pluginapi.ExecutorResponse{}, statusError("invalid_compaction_request", errTranslate.Error(), http.StatusBadRequest)
			}
		}
		requestBody, compactionRequested, errTranslate = compact.Prepare(requestBody, keyMaterials.Active, keyMaterials.Legacy)
		if errTranslate != nil {
			return pluginapi.ExecutorResponse{}, statusError("invalid_compaction_request", errTranslate.Error(), http.StatusBadRequest)
		}
	}
	cacheKey := explicitPromptCacheKey(req.OriginalRequest, req.Metadata)
	if cacheKey == "" {
		cacheKey = explicitPromptCacheKey(req.Payload, req.Metadata)
	}
	if cacheKey == "" && s.Config().PromptCacheKey {
		cacheKey = derivedPromptCacheKey(scopeKey)
	}
	if endpoint == translate.EndpointResponses && cacheKey != "" {
		requestBody, errTranslate = setPromptCacheKey(requestBody, cacheKey)
		if errTranslate != nil {
			return pluginapi.ExecutorResponse{}, statusError("translation_error", errTranslate.Error(), http.StatusUnprocessableEntity)
		}
	}
	requestBody, errFinalize := s.finalizeModelRequest(ctx, req.HostCallbackID, endpoint, requestBody)
	if errFinalize != nil {
		return pluginapi.ExecutorResponse{}, errFinalize
	}
	resp, token, errDo := s.doModelRequest(ctx, req.HostCallbackID, req.AuthID, storage, token, endpoint, requestBody, false)
	if errDo != nil {
		return pluginapi.ExecutorResponse{}, errDo
	}
	if compactionRequested {
		body, errComplete := compact.Complete(resp.Body, keyMaterials.Active, keyMaterials.Legacy)
		if errComplete != nil {
			return pluginapi.ExecutorResponse{}, statusError("compaction_error", redact.ErrorBody([]byte(errComplete.Error()), token.Token, storage.GitHubAccessToken), http.StatusBadGateway)
		}
		var metadata map[string]any
		if len(exclusions) > 0 {
			metadata = map[string]any{"copilot_excluded_native_tools": exclusions}
		}
		return pluginapi.ExecutorResponse{Payload: body, Headers: nativeToolResponseHeaders(filterResponseHeaders(resp.Headers), exclusions), Metadata: metadata}, nil
	}
	body, errResponse := translate.ResponseFromEndpoint(ctx, endpoint, sourceFormat, req.Model, req.OriginalRequest, requestBody, resp.Body)
	if errResponse != nil {
		return pluginapi.ExecutorResponse{}, statusError("translation_error", errResponse.Error(), http.StatusBadGateway)
	}
	if endpoint == translate.EndpointChatCompletions {
		body, errResponse = sealResponseReasoningCarriers(sourceFormat, body, carrierScope)
		if errResponse != nil {
			return pluginapi.ExecutorResponse{}, statusError("reasoning_carrier_error", errResponse.Error(), http.StatusBadGateway)
		}
	}
	if endpoint == translate.EndpointResponses {
		s.recordReasoningReplay(scopeKey, resp.Body)
	}
	metadata := map[string]any{"copilot_endpoint": endpoint}
	if !token.ExpiresAt.IsZero() {
		metadata["token_expires_at"] = token.ExpiresAt.UTC().Format(http.TimeFormat)
	}
	if len(exclusions) > 0 {
		metadata["copilot_excluded_native_tools"] = exclusions
	}
	return pluginapi.ExecutorResponse{Payload: body, Headers: nativeToolResponseHeaders(filterResponseHeaders(resp.Headers), exclusions), Metadata: metadata}, nil
}

func (s *Service) ExecuteStream(ctx context.Context, req ExecuteRequest) (http.Header, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(req.StreamID) == "" {
		return nil, statusError("invalid_request", "stream_id is required", http.StatusBadRequest)
	}
	sourceFormat := normalizeRequestFormat(firstNonEmpty(req.SourceFormat, req.Format))
	if sourceFormat == "" {
		return nil, statusError("unsupported_format", fmt.Sprintf("unsupported request format %q", firstNonEmpty(req.SourceFormat, req.Format)), http.StatusUnprocessableEntity)
	}
	translationPayload, errClaudeInput := normalizeClaudeSourceRequest(sourceFormat, req.Payload)
	if errClaudeInput != nil {
		return nil, claudeSourceNormalizationStatusError(errClaudeInput)
	}
	storage, errParse := parseStorage(req.StorageJSON)
	if errParse != nil {
		return nil, errParse
	}
	v1Compact := strings.EqualFold(strings.TrimSpace(req.Alt), "responses/compact")
	v2Compact := hasCompactionTrigger(req.Payload) || hasCompactionTrigger(req.OriginalRequest)
	if (v1Compact || v2Compact) && sourceFormat != "openai-response" {
		return nil, statusError("unsupported_compaction_format", "Responses compaction requires the OpenAI Responses format", http.StatusUnprocessableEntity)
	}
	if (v1Compact || v2Compact) && !s.compactionEnabled(req.Model) {
		return nil, statusError("compaction_not_enabled", "Responses compaction is not enabled for this model", http.StatusUnprocessableEntity)
	}
	if v1Compact || v2Compact {
		response, errExecute := s.Execute(ctx, req)
		if errExecute != nil {
			return nil, errExecute
		}
		frames, errFrames := buildResponsesCompactionFrames(response.Payload)
		if errFrames != nil {
			return nil, statusError("invalid_compaction_response", errFrames.Error(), http.StatusBadGateway)
		}
		if errContext := ctx.Err(); errContext != nil {
			return nil, errContext
		}
		go s.pumpCompactionStream(ctx, req.StreamID, frames)
		headers := cloneHeader(response.Headers)
		headers.Set("Content-Type", "text/event-stream")
		headers.Set("Cache-Control", "no-cache")
		return headers, nil
	}
	endpoint, model, token, errEndpoint := s.endpointForModelWithPayload(ctx, req.HostCallbackID, req.AuthID, storage, req.Model, sourceFormat, translationPayload)
	if errEndpoint != nil {
		return nil, errEndpoint
	}
	keyMaterials, errKeyMaterial := continuityKeyMaterialsFor(storage, req.AuthID, req.Model, endpoint, token.APIBaseURL)
	if errKeyMaterial != nil {
		return nil, statusError("auth_state_refresh_required", "Refresh Copilot authentication before making this request", http.StatusConflict)
	}
	carrierScope := reasoningCarrierScopeFor(req.AuthID, req.Model, endpoint, token.APIBaseURL, keyMaterials)
	translationPayload, errClaudeInput = unwrapRequestReasoningCarriers(sourceFormat, translationPayload, carrierScope)
	if errClaudeInput != nil {
		return nil, statusError("reasoning_carrier_error", errClaudeInput.Error(), http.StatusUnprocessableEntity)
	}
	if errValidate := validateReasoningRequestForEndpoint(sourceFormat, translationPayload, endpoint); errValidate != nil {
		return nil, statusError("translation_error", errValidate.Error(), http.StatusUnprocessableEntity)
	}
	translationPayload, exclusions, errNativeTools := filterUnrepresentableNativeTools(sourceFormat, endpoint, translationPayload)
	if errNativeTools != nil {
		return nil, errNativeTools
	}
	requestBody, errTranslate := translate.RequestForEndpointFrom(sourceFormat, req.Model, translationPayload, true, endpoint)
	if errTranslate != nil {
		return nil, translationStatusError(errTranslate)
	}
	if endpoint == translate.EndpointMessages {
		requestBody, errTranslate = normalizeClaudeMessagesRequest(model, sourceFormat, req.Payload, requestBody)
		if errTranslate != nil {
			return nil, statusError("translation_error", errTranslate.Error(), http.StatusUnprocessableEntity)
		}
	}
	sessionPayload := req.OriginalRequest
	if len(sessionPayload) == 0 {
		sessionPayload = req.Payload
	}
	sessionID, agentID := protocolSessionIdentity(sessionPayload, req.Headers, req.Metadata)
	scopeKey := protocolScopeKey(req.AuthID, storage, req.Model, token.APIBaseURL, endpoint, sessionID, agentID, token.ConfigGeneration)
	if endpoint == translate.EndpointResponses {
		if sourceFormat == "claude" {
			requestBody = s.restoreReasoningReplay(scopeKey, req.OriginalRequest, requestBody)
		} else if sourceFormat == "openai-response" {
			requestBody = s.restoreNativeResponsesReplay(scopeKey, requestBody)
		}
	}
	if s.compactionEnabled(req.Model) && endpoint == translate.EndpointResponses {
		requestBody, _, errTranslate = compact.Prepare(requestBody, keyMaterials.Active, keyMaterials.Legacy)
		if errTranslate != nil {
			return nil, statusError("invalid_compaction_request", errTranslate.Error(), http.StatusBadRequest)
		}
	}
	cacheKey := explicitPromptCacheKey(req.OriginalRequest, req.Metadata)
	if cacheKey == "" {
		cacheKey = explicitPromptCacheKey(req.Payload, req.Metadata)
	}
	if cacheKey == "" && s.Config().PromptCacheKey {
		cacheKey = derivedPromptCacheKey(scopeKey)
	}
	if endpoint == translate.EndpointResponses && cacheKey != "" {
		requestBody, errTranslate = setPromptCacheKey(requestBody, cacheKey)
		if errTranslate != nil {
			return nil, statusError("translation_error", errTranslate.Error(), http.StatusUnprocessableEntity)
		}
	}
	requestBody, errFinalize := s.finalizeModelRequest(ctx, req.HostCallbackID, endpoint, requestBody)
	if errFinalize != nil {
		return nil, errFinalize
	}
	upstream, token, errOpen := s.openModelStream(ctx, req.HostCallbackID, req.AuthID, storage, token, endpoint, requestBody)
	if errOpen != nil {
		return nil, errOpen
	}
	if upstream.StatusCode < 200 || upstream.StatusCode >= 300 {
		body, errCollect := s.collectStreamError(ctx, upstream, token.Token, storage.GitHubAccessToken)
		if errCollect != nil {
			return nil, errCollect
		}
		return nil, upstreamStatusError(upstream.StatusCode, redact.ErrorBody(body, token.Token, storage.GitHubAccessToken))
	}
	go s.pumpStream(ctx, req.StreamID, endpoint, sourceFormat, req.Model, req.OriginalRequest, requestBody, upstream, scopeKey, carrierScope, token.Token, storage.GitHubAccessToken)
	headers := nativeToolResponseHeaders(filterResponseHeaders(upstream.Headers), exclusions)
	headers.Set("Content-Type", "text/event-stream")
	headers.Set("Cache-Control", "no-cache")
	return headers, nil
}

func (s *Service) pumpCompactionStream(ctx context.Context, outputID string, frames [][]byte) {
	terminalError := ""
	defer func() { s.host.CloseOutput(context.WithoutCancel(ctx), outputID, terminalError) }()
	for _, frame := range frames {
		if ctx.Err() != nil {
			terminalError = "Responses compaction stream canceled"
			return
		}
		if err := s.host.Emit(ctx, outputID, frame); err != nil {
			terminalError = "Responses compaction stream emit failed"
			return
		}
	}
}

func (s *Service) doModelRequest(ctx context.Context, callbackID, authID string, storage authStorage, token copilotTokenEntry, endpoint string, body []byte, stream bool) (transport.Response, copilotTokenEntry, error) {
	request := transport.Request{
		Method:  http.MethodPost,
		URL:     token.APIBaseURL + endpoint,
		Headers: copilotRequestHeaders(token, stream),
		Body:    body,
	}
	resp, errDo := s.host.Do(ctx, callbackID, request)
	if errDo != nil {
		return transport.Response{}, token, fmt.Errorf("call Copilot model endpoint: %w", errDo)
	}
	if effectiveAuthMode(token.Mode) == authModeDirectOAuth && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
		s.invalidateAuthForCredential(authID, token.Fingerprint)
	} else if effectiveAuthMode(token.Mode) != authModeDirectOAuth && resp.StatusCode == http.StatusUnauthorized {
		s.invalidateAuth(authID)
		refreshed, errToken := s.copilotToken(ctx, callbackID, authID, storage)
		if errToken != nil {
			return transport.Response{}, token, errToken
		}
		if !sameCopilotAPIBaseURL(token.APIBaseURL, refreshed.APIBaseURL) {
			return transport.Response{}, refreshed, statusError("copilot_origin_changed", "Copilot API origin changed after token refresh", http.StatusConflict)
		}
		token = refreshed
		request.URL = token.APIBaseURL + endpoint
		request.Headers = copilotRequestHeaders(token, stream)
		resp, errDo = s.host.Do(ctx, callbackID, request)
		if errDo != nil {
			return transport.Response{}, token, fmt.Errorf("call Copilot model endpoint after token refresh: %w", errDo)
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, token, upstreamStatusError(resp.StatusCode, redact.ErrorBody(resp.Body, token.Token, storage.GitHubAccessToken))
	}
	return resp, token, nil
}

func (s *Service) openModelStream(ctx context.Context, callbackID, authID string, storage authStorage, token copilotTokenEntry, endpoint string, body []byte) (transport.Stream, copilotTokenEntry, error) {
	request := transport.Request{
		Method:  http.MethodPost,
		URL:     token.APIBaseURL + endpoint,
		Headers: copilotRequestHeaders(token, true),
		Body:    body,
	}
	stream, errOpen := s.host.OpenStream(ctx, callbackID, request)
	if errOpen != nil {
		return transport.Stream{}, token, fmt.Errorf("open Copilot model stream: %w", errOpen)
	}
	if effectiveAuthMode(token.Mode) == authModeDirectOAuth && (stream.StatusCode == http.StatusUnauthorized || stream.StatusCode == http.StatusForbidden) {
		s.invalidateAuthForCredential(authID, token.Fingerprint)
	} else if effectiveAuthMode(token.Mode) != authModeDirectOAuth && stream.StatusCode == http.StatusUnauthorized {
		_ = s.host.CloseStream(ctx, stream.ID)
		s.invalidateAuth(authID)
		refreshed, errToken := s.copilotToken(ctx, callbackID, authID, storage)
		if errToken != nil {
			return transport.Stream{}, token, errToken
		}
		if !sameCopilotAPIBaseURL(token.APIBaseURL, refreshed.APIBaseURL) {
			return transport.Stream{}, refreshed, statusError("copilot_origin_changed", "Copilot API origin changed after token refresh", http.StatusConflict)
		}
		token = refreshed
		request.URL = token.APIBaseURL + endpoint
		request.Headers = copilotRequestHeaders(token, true)
		stream, errOpen = s.host.OpenStream(ctx, callbackID, request)
		if errOpen != nil {
			return transport.Stream{}, token, fmt.Errorf("open Copilot model stream after token refresh: %w", errOpen)
		}
	}
	return stream, token, nil
}

func sameCopilotAPIBaseURL(left, right string) bool {
	return strings.TrimRight(strings.TrimSpace(left), "/") == strings.TrimRight(strings.TrimSpace(right), "/")
}

func (s *Service) collectStreamError(ctx context.Context, stream transport.Stream, copilotToken, githubToken string) ([]byte, error) {
	defer func() { _ = s.host.CloseStream(context.WithoutCancel(ctx), stream.ID) }()
	var body []byte
	for {
		chunk, errRead := s.host.ReadStream(ctx, stream.ID)
		if errRead != nil {
			return nil, fmt.Errorf("read Copilot error stream: %s", redact.ErrorBody([]byte(errRead.Error()), copilotToken, githubToken))
		}
		if chunk.Error != "" {
			return nil, fmt.Errorf("read Copilot error stream: %s", redact.ErrorBody([]byte(chunk.Error), copilotToken, githubToken))
		}
		const maxUpstreamErrorStreamBytes = 1 << 20
		remaining := maxUpstreamErrorStreamBytes - len(body)
		if remaining > 0 {
			if len(chunk.Payload) > remaining {
				body = append(body, chunk.Payload[:remaining]...)
				return body, nil
			}
			body = append(body, chunk.Payload...)
			if len(body) == maxUpstreamErrorStreamBytes {
				return body, nil
			}
		}
		if chunk.Done {
			return body, nil
		}
	}
}

func (s *Service) pumpStream(ctx context.Context, outputID, endpoint, destination, model string, original, translated []byte, upstream transport.Stream, scopeKey string, carrierScope reasoningCarrierScope, copilotToken, githubToken string) {
	var terminalErr error
	var terminal streamTerminal
	defer func() {
		_ = s.host.CloseStream(context.WithoutCancel(ctx), upstream.ID)
		message := ""
		if terminalErr != nil {
			message = redact.ErrorBody([]byte(terminalErr.Error()), copilotToken, githubToken)
		}
		s.host.CloseOutput(context.WithoutCancel(ctx), outputID, message)
	}()
	decoder := &sse.Decoder{}
	var state any
	emit := func(frame []byte) error {
		frames, errTranslate := translate.StreamFromEndpoint(ctx, endpoint, destination, model, original, translated, frame, &state)
		if errTranslate != nil {
			return errTranslate
		}
		for _, output := range frames {
			if len(output) == 0 {
				continue
			}
			if endpoint == translate.EndpointChatCompletions {
				sealed, errSeal := sealReasoningCarrierSSEFrame(destination, output, carrierScope)
				if errSeal != nil {
					return errSeal
				}
				output = sealed
			}
			if destination == "openai" {
				chunk, shouldEmit, errUnwrap := chatClientChunk(output)
				if errUnwrap != nil {
					return errUnwrap
				}
				if !shouldEmit {
					continue
				}
				output = chunk
			}
			if errEmit := s.host.Emit(ctx, outputID, output); errEmit != nil {
				return errEmit
			}
		}
		return nil
	}
	for {
		if errContext := ctx.Err(); errContext != nil {
			terminalErr = fmt.Errorf("Copilot stream canceled: %w", errContext)
			return
		}
		chunk, errRead := s.host.ReadStream(ctx, upstream.ID)
		if errRead != nil {
			terminalErr = fmt.Errorf("read Copilot stream: %s", redact.ErrorBody([]byte(errRead.Error()), copilotToken, githubToken))
			return
		}
		if chunk.Error != "" {
			terminalErr = fmt.Errorf("Copilot stream error: %s", redact.ErrorBody([]byte(chunk.Error), copilotToken, githubToken))
			return
		}
		for _, frame := range decoder.Feed(chunk.Payload) {
			done, errTerminal := terminal.observe(endpoint, frame, copilotToken, githubToken)
			if errTerminal != nil {
				terminalErr = errTerminal
				return
			}
			if errEmit := emit(frame); errEmit != nil {
				terminalErr = fmt.Errorf("translate Copilot stream: %s", redact.ErrorBody([]byte(errEmit.Error()), copilotToken, githubToken))
				return
			}
			if done {
				if endpoint == translate.EndpointResponses && terminal.completed {
					s.recordReasoningReplay(scopeKey, terminal.response)
				}
				return
			}
		}
		if chunk.Done {
			if trailing := decoder.Flush(); len(trailing) > 0 {
				done, errTerminal := terminal.observe(endpoint, trailing, copilotToken, githubToken)
				if errTerminal != nil {
					terminalErr = errTerminal
					return
				}
				if errEmit := emit(trailing); errEmit != nil {
					terminalErr = fmt.Errorf("translate final Copilot stream frame: %s", redact.ErrorBody([]byte(errEmit.Error()), copilotToken, githubToken))
					return
				}
				if done {
					if endpoint == translate.EndpointResponses && terminal.completed {
						s.recordReasoningReplay(scopeKey, terminal.response)
					}
					return
				}
			}
			if !terminal.completed {
				terminalErr = fmt.Errorf("Copilot stream ended without a successful terminal event")
				return
			}
			return
		}
	}
}

func normalizeRequestFormat(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "responses", "openai-response", "openai-responses":
		return "openai-response"
	case "openai", "chat", "chat-completions", "chat/completions":
		return "openai"
	case "claude", "anthropic":
		return "claude"
	default:
		return ""
	}
}

func (s *Service) HTTP(ctx context.Context, req HTTPRequest) (pluginapi.ExecutorHTTPResponse, error) {
	storage, errParse := parseStorage(req.StorageJSON)
	if errParse != nil {
		return pluginapi.ExecutorHTTPResponse{}, errParse
	}
	token, errToken := s.copilotToken(ctx, req.HostCallbackID, req.AuthID, storage)
	if errToken != nil {
		return pluginapi.ExecutorHTTPResponse{}, errToken
	}
	target, errURL := url.Parse(strings.TrimSpace(req.URL))
	if errURL != nil || target.Hostname() == "" {
		return pluginapi.ExecutorHTTPResponse{}, statusError("invalid_request", "executor HTTP URL is invalid", http.StatusBadRequest)
	}
	base, _ := url.Parse(token.APIBaseURL)
	if !strings.EqualFold(target.Scheme, base.Scheme) || !strings.EqualFold(target.Host, base.Host) {
		return pluginapi.ExecutorHTTPResponse{}, statusError("forbidden_url", "executor HTTP URL is outside the authenticated Copilot API origin", http.StatusForbidden)
	}
	headers := executorHTTPHeaders(token, req.Headers)
	resp, errDo := s.host.Do(ctx, req.HostCallbackID, transport.Request{
		Method:  req.Method,
		URL:     target.String(),
		Headers: headers,
		Body:    append([]byte(nil), req.Body...),
	})
	if errDo != nil {
		return pluginapi.ExecutorHTTPResponse{}, errDo
	}
	if effectiveAuthMode(token.Mode) == authModeDirectOAuth && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
		s.invalidateAuthForCredential(req.AuthID, token.Fingerprint)
	}
	return pluginapi.ExecutorHTTPResponse{
		StatusCode: resp.StatusCode,
		Headers:    filterResponseHeaders(resp.Headers),
		Body:       resp.Body,
	}, nil
}

func copilotHeaders(token string, stream bool) http.Header {
	requestID, _ := randomIdentifier(16)
	accept := "application/json"
	if stream {
		accept = "text/event-stream"
	}
	headers := http.Header{}
	headers.Set("Accept", accept)
	headers.Set("Authorization", "Bearer "+token)
	headers.Set("Content-Type", "application/json")
	headers.Set("Copilot-Integration-Id", copilotIntegrationID)
	headers.Set("Editor-Plugin-Version", copilotPluginVersion)
	headers.Set("Editor-Version", copilotEditorVersion)
	headers.Set("OpenAI-Intent", "conversation-edits")
	headers.Set("User-Agent", copilotUserAgent)
	headers.Set("X-Agent-Task-Id", requestID)
	headers.Set("X-GitHub-Api-Version", copilotAPIVersion)
	headers.Set("X-Initiator", "user")
	headers.Set("X-Interaction-Type", "conversation-edits")
	headers.Set("X-Request-Id", requestID)
	return headers
}

func copilotRequestHeaders(token copilotTokenEntry, stream bool) http.Header {
	if effectiveAuthMode(token.Mode) != authModeDirectOAuth {
		return copilotHeaders(token.Token, stream)
	}
	accept := "application/json"
	if stream {
		accept = "text/event-stream"
	}
	headers := http.Header{}
	headers.Set("Accept", accept)
	headers.Set("Authorization", "Bearer "+token.Token)
	headers.Set("Content-Type", "application/json")
	headers.Set("User-Agent", userAgent())
	headers.Set("X-GitHub-Api-Version", copilotAPIVersion)
	return headers
}

func executorHTTPHeaders(token copilotTokenEntry, requestHeaders http.Header) http.Header {
	headers := cloneHeader(requestHeaders)
	headers.Set("Authorization", "Bearer "+token.Token)
	if effectiveAuthMode(token.Mode) != authModeDirectOAuth {
		headers.Set("User-Agent", copilotUserAgent)
		headers.Set("X-GitHub-Api-Version", copilotAPIVersion)
		return headers
	}
	for _, name := range []string{
		"Copilot-Integration-Id",
		"Editor-Plugin-Version",
		"Editor-Version",
		"OpenAI-Intent",
		"X-Agent-Task-Id",
		"X-Initiator",
		"X-Interaction-Type",
		"X-Request-Id",
	} {
		headers.Del(name)
	}
	headers.Set("User-Agent", userAgent())
	headers.Set("X-GitHub-Api-Version", copilotAPIVersion)
	return headers
}

func filterResponseHeaders(headers http.Header) http.Header {
	out := http.Header{}
	for key, values := range headers {
		switch strings.ToLower(key) {
		case "content-type", "cache-control", "retry-after", "x-github-request-id", "x-request-id":
			out[key] = append([]string(nil), values...)
		}
	}
	return out
}

func cloneHeader(headers http.Header) http.Header {
	if len(headers) == 0 {
		return http.Header{}
	}
	return headers.Clone()
}

func claudeSourceNormalizationStatusError(err error) error {
	if errors.Is(err, errClaudeSafeguardsUnsupported) {
		return statusError("unsupported_safeguards", errClaudeSafeguardsUnsupported.Error(), http.StatusBadRequest)
	}
	return statusError("translation_error", err.Error(), http.StatusUnprocessableEntity)
}
