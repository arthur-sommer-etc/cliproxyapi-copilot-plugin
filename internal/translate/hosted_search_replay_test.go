package translate

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const syntheticHostedSearchReplay = `{"input":[{"type":"message","role":"user","content":"Search."},{"type":"web_search_call","id":"ws_srvtoolu_fixture_search","status":"completed","action":{"type":"search","query":"  exact query\n\t "},"results":[{"type":"web_search_result","url":"https://example.test/report?source=fixture","title":"Fixture report","encrypted_content":"fixture_opaque_replay_token","page_age":"one day"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Fixture answer.","annotations":[{"type":"web_search_result_location","url":"https://example.test/report?source=fixture","title":"Fixture report","encrypted_index":"fixture_opaque_citation","cited_text":"Fixture excerpt."}]}]},{"type":"message","role":"user","content":"Continue without another search."}]}`

func TestHostedSearchReplayPreservesDestinationNativeBlocks(t *testing.T) {
	for _, results := range []string{
		gjson.Get(syntheticHostedSearchReplay, "input.1.results").Raw,
		`[]`,
		`{"type":"web_search_tool_result_error","error_code":"max_uses_exceeded"}`,
		`[{"type":"web_search_tool_result_error","error_code":"max_uses_exceeded"}]`,
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", results, stream), func(t *testing.T) {
				request, err := sjson.SetRaw(syntheticHostedSearchReplay, "input.1.results", results)
				if err != nil {
					t.Fatal(err)
				}
				translated, err := RequestForEndpointFrom("openai-response", "claude-haiku-5.5", []byte(request), stream, EndpointMessages)
				if err != nil {
					t.Fatal(err)
				}
				var blocks []gjson.Result
				for _, message := range gjson.GetBytes(translated, "messages").Array() {
					if message.Get("role").String() == "assistant" {
						blocks = append(blocks, message.Get("content").Array()...)
					}
				}
				if len(blocks) != 3 {
					t.Fatalf("hosted search pair and cited answer = %d blocks: %s", len(blocks), translated)
				}
				wantUse := []byte(`{"type":"server_tool_use","id":"srvtoolu_fixture_search","name":"web_search","input":{"query":"  exact query\n\t "}}`)
				requireLiveBodyJSONEqual(t, []byte(blocks[0].Raw), wantUse)
				wantResult := []byte(`{"type":"web_search_tool_result","tool_use_id":"srvtoolu_fixture_search","content":` + results + `}`)
				requireLiveBodyJSONEqual(t, []byte(blocks[1].Raw), wantResult)
				wantAnswer := []byte(`{"type":"text","text":"Fixture answer.","citations":` + gjson.Get(request, "input.2.content.0.annotations").Raw + `}`)
				requireLiveBodyJSONEqual(t, []byte(blocks[2].Raw), wantAnswer)
				if gjson.GetBytes(translated, "messages.0.content").String() != "Search." || gjson.GetBytes(translated, "messages.2.content").String() != "Continue without another search." || gjson.GetBytes(translated, "stream").Bool() != stream {
					t.Fatalf("surrounding turns or stream flag changed: %s", translated)
				}
			})
		}
	}
}

func TestHostedSearchReplayRejectsLossyHistory(t *testing.T) {
	cases := []struct {
		name, path string
		value      any
	}{
		{"native identity", "input.1.id", "ws_fixture_native_search"},
		{"sanitized identity", "input.1.id", "ws_srvtoolu_fixture-search"},
		{"incomplete search", "input.1.status", "in_progress"},
		{"open page", "input.1.action.type", "open_page"},
		{"multiple queries", "input.1.action.queries", []string{"first", "second"}},
		{"action sources", "input.1.action.sources", []any{map[string]any{"url": "https://example.test/report"}}},
		{"extra item state", "input.1.provider_state", "fixture_state"},
		{"missing replay token", "input.1.results.0.encrypted_content", ""},
		{"wrong result type", "input.1.results.0.type", "native_search_result"},
		{"non-object result", "input.1.results", []any{"fixture result"}},
		{"non-result object", "input.1.results", map[string]any{"url": "https://example.test/report"}},
		{"malformed error", "input.1.results", map[string]any{"type": "web_search_tool_result_error", "error_code": ""}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			request, err := sjson.Set(syntheticHostedSearchReplay, test.path, test.value)
			if err != nil {
				t.Fatal(err)
			}
			for _, stream := range []bool{false, true} {
				if out, err := RequestForEndpointFrom("openai-response", "claude-haiku-5.5", []byte(request), stream, EndpointMessages); err == nil || !strings.Contains(err.Error(), "web search") || len(out) != 0 {
					t.Fatalf("lossy search accepted: %s, error=%v", out, err)
				}
			}
		})
	}
	for _, field := range []string{"input.1.results", "input.1.action.query"} {
		request, err := sjson.Delete(syntheticHostedSearchReplay, field)
		if err != nil {
			t.Fatal(err)
		}
		if out, err := RequestForEndpointFrom("openai-response", "claude-haiku-5.5", []byte(request), false, EndpointMessages); err == nil || len(out) != 0 {
			t.Fatalf("missing %s accepted: %s, error=%v", field, out, err)
		}
	}
	var request map[string]any
	if err := json.Unmarshal([]byte(syntheticHostedSearchReplay), &request); err != nil {
		t.Fatal(err)
	}
	input := request["input"].([]any)
	request["input"] = append(input, input[1])
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := RequestForEndpointFrom("openai-response", "claude-haiku-5.5", body, false, EndpointMessages); err == nil || len(out) != 0 {
		t.Fatalf("duplicate hosted identity accepted: %s, error=%v", out, err)
	}
}

func TestCapturedHostedSearchHistoryRequiresNativeDestination(t *testing.T) {
	body := readNativeServerToolFixture(t, "gpt-responses-search", "response.json")
	request := []byte(`{"input":` + gjson.GetBytes(body, "output").Raw + `}`)
	for _, stream := range []bool{false, true} {
		translated, err := RequestForEndpointFrom("openai-response", "gpt-6-luna", request, stream, EndpointResponses)
		if err != nil {
			t.Fatal(err)
		}
		requireLiveBodyJSONEqual(t, []byte(gjson.GetBytes(translated, "input").Raw), []byte(gjson.GetBytes(request, "input").Raw))
		if out, err := RequestForEndpointFrom("openai-response", "claude-haiku-5.5", request, stream, EndpointMessages); err == nil || !strings.Contains(err.Error(), "web search") || len(out) != 0 {
			t.Fatalf("native provider search history became Claude replay state: %s, error=%v", out, err)
		}
	}
}
