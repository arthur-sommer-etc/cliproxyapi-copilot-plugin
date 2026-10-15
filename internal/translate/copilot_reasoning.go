package translate

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const copilotReplayPrefix = "cpa-copilot-chat:v1:"

// ErrCopilotReasoning identifies reasoning that cannot be recovered without changing its owner or history.
var ErrCopilotReasoning = errors.New("invalid Copilot reasoning state")

// CopilotReplay authenticates Chat reasoning carriers for one credential, model and API endpoint.
type CopilotReplay struct {
	key   [32]byte
	scope string
}

type copilotReplayValue struct {
	History string         `json:"history"`
	Message map[string]any `json:"message"`
}

type chatMessageGroup struct {
	start int
	end   int
	value map[string]any
}

// NewCopilotReplay binds replay to the host-selected route and persistent OAuth credential.
func NewCopilotReplay(authID, model, endpoint, apiBaseURL, credential string) *CopilotReplay {
	scope, _ := json.Marshal([]string{"copilot", authID, model, endpoint, apiBaseURL})
	return &CopilotReplay{key: sha256.Sum256([]byte(credential)), scope: string(scope)}
}

// Request restores authenticated reasoning after ordinary protocol translation.
func (r *CopilotReplay) Request(source, model string, body []byte, stream bool, endpoint string) ([]byte, error) {
	root, err := decodeObject(body)
	if err != nil {
		return nil, err
	}
	carriers, err := requestReplayCarriers(source, root, endpoint)
	if err != nil {
		return nil, err
	}
	out, err := RequestForEndpointFrom(source, model, body, stream, endpoint)
	if err != nil || len(carriers) == 0 {
		return out, err
	}
	chat, err := decodeObject(out)
	if err != nil {
		return nil, err
	}
	for _, carrier := range carriers {
		value, err := r.unseal(carrier.signature)
		if err != nil {
			return nil, err
		}
		if rawStringValue(value.Message["reasoning_text"]) != carrier.text {
			return nil, ErrCopilotReasoning
		}
		messages, _ := chat["messages"].([]any)
		groups := groupChatMessages(messages)
		var prefix []any
		start, end := -1, -1
		shape := chatMessageShape(value.Message)
		calls, _ := shape["tool_calls"].([]any)
		reasoningOnly := shape["content"] == "" && len(calls) == 0
		for _, group := range groups {
			if replayDigest(prefix) == value.History && (replayDigest(group.value) == replayDigest(shape) || reasoningOnly && stringValue(group.value["role"]) != "assistant") {
				if start >= 0 {
					return nil, ErrCopilotReasoning
				}
				start, end = group.start, group.end
				if stringValue(group.value["role"]) != "assistant" {
					end = start
				}
			}
			prefix = append(prefix, group.value)
		}
		if reasoningOnly && replayDigest(prefix) == value.History {
			start, end = len(messages), len(messages)
		}
		if start < 0 {
			return nil, ErrCopilotReasoning
		}
		updated := append([]any{}, messages[:start]...)
		updated = append(updated, value.Message)
		chat["messages"] = append(updated, messages[end:]...)
	}
	return json.Marshal(chat)
}

type requestReplayCarrier struct {
	signature string
	text      string
}

func requestReplayCarriers(source string, root map[string]any, endpoint string) ([]requestReplayCarrier, error) {
	var carriers []requestReplayCarrier
	accept := func(signature, text string) error {
		if endpoint != EndpointChatCompletions || !strings.HasPrefix(signature, copilotReplayPrefix) {
			return ErrCopilotReasoning
		}
		for _, prior := range carriers {
			if prior.signature == signature {
				return ErrCopilotReasoning
			}
		}
		carriers = append(carriers, requestReplayCarrier{signature: signature, text: text})
		return nil
	}
	switch source {
	case "claude":
		messages, _ := root["messages"].([]any)
		for _, raw := range messages {
			message := objectValue(raw)
			blocks, _ := message["content"].([]any)
			for _, raw := range blocks {
				block := objectValue(raw)
				kind := stringValue(block["type"])
				if kind == "thinking" || kind == "redacted_thinking" {
					signature := rawStringValue(block["signature"])
					if endpoint == EndpointChatCompletions || strings.HasPrefix(signature, copilotReplayPrefix) {
						if kind != "thinking" || stringValue(message["role"]) != "assistant" {
							return nil, ErrCopilotReasoning
						}
						if err := accept(signature, rawStringValue(block["thinking"])); err != nil {
							return nil, err
						}
					}
				}
			}
		}
	case "openai-response":
		items, _ := root["input"].([]any)
		for _, raw := range items {
			item := objectValue(raw)
			if stringValue(item["type"]) == "reasoning" {
				signature := rawStringValue(item["encrypted_content"])
				if endpoint == EndpointChatCompletions || strings.HasPrefix(signature, copilotReplayPrefix) {
					if err := accept(signature, responsesReasoningText(item)); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	return carriers, nil
}

// Response retains the Copilot wire dialect while ordinary content uses the existing translators.
func (r *CopilotReplay) Response(ctx context.Context, endpoint, destination, model string, original, translated, body []byte) ([]byte, error) {
	if endpoint != EndpointChatCompletions || destination == "openai" {
		return ResponseFromEndpoint(ctx, endpoint, destination, model, original, translated, body)
	}
	root, err := decodeObject(body)
	if err != nil {
		return nil, err
	}
	choices, _ := root["choices"].([]any)
	var carrier string
	for _, raw := range choices {
		choice := objectValue(raw)
		message := objectValue(choice["message"])
		if hasCopilotReasoning(message) {
			if len(choices) != 1 || stringValue(message["role"]) != "assistant" {
				return nil, ErrCopilotReasoning
			}
			carrier, err = r.seal(message, translated)
			if err != nil {
				return nil, err
			}
			if text, ok := message["reasoning_text"]; ok {
				if _, ok := text.(string); !ok {
					return nil, ErrCopilotReasoning
				}
				message["reasoning_content"] = text
			}
		}
	}
	if carrier == "" {
		return ResponseFromEndpoint(ctx, endpoint, destination, model, original, translated, body)
	}
	normalized, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	out, err := ResponseToResponses(ctx, endpoint, model, original, translated, normalized)
	if err != nil {
		return nil, err
	}
	if carrier != "" {
		out, err = attachChatCarrier(out, carrier)
		if err != nil {
			return nil, err
		}
	}
	if destination == "claude" {
		return responsesResponseToClaude(model, out)
	}
	return out, nil
}

func hasCopilotReasoning(message map[string]any) bool {
	_, text := message["reasoning_text"]
	opaque, exists := message["reasoning_opaque"]
	return text || exists && opaque != nil
}

func (r *CopilotReplay) seal(message map[string]any, request []byte) (string, error) {
	root, err := decodeObject(request)
	if err != nil {
		return "", err
	}
	messages, _ := root["messages"].([]any)
	var history []any
	for _, group := range groupChatMessages(messages) {
		history = append(history, group.value)
	}
	assistant := map[string]any{"role": "assistant"}
	for _, field := range []string{"content", "tool_calls", "reasoning_text", "reasoning_opaque"} {
		if value, exists := message[field]; exists {
			assistant[field] = value
		}
	}
	payload, err := json.Marshal(copilotReplayValue{History: replayDigest(history), Message: assistant})
	if err != nil {
		return "", err
	}
	mac := r.mac(payload)
	return copilotReplayPrefix + base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac), nil
}

func (r *CopilotReplay) mac(payload []byte) []byte {
	mac := hmac.New(sha256.New, r.key[:])
	mac.Write([]byte(copilotReplayPrefix))
	mac.Write([]byte(r.scope))
	mac.Write([]byte{0})
	mac.Write(payload)
	return mac.Sum(nil)
}

func (r *CopilotReplay) unseal(signature string) (copilotReplayValue, error) {
	var value copilotReplayValue
	encoded, ok := strings.CutPrefix(signature, copilotReplayPrefix)
	if !ok {
		return value, ErrCopilotReasoning
	}
	payloadPart, macPart, ok := strings.Cut(encoded, ".")
	payload, payloadErr := base64.RawURLEncoding.DecodeString(payloadPart)
	mac, macErr := base64.RawURLEncoding.DecodeString(macPart)
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.UseNumber()
	if !ok || payloadErr != nil || macErr != nil || !hmac.Equal(mac, r.mac(payload)) || decoder.Decode(&value) != nil {
		return value, ErrCopilotReasoning
	}
	return value, nil
}

func replayDigest(value any) string {
	encoded, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func groupChatMessages(messages []any) []chatMessageGroup {
	var groups []chatMessageGroup
	for index, raw := range messages {
		message := objectValue(raw)
		shape := chatMessageShape(message)
		if len(groups) > 0 && stringValue(shape["role"]) == "assistant" && stringValue(groups[len(groups)-1].value["role"]) == "assistant" {
			group := &groups[len(groups)-1]
			group.end = index + 1
			group.value["content"] = concatenateChatContent(group.value["content"], shape["content"])
			priorCalls, _ := group.value["tool_calls"].([]any)
			calls, _ := shape["tool_calls"].([]any)
			group.value["tool_calls"] = append(priorCalls, calls...)
		} else {
			groups = append(groups, chatMessageGroup{start: index, end: index + 1, value: shape})
		}
	}
	return groups
}

func chatMessageShape(message map[string]any) map[string]any {
	content := message["content"]
	if content == nil {
		content = ""
	} else if parts, ok := content.([]any); ok {
		var text strings.Builder
		textOnly := true
		for _, raw := range parts {
			part := objectValue(raw)
			if stringValue(part["type"]) == "text" {
				text.WriteString(rawStringValue(part["text"]))
			} else {
				textOnly = false
			}
		}
		if textOnly {
			content = text.String()
		}
	}
	shape := map[string]any{"role": message["role"], "content": content}
	if stringValue(message["role"]) == "assistant" {
		var calls []any
		tools, _ := message["tool_calls"].([]any)
		for _, raw := range tools {
			tool := objectValue(raw)
			function := objectValue(tool["function"])
			var arguments any
			decoder := json.NewDecoder(strings.NewReader(rawStringValue(function["arguments"])))
			decoder.UseNumber()
			if decoder.Decode(&arguments) != nil {
				arguments = function["arguments"]
			}
			calls = append(calls, map[string]any{"id": tool["id"], "name": function["name"], "arguments": arguments})
		}
		shape["tool_calls"] = calls
	} else if id, exists := message["tool_call_id"]; exists {
		shape["tool_call_id"] = id
	}
	return shape
}

func concatenateChatContent(first, second any) any {
	left, leftText := first.(string)
	right, rightText := second.(string)
	if leftText && rightText {
		return left + right
	}
	var parts []any
	for _, value := range []any{first, second} {
		if text, ok := value.(string); ok {
			if text != "" {
				parts = append(parts, map[string]any{"type": "text", "text": text})
			}
		} else if items, ok := value.([]any); ok {
			parts = append(parts, items...)
		}
	}
	return parts
}

func attachChatCarrier(body []byte, carrier string) ([]byte, error) {
	root, err := decodeObject(body)
	if err != nil {
		return nil, err
	}
	items, _ := root["output"].([]any)
	attached := false
	for _, raw := range items {
		item := objectValue(raw)
		if stringValue(item["type"]) == "reasoning" {
			item["encrypted_content"] = carrier
			attached = true
		}
	}
	if !attached {
		items = append(items, map[string]any{"id": "rs_copilot_" + replayDigest(carrier)[:20], "type": "reasoning", "summary": []any{}, "encrypted_content": carrier})
	}
	root["output"] = items
	return json.Marshal(root)
}
