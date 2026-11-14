package translate

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestClaudeStreamToChatPreservesSignedAndRedactedReasoning(t *testing.T) {
	t.Parallel()
	const model = "claude-sonnet-5.5"
	request := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"continue"}]}`)
	frames := [][]byte{
		[]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-sonnet-5.5\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"),
		[]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n"),
		[]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"inspect\"}}\n\n"),
		[]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"signature/+ exact\"}}\n\n"),
		[]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"),
		[]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"redacted_thinking\",\"data\":\"redacted/+ exact\"}}\n\n"),
		[]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n"),
		[]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"),
		[]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"text_delta\",\"text\":\"answer\"}}\n\n"),
		[]byte("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n"),
		[]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
	}
	var state any
	var output [][]byte
	for _, frame := range frames {
		translated, err := StreamFromEndpoint(context.Background(), EndpointMessages, "openai", model, request, []byte(`{"model":"`+model+`","messages":[{"role":"user","content":"continue"}]}`), frame, &state)
		if err != nil {
			t.Fatalf("translate Claude frame: %v", err)
		}
		output = append(output, translated...)
	}
	var carrier string
	var text string
	for _, chunk := range output {
		if !gjson.ValidBytes(chunk) {
			t.Fatalf("Chat chunk is not JSON: %s", chunk)
		}
		text += gjson.GetBytes(chunk, "choices.0.delta.content").String()
		if value := gjson.GetBytes(chunk, "choices.0.delta.reasoning_opaque").String(); value != "" {
			carrier = value
		}
	}
	if text != "answer" {
		t.Fatalf("text = %q; chunks=%q", text, output)
	}
	decoded, err := decodeCopilotOpaque(carrier, model)
	if err != nil {
		t.Fatalf("decode Chat opaque carrier %q: %v; chunks=%q", carrier, err, output)
	}
	if got := gjson.GetBytes(decoded.Raw, "0.encrypted_content").String(); got != "signature/+ exact" {
		t.Fatalf("signature = %q; carrier=%s", got, decoded.Raw)
	}
	if got := gjson.GetBytes(decoded.Raw, "1.encrypted_content").String(); got != redactedThinkingPrefix+"redacted/+ exact" {
		t.Fatalf("redacted data = %q; carrier=%s", got, decoded.Raw)
	}
}

func TestResponsesStreamToChatPreservesOpaqueReasoningAndToolIdentity(t *testing.T) {
	t.Parallel()
	const model = "gpt-6-luna"
	request := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"inspect this"}],"tools":[{"type":"function","function":{"name":"inspect","parameters":{"type":"object"}}}]}`)
	itemID := "item_1/+"
	callID := "call_1/+"
	reasoning := `{"id":"rs_1/+","type":"reasoning","status":"completed","summary":[{"type":"summary_text","text":"private summary"}],"encrypted_content":"encrypted/+ exact"}`
	terminalReasoning := `{"id":"rs_1/+","type":"reasoning","status":"completed","summary":[{"type":"summary_text","text":"private summary"}]}`
	toolCall := `{"id":"` + itemID + `","type":"function_call","call_id":"` + callID + `","name":"inspect","arguments":"{\"query\":\"x\"}"}`
	message := `{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"answer"}]}`
	frames := [][]byte{
		[]byte("event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-6-luna\",\"output\":[]}}\n\n"),
		[]byte("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"output_index\":0,\"item\":" + reasoning + "}\n\n"),
		[]byte("event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":2,\"output_index\":0,\"item\":" + reasoning + "}\n\n"),
		[]byte("data: {\"type\":\"response.completed\",\"sequence_number\":3,\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-6-luna\",\"status\":\"completed\",\"output\":[" + terminalReasoning + "," + message + "," + toolCall + "],\"usage\":{\"input_tokens\":1,\"output_tokens\":2}}}\n\n"),
	}
	var state any
	var output [][]byte
	for _, frame := range frames {
		translated, err := StreamFromEndpoint(context.Background(), EndpointResponses, "openai", model, request, []byte(`{"model":"`+model+`","input":[]}`), frame, &state)
		if err != nil {
			t.Fatalf("translate Responses frame: %v", err)
		}
		output = append(output, translated...)
	}
	var carrier string
	var text string
	var chatToolID string
	for _, chunk := range output {
		if !gjson.ValidBytes(chunk) {
			t.Fatalf("Chat chunk is not JSON: %s", chunk)
		}
		text += gjson.GetBytes(chunk, "choices.0.delta.content").String()
		if value := gjson.GetBytes(chunk, "choices.0.delta.reasoning_opaque").String(); value != "" {
			carrier = value
		}
		if id := gjson.GetBytes(chunk, "choices.0.delta.tool_calls.0.id").String(); id != "" {
			chatToolID = id
		}
	}
	if text != "answer" {
		t.Fatalf("text = %q; chunks=%q", text, output)
	}
	gotItemID, gotCallID, encoded := DecodeClaudeToolIDs(chatToolID)
	if !encoded || gotItemID != itemID || gotCallID != callID {
		t.Fatalf("tool ID %q decoded as (%q, %q, %v)", chatToolID, gotItemID, gotCallID, encoded)
	}
	decoded, err := decodeCopilotOpaque(carrier, model)
	if err != nil {
		t.Fatalf("decode Chat opaque carrier %q: %v; chunks=%q", carrier, err, output)
	}
	if got := gjson.GetBytes(decoded.Raw, "0.encrypted_content").String(); got != "encrypted/+ exact" {
		t.Fatalf("encrypted content = %q; carrier=%s", got, decoded.Raw)
	}
	if got := gjson.GetBytes(decoded.Raw, "0.id").String(); got != "rs_1/+" {
		t.Fatalf("reasoning item ID = %q; carrier=%s", got, decoded.Raw)
	}
}

func TestResponsesStreamToChatUsesUpstreamModelWhenCallerUsesAlias(t *testing.T) {
	t.Parallel()
	const alias = "mai-code-1-1-flash"
	const upstreamModel = "mai-code-1.1-flash"
	original := []byte(`{"model":"` + alias + `","messages":[{"role":"user","content":"answer"}]}`)
	translated := []byte(`{"model":"` + upstreamModel + `","input":[]}`)
	frames := [][]byte{
		[]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_model\",\"model\":\"" + upstreamModel + "\",\"output\":[]}}\n\n"),
		[]byte("data: {\"type\":\"response.output_text.delta\",\"response_id\":\"resp_model\",\"item_id\":\"msg_model\",\"output_index\":0,\"delta\":\"answer\"}\n\n"),
		[]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_model\",\"model\":\"" + upstreamModel + "\",\"status\":\"completed\",\"output\":[{\"id\":\"msg_model\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"),
	}
	var state any
	var output [][]byte
	for _, frame := range frames {
		translatedFrames, err := StreamFromEndpoint(context.Background(), EndpointResponses, "openai", alias, original, translated, frame, &state)
		if err != nil {
			t.Fatalf("translate Responses model frame: %v", err)
		}
		output = append(output, translatedFrames...)
	}
	if len(output) == 0 {
		t.Fatal("Responses stream emitted no Chat chunks")
	}
	for index, chunk := range output {
		if got := gjson.GetBytes(chunk, "model").String(); got != upstreamModel {
			t.Fatalf("Chat chunk %d model = %q, want upstream model %q: %s", index, got, upstreamModel, chunk)
		}
	}
}

func TestResponsesStreamToChatFallsBackToRouteModel(t *testing.T) {
	t.Parallel()
	const routeModel = "mai-code-1.1-flash"
	request := []byte(`{"model":"` + routeModel + `","messages":[{"role":"user","content":"answer"}]}`)
	translated := []byte(`{"model":"` + routeModel + `","input":[]}`)
	frames := [][]byte{
		[]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_fallback\",\"output\":[]}}\n\n"),
		[]byte("data: {\"type\":\"response.output_text.delta\",\"response_id\":\"resp_fallback\",\"item_id\":\"msg_fallback\",\"output_index\":0,\"delta\":\"answer\"}\n\n"),
		[]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fallback\",\"status\":\"completed\",\"output\":[{\"id\":\"msg_fallback\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"),
	}
	var state any
	var output [][]byte
	for _, frame := range frames {
		translatedFrames, err := StreamFromEndpoint(context.Background(), EndpointResponses, "openai", routeModel, request, translated, frame, &state)
		if err != nil {
			t.Fatalf("translate Responses fallback frame: %v", err)
		}
		output = append(output, translatedFrames...)
	}
	if len(output) == 0 {
		t.Fatal("Responses stream emitted no Chat chunks")
	}
	for index, chunk := range output {
		if got := gjson.GetBytes(chunk, "model").String(); got != routeModel {
			t.Fatalf("Chat chunk %d model = %q, want route model %q: %s", index, got, routeModel, chunk)
		}
	}
}

func TestClaudeStreamToChatUsesUpstreamModelWhenCallerUsesAlias(t *testing.T) {
	t.Parallel()
	const alias = "claude-sonnet-5-5"
	const upstreamModel = "claude-sonnet-5.5"
	original := []byte(`{"model":"` + alias + `","messages":[{"role":"user","content":"answer"}]}`)
	translated := []byte(`{"model":"` + upstreamModel + `","messages":[{"role":"user","content":[{"type":"text","text":"answer"}]}]}`)
	frames := [][]byte{
		[]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_model\",\"model\":\"" + upstreamModel + "\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"),
		[]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"),
		[]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"answer\"}}\n\n"),
		[]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"),
		[]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"),
		[]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
	}
	var state any
	var output [][]byte
	for _, frame := range frames {
		translatedFrames, err := StreamFromEndpoint(context.Background(), EndpointMessages, "openai", alias, original, translated, frame, &state)
		if err != nil {
			t.Fatalf("translate Claude model frame: %v", err)
		}
		output = append(output, translatedFrames...)
	}
	if len(output) == 0 {
		t.Fatal("Claude stream emitted no Chat chunks")
	}
	for index, chunk := range output {
		if got := gjson.GetBytes(chunk, "model").String(); got != upstreamModel {
			t.Fatalf("Chat chunk %d model = %q, want upstream model %q: %s", index, got, upstreamModel, chunk)
		}
	}
}
