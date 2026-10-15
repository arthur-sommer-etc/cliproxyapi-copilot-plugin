package translate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestResponsesClaudeStreamUsesTerminalToolIDsAndArguments(t *testing.T) {
	state := new(any)
	var frames [][]byte
	for _, event := range []struct {
		name   string
		fields map[string]any
	}{
		{name: "response.created", fields: map[string]any{"response": map[string]any{"id": "resp_1", "status": "in_progress", "model": "gpt-test"}}},
		{name: "response.output_item.added", fields: map[string]any{"output_index": 0, "item": map[string]any{"id": "reason_added", "type": "reasoning", "summary": []any{}}}},
		{name: "response.reasoning_summary_text.delta", fields: map[string]any{"output_index": 0, "item_id": "reason_added", "delta": "Plan."}},
		{name: "response.output_item.done", fields: map[string]any{"output_index": 0, "item": map[string]any{"id": "reason_done", "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "Plan."}}}}},
		{name: "response.output_item.added", fields: map[string]any{"output_index": 1, "item": map[string]any{"id": "added_item_one_opaque", "type": "function_call", "call_id": "added_call_one_opaque", "name": "lookup", "arguments": ""}}},
		{name: "response.function_call_arguments.delta", fields: map[string]any{"output_index": 1, "item_id": "added_item_one_opaque", "call_id": "delta_call_one_opaque", "name": "lookup", "delta": `{"q":`}},
		{name: "response.output_item.done", fields: map[string]any{"output_index": 1, "item": map[string]any{"id": "done_item_one_opaque", "type": "function_call", "call_id": "done_call_one_opaque", "name": "lookup", "arguments": `{"q":"x"}`}}},
		{name: "response.output_item.added", fields: map[string]any{"output_index": 2, "item": map[string]any{"id": "added_item_two_opaque", "type": "custom_tool_call", "call_id": "added_call_two_opaque", "name": "write_file", "input": map[string]any{}}}},
		{name: "response.custom_tool_call_input.delta", fields: map[string]any{"output_index": 2, "item_id": "added_item_two_opaque", "call_id": "delta_call_two_opaque", "name": "write_file", "delta": `{"path":`}},
		{name: "response.output_item.done", fields: map[string]any{"output_index": 2, "item": map[string]any{"id": "done_item_two_opaque", "type": "custom_tool_call", "call_id": "done_call_two_opaque", "name": "write_file", "input": map[string]any{"path": "/tmp/output.txt"}}}},
	} {
		translated := translateClaudeEvent(t, state, event.name, event.fields)
		for _, frame := range translated {
			if gjson.GetBytes(frame, "data.type").String() == "content_block_start" && gjson.GetBytes(frame, "data.content_block.type").String() == "tool_use" {
				t.Fatalf("tool_use was emitted before response.completed: %s", frame)
			}
			for _, intermediate := range []string{"added_item_one_opaque", "added_call_one_opaque", "delta_call_one_opaque", "done_item_one_opaque", "added_item_two_opaque", "added_call_two_opaque", "delta_call_two_opaque", "done_item_two_opaque"} {
				if strings.Contains(string(frame), intermediate) {
					t.Fatalf("intermediate tool identity leaked before terminal response: %s", frame)
				}
			}
		}
		frames = append(frames, translated...)
	}
	completed := map[string]any{"id": "resp_1", "status": "completed", "model": "gpt-test", "output": []any{
		map[string]any{"id": "reason_terminal", "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "Plan."}}},
		map[string]any{"id": "terminal_item_one_opaque", "type": "function_call", "call_id": "terminal_call_one_opaque", "name": "lookup", "arguments": `{"q":"x"}`},
		map[string]any{"id": "terminal_item_two_opaque", "type": "custom_tool_call", "call_id": "terminal_call_two_opaque", "name": "write_file", "input": map[string]any{"path": "/tmp/output.txt"}},
	}, "usage": map[string]any{"input_tokens": 8, "output_tokens": 4}}
	frames = append(frames, translateClaudeEvent(t, state, "response.completed", map[string]any{"response": completed})...)
	var toolIDs []string
	toolArguments := make(map[int][]string)
	var sawThinkingStart bool
	var sawThinkingDelta bool
	var sawThinkingStop bool
	for _, frame := range frames {
		payload := claudeStreamPayload(t, frame)
		switch payload.Get("type").String() {
		case "content_block_start":
			block := payload.Get("content_block")
			if block.Get("type").String() == "thinking" {
				sawThinkingStart = true
			}
			if block.Get("type").String() == "tool_use" {
				toolIDs = append(toolIDs, block.Get("id").String())
			}
		case "content_block_delta":
			delta := payload.Get("delta")
			if delta.Get("type").String() == "thinking_delta" && delta.Get("thinking").String() == "Plan." {
				sawThinkingDelta = true
			}
			if delta.Get("type").String() == "input_json_delta" {
				index := int(payload.Get("index").Int())
				toolArguments[index] = append(toolArguments[index], delta.Get("partial_json").String())
			}
		case "content_block_stop":
			if payload.Get("index").Int() == 0 {
				sawThinkingStop = true
			}
		}
	}
	if !sawThinkingStart || !sawThinkingDelta || !sawThinkingStop {
		t.Fatalf("reasoning stopped streaming: start=%v delta=%v stop=%v frames=%q", sawThinkingStart, sawThinkingDelta, sawThinkingStop, frames)
	}
	if len(toolIDs) != 2 {
		t.Fatalf("terminal tool count = %d; frames=%q", len(toolIDs), frames)
	}
	wantIdentities := [][2]string{{"terminal_item_one_opaque", "terminal_call_one_opaque"}, {"terminal_item_two_opaque", "terminal_call_two_opaque"}}
	for index, toolID := range toolIDs {
		itemID, callID, ok := DecodeClaudeToolIDs(toolID)
		if !ok || itemID != wantIdentities[index][0] || callID != wantIdentities[index][1] {
			t.Fatalf("tool %d identity = %q / %q / %v", index, itemID, callID, ok)
		}
	}
	if got := strings.Join(toolArguments[1], ""); got != `{"q":"x"}` {
		t.Fatalf("function arguments = %q", got)
	}
	if got := strings.Join(toolArguments[2], ""); got != `{"path":"/tmp/output.txt"}` {
		t.Fatalf("custom tool input = %q", got)
	}
	followup, err := json.Marshal(map[string]any{"messages": []any{
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": toolIDs[0], "name": "lookup", "input": map[string]any{"q": "x"}}}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": toolIDs[0], "content": "found"}}},
	}})
	if err != nil {
		t.Fatalf("encode follow-up Claude request: %v", err)
	}
	replayed, err := RequestForEndpointFrom("claude", "gpt-test", followup, false, EndpointResponses)
	if err != nil {
		t.Fatalf("translate follow-up Claude request: %v", err)
	}
	if got := gjson.GetBytes(replayed, "input.0.id").String(); got != "terminal_item_one_opaque" {
		t.Fatalf("replayed terminal item ID = %q, body=%s", got, replayed)
	}
	if got := gjson.GetBytes(replayed, "input.0.call_id").String(); got != "terminal_call_one_opaque" || gjson.GetBytes(replayed, "input.1.call_id").String() != got {
		t.Fatalf("replayed terminal call ID = %q, body=%s", got, replayed)
	}
	if claudeStreamPayload(t, frames[len(frames)-2]).Get("delta.stop_reason").String() != "tool_use" || claudeStreamPayload(t, frames[len(frames)-1]).Get("type").String() != "message_stop" {
		t.Fatalf("terminal Claude events are missing: %q %q", frames[len(frames)-2], frames[len(frames)-1])
	}
}

func TestResponsesClaudeStreamSupportsTerminalOnlyToolItems(t *testing.T) {
	state := new(any)
	response := map[string]any{"id": "resp_2", "status": "completed", "output": []any{
		map[string]any{"id": "terminal_item", "type": "function_call", "call_id": "terminal_call", "name": "lookup", "arguments": `{"q":"x"}`},
	}}
	frames := translateClaudeEvent(t, state, "response.completed", map[string]any{"response": response})
	var toolID string
	for _, frame := range frames {
		payload := claudeStreamPayload(t, frame)
		if payload.Get("content_block.type").String() == "tool_use" {
			toolID = payload.Get("content_block.id").String()
		}
	}
	itemID, callID, ok := DecodeClaudeToolIDs(toolID)
	if !ok || itemID != "terminal_item" || callID != "terminal_call" {
		t.Fatalf("terminal-only tool identity = %q/%q/%v, frames=%q", itemID, callID, ok, frames)
	}
}

func TestResponsesClaudeStreamWrapsCustomFreeformInput(t *testing.T) {
	state := new(any)
	response := map[string]any{"id": "resp_custom", "status": "completed", "output": []any{
		map[string]any{"id": "terminal_item", "type": "custom_tool_call", "call_id": "terminal_call", "name": "run", "input": "echo hello"},
	}}
	frames := translateClaudeEvent(t, state, "response.completed", map[string]any{"response": response})
	var arguments string
	for _, frame := range frames {
		payload := claudeStreamPayload(t, frame)
		if payload.Get("delta.type").String() == "input_json_delta" {
			arguments += payload.Get("delta.partial_json").String()
		}
	}
	if arguments != `{"input":"echo hello"}` {
		t.Fatalf("wrapped custom tool input = %q", arguments)
	}
}

func TestResponsesClaudeStreamPreservesTerminalOutputOrderAfterTool(t *testing.T) {
	state := new(any)
	frames := translateClaudeEvent(t, state, "response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"id": "early_item", "type": "function_call", "call_id": "early_call", "name": "lookup"}})
	if buffered := translateClaudeEvent(t, state, "response.output_text.delta", map[string]any{"output_index": 1, "content_index": 0, "delta": "after tool"}); len(buffered) != 0 {
		t.Fatalf("text after the first tool was emitted before terminal reconciliation: %q", buffered)
	}
	if buffered := translateClaudeEvent(t, state, "response.reasoning_summary_text.delta", map[string]any{"output_index": 2, "delta": "also after tool"}); len(buffered) != 0 {
		t.Fatalf("reasoning after the first tool was emitted before terminal reconciliation: %q", buffered)
	}
	completed := map[string]any{"id": "resp_order", "status": "completed", "output": []any{
		map[string]any{"id": "terminal_item", "type": "function_call", "call_id": "terminal_call", "name": "lookup", "arguments": `{"q":"x"}`},
		map[string]any{"id": "message_item", "type": "message", "content": []any{map[string]any{"type": "output_text", "text": "after tool"}}},
		map[string]any{"id": "reason_item", "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "also after tool"}}},
	}}
	frames = append(frames, translateClaudeEvent(t, state, "response.completed", map[string]any{"response": completed})...)
	var starts []string
	for _, frame := range frames {
		payload := claudeStreamPayload(t, frame)
		if payload.Get("type").String() == "content_block_start" {
			starts = append(starts, payload.Get("content_block.type").String())
		}
	}
	if got := strings.Join(starts, ","); got != "tool_use,text,thinking" {
		t.Fatalf("terminal Claude block order = %q, want tool_use,text,thinking; frames=%q", got, frames)
	}
}

func TestResponsesClaudeStreamNeverEmitsToolsWithoutSuccessfulTerminal(t *testing.T) {
	for _, terminal := range []struct {
		name    string
		event   string
		payload string
	}{
		{name: "failed", event: "response.failed", payload: `{"type":"response.failed","response":{"status":"failed"}}`},
		{name: "incomplete", event: "response.incomplete", payload: `{"type":"response.incomplete","response":{"status":"incomplete","output":[{"type":"function_call","id":"terminal_item","call_id":"terminal_call","name":"lookup","arguments":"{}"}]}}`},
		{name: "missing terminal output", event: "response.completed", payload: `{"type":"response.completed","response":{"status":"completed","output":[]}}`},
		{name: "invalid terminal arguments", event: "response.completed", payload: `{"type":"response.completed","response":{"status":"completed","output":[{"id":"terminal_item","type":"function_call","call_id":"terminal_call","name":"lookup","arguments":"[]"}]}}`},
	} {
		t.Run(terminal.name, func(t *testing.T) {
			state := new(any)
			added := translateClaudeEvent(t, state, "response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"id": "intermediate_item", "type": "function_call", "call_id": "intermediate_call", "name": "lookup"}})
			if containsClaudeToolStart(added) {
				t.Fatal("tool was emitted before the terminal response")
			}
			frames, err := StreamFromEndpoint(context.Background(), EndpointResponses, "claude", "gpt-test", nil, nil, []byte("event: "+terminal.event+"\ndata: "+terminal.payload+"\n\n"), state)
			if err == nil || len(frames) != 0 {
				t.Fatalf("unsuccessful or missing terminal tool output accepted: frames=%q err=%v", frames, err)
			}
			if terminal.name == "missing terminal output" && !strings.Contains(err.Error(), "without a terminal tool item") {
				t.Fatalf("completed response error = %v", err)
			}
			if terminal.name == "invalid terminal arguments" && !strings.Contains(err.Error(), "arguments are not a JSON object") {
				t.Fatalf("invalid terminal arguments error = %v", err)
			}
		})
	}
	state := new(any)
	translateClaudeEvent(t, state, "response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"id": "intermediate_zero", "type": "function_call", "call_id": "intermediate_call_zero", "name": "lookup"}})
	translateClaudeEvent(t, state, "response.output_item.added", map[string]any{"output_index": 1, "item": map[string]any{"id": "intermediate_one", "type": "function_call", "call_id": "intermediate_call_one", "name": "lookup"}})
	terminalData := `{"type":"response.completed","response":{"status":"completed","output":[{"id":"terminal_zero","type":"function_call","call_id":"terminal_call_zero","name":"lookup","arguments":"{}"}]}}`
	frames, err := StreamFromEndpoint(context.Background(), EndpointResponses, "claude", "gpt-test", nil, nil, []byte(fmt.Sprintf("event: response.completed\ndata: %s\n\n", terminalData)), state)
	if err == nil || len(frames) != 0 || !strings.Contains(err.Error(), "output index 1") {
		t.Fatalf("partial terminal output accepted: frames=%q err=%v", frames, err)
	}
	state = new(any)
	added := translateClaudeEvent(t, state, "response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"id": "intermediate_item", "type": "function_call", "call_id": "intermediate_call", "name": "lookup"}})
	if containsClaudeToolStart(added) {
		t.Fatal("tool was emitted before missing terminal marker")
	}
	frames, err = StreamFromEndpoint(context.Background(), EndpointResponses, "claude", "gpt-test", nil, nil, []byte("data: [DONE]\n\n"), state)
	if err == nil || len(frames) != 0 {
		t.Fatalf("missing terminal marker accepted: frames=%q err=%v", frames, err)
	}
}

func TestResponsesClaudeStreamRejectsToolEventsWithoutOutputIndex(t *testing.T) {
	for _, test := range []struct {
		event   string
		payload string
	}{
		{event: "response.output_item.added", payload: `{"type":"response.output_item.added","item":{"id":"opaque_item","type":"function_call","call_id":"opaque_call","name":"lookup"}}`},
		{event: "response.function_call_arguments.delta", payload: `{"type":"response.function_call_arguments.delta","item_id":"opaque_item","call_id":"opaque_call","name":"lookup","delta":"{}"}`},
	} {
		t.Run(test.event, func(t *testing.T) {
			state := new(any)
			frame := []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", test.event, test.payload))
			if translated, err := StreamFromEndpoint(context.Background(), EndpointResponses, "claude", "gpt-test", nil, nil, frame, state); err == nil || len(translated) != 0 {
				t.Fatalf("tool event without output_index accepted: frames=%q err=%v", translated, err)
			}
		})
	}
}

func translateClaudeEvent(t *testing.T, state *any, event string, fields map[string]any) [][]byte {
	t.Helper()
	fields["type"] = event
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("encode %s event: %v", event, err)
	}
	frame := []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event, data))
	translated, err := StreamFromEndpoint(context.Background(), EndpointResponses, "claude", "gpt-test", nil, nil, frame, state)
	if err != nil {
		t.Fatalf("translate %s event: %v", event, err)
	}
	return translated
}

func containsClaudeToolStart(frames [][]byte) bool {
	for _, frame := range frames {
		payload := claudeStreamPayload(nil, frame)
		if payload.Get("type").String() == "content_block_start" && payload.Get("content_block.type").String() == "tool_use" {
			return true
		}
	}
	return false
}

func claudeStreamPayload(t *testing.T, frame []byte) gjson.Result {
	if t != nil {
		t.Helper()
	}
	_, data, _, err := parseSSEFrame(frame)
	if err != nil {
		if t != nil {
			t.Fatalf("parse Claude SSE frame: %v", err)
		}
		return gjson.Result{}
	}
	return gjson.ParseBytes(data)
}
