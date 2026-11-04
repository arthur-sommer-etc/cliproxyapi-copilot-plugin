package translate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"strings"
	"testing"
)

func TestCopilotChatThinkingRoundTripsResponsesAndClaude(t *testing.T) {
	const model = "gemini-3.8-flash"
	const thinking = "  first thought\nsecond thought  "
	for _, source := range []string{"openai-response", "claude"} {
		t.Run(source, func(t *testing.T) {
			original := []byte(`{"model":"gemini-3.8-flash","input":[{"role":"user","content":"run two tools"}]}`)
			if source == "claude" {
				original = []byte(`{"model":"gemini-3.8-flash","max_tokens":256,"messages":[{"role":"user","content":"run two tools"}]}`)
			}
			request, err := RequestForEndpointFrom(source, model, original, false, EndpointChatCompletions)
			if err != nil {
				t.Fatal(err)
			}
			upstream := []byte(`{"id":"chatcmpl_1","object":"chat.completion","model":"gemini-3.8-flash","choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_opaque":"signed-thought","tool_calls":[{"id":"call_a","type":"function","function":{"name":"first","arguments":"{\"x\":1}"}},{"id":"call_b","type":"function","function":{"name":"second","arguments":"{\"x\":2}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`)
			upstream, err = sjson.SetBytes(upstream, "choices.0.message.reasoning_text", thinking)
			if err != nil {
				t.Fatal(err)
			}
			translated, err := ResponseFromEndpoint(context.Background(), EndpointChatCompletions, source, model, original, request, upstream)
			if err != nil {
				t.Fatal(err)
			}
			var value gjson.Result
			if source == "claude" {
				value = gjson.GetBytes(translated, "content.#(type==thinking).thinking")
			} else {
				value = gjson.GetBytes(translated, "output.#(type==reasoning).summary.0.text")
			}
			if value.Type != gjson.String || value.String() != thinking {
				t.Fatalf("thinking lost: got %q response=%s", value.String(), translated)
			}
			var followup []byte
			if source == "openai-response" {
				inputs := []json.RawMessage{json.RawMessage(`{"role":"user","content":"run two tools"}`)}
				for _, item := range gjson.GetBytes(translated, "output").Array() {
					inputs = append(inputs, json.RawMessage(item.Raw))
				}
				inputs = append(inputs, json.RawMessage(`{"type":"function_call_output","call_id":"call_a","output":"a"}`), json.RawMessage(`{"type":"function_call_output","call_id":"call_b","output":"b"}`))
				encoded, err := json.Marshal(inputs)
				if err != nil {
					t.Fatal(err)
				}
				followup, err = sjson.SetRawBytes(original, "input", encoded)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				followup = []byte(`{"model":"gemini-3.8-flash","max_tokens":256,"messages":[{"role":"user","content":"run two tools"},{"role":"assistant","content":[]},{"role":"user","content":[]}]}`)
				followup, err = sjson.SetRawBytes(followup, "messages.1.content", []byte(gjson.GetBytes(translated, "content").Raw))
				if err != nil {
					t.Fatal(err)
				}
				var results []map[string]string
				for _, block := range gjson.GetBytes(translated, "content").Array() {
					if block.Get("type").String() == "tool_use" {
						results = append(results, map[string]string{"type": "tool_result", "tool_use_id": block.Get("id").String(), "content": "done"})
					}
				}
				followup, err = sjson.SetBytes(followup, "messages.2.content", results)
				if err != nil {
					t.Fatal(err)
				}
			}
			replay, err := RequestForEndpointFrom(source, model, followup, false, EndpointChatCompletions)
			if err != nil {
				t.Fatalf("replay: %v", err)
			}
			assistant := replayedChatAssistantWithTool(t, replay, "call_a")
			if assistant.Get("reasoning_text").String() != thinking || assistant.Get("reasoning_opaque").String() != "signed-thought" || assistant.Get("reasoning_content").Exists() || len(assistant.Get("tool_calls").Array()) != 2 {
				t.Fatalf("replayed reasoning or parallel tools changed: %s", replay)
			}
			var tampered []byte
			if source == "claude" {
				for i, block := range gjson.GetBytes(followup, "messages.1.content").Array() {
					if block.Get("type").String() == "thinking" {
						tampered, err = sjson.SetBytes(followup, fmt.Sprintf("messages.1.content.%d.thinking", i), "changed")
					}
				}
			} else {
				for i, item := range gjson.GetBytes(followup, "input").Array() {
					if item.Get("type").String() == "reasoning" {
						tampered, err = sjson.SetBytes(followup, fmt.Sprintf("input.%d.summary.0.text", i), "changed")
					}
				}
			}
			if err != nil || len(tampered) == 0 {
				t.Fatalf("tamper fixture: %v", err)
			}
			if _, err := RequestForEndpointFrom(source, model, tampered, false, EndpointChatCompletions); err == nil {
				t.Fatal("changed thinking accepted")
			}
			if source == "openai-response" {
				for i, item := range gjson.GetBytes(followup, "input").Array() {
					if item.Get("type").String() != "reasoning" {
						continue
					}
					conflict, err := sjson.SetRawBytes(followup, fmt.Sprintf("input.%d.content", i), []byte(`[{"type":"reasoning_text","text":"altered thought"}]`))
					if err != nil {
						t.Fatal(err)
					}
					if _, err := RequestForEndpointFrom(source, model, conflict, false, EndpointChatCompletions); err == nil {
						t.Fatal("conflicting thinking content accepted")
					}
					missing, err := sjson.DeleteBytes(followup, fmt.Sprintf("input.%d.summary", i))
					if err != nil {
						t.Fatal(err)
					}
					if _, err := RequestForEndpointFrom(source, model, missing, false, EndpointChatCompletions); err == nil {
						t.Fatal("missing thinking summary accepted")
					}
				}
			}
		})
	}
}

func TestCopilotChatThinkingSSEKeepsLateOpaqueState(t *testing.T) {
	const model = "gemini-3.8-flash"
	for _, source := range []string{"openai-response", "claude"} {
		t.Run(source, func(t *testing.T) {
			original := []byte(`{"model":"gemini-3.8-flash","input":[{"role":"user","content":"stream tools"}]}`)
			if source == "claude" {
				original = []byte(`{"model":"gemini-3.8-flash","max_tokens":256,"messages":[{"role":"user","content":"stream tools"}]}`)
			}
			request, err := RequestForEndpointFrom(source, model, original, true, EndpointChatCompletions)
			if err != nil {
				t.Fatal(err)
			}
			frames := [][]byte{
				[]byte("data: {\"id\":\"chat_stream\",\"object\":\"chat.completion.chunk\",\"created\":7,\"model\":\"gemini-3.8-flash\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning_text\":\"first \"},\"finish_reason\":null}]}\n\n"),
				[]byte("data: {\"id\":\"chat_stream\",\"object\":\"chat.completion.chunk\",\"created\":7,\"model\":\"gemini-3.8-flash\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_text\":\"second\",\"tool_calls\":[{\"index\":0,\"id\":\"call_a\",\"type\":\"function\",\"function\":{\"name\":\"first\",\"arguments\":\"{\\\"x\\\":1}\"}},{\"index\":1,\"id\":\"call_b\",\"type\":\"function\",\"function\":{\"name\":\"second\",\"arguments\":\"{\\\"x\\\":2}\"}}]},\"finish_reason\":null}]}\n\n"),
				[]byte("data: {\"id\":\"chat_stream\",\"object\":\"chat.completion.chunk\",\"created\":7,\"model\":\"gemini-3.8-flash\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"),
				[]byte("data: {\"id\":\"chat_stream\",\"object\":\"chat.completion.chunk\",\"created\":7,\"model\":\"gemini-3.8-flash\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_opaque\":\"late-state\"},\"finish_reason\":null}]}\n\n"),
				[]byte("data: [DONE]\n\n"),
			}
			var state any
			var output [][]byte
			for _, frame := range frames {
				parts, err := StreamFromEndpoint(context.Background(), EndpointChatCompletions, source, model, original, request, frame, &state)
				if err != nil {
					t.Fatalf("translate stream: %v", err)
				}
				output = append(output, parts...)
			}
			var thinking, carrier string
			for _, frame := range output {
				_, data, _, err := parseSSEFrame(frame)
				if err != nil {
					t.Fatal(err)
				}
				if source == "openai-response" && gjson.GetBytes(data, "type").String() == "response.output_item.done" && gjson.GetBytes(data, "item.type").String() == "reasoning" {
					thinking = gjson.GetBytes(data, "item.summary.0.text").String()
					carrier = gjson.GetBytes(data, "item.encrypted_content").String()
				}
				if source == "claude" && gjson.GetBytes(data, "type").String() == "content_block_delta" {
					switch gjson.GetBytes(data, "delta.type").String() {
					case "thinking_delta":
						thinking += gjson.GetBytes(data, "delta.thinking").String()
					case "signature_delta":
						carrier += gjson.GetBytes(data, "delta.signature").String()
					}
				}
			}
			if thinking != "first second" {
				t.Fatalf("stream thinking = %q; frames=%q", thinking, output)
			}
			value, err := decodeCopilotOpaque(carrier, model)
			if err != nil || string(value.Raw) != `"late-state"` || value.Thinking == nil || *value.Thinking != thinking || len(value.Anchor.ToolCallIDs) != 2 {
				t.Fatalf("stream carrier lost state: %#v error=%v; frames=%q", value, err, output)
			}
		})
	}
}

func TestLegacyCopilotCarrierRejectsToolArguments(t *testing.T) {
	const model = "gemini-3.8-flash"
	for _, test := range []struct {
		name, arguments, replay string
		allowed                 bool
	}{
		{name: "nonnumeric", arguments: `{"name":"file"}`, replay: `{"name":"file"}`},
		{name: "numeric unchanged", arguments: `{"n":9007199254740993}`, replay: `{"n":9007199254740993}`},
		{name: "numeric rounded", arguments: `{"n":9007199254740993}`, replay: `{"n":9007199254740992}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := []byte(`{"input":[{"role":"user","content":"hi"}]}`)
			request, err := RequestForEndpointFrom("openai-response", model, original, false, EndpointChatCompletions)
			if err != nil {
				t.Fatal(err)
			}
			upstream := []byte(`{"id":"chat_legacy","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_opaque":"legacy-state","tool_calls":[{"id":"call_legacy","type":"function","function":{"name":"inspect","arguments":""}}]},"finish_reason":"tool_calls"}]}`)
			upstream, err = sjson.SetBytes(upstream, "choices.0.message.tool_calls.0.function.arguments", test.arguments)
			if err != nil {
				t.Fatal(err)
			}
			response, err := ResponseFromEndpoint(context.Background(), EndpointChatCompletions, "openai-response", model, original, request, upstream)
			if err != nil {
				t.Fatal(err)
			}
			reasoning := []byte(gjson.GetBytes(response, "output.#(type==reasoning)").Raw)
			carrier := gjson.GetBytes(reasoning, "encrypted_content").String()
			envelope, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(carrier, copilotOpaquePrefix))
			if err != nil {
				t.Fatal(err)
			}
			envelope, err = sjson.SetBytes(envelope, "v", 1)
			if err != nil {
				t.Fatal(err)
			}
			if !test.allowed {
				envelope, err = sjson.SetBytes(envelope, "anchor.tool_calls.0.arguments_sha256", canonicalJSONDigest(`{"n":9007199254740992}`))
				if err != nil {
					t.Fatal(err)
				}
			}
			carrier = copilotOpaquePrefix + base64.RawURLEncoding.EncodeToString(envelope)
			reasoning, err = sjson.SetBytes(reasoning, "encrypted_content", carrier)
			if err != nil {
				t.Fatal(err)
			}
			call := []byte(gjson.GetBytes(response, "output.#(type==function_call)").Raw)
			call, err = sjson.SetBytes(call, "arguments", test.replay)
			if err != nil {
				t.Fatal(err)
			}
			followup := []byte(`{"input":[{"role":"user","content":"hi"}]}`)
			followup, err = sjson.SetRawBytes(followup, "input.-1", reasoning)
			if err != nil {
				t.Fatal(err)
			}
			followup, err = sjson.SetRawBytes(followup, "input.-1", call)
			if err != nil {
				t.Fatal(err)
			}
			followup, err = sjson.SetRawBytes(followup, "input.-1", []byte(`{"type":"function_call_output","call_id":"call_legacy","output":"done"}`))
			if err != nil {
				t.Fatal(err)
			}
			_, err = RequestForEndpointFrom("openai-response", model, followup, false, EndpointChatCompletions)
			if (err == nil) != test.allowed {
				t.Fatalf("allowed=%v error=%v", test.allowed, err)
			}
		})
	}
}

func TestLegacyCopilotCarrierWithoutToolsRequiresFreshTurn(t *testing.T) {
	const model = "gemini-3.8-flash"
	carrier, err := encodeCopilotOpaque([]byte(`"opaque"`), model, copilotOpaqueAnchor{PriorUserSHA256: digest([]byte("legacy user"))})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(carrier, copilotOpaquePrefix))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err = sjson.SetBytes(envelope, "v", 1)
	if err != nil {
		t.Fatal(err)
	}
	carrier = copilotOpaquePrefix + base64.RawURLEncoding.EncodeToString(envelope)
	if _, err = decodeCopilotOpaque(carrier, model); err == nil {
		t.Fatal("legacy carrier without tools was accepted")
	}
}

func TestCopilotReasoningStreamRejectsLossyUpstreamChat(t *testing.T) {
	const model = "gemini-3.8-flash"
	for _, source := range []string{"openai-response", "claude"} {
		for _, delta := range []string{
			`{"role":"assistant","content":"\ud800"}`,
			`{"role":"assistant","tool_calls":[{"index":0,"id":"\ud800","type":"function","function":{"name":"foo","arguments":"{}"}}]}`,
			`{"role":"assistant","tool_calls":[{"index":0,"id":"call","type":"function","function":{"name":"\ud800","arguments":"{}"}}]}`,
		} {
			t.Run(source+"/"+delta, func(t *testing.T) {
				initial := []byte(`{"model":"gemini-3.8-flash","input":[{"role":"user","content":"hi"}]}`)
				if source == "claude" {
					initial = []byte(`{"model":"gemini-3.8-flash","max_tokens":128,"messages":[{"role":"user","content":"hi"}]}`)
				}
				request, err := RequestForEndpointFrom(source, model, initial, true, EndpointChatCompletions)
				if err != nil {
					t.Fatal(err)
				}
				var state any
				first := []byte("data: {\"id\":\"chat_a\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_text\":\"secret\"},\"finish_reason\":null}]}\n\n")
				if _, err = StreamFromEndpoint(context.Background(), EndpointChatCompletions, source, model, initial, request, first, &state); err != nil {
					t.Fatal(err)
				}
				frame := []byte(`data: {"id":"chat_a","object":"chat.completion.chunk","choices":[{"index":0,"delta":` + delta + `,"finish_reason":null}]}` + "\n\n")
				if _, err = StreamFromEndpoint(context.Background(), EndpointChatCompletions, source, model, initial, request, frame, &state); err == nil {
					t.Fatal("malformed Unicode accepted")
				}
			})
		}
	}
}
