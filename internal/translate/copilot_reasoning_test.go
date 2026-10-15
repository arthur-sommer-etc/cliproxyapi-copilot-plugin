package translate

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestCopilotGeminiThinkingResponse(t *testing.T) {
	request := []byte(`{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"inspect"}]}`)
	response := []byte(`{"id":"chat_1","model":"gemini-3.8-flash","choices":[{"index":0,"message":{"role":"assistant","content":"answer","reasoning_text":"plan","reasoning_opaque":"synthetic-opaque"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`)
	replay := NewCopilotReplay("account", "gemini-3.8-flash", EndpointChatCompletions, "https://api.example.invalid", "synthetic-credential")
	out, err := replay.Response(context.Background(), EndpointChatCompletions, "openai-response", "gemini-3.8-flash", nil, request, response)
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(out, `output.#(type=="reasoning").summary.0.text`).String(); got != "plan" {
		t.Fatalf("reasoning summary = %q", got)
	}
	if got := gjson.GetBytes(out, `output.#(type=="reasoning").encrypted_content`).String(); got == "" {
		t.Fatal("reasoning carrier is empty")
	}
}

func TestCopilotGeminiThinkingRoundTrip(t *testing.T) {
	for _, destination := range []string{"claude", "openai-response"} {
		for _, scenario := range []struct {
			name    string
			message string
		}{
			{"answer", `{"role":"assistant","content":"answer","reasoning_text":" plan\n","reasoning_opaque":"synthetic-opaque"}`},
			{"parallel tools", `{"role":"assistant","content":"checking","reasoning_text":"plan","reasoning_opaque":{"synthetic":"state"},"tool_calls":[{"id":"call_a","type":"function","function":{"name":"first","arguments":"{\"n\":1}"}},{"id":"call_b","type":"function","function":{"name":"second","arguments":"{\"n\":2}"}}]}`},
			{"text without opaque", `{"role":"assistant","content":"answer","reasoning_text":"plan"}`},
			{"opaque without text", `{"role":"assistant","content":"answer","reasoning_opaque":"synthetic-opaque"}`},
			{"structured opaque integer", `{"role":"assistant","content":"answer","reasoning_text":"plan","reasoning_opaque":{"counter":9007199254740993}}`},
			{"thinking only", `{"role":"assistant","content":null,"reasoning_text":"plan","reasoning_opaque":"synthetic-opaque"}`},
			{"tools without answer", `{"role":"assistant","content":null,"reasoning_text":"plan","reasoning_opaque":"synthetic-opaque","tool_calls":[{"id":"call_a","type":"function","function":{"name":"first","arguments":"{\"n\":1}"}},{"id":"call_b","type":"function","function":{"name":"second","arguments":"{\"n\":2}"}}]}`},
		} {
			t.Run(destination+"/"+scenario.name, func(t *testing.T) {
				replay := testReplay()
				initial := initialReasoningRequest(destination)
				chat, err := replay.Request(destination, "gemini-3.8-flash", initial, false, EndpointChatCompletions)
				if err != nil {
					t.Fatal(err)
				}
				upstream := []byte(`{"id":"chat_1","model":"gemini-3.8-flash","choices":[{"index":0,"message":` + scenario.message + `,"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`)
				response, err := replay.Response(context.Background(), EndpointChatCompletions, destination, "gemini-3.8-flash", initial, chat, upstream)
				if err != nil {
					t.Fatal(err)
				}
				next := nextReasoningRequest(t, destination, initial, response)
				out, err := replay.Request(destination, "gemini-3.8-flash", next, false, EndpointChatCompletions)
				if err != nil {
					t.Fatal(err)
				}
				assertRestoredAssistant(t, out, scenario.message)
			})
		}
	}
}

func TestCopilotGeminiThinkingStreamFailures(t *testing.T) {
	valid := reasoningStreamFrames(false)
	for _, test := range []struct {
		name   string
		frames [][]byte
	}{
		{"missing done", valid[:len(valid)-1]},
		{"duplicate done", append(append([][]byte{}, valid...), []byte("data: [DONE]\n\n"))},
		{"malformed trailing frame", append(append([][]byte{}, valid...), []byte("data: {\n\n"))},
		{"overflow", [][]byte{[]byte(`data: {"choices":[{"index":0,"delta":{"reasoning_text":"` + strings.Repeat("x", maxCopilotReasoningStreamBytes) + `"}}]}`)}},
		{"provider error", [][]byte{valid[0], []byte(`data: {"error":{"code":"synthetic_error"}}`)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			replay := testReplay()
			initial := initialReasoningRequest("openai-response")
			chat, err := replay.Request("openai-response", "gemini-3.8-flash", initial, true, EndpointChatCompletions)
			if err != nil {
				t.Fatal(err)
			}
			var state CopilotChatStream
			for _, frame := range test.frames {
				_, err = replay.Stream(context.Background(), "openai-response", "gemini-3.8-flash", initial, chat, frame, &state)
				if err != nil {
					break
				}
			}
			frames, finishErr := replay.FinishStream(context.Background(), "openai-response", "gemini-3.8-flash", initial, chat, &state)
			if frames != nil || !errors.Is(finishErr, ErrCopilotReasoning) {
				t.Fatalf("frames present = %v, finish error = %v, stream error = %v", frames != nil, finishErr, err)
			}
		})
	}
}

func TestCopilotGeminiThinkingRejectsChangedReplay(t *testing.T) {
	for _, destination := range []string{"claude", "openai-response"} {
		replay := testReplay()
		initial := initialReasoningRequest(destination)
		chat, err := replay.Request(destination, "gemini-3.8-flash", initial, false, EndpointChatCompletions)
		if err != nil {
			t.Fatal(err)
		}
		upstream := []byte(`{"id":"chat_1","choices":[{"index":0,"message":{"role":"assistant","content":"answer","reasoning_text":"plan","reasoning_opaque":"synthetic-opaque"},"finish_reason":"stop"}]}`)
		response, err := replay.Response(context.Background(), EndpointChatCompletions, destination, "gemini-3.8-flash", initial, chat, upstream)
		if err != nil {
			t.Fatal(err)
		}
		next := nextReasoningRequest(t, destination, initial, response)
		for _, test := range []struct {
			name     string
			replay   *CopilotReplay
			body     []byte
			endpoint string
		}{
			{"credential", NewCopilotReplay("account", "gemini-3.8-flash", EndpointChatCompletions, "https://api.example.invalid", "other-synthetic-credential"), next, EndpointChatCompletions},
			{"account", NewCopilotReplay("other", "gemini-3.8-flash", EndpointChatCompletions, "https://api.example.invalid", "synthetic-credential"), next, EndpointChatCompletions},
			{"model", NewCopilotReplay("account", "other-model", EndpointChatCompletions, "https://api.example.invalid", "synthetic-credential"), next, EndpointChatCompletions},
			{"origin", NewCopilotReplay("account", "gemini-3.8-flash", EndpointChatCompletions, "https://other.example.invalid", "synthetic-credential"), next, EndpointChatCompletions},
			{"endpoint", replay, next, EndpointMessages},
			{"prior user", replay, []byte(strings.ReplaceAll(string(next), "inspect", "changed")), EndpointChatCompletions},
			{"answer", replay, []byte(strings.ReplaceAll(string(next), "answer", "changed")), EndpointChatCompletions},
			{"thinking", replay, []byte(strings.ReplaceAll(string(next), "plan", "changed")), EndpointChatCompletions},
			{"signature", replay, []byte(strings.ReplaceAll(string(next), copilotReplayPrefix, copilotReplayPrefix+"x")), EndpointChatCompletions},
		} {
			t.Run(destination+"/"+test.name, func(t *testing.T) {
				out, err := test.replay.Request(destination, "gemini-3.8-flash", test.body, false, test.endpoint)
				if out != nil || !errors.Is(err, ErrCopilotReasoning) {
					t.Fatalf("payload present = %v, error = %v", out != nil, err)
				}
			})
		}
	}
}

func TestCopilotGeminiThinkingStreamRoundTrip(t *testing.T) {
	for _, destination := range []string{"claude", "openai-response"} {
		for _, opaqueOnly := range []bool{false, true} {
			t.Run(destination+"/"+map[bool]string{false: "reasoning and parallel tools", true: "opaque only"}[opaqueOnly], func(t *testing.T) {
				replay := testReplay()
				initial := initialReasoningRequest(destination)
				chat, err := replay.Request(destination, "gemini-3.8-flash", initial, true, EndpointChatCompletions)
				if err != nil {
					t.Fatal(err)
				}
				var state CopilotChatStream
				var output [][]byte
				for _, frame := range reasoningStreamFrames(opaqueOnly) {
					frames, err := replay.Stream(context.Background(), destination, "gemini-3.8-flash", initial, chat, frame, &state)
					if err != nil {
						t.Fatal(err)
					}
					output = append(output, frames...)
				}
				for _, frame := range output {
					_, payload, _, _ := parseSSEFrame(frame)
					kind := gjson.GetBytes(payload, "type").String()
					if kind == "response.completed" || kind == "message_stop" {
						t.Fatalf("terminal event before EOF: %q", kind)
					}
				}
				frames, err := replay.FinishStream(context.Background(), destination, "gemini-3.8-flash", initial, chat, &state)
				if err != nil {
					t.Fatal(err)
				}
				output = append(output, frames...)
				response := assembledReasoningResponse(t, destination, output)
				next := nextReasoningRequest(t, destination, initial, response)
				restored, err := replay.Request(destination, "gemini-3.8-flash", next, false, EndpointChatCompletions)
				if err != nil {
					t.Fatal(err)
				}
				want := `{"role":"assistant","content":"answer","reasoning_opaque":"synthetic-opaque"}`
				if !opaqueOnly {
					want = `{"role":"assistant","content":"answer","reasoning_text":"plan","reasoning_opaque":"synthetic-opaque","tool_calls":[{"id":"call_a","type":"function","function":{"name":"first","arguments":"{\"n\":1}"}},{"id":"call_b","type":"function","function":{"name":"second","arguments":"{\"n\":2}"}}]}`
				}
				assertRestoredAssistant(t, restored, want)
			})
		}
	}
}

func testReplay() *CopilotReplay {
	return NewCopilotReplay("account", "gemini-3.8-flash", EndpointChatCompletions, "https://api.example.invalid", "synthetic-credential")
}

func initialReasoningRequest(destination string) []byte {
	if destination == "claude" {
		return []byte(`{"model":"gemini-3.8-flash","max_tokens":100,"messages":[{"role":"user","content":"inspect"}]}`)
	}
	return []byte(`{"model":"gemini-3.8-flash","input":[{"role":"user","content":"inspect"}]}`)
}

func nextReasoningRequest(t *testing.T, destination string, initial, response []byte) []byte {
	t.Helper()
	request, err := decodeObject(initial)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := decodeObject(response)
	if err != nil {
		t.Fatal(err)
	}
	if destination == "claude" {
		messages := request["messages"].([]any)
		messages = append(messages, map[string]any{"role": "assistant", "content": reply["content"]})
		var results []any
		for _, raw := range reply["content"].([]any) {
			block := objectValue(raw)
			if stringValue(block["type"]) == "tool_use" {
				results = append(results, map[string]any{"type": "tool_result", "tool_use_id": block["id"], "content": "result"})
			}
		}
		if len(results) == 0 {
			results = []any{map[string]any{"type": "text", "text": "follow up"}}
		}
		request["messages"] = append(messages, map[string]any{"role": "user", "content": results})
	} else {
		items := append(request["input"].([]any), reply["output"].([]any)...)
		for _, raw := range reply["output"].([]any) {
			item := objectValue(raw)
			if stringValue(item["type"]) == "function_call" {
				items = append(items, map[string]any{"type": "function_call_output", "call_id": item["call_id"], "output": "result"})
			}
		}
		request["input"] = append(items, map[string]any{"role": "user", "content": "follow up"})
	}
	out, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func assertRestoredAssistant(t *testing.T, body []byte, want string) {
	t.Helper()
	root, err := decodeObject(body)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := decodeObject([]byte(want))
	if err != nil {
		t.Fatal(err)
	}
	var actual []map[string]any
	var resultIDs []string
	for _, raw := range root["messages"].([]any) {
		message := objectValue(raw)
		if stringValue(message["role"]) == "assistant" {
			actual = append(actual, message)
		} else if stringValue(message["role"]) == "tool" {
			resultIDs = append(resultIDs, stringValue(message["tool_call_id"]))
		}
	}
	if !reflect.DeepEqual(actual, []map[string]any{expected}) {
		t.Fatalf("assistant fields = %#v, want %#v", actual, expected)
	}
	if expected["tool_calls"] != nil && !reflect.DeepEqual(resultIDs, []string{"call_a", "call_b"}) {
		t.Fatalf("tool result IDs = %v", resultIDs)
	}
}

func reasoningStreamFrames(opaqueOnly bool) [][]byte {
	deltas := []string{`{"role":"assistant"}`, `{"content":"ans"}`, `{"content":"wer"}`}
	if !opaqueOnly {
		deltas = []string{`{"role":"assistant","reasoning_text":"pl"}`, `{"reasoning_text":"an"}`, `{"content":"ans"}`, `{"content":"wer"}`, `{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"first","arguments":"{\"n\":"}},{"index":1,"id":"call_b","type":"function","function":{"name":"second","arguments":"{\"n\":2}"}}]}`, `{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}`}
	}
	var frames [][]byte
	for _, delta := range deltas {
		frames = append(frames, []byte(`data: {"id":"chat_1","model":"gemini-3.8-flash","choices":[{"index":0,"delta":`+delta+`,"finish_reason":null}]}`+"\n\n"))
	}
	frames = append(frames, []byte(`data: {"id":"chat_1","choices":[{"index":0,"delta":{"reasoning_opaque":"synthetic-opaque"},"finish_reason":"stop"}]}`+"\n\n"), []byte(`data: {"id":"chat_1","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`+"\n\n"), []byte("data: [DONE]\n\n"))
	return frames
}

func assembledReasoningResponse(t *testing.T, destination string, frames [][]byte) []byte {
	t.Helper()
	var response map[string]any
	var blocks []any
	open := -1
	stopped := 0
	lastSequence := -1
	for _, frame := range frames {
		_, data, _, err := parseSSEFrame(frame)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := decodeObject(data)
		if err != nil {
			t.Fatal(err)
		}
		kind := stringValue(payload["type"])
		if destination == "openai-response" {
			sequence := int(int64Value(payload["sequence_number"]))
			if sequence != lastSequence+1 {
				t.Fatalf("sequence = %d after %d", sequence, lastSequence)
			}
			lastSequence = sequence
			if kind == "response.completed" {
				response = objectValue(payload["response"])
				stopped++
			}
		} else {
			switch kind {
			case "message_start":
				response = objectValue(payload["message"])
			case "content_block_start":
				if open != -1 {
					t.Fatalf("block %d still open", open)
				}
				open = int(int64Value(payload["index"]))
				if open != len(blocks) {
					t.Fatalf("block index = %d, want %d", open, len(blocks))
				}
				blocks = append(blocks, payload["content_block"])
			case "content_block_delta":
				index := int(int64Value(payload["index"]))
				if index != open {
					t.Fatalf("delta index = %d, open = %d", index, open)
				}
				delta := objectValue(payload["delta"])
				block := objectValue(blocks[index])
				for _, field := range []string{"text", "thinking", "signature", "partial_json"} {
					if value, exists := delta[field]; exists {
						block[field] = rawStringValue(block[field]) + rawStringValue(value)
					}
				}
			case "content_block_stop":
				if int(int64Value(payload["index"])) != open {
					t.Fatalf("stop index differs from %d", open)
				}
				block := objectValue(blocks[open])
				if stringValue(block["type"]) == "tool_use" {
					var input any
					if err := json.Unmarshal([]byte(rawStringValue(block["partial_json"])), &input); err != nil {
						t.Fatal(err)
					}
					block["input"] = input
					delete(block, "partial_json")
				}
				open = -1
			case "message_stop":
				if open != -1 {
					t.Fatalf("block %d still open at terminal", open)
				}
				stopped++
			}
		}
	}
	if stopped != 1 {
		t.Fatalf("terminal count = %d", stopped)
	}
	if destination == "claude" {
		response["content"] = blocks
	}
	out, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
