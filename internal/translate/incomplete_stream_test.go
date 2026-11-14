package translate

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestResponsesIncompletePreservesTranslatedPartialOutput(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		destination string
		reason      string
	}{
		{destination: "claude", reason: "max_output_tokens"}, {destination: "openai", reason: "max_output_tokens"},
		{destination: "claude", reason: "content_filter"}, {destination: "openai", reason: "content_filter"},
	} {
		t.Run(test.destination+"/"+test.reason, func(t *testing.T) {
			destination := test.destination
			request := []byte(`{"model":"gpt-6-luna","messages":[{"role":"user","content":"Synthetic partial probe"}],"stream":true}`)
			frames := []string{
				`{"type":"response.created","response":{"id":"resp_partial","model":"gpt-6-luna","status":"in_progress","output":[]}}`,
				`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"Partial "}`,
				`{"type":"response.incomplete","response":{"id":"resp_partial","model":"gpt-6-luna","status":"incomplete","error":null,"incomplete_details":{"reason":"max_output_tokens"},"output":[{"id":"msg_partial","type":"message","status":"incomplete","role":"assistant","content":[{"type":"output_text","text":"Partial answer","annotations":[]}]}],"usage":{"input_tokens":12,"output_tokens":5,"total_tokens":17,"input_tokens_details":{"cached_tokens":3},"output_tokens_details":{"reasoning_tokens":2}}}}`,
			}
			var state any
			var text strings.Builder
			var final gjson.Result
			stops := 0
			for _, source := range frames {
				source = strings.ReplaceAll(source, "max_output_tokens", test.reason)
				out, err := StreamFromEndpoint(context.Background(), EndpointResponses, destination, "gpt-6-luna", request, request, []byte("data: "+source+"\n\n"), &state)
				if err != nil {
					t.Fatal(err)
				}
				for _, frame := range out {
					data := frame
					if destination == "claude" {
						_, data, _, err = parseSSEFrame(frame)
						if err != nil {
							t.Fatal(err)
						}
					}
					payload := gjson.ParseBytes(data)
					if destination == "claude" {
						text.WriteString(payload.Get("delta.text").String())
						if payload.Get("type").String() == "message_delta" {
							final = payload
						}
						if payload.Get("type").String() == "message_stop" {
							stops++
						}
					} else {
						text.WriteString(payload.Get("choices.0.delta.content").String())
						if payload.Get("choices.0.finish_reason").String() != "" {
							final = payload
							stops++
						}
					}
				}
			}
			if text.String() != "Partial answer" || stops != 1 {
				t.Fatalf("partial output=%q terminal count=%d", text.String(), stops)
			}
			if destination == "claude" {
				stop := "max_tokens"
				if test.reason == "content_filter" {
					stop = "refusal"
				}
				if final.Get("delta.stop_reason").String() != stop || final.Get("usage.input_tokens").Int() != 12 || final.Get("usage.output_tokens").Int() != 5 || final.Get("usage.cache_read_input_tokens").Int() != 3 {
					t.Fatalf("partial Messages terminal changed: %s", final.Raw)
				}
			} else {
				stop := "length"
				if test.reason == "content_filter" {
					stop = "content_filter"
				}
				if final.Get("choices.0.finish_reason").String() != stop || final.Get("usage.prompt_tokens").Int() != 12 || final.Get("usage.completion_tokens").Int() != 5 || final.Get("usage.total_tokens").Int() != 17 || final.Get("usage.completion_tokens_details.reasoning_tokens").Int() != 2 {
					t.Fatalf("partial Chat terminal changed: %s", final.Raw)
				}
			}
		})
	}
}

func TestNativeResponsesIncompleteWebSearchPreservesAuthoritativeSnapshot(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"completed", "completed snapshot only", "in_progress", "searching", "failed"} {
		t.Run(name, func(t *testing.T) {
			status := strings.TrimSuffix(name, " snapshot only")
			frames := [][]byte{[]byte(`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"early","type":"web_search_call","status":"in_progress"}}` + "\n\n")}
			if name == "completed" {
				frames = append(frames, []byte(`data: {"type":"response.web_search_call.completed","output_index":0,"item_id":"phase"}`+"\n\n"), []byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"done","type":"web_search_call","status":"completed"}}`+"\n\n"))
			} else if status == "failed" {
				frames = append(frames, []byte(`data: {"type":"response.web_search_call.failed","output_index":0,"item_id":"phase"}`+"\n\n"))
			}
			terminal := []byte(`data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"id":"final","type":"web_search_call","status":"` + status + `"}],"usage":{"input_tokens":12,"output_tokens":5,"total_tokens":17}}}` + "\n\n")
			frames = append(frames, terminal)
			var state any
			var output [][]byte
			for index, frame := range frames {
				out, err := nativeResponsesStream(frame, &state, false)
				if err != nil {
					t.Fatal(err)
				}
				if index < len(frames)-1 && len(out) != 0 {
					t.Fatal("search identity emitted before authoritative terminal")
				}
				output = append(output, out...)
			}
			if len(output) != len(frames) || !bytes.Equal(output[len(output)-1], terminal) || state != nil {
				t.Fatal("incomplete hosted search terminal or event count changed")
			}
			for _, frame := range output[:len(output)-1] {
				_, data, _, err := parseSSEFrame(frame)
				if err != nil {
					t.Fatal(err)
				}
				id := gjson.GetBytes(data, "item.id").String()
				if id == "" {
					id = gjson.GetBytes(data, "item_id").String()
				}
				if id != "final" {
					t.Fatalf("earlier lifecycle identity=%q", id)
				}
			}
		})
	}
}

func TestNativeResponsesIncompleteWebSearchRejectsMalformedSnapshot(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		path  string
		value any
		done  bool
	}{
		{name: "missing identity", path: "response.output.0.id", value: nil},
		{name: "numeric identity", path: "response.output.0.id", value: 123},
		{name: "empty identity", path: "response.output.0.id", value: ""},
		{name: "wrong item", path: "response.output.0.type", value: "message"},
		{name: "unknown search status", path: "response.output.0.status", value: "unknown"},
		{name: "inconsistent lifecycle", path: "response.output.0.status", value: "in_progress", done: true},
		{name: "missing reason", path: "response.incomplete_details", value: nil},
		{name: "unsuccessful status", path: "response.status", value: "failed"},
		{name: "response error", path: "response.error", value: map[string]string{"message": "synthetic-error"}},
		{name: "envelope error", path: "error", value: map[string]string{"message": "synthetic-error"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var state any
			first := []byte(`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"early","type":"web_search_call","status":"in_progress"}}` + "\n\n")
			if _, err := nativeResponsesStream(first, &state, false); err != nil {
				t.Fatal(err)
			}
			if test.done {
				if _, err := nativeResponsesStream([]byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"done","type":"web_search_call","status":"completed"}}`+"\n\n"), &state, false); err != nil {
					t.Fatal(err)
				}
			}
			terminal, err := sjson.SetBytes([]byte(`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"id":"final","type":"web_search_call","status":"in_progress"}]}}`), test.path, test.value)
			if err != nil {
				t.Fatal(err)
			}
			if out, err := nativeResponsesStream(append(append([]byte("data: "), terminal...), '\n', '\n'), &state, false); err == nil || len(out) != 0 {
				t.Fatalf("malformed incomplete snapshot accepted: frames=%d error=%v", len(out), err)
			}
		})
	}
}

func TestResponsesIncompleteRejectsUnrepresentableReason(t *testing.T) {
	t.Parallel()
	for _, destination := range []string{"claude", "openai"} {
		t.Run(destination, func(t *testing.T) {
			var state any
			request := []byte(`{"model":"gpt-6-luna","messages":[{"role":"user","content":"Synthetic prompt"}],"stream":true}`)
			frame := []byte(`data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"unrepresentable-reason"},"output":[]}}` + "\n\n")
			if out, err := StreamFromEndpoint(context.Background(), EndpointResponses, destination, "gpt-6-luna", request, request, frame, &state); err == nil || len(out) != 0 {
				t.Fatalf("unrepresentable reason accepted: frames=%d error=%v", len(out), err)
			}
		})
	}
}

func TestResponsesIncompleteNeverInventsToolInput(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		missing bool
		value   any
		custom  bool
	}{
		{name: "missing", missing: true}, {name: "empty", value: ""}, {name: "whitespace", value: " "},
		{name: "null", value: nil}, {name: "truncated JSON", value: `{"query":`}, {name: "array", value: `[]`},
		{name: "custom missing", missing: true, custom: true}, {name: "custom empty", value: "", custom: true},
		{name: "custom whitespace", value: " ", custom: true}, {name: "custom null", value: nil, custom: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			terminal := []byte(`{"type":"response.incomplete","response":{"id":"resp_partial_tool","status":"incomplete","error":null,"incomplete_details":{"reason":"max_output_tokens"},"output":[{"id":"fc_partial","type":"function_call","status":"incomplete","call_id":"call_partial","name":"lookup","arguments":"{}"}],"usage":{"input_tokens":12,"output_tokens":5,"total_tokens":17}}}`)
			var err error
			path := "response.output.0.arguments"
			if test.custom {
				terminal, err = sjson.SetBytes(terminal, "response.output.0.type", "custom_tool_call")
				if err != nil {
					t.Fatal(err)
				}
				terminal, err = sjson.DeleteBytes(terminal, "response.output.0.arguments")
				if err != nil {
					t.Fatal(err)
				}
				path = "response.output.0.input"
			}
			if test.missing {
				terminal, err = sjson.DeleteBytes(terminal, path)
			} else {
				terminal, err = sjson.SetBytes(terminal, path, test.value)
			}
			if err != nil {
				t.Fatal(err)
			}
			frame := append(append([]byte("data: "), terminal...), '\n', '\n')
			var nativeState any
			native, err := StreamFromEndpoint(context.Background(), EndpointResponses, "openai-response", "gpt-6-luna", nil, nil, frame, &nativeState)
			if err != nil || len(native) != 1 || !bytes.Equal(native[0], frame) {
				t.Fatalf("native partial call changed: frames=%q error=%v", native, err)
			}
			for _, destination := range []string{"claude", "openai"} {
				var state any
				request := []byte(`{"model":"gpt-6-luna","messages":[{"role":"user","content":"Synthetic prompt"}],"stream":true}`)
				added := []byte(`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc_partial","type":"function_call","status":"in_progress","call_id":"call_partial","name":"lookup","arguments":""}}` + "\n\n")
				if _, err := StreamFromEndpoint(context.Background(), EndpointResponses, destination, "gpt-6-luna", request, request, added, &state); err != nil {
					t.Fatal(err)
				}
				if out, err := StreamFromEndpoint(context.Background(), EndpointResponses, destination, "gpt-6-luna", request, request, frame, &state); err == nil || len(out) != 0 {
					t.Fatalf("%s invented arguments for partial call: frames=%q error=%v", destination, out, err)
				}
			}
		})
	}
}

func TestResponsesIncompletePreservesExplicitToolInput(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		item string
		want string
	}{
		{name: "function object", item: `{"id":"partial_function","type":"function_call","call_id":"call_partial","name":"lookup","arguments":"{}","status":"incomplete"}`, want: `{}`},
		{name: "custom object", item: `{"id":"partial_custom","type":"custom_tool_call","call_id":"call_partial","name":"lookup","input":{},"status":"incomplete"}`, want: `{}`},
		{name: "custom text", item: `{"id":"partial_custom","type":"custom_tool_call","call_id":"call_partial","name":"lookup","input":"actual input","status":"incomplete"}`, want: `{"input":"actual input"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, destination := range []string{"claude", "openai"} {
				request := []byte(`{"model":"gpt-6-luna","messages":[{"role":"user","content":"Synthetic prompt"}],"stream":true}`)
				frame := []byte(`data: {"type":"response.incomplete","response":{"id":"resp_partial_tool","status":"incomplete","error":null,"incomplete_details":{"reason":"max_output_tokens"},"output":[` + test.item + `],"usage":{"input_tokens":12,"output_tokens":5,"total_tokens":17}}}` + "\n\n")
				var state any
				out, err := StreamFromEndpoint(context.Background(), EndpointResponses, destination, "gpt-6-luna", request, request, frame, &state)
				if err != nil {
					t.Fatal(err)
				}
				var actual strings.Builder
				for _, chunk := range out {
					data := chunk
					if destination == "claude" {
						_, data, _, err = parseSSEFrame(chunk)
						if err != nil {
							t.Fatal(err)
						}
						actual.WriteString(gjson.GetBytes(data, "delta.partial_json").String())
					} else {
						for _, call := range gjson.GetBytes(data, "choices.0.delta.tool_calls").Array() {
							actual.WriteString(call.Get("function.arguments").String())
						}
					}
				}
				if actual.String() != test.want {
					t.Fatalf("%s tool input=%q want=%q", destination, actual.String(), test.want)
				}
			}
		})
	}
}
