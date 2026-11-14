package translate

import (
	"context"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"testing"
)

func TestCarrierAttackConflictingVisibleContent(t *testing.T) {
	const model = "gemini-3.8-flash"
	original := []byte(`{"model":"gemini-3.8-flash","input":[{"role":"user","content":"hi"}]}`)
	req, err := RequestForEndpointFrom("openai-response", model, original, false, EndpointChatCompletions)
	if err != nil {
		t.Fatal(err)
	}
	upstream := []byte(`{"id":"chat_a","object":"chat.completion","model":"gemini-3.8-flash","choices":[{"index":0,"message":{"role":"assistant","content":"answer","reasoning_text":"real thought","reasoning_opaque":"seal"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`)
	resp, err := ResponseFromEndpoint(context.Background(), EndpointChatCompletions, "openai-response", model, original, req, upstream)
	if err != nil {
		t.Fatal(err)
	}
	item := gjson.GetBytes(resp, "output.#(type==reasoning)")
	if !item.Exists() {
		t.Fatalf("no reason: %s", resp)
	}
	reasoning, err := sjson.SetRawBytes([]byte(item.Raw), "content", []byte(`[{"type":"reasoning_text","text":"altered thought"}]`))
	if err != nil {
		t.Fatal(err)
	}
	responseMsg := gjson.GetBytes(resp, "output.#(type==message)")
	follow := []byte(`{"model":"gemini-3.8-flash","input":[{"role":"user","content":"hi"}]}`)
	follow, err = sjson.SetRawBytes(follow, "input.-1", reasoning)
	if err != nil {
		t.Fatal(err)
	}
	follow, err = sjson.SetRawBytes(follow, "input.-1", []byte(responseMsg.Raw))
	if err != nil {
		t.Fatal(err)
	}
	follow, err = sjson.SetRawBytes(follow, "input.-1", []byte(`{"role":"user","content":"continue"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := RequestForEndpointFrom("openai-response", model, follow, false, EndpointChatCompletions)
	if err == nil {
		t.Fatalf("conflicting content accepted: %s", got)
	}
}
func TestCarrierAttackDistinctBigIntegerToolArguments(t *testing.T) {
	const model = "gemini-3.8-flash"
	original := []byte(`{"model":"gemini-3.8-flash","input":[{"role":"user","content":"hi"}]}`)
	req, err := RequestForEndpointFrom("openai-response", model, original, false, EndpointChatCompletions)
	if err != nil {
		t.Fatal(err)
	}
	upstream := []byte(`{"id":"chat_a","object":"chat.completion","model":"gemini-3.8-flash","choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_text":"real thought","reasoning_opaque":"seal","tool_calls":[{"id":"call_big","type":"function","function":{"name":"inspect","arguments":"{\"n\":9007199254740993}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`)
	resp, err := ResponseFromEndpoint(context.Background(), EndpointChatCompletions, "openai-response", model, original, req, upstream)
	if err != nil {
		t.Fatal(err)
	}
	item := gjson.GetBytes(resp, "output.#(type==reasoning)")
	call := gjson.GetBytes(resp, "output.#(type==function_call)")
	if !item.Exists() || !call.Exists() {
		t.Fatalf("no reason/call: %s", resp)
	}
	altered, err := sjson.SetBytes([]byte(call.Raw), "arguments", `{"n":9007199254740992}`)
	if err != nil {
		t.Fatal(err)
	}
	follow := []byte(`{"model":"gemini-3.8-flash","input":[{"role":"user","content":"hi"}]}`)
	follow, err = sjson.SetRawBytes(follow, "input.-1", []byte(item.Raw))
	if err != nil {
		t.Fatal(err)
	}
	follow, err = sjson.SetRawBytes(follow, "input.-1", altered)
	if err != nil {
		t.Fatal(err)
	}
	follow, err = sjson.SetRawBytes(follow, "input.-1", []byte(`{"type":"function_call_output","call_id":"call_big","output":"result"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := RequestForEndpointFrom("openai-response", model, follow, false, EndpointChatCompletions)
	if err == nil {
		t.Fatalf("changed tool arguments accepted: %s", got)
	}
}
func TestCarrierAttackThinkingOnlyTurnDuplicatesAssistant(t *testing.T) {
	const model = "gemini-3.8-flash"
	original := []byte(`{"model":"gemini-3.8-flash","input":[{"role":"user","content":"hi"}]}`)
	req, err := RequestForEndpointFrom("openai-response", model, original, false, EndpointChatCompletions)
	if err != nil {
		t.Fatal(err)
	}
	upstream := []byte(`{"id":"chat_a","object":"chat.completion","model":"gemini-3.8-flash","choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_text":"thought"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`)
	resp, err := ResponseFromEndpoint(context.Background(), EndpointChatCompletions, "openai-response", model, original, req, upstream)
	if err != nil {
		t.Fatalf("thinking-only upstream was rejected: %v", err)
	}
	item := gjson.GetBytes(resp, "output.#(type==reasoning)")
	if !item.Exists() {
		t.Fatalf("no reason: %s", resp)
	}
	follow := []byte(`{"model":"gemini-3.8-flash","input":[{"role":"user","content":"hi"}]}`)
	follow, err = sjson.SetRawBytes(follow, "input.-1", []byte(item.Raw))
	if err != nil {
		t.Fatal(err)
	}
	follow, err = sjson.SetRawBytes(follow, "input.-1", []byte(`{"role":"user","content":"continue"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := RequestForEndpointFrom("openai-response", model, follow, false, EndpointChatCompletions)
	if err != nil {
		t.Fatalf("valid replay rejected: %v", err)
	}
	count := 0
	for _, m := range gjson.GetBytes(got, "messages").Array() {
		if m.Get("role").String() == "assistant" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("assistant count=%d; replay=%s", count, got)
	}
}
func TestCarrierAttackDistinctProviderReasoningFields(t *testing.T) {
	const model = "gemini-3.8-flash"
	original := []byte(`{"model":"gemini-3.8-flash","input":[{"role":"user","content":"hi"}]}`)
	req, err := RequestForEndpointFrom("openai-response", model, original, false, EndpointChatCompletions)
	if err != nil {
		t.Fatal(err)
	}
	upstream := []byte(`{"id":"chat_a","object":"chat.completion","model":"gemini-3.8-flash","choices":[{"index":0,"message":{"role":"assistant","content":"answer","reasoning_text":"native thought","reasoning_content":"other thought","reasoning_opaque":"seal"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`)
	resp, err := ResponseFromEndpoint(context.Background(), EndpointChatCompletions, "openai-response", model, original, req, upstream)
	if err != nil {
		return
	}
	if !gjson.GetBytes(resp, `output.#(type==reasoning).summary.#(text=="other thought")`).Exists() {
		t.Fatalf("provider reasoning_content silently lost: %s", resp)
	}
}
func TestCarrierAttackMovedThinkingAcrossTurns(t *testing.T) {
	const model = "gemini-3.8-flash"
	original := []byte(`{"model":"gemini-3.8-flash","input":[{"role":"user","content":"hi"}]}`)
	req, err := RequestForEndpointFrom("openai-response", model, original, false, EndpointChatCompletions)
	if err != nil {
		t.Fatal(err)
	}
	upstream := []byte(`{"id":"chat_a","object":"chat.completion","model":"gemini-3.8-flash","choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_text":"thought","reasoning_opaque":"seal"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`)
	resp, err := ResponseFromEndpoint(context.Background(), EndpointChatCompletions, "openai-response", model, original, req, upstream)
	if err != nil {
		t.Fatal(err)
	}
	item := gjson.GetBytes(resp, "output.#(type==reasoning)")
	if !item.Exists() {
		t.Fatalf("no reason: %s", resp)
	}
	follow := []byte(`{"model":"gemini-3.8-flash","input":[{"role":"user","content":"hi"},{"role":"user","content":"different turn"}]}`)
	follow, err = sjson.SetRawBytes(follow, "input.-1", []byte(item.Raw))
	if err != nil {
		t.Fatal(err)
	}
	follow, err = sjson.SetRawBytes(follow, "input.-1", []byte(`{"role":"user","content":"continue"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := RequestForEndpointFrom("openai-response", model, follow, false, EndpointChatCompletions)
	if err == nil {
		t.Fatalf("moved reasoning across user turns accepted: %s", got)
	}
	t.Logf("rejection: %v", err)
}

func TestCarrierEquivalentNumberFormats(t *testing.T) {
	const model = "gemini-3.8-flash"
	original := []byte(`{"model":"gemini-3.8-flash","input":[{"role":"user","content":"hi"}]}`)
	req, err := RequestForEndpointFrom("openai-response", model, original, false, EndpointChatCompletions)
	if err != nil {
		t.Fatal(err)
	}
	upstream := []byte(`{"id":"chat_a","object":"chat.completion","model":"gemini-3.8-flash","choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_text":"real thought","reasoning_opaque":"seal","tool_calls":[{"id":"call_big","type":"function","function":{"name":"inspect","arguments":"{\"n\":1.0}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`)
	resp, err := ResponseFromEndpoint(context.Background(), EndpointChatCompletions, "openai-response", model, original, req, upstream)
	if err != nil {
		t.Fatal(err)
	}
	item := gjson.GetBytes(resp, "output.#(type==reasoning)")
	call := gjson.GetBytes(resp, "output.#(type==function_call)")
	if !item.Exists() || !call.Exists() {
		t.Fatalf("no reason/call: %s", resp)
	}
	altered, err := sjson.SetBytes([]byte(call.Raw), "arguments", `{"n":1}`)
	if err != nil {
		t.Fatal(err)
	}
	follow := []byte(`{"model":"gemini-3.8-flash","input":[{"role":"user","content":"hi"}]}`)
	follow, err = sjson.SetRawBytes(follow, "input.-1", []byte(item.Raw))
	if err != nil {
		t.Fatal(err)
	}
	follow, err = sjson.SetRawBytes(follow, "input.-1", altered)
	if err != nil {
		t.Fatal(err)
	}
	follow, err = sjson.SetRawBytes(follow, "input.-1", []byte(`{"type":"function_call_output","call_id":"call_big","output":"result"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := RequestForEndpointFrom("openai-response", model, follow, false, EndpointChatCompletions)
	if err != nil {
		t.Fatalf("equivalent tool arguments rejected: %v, %s", err, got)
	}
}
func TestCarrierAttackRepeatedOriginalRestoreThinkingOnly(t *testing.T) {
	const model = "gemini-3.8-flash"
	original := []byte(`{"model":"gemini-3.8-flash","input":[{"role":"user","content":"hi"}]}`)
	req, err := RequestForEndpointFrom("openai-response", model, original, false, EndpointChatCompletions)
	if err != nil {
		t.Fatal(err)
	}
	upstream := []byte(`{"id":"chat_a","object":"chat.completion","model":"gemini-3.8-flash","choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_text":"thought"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`)
	resp, err := ResponseFromEndpoint(context.Background(), EndpointChatCompletions, "openai-response", model, original, req, upstream)
	if err != nil {
		t.Fatal(err)
	}
	item := gjson.GetBytes(resp, "output.#(type==reasoning)")
	if !item.Exists() {
		t.Fatalf("no reason: %s", resp)
	}
	follow := []byte(`{"model":"gemini-3.8-flash","input":[{"role":"user","content":"hi"}]}`)
	follow, err = sjson.SetRawBytes(follow, "input.-1", []byte(item.Raw))
	if err != nil {
		t.Fatal(err)
	}
	follow, err = sjson.SetRawBytes(follow, "input.-1", []byte(`{"role":"user","content":"continue"}`))
	if err != nil {
		t.Fatal(err)
	}
	first, err := RequestForEndpointFrom("openai-response", model, follow, false, EndpointChatCompletions)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RestoreCopilotReasoningFromOriginal("openai-response", model, follow, first, false)
	if err != nil {
		t.Fatalf("second restore failed: %v", err)
	}
	n := 0
	for _, m := range gjson.GetBytes(second, "messages").Array() {
		if m.Get("role").String() == "assistant" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("restored assistant count=%d; output=%s", n, second)
	}
}
func TestCarrierAttackChangedExistingNativeThinking(t *testing.T) {
	const model = "gemini-3.8-flash"
	original := []byte(`{"model":"gemini-3.8-flash","input":[{"role":"user","content":"hi"}]}`)
	req, err := RequestForEndpointFrom("openai-response", model, original, false, EndpointChatCompletions)
	if err != nil {
		t.Fatal(err)
	}
	upstream := []byte(`{"id":"chat_a","object":"chat.completion","model":"gemini-3.8-flash","choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_text":"thought"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`)
	resp, err := ResponseFromEndpoint(context.Background(), EndpointChatCompletions, "openai-response", model, original, req, upstream)
	if err != nil {
		t.Fatal(err)
	}
	item := gjson.GetBytes(resp, "output.#(type==reasoning)")
	if !item.Exists() {
		t.Fatalf("no reason: %s", resp)
	}
	follow := []byte(`{"model":"gemini-3.8-flash","input":[{"role":"user","content":"hi"}]}`)
	follow, err = sjson.SetRawBytes(follow, "input.-1", []byte(item.Raw))
	if err != nil {
		t.Fatal(err)
	}
	follow, err = sjson.SetRawBytes(follow, "input.-1", []byte(`{"role":"user","content":"continue"}`))
	if err != nil {
		t.Fatal(err)
	}
	first, err := RequestForEndpointFrom("openai-response", model, follow, false, EndpointChatCompletions)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := sjson.SetBytes(first, "messages.1.reasoning_text", "changed")
	if err != nil {
		t.Fatal(err)
	}
	got, err := RestoreCopilotReasoningFromOriginal("openai-response", model, follow, changed, false)
	if err == nil {
		t.Fatalf("conflicting preexisting native reasoning accepted: %s", got)
	}
}
func TestCarrierAttackChangedHostSummary(t *testing.T) {
	const model = "gemini-3.8-flash"
	initial := []byte(`{"model":"gemini-3.8-flash","input":[{"role":"user","content":"hi"}]}`)
	first, err := RequestForEndpointFrom("openai-response", model, initial, false, EndpointChatCompletions)
	if err != nil {
		t.Fatal(err)
	}
	upstream := []byte(`{"id":"chat_a","object":"chat.completion","model":"gemini-3.8-flash","choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_text":"thought"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`)
	resp, err := ResponseFromEndpoint(context.Background(), EndpointChatCompletions, "openai-response", model, initial, first, upstream)
	if err != nil {
		t.Fatal(err)
	}
	item := gjson.GetBytes(resp, "output.#(type==reasoning)")
	if !item.Exists() {
		t.Fatalf("no reason: %s", resp)
	}
	original := []byte(`{"model":"gemini-3.8-flash","input":[{"role":"user","content":"hi"}]}`)
	original, err = sjson.SetRawBytes(original, "input.-1", []byte(item.Raw))
	if err != nil {
		t.Fatal(err)
	}
	original, err = sjson.SetRawBytes(original, "input.-1", []byte(`{"role":"user","content":"continue"}`))
	if err != nil {
		t.Fatal(err)
	}
	host := []byte(`{"model":"gemini-3.8-flash","input":[{"role":"user","content":"hi"},{"type":"reasoning","summary":[{"type":"summary_text","text":"changed"}]},{"role":"user","content":"continue"}]}`)
	translated, err := RequestForEndpointFrom("openai-response", model, host, false, EndpointChatCompletions)
	if err != nil {
		t.Fatal(err)
	}
	got, err := RestoreCopilotReasoningFromOriginal("openai-response", model, original, translated, false)
	if err == nil {
		t.Fatalf("conflicting host summary accepted: %s", got)
	}
}
func TestCarrierAttackConflictingValidCarriersSameUser(t *testing.T) {
	const model = "gemini-3.8-flash"
	initial := []byte(`{"model":"gemini-3.8-flash","input":[{"role":"user","content":"hi"}]}`)
	req, err := RequestForEndpointFrom("openai-response", model, initial, false, EndpointChatCompletions)
	if err != nil {
		t.Fatal(err)
	}
	itemFor := func(text, opaque string) []byte {
		upstream := []byte(`{"id":"chat_a","object":"chat.completion","model":"gemini-3.8-flash","choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_text":"","reasoning_opaque":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`)
		upstream, _ = sjson.SetBytes(upstream, "choices.0.message.reasoning_text", text)
		upstream, _ = sjson.SetBytes(upstream, "choices.0.message.reasoning_opaque", opaque)
		resp, e := ResponseFromEndpoint(context.Background(), EndpointChatCompletions, "openai-response", model, initial, req, upstream)
		if e != nil {
			t.Fatal(e)
		}
		return []byte(gjson.GetBytes(resp, "output.#(type==reasoning)").Raw)
	}
	history := func(item []byte) []byte {
		b := append([]byte(nil), initial...)
		b, err = sjson.SetRawBytes(b, "input.-1", item)
		if err != nil {
			t.Fatal(err)
		}
		b, err = sjson.SetRawBytes(b, "input.-1", []byte(`{"role":"user","content":"continue"}`))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	original := history(itemFor("thought one", "opaque-one"))
	payload := history(itemFor("thought two", "opaque-two"))
	translated, err := RequestForEndpointFrom("openai-response", model, payload, false, EndpointChatCompletions)
	if err != nil {
		t.Fatal(err)
	}
	got, err := RestoreCopilotReasoningFromOriginal("openai-response", model, original, translated, false)
	if err == nil {
		t.Fatalf("conflicting valid carrier overwritten: %s", got)
	}
}

func TestCopilotThinkingOnlyAfterImageUser(t *testing.T) {
	const model = "gemini-3.8-flash"
	original := []byte(`{"model":"gemini-3.8-flash","input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"https://example.test/image.png"}]}]}`)
	req, err := RequestForEndpointFrom("openai-response", model, original, false, EndpointChatCompletions)
	if err != nil {
		t.Fatal(err)
	}
	upstream := []byte(`{"id":"chat_a","object":"chat.completion","model":"gemini-3.8-flash","choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_text":"thought"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`)
	resp, err := ResponseFromEndpoint(context.Background(), EndpointChatCompletions, "openai-response", model, original, req, upstream)
	if err != nil {
		t.Fatal(err)
	}
	item := gjson.GetBytes(resp, "output.#(type==reasoning)")
	if !item.Exists() {
		t.Fatalf("missing reasoning: %s", resp)
	}
	follow, err := sjson.SetRawBytes(original, "input.-1", []byte(item.Raw))
	if err != nil {
		t.Fatal(err)
	}
	follow, err = sjson.SetRawBytes(follow, "input.-1", []byte(`{"role":"user","content":"continue"}`))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := RequestForEndpointFrom("openai-response", model, follow, false, EndpointChatCompletions)
	if err != nil {
		t.Fatalf("image continuation: %v", err)
	}
	if gjson.GetBytes(replay, `messages.#(role==assistant).reasoning_text`).String() != "thought" {
		t.Fatalf("missing thinking: %s", replay)
	}
}
func TestCarrierAttackConflictingParallelToolCarriers(t *testing.T) {
	const model = "gemini-3.8-flash"
	initial := []byte(`{"model":"gemini-3.8-flash","input":[{"role":"user","content":"hi"}]}`)
	req, err := RequestForEndpointFrom("openai-response", model, initial, false, EndpointChatCompletions)
	if err != nil {
		t.Fatal(err)
	}
	itemFor := func(text, opaque string) ([]byte, [][]byte) {
		upstream := []byte(`{"id":"chat_a","object":"chat.completion","model":"gemini-3.8-flash","choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_text":"","reasoning_opaque":"","tool_calls":[{"id":"call_a","type":"function","function":{"name":"first","arguments":"{\"x\":1}"}},{"id":"call_b","type":"function","function":{"name":"second","arguments":"{\"x\":2}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`)
		upstream, _ = sjson.SetBytes(upstream, "choices.0.message.reasoning_text", text)
		upstream, _ = sjson.SetBytes(upstream, "choices.0.message.reasoning_opaque", opaque)
		resp, e := ResponseFromEndpoint(context.Background(), EndpointChatCompletions, "openai-response", model, initial, req, upstream)
		if e != nil {
			t.Fatal(e)
		}
		reason := []byte(gjson.GetBytes(resp, "output.#(type==reasoning)").Raw)
		var calls [][]byte
		for _, v := range gjson.GetBytes(resp, "output").Array() {
			if v.Get("type").String() == "function_call" {
				calls = append(calls, []byte(v.Raw))
			}
		}
		if len(reason) == 0 || len(calls) != 2 {
			t.Fatalf("missing items: %s", resp)
		}
		return reason, calls
	}
	history := func(reason []byte, calls [][]byte) []byte {
		b := append([]byte(nil), initial...)
		b, err = sjson.SetRawBytes(b, "input.-1", reason)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range calls {
			b, err = sjson.SetRawBytes(b, "input.-1", c)
			if err != nil {
				t.Fatal(err)
			}
		}
		for _, id := range []string{"call_a", "call_b"} {
			b, err = sjson.SetRawBytes(b, "input.-1", []byte(`{"type":"function_call_output","call_id":"`+id+`","output":"result"}`))
			if err != nil {
				t.Fatal(err)
			}
		}
		return b
	}
	reason1, calls1 := itemFor("thought one", "opaque-one")
	reason2, calls2 := itemFor("thought two", "opaque-two")
	original := history(reason1, calls1)
	payload := history(reason2, calls2)
	translated, err := RequestForEndpointFrom("openai-response", model, payload, false, EndpointChatCompletions)
	if err != nil {
		t.Fatal(err)
	}
	got, err := RestoreCopilotReasoningFromOriginal("openai-response", model, original, translated, false)
	if err == nil {
		t.Fatalf("parallel tool carrier conflict overwritten: %s", got)
	}
	t.Logf("rejection: %v", err)
}
func TestCarrierAttackUnsignedHostSummaryWithMatchingParallelTools(t *testing.T) {
	const model = "gemini-3.8-flash"
	initial := []byte(`{"model":"gemini-3.8-flash","input":[{"role":"user","content":"hi"}]}`)
	req, err := RequestForEndpointFrom("openai-response", model, initial, false, EndpointChatCompletions)
	if err != nil {
		t.Fatal(err)
	}
	upstream := []byte(`{"id":"chat_a","object":"chat.completion","model":"gemini-3.8-flash","choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_text":"thought one","reasoning_opaque":"opaque-one","tool_calls":[{"id":"call_a","type":"function","function":{"name":"first","arguments":"{\"x\":1}"}},{"id":"call_b","type":"function","function":{"name":"second","arguments":"{\"x\":2}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`)
	resp, err := ResponseFromEndpoint(context.Background(), EndpointChatCompletions, "openai-response", model, initial, req, upstream)
	if err != nil {
		t.Fatal(err)
	}
	original := append([]byte(nil), initial...)
	payload := append([]byte(nil), initial...)
	for _, item := range gjson.GetBytes(resp, "output").Array() {
		if item.Get("type").String() == "reasoning" {
			original, err = sjson.SetRawBytes(original, "input.-1", []byte(item.Raw))
			if err != nil {
				t.Fatal(err)
			}
			payload, err = sjson.SetRawBytes(payload, "input.-1", []byte(`{"type":"reasoning","summary":[{"type":"summary_text","text":"changed"}]}`))
			if err != nil {
				t.Fatal(err)
			}
		} else if item.Get("type").String() == "function_call" {
			original, err = sjson.SetRawBytes(original, "input.-1", []byte(item.Raw))
			if err != nil {
				t.Fatal(err)
			}
			payload, err = sjson.SetRawBytes(payload, "input.-1", []byte(item.Raw))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, id := range []string{"call_a", "call_b"} {
		output := []byte(`{"type":"function_call_output","call_id":"` + id + `","output":"result"}`)
		original, err = sjson.SetRawBytes(original, "input.-1", output)
		if err != nil {
			t.Fatal(err)
		}
		payload, err = sjson.SetRawBytes(payload, "input.-1", output)
		if err != nil {
			t.Fatal(err)
		}
	}
	translated, err := RequestForEndpointFrom("openai-response", model, payload, false, EndpointChatCompletions)
	if err != nil {
		t.Fatal(err)
	}
	got, err := RestoreCopilotReasoningFromOriginal("openai-response", model, original, translated, false)
	if err == nil {
		t.Fatalf("unsigned host summary conflict overwritten: %s", got)
	}
}

func TestDuplicateToolArgumentsRequireExactReplay(t *testing.T) {
	const model = "gemini-3.8-flash"
	original := []byte(`{"model":"gemini-3.8-flash","input":[{"role":"user","content":"hi"}]}`)
	req, err := RequestForEndpointFrom("openai-response", model, original, false, EndpointChatCompletions)
	if err != nil {
		t.Fatal(err)
	}
	upstream := []byte(`{"id":"chat_a","object":"chat.completion","model":"gemini-3.8-flash","choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_text":"thought","tool_calls":[{"id":"call_a","type":"function","function":{"name":"inspect","arguments":"{\"x\":1,\"x\":2}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`)
	response, err := ResponseFromEndpoint(context.Background(), EndpointChatCompletions, "openai-response", model, original, req, upstream)
	if err != nil {
		t.Fatal(err)
	}
	reason := gjson.GetBytes(response, "output.#(type==reasoning)")
	call := gjson.GetBytes(response, "output.#(type==function_call)")
	if !reason.Exists() || !call.Exists() {
		t.Fatalf("missing items: %s", response)
	}
	for _, tc := range []struct {
		name, args string
		accept     bool
	}{
		{"identical", `{"x":1,"x":2}`, true},
		{"collapsed", `{"x":2}`, false},
		{"whitespace", `{"x":1, "x":2}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed, err := sjson.SetBytes([]byte(call.Raw), "arguments", tc.args)
			if err != nil {
				t.Fatal(err)
			}
			follow, err := sjson.SetRawBytes(original, "input.-1", []byte(reason.Raw))
			if err != nil {
				t.Fatal(err)
			}
			follow, err = sjson.SetRawBytes(follow, "input.-1", changed)
			if err != nil {
				t.Fatal(err)
			}
			follow, err = sjson.SetRawBytes(follow, "input.-1", []byte(`{"type":"function_call_output","call_id":"call_a","output":"done"}`))
			if err != nil {
				t.Fatal(err)
			}
			_, err = RequestForEndpointFrom("openai-response", model, follow, false, EndpointChatCompletions)
			if (err == nil) != tc.accept {
				t.Fatalf("accept=%v, err=%v", tc.accept, err)
			}
		})
	}
}
func TestCopilotReasoningRejectsLossyUpstreamChat(t *testing.T) {
	const model = "gemini-3.8-flash"
	initial := []byte(`{"model":"gemini-3.8-flash","input":[{"role":"user","content":"hello"}]}`)
	req, err := RequestForEndpointFrom("openai-response", model, initial, false, EndpointChatCompletions)
	if err != nil {
		t.Fatal(err)
	}
	for _, upstream := range []string{
		`{"id":"chat_a","object":"chat.completion","model":"gemini-3.8-flash","choices":[{"index":0,"message":{"role":"assistant","content":"\ud800","reasoning_text":"secret"},"finish_reason":"stop"}]}`,
		`{"id":"chat_a","object":"chat.completion","model":"gemini-3.8-flash","choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_text":"secret","tool_calls":[{"id":"\ud800","type":"function","function":{"name":"foo","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
		`{"id":"chat_a","object":"chat.completion","model":"gemini-3.8-flash","choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_text":"secret","tool_calls":[{"id":"call","type":"function","function":{"name":"\ud800","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
	} {
		response, err := ResponseFromEndpoint(context.Background(), EndpointChatCompletions, "openai-response", model, initial, req, []byte(upstream))
		if err == nil {
			t.Errorf("lossy upstream accepted: %s", response)
		}
	}
}
