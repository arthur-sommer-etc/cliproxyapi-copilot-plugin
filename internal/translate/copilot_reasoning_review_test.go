package translate

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestCopilotReplayRequiresEarlierThinking(t *testing.T) {
	for _, destination := range []string{"claude", "openai-response"} {
		t.Run(destination, func(t *testing.T) {
			replay := testReplay()
			request := initialReasoningRequest(destination)
			for range 2 {
				chat, err := replay.Request(destination, "gemini-3.8-flash", request, false, EndpointChatCompletions)
				if err != nil {
					t.Fatal(err)
				}
				response, err := replay.Response(context.Background(), EndpointChatCompletions, destination, "gemini-3.8-flash", request, chat, []byte(`{"id":"chat_1","choices":[{"index":0,"message":{"role":"assistant","content":"answer","reasoning_text":"plan","reasoning_opaque":"synthetic-opaque"},"finish_reason":"stop"}]}`))
				if err != nil {
					t.Fatal(err)
				}
				request = nextReasoningRequest(t, destination, request, response)
			}
			chat, err := replay.Request(destination, "gemini-3.8-flash", request, false, EndpointChatCompletions)
			if err != nil {
				t.Fatal(err)
			}
			for _, index := range []string{"1", "3"} {
				if gjson.GetBytes(chat, "messages."+index+".reasoning_text").String() != "plan" || gjson.GetBytes(chat, "messages."+index+".reasoning_opaque").String() != "synthetic-opaque" {
					t.Fatalf("assistant %s = %s", index, gjson.GetBytes(chat, "messages."+index).Raw)
				}
			}
			root, err := decodeObject(request)
			if err != nil {
				t.Fatal(err)
			}
			if destination == "claude" {
				message := objectValue(root["messages"].([]any)[1])
				var content []any
				for _, raw := range message["content"].([]any) {
					if stringValue(objectValue(raw)["type"]) != "thinking" {
						content = append(content, raw)
					}
				}
				message["content"] = content
			} else {
				var input []any
				removed := false
				for _, raw := range root["input"].([]any) {
					if !removed && stringValue(objectValue(raw)["type"]) == "reasoning" {
						removed = true
					} else {
						input = append(input, raw)
					}
				}
				root["input"] = input
			}
			changed, err := json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			out, err := replay.Request(destination, "gemini-3.8-flash", changed, false, EndpointChatCompletions)
			if out != nil || !errors.Is(err, ErrCopilotReasoning) {
				t.Fatalf("payload present = %v, error = %v", out != nil, err)
			}
		})
	}
}

func TestCopilotSeparatedThinkingSegmentsRoundTrip(t *testing.T) {
	for _, destination := range []string{"claude", "openai-response"} {
		t.Run(destination, func(t *testing.T) {
			replay := testReplay()
			request := initialReasoningRequest(destination)
			chat, err := replay.Request(destination, "gemini-3.8-flash", request, true, EndpointChatCompletions)
			if err != nil {
				t.Fatal(err)
			}
			var state CopilotChatStream
			frames := reasoningStreamFrames(true)
			frames[0] = []byte(`data: {"id":"chat_1","choices":[{"index":0,"delta":{"role":"assistant","reasoning_text":"a"}}]}` + "\n\n")
			frames = append(frames[:3], append([][]byte{[]byte(`data: {"id":"chat_1","choices":[{"index":0,"delta":{"reasoning_text":"b"}}]}` + "\n\n")}, frames[3:]...)...)
			var output [][]byte
			for _, frame := range frames {
				converted, err := replay.Stream(context.Background(), destination, "gemini-3.8-flash", request, chat, frame, &state)
				if err != nil {
					t.Fatal(err)
				}
				output = append(output, converted...)
			}
			converted, err := replay.FinishStream(context.Background(), destination, "gemini-3.8-flash", request, chat, &state)
			if err != nil {
				t.Fatal(err)
			}
			response := assembledReasoningResponse(t, destination, append(output, converted...))
			next := nextReasoningRequest(t, destination, request, response)
			restored, err := replay.Request(destination, "gemini-3.8-flash", next, false, EndpointChatCompletions)
			if err != nil {
				t.Fatal(err)
			}
			assertRestoredAssistant(t, restored, `{"role":"assistant","content":"answer","reasoning_text":"ab","reasoning_opaque":"synthetic-opaque"}`)
		})
	}
}

func TestCopilotTokenLimitSemantics(t *testing.T) {
	for _, destination := range []string{"claude", "openai-response"} {
		t.Run(destination, func(t *testing.T) {
			replay := testReplay()
			request := initialReasoningRequest(destination)
			chat, err := replay.Request(destination, "gemini-3.8-flash", request, true, EndpointChatCompletions)
			if err != nil {
				t.Fatal(err)
			}
			body, err := replay.Response(context.Background(), EndpointChatCompletions, destination, "gemini-3.8-flash", request, chat, []byte(`{"id":"chat_1","choices":[{"index":0,"message":{"role":"assistant","content":"answer","reasoning_text":"plan","reasoning_opaque":"synthetic-opaque"},"finish_reason":"length"}]}`))
			if err != nil {
				t.Fatal(err)
			}
			assertTokenLimit(t, destination, body)
			for _, thinking := range []bool{false, true} {
				frames := reasoningStreamFrames(true)
				if thinking {
					frames = reasoningStreamFrames(false)
				} else {
					frames[len(frames)-3] = []byte(`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n")
				}
				var state CopilotChatStream
				var output [][]byte
				for _, frame := range frames {
					frame = []byte(strings.ReplaceAll(string(frame), `"finish_reason":"stop"`, `"finish_reason":"length"`))
					converted, err := replay.Stream(context.Background(), destination, "gemini-3.8-flash", request, chat, frame, &state)
					if err != nil {
						t.Fatal(err)
					}
					output = append(output, converted...)
				}
				converted, err := replay.FinishStream(context.Background(), destination, "gemini-3.8-flash", request, chat, &state)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, frame := range append(output, converted...) {
					_, data, _, err := parseSSEFrame(frame)
					if err != nil {
						t.Fatal(err)
					}
					kind := gjson.GetBytes(data, "type").String()
					if destination == "claude" && kind == "message_delta" {
						assertTokenLimit(t, destination, []byte(gjson.GetBytes(data, "delta").Raw))
						found = true
					} else if destination == "openai-response" && (kind == "response.completed" || kind == "response.incomplete") {
						if kind != "response.incomplete" {
							t.Fatalf("terminal type = %q", kind)
						}
						assertTokenLimit(t, destination, []byte(gjson.GetBytes(data, "response").Raw))
						found = true
					}
				}
				if !found {
					t.Fatal("missing terminal fields")
				}
			}
		})
	}
}

func assertTokenLimit(t *testing.T, destination string, body []byte) {
	t.Helper()
	if destination == "claude" {
		if got := gjson.GetBytes(body, "stop_reason").String(); got != "max_tokens" {
			t.Fatalf("stop_reason = %q", got)
		}
	} else if gjson.GetBytes(body, "status").String() != "incomplete" || gjson.GetBytes(body, "incomplete_details.reason").String() != "max_output_tokens" {
		t.Fatalf("terminal fields = %s", body)
	}
}

func TestCopilotCombinedThinkingStreamRoundTrip(t *testing.T) {
	for _, destination := range []string{"claude", "openai-response"} {
		for _, priorContent := range []bool{false, true} {
			t.Run(destination+"/"+map[bool]string{false: "combined role", true: "prior text"}[priorContent], func(t *testing.T) {
				replay := testReplay()
				request := initialReasoningRequest(destination)
				chat, err := replay.Request(destination, "gemini-3.8-flash", request, true, EndpointChatCompletions)
				if err != nil {
					t.Fatal(err)
				}
				frames := reasoningStreamFrames(true)
				frames = append([][]byte{[]byte(`data: {"id":"chat_1","choices":[{"index":0,"delta":{"role":"assistant","reasoning_text":"plan","content":"answer"}}]}` + "\n\n")}, frames[3:]...)
				if priorContent {
					frames = append([][]byte{[]byte(`data: {"id":"chat_1","choices":[{"index":0,"delta":{"role":"assistant","content":"first"}}]}` + "\n\n")}, frames...)
				}
				var state CopilotChatStream
				var output [][]byte
				for index, frame := range frames {
					converted, err := replay.Stream(context.Background(), destination, "gemini-3.8-flash", request, chat, frame, &state)
					if err != nil {
						t.Fatal(err)
					}
					output = append(output, converted...)
					if priorContent && index == 0 {
						var text string
						for _, frame := range converted {
							_, data, _, _ := parseSSEFrame(frame)
							if gjson.GetBytes(data, "type").String() == "content_block_delta" {
								text += gjson.GetBytes(data, "delta.text").String()
							} else if gjson.GetBytes(data, "type").String() == "response.output_text.delta" {
								text += gjson.GetBytes(data, "delta").String()
							}
						}
						if text != "first" {
							t.Fatalf("early content = %q", text)
						}
					}
				}
				converted, err := replay.FinishStream(context.Background(), destination, "gemini-3.8-flash", request, chat, &state)
				if err != nil {
					t.Fatal(err)
				}
				response := assembledReasoningResponse(t, destination, append(output, converted...))
				next := nextReasoningRequest(t, destination, request, response)
				restored, err := replay.Request(destination, "gemini-3.8-flash", next, false, EndpointChatCompletions)
				if err != nil {
					t.Fatal(err)
				}
				want := `{"role":"assistant","content":"answer","reasoning_text":"plan","reasoning_opaque":"synthetic-opaque"}`
				if priorContent {
					want = `{"role":"assistant","content":"firstanswer","reasoning_text":"plan","reasoning_opaque":"synthetic-opaque"}`
				}
				assertRestoredAssistant(t, restored, want)
			})
		}
	}
}

func TestCopilotRejectsMalformedToolArguments(t *testing.T) {
	replay := testReplay()
	response := []byte(`{"id":"chat_1","choices":[{"index":0,"message":{"role":"assistant","content":null,"reasoning_text":"plan","tool_calls":[{"id":"call_a","type":"function","function":{"name":"first","arguments":"{\"n\":1}garbage"}}]},"finish_reason":"tool_calls"}]}`)
	_, err := replay.Response(context.Background(), EndpointChatCompletions, "claude", "gemini-3.8-flash", nil, []byte(`{"messages":[]}`), response)
	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) {
		t.Fatalf("error type = %T", err)
	}
}
