package translate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestResponsesChatToolIDsRoundTripAcrossMultiturnHistory(t *testing.T) {
	const functionItemID = "fc-initial/+opaque-1"
	const functionCallID = "call-initial/+opaque-1"
	const customItemID = "ctc-initial/+opaque-2"
	const customCallID = "call-initial/+opaque-2"
	const cacheKey = "prefix-key/opaque"
	responsesRequest := []byte(`{"model":"gpt-test","prompt_cache_key":"` + cacheKey + `","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"inspect these tools"}]},{"type":"function_call","id":"` + functionItemID + `","call_id":"` + functionCallID + `","name":"inspect","arguments":"{\"query\":\"value\"}"},{"type":"custom_tool_call","id":"` + customItemID + `","call_id":"` + customCallID + `","name":"exec_command","input":"printf exact custom payload"},{"type":"function_call_output","call_id":"` + functionCallID + `","output":"inspection complete"},{"type":"custom_tool_call_output","call_id":"` + customCallID + `","output":"command complete"},{"type":"function_call","call_id":"call-no-item/+opaque","name":"anonymous","arguments":"{}"},{"type":"function_call_output","call_id":"call-no-item/+opaque","output":"anonymous complete"},{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}]}`)
	var previousIDs = make(map[string]string)
	for _, stream := range []bool{false, true} {
		chatRequest, err := RequestForEndpointFrom("openai-response", "gpt-test", responsesRequest, stream, EndpointChatCompletions)
		if err != nil {
			t.Fatalf("Responses-to-Chat request with stream=%t: %v", stream, err)
		}
		if got := gjson.GetBytes(chatRequest, "prompt_cache_key").String(); got != cacheKey {
			t.Fatalf("Chat prompt_cache_key = %q, want %q; request=%s", got, cacheKey, chatRequest)
		}
		if got := gjson.GetBytes(chatRequest, "stream").Bool(); got != stream {
			t.Fatalf("Chat stream = %t, want %t; request=%s", got, stream, chatRequest)
		}
		functionToolID := chatToolID(t, chatRequest, "inspect")
		customToolID := chatToolID(t, chatRequest, "exec_command")
		anonymousToolID := chatToolID(t, chatRequest, "anonymous")
		assertDecodedToolIDs(t, functionToolID, functionItemID, functionCallID)
		assertDecodedToolIDs(t, customToolID, customItemID, customCallID)
		assertDecodedToolIDs(t, anonymousToolID, "", "call-no-item/+opaque")
		if got := chatToolResultID(t, chatRequest, "inspection complete"); got != functionToolID {
			t.Fatalf("function tool result ID = %q, want matching call ID %q", got, functionToolID)
		}
		if got := chatToolResultID(t, chatRequest, "command complete"); got != customToolID {
			t.Fatalf("custom tool result ID = %q, want matching call ID %q", got, customToolID)
		}
		if got := chatToolResultID(t, chatRequest, "anonymous complete"); got != anonymousToolID {
			t.Fatalf("tool result without a source item ID = %q, want matching call ID %q", got, anonymousToolID)
		}
		if got := chatToolArguments(t, chatRequest, "inspect"); got != `{"query":"value"}` {
			t.Fatalf("function tool arguments = %q, want exact JSON; request=%s", got, chatRequest)
		}
		if got := chatToolArguments(t, chatRequest, "exec_command"); got != `{"input":"printf exact custom payload"}` {
			t.Fatalf("custom tool input wrapper = %q; request=%s", got, chatRequest)
		}
		if old, exists := previousIDs["inspect"]; exists && old != functionToolID {
			t.Fatalf("function carrier changed between request modes: %q -> %q", old, functionToolID)
		}
		if old, exists := previousIDs["exec_command"]; exists && old != customToolID {
			t.Fatalf("custom carrier changed between request modes: %q -> %q", old, customToolID)
		}
		previousIDs["inspect"] = functionToolID
		previousIDs["exec_command"] = customToolID
	}
}

func TestClaudeToolIDCarrierEscapesReservedIDsAndRejectsMalformedValues(t *testing.T) {
	if got := encodeClaudeToolID("call_simple_1", "call_simple_1"); got != "call_simple_1" {
		t.Fatalf("equal safe IDs should remain literal, got %q", got)
	}
	for _, ids := range [][2]string{
		{"fc-item", "call-id"},
		{"", "call-id"},
		{"cpa_tool_v2_raw_item", "cpa_tool_v2_raw_call"},
		{"cpa_tool_v1_not-a-carrier", "cpa_tool_v2_raw_call"},
	} {
		encoded := encodeClaudeToolID(ids[0], ids[1])
		itemID, callID, ok := DecodeClaudeToolIDs(encoded)
		if !ok || itemID != ids[0] || callID != ids[1] {
			t.Fatalf("carrier %q decoded to (%q, %q, %t), want (%q, %q, true)", encoded, itemID, callID, ok, ids[0], ids[1])
		}
	}
	unknownFields := "cpa_tool_v1_" + base64.RawURLEncoding.EncodeToString([]byte(`{"i":"item","c":"call","extra":true}`))
	nonCanonical := "cpa_tool_v1_" + base64.RawURLEncoding.EncodeToString([]byte(` {"i":"item","c":"call"}`))
	for _, malformed := range []string{"cpa_tool_v2_future", "cpa_tool_v1_bad", unknownFields, nonCanonical} {
		if _, _, err := responsesToolIDsFromClaude(malformed); err == nil {
			t.Fatalf("malformed or unknown reserved carrier %q was accepted", malformed)
		}
	}
}

func TestClaudeToolIDCarrierRoundTripsLongDistinctIDs(t *testing.T) {
	itemID := "fc_" + strings.Repeat("i", 4096)
	callID := "call_" + strings.Repeat("c", 4096)
	encoded := encodeClaudeToolID(itemID, callID)
	decodedItemID, decodedCallID, ok := DecodeClaudeToolIDs(encoded)
	if !ok || decodedItemID != itemID || decodedCallID != callID {
		t.Fatalf("long carrier failed to round-trip exactly: ok=%t item_len=%d call_len=%d", ok, len(decodedItemID), len(decodedCallID))
	}
}

func chatToolID(t *testing.T, request []byte, name string) string {
	t.Helper()
	for _, message := range gjson.GetBytes(request, "messages").Array() {
		for _, call := range message.Get("tool_calls").Array() {
			if call.Get("function.name").String() == name {
				return call.Get("id").String()
			}
		}
	}
	t.Fatalf("Chat tool call %q missing: %s", name, request)
	return ""
}

func chatToolResultID(t *testing.T, request []byte, content string) string {
	t.Helper()
	for _, message := range gjson.GetBytes(request, "messages").Array() {
		if message.Get("role").String() == "tool" && message.Get("content").String() == content {
			return message.Get("tool_call_id").String()
		}
	}
	t.Fatalf("Chat tool result %q missing: %s", content, request)
	return ""
}

func chatToolArguments(t *testing.T, request []byte, name string) string {
	t.Helper()
	for _, message := range gjson.GetBytes(request, "messages").Array() {
		for _, call := range message.Get("tool_calls").Array() {
			if call.Get("function.name").String() == name {
				return call.Get("function.arguments").String()
			}
		}
	}
	t.Fatalf("Chat tool call %q missing: %s", name, request)
	return ""
}

func assertDecodedToolIDs(t *testing.T, value, wantItemID, wantCallID string) {
	t.Helper()
	itemID, callID, ok := DecodeClaudeToolIDs(value)
	if !ok || itemID != wantItemID || callID != wantCallID {
		t.Fatalf("tool ID %q decoded to (%q, %q, %t), want (%q, %q, true)", value, itemID, callID, ok, wantItemID, wantCallID)
	}
}

func TestResponsesToClaudePlainAndEncodedIDsRemainDistinct(t *testing.T) {
	response := []byte(`{"id":"resp_ids","status":"completed","model":"gpt-test","output":[{"type":"function_call","id":"call_simple_1","call_id":"call_simple_1","name":"plain","arguments":"{}"},{"type":"function_call","id":"fc-special/+item","call_id":"call-special/+call","name":"distinct","arguments":"{}"}]}`)
	claude, err := ResponseFromEndpoint(context.Background(), EndpointResponses, "claude", "gpt-test", nil, nil, response)
	if err != nil {
		t.Fatalf("Responses-to-Claude response: %v", err)
	}
	plainID := gjson.GetBytes(claude, "content.0.id").String()
	if plainID != "call_simple_1" {
		t.Fatalf("simple tool ID changed to %q; response=%s", plainID, claude)
	}
	assertDecodedToolIDs(t, gjson.GetBytes(claude, "content.1.id").String(), "fc-special/+item", "call-special/+call")
}

func TestChatSSEToolIDPairReplaysThroughResponsesHistory(t *testing.T) {
	const model = "gpt-test"
	const upstreamToolID = "call_stream/+opaque"
	request := []byte(`{"model":"` + model + `","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"inspect"}]}]}`)
	chatRequest, err := RequestForEndpoint(model, request, true, EndpointChatCompletions)
	if err != nil {
		t.Fatalf("translate Responses stream request: %v", err)
	}
	var state any
	chunk, err := json.Marshal(map[string]any{
		"id": "chatcmpl_stream", "object": "chat.completion.chunk", "created": 7, "model": model,
		"choices": []any{map[string]any{
			"index": 0,
			"delta": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
				"index": 0, "id": upstreamToolID, "type": "function",
				"function": map[string]any{"name": "inspect", "arguments": `{"query":"value"}`},
			}}},
			"finish_reason": "tool_calls",
		}},
	})
	if err != nil {
		t.Fatalf("encode Chat stream chunk: %v", err)
	}
	frame := append(append([]byte("data: "), chunk...), []byte("\n\n")...)
	frames, err := StreamFromEndpoint(context.Background(), EndpointChatCompletions, "openai-response", model, request, chatRequest, frame, &state)
	if err != nil {
		t.Fatalf("translate Chat tool stream to Responses: %v", err)
	}
	terminal, err := StreamFromEndpoint(context.Background(), EndpointChatCompletions, "openai-response", model, request, chatRequest, []byte("data: [DONE]\n\n"), &state)
	if err != nil {
		t.Fatalf("finish Chat tool stream: %v", err)
	}
	frames = append(frames, terminal...)
	var itemID, callID, arguments string
	for _, outputFrame := range frames {
		event, data, _, err := parseSSEFrame(outputFrame)
		if err != nil || event != "response.completed" {
			continue
		}
		for _, item := range gjson.GetBytes(data, "response.output").Array() {
			if item.Get("type").String() == "function_call" {
				itemID = item.Get("id").String()
				callID = item.Get("call_id").String()
				arguments = item.Get("arguments").String()
			}
		}
	}
	if itemID == "" || callID != upstreamToolID || arguments != `{"query":"value"}` {
		t.Fatalf("terminal Responses tool identity/input = (%q, %q, %q); frames=%q", itemID, callID, arguments, frames)
	}
	replay := []byte(`{"model":"` + model + `","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"inspect"}]},{"type":"function_call","id":"` + itemID + `","call_id":"` + callID + `","name":"inspect","arguments":"{\"query\":\"value\"}"},{"type":"function_call_output","call_id":"` + callID + `","output":"found"},{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}]}`)
	replayedChat, err := RequestForEndpoint(model, replay, false, EndpointChatCompletions)
	if err != nil {
		t.Fatalf("translate streamed Responses history to Chat: %v", err)
	}
	carrier := chatToolID(t, replayedChat, "inspect")
	assertDecodedToolIDs(t, carrier, itemID, callID)
	if got := chatToolResultID(t, replayedChat, "found"); got != carrier {
		t.Fatalf("replayed Chat tool result ID = %q, want %q", got, carrier)
	}
}

func TestMalformedReservedClaudeToolCarrierFailsRequestTranslation(t *testing.T) {
	request := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"cpa_tool_v2_future","name":"run","input":{}}]}]}`)
	if _, err := RequestForEndpointFrom("claude", "gpt-test", request, false, EndpointResponses); err == nil {
		t.Fatal("unknown reserved tool carrier version was accepted")
	}
}
