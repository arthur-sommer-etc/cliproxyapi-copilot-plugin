package translate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestClaudeHostedSearchSSERequiresStoppedCallAndResultBlocks(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"late garbage", "late valid query", "late empty fragment", "repeated call stop", "missing result stop", "repeated result stop", "content before result stop"} {
		for _, stopReason := range []string{"end_turn", "max_tokens"} {
			t.Run(change+"/"+stopReason, func(t *testing.T) {
				frames := [][]byte{
					claudeHostedFrame(t, "message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_boundaries", "model": "claude-haiku-5.5", "usage": map[string]any{"input_tokens": 1, "output_tokens": 0}}}),
					claudeHostedFrame(t, "content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "server_tool_use", "id": "srvtoolu_boundaries", "name": "web_search", "input": map[string]any{}}}),
					claudeHostedFrame(t, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": `{"query":"q"}`}}),
					claudeHostedFrame(t, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}),
				}
				failureIndex := -1
				switch change {
				case "late garbage", "late valid query", "late empty fragment":
					fragment := " trailing garbage"
					if change == "late valid query" {
						fragment = `{"query":"other"}`
					} else if change == "late empty fragment" {
						fragment = ""
					}
					failureIndex = len(frames)
					frames = append(frames, claudeHostedFrame(t, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": fragment}}))
				case "repeated call stop":
					failureIndex = len(frames)
					frames = append(frames, frames[len(frames)-1])
				}
				frames = append(frames, claudeHostedFrame(t, "content_block_start", map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "web_search_tool_result", "tool_use_id": "srvtoolu_boundaries", "content": []any{}}}))
				if change == "content before result stop" {
					failureIndex = len(frames)
					frames = append(frames, claudeHostedFrame(t, "content_block_start", map[string]any{"type": "content_block_start", "index": 2, "content_block": map[string]any{"type": "text", "text": "after"}}))
				}
				if change != "missing result stop" {
					frames = append(frames, claudeHostedFrame(t, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 1}))
				}
				if change == "repeated result stop" {
					failureIndex = len(frames)
					frames = append(frames, frames[len(frames)-1])
				}
				frames = append(frames,
					claudeHostedFrame(t, "message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stopReason}, "usage": map[string]any{"output_tokens": 1}}),
					claudeHostedFrame(t, "message_stop", map[string]any{"type": "message_stop"}),
				)
				if failureIndex == -1 {
					failureIndex = len(frames) - 1
				}
				var state any
				for index, frame := range frames {
					out, err := StreamFromEndpoint(context.Background(), EndpointMessages, "openai-response", "claude-haiku-5.5", nil, nil, frame, &state)
					if index == failureIndex {
						if err == nil || len(out) != 0 {
							t.Fatalf("invalid block lifecycle accepted: error=%v frames=%q", err, out)
						}
						return
					}
					if err != nil {
						t.Fatalf("unexpected rejection at %d: %v", index, err)
					}
					for _, translated := range out {
						_, data, _, err := parseSSEFrame(translated)
						if err != nil {
							t.Fatal(err)
						}
						if typ := gjson.GetBytes(data, "type").String(); typ == "response.completed" || typ == "response.incomplete" || (typ == "response.output_item.done" && gjson.GetBytes(data, "item.type").String() == "web_search_call") {
							t.Fatalf("unfinished search reported a completed item or response: %s", data)
						}
					}
				}
			})
		}
	}
}

func TestResponsesClaudeSSEBindsSparseTextToUniqueTerminalOwner(t *testing.T) {
	t.Parallel()
	const citation = `{"type":"web_search_result_location","url":"https://example.test/answer","encrypted_index":"opaque_answer"}`
	for _, shape := range []string{"anonymous exact", "anonymous prefix", "item ID only", "output index only", "content index only", "completed part evidence", "citation evidence", "buffered after tool", "two indexed anonymous parts"} {
		for _, terminal := range []string{"response.completed", "response.incomplete"} {
			t.Run(shape+"/"+terminal, func(t *testing.T) {
				var state any
				var translated [][]byte
				feed := func(event string, fields map[string]any) [][]byte {
					t.Helper()
					fields["type"] = event
					out, err := StreamFromEndpoint(context.Background(), EndpointResponses, "claude", "gpt-6-luna", nil, nil, claudeHostedFrame(t, event, fields), &state)
					if err != nil {
						t.Fatal(err)
					}
					translated = append(translated, out...)
					return out
				}
				if shape == "buffered after tool" {
					feed("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"type": "function_call", "id": "fc_sparse", "call_id": "call_sparse", "name": "lookup", "arguments": ""}})
				}
				if shape == "two indexed anonymous parts" {
					feed("response.output_text.delta", map[string]any{"content_index": 0, "delta": "before"})
					feed("response.content_part.done", map[string]any{"content_index": 0, "part": map[string]any{"type": "output_text", "text": "before"}})
				}
				delta := map[string]any{"delta": "answer"}
				switch shape {
				case "anonymous prefix":
					delta["delta"] = "ans"
				case "item ID only":
					delta["item_id"] = "msg_sparse"
				case "output index only":
					delta["output_index"] = 0
				case "content index only":
					delta["content_index"] = 0
				case "two indexed anonymous parts":
					delta["content_index"] = 1
				}
				immediate := feed("response.output_text.delta", delta)
				var immediateText strings.Builder
				for _, payload := range claudePayloads(t, immediate) {
					immediateText.WriteString(payload.Get("delta.text").String())
				}
				if shape != "buffered after tool" && immediateText.String() != delta["delta"] {
					t.Fatalf("sparse text did not stream immediately: %q", immediateText.String())
				}
				part := map[string]any{"type": "output_text", "text": "answer", "annotations": []any{}}
				if shape == "citation evidence" {
					part["annotations"] = []any{json.RawMessage(citation)}
					feed("response.output_text.annotation.added", map[string]any{"annotation_index": 0, "annotation": json.RawMessage(citation)})
				}
				if shape == "completed part evidence" {
					feed("response.content_part.done", map[string]any{"part": part})
				}
				output := []any{}
				if shape == "buffered after tool" {
					output = append(output, map[string]any{"type": "function_call", "id": "fc_sparse", "call_id": "call_sparse", "name": "lookup", "arguments": "{}"})
				}
				output = append(output, map[string]any{"type": "message", "id": "msg_sparse", "role": "assistant", "content": []any{part}})
				wantText, wantBlocks := "answer", 1
				if shape == "two indexed anonymous parts" {
					output[0].(map[string]any)["content"] = []any{map[string]any{"type": "output_text", "text": "before"}, part}
					wantText, wantBlocks = "beforeanswer", 2
				}
				if shape == "completed part evidence" || shape == "citation evidence" {
					otherText := "answer longer"
					if shape == "citation evidence" {
						otherText = "answer"
					}
					output = append(output, map[string]any{"type": "message", "id": "msg_other", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": otherText}}})
					wantText += otherText
					wantBlocks++
				}
				if shape == "buffered after tool" {
					wantBlocks++
				}
				response := map[string]any{"id": "resp_sparse", "status": "completed", "output": output}
				wantStop := "end_turn"
				if shape == "buffered after tool" {
					wantStop = "tool_use"
				}
				if terminal == "response.incomplete" {
					response["status"] = "incomplete"
					response["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
					wantStop = "max_tokens"
				}
				feed(terminal, map[string]any{"response": response})
				var text strings.Builder
				starts, stops, messages, citations := 0, 0, 0, 0
				openIndex := -1
				for _, payload := range claudePayloads(t, translated) {
					index := int(payload.Get("index").Int())
					switch payload.Get("type").String() {
					case "content_block_start":
						if openIndex != -1 || index != starts {
							t.Fatalf("sparse binding changed block order: %s", payload.Raw)
						}
						openIndex = index
						starts++
					case "content_block_delta":
						if openIndex != index {
							t.Fatalf("sparse delta changed block ownership: %s", payload.Raw)
						}
						text.WriteString(payload.Get("delta.text").String())
						if payload.Get("delta.type").String() == "citations_delta" {
							citations++
							if index != 0 {
								t.Fatalf("citation moved to another text block: %s", payload.Raw)
							}
							requireLiveBodyJSONEqual(t, []byte(payload.Get("delta.citation").Raw), []byte(citation))
						}
					case "content_block_stop":
						if openIndex != index {
							t.Fatalf("sparse stop changed block ownership: %s", payload.Raw)
						}
						openIndex = -1
						stops++
					case "message_delta":
						if payload.Get("delta.stop_reason").String() != wantStop {
							t.Fatalf("terminal state changed: %s", payload.Raw)
						}
					case "message_stop":
						messages++
					}
				}
				wantCitations := 0
				if shape == "citation evidence" {
					wantCitations = 1
				}
				if text.String() != wantText || starts != wantBlocks || stops != starts || openIndex != -1 || messages != 1 || citations != wantCitations {
					t.Fatalf("sparse binding duplicated content or lost closure: text=%q starts=%d stops=%d messages=%d citations=%d", text.String(), starts, stops, messages, citations)
				}
			})
		}
	}
}

func TestResponsesClaudeSSERejectsAmbiguousSparseTextOwnership(t *testing.T) {
	t.Parallel()
	for _, shape := range []string{"same text in two messages", "prefix in two messages", "same text in two parts", "changed terminal text", "conflicting identified part", "reordered terminal text", "terminal search before text"} {
		for _, terminal := range []string{"response.completed", "response.incomplete"} {
			t.Run(fmt.Sprintf("%s/%s", shape, terminal), func(t *testing.T) {
				part := map[string]any{"type": "output_text", "text": "answer"}
				output := []any{map[string]any{"type": "message", "id": "msg_sparse", "role": "assistant", "content": []any{part}}}
				frames := [][]byte{claudeHostedFrame(t, "response.output_text.delta", map[string]any{"type": "response.output_text.delta", "delta": "answer"})}
				switch shape {
				case "same text in two messages", "prefix in two messages":
					other := "answer"
					if shape == "prefix in two messages" {
						other = "answer longer"
					}
					output = append(output, map[string]any{"type": "message", "id": "msg_other", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": other}}})
				case "same text in two parts":
					output[0].(map[string]any)["content"] = []any{part, part}
				case "changed terminal text":
					part["text"] = "changed"
				case "conflicting identified part":
					frames = append(frames, claudeHostedFrame(t, "response.output_text.delta", map[string]any{"type": "response.output_text.delta", "item_id": "msg_sparse", "content_index": 0, "delta": "answer"}))
				case "reordered terminal text":
					output = append([]any{map[string]any{"type": "message", "id": "msg_before", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "before"}}}}, output...)
				case "terminal search before text":
					status := "completed"
					if terminal == "response.incomplete" {
						status = "incomplete"
					}
					output = append([]any{map[string]any{"type": "web_search_call", "id": "ws_srvtoolu_sparse", "status": status, "action": map[string]any{"type": "search", "query": "q"}, "results": []any{}}}, output...)
				}
				response := map[string]any{"id": "resp_sparse", "status": "completed", "output": output}
				if terminal == "response.incomplete" {
					response["status"] = "incomplete"
					response["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
				}
				frames = append(frames, claudeHostedFrame(t, terminal, map[string]any{"type": terminal, "response": response}))
				var state any
				for index, frame := range frames {
					out, err := StreamFromEndpoint(context.Background(), EndpointResponses, "claude", "gpt-6-luna", nil, nil, frame, &state)
					if index == len(frames)-1 {
						if err == nil || len(out) != 0 {
							t.Fatalf("ambiguous sparse stream completed: error=%v frames=%q", err, out)
						}
					} else if err != nil {
						t.Fatalf("unexpected rejection before terminal: %v", err)
					}
				}
			})
		}
	}
}
