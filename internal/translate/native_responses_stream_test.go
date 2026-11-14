package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/sse"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestNativeResponsesWebSearchUsesTerminalSnapshotIdentity(t *testing.T) {
	t.Parallel()
	original := readLiveBodyFixture(t, "live-codex-web-search", "client-request.json")
	request := readLiveBodyFixture(t, "live-codex-web-search", "upstream-request.json")
	body := readLiveBodyFixture(t, "live-codex-web-search", "upstream-response.sse")
	decoder := &sse.Decoder{}
	frames := decoder.Feed(body)
	var state any
	var translated [][]byte
	buffering := false
	for _, frame := range frames {
		_, data, _, err := parseSSEFrame(frame)
		if err != nil {
			t.Fatal(err)
		}
		if gjson.GetBytes(data, "item.type").String() == "web_search_call" {
			buffering = true
		}
		out, err := StreamFromEndpoint(context.Background(), EndpointResponses, "openai-response", "gpt-6-luna", original, request, frame, &state)
		if err != nil {
			t.Fatal(err)
		}
		if buffering && gjson.GetBytes(data, "type").String() != "response.completed" && len(out) != 0 {
			t.Fatal("mutable web search identity escaped before terminal completion")
		}
		translated = append(translated, out...)
	}
	if len(translated) != len(frames) {
		t.Fatalf("event count=%d, want captured %d", len(translated), len(frames))
	}
	_, terminal, _, err := parseSSEFrame(frames[len(frames)-1])
	if err != nil {
		t.Fatal(err)
	}
	finalID := gjson.GetBytes(terminal, "response.output.1.id").String()
	if finalID == "" {
		t.Fatal("captured terminal hosted search identity missing")
	}
	changed := 0
	for index, frame := range frames {
		_, source, _, err := parseSSEFrame(frame)
		if err != nil {
			t.Fatal(err)
		}
		_, actual, _, err := parseSSEFrame(translated[index])
		if err != nil {
			t.Fatal(err)
		}
		expected := source
		kind := gjson.GetBytes(source, "type").String()
		if kind != "response.completed" {
			if gjson.GetBytes(source, "response.id").Exists() {
				expected, err = sjson.SetBytes(expected, "response.id", gjson.GetBytes(terminal, "response.id").String())
				if err != nil {
					t.Fatal(err)
				}
			}
			if outputIndex := gjson.GetBytes(source, "output_index"); outputIndex.Exists() {
				id := gjson.GetBytes(terminal, fmt.Sprintf("response.output.%d.id", outputIndex.Int())).String()
				for _, path := range []string{"item.id", "item_id"} {
					if gjson.GetBytes(source, path).Exists() {
						expected, err = sjson.SetBytes(expected, path, id)
						if err != nil {
							t.Fatal(err)
						}
					}
				}
			}
		}
		requireLiveBodyJSONEqual(t, actual, expected)
		if !bytes.Equal(source, expected) {
			changed++
		}
	}
	if changed < 5 || !bytes.Equal(translated[len(translated)-1], frames[len(frames)-1]) {
		t.Fatal("captured hosted lifecycle or authoritative terminal snapshot changed")
	}
}

func TestNestedResponsesWebSearchBuffersFromFirstFrame(t *testing.T) {
	request := []byte(`{"input":[{"type":"message","role":"user","content":"search"},{"type":"additional_tools","tools":[{"type":"web_search"}]}]}`)
	frame := []byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_stream\",\"status\":\"in_progress\"}}\n\n")
	var state any
	out, err := StreamFromEndpoint(context.Background(), EndpointResponses, "openai-response", "gpt-6-luna", request, request, frame, &state)
	if err != nil || len(out) != 0 {
		t.Fatalf("nested search lifecycle leaked its first frame: output=%d error=%v", len(out), err)
	}
	pending, ok := state.(*nativeResponsesWebSearchState)
	if !ok || !pending.WholeLifecycle {
		t.Fatalf("nested search state = %#v, want whole lifecycle buffering", state)
	}
}

func TestNativeResponsesWebSearchRejectsIncompleteLifecycles(t *testing.T) {
	t.Parallel()
	body := readLiveBodyFixture(t, "live-codex-web-search", "upstream-response.sse")
	decoder := &sse.Decoder{}
	frames := decoder.Feed(body)
	tests := []struct {
		name   string
		mutate func([]byte) []byte
		finish []byte
	}{
		{name: "missing terminal done", mutate: func(frame []byte) []byte {
			_, data, _, err := parseSSEFrame(frame)
			if err != nil {
				t.Fatal(err)
			}
			if gjson.GetBytes(data, "type").String() == "response.output_item.done" && gjson.GetBytes(data, "item.type").String() == "web_search_call" {
				return nil
			}
			return frame
		}},
		{name: "missing completed snapshot item", mutate: func(frame []byte) []byte {
			event, data, _, err := parseSSEFrame(frame)
			if err != nil {
				t.Fatal(err)
			}
			if gjson.GetBytes(data, "type").String() == "response.completed" {
				data, err = sjson.SetBytes(data, "response.output.1.type", "message")
				if err != nil {
					t.Fatal(err)
				}
				return responseSSEBytes(event, data)
			}
			return frame
		}},
		{name: "missing completion followed by done sentinel", mutate: func(frame []byte) []byte {
			_, data, _, err := parseSSEFrame(frame)
			if err != nil {
				t.Fatal(err)
			}
			if gjson.GetBytes(data, "type").String() == "response.completed" {
				return nil
			}
			return frame
		}, finish: []byte("data: [DONE]\n\n")},
		{name: "failed completion", mutate: func(frame []byte) []byte {
			event, data, _, err := parseSSEFrame(frame)
			if err != nil {
				t.Fatal(err)
			}
			if gjson.GetBytes(data, "type").String() == "response.completed" {
				data, err = sjson.SetBytes(data, "response.status", "failed")
				if err != nil {
					t.Fatal(err)
				}
				return responseSSEBytes(event, data)
			}
			return frame
		}},
		{name: "numeric done identity", mutate: func(frame []byte) []byte {
			event, data, _, err := parseSSEFrame(frame)
			if err != nil {
				t.Fatal(err)
			}
			if gjson.GetBytes(data, "type").String() == "response.output_item.done" && gjson.GetBytes(data, "item.type").String() == "web_search_call" {
				data, err = sjson.SetBytes(data, "item.id", 123)
				if err != nil {
					t.Fatal(err)
				}
				return responseSSEBytes(event, data)
			}
			return frame
		}},
		{name: "numeric completed snapshot identity", mutate: func(frame []byte) []byte {
			event, data, _, err := parseSSEFrame(frame)
			if err != nil {
				t.Fatal(err)
			}
			if gjson.GetBytes(data, "type").String() == "response.completed" {
				data, err = sjson.SetBytes(data, "response.output.1.id", 123)
				if err != nil {
					t.Fatal(err)
				}
				return responseSSEBytes(event, data)
			}
			return frame
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var state any
			failed := false
			for _, frame := range append(append([][]byte(nil), frames...), test.finish) {
				if len(frame) == 0 {
					continue
				}
				if test.mutate != nil {
					frame = test.mutate(frame)
				}
				if len(frame) == 0 {
					continue
				}
				if _, err := nativeResponsesStream(frame, &state, false); err != nil {
					failed = true
					break
				}
			}
			if !failed {
				t.Fatal("incomplete native hosted search was accepted")
			}
		})
	}
}

func TestNativeResponsesWebSearchBufferIsBounded(t *testing.T) {
	t.Parallel()
	var state any
	first := []byte(`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"web_search_call","id":"early","status":"in_progress"}}` + "\n\n")
	if out, err := nativeResponsesStream(first, &state, false); err != nil || len(out) != 0 {
		t.Fatalf("start buffer: count=%d err=%v", len(out), err)
	}
	frame := []byte(fmt.Sprintf("data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\n", strings.Repeat("x", nativeWebSearchBufferLimit)))
	if _, err := nativeResponsesStream(frame, &state, false); err == nil {
		t.Fatal("unbounded pending native search stream accepted")
	}
}

func TestNativeResponsesDeclaredAutoSearchWithoutInvocationReleasesTerminal(t *testing.T) {
	t.Parallel()
	directory := "testdata/native-hosted-tool-stream-captures/gpt-search-captured-history-continuation"
	terminal := readHostedStreamFixture(t, directory, "response.json")
	for _, item := range gjson.GetBytes(terminal, "output").Array() {
		if item.Get("type").String() == "web_search_call" {
			t.Fatal("captured continuation unexpectedly invokes search")
		}
	}
	request := []byte(`{"model":"gpt-6-luna","stream":true,"tools":[{"type":"web_search"}],"tool_choice":"auto"}`)
	created := []byte(`{"type":"response.created","response":{"id":"unseen-upstream","status":"in_progress","output":[]}}`)
	completed, err := json.Marshal(map[string]any{"type": "response.completed", "response": json.RawMessage(terminal)})
	if err != nil {
		t.Fatal(err)
	}
	first := responseSSEBytes("response.created", created)
	last := responseSSEBytes("response.completed", completed)
	var state any
	if out, err := StreamFromEndpoint(context.Background(), EndpointResponses, "openai-response", "gpt-6-luna", request, request, first, &state); err != nil || out != nil {
		t.Fatalf("declared auto search leaked its created frame: output=%d err=%v", len(out), err)
	}
	out, err := StreamFromEndpoint(context.Background(), EndpointResponses, "openai-response", "gpt-6-luna", request, request, last, &state)
	if err != nil || len(out) != 2 || state != nil {
		t.Fatalf("noninvoked search terminal release: output=%d state=%T err=%v", len(out), state, err)
	}
	_, actualCreated, _, err := parseSSEFrame(out[0])
	if err != nil || gjson.GetBytes(actualCreated, "response.id").String() != gjson.GetBytes(terminal, "id").String() {
		t.Fatalf("created frame did not use the actual terminal identity: %v", err)
	}
	if !bytes.Equal(out[1], last) {
		t.Fatal("noninvoked search changed the captured terminal frame")
	}
}

func TestNativeResponsesDeclaredAutoSearchWithoutInvocationOverflowFailsClosed(t *testing.T) {
	t.Parallel()
	request := []byte(`{"model":"gpt-6-luna","stream":true,"tools":[{"type":"web_search"}],"tool_choice":"auto"}`)
	first := []byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"unseen-upstream\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
	var state any
	if out, err := StreamFromEndpoint(context.Background(), EndpointResponses, "openai-response", "gpt-6-luna", request, request, first, &state); err != nil || out != nil {
		t.Fatalf("start declared-search buffer: output=%d err=%v", len(out), err)
	}
	frame := []byte(fmt.Sprintf("data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\n", strings.Repeat("x", nativeWebSearchBufferLimit)))
	if out, err := StreamFromEndpoint(context.Background(), EndpointResponses, "openai-response", "gpt-6-luna", request, request, frame, &state); err == nil || out != nil {
		t.Fatalf("overflow returned partial native output: output=%d err=%v", len(out), err)
	}
}

func TestNativeResponsesWebSearchRejectsInvalidOutputIndexes(t *testing.T) {
	t.Parallel()
	for _, index := range []string{"1.5", "-1", `"1"`, "null", "1e0"} {
		t.Run(index, func(t *testing.T) {
			var state any
			frame := []byte(`data: {"type":"response.output_item.added","output_index":` + index + `,"item":{"type":"web_search_call","id":"early","status":"in_progress"}}` + "\n\n")
			if _, err := nativeResponsesStream(frame, &state, false); err == nil {
				t.Fatal("malformed output index was associated with a hosted search")
			}
		})
	}
}

func TestNativeResponsesWebSearchKeepsMultipleOutputIndexesSeparate(t *testing.T) {
	t.Parallel()
	frames := []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"web_search_call","id":"early-0","status":"in_progress"}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"web_search_call","id":"early-1","status":"in_progress"}}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"web_search_call","id":"done-1","status":"completed"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"web_search_call","id":"done-0","status":"completed"}}`,
		`{"type":"response.completed","response":{"status":"completed","output":[{"type":"web_search_call","id":"final-0","status":"completed"},{"type":"web_search_call","id":"final-1","status":"completed"}]}}`,
	}
	var state any
	var out [][]byte
	for _, frame := range frames {
		translated, err := nativeResponsesStream([]byte("data: "+frame+"\n\n"), &state, false)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, translated...)
	}
	if len(out) != len(frames) {
		t.Fatal("multiple hosted search event order or count changed")
	}
	for _, frame := range out[:len(out)-1] {
		_, data, _, err := parseSSEFrame(frame)
		if err != nil {
			t.Fatal(err)
		}
		index := gjson.GetBytes(data, "output_index").Int()
		if gjson.GetBytes(data, "item.id").String() != fmt.Sprintf("final-%d", index) {
			t.Fatal("hosted search final identity crossed output indexes")
		}
	}
}

func TestCapturedCodexWebSearchClientAndUpstreamShapes(t *testing.T) {
	t.Parallel()
	original := readLiveBodyFixture(t, "live-codex-web-search", "client-request.json")
	upstreamRequest := readLiveBodyFixture(t, "live-codex-web-search", "upstream-request.json")
	upstreamResponse := readLiveBodyFixture(t, "live-codex-web-search", "upstream-response.sse")
	clientResponse := readLiveBodyFixture(t, "live-codex-web-search", "client-response.sse")
	request, err := RequestForEndpointFrom("openai-response", "gpt-6-luna", original, true, EndpointResponses)
	if err != nil {
		t.Fatal(err)
	}
	requireLiveBodyJSONEqual(t, request, upstreamRequest)
	upstreamEvents, upstreamDone := liveBodySSEEvents(t, upstreamResponse)
	clientEvents, clientDone := liveBodySSEEvents(t, clientResponse)
	if !reflect.DeepEqual(upstreamEvents, clientEvents) || upstreamDone != clientDone {
		t.Fatal("captured native Responses hosted search changed before bridge normalization")
	}
}

func TestNativeResponsesWebSearchPreservesInterleavedClientTool(t *testing.T) {
	t.Parallel()
	body := readLiveBodyFixture(t, "live-codex-web-search", "upstream-response.sse")
	decoder := &sse.Decoder{}
	frames := decoder.Feed(body)
	clientTool := `{"id":"fixture-client-tool","type":"function_call","call_id":"fixture-client-call","name":"exec_command","arguments":"{}","status":"completed"}`
	var source [][]byte
	inserted := false
	for _, frame := range frames {
		event, data, _, err := parseSSEFrame(frame)
		if err != nil {
			t.Fatal(err)
		}
		if gjson.GetBytes(data, "type").String() == "response.completed" {
			data, err = sjson.SetRawBytes(data, "response.output.3", []byte(clientTool))
			if err != nil {
				t.Fatal(err)
			}
			frame = responseSSEBytes(event, data)
		}
		source = append(source, frame)
		if !inserted && gjson.GetBytes(data, "type").String() == "response.web_search_call.searching" {
			source = append(source, []byte(`data: {"type":"response.output_item.added","output_index":3,"item":`+clientTool+`}`+"\n\n"))
			source = append(source, []byte(`data: {"type":"response.output_item.done","output_index":3,"item":`+clientTool+`}`+"\n\n"))
			inserted = true
		}
	}
	var state any
	var translated [][]byte
	for _, frame := range source {
		out, err := nativeResponsesStream(frame, &state, false)
		if err != nil {
			t.Fatal(err)
		}
		translated = append(translated, out...)
	}
	if !inserted || len(translated) != len(source) {
		t.Fatal("interleaved client tool event order or count changed")
	}
	for index, frame := range source {
		_, data, _, err := parseSSEFrame(frame)
		if err != nil {
			t.Fatal(err)
		}
		if gjson.GetBytes(data, "item.type").String() == "function_call" && !bytes.Equal(frame, translated[index]) {
			t.Fatal("native web search normalization changed a client tool opaque identity or arguments")
		}
	}
}
