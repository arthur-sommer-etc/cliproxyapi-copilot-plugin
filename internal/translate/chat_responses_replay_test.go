package translate

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestChatResponsesOpaqueReplayKeepsEmptyAssistantPosition(t *testing.T) {
	t.Parallel()
	const model = "gpt-6-luna"
	prefix := []byte(`{"messages":[{"role":"user","content":"first question"}]}`)
	reasoning := []byte(`{"id":"rs_empty/+exact","type":"reasoning","summary":[],"encrypted_content":"signature-empty/+exact"}`)
	assistant := responsesReplayAssistant(t, model, prefix, reasoning)
	followup := appendChatMessages(t, prefix, assistant, []byte(`{"role":"user","content":"second question"}`))
	out, err := RequestForEndpointFrom("openai", model, followup, false, EndpointResponses)
	if err != nil {
		t.Fatalf("replay reasoning-only assistant: %v", err)
	}
	items := gjson.GetBytes(out, "input").Array()
	if len(items) != 3 || items[0].Get("content.0.text").String() != "first question" || items[1].Raw != string(reasoning) || items[2].Get("content.0.text").String() != "second question" {
		t.Fatalf("reasoning-only assistant moved or fabricated visible output: %s", out)
	}
}

func TestChatResponsesOpaqueReplayPrecedesMixedAssistantOutput(t *testing.T) {
	t.Parallel()
	const model = "gpt-6-luna"
	prefix := []byte(`{"messages":[{"role":"user","content":"inspect this"}]}`)
	reasoning := []byte(`{"id":"rs_mixed/+exact","type":"reasoning","summary":[],"encrypted_content":"signature-mixed/+exact"}`)
	message := []byte(`{"id":"msg_mixed","type":"message","role":"assistant","content":[{"type":"output_text","text":"checking now"}]}`)
	call := []byte(`{"id":"fc_mixed/+exact","type":"function_call","call_id":"call_mixed/+exact","name":"inspect","arguments":"{\"query\":\"value\"}"}`)
	assistant := responsesReplayAssistant(t, model, prefix, reasoning, message, call)
	toolID := gjson.GetBytes(assistant, "tool_calls.0.id").String()
	followup := appendChatMessages(t, prefix, assistant, []byte(`{"role":"tool","tool_call_id":"`+toolID+`","content":"found"}`), []byte(`{"role":"user","content":"continue"}`))
	out, err := RequestForEndpointFrom("openai", model, followup, false, EndpointResponses)
	if err != nil {
		t.Fatalf("replay mixed assistant: %v", err)
	}
	items := gjson.GetBytes(out, "input").Array()
	if len(items) != 6 || items[1].Raw != string(reasoning) || items[2].Get("role").String() != "assistant" || items[2].Get("content.0.text").String() != "checking now" || items[3].Get("type").String() != "function_call" || items[4].Get("type").String() != "function_call_output" || items[5].Get("content.0.text").String() != "continue" {
		t.Fatalf("reasoning moved after visible output: %s", out)
	}
	if items[3].Get("id").String() != "fc_mixed/+exact" || items[3].Get("call_id").String() != "call_mixed/+exact" || items[4].Get("call_id").String() != "call_mixed/+exact" {
		t.Fatalf("item or call identity changed: %s", out)
	}
}

func TestChatResponsesOpaqueReplayEmptyContentShapes(t *testing.T) {
	t.Parallel()
	const model = "gpt-6-luna"
	prefix := []byte(`{"messages":[{"role":"user","content":"question"}]}`)
	reasoning := []byte(`{"id":"rs_shapes","type":"reasoning","summary":[],"encrypted_content":"signature-shapes"}`)
	assistant := responsesReplayAssistant(t, model, prefix, reasoning)
	for _, content := range []string{`""`, `null`, `[]`, `[{"type":"text","text":""}]`} {
		t.Run(content, func(t *testing.T) {
			t.Parallel()
			updated, err := sjson.SetRawBytes(assistant, "content", []byte(content))
			if err != nil {
				t.Fatal(err)
			}
			followup := appendChatMessages(t, prefix, updated, []byte(`{"role":"user","content":"continue"}`))
			out, err := RequestForEndpointFrom("openai", model, followup, false, EndpointResponses)
			if err != nil {
				t.Fatalf("replay empty content %s: %v", content, err)
			}
			if gjson.GetBytes(out, "input.1").Raw != string(reasoning) {
				t.Fatalf("empty assistant fabricated visible output: %s", out)
			}
			for _, item := range gjson.GetBytes(out, "input").Array() {
				if item.Get("role").String() == "assistant" && item.Get("content.0.text").String() != "" {
					t.Fatalf("empty assistant fabricated visible output: %s", out)
				}
			}
		})
	}
}

func TestChatResponsesOpaqueReplayRepeatedTextAndNextTurn(t *testing.T) {
	t.Parallel()
	const model = "gpt-6-luna"
	prefix := []byte(`{"messages":[{"role":"user","content":"first question"}]}`)
	first := []byte(`{"id":"rs_first","type":"reasoning","summary":[],"encrypted_content":"signature-first"}`)
	second := []byte(`{"id":"rs_second","type":"reasoning","summary":[],"encrypted_content":"signature-second"}`)
	third := []byte(`{"id":"rs_third","type":"reasoning","summary":[],"encrypted_content":"signature-third"}`)
	visible := []byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"same answer"}]}`)
	assistant := responsesReplayAssistant(t, model, prefix, first, visible)
	prefix = appendChatMessages(t, prefix, assistant, []byte(`{"role":"user","content":"second question"}`))
	assistant = responsesReplayAssistant(t, model, prefix, second, visible)
	prefix = appendChatMessages(t, prefix, assistant, []byte(`{"role":"user","content":"third question"}`))
	assistant = responsesReplayAssistant(t, model, prefix, third)
	followup := appendChatMessages(t, prefix, assistant, []byte(`{"role":"user","content":"continue"}`))
	out, err := RequestForEndpointFrom("openai", model, followup, false, EndpointResponses)
	if err != nil {
		t.Fatalf("replay next-turn history: %v", err)
	}
	items := gjson.GetBytes(out, "input").Array()
	if len(items) != 9 || items[1].Raw != string(first) || items[2].Get("content.0.text").String() != "same answer" || items[3].Get("content.0.text").String() != "second question" || items[4].Raw != string(second) || items[5].Get("content.0.text").String() != "same answer" || items[6].Get("content.0.text").String() != "third question" || items[7].Raw != string(third) || items[8].Get("content.0.text").String() != "continue" {
		t.Fatalf("identical text or next-turn empty reasoning attached to wrong turn: %s", out)
	}
}

func TestChatResponsesOpaqueReplayGroupedAssistantTurns(t *testing.T) {
	t.Parallel()
	const model = "gpt-6-luna"
	prefix := []byte(`{"messages":[{"role":"user","content":"question"}]}`)
	first := []byte(`{"id":"rs_grouped_first","type":"reasoning","summary":[],"encrypted_content":"signature-first"}`)
	second := []byte(`{"id":"rs_grouped_second","type":"reasoning","summary":[],"encrypted_content":"signature-second"}`)
	assistant := responsesReplayAssistant(t, model, prefix, first, []byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first answer"}]}`))
	prefix = appendChatMessages(t, prefix, assistant)
	assistant = responsesReplayAssistant(t, model, prefix, second, []byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"second answer"}]}`))
	followup := appendChatMessages(t, prefix, assistant, []byte(`{"role":"user","content":"continue"}`))
	out, err := RequestForEndpointFrom("openai", model, followup, false, EndpointResponses)
	if err != nil {
		t.Fatalf("replay grouped assistant turns: %v", err)
	}
	items := gjson.GetBytes(out, "input").Array()
	if len(items) != 6 || items[1].Raw != string(first) || items[2].Get("content.0.text").String() != "first answer" || items[3].Raw != string(second) || items[4].Get("content.0.text").String() != "second answer" {
		t.Fatalf("grouped assistant reasoning order changed: %s", out)
	}
}

func TestChatResponsesOpaqueReplayRejectsInvalidHistory(t *testing.T) {
	t.Parallel()
	const model = "gpt-6-luna"
	prefix := []byte(`{"messages":[{"role":"user","content":"question"}]}`)
	reasoning := []byte(`{"id":"rs_validation","type":"reasoning","summary":[],"encrypted_content":"signature-validation"}`)
	assistant := responsesReplayAssistant(t, model, prefix, reasoning)
	followup := appendChatMessages(t, prefix, assistant, []byte(`{"role":"user","content":"continue"}`))
	for _, test := range []struct {
		name string
		path string
		raw  string
	}{
		{"changed context", "messages.0.content", `"different question"`},
		{"visible text on empty carrier", "messages.1.content", `"invented answer"`},
		{"visible blocks on empty carrier", "messages.1.content", `[{"type":"text","text":"invented answer"}]`},
		{"ambiguous empty turns", "messages.2", `{"role":"assistant","content":""}`},
		{"native opaque string", "messages.1.reasoning_opaque", `"native-state"`},
		{"empty opaque items", "messages.1.reasoning_opaque", `[]`},
		{"unsupported scalar", "messages.1.reasoning_opaque", `17`},
		{"malformed carrier", "messages.1.reasoning_opaque", `"cpa-copilot-reasoning:v1:invalid"`},
		{"unsupported item", "messages.1.reasoning_opaque", `[{"type":"message","content":[]}]`},
		{"missing encrypted object", "messages.1.reasoning_opaque", `{"type":"reasoning"}`},
		{"wrong encrypted type", "messages.1.reasoning_opaque", `[{"type":"reasoning","encrypted_content":17}]`},
		{"empty encrypted value", "messages.1.reasoning_opaque", `[{"type":"reasoning","encrypted_content":""}]`},
		{"wrong item ID", "messages.1.reasoning_opaque", `[{"type":"reasoning","id":17,"encrypted_content":"sig"}]`},
		{"unsigned thinking", "messages.1.reasoning_opaque", `[{"type":"thinking","thinking":"summary"}]`},
		{"wrong thinking text type", "messages.1.reasoning_opaque", `[{"type":"thinking","thinking":17,"signature":"sig"}]`},
		{"wrong summary type", "messages.1.reasoning_opaque", `[{"type":"reasoning","summary":17,"encrypted_content":"sig"}]`},
		{"wrong summary text type", "messages.1.reasoning_opaque", `[{"type":"reasoning","summary":[{"type":"summary_text","text":17}],"encrypted_content":"sig"}]`},
		{"invalid redacted data", "messages.1.reasoning_opaque", `[{"type":"redacted_thinking","data":17}]`},
		{"non-assistant carrier", "messages.1.role", `"user"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request, err := sjson.SetRawBytes(followup, test.path, []byte(test.raw))
			if err != nil {
				t.Fatal(err)
			}
			if out, err := RequestForEndpointFrom("openai", model, request, false, EndpointResponses); err == nil {
				t.Fatalf("accepted invalid opaque replay: %s", out)
			}
		})
	}
	if _, err := RequestForEndpointFrom("openai", "different-model", followup, false, EndpointResponses); err == nil {
		t.Fatal("accepted carrier for another model")
	}
	for _, test := range []struct{ field, value string }{{"provider", "foreign"}, {"endpoint", "responses"}} {
		t.Run(test.field, func(t *testing.T) {
			t.Parallel()
			carrier := gjson.GetBytes(assistant, "reasoning_opaque").String()
			encoded, err := base64.RawURLEncoding.DecodeString(carrier[len(copilotOpaquePrefix):])
			if err != nil {
				t.Fatal(err)
			}
			encoded, err = sjson.SetBytes(encoded, test.field, test.value)
			if err != nil {
				t.Fatal(err)
			}
			request, err := sjson.SetBytes(followup, "messages.1.reasoning_opaque", copilotOpaquePrefix+base64.RawURLEncoding.EncodeToString(encoded))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := RequestForEndpointFrom("openai", model, request, false, EndpointResponses); err == nil {
				t.Fatal("accepted foreign carrier scope")
			}
		})
	}
}

func TestChatResponsesOpaqueReplayRejectsContextCollision(t *testing.T) {
	t.Parallel()
	const model = "gpt-6-luna"
	prefix := []byte(`{"messages":[{"role":"user","content":"same question"}]}`)
	reasoning := []byte(`{"type":"reasoning","summary":[],"encrypted_content":"signature-context"}`)
	assistant := responsesReplayAssistant(t, model, prefix, reasoning, []byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"same answer"}]}`))
	followup := appendChatMessages(t, prefix, assistant, []byte(`{"role":"user","content":"same question"}`), assistant)
	if out, err := RequestForEndpointFrom("openai", model, followup, false, EndpointResponses); err == nil {
		t.Fatalf("accepted identical content and context collision: %s", out)
	}
}

func TestChatResponsesOpaqueReplayRejectsInvalidToolIdentity(t *testing.T) {
	t.Parallel()
	const model = "gpt-6-luna"
	prefix := []byte(`{"messages":[{"role":"user","content":"inspect"}]}`)
	reasoning := []byte(`{"type":"reasoning","summary":[],"encrypted_content":"signature-tool"}`)
	call := []byte(`{"id":"fc_tool/+exact","type":"function_call","call_id":"call_tool/+exact","name":"inspect","arguments":"{}"}`)
	assistant := responsesReplayAssistant(t, model, prefix, reasoning, call)
	toolID := gjson.GetBytes(assistant, "tool_calls.0.id").String()
	followup := appendChatMessages(t, prefix, assistant, []byte(`{"role":"tool","tool_call_id":"`+toolID+`","content":"found"}`))
	for _, test := range []struct {
		name, path string
		value      any
	}{
		{"malformed call carrier", "messages.1.tool_calls.0.id", "cpa_tool_v1_invalid"},
		{"item-only call carrier", "messages.1.tool_calls.0.id", encodeClaudeToolID("fc_tool/+exact", "")},
		{"missing call identity", "messages.1.tool_calls.0.id", ""},
		{"numeric call identity", "messages.1.tool_calls.0.id", 17},
		{"changed call identity", "messages.1.tool_calls.0.id", encodeClaudeToolID("fc_tool/+exact", "different-call")},
		{"malformed output carrier", "messages.2.tool_call_id", "cpa_tool_v1_invalid"},
		{"item-only output carrier", "messages.2.tool_call_id", encodeClaudeToolID("fc_tool/+exact", "")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request, err := sjson.SetBytes(followup, test.path, test.value)
			if err != nil {
				t.Fatal(err)
			}
			if out, err := RequestForEndpointFrom("openai", model, request, false, EndpointResponses); err == nil {
				t.Fatalf("accepted invalid tool identity: %s", out)
			}
		})
	}
}

func TestChatResponsesOpaqueReplayRestoresResponsesShapedField(t *testing.T) {
	t.Parallel()
	const model = "gpt-6-luna"
	prefix := []byte(`{"messages":[{"role":"user","content":"question"}]}`)
	reasoning := []byte(`{"id":"rs_raw","type":"reasoning","summary":[],"encrypted_content":"signature-raw"}`)
	assistant := responsesReplayAssistant(t, model, prefix, reasoning)
	value, err := decodeCopilotOpaque(gjson.GetBytes(assistant, "reasoning_opaque").String(), model)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{value.Raw, reasoning} {
		t.Run(string(raw), func(t *testing.T) {
			t.Parallel()
			updated, err := sjson.SetRawBytes(assistant, "reasoning_opaque", raw)
			if err != nil {
				t.Fatal(err)
			}
			request := appendChatMessages(t, prefix, updated, []byte(`{"role":"user","content":"continue"}`))
			out, err := RequestForEndpointFrom("openai", model, request, false, EndpointResponses)
			if err != nil {
				t.Fatalf("replay Responses-shaped opaque field: %v", err)
			}
			if gjson.GetBytes(out, "input.#").Int() != 3 || gjson.GetBytes(out, "input.1").Raw != string(reasoning) {
				t.Fatalf("Responses-shaped field lost its assistant position: %s", out)
			}
		})
	}
}

func responsesReplayAssistant(t *testing.T, model string, original []byte, output ...[]byte) []byte {
	t.Helper()
	translated, err := RequestForEndpointFrom("openai", model, original, false, EndpointResponses)
	if err != nil {
		t.Fatalf("translate initial Chat request: %v", err)
	}
	response := []byte(`{"id":"resp_fixture","object":"response","status":"completed","model":"` + model + `","output":` + string(rawJSONArray(output)) + `,"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	chatResponse, err := ResponseFromEndpoint(context.Background(), EndpointResponses, "openai", model, original, translated, response)
	if err != nil {
		t.Fatalf("produce Chat assistant from Responses output: %v", err)
	}
	assistant := gjson.GetBytes(chatResponse, "choices.0.message")
	if _, err := decodeCopilotOpaque(assistant.Get("reasoning_opaque").String(), model); err != nil {
		t.Fatalf("producer omitted accepted opaque carrier: %v; response=%s", err, chatResponse)
	}
	return []byte(assistant.Raw)
}
