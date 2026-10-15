package translate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestChatReasoningOpaqueReplaysToClaudeMessages(t *testing.T) {
	t.Parallel()
	const model = "claude-sonnet-5.5"
	first := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"inspect"}]}`)
	translated, err := RequestForEndpointFrom("openai", model, first, false, EndpointMessages)
	if err != nil {
		t.Fatalf("translate initial Chat request: %v", err)
	}
	response := []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"` + model + `","content":[{"type":"thinking","thinking":"inspect safely","signature":"signed/+ exact"},{"type":"redacted_thinking","data":"redacted/+ exact"},{"type":"text","text":"answer"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`)
	chatResponse, err := ResponseFromEndpoint(context.Background(), EndpointMessages, "openai", model, first, translated, response)
	if err != nil {
		t.Fatalf("translate Claude Messages response: %v", err)
	}
	assistant := gjson.GetBytes(chatResponse, "choices.0.message").Raw
	if !gjson.GetBytes(chatResponse, "choices.0.message.reasoning_opaque").Exists() {
		t.Fatalf("Chat response omitted opaque reasoning: %s", chatResponse)
	}
	followup := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"inspect"},` + assistant + `,{"role":"user","content":"continue"}]}`)
	request, err := RequestForEndpointFrom("openai", model, followup, false, EndpointMessages)
	if err != nil {
		t.Fatalf("translate Chat follow-up: %v", err)
	}
	content := gjson.GetBytes(request, "messages.1.content")
	var signature, thinking, redacted string
	var blockTypes []string
	for _, block := range content.Array() {
		blockTypes = append(blockTypes, block.Get("type").String())
		switch block.Get("type").String() {
		case "thinking":
			thinking = block.Get("thinking").String()
			signature = block.Get("signature").String()
		case "redacted_thinking":
			redacted = block.Get("data").String()
		}
	}
	if thinking != "inspect safely" || signature != "signed/+ exact" || redacted != "redacted/+ exact" {
		t.Fatalf("Claude thinking replay changed: thinking=%q signature=%q redacted=%q request=%s", thinking, signature, redacted, request)
	}
	if len(blockTypes) < 3 || blockTypes[0] != "thinking" || blockTypes[1] != "redacted_thinking" || blockTypes[2] != "text" {
		t.Fatalf("Claude thinking blocks moved after visible output: types=%v request=%s", blockTypes, request)
	}
}

func TestChatOpaqueThinkingReplaysForEmptyAssistantTurn(t *testing.T) {
	t.Parallel()
	const model = "claude-sonnet-5.5"
	request := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"first"},{"role":"assistant","content":"","reasoning_opaque":"[{\"type\":\"reasoning\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"think\"}],\"encrypted_content\":\"sig\"}]"},{"role":"user","content":"second"}]}`)
	translated, err := RequestForEndpointFrom("openai", model, request, false, EndpointMessages)
	if err != nil {
		t.Fatalf("translate Chat request with empty assistant turn: %v", err)
	}
	messages := gjson.GetBytes(translated, "messages").Array()
	if len(messages) != 3 || messages[0].Get("role").String() != "user" || messages[1].Get("role").String() != "assistant" || messages[2].Get("role").String() != "user" {
		t.Fatalf("Claude turn order changed: %s", translated)
	}
	blocks := messages[1].Get("content").Array()
	if len(blocks) != 1 || blocks[0].Get("type").String() != "thinking" || blocks[0].Get("thinking").String() != "think" || blocks[0].Get("signature").String() != "sig" {
		t.Fatalf("empty assistant reasoning was not restored at its turn: %s", translated)
	}
}

func TestChatOpaqueThinkingReplaysAcrossMergedToolAndUserTurns(t *testing.T) {
	t.Parallel()
	const model = "claude-sonnet-5.5"
	prefix := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"matrixInputMarker"},{"role":"assistant","content":"","tool_calls":[{"id":"chat-input-call","type":"function","function":{"name":"inspect","arguments":"{\"query\":\"initial-value\"}"}}]},{"role":"tool","tool_call_id":"chat-input-call","content":"matrixToolResult"},{"role":"user","content":"Continue the protocol matrix."}]}`)
	assistant := chatAssistantWithOpaque(t, model, prefix, []byte(`{"role":"assistant","content":"provider text exact","tool_calls":[{"id":"chat-follow-call","type":"function","function":{"name":"inspect","arguments":"{\"query\":\"provider-value\"}"}}]}`), []byte(`[{"type":"reasoning","summary":[{"type":"summary_text","text":"signed thought"}],"encrypted_content":"signed/+ exact"},{"type":"reasoning","summary":[],"encrypted_content":"`+redactedThinkingPrefix+`opaque/+ exact"}]`))
	request := appendChatMessages(t, prefix, assistant, []byte(`{"role":"tool","tool_call_id":"chat-follow-call","content":"followup tool result"}`), []byte(`{"role":"user","content":"matrixContinuation"}`))
	translated, err := RequestForEndpointFrom("openai", model, request, false, EndpointMessages)
	if err != nil {
		t.Fatalf("translate multi-turn Chat follow-up: %v", err)
	}
	messages := gjson.GetBytes(translated, "messages").Array()
	if len(messages) != 5 {
		t.Fatalf("Claude SDK turn grouping changed: %s", translated)
	}
	if messages[2].Get("role").String() != "user" || messages[2].Get("content.0.type").String() != "tool_result" || messages[2].Get("content.1.text").String() != "Continue the protocol matrix." {
		t.Fatalf("tool result and following user turn did not retain native grouping: %s", translated)
	}
	assistantBlocks := messages[3].Get("content").Array()
	if len(assistantBlocks) != 4 || assistantBlocks[0].Get("type").String() != "thinking" || assistantBlocks[0].Get("signature").String() != "signed/+ exact" || assistantBlocks[1].Get("type").String() != "redacted_thinking" || assistantBlocks[1].Get("data").String() != "opaque/+ exact" || assistantBlocks[2].Get("type").String() != "text" || assistantBlocks[2].Get("text").String() != "provider text exact" {
		t.Fatalf("Claude thinking replay moved or changed: %s", translated)
	}
	if assistantBlocks[3].Get("type").String() != "tool_use" || assistantBlocks[3].Get("id").String() != "chat-follow-call" {
		t.Fatalf("assistant tool call changed: %s", translated)
	}
	if messages[4].Get("role").String() != "user" || messages[4].Get("content.0.tool_use_id").String() != "chat-follow-call" || messages[4].Get("content.1.text").String() != "matrixContinuation" {
		t.Fatalf("tool result and final user turn did not retain native grouping: %s", translated)
	}
}

func TestChatOpaqueThinkingKeepsOrderInGroupedAssistantTurns(t *testing.T) {
	t.Parallel()
	const model = "claude-sonnet-5.5"
	prefix := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"start"}]}`)
	first := chatAssistantWithOpaque(t, model, prefix, []byte(`{"role":"assistant","content":"first output"}`), []byte(`[{"type":"thinking","thinking":"first thought","signature":"sig-1"}]`))
	prefix = appendChatMessages(t, prefix, first)
	second := chatAssistantWithOpaque(t, model, prefix, []byte(`{"role":"assistant","content":"second output"}`), []byte(`[{"type":"thinking","thinking":"second thought","signature":"sig-2"}]`))
	request := appendChatMessages(t, prefix, second, []byte(`{"role":"user","content":"continue"}`))
	translated, err := RequestForEndpointFrom("openai", model, request, false, EndpointMessages)
	if err != nil {
		t.Fatalf("translate grouped Chat assistant turns: %v", err)
	}
	content := gjson.GetBytes(translated, "messages.1.content").Array()
	if len(content) != 4 || content[0].Get("signature").String() != "sig-1" || content[1].Get("text").String() != "first output" || content[2].Get("signature").String() != "sig-2" || content[3].Get("text").String() != "second output" {
		t.Fatalf("grouped assistant thinking order changed: %s", translated)
	}
}

func TestChatMessagesToolIDsRestoreBufferedCarriersAcrossPriorTurns(t *testing.T) {
	t.Parallel()
	const model = "claude-sonnet-5.5"
	callIDs := []string{
		"toolu_provider_turn_1/+_" + strings.Repeat("p", 96),
		"toolu_provider_turn_2/+_" + strings.Repeat("q", 112),
	}
	request := []byte(`{"model":"` + model + `","tools":[{"type":"function","function":{"name":"inspect","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":"first request"}]}`)
	firstAssistant := bufferedMessagesToolAssistant(t, model, request, callIDs[0])
	firstCarrier := gjson.GetBytes(firstAssistant, "tool_calls.0.id").String()
	request = appendChatMessages(t, request, firstAssistant, []byte(`{"role":"tool","tool_call_id":"`+firstCarrier+`","content":"first result"}`), []byte(`{"role":"user","content":"second request"}`))
	secondAssistant := bufferedMessagesToolAssistant(t, model, request, callIDs[1])
	secondCarrier := gjson.GetBytes(secondAssistant, "tool_calls.0.id").String()
	request = appendChatMessages(t, request, secondAssistant, []byte(`{"role":"tool","tool_call_id":"`+secondCarrier+`","content":"second result"}`), []byte(`{"role":"user","content":"continue"}`))
	translated, err := RequestForEndpointFrom("openai", model, request, false, EndpointMessages)
	if err != nil {
		t.Fatalf("translate Chat history to Messages: %v", err)
	}
	messages := gjson.GetBytes(translated, "messages").Array()
	if len(messages) != 5 {
		t.Fatalf("Claude SDK turn grouping changed: %s", translated)
	}
	gotIDs := []string{
		messages[1].Get("content.0.id").String(),
		messages[2].Get("content.0.tool_use_id").String(),
		messages[3].Get("content.0.id").String(),
		messages[4].Get("content.0.tool_use_id").String(),
	}
	wantIDs := []string{callIDs[0], callIDs[0], callIDs[1], callIDs[1]}
	for index, wantID := range wantIDs {
		if gotIDs[index] != wantID {
			t.Fatalf("Messages tool identity %d = %q, want %q; request=%s", index, gotIDs[index], wantID, translated)
		}
	}
	if messages[1].Get("content.0.type").String() != "tool_use" || messages[2].Get("content.0.type").String() != "tool_result" || messages[3].Get("content.0.type").String() != "tool_use" || messages[4].Get("content.0.type").String() != "tool_result" {
		t.Fatalf("Messages tool blocks changed: %s", translated)
	}
}

func bufferedMessagesToolAssistant(t *testing.T, model string, original []byte, toolID string) []byte {
	t.Helper()
	translated, err := RequestForEndpointFrom("openai", model, original, false, EndpointMessages)
	if err != nil {
		t.Fatalf("translate Chat request to Messages: %v", err)
	}
	response, err := json.Marshal(map[string]any{
		"id":    "msg_" + toolID,
		"type":  "message",
		"role":  "assistant",
		"model": model,
		"content": []any{map[string]any{
			"type":  "tool_use",
			"id":    toolID,
			"name":  "inspect",
			"input": map[string]any{"query": "value"},
		}},
		"stop_reason": "tool_use",
		"usage":       map[string]any{"input_tokens": 1, "output_tokens": 1},
	})
	if err != nil {
		t.Fatalf("encode Messages tool response: %v", err)
	}
	chatResponse, err := ResponseFromEndpoint(context.Background(), EndpointMessages, "openai", model, original, translated, response)
	if err != nil {
		t.Fatalf("translate Messages tool response to Chat: %v", err)
	}
	assistant := gjson.GetBytes(chatResponse, "choices.0.message")
	carrier := assistant.Get("tool_calls.0.id").String()
	_, gotToolID, encoded := DecodeClaudeToolIDs(carrier)
	if !encoded || gotToolID != toolID {
		t.Fatalf("buffered Chat tool ID %q did not preserve Messages ID %q", carrier, toolID)
	}
	return []byte(assistant.Raw)
}

func chatAssistantWithOpaque(t *testing.T, model string, history, assistant, reasoning []byte) []byte {
	t.Helper()
	anchor := copilotAnchorForMessage(gjson.ParseBytes(assistant), history)
	carrier, err := encodeCopilotOpaque(reasoning, model, anchor)
	if err != nil {
		t.Fatalf("encode opaque reasoning fixture: %v", err)
	}
	updated, err := sjson.SetBytes(assistant, "reasoning_opaque", carrier)
	if err != nil {
		t.Fatalf("add opaque reasoning fixture: %v", err)
	}
	return updated
}

func appendChatMessages(t *testing.T, request []byte, added ...[]byte) []byte {
	t.Helper()
	messages := chatMessageBytes(gjson.GetBytes(request, "messages").Array())
	messages = append(messages, added...)
	updated, err := sjson.SetRawBytes(request, "messages", rawJSONArray(messages))
	if err != nil {
		t.Fatalf("append Chat message fixtures: %v", err)
	}
	return updated
}
