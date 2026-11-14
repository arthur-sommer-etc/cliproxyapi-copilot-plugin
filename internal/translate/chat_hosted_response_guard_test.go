package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func chatGuardClaudeFixture(t *testing.T) map[string]any {
	t.Helper()
	var history map[string]any
	if err := json.Unmarshal([]byte(claudeHostedSearchRequest), &history); err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"id": "msg_chat_guard", "type": "message", "role": "assistant", "model": "claude-haiku-5.5",
		"content":     objectValue(arrayValue(history["messages"])[1])["content"],
		"stop_reason": "end_turn", "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": 12, "output_tokens": 8},
	}
}

func chatGuardJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestChatHostedResponseJSONRejectsReplayState(t *testing.T) {
	ctx := context.Background()
	request := []byte(`{"messages":[{"role":"user","content":"Search."}]}`)
	root := chatGuardClaudeFixture(t)
	blocks := arrayValue(root["content"])
	for _, test := range []struct {
		name    string
		content []any
		want    string
	}{
		{"cited text only", blocks[2:], "annotations"},
		{"paired hosted call and result", blocks[:2], "web_search_call"},
		{"hosted pair and cited text", blocks, "web_search_call"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root["content"] = test.content
			claudeBody := chatGuardJSON(t, root)
			native, err := ResponseFromEndpoint(ctx, EndpointMessages, "claude", "claude-haiku-5.5", nil, nil, claudeBody)
			if err != nil || !bytes.Equal(native, claudeBody) {
				t.Fatalf("native Claude state changed: %s, error=%v", native, err)
			}
			responses, err := ResponseFromEndpoint(ctx, EndpointMessages, "openai-response", "claude-haiku-5.5", nil, nil, claudeBody)
			if err != nil {
				t.Fatal(err)
			}
			native, err = ResponseFromEndpoint(ctx, EndpointResponses, "openai-response", "claude-haiku-5.5", nil, nil, responses)
			if err != nil || !bytes.Equal(native, responses) {
				t.Fatalf("native Responses state changed: %s, error=%v", native, err)
			}
			restored, err := ResponseFromEndpoint(ctx, EndpointResponses, "claude", "claude-haiku-5.5", nil, nil, responses)
			if err != nil {
				t.Fatal(err)
			}
			requireLiveBodyJSONEqual(t, []byte(gjson.GetBytes(restored, "content").Raw), chatGuardJSON(t, test.content))
			for _, endpoint := range []string{EndpointMessages, EndpointResponses} {
				body := responses
				if endpoint == EndpointMessages {
					body = claudeBody
				}
				out, err := ResponseFromEndpoint(ctx, endpoint, "openai", "claude-haiku-5.5", request, nil, body)
				if err == nil || !strings.Contains(err.Error(), test.want) || len(out) != 0 {
					t.Fatalf("%s discarded hosted state: %s, error=%v", endpoint, out, err)
				}
			}
			out, err := responsesResponseToChat(ctx, "claude-haiku-5.5", request, nil, responses)
			if err == nil || !strings.Contains(err.Error(), test.want) || len(out) != 0 {
				t.Fatalf("internal Responses helper discarded hosted state: %s, error=%v", out, err)
			}
		})
	}
}

func TestChatResponsesSSERejectsHostedStateBeforeOutput(t *testing.T) {
	ctx := context.Background()
	request := []byte(`{"messages":[{"role":"user","content":"Search."}]}`)
	claude := chatGuardJSON(t, chatGuardClaudeFixture(t))
	replay, err := ResponseFromEndpoint(ctx, EndpointMessages, "openai-response", "claude-haiku-5.5", nil, nil, claude)
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		name string
		body []byte
	}{
		{"Claude replay", replay},
		{"native GPT capture", readNativeServerToolFixture(t, "gpt-responses-search", "response.json")},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			root, err := decodeObject(fixture.body)
			if err != nil {
				t.Fatal(err)
			}
			var search, message map[string]any
			for _, raw := range arrayValue(root["output"]) {
				item := objectValue(raw)
				switch stringValue(item["type"]) {
				case "web_search_call":
					search = item
				case "message":
					message = item
				}
			}
			part := objectValue(arrayValue(message["content"])[0])
			annotations := arrayValue(part["annotations"])
			if search == nil || !hasMeaningfulValue(search["results"]) || len(annotations) == 0 {
				t.Fatal("fixture requires paired hosted results and citations")
			}
			type frameCase struct {
				name    string
				payload map[string]any
				want    string
			}
			var cases []frameCase
			for _, event := range []string{"response.output_item.added", "response.output_item.done"} {
				cases = append(cases,
					frameCase{event + " search", map[string]any{"type": event, "output_index": 0, "item": search}, "web_search_call"},
					frameCase{event + " citations", map[string]any{"type": event, "output_index": 0, "item": message}, "annotations"},
				)
			}
			for _, event := range []string{"response.content_part.added", "response.content_part.done"} {
				cases = append(cases, frameCase{event, map[string]any{"type": event, "output_index": 0, "content_index": 0, "part": part}, "annotations"})
			}
			cases = append(cases, frameCase{"response.output_text.annotation.added", map[string]any{"type": "response.output_text.annotation.added", "output_index": 0, "content_index": 0, "annotation_index": 0, "annotation": annotations[0]}, "annotations"})
			for _, event := range []string{"response.created", "response.in_progress", "response.completed", "response.incomplete"} {
				for _, item := range []map[string]any{search, message} {
					want := "annotations"
					if item == nil {
						t.Fatal("missing item")
					}
					if stringValue(item["type"]) == "web_search_call" {
						want = "web_search_call"
					}
					cases = append(cases, frameCase{event + " " + want, map[string]any{"type": event, "response": map[string]any{"id": "resp_guard", "status": strings.TrimPrefix(event, "response."), "output": []any{item}}}, want})
				}
			}
			for _, event := range []string{"response.web_search_call.in_progress", "response.web_search_call.searching", "response.web_search_call.completed"} {
				cases = append(cases, frameCase{event, map[string]any{"type": event, "output_index": 0, "item_id": search["id"]}, "web_search_call"})
			}
			for _, test := range cases {
				t.Run(test.name, func(t *testing.T) {
					for _, primed := range []bool{false, true} {
						for _, internal := range []bool{false, true} {
							var state any
							translate := func(frame []byte) ([][]byte, error) {
								if internal {
									return responsesStreamToChat(ctx, "claude-haiku-5.5", request, nil, frame, &state)
								}
								return StreamFromEndpoint(ctx, EndpointResponses, "openai", "claude-haiku-5.5", request, nil, frame, &state)
							}
							if primed {
								_, err := translate(claudeHostedFrame(t, "response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_guard", "output": []any{}}}))
								if err != nil {
									t.Fatal(err)
								}
							}
							frame := claudeHostedFrame(t, stringValue(test.payload["type"]), test.payload)
							out, err := translate(frame)
							if err == nil || !strings.Contains(err.Error(), test.want) || len(out) != 0 {
								t.Fatalf("primed=%t internal=%t: hosted frame produced data: %q, error=%v", primed, internal, out, err)
							}
						}
					}
				})
			}
		})
	}
}

func TestChatClaudeSSERejectsHostedStateBeforeOutput(t *testing.T) {
	ctx := context.Background()
	blocks := arrayValue(chatGuardClaudeFixture(t)["content"])
	cited := objectValue(blocks[2])
	for _, test := range []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{"server call", claudeBlockEvent("content_block_start", 0, objectValue(blocks[0])), "server_tool_use"},
		{"server result", claudeBlockEvent("content_block_start", 0, objectValue(blocks[1])), "web_search_tool_result"},
		{"cited text", claudeBlockEvent("content_block_start", 0, cited), "citations"},
		{"citation delta", claudeBlockDelta(0, map[string]any{"type": "citations_delta", "citation": arrayValue(cited["citations"])[0]}), "citations_delta"},
	} {
		t.Run(test.name, func(t *testing.T) {
			frame := claudeHostedFrame(t, stringValue(test.payload["type"]), test.payload)
			for _, primed := range []bool{false, true} {
				for _, route := range []string{"public", "internal", "bridge"} {
					var state any
					output := chatOpaqueStreamState{Model: "claude-haiku-5.5", LastSequence: -1}
					attached := false
					translate := func(frame []byte) ([][]byte, error) {
						switch route {
						case "internal":
							return claudeStreamToChat(ctx, "claude-haiku-5.5", nil, nil, frame, &state)
						case "bridge":
							return streamClaudeFrameToChat(ctx, "claude-haiku-5.5", nil, nil, frame, &state, &output, nil, &attached)
						default:
							return StreamFromEndpoint(ctx, EndpointMessages, "openai", "claude-haiku-5.5", nil, nil, frame, &state)
						}
					}
					if primed {
						for _, prefix := range []map[string]any{
							{"type": "message_start", "message": map[string]any{"id": "msg_guard", "model": "claude-haiku-5.5", "usage": map[string]any{"input_tokens": 1, "output_tokens": 0}}},
							claudeBlockEvent("content_block_start", 0, map[string]any{"type": "text", "text": ""}),
							claudeBlockDelta(0, map[string]any{"type": "text_delta", "text": "Earlier text."}),
						} {
							if _, err := translate(claudeHostedFrame(t, stringValue(prefix["type"]), prefix)); err != nil {
								t.Fatal(err)
							}
						}
					}
					out, err := translate(frame)
					if err == nil || !strings.Contains(err.Error(), test.want) || len(out) != 0 {
						t.Fatalf("primed=%t route=%s: hosted frame produced data: %q, error=%v", primed, route, out, err)
					}
				}
			}
			var nativeState any
			native, err := StreamFromEndpoint(ctx, EndpointMessages, "claude", "claude-haiku-5.5", nil, nil, frame, &nativeState)
			if err != nil || len(native) != 1 || !bytes.Equal(native[0], frame) {
				t.Fatalf("native Claude frame changed: %q, error=%v", native, err)
			}
		})
	}
}

func TestChatResponseEmptyAnnotationsRemainSupported(t *testing.T) {
	ctx := context.Background()
	request := []byte(`{"messages":[{"role":"user","content":"Answer."}]}`)
	for _, empty := range []any{nil, []any{}, map[string]any{}, ""} {
		part := map[string]any{"type": "output_text", "text": "Ordinary answer.", "annotations": empty}
		root := map[string]any{"id": "resp_empty", "status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{part}}}}
		out, err := ResponseFromEndpoint(ctx, EndpointResponses, "openai", "claude-haiku-5.5", request, nil, chatGuardJSON(t, root))
		if err != nil || gjson.GetBytes(out, "choices.0.message.content").String() != "Ordinary answer." {
			t.Fatalf("empty annotations rejected or text changed: %s, error=%v", out, err)
		}
		var state any
		frame := claudeHostedFrame(t, "response.content_part.added", map[string]any{"type": "response.content_part.added", "output_index": 0, "content_index": 0, "part": part})
		frames, err := StreamFromEndpoint(ctx, EndpointResponses, "openai", "claude-haiku-5.5", request, nil, frame, &state)
		var text strings.Builder
		for _, chunk := range frames {
			text.WriteString(gjson.GetBytes(chunk, "choices.0.delta.content").String())
		}
		if err != nil || text.String() != "Ordinary answer." {
			t.Fatalf("empty SSE annotations rejected or text changed: %q, error=%v", frames, err)
		}
		frame = claudeHostedFrame(t, "response.output_text.annotation.added", map[string]any{"type": "response.output_text.annotation.added", "output_index": 0, "content_index": 0, "annotation_index": 0, "annotation": empty})
		frames, err = StreamFromEndpoint(ctx, EndpointResponses, "openai", "claude-haiku-5.5", request, nil, frame, &state)
		if err != nil {
			t.Fatalf("empty annotation event rejected: %q, error=%v", frames, err)
		}
		claude := chatGuardClaudeFixture(t)
		claude["content"] = []any{map[string]any{"type": "text", "text": "Ordinary answer.", "citations": empty}}
		out, err = ResponseFromEndpoint(ctx, EndpointMessages, "openai", "claude-haiku-5.5", request, nil, chatGuardJSON(t, claude))
		if err != nil || gjson.GetBytes(out, "choices.0.message.content").String() != "Ordinary answer." {
			t.Fatalf("empty Claude citations rejected or text changed: %s, error=%v", out, err)
		}
		state = nil
		text.Reset()
		for _, payload := range []map[string]any{
			{"type": "message_start", "message": map[string]any{"id": "msg_empty", "model": "claude-haiku-5.5", "usage": map[string]any{"input_tokens": 1, "output_tokens": 0}}},
			claudeBlockEvent("content_block_start", 0, map[string]any{"type": "text", "text": "", "citations": empty}),
			claudeBlockDelta(0, map[string]any{"type": "text_delta", "text": "Ordinary answer."}),
		} {
			frames, err = StreamFromEndpoint(ctx, EndpointMessages, "openai", "claude-haiku-5.5", request, nil, claudeHostedFrame(t, stringValue(payload["type"]), payload), &state)
			if err != nil {
				t.Fatal(err)
			}
			for _, chunk := range frames {
				text.WriteString(gjson.GetBytes(chunk, "choices.0.delta.content").String())
			}
		}
		if text.String() != "Ordinary answer." {
			t.Fatalf("empty Claude SSE citations changed text: %q", text.String())
		}
	}
}

func TestChatHostedGuardPreservesClaudeResponsesSSEControls(t *testing.T) {
	ctx := context.Background()
	root := chatGuardClaudeFixture(t)
	for _, content := range [][]any{arrayValue(root["content"]), arrayValue(root["content"])[2:]} {
		root["content"] = content
		events, err := claudeMessageResponseEvents(root)
		if err != nil {
			t.Fatal(err)
		}
		var streamEvents []map[string]any
		for _, event := range events {
			streamEvents = append(streamEvents, event)
			if stringValue(objectValue(event["content_block"])["type"]) == "web_search_tool_result" {
				streamEvents = append(streamEvents, claudeBlockEvent("content_block_stop", intValue(event["index"]), nil))
			}
		}
		var responsesState, claudeState, nativeState any
		var restoredBlocks []any
		var sawTerminal, sawStop bool
		for _, event := range streamEvents {
			frame := claudeHostedFrame(t, stringValue(event["type"]), event)
			native, err := StreamFromEndpoint(ctx, EndpointMessages, "claude", "claude-haiku-5.5", nil, nil, frame, nil)
			if err != nil || len(native) != 1 || !bytes.Equal(native[0], frame) {
				t.Fatalf("native Claude stream changed: %q, error=%v", native, err)
			}
			responseFrames, err := StreamFromEndpoint(ctx, EndpointMessages, "openai-response", "claude-haiku-5.5", nil, nil, frame, &responsesState)
			if err != nil {
				t.Fatal(err)
			}
			for _, responseFrame := range responseFrames {
				_, data, _, err := parseSSEFrame(responseFrame)
				if err != nil {
					t.Fatal(err)
				}
				nativeFrames, err := StreamFromEndpoint(ctx, EndpointResponses, "openai-response", "claude-haiku-5.5", nil, nil, responseFrame, &nativeState)
				if err != nil {
					t.Fatal(err)
				}
				if gjson.GetBytes(data, "type").String() == "response.completed" {
					sawTerminal = true
					if len(nativeFrames) == 0 || !bytes.Equal(nativeFrames[len(nativeFrames)-1], responseFrame) {
						t.Fatalf("native Responses terminal lost state: %q", nativeFrames)
					}
					jsonControl, err := ResponseFromEndpoint(ctx, EndpointMessages, "openai-response", "claude-haiku-5.5", nil, nil, chatGuardJSON(t, root))
					if err != nil {
						t.Fatal(err)
					}
					requireLiveBodyJSONEqual(t, []byte(gjson.GetBytes(data, "response.output").Raw), []byte(gjson.GetBytes(jsonControl, "output").Raw))
				}
				claudeFrames, err := StreamFromEndpoint(ctx, EndpointResponses, "claude", "claude-haiku-5.5", nil, nil, responseFrame, &claudeState)
				if err != nil {
					t.Fatal(err)
				}
				for _, payload := range claudePayloads(t, claudeFrames) {
					index := int(payload.Get("index").Int())
					switch payload.Get("type").String() {
					case "content_block_start":
						if index != len(restoredBlocks) {
							t.Fatalf("block order changed: %s", payload.Raw)
						}
						block, err := decodeObject([]byte(payload.Get("content_block").Raw))
						if err != nil {
							t.Fatal(err)
						}
						restoredBlocks = append(restoredBlocks, block)
					case "content_block_delta":
						if index < 0 || index >= len(restoredBlocks) {
							t.Fatalf("delta has no block: %s", payload.Raw)
						}
						block := objectValue(restoredBlocks[index])
						switch payload.Get("delta.type").String() {
						case "text_delta":
							block["text"] = rawStringValue(block["text"]) + payload.Get("delta.text").String()
						case "citations_delta":
							citation, err := decodeObject([]byte(payload.Get("delta.citation").Raw))
							if err != nil {
								t.Fatal(err)
							}
							block["citations"] = append(arrayValue(block["citations"]), citation)
						}
					case "message_stop":
						sawStop = true
					}
				}
			}
		}
		if !sawTerminal || !sawStop {
			t.Fatal("control stream failed to complete")
		}
		requireLiveBodyJSONEqual(t, chatGuardJSON(t, restoredBlocks), chatGuardJSON(t, content))
	}
}
