package translate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const claudeHostedSearchRequest = `{"model":"claude-haiku-5.5","max_tokens":256,"messages":[{"role":"user","content":"Search."},{"role":"assistant","content":[{"type":"server_tool_use","id":"srvtoolu_roundtrip_search","name":"web_search","input":{"query":"  exact query\n\t "}},{"type":"web_search_tool_result","tool_use_id":"srvtoolu_roundtrip_search","content":[{"type":"web_search_result","url":"https://example.test/report?source=fixture","title":"Fixture report","encrypted_content":"fixture_opaque_replay_token","page_age":"one day","snippet":"Exact source."}]},{"type":"text","text":"Fixture answer.","citations":[{"type":"web_search_result_location","url":"https://example.test/report?source=fixture","title":"Fixture report","encrypted_index":"fixture_opaque_citation_1","cited_text":"First excerpt."},{"type":"web_search_result_location","url":"https://example.test/report?source=fixture","title":"Fixture report","encrypted_index":"fixture_opaque_citation_2","cited_text":"Second excerpt."}]}]},{"role":"user","content":"Continue without another search."}]}`

func TestClaudeHostedSearchRequestHistoryRoundTripPreservesOpaqueState(t *testing.T) {
	resultBodies := []string{
		`[{"type":"web_search_result","url":"https://example.test/report?source=fixture","title":"Fixture report","encrypted_content":"fixture_opaque_replay_token","page_age":"one day","snippet":"Exact source."}]`,
		`[]`,
		`[{"type":"web_search_tool_result_error","error_code":"max_uses_exceeded"}]`,
	}
	for index, results := range resultBodies {
		t.Run(fmt.Sprintf("results_%d", index), func(t *testing.T) {
			request, err := sjson.SetRaw(claudeHostedSearchRequest, "messages.1.content.1.content", results)
			if err != nil {
				t.Fatal(err)
			}
			responses, err := RequestForEndpointFrom("claude", "gpt-6-luna", []byte(request), false, EndpointResponses)
			if err != nil {
				t.Fatalf("Claude request to Responses: %v", err)
			}
			search := gjson.GetBytes(responses, "input.1")
			if search.Get("id").String() != "ws_srvtoolu_roundtrip_search" || search.Get("action.query").String() != "  exact query\n\t " {
				t.Fatalf("Responses hosted search changed its identity or exact query: %s", search.Raw)
			}
			requireLiveBodyJSONEqual(t, []byte(search.Get("results").Raw), []byte(results))
			requireLiveBodyJSONEqual(t, []byte(gjson.GetBytes(responses, "input.2.content.0.annotations").Raw), []byte(gjson.Get(request, "messages.1.content.2.citations").Raw))

			messages, err := RequestForEndpointFrom("openai-response", "claude-haiku-5.5", responses, false, EndpointMessages)
			if err != nil {
				t.Fatalf("Responses request history to Claude: %v", err)
			}
			blocks := assistantBlocks(t, messages)
			if len(blocks) != 3 {
				t.Fatalf("restored assistant blocks = %d, want search pair and answer: %s", len(blocks), messages)
			}
			requireLiveBodyJSONEqual(t, []byte(blocks[0].Raw), []byte(`{"type":"server_tool_use","id":"srvtoolu_roundtrip_search","name":"web_search","input":{"query":"  exact query\n\t "}}`))
			requireLiveBodyJSONEqual(t, []byte(blocks[1].Raw), []byte(`{"type":"web_search_tool_result","tool_use_id":"srvtoolu_roundtrip_search","content":`+results+`}`))
			requireLiveBodyJSONEqual(t, []byte(blocks[2].Raw), []byte(gjson.Get(request, "messages.1.content.2").Raw))
		})
	}
}

func TestClaudeHostedSearchJSONResponseRoundTripPreservesOpaqueState(t *testing.T) {
	response := []byte(`{"id":"msg_roundtrip","type":"message","role":"assistant","model":"claude-haiku-5.5","content":[{"type":"server_tool_use","id":"srvtoolu_roundtrip_search","name":"web_search","input":{"query":"  exact query\n\t "}},{"type":"web_search_tool_result","tool_use_id":"srvtoolu_roundtrip_search","content":[{"type":"web_search_result","url":"https://example.test/report?source=fixture","title":"Fixture report","encrypted_content":"fixture_opaque_replay_token","page_age":"one day","snippet":"Exact source."}]},{"type":"text","text":"Fixture answer.","citations":[{"type":"web_search_result_location","url":"https://example.test/report?source=fixture","title":"Fixture report","encrypted_index":"fixture_opaque_citation_1","cited_text":"First excerpt."},{"type":"web_search_result_location","url":"https://example.test/report?source=fixture","title":"Fixture report","encrypted_index":"fixture_opaque_citation_2","cited_text":"Second excerpt."}]}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":12,"output_tokens":8}}`)
	responses, err := ResponseFromEndpoint(context.Background(), EndpointMessages, "openai-response", "claude-haiku-5.5", nil, nil, response)
	if err != nil {
		t.Fatalf("Claude JSON response to Responses: %v", err)
	}
	search := gjson.GetBytes(responses, `output.#(type==web_search_call)`)
	if search.Get("id").String() != "ws_srvtoolu_roundtrip_search" || search.Get("action.query").String() != "  exact query\n\t " {
		t.Fatalf("Responses response changed hosted-search identity or query: %s", search.Raw)
	}
	requireLiveBodyJSONEqual(t, []byte(search.Get("results").Raw), []byte(gjson.GetBytes(response, "content.1.content").Raw))
	annotations := gjson.GetBytes(responses, `output.#(type==message).content.0.annotations`)
	requireLiveBodyJSONEqual(t, []byte(annotations.Raw), []byte(gjson.GetBytes(response, "content.2.citations").Raw))

	messages, err := ResponseFromEndpoint(context.Background(), EndpointResponses, "claude", "claude-haiku-5.5", nil, nil, responses)
	if err != nil {
		t.Fatalf("Responses JSON response to Claude: %v", err)
	}
	blocks := gjson.GetBytes(messages, "content").Array()
	if len(blocks) != 3 {
		t.Fatalf("restored response blocks = %d, want search pair and cited answer: %s", len(blocks), messages)
	}
	requireLiveBodyJSONEqual(t, []byte(blocks[0].Raw), []byte(gjson.GetBytes(response, "content.0").Raw))
	requireLiveBodyJSONEqual(t, []byte(blocks[1].Raw), []byte(gjson.GetBytes(response, "content.1").Raw))
	requireLiveBodyJSONEqual(t, []byte(blocks[2].Raw), []byte(gjson.GetBytes(response, "content.2").Raw))
}

func TestClaudeHostedSearchJSONResponseKeepsCitationsAcrossSearchOrder(t *testing.T) {
	response := []byte(`{"id":"msg_order","type":"message","role":"assistant","model":"claude-haiku-5.5","content":[{"type":"text","text":"Before.","citations":[{"type":"web_search_result_location","url":"https://example.test/before","encrypted_index":"citation_before","cited_text":"Before."}]},{"type":"server_tool_use","id":"srvtoolu_order","name":"web_search","input":{"query":"  ordered query  "}},{"type":"web_search_tool_result","tool_use_id":"srvtoolu_order","content":[]},{"type":"text","text":"After.","citations":[{"type":"web_search_result_location","url":"https://example.test/after","encrypted_index":"citation_after","cited_text":"After."}]}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":4,"output_tokens":4}}`)
	responses, err := ResponseFromEndpoint(context.Background(), EndpointMessages, "openai-response", "claude-haiku-5.5", nil, nil, response)
	if err != nil {
		t.Fatalf("Claude JSON response to Responses: %v", err)
	}
	if gjson.GetBytes(responses, "output.0.type").String() != "message" || gjson.GetBytes(responses, "output.1.type").String() != "web_search_call" || gjson.GetBytes(responses, "output.2.type").String() != "message" {
		t.Fatalf("Responses output order changed around web search: %s", responses)
	}
	messages, err := ResponseFromEndpoint(context.Background(), EndpointResponses, "claude", "claude-haiku-5.5", nil, nil, responses)
	if err != nil {
		t.Fatalf("Responses JSON response to Claude: %v", err)
	}
	blocks := gjson.GetBytes(messages, "content").Array()
	if len(blocks) != 4 {
		t.Fatalf("restored block order length = %d: %s", len(blocks), messages)
	}
	for index, sourceIndex := range []int{0, 1, 2, 3} {
		requireLiveBodyJSONEqual(t, []byte(blocks[index].Raw), []byte(gjson.GetBytes(response, fmt.Sprintf("content.%d", sourceIndex)).Raw))
	}
}

func TestClaudeHostedSearchSSEResponseRoundTripPreservesFragments(t *testing.T) {
	query := "  exact query\n\t "
	encodedQuery, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		t.Fatal(err)
	}
	fragments := [][]byte{encodedQuery[:9], encodedQuery[9:17], encodedQuery[17:]}
	for _, result := range []string{
		`[]`,
		`[{"type":"web_search_tool_result_error","error_code":"rate_limited"}]`,
		`[{"type":"web_search_result","url":"https://example.test/report?source=fixture","title":"Fixture report","encrypted_content":"fixture_opaque_replay_token","page_age":"one day"}]`,
	} {
		t.Run(result, func(t *testing.T) {
			claudeFrames := [][]byte{
				claudeHostedFrame(t, "message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_stream", "model": "claude-haiku-5.5", "usage": map[string]any{"input_tokens": 12, "output_tokens": 0}}}),
				claudeHostedFrame(t, "content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "server_tool_use", "id": "srvtoolu_roundtrip_search", "name": "web_search", "input": map[string]any{}}}),
			}
			for _, fragment := range fragments {
				claudeFrames = append(claudeFrames, claudeHostedFrame(t, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(fragment)}}))
			}
			resultBytes, err := json.Marshal(json.RawMessage(result))
			if err != nil {
				t.Fatal(err)
			}
			var resultContent any
			if err := json.Unmarshal(resultBytes, &resultContent); err != nil {
				t.Fatal(err)
			}
			claudeFrames = append(claudeFrames,
				claudeHostedFrame(t, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}),
				claudeHostedFrame(t, "content_block_start", map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "web_search_tool_result", "tool_use_id": "srvtoolu_roundtrip_search", "content": resultContent}}),
				claudeHostedFrame(t, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 1}),
				claudeHostedFrame(t, "content_block_start", map[string]any{"type": "content_block_start", "index": 2, "content_block": map[string]any{"type": "text", "text": ""}}),
				claudeHostedFrame(t, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 2, "delta": map[string]any{"type": "text_delta", "text": "Fixture "}}),
				claudeHostedFrame(t, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 2, "delta": map[string]any{"type": "text_delta", "text": "answer."}}),
				claudeHostedFrame(t, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 2, "delta": map[string]any{"type": "citations_delta", "citation": map[string]any{"type": "web_search_result_location", "url": "https://example.test/report?source=fixture", "title": "Fixture report", "encrypted_index": "fixture_opaque_citation", "cited_text": "Fixture answer."}}}),
				claudeHostedFrame(t, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 2}),
				claudeHostedFrame(t, "message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]any{"input_tokens": 12, "output_tokens": 8}}),
				claudeHostedFrame(t, "message_stop", map[string]any{"type": "message_stop"}),
			)

			var responsesState any
			var responseFrames [][]byte
			for _, frame := range claudeFrames {
				translated, err := StreamFromEndpoint(context.Background(), EndpointMessages, "openai-response", "gpt-6-luna", nil, nil, frame, &responsesState)
				if err != nil {
					t.Fatalf("Claude SSE to Responses: %v", err)
				}
				responseFrames = append(responseFrames, translated...)
			}
			if len(responseFrames) == 0 {
				t.Fatal("Claude SSE produced no Responses events")
			}
			var completed gjson.Result
			for _, frame := range responseFrames {
				_, data, _, err := parseSSEFrame(frame)
				if err != nil {
					t.Fatal(err)
				}
				if gjson.GetBytes(data, "type").String() == "response.completed" {
					completed = gjson.GetBytes(data, "response")
				}
			}
			if !completed.Exists() {
				t.Fatalf("translated Responses SSE has no completed snapshot: %q", responseFrames)
			}
			search := gjson.Get(completed.Raw, `output.#(type==web_search_call)`)
			if search.Get("id").String() != "ws_srvtoolu_roundtrip_search" || search.Get("action.query").String() != query {
				t.Fatalf("Responses SSE changed search identity or fragmented query: %s", search.Raw)
			}
			requireLiveBodyJSONEqual(t, []byte(search.Get("results").Raw), []byte(result))
			citation := gjson.Get(completed.Raw, `output.#(type==message).content.0.annotations.0`)
			if citation.Get("encrypted_index").String() != "fixture_opaque_citation" || citation.Get("cited_text").String() != "Fixture answer." {
				t.Fatalf("Responses SSE changed citation: %s", citation.Raw)
			}

			var claudeState any
			var clientFrames [][]byte
			for _, frame := range responseFrames {
				translated, err := StreamFromEndpoint(context.Background(), EndpointResponses, "claude", "claude-haiku-5.5", nil, nil, frame, &claudeState)
				if err != nil {
					t.Fatalf("Responses SSE to Claude: %v", err)
				}
				clientFrames = append(clientFrames, translated...)
			}
			if len(clientFrames) == 0 {
				t.Fatal("Responses SSE produced no Claude events")
			}
			var output strings.Builder
			var sawSearchUse, sawSearchResult, sawCitation bool
			for _, frame := range clientFrames {
				output.Write(frame)
				_, data, _, err := parseSSEFrame(frame)
				if err != nil || len(data) == 0 {
					continue
				}
				payload, err := decodeObject(data)
				if err != nil {
					t.Fatal(err)
				}
				if stringValue(payload["type"]) == "content_block_start" {
					block := objectValue(payload["content_block"])
					switch stringValue(block["type"]) {
					case "server_tool_use":
						sawSearchUse = true
						if rawStringValue(objectValue(block["input"])["query"]) != query {
							t.Fatalf("Claude SSE changed exact search query: %s", data)
						}
					case "web_search_tool_result":
						sawSearchResult = true
						content, err := json.Marshal(block["content"])
						if err != nil {
							t.Fatal(err)
						}
						requireLiveBodyJSONEqual(t, content, []byte(result))
					}
				}
				if gjson.GetBytes(data, "delta.type").String() == "citations_delta" {
					sawCitation = true
				}
			}
			streamText := output.String()
			if !sawSearchUse || !sawSearchResult || !sawCitation {
				t.Fatalf("Claude SSE changed hosted search or citation fields:\n%s", streamText)
			}
			if strings.Index(streamText, `"type":"server_tool_use"`) > strings.Index(streamText, `"type":"web_search_tool_result"`) || strings.Index(streamText, `"type":"web_search_tool_result"`) > strings.Index(streamText, `"type":"text"`) {
				t.Fatalf("Claude SSE changed hosted-search/text order:\n%s", streamText)
			}
		})
	}
}

func TestClaudeHostedSearchRejectsAmbiguousPairsAndLossyFields(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "unmatched call", body: `{"type":"message","content":[{"type":"server_tool_use","id":"srvtoolu_one","name":"web_search","input":{"query":"q"}}]}`},
		{name: "unmatched result", body: `{"type":"message","content":[{"type":"web_search_tool_result","tool_use_id":"srvtoolu_one","content":[]}]}`},
		{name: "nonadjacent pair", body: `{"type":"message","content":[{"type":"server_tool_use","id":"srvtoolu_one","name":"web_search","input":{"query":"q"}},{"type":"text","text":"between"},{"type":"web_search_tool_result","tool_use_id":"srvtoolu_one","content":[]}]}`},
		{name: "duplicate call", body: `{"type":"message","content":[{"type":"server_tool_use","id":"srvtoolu_one","name":"web_search","input":{"query":"q"}},{"type":"web_search_tool_result","tool_use_id":"srvtoolu_one","content":[]},{"type":"server_tool_use","id":"srvtoolu_one","name":"web_search","input":{"query":"q"}},{"type":"web_search_tool_result","tool_use_id":"srvtoolu_one","content":[]}]}`},
		{name: "extra search field", body: `{"type":"message","content":[{"type":"server_tool_use","id":"srvtoolu_one","name":"web_search","input":{"query":"q","extra":"loss"}},{"type":"web_search_tool_result","tool_use_id":"srvtoolu_one","content":[]}]}`},
		{name: "native bash", body: `{"type":"message","content":[{"type":"server_tool_use","id":"srvtoolu_one","name":"bash_code_execution","input":{"command":"pwd"}}]}`},
		{name: "citation extra field", body: `{"type":"message","content":[{"type":"text","text":"cited","citations":[{"type":"web_search_result_location","url":"https://example.test","encrypted_index":"opaque","provider_state":"loss"}]}]}`},
		{name: "citation without replay token", body: `{"type":"message","content":[{"type":"text","text":"cited","citations":[{"type":"web_search_result_location","url":"https://example.test"}]}]}`},
		{name: "result without replay token", body: `{"type":"message","content":[{"type":"server_tool_use","id":"srvtoolu_one","name":"web_search","input":{"query":"q"}},{"type":"web_search_tool_result","tool_use_id":"srvtoolu_one","content":[{"type":"web_search_result","url":"https://example.test"}]}]}`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if out, err := ResponseFromEndpoint(context.Background(), EndpointMessages, "openai-response", "claude-haiku-5.5", nil, nil, []byte(test.body)); err == nil || len(out) != 0 {
				t.Fatalf("lossy Claude response accepted: %s, error=%v", out, err)
			}
		})
	}
}

func TestResponsesHostedSearchOutputRejectsNativeAndIncompleteState(t *testing.T) {
	bodies := []string{
		`{"status":"completed","output":[{"type":"web_search_call","id":"ws_native_gpt_search","status":"completed","action":{"type":"search","query":"q"},"results":[]}]}`,
		`{"status":"completed","output":[{"type":"web_search_call","id":"ws_srvtoolu_incomplete","status":"in_progress","action":{"type":"search","query":"q"}}]}`,
		`{"status":"completed","output":[{"type":"web_search_call","id":"ws_srvtoolu_extra","status":"completed","action":{"type":"search","query":"q","queries":["other"]},"results":[]}]}`,
		`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"cited","annotations":[{"type":"url_citation","url":"https://example.test","encrypted_index":"opaque"}]}]}]}`,
		`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"cited","annotations":[{"type":"web_search_result_location","url":"https://example.test","encrypted_index":"opaque","extra":"loss"}]}]}]}`,
	}
	for _, body := range bodies {
		if out, err := ResponseFromEndpoint(context.Background(), EndpointResponses, "claude", "claude-haiku-5.5", nil, nil, []byte(body)); err == nil || len(out) != 0 {
			t.Fatalf("unsupported Responses hosted-search/citation state accepted: %s, error=%v", out, err)
		}
		frame := claudeHostedFrame(t, "response.completed", map[string]any{"type": "response.completed", "response": json.RawMessage(body)})
		if out, err := StreamFromEndpoint(context.Background(), EndpointResponses, "claude", "claude-haiku-5.5", nil, nil, frame, new(any)); err == nil || len(out) != 0 {
			t.Fatalf("unsupported Responses SSE hosted-search/citation state accepted: %s, error=%v", out, err)
		}
	}
}

func TestClaudeHostedSearchSSERejectsIncompleteAndAmbiguousState(t *testing.T) {
	start := func(t *testing.T, id, name string, input map[string]any) []byte {
		return claudeHostedFrame(t, "content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "server_tool_use", "id": id, "name": name, "input": input}})
	}
	result := func(t *testing.T, id string) []byte {
		return claudeHostedFrame(t, "content_block_start", map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "web_search_tool_result", "tool_use_id": id, "content": []any{}}})
	}
	for _, test := range []struct {
		name   string
		frames [][]byte
	}{
		{name: "incomplete query JSON", frames: [][]byte{start(t, "srvtoolu_incomplete", "web_search", map[string]any{}), claudeHostedFrame(t, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": `{"query":"unfinished"`}}), claudeHostedFrame(t, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})}},
		{name: "unmatched result", frames: [][]byte{result(t, "srvtoolu_missing")}},
		{name: "native bash", frames: [][]byte{start(t, "srvtoolu_bash", "bash_code_execution", map[string]any{"command": "pwd"})}},
		{name: "extra call field", frames: [][]byte{claudeHostedFrame(t, "content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "server_tool_use", "id": "srvtoolu_extra", "name": "web_search", "input": map[string]any{}, "provider_state": "unmapped"}})}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var state any
			for _, frame := range test.frames {
				_, err := StreamFromEndpoint(context.Background(), EndpointMessages, "openai-response", "gpt-6-luna", nil, nil, frame, &state)
				if err != nil {
					return
				}
			}
			t.Fatal("incomplete or ambiguous Claude search state was accepted")
		})
	}
}

func assistantBlocks(t *testing.T, body []byte) []gjson.Result {
	t.Helper()
	var blocks []gjson.Result
	for _, message := range gjson.GetBytes(body, "messages").Array() {
		if message.Get("role").String() == "assistant" {
			blocks = append(blocks, message.Get("content").Array()...)
		}
	}
	return blocks
}

func claudeHostedFrame(t *testing.T, event string, payload map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return []byte("event: " + event + "\ndata: " + string(data) + "\n\n")
}

func TestClaudeHostedSearchAdjacentTextKeepsCitationOwnership(t *testing.T) {
	for _, content := range []string{
		`[{"type":"text","text":"Uncited."},{"type":"text","text":"A","citations":[{"type":"web_search_result_location","url":"https://example.test/a","encrypted_index":"opaque_a","cited_text":"A"}]}]`,
		`[{"type":"text","text":"A","citations":[{"type":"web_search_result_location","url":"https://example.test/a","encrypted_index":"opaque_a","cited_text":"A"}]},{"type":"text","text":"A","citations":[{"type":"web_search_result_location","url":"https://example.test/b","encrypted_index":"opaque_b","cited_text":"B"}]},{"type":"text","text":"Uncited."},{"type":"server_tool_use","id":"srvtoolu_boundary","name":"web_search","input":{"query":" q "}},{"type":"web_search_tool_result","tool_use_id":"srvtoolu_boundary","content":[]},{"type":"text","text":"A","citations":[{"type":"web_search_result_location","url":"https://example.test/c","encrypted_index":"opaque_c"}]},{"type":"text","text":"Uncited."}]`,
	} {
		t.Run(content, func(t *testing.T) {
			body := []byte(`{"id":"msg_adjacent","type":"message","role":"assistant","model":"claude-haiku-5.5","content":` + content + `,"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
			responses, err := ResponseFromEndpoint(context.Background(), EndpointMessages, "openai-response", "claude-haiku-5.5", nil, nil, body)
			if err != nil {
				t.Fatal(err)
			}
			claude, err := ResponseFromEndpoint(context.Background(), EndpointResponses, "claude", "claude-haiku-5.5", nil, nil, responses)
			if err != nil {
				t.Fatal(err)
			}
			requireLiveBodyJSONEqual(t, []byte(gjson.GetBytes(claude, "content").Raw), []byte(content))
		})
	}
}

func TestClaudeHostedSearchSSECitedTextBeforeSearchRemainsReplayable(t *testing.T) {
	const content = `[{"type":"text","text":"Uncited."},{"type":"text","text":"A","citations":[{"type":"web_search_result_location","url":"https://example.test/a","encrypted_index":"opaque_a","cited_text":"A"}]},{"type":"text","text":"A","citations":[{"type":"web_search_result_location","url":"https://example.test/b","encrypted_index":"opaque_b","cited_text":"B"}]},{"type":"server_tool_use","id":"srvtoolu_before","name":"web_search","input":{"query":" q "}},{"type":"web_search_tool_result","tool_use_id":"srvtoolu_before","content":[{"type":"web_search_result","url":"https://example.test/a","encrypted_content":"opaque_result"}]},{"type":"text","text":"After."},{"type":"text","text":"A","citations":[{"type":"web_search_result_location","url":"https://example.test/c","encrypted_index":"opaque_c"}]},{"type":"text","text":"Uncited."}]`
	var source []map[string]any
	if err := json.Unmarshal([]byte(content), &source); err != nil {
		t.Fatal(err)
	}
	frames := [][]byte{claudeHostedFrame(t, "message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_before", "model": "claude-haiku-5.5", "usage": map[string]any{"input_tokens": 1, "output_tokens": 0}}})}
	for index, block := range source {
		start := block
		if block["type"] == "text" {
			start = map[string]any{"type": "text", "text": ""}
		}
		frames = append(frames, claudeHostedFrame(t, "content_block_start", map[string]any{"type": "content_block_start", "index": index, "content_block": start}))
		if block["type"] == "text" {
			frames = append(frames, claudeHostedFrame(t, "content_block_delta", map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": "text_delta", "text": block["text"]}}))
			for _, citation := range arrayValue(block["citations"]) {
				frames = append(frames, claudeHostedFrame(t, "content_block_delta", map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": "citations_delta", "citation": citation}}))
			}
		}
		frames = append(frames, claudeHostedFrame(t, "content_block_stop", map[string]any{"type": "content_block_stop", "index": index}))
	}
	frames = append(frames,
		claudeHostedFrame(t, "message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 1}}),
		claudeHostedFrame(t, "message_stop", map[string]any{"type": "message_stop"}),
	)
	var responsesState, claudeState any
	var translated [][]byte
	doneMessages := make(map[string]gjson.Result)
	messageIDs := make(map[int64]string)
	sequence := int64(0)
	partIndex := 0
	for _, frame := range frames {
		_, sourceData, _, err := parseSSEFrame(frame)
		if err != nil {
			t.Fatal(err)
		}
		sourceEvent := gjson.GetBytes(sourceData, "type").String()
		sourceIndex := int(gjson.GetBytes(sourceData, "index").Int())
		textBlock := sourceIndex < len(source) && source[sourceIndex]["type"] == "text"
		if sourceEvent == "content_block_start" && !textBlock {
			partIndex = 0
		}
		out, err := StreamFromEndpoint(context.Background(), EndpointMessages, "openai-response", "claude-haiku-5.5", nil, nil, frame, &responsesState)
		if err != nil {
			t.Fatal(err)
		}
		addedParts, doneParts, doneTexts := 0, 0, 0
		for _, responseFrame := range out {
			_, data, _, err := parseSSEFrame(responseFrame)
			if err != nil {
				t.Fatal(err)
			}
			payload := gjson.ParseBytes(data)
			if payload.Get("sequence_number").Int() <= sequence {
				t.Fatalf("Responses sequence did not advance: %s", data)
			}
			sequence = payload.Get("sequence_number").Int()
			if payload.Get("type").String() == "response.output_item.added" && payload.Get("item.type").String() == "message" {
				messageIDs[payload.Get("output_index").Int()] = payload.Get("item.id").String()
			}
			switch payload.Get("type").String() {
			case "response.content_part.added", "response.content_part.done", "response.output_text.done", "response.output_text.delta":
				if !textBlock || payload.Get("content_index").Int() != int64(partIndex) || payload.Get("item_id").String() == "" || payload.Get("item_id").String() != messageIDs[payload.Get("output_index").Int()] {
					t.Fatalf("text lifecycle lost its source boundary or identity: %s, source=%s", data, sourceData)
				}
				switch payload.Get("type").String() {
				case "response.content_part.added":
					addedParts++
					if sourceEvent != "content_block_start" || payload.Get("part.text").String() != "" {
						t.Fatalf("part addition did not coincide with its source start: %s", data)
					}
				case "response.content_part.done":
					doneParts++
					if sourceEvent != "content_block_stop" || payload.Get("part.text").String() != source[sourceIndex]["text"] {
						t.Fatalf("part completion did not preserve source text at its stop: %s", data)
					}
					citations := arrayValue(source[sourceIndex]["citations"])
					if len(payload.Get("part.annotations").Array()) != len(citations) {
						t.Fatalf("part completion lost citation ownership: %s", data)
					}
					for index, citation := range citations {
						encoded, err := json.Marshal(citation)
						if err != nil {
							t.Fatal(err)
						}
						requireLiveBodyJSONEqual(t, []byte(payload.Get("part.annotations").Array()[index].Raw), encoded)
					}
				case "response.output_text.done":
					doneTexts++
					if sourceEvent != "content_block_stop" || payload.Get("text").String() != source[sourceIndex]["text"] {
						t.Fatalf("text completion did not coincide with its source stop: %s", data)
					}
				}
			}
			if gjson.GetBytes(data, "type").String() == "response.output_item.done" && gjson.GetBytes(data, "item.type").String() == "message" {
				item := gjson.GetBytes(data, "item")
				doneMessages[item.Get("id").String()] = item
			}
			if gjson.GetBytes(data, "type").String() == "response.completed" {
				for _, item := range gjson.GetBytes(data, "response.output").Array() {
					if item.Get("type").String() == "message" {
						done := doneMessages[item.Get("id").String()]
						if !done.Exists() {
							t.Fatalf("terminal message has no done snapshot: %s", item.Raw)
						}
						requireLiveBodyJSONEqual(t, []byte(done.Get("content").Raw), []byte(item.Get("content").Raw))
					}
				}
			}
			client, err := StreamFromEndpoint(context.Background(), EndpointResponses, "claude", "claude-haiku-5.5", nil, nil, responseFrame, &claudeState)
			if err != nil {
				t.Fatal(err)
			}
			translated = append(translated, client...)
		}
		if textBlock && sourceEvent == "content_block_start" && addedParts != 1 {
			t.Fatalf("text block start emitted %d part additions", addedParts)
		}
		if textBlock && sourceEvent == "content_block_stop" {
			if doneParts != 1 || doneTexts != 1 {
				t.Fatalf("text block stop emitted %d part and %d text completions", doneParts, doneTexts)
			}
			partIndex++
		}
	}
	var blocks []any
	openIndex := -1
	stopped := false
	for _, payload := range claudePayloads(t, translated) {
		index := int(payload.Get("index").Int())
		switch payload.Get("type").String() {
		case "content_block_start":
			if openIndex != -1 || index != len(blocks) {
				t.Fatalf("invalid content block lifecycle: %s", payload.Raw)
			}
			var block map[string]any
			if err := json.Unmarshal([]byte(payload.Get("content_block").Raw), &block); err != nil {
				t.Fatal(err)
			}
			blocks = append(blocks, block)
			openIndex = index
		case "content_block_delta":
			if openIndex != index {
				t.Fatalf("delta on closed or missing block: %s", payload.Raw)
			}
			block := blocks[index].(map[string]any)
			switch payload.Get("delta.type").String() {
			case "text_delta":
				block["text"] = rawStringValue(block["text"]) + payload.Get("delta.text").String()
			case "citations_delta":
				var citation any
				if err := json.Unmarshal([]byte(payload.Get("delta.citation").Raw), &citation); err != nil {
					t.Fatal(err)
				}
				block["citations"] = append(arrayValue(block["citations"]), citation)
			}
		case "content_block_stop":
			if openIndex != index {
				t.Fatalf("stop on closed or missing block: %s", payload.Raw)
			}
			openIndex = -1
		case "message_stop":
			stopped = true
		}
	}
	if !stopped || openIndex != -1 {
		t.Fatal("translated stream did not close its content and message")
	}
	encoded, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	requireLiveBodyJSONEqual(t, encoded, []byte(content))
	request, err := json.Marshal(map[string]any{"model": "claude-haiku-5.5", "max_tokens": 128, "messages": []any{
		map[string]any{"role": "user", "content": "Search."},
		map[string]any{"role": "assistant", "content": blocks},
		map[string]any{"role": "user", "content": "Continue."},
	}})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := RequestForEndpointFrom("claude", "gpt-6-luna", request, false, EndpointResponses)
	if err != nil {
		t.Fatalf("completed stream is not replayable: %v", err)
	}
	restored, err := RequestForEndpointFrom("openai-response", "claude-haiku-5.5", replay, false, EndpointMessages)
	if err != nil {
		t.Fatal(err)
	}
	replayedBlocks := assistantBlocks(t, restored)
	if len(replayedBlocks) != len(source) {
		t.Fatalf("replay changed block boundaries: %s", restored)
	}
	for index, block := range replayedBlocks {
		requireLiveBodyJSONEqual(t, []byte(block.Raw), []byte(gjson.Get(content, fmt.Sprint(index)).Raw))
	}
}

func TestClaudeHostedSearchSSETerminalsPreserveExactQueriesAndResults(t *testing.T) {
	const query = "  q\n\t  "
	for _, initial := range []bool{false, true} {
		for _, stop := range []string{"end_turn", "max_tokens"} {
			for _, results := range []string{`[]`, `[{"type":"web_search_result","url":"https://example.test/q","encrypted_content":"opaque_result","title":"Exact title","page_age":"one day","snippet":"Exact snippet"}]`, `{"type":"web_search_tool_result_error","error_code":"rate_limited"}`} {
				t.Run(fmt.Sprintf("initial_%t/%s/%s", initial, stop, results), func(t *testing.T) {
					input := map[string]any{}
					if initial {
						input["query"] = query
					}
					frames := [][]byte{
						claudeHostedFrame(t, "message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_terminal", "model": "claude-haiku-5.5", "usage": map[string]any{"input_tokens": 1, "output_tokens": 0}}}),
						claudeHostedFrame(t, "content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "server_tool_use", "id": "srvtoolu_terminal", "name": "web_search", "input": input}}),
					}
					if !initial {
						encoded, err := json.Marshal(map[string]string{"query": query})
						if err != nil {
							t.Fatal(err)
						}
						for _, fragment := range []string{string(encoded[:10]), string(encoded[10:14]), string(encoded[14:])} {
							frames = append(frames, claudeHostedFrame(t, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": fragment}}))
						}
					}
					frames = append(frames,
						claudeHostedFrame(t, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}),
						claudeHostedFrame(t, "content_block_start", map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "web_search_tool_result", "tool_use_id": "srvtoolu_terminal", "content": json.RawMessage(results)}}),
						claudeHostedFrame(t, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 1}),
						claudeHostedFrame(t, "message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop}, "usage": map[string]any{"output_tokens": 1}}),
						claudeHostedFrame(t, "message_stop", map[string]any{"type": "message_stop"}),
					)
					var state any
					var doneItem, terminalItem gjson.Result
					terminalCount := 0
					for _, frame := range frames {
						out, err := StreamFromEndpoint(context.Background(), EndpointMessages, "openai-response", "claude-haiku-5.5", nil, nil, frame, &state)
						if err != nil {
							t.Fatal(err)
						}
						for _, translated := range out {
							_, data, _, err := parseSSEFrame(translated)
							if err != nil {
								t.Fatal(err)
							}
							switch gjson.GetBytes(data, "type").String() {
							case "response.output_item.added":
								if initial && gjson.GetBytes(data, "item.action.query").String() != query {
									t.Fatalf("initial query was lost in search addition: %s", data)
								}
							case "response.output_item.done":
								doneItem = gjson.GetBytes(data, "item")
							case "response.completed", "response.incomplete":
								terminalCount++
								wantEvent, wantStatus := "response.completed", "completed"
								if stop == "max_tokens" {
									wantEvent, wantStatus = "response.incomplete", "incomplete"
								}
								if gjson.GetBytes(data, "type").String() != wantEvent || gjson.GetBytes(data, "response.status").String() != wantStatus {
									t.Fatalf("changed terminal state: %s", data)
								}
								terminalItem = gjson.GetBytes(data, "response.output.0")
								if terminalItem.Get("status").String() != wantStatus {
									t.Fatalf("interrupted search status changed: %s", terminalItem.Raw)
								}
							}
						}
					}
					if terminalCount != 1 || !doneItem.Exists() || !terminalItem.Exists() {
						t.Fatalf("missing or ambiguous completion: done=%s terminal=%s count=%d", doneItem.Raw, terminalItem.Raw, terminalCount)
					}
					for _, item := range []gjson.Result{doneItem, terminalItem} {
						if item.Get("id").String() != "ws_srvtoolu_terminal" || item.Get("action.query").String() != query {
							t.Fatalf("exact query or native identity changed: %s", item.Raw)
						}
						requireLiveBodyJSONEqual(t, []byte(item.Get("results").Raw), []byte(results))
					}
					requireLiveBodyJSONEqual(t, []byte(doneItem.Raw), []byte(terminalItem.Raw))
				})
			}
		}
	}
}

func TestClaudeHostedSearchSSERejectsAmbiguousQueryFragments(t *testing.T) {
	for _, fragments := range [][]string{
		{`{"query":"one",`, `"query":"two"}`},
		{`{"query":"one",`, `"qu\u0065ry":"two"}`},
		{`{"query":"q"}`, ` trailing garbage`},
		{`{"query":"q"}`, ` {"query":"other"}`},
		{`{"query":"q","extra":"loss"}`},
		{`{"query":null}`},
		{`{"query": ["q"]}`},
		{`{"query":"unfinished`},
	} {
		t.Run(strings.Join(fragments, ""), func(t *testing.T) {
			frames := [][]byte{claudeHostedFrame(t, "content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "server_tool_use", "id": "srvtoolu_query", "name": "web_search", "input": map[string]any{}}})}
			for _, fragment := range fragments {
				frames = append(frames, claudeHostedFrame(t, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": fragment}}))
			}
			frames = append(frames, claudeHostedFrame(t, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}))
			var state any
			for index, frame := range frames {
				out, err := StreamFromEndpoint(context.Background(), EndpointMessages, "openai-response", "claude-haiku-5.5", nil, nil, frame, &state)
				if index == len(frames)-1 {
					if err == nil || len(out) != 0 {
						t.Fatalf("ambiguous query completed: error=%v frames=%q", err, out)
					}
				} else if err != nil {
					t.Fatalf("query was rejected before its fragments were assembled: %v", err)
				}
			}
		})
	}
}

func TestResponsesHostedSearchSSEReconcilesEveryIdentityAndSnapshot(t *testing.T) {
	const added = `{"id":"ws_srvtoolu_identity","type":"web_search_call","status":"in_progress","action":{"type":"search","query":""}}`
	const completed = `{"id":"ws_srvtoolu_identity","type":"web_search_call","status":"completed","action":{"type":"search","query":"  exact q  "},"results":[{"type":"web_search_result","url":"https://example.test/q","encrypted_content":"opaque_result","snippet":"Exact source"}]}`
	itemFrame := func(event string, index any, item string) []byte {
		return claudeHostedFrame(t, event, map[string]any{"type": event, "output_index": index, "item": json.RawMessage(item)})
	}
	for _, name := range []string{
		"exact completion", "terminal snapshot only", "terminal snapshot without status", "omitted search", "changed terminal ID", "missing terminal ID", "changed terminal query", "changed terminal results", "changed terminal status", "moved terminal index", "duplicate terminal ID", "missing done", "done without added", "duplicate added", "duplicate done", "reused ID at another index", "changed done ID", "changed initial query", "invalid output index", "conflicting event ID", "changed streamed item type", "completed event with incomplete status", "completed event without status", "duplicate terminal event", "exact incomplete response with completed search", "buffered text before search",
	} {
		t.Run(name, func(t *testing.T) {
			frames := [][]byte{itemFrame("response.output_item.added", 0, added), itemFrame("response.output_item.done", 0, completed)}
			terminal := `{"id":"resp_identity","status":"completed","output":[` + completed + `]}`
			event := "response.completed"
			wantErr := true
			var err error
			switch name {
			case "exact completion":
				wantErr = false
			case "terminal snapshot only":
				wantErr = false
				frames = nil
			case "terminal snapshot without status":
				frames = nil
				terminal, err = sjson.Delete(terminal, "status")
			case "omitted search":
				terminal, err = sjson.SetRaw(terminal, "output", `[]`)
			case "changed terminal ID":
				terminal, err = sjson.Set(terminal, "output.0.id", "ws_srvtoolu_changed")
			case "missing terminal ID":
				terminal, err = sjson.Delete(terminal, "output.0.id")
			case "changed terminal query":
				terminal, err = sjson.Set(terminal, "output.0.action.query", "exact q")
			case "changed terminal results":
				terminal, err = sjson.Set(terminal, "output.0.results.0.encrypted_content", "changed_replay_token")
			case "changed terminal status":
				terminal, err = sjson.Set(terminal, "output.0.status", "incomplete")
			case "moved terminal index":
				terminal, err = sjson.SetRaw(terminal, "output", `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Before"}]},`+completed+`]`)
			case "duplicate terminal ID":
				terminal, err = sjson.SetRaw(terminal, "output.1", completed)
			case "missing done":
				frames = frames[:1]
			case "done without added":
				frames = frames[1:]
			case "duplicate added":
				frames = append(frames[:1], frames...)
			case "duplicate done":
				frames = append(frames, frames[1])
			case "reused ID at another index":
				frames = append(frames, itemFrame("response.output_item.added", 1, added))
			case "changed done ID":
				var changed string
				changed, err = sjson.Set(completed, "id", "ws_srvtoolu_changed")
				frames[1] = itemFrame("response.output_item.done", 0, changed)
			case "changed initial query":
				var changed string
				changed, err = sjson.Set(added, "action.query", "different")
				frames[0] = itemFrame("response.output_item.added", 0, changed)
			case "invalid output index":
				frames[0] = itemFrame("response.output_item.added", 0.5, added)
			case "conflicting event ID":
				frames[0] = claudeHostedFrame(t, "response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 0, "item_id": "ws_srvtoolu_other", "item": json.RawMessage(added)})
			case "changed streamed item type":
				frames = append(frames, itemFrame("response.output_item.done", 0, `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Changed"}]}`))
			case "completed event with incomplete status":
				terminal, err = sjson.Set(terminal, "status", "incomplete")
			case "completed event without status":
				terminal, err = sjson.Delete(terminal, "status")
			case "duplicate terminal event":
				frames = append(frames, claudeHostedFrame(t, event, map[string]any{"type": event, "response": json.RawMessage(terminal)}))
			case "exact incomplete response with completed search":
				wantErr = false
				event = "response.incomplete"
				terminal = `{"id":"resp_identity","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[` + completed + `]}`
			case "buffered text before search":
				wantErr = false
				frames = [][]byte{
					claudeHostedFrame(t, "response.output_text.delta", map[string]any{"type": "response.output_text.delta", "output_index": 0, "content_index": 0, "delta": "Before"}),
					itemFrame("response.output_item.added", 1, added),
					itemFrame("response.output_item.done", 1, completed),
				}
				terminal, err = sjson.SetRaw(terminal, "output", `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Before"}]},`+completed+`]`)
			}
			if err != nil {
				t.Fatal(err)
			}
			frames = append(frames, claudeHostedFrame(t, event, map[string]any{"type": event, "response": json.RawMessage(terminal)}))
			var state any
			var translated [][]byte
			for _, frame := range frames {
				out, err := StreamFromEndpoint(context.Background(), EndpointResponses, "claude", "claude-haiku-5.5", nil, nil, frame, &state)
				if err != nil {
					if !wantErr || len(out) != 0 {
						t.Fatalf("unexpected error or partial error output: %v, %q", err, out)
					}
					return
				}
				translated = append(translated, out...)
			}
			if wantErr {
				t.Fatalf("ambiguous search completed successfully: %q", translated)
			}
			searchCount, resultCount, stopCount := 0, 0, 0
			for _, payload := range claudePayloads(t, translated) {
				switch payload.Get("content_block.type").String() {
				case "server_tool_use":
					searchCount++
					if payload.Get("content_block.id").String() != "srvtoolu_identity" || payload.Get("content_block.input.query").String() != "  exact q  " {
						t.Fatalf("native ID or exact query changed: %s", payload.Raw)
					}
				case "web_search_tool_result":
					resultCount++
					requireLiveBodyJSONEqual(t, []byte(payload.Get("content_block.content").Raw), []byte(gjson.Get(completed, "results").Raw))
				}
				if payload.Get("type").String() == "message_stop" {
					stopCount++
				}
			}
			if searchCount != 1 || resultCount != 1 || stopCount != 1 {
				t.Fatalf("completion lost or duplicated a search pair: %q", translated)
			}
		})
	}
}

func TestClaudeHostedSearchSSEInterruptedSearchFailsExplicitly(t *testing.T) {
	for _, stoppedQuery := range []bool{false, true} {
		t.Run(fmt.Sprintf("query_stopped_%t", stoppedQuery), func(t *testing.T) {
			frames := [][]byte{claudeHostedFrame(t, "content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "server_tool_use", "id": "srvtoolu_interrupted", "name": "web_search", "input": map[string]any{"query": " exact query "}}})}
			if stoppedQuery {
				frames = append(frames, claudeHostedFrame(t, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}))
			}
			frames = append(frames,
				claudeHostedFrame(t, "message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "max_tokens"}, "usage": map[string]any{"output_tokens": 1}}),
				claudeHostedFrame(t, "message_stop", map[string]any{"type": "message_stop"}),
			)
			var state any
			for index, frame := range frames {
				out, err := StreamFromEndpoint(context.Background(), EndpointMessages, "openai-response", "claude-haiku-5.5", nil, nil, frame, &state)
				if index == len(frames)-1 {
					if err == nil || len(out) != 0 {
						t.Fatalf("interrupted search claimed completion: %v, %q", err, out)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestClaudeHostedSearchSSERejectsInitialAndFragmentedQueries(t *testing.T) {
	for _, query := range []string{"", "initial"} {
		t.Run(query, func(t *testing.T) {
			var state any
			start := claudeHostedFrame(t, "content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "server_tool_use", "id": "srvtoolu_ambiguous", "name": "web_search", "input": map[string]any{"query": query}}})
			if _, err := StreamFromEndpoint(context.Background(), EndpointMessages, "openai-response", "claude-haiku-5.5", nil, nil, start, &state); err != nil {
				t.Fatal(err)
			}
			delta := claudeHostedFrame(t, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": `{"query":"fragmented"}`}})
			if out, err := StreamFromEndpoint(context.Background(), EndpointMessages, "openai-response", "claude-haiku-5.5", nil, nil, delta, &state); err == nil || len(out) != 0 {
				t.Fatalf("ambiguous query sources accepted: %v, %q", err, out)
			}
		})
	}
}

func TestResponsesClaudeSSEOrdinaryTextStreamsBeforeTerminal(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"output_text", "refusal"} {
		for _, terminal := range []string{"response.completed", "response.incomplete"} {
			for _, added := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/part_added_%t", kind, terminal, added), func(t *testing.T) {
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
					feed("response.created", map[string]any{"response": map[string]any{"id": "resp_streaming", "model": "gpt-6-luna", "status": "in_progress", "output": []any{}}})
					textField, deltaEvent := "text", "response.output_text.delta"
					if kind == "refusal" {
						textField, deltaEvent = "refusal", "response.refusal.delta"
					}
					if added {
						feed("response.content_part.added", map[string]any{"output_index": 0, "content_index": 0, "item_id": "msg_streaming", "part": map[string]any{"type": kind, textField: ""}})
					}
					for _, fragment := range []string{"First ", "second."} {
						out := feed(deltaEvent, map[string]any{"output_index": 0, "content_index": 0, "item_id": "msg_streaming", "delta": fragment})
						var actual strings.Builder
						for _, payload := range claudePayloads(t, out) {
							if payload.Get("type").String() == "message_stop" {
								t.Fatal("stream stopped before its terminal frame")
							}
							if payload.Get("delta.type").String() == "text_delta" {
								actual.WriteString(payload.Get("delta.text").String())
							}
						}
						if actual.String() != fragment {
							t.Fatalf("%s did not deliver its actual text before terminal: got %q, want %q, frames=%q", deltaEvent, actual.String(), fragment, out)
						}
					}
					part := map[string]any{"type": kind, textField: "First second.", "annotations": []any{}}
					if added {
						feed("response.content_part.done", map[string]any{"output_index": 0, "content_index": 0, "item_id": "msg_streaming", "part": part})
					}
					response := map[string]any{"id": "resp_streaming", "status": "completed", "output": []any{map[string]any{"id": "msg_streaming", "type": "message", "role": "assistant", "content": []any{part}}}}
					if terminal == "response.incomplete" {
						response["status"] = "incomplete"
						response["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
					}
					feed(terminal, map[string]any{"response": response})
					var text strings.Builder
					starts, stops, messageStops := 0, 0, 0
					for _, payload := range claudePayloads(t, translated) {
						text.WriteString(payload.Get("delta.text").String())
						switch payload.Get("type").String() {
						case "content_block_start":
							starts++
						case "content_block_stop":
							stops++
						case "message_stop":
							messageStops++
						}
					}
					if text.String() != "First second." || starts != 1 || stops != 1 || messageStops != 1 {
						t.Fatalf("terminal duplicated text or changed its block lifecycle: text=%q starts=%d stops=%d message_stops=%d", text.String(), starts, stops, messageStops)
					}
				})
			}
		}
	}
}

func TestResponsesClaudeSSERejectsChangedTextPartBoundariesAndCitations(t *testing.T) {
	t.Parallel()
	const citation = `{"type":"web_search_result_location","url":"https://example.test/a","encrypted_index":"opaque_a","cited_text":"A"}`
	const parts = `[{"type":"output_text","text":"Uncited.","annotations":[]},{"type":"output_text","text":"A","annotations":[` + citation + `]}]`
	for _, buffered := range []bool{false, true} {
		for _, changed := range []string{"text", "boundary", "citation", "omitted part", "duplicate part done"} {
			t.Run(fmt.Sprintf("buffered_%t/%s", buffered, changed), func(t *testing.T) {
				outputIndex := 0
				var frames [][]byte
				if buffered {
					outputIndex = 1
					frames = append(frames, claudeHostedFrame(t, "response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "function_call", "id": "fc_buffer", "call_id": "call_buffer", "name": "lookup", "arguments": ""}}))
				}
				for index, part := range gjson.Parse(parts).Array() {
					frames = append(frames,
						claudeHostedFrame(t, "response.output_text.delta", map[string]any{"type": "response.output_text.delta", "output_index": outputIndex, "content_index": index, "delta": part.Get("text").String()}),
						claudeHostedFrame(t, "response.content_part.done", map[string]any{"type": "response.content_part.done", "output_index": outputIndex, "content_index": index, "part": json.RawMessage(part.Raw)}),
					)
				}
				terminalParts := parts
				var err error
				switch changed {
				case "text":
					terminalParts, err = sjson.Set(parts, "1.text", "Changed")
				case "boundary":
					terminalParts = `[{"type":"output_text","text":"Uncited.A","annotations":[` + citation + `]}]`
				case "citation":
					terminalParts, err = sjson.Set(parts, "1.annotations.0.encrypted_index", "changed_ownership")
				case "omitted part":
					terminalParts, err = sjson.Delete(parts, "1")
				case "duplicate part done":
					frames = append(frames, frames[len(frames)-1])
				}
				if err != nil {
					t.Fatal(err)
				}
				output := []any{}
				if buffered {
					output = append(output, map[string]any{"type": "function_call", "id": "fc_buffer", "call_id": "call_buffer", "name": "lookup", "arguments": "{}"})
				}
				output = append(output, map[string]any{"type": "message", "role": "assistant", "content": json.RawMessage(terminalParts)})
				frames = append(frames, claudeHostedFrame(t, "response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": output}}))
				var state any
				for index, frame := range frames {
					out, err := StreamFromEndpoint(context.Background(), EndpointResponses, "claude", "claude-haiku-5.5", nil, nil, frame, &state)
					if err != nil {
						failureIndex := len(frames) - 1
						if changed == "duplicate part done" {
							failureIndex--
						}
						if index != failureIndex || len(out) != 0 {
							t.Fatalf("text failed at an unexpected event or emitted partial error output: index=%d error=%v frames=%q", index, err, out)
						}
						return
					}
				}
				t.Fatal("changed text boundaries or citation ownership completed successfully")
			})
		}
	}
}

func TestClaudeHostedSearchSSEAdjacentTextWithoutSearchKeepsCitationOwnership(t *testing.T) {
	t.Parallel()
	const content = `[{"type":"text","text":""},{"type":"text","text":"Uncited."},{"type":"text","text":"A","citations":[{"type":"web_search_result_location","url":"https://example.test/a","encrypted_index":"opaque_a","cited_text":"A"}]},{"type":"text","text":"A","citations":[{"type":"web_search_result_location","url":"https://example.test/b","encrypted_index":"opaque_b","cited_text":"B"}]}]`
	for _, stop := range []string{"end_turn", "max_tokens"} {
		t.Run(stop, func(t *testing.T) {
			var source []map[string]any
			if err := json.Unmarshal([]byte(content), &source); err != nil {
				t.Fatal(err)
			}
			frames := [][]byte{claudeHostedFrame(t, "message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_adjacent_stream", "model": "claude-haiku-5.5", "usage": map[string]any{"input_tokens": 1, "output_tokens": 0}}})}
			for index, block := range source {
				start := map[string]any{"type": "text", "text": ""}
				frames = append(frames,
					claudeHostedFrame(t, "content_block_start", map[string]any{"type": "content_block_start", "index": index, "content_block": start}),
					claudeHostedFrame(t, "content_block_delta", map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": "text_delta", "text": block["text"]}}),
				)
				for _, citation := range arrayValue(block["citations"]) {
					frames = append(frames, claudeHostedFrame(t, "content_block_delta", map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": "citations_delta", "citation": citation}}))
				}
				frames = append(frames, claudeHostedFrame(t, "content_block_stop", map[string]any{"type": "content_block_stop", "index": index}))
			}
			frames = append(frames,
				claudeHostedFrame(t, "message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop}, "usage": map[string]any{"output_tokens": 1}}),
				claudeHostedFrame(t, "message_stop", map[string]any{"type": "message_stop"}),
			)
			var responseState, claudeState any
			var clientFrames [][]byte
			for _, frame := range frames {
				_, sourceData, _, err := parseSSEFrame(frame)
				if err != nil {
					t.Fatal(err)
				}
				out, err := StreamFromEndpoint(context.Background(), EndpointMessages, "openai-response", "gpt-6-luna", nil, nil, frame, &responseState)
				if err != nil {
					t.Fatal(err)
				}
				var immediate strings.Builder
				for _, responseFrame := range out {
					translated, err := StreamFromEndpoint(context.Background(), EndpointResponses, "claude", "claude-haiku-5.5", nil, nil, responseFrame, &claudeState)
					if err != nil {
						t.Fatal(err)
					}
					for _, payload := range claudePayloads(t, translated) {
						immediate.WriteString(payload.Get("delta.text").String())
					}
					clientFrames = append(clientFrames, translated...)
				}
				if gjson.GetBytes(sourceData, "delta.type").String() == "text_delta" && immediate.String() != gjson.GetBytes(sourceData, "delta.text").String() {
					t.Fatalf("Claude-origin ordinary text was buffered: source=%s delivered=%q", sourceData, immediate.String())
				}
			}
			var blocks []any
			openIndex := -1
			for _, payload := range claudePayloads(t, clientFrames) {
				index := int(payload.Get("index").Int())
				switch payload.Get("type").String() {
				case "content_block_start":
					if openIndex != -1 || index != len(blocks) {
						t.Fatalf("adjacent text blocks overlapped or lost their boundary: %s", payload.Raw)
					}
					blocks = append(blocks, map[string]any{"type": "text", "text": ""})
					openIndex = index
				case "content_block_delta":
					if openIndex != index {
						t.Fatalf("delta changed a closed text block: %s", payload.Raw)
					}
					block := blocks[index].(map[string]any)
					if payload.Get("delta.type").String() == "text_delta" {
						block["text"] = rawStringValue(block["text"]) + payload.Get("delta.text").String()
					} else if payload.Get("delta.type").String() == "citations_delta" {
						var citation any
						if err := json.Unmarshal([]byte(payload.Get("delta.citation").Raw), &citation); err != nil {
							t.Fatal(err)
						}
						block["citations"] = append(arrayValue(block["citations"]), citation)
					}
				case "content_block_stop":
					if openIndex != index {
						t.Fatalf("stop changed a closed text block: %s", payload.Raw)
					}
					openIndex = -1
				}
			}
			encoded, err := json.Marshal(blocks)
			if err != nil {
				t.Fatal(err)
			}
			if openIndex != -1 {
				t.Fatal("adjacent text stream left a content block open")
			}
			requireLiveBodyJSONEqual(t, encoded, []byte(content))
		})
	}
}

func TestClaudeHostedSearchSSERejectsLateTextOrCitations(t *testing.T) {
	t.Parallel()
	for _, delta := range []map[string]any{
		{"type": "text_delta", "text": "Changed after stop."},
		{"type": "citations_delta", "citation": map[string]any{"type": "web_search_result_location", "url": "https://example.test/a", "encrypted_index": "opaque_late", "cited_text": "A"}},
	} {
		t.Run(stringValue(delta["type"]), func(t *testing.T) {
			frames := [][]byte{
				claudeHostedFrame(t, "message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_late", "model": "claude-haiku-5.5", "usage": map[string]any{"input_tokens": 1, "output_tokens": 0}}}),
				claudeHostedFrame(t, "content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}}),
				claudeHostedFrame(t, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "A"}}),
				claudeHostedFrame(t, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}),
				claudeHostedFrame(t, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": delta}),
			}
			var state any
			for index, frame := range frames {
				out, err := StreamFromEndpoint(context.Background(), EndpointMessages, "openai-response", "claude-haiku-5.5", nil, nil, frame, &state)
				if index == len(frames)-1 {
					if err == nil || len(out) != 0 {
						t.Fatalf("completed text part accepted a late update: error=%v frames=%q", err, out)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
