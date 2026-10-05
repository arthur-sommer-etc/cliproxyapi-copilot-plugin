package translate

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestCopilotOpaqueRoundTripsResponsesHistoryAndCacheKey(t *testing.T) {
	t.Parallel()

	const model = "gemini-3.8-flash"
	rawOpaque := []byte(`"{\"sig\": \"preserve exactly\"}"`)
	responsesRequest := []byte(`{"model":"` + model + `","prompt_cache_key":"cache-key-123","input":[{"role":"user","content":[{"type":"input_text","text":"run this"}]}]}`)
	chatRequest, err := RequestForEndpoint(model, responsesRequest, false, EndpointChatCompletions)
	if err != nil {
		t.Fatalf("translate Responses request: %v", err)
	}
	if got := gjson.GetBytes(chatRequest, "prompt_cache_key").Raw; got != `"cache-key-123"` {
		t.Fatalf("prompt_cache_key = %s; request=%s", got, chatRequest)
	}
	chatResponse := []byte(`{"id":"chatcmpl_1","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_run_1","type":"function","function":{"name":"run","arguments":"{\"x\":1}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":12,"completion_tokens":2,"total_tokens":14,"prompt_tokens_details":{"cached_tokens":7}}}`)
	chatResponse, err = sjson.SetRawBytes(chatResponse, "choices.0.message.reasoning_opaque", rawOpaque)
	if err != nil {
		t.Fatalf("set raw opaque field: %v", err)
	}
	responsesResponse, err := ResponseToResponses(context.Background(), EndpointChatCompletions, model, responsesRequest, chatRequest, chatResponse)
	if err != nil {
		t.Fatalf("translate Chat response: %v", err)
	}
	carrier := gjson.GetBytes(responsesResponse, "output.#(type==reasoning).encrypted_content").String()
	decoded, err := decodeCopilotOpaque(carrier, model)
	if err != nil {
		t.Fatalf("decode Responses carrier: %v", err)
	}
	if string(decoded.Raw) != string(rawOpaque) {
		t.Fatalf("opaque raw JSON changed: got %s, want %s", decoded.Raw, rawOpaque)
	}
	if gjson.GetBytes(responsesResponse, "output.#(type==reasoning).summary.#").Int() != 0 {
		t.Fatalf("opaque value leaked into reasoning summary: %s", responsesResponse)
	}
	if got := gjson.GetBytes(responsesResponse, "usage.input_tokens_details.cached_tokens").Int(); got != 7 {
		t.Fatalf("cached prompt tokens = %d; response=%s", got, responsesResponse)
	}
	replay := []byte(`{"model":"` + model + `","prompt_cache_key":"cache-key-123","input":[{"role":"user","content":[{"type":"input_text","text":"run this"}]},{"type":"reasoning","summary":[],"encrypted_content":"` + carrier + `"},{"type":"function_call","id":"item_run_1","call_id":"call_run_1","name":"run","arguments":"{\"x\":1}"},{"type":"function_call_output","call_id":"call_run_1","output":"done"}]}`)
	replayedChat, err := RequestForEndpointFrom("openai-response", model, replay, false, EndpointChatCompletions)
	if err != nil {
		t.Fatalf("translate Responses replay: %v", err)
	}
	if got := gjson.GetBytes(replayedChat, "prompt_cache_key").Raw; got != `"cache-key-123"` {
		t.Fatalf("replayed prompt_cache_key = %s; request=%s", got, replayedChat)
	}
	assistant := replayedChatAssistantWithTool(t, replayedChat, "call_run_1")
	if got := assistant.Get("reasoning_opaque").Raw; got != string(rawOpaque) {
		t.Fatalf("replayed reasoning_opaque = %s, want %s; request=%s", got, rawOpaque, replayedChat)
	}
	if got := assistant.Get("tool_calls.0.function.arguments").String(); got != `{"x":1}` {
		t.Fatalf("tool arguments changed: %q", got)
	}
	if got := gjson.GetBytes(replayedChat, "messages.2.reasoning_content"); got.Exists() {
		t.Fatalf("opaque carrier leaked as reasoning text: %s", replayedChat)
	}
}

func TestCopilotOpaqueClaudeSignatureReplaysToChat(t *testing.T) {
	t.Parallel()

	const model = "gemini-3.8-flash"
	claudeRequest := []byte(`{"model":"claude","prompt_cache_key":"cache-key-claude","system":[{"type":"text","text":"Use the tool.","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"run this"}],"tools":[{"name":"run","input_schema":{"type":"object"}}]}`)
	chatRequest, err := RequestForEndpointFrom("claude", model, claudeRequest, false, EndpointChatCompletions)
	if err != nil {
		t.Fatalf("translate Claude request: %v", err)
	}
	if got := gjson.GetBytes(chatRequest, "prompt_cache_key").Raw; got != `"cache-key-claude"` {
		t.Fatalf("Claude prompt_cache_key = %s; request=%s", got, chatRequest)
	}
	if gjson.GetBytes(chatRequest, "messages.0.content.0.text").String() != "Use the tool." {
		t.Fatalf("Claude system content was lost: %s", chatRequest)
	}
	if got := gjson.GetBytes(chatRequest, "messages.0.content.0.cache_control").Raw; got != `{"type":"ephemeral"}` {
		t.Fatalf("Claude cache_control was not preserved for Chat: %s", chatRequest)
	}
	if got := gjson.GetBytes(chatRequest, "messages.0.content.0.text").String(); got != "Use the tool." {
		t.Fatalf("Claude system prefix text was lost: %s", chatRequest)
	}
	rawOpaque := []byte(`"copilot-turn-state"`)
	chatResponse := []byte(`{"id":"chatcmpl_2","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_run_2","type":"function","function":{"name":"run","arguments":"{\"x\":2}"}}]},"finish_reason":"tool_calls"}]}`)
	chatResponse, err = sjson.SetRawBytes(chatResponse, "choices.0.message.reasoning_opaque", rawOpaque)
	if err != nil {
		t.Fatalf("set raw opaque field: %v", err)
	}
	claudeResponse, err := ResponseFromEndpoint(context.Background(), EndpointChatCompletions, "claude", model, claudeRequest, chatRequest, chatResponse)
	if err != nil {
		t.Fatalf("translate Chat response to Claude: %v", err)
	}
	var claudeMessage struct {
		Content []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(claudeResponse, &claudeMessage); err != nil {
		t.Fatalf("decode Claude response: %v", err)
	}
	var signature, toolID string
	for _, block := range claudeMessage.Content {
		switch block["type"] {
		case "thinking":
			signature, _ = block["signature"].(string)
		case "tool_use":
			toolID, _ = block["id"].(string)
		}
	}
	decoded, err := decodeCopilotOpaque(signature, model)
	if err != nil || string(decoded.Raw) != string(rawOpaque) {
		t.Fatalf("Claude signature does not carry exact Copilot state: raw=%s err=%v", decoded.Raw, err)
	}
	if toolID == "" {
		t.Fatalf("Claude response lost tool ID: %s", claudeResponse)
	}
	followup := []byte(`{"model":"claude","prompt_cache_key":"cache-key-claude","system":[{"type":"text","text":"Use the tool.","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"run this"},{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"` + signature + `"},{"type":"tool_use","id":"` + toolID + `","name":"run","input":{"x":2}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + toolID + `","content":"done"}]}],"tools":[{"name":"run","input_schema":{"type":"object"}}]}`)
	replayedChat, err := RequestForEndpointFrom("claude", model, followup, false, EndpointChatCompletions)
	if err != nil {
		t.Fatalf("translate Claude follow-up: %v", err)
	}
	assistant := replayedChatAssistantWithTool(t, replayedChat, "call_run_2")
	if got := assistant.Get("reasoning_opaque").Raw; got != string(rawOpaque) {
		t.Fatalf("Claude replay changed reasoning_opaque: got %s want %s; request=%s", got, rawOpaque, replayedChat)
	}
	if got := gjson.GetBytes(replayedChat, "prompt_cache_key").Raw; got != `"cache-key-claude"` {
		t.Fatalf("replayed Claude prompt_cache_key = %s; request=%s", got, replayedChat)
	}
}

func TestCopilotOpaqueFinalOnlySSETranslatesAndReplays(t *testing.T) {
	t.Parallel()

	const model = "gemini-3.8-flash"
	rawOpaque := []byte(`"stream-final-state"`)
	responsesRequest := []byte(`{"model":"` + model + `","prompt_cache_key":"stream-cache-key","input":[{"role":"user","content":[{"type":"input_text","text":"run streamed tool"}]}]}`)
	chatRequest, err := RequestForEndpoint(model, responsesRequest, true, EndpointChatCompletions)
	if err != nil {
		t.Fatalf("translate Responses request: %v", err)
	}
	finalChunk := []byte(`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","created":7,"model":"` + model + `","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_stream_1","type":"function","function":{"name":"run","arguments":"{\"x\":3}"}}]},"finish_reason":"tool_calls"}]}`)
	finalChunk, err = sjson.SetRawBytes(finalChunk, "choices.0.delta.reasoning_opaque", rawOpaque)
	if err != nil {
		t.Fatalf("set stream opaque field: %v", err)
	}
	finalChunk = append(finalChunk, []byte("\n\n")...)
	var responsesState any
	responsesFrames, err := StreamFromEndpoint(context.Background(), EndpointChatCompletions, "openai-response", model, responsesRequest, chatRequest, finalChunk, &responsesState)
	if err != nil {
		t.Fatalf("translate Chat SSE to Responses: %v", err)
	}
	lastResponsesFrames, err := StreamFromEndpoint(context.Background(), EndpointChatCompletions, "openai-response", model, responsesRequest, chatRequest, []byte("data: [DONE]\n\n"), &responsesState)
	if err != nil {
		t.Fatalf("finish Chat SSE to Responses: %v", err)
	}
	responsesFrames = append(responsesFrames, lastResponsesFrames...)
	var carrier string
	var completed []byte
	added, done := false, false
	for _, frame := range responsesFrames {
		event, data, isDone, err := parseSSEFrame(frame)
		if err != nil || isDone || len(data) == 0 {
			continue
		}
		if event == "response.output_item.added" || event == "response.output_item.done" {
			item := gjson.GetBytes(data, "item")
			if item.Get("type").String() == "reasoning" {
				if event == "response.output_item.added" {
					added = true
				} else {
					done = true
				}
				carrier = item.Get("encrypted_content").String()
			}
		}
		if event == "response.completed" {
			completed = data
		}
	}
	if !added || !done || len(completed) == 0 || carrier == "" {
		t.Fatalf("final-only opaque delta did not create a completed reasoning item: added=%v done=%v completed=%s frames=%q", added, done, completed, responsesFrames)
	}
	decoded, err := decodeCopilotOpaque(carrier, model)
	if err != nil || string(decoded.Raw) != string(rawOpaque) {
		t.Fatalf("stream carrier changed opaque JSON: raw=%s err=%v", decoded.Raw, err)
	}
	terminalCarrier := gjson.GetBytes(completed, "response.output.#(type==reasoning).encrypted_content").String()
	if terminalCarrier != carrier {
		t.Fatalf("terminal carrier differs from item events: %q != %q", terminalCarrier, carrier)
	}
	replay := []byte(`{"model":"` + model + `","input":[{"role":"user","content":[{"type":"input_text","text":"run streamed tool"}]},{"type":"reasoning","summary":[],"encrypted_content":"` + carrier + `"},{"type":"function_call","id":"item_stream_1","call_id":"call_stream_1","name":"run","arguments":"{\"x\":3}"}]}`)
	replayedChat, err := RequestForEndpointFrom("openai-response", model, replay, false, EndpointChatCompletions)
	if err != nil {
		t.Fatalf("translate streamed Responses replay: %v", err)
	}
	assistant := replayedChatAssistantWithTool(t, replayedChat, "call_stream_1")
	if got := assistant.Get("reasoning_opaque").Raw; got != string(rawOpaque) {
		t.Fatalf("stream replay changed reasoning_opaque: got %s want %s", got, rawOpaque)
	}

	claudeRequest := []byte(`{"model":"claude","messages":[{"role":"user","content":"run streamed tool"}]}`)
	claudeChatRequest, err := RequestForEndpointFrom("claude", model, claudeRequest, true, EndpointChatCompletions)
	if err != nil {
		t.Fatalf("translate Claude stream request: %v", err)
	}
	var claudeState any
	claudeFrames, err := StreamFromEndpoint(context.Background(), EndpointChatCompletions, "claude", model, claudeRequest, claudeChatRequest, finalChunk, &claudeState)
	if err != nil {
		t.Fatalf("translate Chat SSE to Claude: %v", err)
	}
	lastClaudeFrames, err := StreamFromEndpoint(context.Background(), EndpointChatCompletions, "claude", model, claudeRequest, claudeChatRequest, []byte("data: [DONE]\n\n"), &claudeState)
	if err != nil {
		t.Fatalf("finish Chat SSE to Claude: %v", err)
	}
	claudeFrames = append(claudeFrames, lastClaudeFrames...)
	var signature string
	for _, frame := range claudeFrames {
		_, data, isDone, err := parseSSEFrame(frame)
		if err != nil || isDone || len(data) == 0 {
			continue
		}
		payload, err := decodeObject(data)
		if err != nil {
			continue
		}
		if stringValue(payload["type"]) == "content_block_delta" && stringValue(objectValue(payload["delta"])["type"]) == "signature_delta" {
			signature = stringValue(objectValue(payload["delta"])["signature"])
		}
	}
	streamDecoded, err := decodeCopilotOpaque(signature, model)
	if err != nil || string(streamDecoded.Raw) != string(rawOpaque) {
		t.Fatalf("Claude signature stream lost exact carrier: raw=%s err=%v frames=%q", streamDecoded.Raw, err, claudeFrames)
	}
}

func TestChatOpaqueStreamUsesOrdinalForCompleteParallelToolSnapshot(t *testing.T) {
	t.Parallel()

	const model = "gemini-3.8-flash"
	request := []byte(`{"messages":[{"role":"user","content":"run both"}]}`)
	frame := []byte("data: {\"choices\":[{\"index\":0,\"message\":{\"role\":\"assistant\",\"content\":\"\",\"tool_calls\":[{\"id\":\"call_one\",\"type\":\"function\",\"function\":{\"name\":\"one\",\"arguments\":\"{\\\"n\\\":1}\"}},{\"id\":\"call_two\",\"type\":\"function\",\"function\":{\"name\":\"two\",\"arguments\":\"{\\\"n\\\":2}\"}}]}}]}\n\n")
	frame, err := sjson.SetRawBytes(frame, "choices.0.message.reasoning_opaque", []byte(`"parallel-state"`))
	if err != nil {
		t.Fatalf("set snapshot opaque field: %v", err)
	}
	state := &chatOpaqueStreamState{Model: model}
	if err := state.observe(frame, request, model); err != nil {
		t.Fatalf("observe complete Chat snapshot: %v", err)
	}
	carrier, exists, err := state.carrier(request, model)
	if err != nil || !exists {
		t.Fatalf("build snapshot carrier: exists=%v err=%v", exists, err)
	}
	decoded, err := decodeCopilotOpaque(carrier, model)
	if err != nil {
		t.Fatalf("decode snapshot carrier: %v", err)
	}
	if len(decoded.Anchor.ToolCallIDs) != 2 || decoded.Anchor.ToolCallIDs[0] != "call_one" || decoded.Anchor.ToolCallIDs[1] != "call_two" {
		t.Fatalf("parallel tool IDs collapsed or reordered: %#v", decoded.Anchor.ToolCallIDs)
	}
	if len(decoded.Anchor.ToolCalls) != 2 || decoded.Anchor.ToolCalls[0].Name != "one" || decoded.Anchor.ToolCalls[1].Name != "two" {
		t.Fatalf("parallel tool signatures collapsed or reordered: %#v", decoded.Anchor.ToolCalls)
	}
}

func TestClaudeRequestRejectsUnknownNonemptyContentBlocks(t *testing.T) {
	t.Parallel()

	request := []byte(`{"messages":[{"role":"assistant","content":[{"type":"future_block","payload":"must not disappear"}]}]}`)
	if _, err := RequestForEndpointFrom("claude", "gemini-3.8-flash", request, false, EndpointResponses); err == nil {
		t.Fatal("unknown nonempty Claude block was silently dropped")
	}
}

func replayedChatAssistantWithTool(t *testing.T, request []byte, id string) gjson.Result {
	t.Helper()
	var match gjson.Result
	for _, message := range gjson.GetBytes(request, "messages").Array() {
		if message.Get("role").String() != "assistant" {
			continue
		}
		for _, call := range message.Get("tool_calls").Array() {
			callID := call.Get("id").String()
			if _, decodedCallID, ok := DecodeClaudeToolIDs(callID); ok {
				callID = decodedCallID
			}
			if callID == id {
				if match.Exists() {
					t.Fatalf("multiple assistant calls match id %q: %s", id, request)
				}
				match = message
			}
		}
	}
	if !match.Exists() {
		t.Fatalf("assistant tool call %q was lost: %s", id, request)
	}
	return match
}
