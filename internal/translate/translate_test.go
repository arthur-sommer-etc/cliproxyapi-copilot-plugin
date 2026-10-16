package translate

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestClaudeRequestToResponsesPreservesCoreContent(t *testing.T) {
	t.Parallel()

	claudeRequest := []byte(`{
		"model":"ignored",
		"max_tokens":321,
		"system":"Be concise.",
		"messages":[
			{"role":"user","content":[
				{"type":"text","text":"describe this"},
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"YWJj"}}
			]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"inspect first","signature":"sig"},
				{"type":"redacted_thinking","data":"opaque-redacted"},
				{"type":"tool_use","id":"call_1","name":"lookup","input":{"q":"x"}}
			]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"found"}]}
		],
		"tools":[{"name":"lookup","description":"Look up data","input_schema":{"type":"object","properties":{"q":{"type":"string"}}}}]
	}`)

	out, err := RequestForEndpointFrom("claude", "gpt-5.6-sol", claudeRequest, false, EndpointResponses)
	if err != nil {
		t.Fatalf("translate Claude request: %v", err)
	}
	data := gjson.ParseBytes(out)
	if got := data.Get("model").String(); got != "gpt-5.6-sol" {
		t.Fatalf("model = %q", got)
	}
	if got := data.Get("max_output_tokens").Int(); got != 321 {
		t.Fatalf("max_output_tokens = %d", got)
	}
	text := string(out)
	for _, needle := range []string{
		"Be concise.",
		"describe this",
		"data:image/png;base64,YWJj",
		"inspect first",
		`"type":"function_call"`,
		`"call_id":"call_1"`,
		`"type":"function_call_output"`,
		`"name":"lookup"`,
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("translated request omits %q: %s", needle, out)
		}
	}
	if got := data.Get("input.1.encrypted_content").String(); got != "sig" {
		t.Fatalf("thinking signature = %q; request=%s", got, out)
	}
	if got := data.Get("input.2.encrypted_content").String(); got != redactedThinkingPrefix+"opaque-redacted" {
		t.Fatalf("redacted thinking data = %q; request=%s", got, out)
	}
}

func TestClaudeStructuredOutputAddsResponsesSchemaName(t *testing.T) {
	t.Parallel()

	original := []byte(`{
		"model":"gpt-5.6-terra",
		"messages":[{"role":"user","content":"Create a title."}],
		"output_config":{"format":{"type":"json_schema","schema":{"type":"object"}}}
	}`)
	out, err := RequestForEndpointFrom("claude", "gpt-5.6-terra", original, true, EndpointResponses)
	if err != nil {
		t.Fatalf("translate request: %v", err)
	}
	if got := gjson.GetBytes(out, "text.format.name").String(); got != "claude_structured_output" {
		t.Fatalf("text.format.name = %q; request=%s", got, out)
	}
}

func TestChatNonStreamResponseToResponses(t *testing.T) {
	t.Parallel()

	original := []byte(`{"model":"gpt-test","input":[{"role":"user","content":[{"type":"input_text","text":"ping"}]}]}`)
	translated, err := RequestForEndpoint("gpt-test", original, false, EndpointChatCompletions)
	if err != nil {
		t.Fatalf("translate request: %v", err)
	}
	upstream := []byte(`{
		"id":"chatcmpl_1","object":"chat.completion","created":1,"model":"gpt-test",
		"choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}
	}`)
	out, err := ResponseToResponses(context.Background(), EndpointChatCompletions, "gpt-test", original, translated, upstream)
	if err != nil {
		t.Fatalf("translate response: %v", err)
	}
	data := gjson.ParseBytes(out)
	if got := data.Get("status").String(); got != "completed" {
		t.Fatalf("status = %q; response=%s", got, out)
	}
	if got := data.Get("output.0.content.0.text").String(); got != "pong" {
		t.Fatalf("output text = %q; response=%s", got, out)
	}
	if got := data.Get("usage.total_tokens").Int(); got != 3 {
		t.Fatalf("total tokens = %d; response=%s", got, out)
	}
}

func TestChatRequestToResponsesPreservesToolIDsThinkingAndCacheKey(t *testing.T) {
	t.Parallel()
	const model = "gpt-6-luna"
	request := []byte(`{"model":"ignored","prompt_cache_key":"chat-cache-key","reasoning_effort":"low","messages":[{"role":"user","content":"inspect this"},{"role":"assistant","tool_calls":[{"id":"chat-call/+opaque-1","type":"function","function":{"name":"inspect","arguments":"{\"query\":\"value\"}"}}]},{"role":"tool","tool_call_id":"chat-call/+opaque-1","content":"found"},{"role":"user","content":"continue"}],"tools":[{"type":"function","function":{"name":"inspect","parameters":{"type":"object","properties":{"query":{"type":"string"}}}}}]}`)
	out, err := RequestForEndpointFrom("openai", model, request, false, EndpointResponses)
	if err != nil {
		t.Fatalf("translate Chat request: %v", err)
	}
	if got := gjson.GetBytes(out, "prompt_cache_key").String(); got != "chat-cache-key" {
		t.Fatalf("prompt_cache_key = %q; request=%s", got, out)
	}
	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "low" {
		t.Fatalf("reasoning.effort = %q; request=%s", got, out)
	}
	if got := gjson.GetBytes(out, "input.#(type==function_call).call_id").String(); got != "chat-call/+opaque-1" {
		t.Fatalf("function call ID = %q; request=%s", got, out)
	}
	if got := gjson.GetBytes(out, "input.#(type==function_call_output).call_id").String(); got != "chat-call/+opaque-1" {
		t.Fatalf("function output ID = %q; request=%s", got, out)
	}
	if got := gjson.GetBytes(out, "input.#(type==function_call).arguments").String(); got != `{"query":"value"}` {
		t.Fatalf("function arguments = %q; request=%s", got, out)
	}
}

func TestResponsesResponseToChatPreservesOpaqueReasoningAndCallIDs(t *testing.T) {
	t.Parallel()
	const model = "gpt-6-luna"
	itemID := strings.Repeat("i", 424)
	callID := "call_1/+"
	original := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"inspect this"}],"tools":[{"type":"function","function":{"name":"inspect","parameters":{"type":"object"}}}]}`)
	translated := []byte(`{"model":"` + model + `","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"inspect this"}]}]}`)
	reasoning := []byte(`{"id":"rs_opaque/+1","type":"reasoning","status":"completed","summary":[{"type":"summary_text","text":"private summary"}],"encrypted_content":"signature/+ exact"}`)
	response := []byte(`{"id":"resp_1","object":"response","status":"completed","model":"` + model + `","output":[` + string(reasoning) + `,{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"pong"}]},{"id":"` + itemID + `","type":"function_call","call_id":"` + callID + `","name":"inspect","arguments":"{\"query\":\"x\"}"}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
	out, err := ResponseFromEndpoint(context.Background(), EndpointResponses, "openai", model, original, translated, response)
	if err != nil {
		t.Fatalf("translate Responses response: %v", err)
	}
	chatToolID := gjson.GetBytes(out, "choices.0.message.tool_calls.0.id").String()
	gotItemID, gotCallID, encoded := DecodeClaudeToolIDs(chatToolID)
	if !encoded || gotItemID != itemID || gotCallID != callID {
		t.Fatalf("tool call ID %q decoded as (%q, %q, %v); want (%q, %q)", chatToolID, gotItemID, gotCallID, encoded, itemID, callID)
	}
	carrier := gjson.GetBytes(out, "choices.0.message.reasoning_opaque").String()
	decoded, err := decodeCopilotOpaque(carrier, model)
	if err != nil {
		t.Fatalf("decode Chat reasoning carrier: %v", err)
	}
	if string(decoded.Raw) != `[`+string(reasoning)+`]` {
		t.Fatalf("reasoning items changed: got %s, want [%s]", decoded.Raw, reasoning)
	}
}

func TestChatRequestToResponsesRestoresOpaqueReasoningHistory(t *testing.T) {
	t.Parallel()
	const model = "gpt-6-luna"
	itemID := strings.Repeat("i", 424)
	callID := "call_1/+"
	original := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"inspect this"}],"tools":[{"type":"function","function":{"name":"inspect","parameters":{"type":"object"}}}]}`)
	translated := []byte(`{"model":"` + model + `","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"inspect this"}]}]}`)
	reasoning := []byte(`{"id":"rs_opaque/+1","type":"reasoning","status":"completed","summary":[{"type":"summary_text","text":"private summary"}],"encrypted_content":"signature/+ exact"}`)
	response := []byte(`{"id":"resp_1","object":"response","status":"completed","model":"` + model + `","output":[` + string(reasoning) + `,{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":""}]},{"id":"` + itemID + `","type":"function_call","call_id":"` + callID + `","name":"inspect","arguments":"{\"query\":\"x\"}"}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
	chatResponse, err := ResponseFromEndpoint(context.Background(), EndpointResponses, "openai", model, original, translated, response)
	if err != nil {
		t.Fatalf("translate Responses response: %v", err)
	}
	assistant := gjson.GetBytes(chatResponse, "choices.0.message").Raw
	chatToolID := gjson.GetBytes(chatResponse, "choices.0.message.tool_calls.0.id").String()
	gotItemID, gotCallID, encoded := DecodeClaudeToolIDs(chatToolID)
	if !encoded || gotItemID != itemID || gotCallID != callID {
		t.Fatalf("tool call ID %q decoded as (%q, %q, %v); want (%q, %q)", chatToolID, gotItemID, gotCallID, encoded, itemID, callID)
	}
	followup := []byte(`{"model":"` + model + `","prompt_cache_key":"cache-1","messages":[{"role":"user","content":"inspect this"},` + assistant + `,{"role":"tool","tool_call_id":"` + chatToolID + `","content":"found"},{"role":"user","content":"continue"}],"tools":[{"type":"function","function":{"name":"inspect","parameters":{"type":"object"}}}]}`)
	out, err := RequestForEndpointFrom("openai", model, followup, false, EndpointResponses)
	if err != nil {
		t.Fatalf("translate Chat follow-up: %v", err)
	}
	reasoningItem := gjson.GetBytes(out, "input.#(type==reasoning)")
	if !reasoningItem.Exists() || reasoningItem.Raw != string(reasoning) {
		t.Fatalf("replayed reasoning item = %s, want %s; request=%s", reasoningItem.Raw, reasoning, out)
	}
	if got := gjson.GetBytes(out, "input.#(type==function_call).call_id").String(); got != callID {
		t.Fatalf("replayed call ID = %q, want %q; request=%s", got, callID, out)
	}
	if got := gjson.GetBytes(out, "input.#(type==function_call).id").String(); got != itemID {
		t.Fatalf("replayed item ID = %q, want %q; request=%s", got, itemID, out)
	}
	if got := gjson.GetBytes(out, "input.#(type==function_call_output).call_id").String(); got != callID {
		t.Fatalf("replayed tool-result ID = %q, want %q; request=%s", got, callID, out)
	}
}

func TestChatRequestToResponsesMapsDuplicateAssistantTextInOrder(t *testing.T) {
	t.Parallel()
	const model = "gpt-6-luna"
	first := []byte(`{"id":"reasoning-first","type":"reasoning","summary":[],"encrypted_content":"signature-first"}`)
	second := []byte(`{"id":"reasoning-second","type":"reasoning","summary":[],"encrypted_content":"signature-second"}`)
	firstCarrier, err := encodeCopilotOpaque(rawJSONArray([][]byte{first}), model, copilotOpaqueAnchor{ContentSHA256: digest([]byte("same answer")), PriorUserSHA256: digest([]byte("first question"))})
	if err != nil {
		t.Fatalf("encode first opaque carrier: %v", err)
	}
	secondCarrier, err := encodeCopilotOpaque(rawJSONArray([][]byte{second}), model, copilotOpaqueAnchor{ContentSHA256: digest([]byte("same answer")), PriorUserSHA256: digest([]byte("second question"))})
	if err != nil {
		t.Fatalf("encode second opaque carrier: %v", err)
	}
	request := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"first question"},{"role":"assistant","content":"same answer","reasoning_opaque":"` + firstCarrier + `"},{"role":"user","content":"second question"},{"role":"assistant","content":"same answer","reasoning_opaque":"` + secondCarrier + `"}]}`)
	out, err := RequestForEndpointFrom("openai", model, request, false, EndpointResponses)
	if err != nil {
		t.Fatalf("translate duplicate-text history: %v", err)
	}
	items := gjson.GetBytes(out, "input").Array()
	var signatures []string
	for _, item := range items {
		if item.Get("type").String() == "reasoning" {
			signatures = append(signatures, item.Get("encrypted_content").String())
		}
	}
	if len(signatures) != 2 || signatures[0] != "signature-first" || signatures[1] != "signature-second" {
		t.Fatalf("reasoning order = %v; request=%s", signatures, out)
	}
	firstAssistantIndex, secondAssistantIndex := -1, -1
	for index, item := range items {
		if item.Get("type").String() == "message" && item.Get("role").String() == "assistant" && item.Get("content.0.text").String() == "same answer" {
			if firstAssistantIndex < 0 {
				firstAssistantIndex = index
			} else {
				secondAssistantIndex = index
			}
		}
	}
	firstReasoningIndex, secondReasoningIndex := -1, -1
	for index, item := range items {
		if item.Get("type").String() == "reasoning" {
			if firstReasoningIndex < 0 {
				firstReasoningIndex = index
			} else {
				secondReasoningIndex = index
			}
		}
	}
	if firstAssistantIndex < 0 || secondAssistantIndex <= firstAssistantIndex || firstReasoningIndex >= firstAssistantIndex || secondReasoningIndex <= firstAssistantIndex || secondReasoningIndex >= secondAssistantIndex {
		t.Fatalf("reasoning blocks were attached to the wrong assistant turns: %s", out)
	}
}

func TestChatRequestToResponsesRejectsUnsupportedBlocks(t *testing.T) {
	t.Parallel()
	for _, request := range []string{
		`{"messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"YWJj"}}]}]}`,
		`{"messages":[{"role":"assistant","tool_calls":[{"id":"call-1","type":"custom","custom":{"name":"run","input":"x"}}]}]}`,
	} {
		if _, err := RequestForEndpointFrom("openai", "gpt-6-luna", []byte(request), false, EndpointResponses); err == nil {
			t.Fatalf("accepted a Chat block that native translation drops: %s", request)
		}
	}
}

func TestResponsesNonStreamResponseToClaude(t *testing.T) {
	t.Parallel()

	original := []byte(`{"model":"gpt-5.6-sol","max_tokens":20,"messages":[{"role":"user","content":"ping"}]}`)
	translated, err := RequestForEndpointFrom("claude", "gpt-5.6-sol", original, false, EndpointResponses)
	if err != nil {
		t.Fatalf("translate request: %v", err)
	}
	upstream := []byte(`{
		"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"gpt-5.6-sol",
		"output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"pong","annotations":[]}]}],
		"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}
	}`)
	out, err := ResponseFromEndpoint(context.Background(), EndpointResponses, "claude", "gpt-5.6-sol", original, translated, upstream)
	if err != nil {
		t.Fatalf("translate response: %v", err)
	}
	data := gjson.ParseBytes(out)
	if got := data.Get("type").String(); got != "message" {
		t.Fatalf("Claude response type = %q; response=%s", got, out)
	}
	if got := data.Get("content.0.text").String(); got != "pong" {
		t.Fatalf("Claude response text = %q; response=%s", got, out)
	}
	if got := data.Get("usage.input_tokens").Int(); got != 2 {
		t.Fatalf("Claude input tokens = %d; response=%s", got, out)
	}
}

func TestChatSSEToResponses(t *testing.T) {
	t.Parallel()

	original := []byte(`{"model":"gpt-test","input":[{"role":"user","content":[{"type":"input_text","text":"ping"}]}]}`)
	translated, err := RequestForEndpoint("gpt-test", original, true, EndpointChatCompletions)
	if err != nil {
		t.Fatalf("translate request: %v", err)
	}
	chunks := [][]byte{
		[]byte(`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant","content":"po"},"finish_reason":null}]}`),
		[]byte(`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"gpt-test","choices":[{"index":0,"delta":{"content":"ng"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`),
		[]byte(`data: [DONE]`),
	}
	var state any
	var output strings.Builder
	for _, chunk := range chunks {
		frames, errTranslate := StreamToResponses(context.Background(), EndpointChatCompletions, "gpt-test", original, translated, chunk, &state)
		if errTranslate != nil {
			t.Fatalf("translate SSE: %v", errTranslate)
		}
		for _, frame := range frames {
			output.Write(frame)
		}
	}
	text := output.String()
	for _, needle := range []string{
		"event: response.created",
		"event: response.output_text.delta",
		`"delta":"po"`,
		`"delta":"ng"`,
		"event: response.completed",
		`"total_tokens":3`,
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("translated SSE omits %q:\n%s", needle, text)
		}
	}
}

func TestResponsesSSEToClaude(t *testing.T) {
	t.Parallel()

	original := []byte(`{"model":"gpt-5.6-sol","max_tokens":20,"messages":[{"role":"user","content":"ping"}]}`)
	translated, err := RequestForEndpointFrom("claude", "gpt-5.6-sol", original, true, EndpointResponses)
	if err != nil {
		t.Fatalf("translate request: %v", err)
	}
	chunks := [][]byte{
		[]byte(`event: response.created
data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress","model":"gpt-5.6-sol","output":[],"usage":{"input_tokens":2,"output_tokens":0}}}

`),
		[]byte(`event: response.content_part.added
data: {"type":"response.content_part.added","item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}

`),
		[]byte(`event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"one"}

`),
		[]byte(`event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":" two"}

`),
		[]byte(`event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":" three"}

`),
		[]byte(`event: response.content_part.done
data: {"type":"response.content_part.done","item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":"one two three"}}

`),
		[]byte(`event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.6-sol","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"one two three"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}}

`),
	}
	var state any
	var output strings.Builder
	for _, chunk := range chunks {
		frames, errTranslate := StreamFromEndpoint(context.Background(), EndpointResponses, "claude", "gpt-5.6-sol", original, translated, chunk, &state)
		if errTranslate != nil {
			t.Fatalf("translate SSE: %v", errTranslate)
		}
		for _, frame := range frames {
			output.Write(frame)
		}
	}
	text := output.String()
	for _, needle := range []string{
		"event: message_start",
		"event: content_block_start",
		`"type":"text"`,
		`"text":"one"`,
		`"text":" two"`,
		`"text":" three"`,
		`"stop_reason":"end_turn"`,
		`"output_tokens":3`,
		"event: message_stop",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("Claude SSE omits %q:\n%s", needle, text)
		}
	}
}
