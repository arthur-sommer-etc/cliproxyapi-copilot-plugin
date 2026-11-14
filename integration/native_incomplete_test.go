package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestNativeHostPreservesIncompleteResponsesTerminal(t *testing.T) {
	binary := os.Getenv("CPA_BINARY")
	if binary == "" {
		t.Skip("set CPA_BINARY to the prepared CLIProxyAPI v8 server for native host integration")
	}
	state := newNativeFixture(t)
	partial := map[string]any{
		"id": "resp_partial", "object": "response", "model": "gpt-6-luna", "status": "incomplete", "error": nil,
		"incomplete_details": map[string]any{"reason": "max_output_tokens"},
		"output": []any{map[string]any{
			"id": "msg_partial", "type": "message", "role": "assistant", "status": "incomplete",
			"content": []any{map[string]any{"type": "output_text", "text": "Synthetic partial answer", "annotations": []any{}}},
		}},
		"usage": map[string]any{"input_tokens": 12, "output_tokens": 5, "total_tokens": 17, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens_details": map[string]any{"reasoning_tokens": 2}},
	}
	terminal := map[string]any{"type": "response.incomplete", "sequence_number": 3, "response": partial}
	terminalJSON, err := json.Marshal(terminal)
	if err != nil {
		t.Fatal(err)
	}
	state.seeds["responses.sse"] = []byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_partial\",\"status\":\"in_progress\",\"model\":\"gpt-6-luna\",\"output\":[]}}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_partial\",\"output_index\":0,\"content_index\":0,\"delta\":\"Synthetic partial answer\"}\n\nevent: response.incomplete\ndata: " + string(terminalJSON) + "\n\n")
	upstream := httptest.NewServer(state)
	t.Cleanup(upstream.Close)
	base := startProxy(t, binary, upstream.URL)
	request := map[string]any{"model": "gpt-6-luna", "input": "Synthetic partial termination probe", "max_output_tokens": 5, "stream": true}
	status, body := postProxyWithSession(t, base+"/v1/responses", request, "synthetic-incomplete-session")
	if status != http.StatusOK {
		t.Fatalf("incomplete response status=%d body=%s", status, body)
	}
	events, done := parseSSEDataEventsWithDone(t, body)
	if done || len(events) != 3 {
		t.Fatalf("incomplete stream has %d events and DONE=%v: %s", len(events), done, body)
	}
	assertEventType(t, events[1], "response.output_text.delta")
	assertEqualJSON(t, events[len(events)-1], terminal)
	captured, path := lastUpstreamRequest(t, state)
	if path != "/responses" || captured["model"] != "gpt-6-luna" || captured["stream"] != true {
		t.Fatalf("incomplete probe upstream request: path=%q request=%+v", path, captured)
	}
	for _, destination := range []struct {
		name string
		path string
		stop string
	}{
		{name: "Chat", path: "/v1/chat/completions", stop: "length"},
		{name: "Messages", path: "/v1/messages", stop: "max_tokens"},
	} {
		t.Run(destination.name, func(t *testing.T) {
			request := map[string]any{"model": "gpt-6-luna", "messages": []any{map[string]any{"role": "user", "content": "Synthetic partial termination probe"}}, "max_tokens": 5, "stream": true}
			status, body := postProxyWithSession(t, base+destination.path, request, "synthetic-incomplete-"+destination.name)
			if status != http.StatusOK {
				t.Fatalf("partial %s status=%d body=%s", destination.name, status, body)
			}
			events, _ := parseSSEDataEventsWithDone(t, body)
			var text strings.Builder
			terminals := 0
			for _, event := range events {
				if event["type"] == "error" || event["error"] != nil {
					t.Fatalf("partial stream returned an error: %+v", event)
				}
				if destination.name == "Messages" {
					delta, _ := event["delta"].(map[string]any)
					text.WriteString(stringValue(delta["text"]))
					if event["type"] == "message_delta" {
						usage, _ := event["usage"].(map[string]any)
						if delta["stop_reason"] != destination.stop || usage["input_tokens"] != float64(12) || usage["output_tokens"] != float64(5) {
							t.Fatalf("partial Messages terminal changed: %+v", event)
						}
						terminals++
					}
				} else {
					for _, choice := range jsonObjects(event["choices"]) {
						delta, _ := choice["delta"].(map[string]any)
						text.WriteString(stringValue(delta["content"]))
						if choice["finish_reason"] != nil {
							usage, _ := event["usage"].(map[string]any)
							if choice["finish_reason"] != destination.stop || usage["prompt_tokens"] != float64(12) || usage["completion_tokens"] != float64(5) || usage["total_tokens"] != float64(17) {
								t.Fatalf("partial Chat terminal changed: %+v", event)
							}
							terminals++
						}
					}
				}
			}
			if text.String() != "Synthetic partial answer" || terminals != 1 {
				t.Fatalf("partial %s text=%q terminal count=%d", destination.name, text.String(), terminals)
			}
			captured, path := lastUpstreamRequest(t, state)
			if path != "/responses" || captured["model"] != "gpt-6-luna" || captured["stream"] != true {
				t.Fatalf("partial translated probe upstream request: path=%q request=%+v", path, captured)
			}
		})
	}
}
