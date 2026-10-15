package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestResponsesToMessagesPreservesExplicitTemperature(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, temperature := range []string{"0", "0.75", "1"} {
			body := []byte(`{"input":[{"role":"user","content":"hello"}],"temperature":` + temperature + `}`)
			translated, err := RequestForEndpointFrom("openai-response", "claude-haiku-5.5", body, stream, EndpointMessages)
			if err != nil {
				t.Fatalf("translate Responses temperature %s: %v", temperature, err)
			}
			if actual := gjson.GetBytes(translated, "temperature"); actual.Type != gjson.Number || actual.Raw != temperature {
				t.Fatalf("temperature %s changed: %s", temperature, translated)
			}
		}
		for _, temperature := range []string{"null", `"0"`, "-1", "2", "1e999"} {
			body := []byte(`{"input":[{"role":"user","content":"hello"}],"temperature":` + temperature + `}`)
			if translated, err := RequestForEndpointFrom("openai-response", "claude-haiku-5.5", body, stream, EndpointMessages); err == nil || len(translated) != 0 {
				t.Fatalf("unsupported Messages temperature %s accepted: %s, %v", temperature, translated, err)
			}
		}
	}
}

func TestChatToResponsesPreservesNativeConstraintsAndToolHistory(t *testing.T) {
	request := []byte(`{"messages":[{"role":"user","content":"look up the value"},{"role":"assistant","tool_calls":[{"id":"call/+preserve","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call/+preserve","content":"found"}],"tools":[{"type":"function","function":{"name":"lookup","description":"Find a value","parameters":{"type":"object"},"strict":false}},{"type":"function","function":{"name":"write","parameters":{"type":"object"},"strict":true}},{"type":"function","function":{"name":"summarize","parameters":{"type":"object"}}}],"response_format":{"type":"json_schema","json_schema":{"name":"answer","description":"A concise answer","strict":false,"schema":{"type":"object","properties":{"answer":{"type":"string"}}}}}}`)

	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			translated, err := RequestForEndpointFrom("openai", "gpt-6-luna", request, stream, EndpointResponses)
			if err != nil {
				t.Fatalf("translate Chat request: %v", err)
			}
			if gjson.GetBytes(translated, "text.format.type").String() != "json_schema" || gjson.GetBytes(translated, "text.format.name").String() != "answer" || gjson.GetBytes(translated, "text.format.description").String() != "A concise answer" || !gjson.GetBytes(translated, "text.format.strict").Exists() || gjson.GetBytes(translated, "text.format.strict").Bool() {
				t.Fatalf("Chat response_format changed: %s", translated)
			}
			if gjson.GetBytes(translated, "text.format.schema.properties.answer.type").String() != "string" {
				t.Fatalf("Chat JSON schema was not preserved: %s", translated)
			}
			if len(gjson.GetBytes(translated, "tools").Array()) != 3 {
				t.Fatalf("function tools were lost: %s", translated)
			}
			if gjson.GetBytes(translated, "tools.0.strict").Bool() || !gjson.GetBytes(translated, "tools.1.strict").Bool() || gjson.GetBytes(translated, "tools.2.strict").Type != gjson.False {
				t.Fatalf("Chat function strictness changed: %s", translated)
			}
			if strings.Contains(gjson.GetBytes(translated, "instructions").String(), "Schema Name: answer") {
				t.Fatalf("Chat response_format became instruction prose instead of a native constraint: %s", translated)
			}
			foundCall, foundResult := false, false
			for _, item := range gjson.GetBytes(translated, "input").Array() {
				switch item.Get("type").String() {
				case "function_call":
					foundCall = item.Get("call_id").String() == "call/+preserve" && item.Get("arguments").String() == "{}"
				case "function_call_output":
					foundResult = item.Get("call_id").String() == "call/+preserve" && item.Get("output").String() == "found"
				}
			}
			if !foundCall || !foundResult {
				t.Fatalf("explicit empty arguments or paired tool identity changed: %s", translated)
			}
		})
	}
}

func TestChatToResponsesPreservesTextAndJSONModeFormats(t *testing.T) {
	for _, test := range []struct {
		name   string
		format string
		want   string
	}{
		{name: "text", format: `{"type":"text"}`, want: "text"},
		{name: "json object", format: `{"type":"json_object"}`, want: "json_object"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := []byte(`{"messages":[{"role":"user","content":"respond"}],"response_format":` + test.format + `}`)
			translated, err := RequestForEndpointFrom("openai", "gpt-6-luna", request, false, EndpointResponses)
			if err != nil {
				t.Fatalf("translate Chat response format: %v", err)
			}
			if got := gjson.GetBytes(translated, "text.format.type").String(); got != test.want {
				t.Fatalf("text.format.type = %q, want %q; request=%s", got, test.want, translated)
			}
		})
	}
}

func TestChatToMessagesPreservesZeroTemperatureAndToolIdentity(t *testing.T) {
	request := []byte(`{"temperature":0,"messages":[{"role":"user","content":"lookup"},{"role":"assistant","tool_calls":[{"id":"call/+messages","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call/+messages","content":"found"}]}`)
	translated, err := RequestForEndpointFrom("openai", "claude-sonnet-5.5", request, false, EndpointMessages)
	if err != nil {
		t.Fatalf("translate Chat request to Messages: %v", err)
	}
	if !gjson.GetBytes(translated, "temperature").Exists() || gjson.GetBytes(translated, "temperature").Type != gjson.Number || gjson.GetBytes(translated, "temperature").Float() != 0 {
		t.Fatalf("explicit zero temperature was lost: %s", translated)
	}
	var callID, resultID string
	for _, message := range gjson.GetBytes(translated, "messages").Array() {
		for _, block := range message.Get("content").Array() {
			switch block.Get("type").String() {
			case "tool_use":
				callID = block.Get("id").String()
				if block.Get("input").Raw != `{}` {
					t.Fatalf("explicit empty function arguments changed: %s", translated)
				}
			case "tool_result":
				resultID = block.Get("tool_use_id").String()
			}
		}
	}
	if callID != "call/+messages" || resultID != callID {
		t.Fatalf("Messages tool call/result identity changed: call=%q result=%q request=%s", callID, resultID, translated)
	}
}

func TestChatFunctionHistoryRequiresCompleteObjectCallsForBothDestinations(t *testing.T) {
	base := []byte(`{"messages":[{"role":"user","content":"lookup"},{"role":"assistant","tool_calls":[{"id":"call-valid","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call-valid","content":"found"}]}`)
	for _, test := range []struct {
		name string
		path string
		raw  string
		set  bool
	}{
		{name: "missing call ID", path: "messages.1.tool_calls.0.id", set: false},
		{name: "blank call ID", path: "messages.1.tool_calls.0.id", raw: `"  "`, set: true},
		{name: "missing function name", path: "messages.1.tool_calls.0.function.name", set: false},
		{name: "blank function name", path: "messages.1.tool_calls.0.function.name", raw: `"\t"`, set: true},
		{name: "missing arguments", path: "messages.1.tool_calls.0.function.arguments", set: false},
		{name: "blank arguments", path: "messages.1.tool_calls.0.function.arguments", raw: `"  "`, set: true},
		{name: "malformed arguments", path: "messages.1.tool_calls.0.function.arguments", raw: `"{bad"`, set: true},
		{name: "array arguments", path: "messages.1.tool_calls.0.function.arguments", raw: `"[]"`, set: true},
		{name: "scalar arguments", path: "messages.1.tool_calls.0.function.arguments", raw: `"null"`, set: true},
		{name: "unpaired result", path: "messages.2.tool_call_id", raw: `"missing-call"`, set: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := append([]byte(nil), base...)
			var err error
			if test.set {
				request, err = sjson.SetRawBytes(request, test.path, []byte(test.raw))
			} else {
				request, err = sjson.DeleteBytes(request, test.path)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, endpoint := range []string{EndpointResponses, EndpointMessages} {
				if translated, err := RequestForEndpointFrom("openai", "gpt-6-luna", request, false, endpoint); err == nil {
					t.Fatalf("%s accepted invalid tool history: %s", endpoint, translated)
				}
			}
		})
	}
}

func TestChatHostedSearchRequiresLosslessResponseAndHistoryBeforeTranslation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, endpoint := range []string{EndpointResponses, EndpointMessages} {
			for _, test := range []struct {
				name   string
				fields string
			}{
				{name: "search only", fields: `"web_search_options":{}`},
				{name: "nested approximate location", fields: `"web_search_options":{"search_context_size":"low","user_location":{"type":"approximate","approximate":{"country":"US","city":"Boston","region":"Massachusetts","timezone":"America/New_York"}}}`},
				{name: "search and functions", fields: `"web_search_options":{},"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]`},
				{name: "auto choice", fields: `"web_search_options":{},"tool_choice":"auto"`},
				{name: "required choice", fields: `"web_search_options":{},"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"tool_choice":"required"`},
				{name: "disabled choice", fields: `"web_search_options":{},"tool_choice":"none"`},
				{name: "function choice", fields: `"web_search_options":{},"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"tool_choice":{"type":"function","function":{"name":"lookup"}}`},
				{name: "unknown search option", fields: `"web_search_options":{"region":"US"}`},
				{name: "invalid context size", fields: `"web_search_options":{"search_context_size":"large"}`},
				{name: "null declaration", fields: `"web_search_options":null`},
			} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", test.name, endpoint, stream), func(t *testing.T) {
					request := []byte(`{"messages":[{"role":"user","content":"Search before answering."}],` + test.fields + `}`)
					original := bytes.Clone(request)
					translated, err := RequestForEndpointFrom("openai", "gpt-6-luna", request, stream, endpoint)
					if err == nil || !strings.Contains(err.Error(), "Chat hosted web search cannot be represented") || !strings.Contains(err.Error(), "response/history") || len(translated) != 0 {
						t.Fatalf("offered search did not fail at the representation boundary: output=%s error=%v", translated, err)
					}
					if !bytes.Equal(request, original) {
						t.Fatal("refusal mutated the original Chat request")
					}
				})
			}
		}
	}
}

func TestNativeChatHostedSearchPreservesRequestExactly(t *testing.T) {
	for _, stream := range []bool{false, true} {
		request := []byte(fmt.Sprintf(`{ "model":"gpt-6-luna", "stream":%t, "messages":[{"role":"user","content":"Search before answering."}], "web_search_options": {"search_context_size":"low","user_location":{"type":"approximate","approximate":{"country":"US","city":"Boston","region":"Massachusetts","timezone":"America/New_York"}}}, "tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"},"strict":false}}], "tool_choice":"auto" }`, stream))
		translated, err := RequestForEndpointFrom("openai", "gpt-6-luna", request, stream, EndpointChatCompletions)
		if err != nil || !bytes.Equal(translated, request) {
			t.Fatalf("native Chat search request changed: output=%s error=%v", translated, err)
		}
	}
}

func TestCapturedResponsesSearchRetainsNativeStateAndRejectsChat(t *testing.T) {
	body := readNativeServerToolFixture(t, "gpt-responses-search", "response.json")
	var compact bytes.Buffer
	if err := json.Compact(&compact, body); err != nil {
		t.Fatal(err)
	}
	var search, message gjson.Result
	var searchIndex int
	for index, item := range gjson.GetBytes(compact.Bytes(), "output").Array() {
		switch item.Get("type").String() {
		case "web_search_call":
			search = item
			searchIndex = index
		case "message":
			message = item
		}
	}
	if search.Get("status").String() != "completed" || len(search.Get("results").Array()) == 0 || len(message.Get("content.0.annotations").Array()) == 0 {
		t.Fatal("capture must contain completed hosted search results and answer citations")
	}
	t.Run("native Responses response and history", func(t *testing.T) {
		out, err := ResponseFromEndpoint(context.Background(), EndpointResponses, "openai-response", "gpt-6-luna", nil, nil, body)
		if err != nil || !bytes.Equal(out, body) {
			t.Fatalf("native hosted results or citations changed: output=%s error=%v", out, err)
		}
		for _, stream := range []bool{false, true} {
			history := []byte(fmt.Sprintf(`{"model":"gpt-6-luna","stream":%t,"input":%s}`, stream, gjson.GetBytes(body, "output").Raw))
			out, err := RequestForEndpointFrom("openai-response", "gpt-6-luna", history, stream, EndpointResponses)
			if err != nil || !bytes.Equal(out, history) {
				t.Fatalf("native hosted history changed: output=%s error=%v", out, err)
			}
		}
		var state any
		declared := []byte(`{"tools":[{"type":"web_search"}]}`)
		done := []byte(fmt.Sprintf("event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":%d,\"item\":%s}\n\n", searchIndex, search.Raw))
		frames, err := StreamFromEndpoint(context.Background(), EndpointResponses, "openai-response", "gpt-6-luna", nil, declared, done, &state)
		if err != nil || len(frames) != 0 {
			t.Fatalf("native search barrier did not retain the actual hosted item: frames=%s error=%v", frames, err)
		}
		terminal := []byte("event: response.completed\ndata: " + `{"type":"response.completed","response":` + compact.String() + "}\n\n")
		frames, err = StreamFromEndpoint(context.Background(), EndpointResponses, "openai-response", "gpt-6-luna", nil, declared, terminal, &state)
		if err != nil || len(frames) != 2 || !bytes.Equal(frames[1], terminal) {
			t.Fatalf("native hosted terminal bytes changed: frames=%s error=%v", frames, err)
		}
	})
	request := []byte(`{"messages":[{"role":"user","content":"Search before answering."}]}`)
	for _, test := range []struct {
		name      string
		body      []byte
		wantError string
	}{
		{name: "complete hosted response", body: compact.Bytes(), wantError: "web_search_call"},
		{name: "hosted call and results", body: []byte(`{"status":"completed","output":[` + search.Raw + `]}`), wantError: "web_search_call"},
		{name: "answer citations", body: []byte(`{"status":"completed","output":[` + message.Raw + `]}`), wantError: "annotations"},
	} {
		t.Run(test.name, func(t *testing.T) {
			out, err := ResponseFromEndpoint(context.Background(), EndpointResponses, "openai", "gpt-6-luna", request, nil, test.body)
			if test.name == "answer citations" {
				if err != nil {
					t.Fatal(err)
				}
				if gjson.GetBytes(out, "choices.0.message.content").String() != message.Get("content.0.text").String() || gjson.GetBytes(out, "choices.0.message.annotations.0.url_citation.url").String() != message.Get("content.0.annotations.0.url").String() {
					t.Fatalf("representable citation or text changed: %s", out)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.wantError) || len(out) != 0 {
				t.Fatalf("unrepresentable hosted response was accepted: output=%s error=%v", out, err)
			}
			var state any
			frame := []byte("event: response.completed\ndata: " + `{"type":"response.completed","response":` + string(test.body) + "}\n\n")
			frames, err := StreamFromEndpoint(context.Background(), EndpointResponses, "openai", "gpt-6-luna", request, nil, frame, &state)
			if err == nil || !strings.Contains(err.Error(), test.wantError) || len(frames) != 0 {
				t.Fatalf("unrepresentable hosted terminal was accepted: frames=%s error=%v", frames, err)
			}
		})
	}
	for _, item := range []gjson.Result{search, message} {
		wantError := "annotations"
		if item.Get("type").String() == "web_search_call" {
			wantError = "web_search_call"
		}
		for _, event := range []string{"response.output_item.added", "response.output_item.done"} {
			var state any
			frame := []byte("event: " + event + "\ndata: " + `{"type":"` + event + `","output_index":0,"item":` + item.Raw + "}\n\n")
			frames, err := StreamFromEndpoint(context.Background(), EndpointResponses, "openai", "gpt-6-luna", request, nil, frame, &state)
			if err == nil || !strings.Contains(err.Error(), wantError) || len(frames) != 0 {
				t.Fatalf("unrepresentable hosted item was accepted: frames=%s error=%v", frames, err)
			}
		}
	}
}

func TestChatMessagesRejectsInvalidExplicitTemperature(t *testing.T) {
	for _, raw := range []string{`null`, `"0"`, `true`, `-0.1`, `1.1`, `1e1000`} {
		t.Run(raw, func(t *testing.T) {
			request := []byte(`{"temperature":` + raw + `,"messages":[{"role":"user","content":"hello"}]}`)
			if translated, err := RequestForEndpointFrom("openai", "claude-sonnet-5.5", request, false, EndpointMessages); err == nil {
				t.Fatalf("accepted invalid Chat temperature: %s", translated)
			}
		})
	}
}

func TestChatResponsesRejectsUnsupportedResponseFormats(t *testing.T) {
	for _, raw := range []string{
		`{"type":"grammar","grammar":"x"}`,
		`{"type":"json_schema","json_schema":{"name":"answer","schema":{"type":"object"},"strict":"true"}}`,
		`{"type":"json_schema","json_schema":{"name":"answer","schema":{"type":"object"},"additional_option":true}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			request := []byte(`{"messages":[{"role":"user","content":"answer"}],"response_format":` + raw + `}`)
			if translated, err := RequestForEndpointFrom("openai", "gpt-6-luna", request, false, EndpointResponses); err == nil {
				t.Fatalf("accepted unsupported response format: %s", translated)
			}
		})
	}
}

func TestChatMessagesPropagatesSDKRefusalForAudioTurn(t *testing.T) {
	request := []byte(`{"messages":[{"role":"system","content":"policy"},{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"UklGRg==","format":"wav"}}]},{"role":"user","content":"another valid turn"}]}`)
	envelope := registry.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, sdktranslator.RequestEnvelope{
		Format: sdktranslator.FormatOpenAI,
		Model:  "claude-sonnet-5.5",
		Body:   request,
	})
	if envelope.Err == nil {
		t.Fatal("SDK no longer refuses the unsupported audio turn; update the regression to the maintained refusal")
	}
	if translated, err := RequestForEndpointFrom("openai", "claude-sonnet-5.5", request, false, EndpointMessages); err == nil || !strings.Contains(err.Error(), envelope.Err.Error()) {
		t.Fatalf("SDK refusal was not propagated: output=%s error=%v sdk=%v", translated, err, envelope.Err)
	}
}
