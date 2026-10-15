package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator/builtin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	EndpointResponses       = "/responses"
	EndpointChatCompletions = "/chat/completions"
	EndpointMessages        = "/v1/messages"
)

var registry = builtin.Registry()

func ClaudeToResponses(model string, body []byte, stream bool) ([]byte, error) {
	return request(sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse, model, body, stream)
}

func ResponsesToClaude(ctx context.Context, model string, original, translated, body []byte) ([]byte, error) {
	return response(ctx, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, model, original, translated, body)
}

func RequestForEndpoint(model string, body []byte, stream bool, endpoint string) ([]byte, error) {
	return RequestForEndpointFrom(sdktranslator.FormatOpenAIResponse.String(), model, body, stream, endpoint)
}

func RequestForEndpointFrom(source, model string, body []byte, stream bool, endpoint string) ([]byte, error) {
	from := sdktranslator.FromString(source)
	to, err := endpointFormat(endpoint)
	if err != nil {
		return nil, err
	}
	if err := rejectMismatchedCopilotCarrier(from, to, model, body); err != nil {
		return nil, err
	}
	if from == sdktranslator.FormatOpenAI && from != to {
		root, err := decodeObject(body)
		if err != nil {
			return nil, fmt.Errorf("decode Chat request: %w", err)
		}
		if _, offered := root["web_search_options"]; offered {
			return nil, fmt.Errorf("Chat hosted web search cannot be represented through %s: native hosted state, results and citations have no lossless Chat response/history representation", endpoint)
		}
	}
	if from != to && from != sdktranslator.FormatOpenAIResponse {
		if err := validateRequestAttachments(body, from, to); err != nil {
			return nil, err
		}
	}
	if from == sdktranslator.FormatOpenAIResponse && from != to {
		if err := validateResponsesRequestForTarget(body, to); err != nil {
			return nil, err
		}
	}
	if from == to && to == sdktranslator.FormatOpenAIResponse && containsCacheControl(body) {
		return nil, fmt.Errorf("Responses endpoint does not accept cache_control fields")
	}
	var out []byte
	var opaqueSource []byte
	if from == sdktranslator.FormatOpenAI && to == sdktranslator.FormatOpenAIResponse {
		out, err = chatRequestToResponses(model, body, stream)
	} else if from == sdktranslator.FormatOpenAI && to == sdktranslator.FormatClaude {
		out, err = chatRequestToClaude(model, body, stream)
	} else if from == sdktranslator.FormatClaude && to == sdktranslator.FormatOpenAI {
		root, decodeErr := decodeObject(body)
		if decodeErr != nil {
			return nil, fmt.Errorf("decode Claude request: %w", decodeErr)
		}
		stopSequences, stopErr := claudeStopSequences(root)
		if stopErr != nil {
			return nil, stopErr
		}
		chatSource := body
		if len(stopSequences) > 0 {
			chatSource, err = sjson.DeleteBytes(body, "stop_sequences")
			if err != nil {
				return nil, fmt.Errorf("remove Claude stop sequences before Responses translation: %w", err)
			}
		}
		var intermediate []byte
		intermediate, err = claudeRequestToResponses(model, chatSource, stream)
		if err == nil {
			err = validateResponsesRequestForTarget(intermediate, to)
			if err == nil {
				out, err = requestFromResponsesToChat(model, intermediate, stream)
			}
			if err == nil && len(stopSequences) > 0 {
				out, err = sjson.SetBytes(out, "stop", stopSequences)
				if err != nil {
					return nil, fmt.Errorf("set translated Chat stop sequences: %w", err)
				}
			}
			opaqueSource = intermediate
		}
	} else if from == sdktranslator.FormatOpenAIResponse && to == sdktranslator.FormatOpenAI {
		out, err = requestFromResponsesToChat(model, body, stream)
		opaqueSource = body
	} else if from == to {
		out = append([]byte(nil), body...)
	} else {
		out, err = request(from, to, model, body, stream)
	}
	if err != nil {
		return nil, err
	}
	if from != to {
		out, err = restoreRequestAttachmentFields(body, out, from, to)
		if err != nil {
			return nil, err
		}
	}
	if from == sdktranslator.FormatClaude && to == sdktranslator.FormatOpenAIResponse {
		out, err = stripCacheControlFields(out)
		if err != nil {
			return nil, err
		}
	}
	out, err = setModelAndStream(out, model, stream)
	if err != nil {
		return nil, err
	}
	if opaqueSource != nil {
		return restoreCopilotOpaqueToChat(out, opaqueSource, model)
	}
	return out, nil
}

func requestFromResponsesToChat(model string, body []byte, stream bool) ([]byte, error) {
	if !json.Valid(body) {
		return nil, fmt.Errorf("request body is not valid JSON")
	}
	if err := rejectOpaqueReasoningRequest(sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAI, body); err != nil {
		return nil, err
	}
	carriedBody, orphaned, err := prepareOrphanResponsesToolOutputs(body)
	if err != nil {
		return nil, err
	}
	carriedBody, err = responsesRequestToChatToolIDs(carriedBody)
	if err != nil {
		return nil, err
	}
	carriedBody, files, err := prepareResponsesChatAttachments(carriedBody)
	if err != nil {
		return nil, err
	}
	out, err := translateSDKRequest(sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAI, model, carriedBody, stream)
	if err != nil {
		return nil, err
	}
	out, err = restoreChatAttachmentFiles(out, files)
	if err != nil {
		return nil, err
	}
	out, err = restoreOrphanResponsesToolOutputs(out, orphaned)
	if err != nil {
		return nil, err
	}
	return copyResponsesPromptCacheKey(out, body)
}

func ResponseToResponses(ctx context.Context, endpoint, model string, original, translated, body []byte) ([]byte, error) {
	return ResponseFromEndpoint(ctx, endpoint, sdktranslator.FormatOpenAIResponse.String(), model, original, translated, body)
}

func ResponseFromEndpoint(ctx context.Context, endpoint, destination, model string, original, translated, body []byte) ([]byte, error) {
	from, err := endpointFormat(endpoint)
	if err != nil {
		return nil, err
	}
	to := sdktranslator.FromString(destination)
	if from == to {
		if !json.Valid(body) {
			return nil, fmt.Errorf("Copilot API returned invalid JSON")
		}
		if from == sdktranslator.FormatOpenAI {
			return nativeChatResponseObject(body, "chat.completion")
		}
		return append([]byte(nil), body...), nil
	}
	return response(ctx, from, to, model, original, translated, body)
}

func StreamToResponses(ctx context.Context, endpoint, model string, original, translated, frame []byte, state *any) ([][]byte, error) {
	return StreamFromEndpoint(ctx, endpoint, sdktranslator.FormatOpenAIResponse.String(), model, original, translated, frame, state)
}

func StreamFromEndpoint(ctx context.Context, endpoint, destination, model string, original, translated, frame []byte, state *any) ([][]byte, error) {
	from, err := endpointFormat(endpoint)
	if err != nil {
		return nil, err
	}
	to := sdktranslator.FromString(destination)
	if from == to {
		if from == sdktranslator.FormatOpenAIResponse {
			return nativeResponsesStream(frame, state, declaresNativeResponsesWebSearch(translated))
		}
		if from == sdktranslator.FormatOpenAI {
			event, data, done, err := parseSSEFrame(frame)
			if err != nil {
				return nil, err
			}
			if !done && len(data) > 0 {
				out, err := nativeChatResponseObject(data, "chat.completion.chunk")
				if err != nil {
					return nil, err
				}
				if !bytes.Equal(out, data) {
					return [][]byte{responseSSEBytes(event, out)}, nil
				}
			}
		}
		return [][]byte{append([]byte(nil), frame...)}, nil
	}
	return stream(ctx, from, to, model, original, translated, frame, state)
}

func declaresNativeResponsesWebSearch(request []byte) bool {
	containsSearch := func(tools gjson.Result) bool {
		for _, tool := range tools.Array() {
			switch tool.Get("type").String() {
			case "web_search", "web_search_preview":
				return true
			}
		}
		return false
	}
	if containsSearch(gjson.GetBytes(request, "tools")) {
		return true
	}
	for _, item := range gjson.GetBytes(request, "input").Array() {
		if item.Get("type").String() == "additional_tools" && containsSearch(item.Get("tools")) {
			return true
		}
	}
	return false
}

func nativeChatResponseObject(body []byte, object string) ([]byte, error) {
	if !gjson.GetBytes(body, "object").Exists() && gjson.GetBytes(body, "choices").IsArray() {
		out, err := sjson.SetBytes(body, "object", object)
		if err != nil {
			return nil, fmt.Errorf("set Copilot Chat response object: %w", err)
		}
		return out, nil
	}
	return append([]byte(nil), body...), nil
}

func ResponsesSSEToClaude(ctx context.Context, model string, original, translated, frame []byte, state *any) ([][]byte, error) {
	return stream(ctx, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, model, original, translated, frame, state)
}

func request(from, to sdktranslator.Format, model string, body []byte, stream bool) ([]byte, error) {
	if !json.Valid(body) {
		return nil, fmt.Errorf("request body is not valid JSON")
	}
	if err := rejectOpaqueReasoningRequest(from, to, body); err != nil {
		return nil, err
	}
	if from == sdktranslator.FormatOpenAIResponse && from != to {
		if err := validateResponsesRequestForTarget(body, to); err != nil {
			return nil, err
		}
	}
	if from == sdktranslator.FormatClaude && to == sdktranslator.FormatOpenAIResponse {
		return claudeRequestToResponses(model, body, stream)
	}
	if from == sdktranslator.FormatOpenAIResponse && to == sdktranslator.FormatClaude {
		return responsesRequestToClaude(model, body, stream)
	}
	if from != to && !registry.HasRequestTransformer(from, to) {
		intermediate, ok := intermediateFormat(from, to)
		if !ok {
			return nil, fmt.Errorf("official translator has no request route from %s to %s", from, to)
		}
		first, err := translateSDKRequest(from, intermediate, model, body, stream)
		if err != nil {
			return nil, err
		}
		return translateSDKRequest(intermediate, to, model, first, stream)
	}
	return translateSDKRequest(from, to, model, body, stream)
}

func translateSDKRequest(from, to sdktranslator.Format, model string, body []byte, stream bool) ([]byte, error) {
	envelope := registry.TranslateRequestEnvelope(context.Background(), from, to, sdktranslator.RequestEnvelope{
		Format: from,
		Model:  model,
		Stream: stream,
		Body:   body,
	})
	if envelope.Err != nil {
		return nil, fmt.Errorf("official request translation from %s to %s refused the request: %w", from, to, envelope.Err)
	}
	if len(envelope.Body) == 0 || !json.Valid(envelope.Body) {
		return nil, fmt.Errorf("official request translation from %s to %s failed", from, to)
	}
	return envelope.Body, nil
}

func response(ctx context.Context, from, to sdktranslator.Format, model string, original, translated, body []byte) ([]byte, error) {
	if !json.Valid(body) {
		return nil, fmt.Errorf("response body is not valid JSON")
	}
	if from == sdktranslator.FormatOpenAIResponse && to == sdktranslator.FormatClaude {
		return responsesResponseToClaude(model, body)
	}
	if from == sdktranslator.FormatOpenAIResponse && to == sdktranslator.FormatOpenAI {
		return responsesResponseToChat(ctx, model, original, translated, body)
	}
	if from == sdktranslator.FormatOpenAI && to == sdktranslator.FormatOpenAIResponse {
		return chatResponseToResponses(ctx, model, original, translated, body)
	}
	if from == sdktranslator.FormatOpenAI && to == sdktranslator.FormatClaude {
		responses, err := chatResponseToResponses(ctx, model, original, translated, body)
		if err != nil {
			return nil, err
		}
		return responsesResponseToClaude(model, responses)
	}
	if from == sdktranslator.FormatClaude && to == sdktranslator.FormatOpenAIResponse {
		return claudeMessageResponseToResponses(ctx, model, original, translated, body)
	}
	if from == sdktranslator.FormatClaude && to == sdktranslator.FormatOpenAI {
		responses, err := claudeMessageResponseToResponses(ctx, model, original, translated, body)
		if err != nil {
			return nil, err
		}
		return responsesResponseToChat(ctx, model, original, translated, responses)
	}
	if err := rejectOpaqueReasoningResponse(from, to, body); err != nil {
		return nil, err
	}
	if from != to && !registry.HasNonStreamResponseTransformer(from, to) {
		intermediate, ok := intermediateFormat(to, from)
		if !ok {
			return nil, fmt.Errorf("official translator has no response route from %s to %s", from, to)
		}
		intermediateRequest, err := translateSDKRequest(to, intermediate, model, original, false)
		if err != nil {
			return nil, err
		}
		first := registry.TranslateNonStream(ctx, from, intermediate, model, intermediateRequest, translated, body, nil)
		out := registry.TranslateNonStream(ctx, intermediate, to, model, original, intermediateRequest, first, nil)
		if len(out) == 0 || !json.Valid(out) {
			return nil, fmt.Errorf("official two-hop response translation from %s to %s failed", from, to)
		}
		return out, nil
	}
	out := registry.TranslateNonStream(ctx, from, to, model, original, translated, body, nil)
	if len(out) == 0 || !json.Valid(out) {
		return nil, fmt.Errorf("official response translation from %s to %s failed", from, to)
	}
	return out, nil
}

func chatResponseToResponses(ctx context.Context, model string, original, translated, body []byte) ([]byte, error) {
	if err := chatResponseFailure(body); err != nil {
		return nil, err
	}
	out := registry.TranslateNonStream(ctx, sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse, model, original, translated, body, nil)
	if len(out) == 0 || !json.Valid(out) {
		return nil, fmt.Errorf("official Chat-to-Responses response translation failed")
	}
	carrier, exists, err := copilotOpaqueFromChatResponse(body, translated, model)
	if err == nil && exists {
		return insertOpaqueReasoning(out, carrier)
	}
	return out, err
}

func chatResponseFailure(body []byte) error {
	for _, choice := range gjson.GetBytes(body, "choices").Array() {
		if choice.Get("finish_reason").String() == "error" {
			return fmt.Errorf("Copilot Chat request failed")
		}
	}
	return nil
}

func stream(ctx context.Context, from, to sdktranslator.Format, model string, original, translated, frame []byte, state *any) ([][]byte, error) {
	if from == sdktranslator.FormatOpenAIResponse && from != to {
		if err := validateResponsesStreamFrame(frame, to); err != nil {
			return nil, err
		}
	}
	if from == sdktranslator.FormatOpenAIResponse && to == sdktranslator.FormatClaude {
		return responsesStreamToClaude(model, frame, state)
	}
	if from == sdktranslator.FormatOpenAIResponse && to == sdktranslator.FormatOpenAI {
		return responsesStreamToChat(ctx, model, original, translated, frame, state)
	}
	if from == sdktranslator.FormatClaude && to == sdktranslator.FormatOpenAI {
		return claudeStreamToChat(ctx, model, original, translated, frame, state)
	}
	if from == sdktranslator.FormatClaude && to == sdktranslator.FormatOpenAIResponse {
		return claudeStreamToResponses(ctx, model, original, translated, frame, state)
	}
	if from == sdktranslator.FormatOpenAI && (to == sdktranslator.FormatOpenAIResponse || to == sdktranslator.FormatClaude) {
		if state == nil {
			return nil, fmt.Errorf("Chat opaque reasoning stream translation requires state")
		}
		streamState, ok := (*state).(*chatOpaqueStreamState)
		if !ok {
			streamState = &chatOpaqueStreamState{Model: model, LastSequence: -1}
			*state = streamState
		}
		if streamState.Failure != nil {
			return nil, streamState.Failure
		}
		if streamState.Model != model {
			return nil, fmt.Errorf("Chat opaque reasoning stream model changed")
		}
		_, data, _, err := parseSSEFrame(frame)
		if err != nil {
			return nil, err
		}
		if err := chatResponseFailure(data); err != nil {
			streamState.Failure = err
			return nil, err
		}
		if err := streamState.observe(frame, translated, model); err != nil {
			return nil, err
		}
		responsesFrames, err := chatToResponsesStream(ctx, model, original, translated, frame, streamState)
		if err == nil && to == sdktranslator.FormatClaude {
			var out [][]byte
			for _, responsesFrame := range responsesFrames {
				claudeFrames, err := responsesStreamToClaude(model, responsesFrame, &streamState.Claude)
				if err != nil {
					return nil, err
				}
				out = append(out, claudeFrames...)
			}
			return out, nil
		}
		return responsesFrames, err
	}
	if err := rejectOpaqueReasoningStream(from, to, frame); err != nil {
		return nil, err
	}
	if from != to && !registry.HasStreamResponseTransformer(from, to) {
		intermediate, ok := intermediateFormat(to, from)
		if !ok {
			return nil, fmt.Errorf("official translator has no stream route from %s to %s", from, to)
		}
		if state == nil {
			return nil, fmt.Errorf("two-hop stream translation requires state")
		}
		hopState, okState := (*state).(*twoHopStreamState)
		if !okState {
			hopState = &twoHopStreamState{}
			*state = hopState
		}
		intermediateRequest, err := translateSDKRequest(to, intermediate, model, original, true)
		if err != nil {
			return nil, err
		}
		first := registry.TranslateStream(ctx, from, intermediate, model, intermediateRequest, translated, frame, &hopState.First)
		var out [][]byte
		for _, firstFrame := range first {
			out = append(out, registry.TranslateStream(ctx, intermediate, to, model, original, intermediateRequest, firstFrame, &hopState.Second)...)
		}
		return out, nil
	}
	out := registry.TranslateStream(ctx, from, to, model, original, translated, frame, state)
	if len(out) == 1 && bytes.Equal(out[0], frame) && from != to {
		return nil, fmt.Errorf("official stream translation from %s to %s fell back unchanged", from, to)
	}
	return out, nil
}

type twoHopStreamState struct {
	First  any
	Second any
}

func intermediateFormat(from, to sdktranslator.Format) (sdktranslator.Format, bool) {
	for _, candidate := range []sdktranslator.Format{
		sdktranslator.FormatOpenAI,
		sdktranslator.FormatOpenAIResponse,
		sdktranslator.FormatClaude,
	} {
		if candidate != from && candidate != to &&
			registry.HasRequestTransformer(from, candidate) &&
			registry.HasRequestTransformer(candidate, to) {
			return candidate, true
		}
	}
	return "", false
}

func endpointFormat(endpoint string) (sdktranslator.Format, error) {
	switch endpoint {
	case EndpointResponses:
		return sdktranslator.FormatOpenAIResponse, nil
	case EndpointChatCompletions:
		return sdktranslator.FormatOpenAI, nil
	case EndpointMessages:
		return sdktranslator.FormatClaude, nil
	default:
		return "", fmt.Errorf("unsupported Copilot endpoint %q", endpoint)
	}
}

func setModelAndStream(body []byte, model string, streamEnabled bool) ([]byte, error) {
	out, err := sjson.SetBytes(body, "model", model)
	if err != nil {
		return nil, fmt.Errorf("set translated request model: %w", err)
	}
	out, err = sjson.SetBytes(out, "stream", streamEnabled)
	if err != nil {
		return nil, fmt.Errorf("set translated request stream flag: %w", err)
	}
	return out, nil
}
