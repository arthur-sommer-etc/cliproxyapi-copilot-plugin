package translate

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestClaudeStopSequencesTranslateToChatWithoutChangingOtherRequestFields(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run("stream="+map[bool]string{false: "false", true: "true"}[stream], func(t *testing.T) {
			body := []byte(`{
				"system":"Root policy.",
				"messages":[
					{"role":"user","content":"Before."},
					{"role":"system","content":"Ordered policy."},
					{"role":"assistant","content":[{"type":"text","text":"I will check."},{"type":"tool_use","id":"toolu_lookup_1","name":"lookup","input":{"query":"prior"}}]},
					{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_lookup_1","content":"value found"},{"type":"text","text":"After."}]}
				],
				"tools":[{"name":"lookup","description":"Find a value.","input_schema":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}],
				"tool_choice":{"type":"auto","disable_parallel_tool_use":true},
				"stop_sequences":["<END>","STOP"]
			}`)
			translated, err := RequestForEndpointFrom("claude", "exact-model", body, stream, EndpointChatCompletions)
			if err != nil {
				t.Fatalf("translate Claude request to Chat: %v", err)
			}
			stops := gjson.GetBytes(translated, "stop").Array()
			if len(stops) != 2 || stops[0].String() != "<END>" || stops[1].String() != "STOP" {
				t.Fatalf("Chat stop sequences = %s, want exact ordered strings", gjson.GetBytes(translated, "stop").Raw)
			}
			if gjson.GetBytes(translated, "stop_sequences").Exists() {
				t.Fatalf("Claude stop_sequences leaked into Chat request: %s", translated)
			}
			parallel := gjson.GetBytes(translated, "parallel_tool_calls")
			if !parallel.Exists() || parallel.Bool() {
				t.Fatalf("disabled parallel tool use was lost: %s", translated)
			}
			tool := gjson.GetBytes(translated, "tools.0.function")
			if tool.Get("name").String() != "lookup" || tool.Get("parameters.properties.query.type").String() != "string" || tool.Get("parameters.required.0").String() != "query" {
				t.Fatalf("function schema changed: %s", translated)
			}
			messages := gjson.GetBytes(translated, "messages").Array()
			if len(messages) != 6 || messages[0].Get("role").String() != "system" || messages[1].Get("role").String() != "user" || messages[2].Get("role").String() != "system" || messages[3].Get("role").String() != "assistant" || messages[4].Get("role").String() != "tool" || messages[5].Get("role").String() != "user" {
				t.Fatalf("system and user message order changed: %s", translated)
			}
			content := messages[2].Get("content")
			midSystem := content.String()
			if content.IsArray() {
				parts := content.Array()
				if len(parts) > 0 {
					midSystem = parts[0].Get("text").String()
				}
			}
			if messages[0].Get("content").String() != "Root policy." || midSystem != "Ordered policy." {
				t.Fatalf("system continuity changed: root=%q middle=%q request=%s", messages[0].Get("content").String(), midSystem, translated)
			}
			toolCall := messages[3].Get("tool_calls.0")
			callID := toolCall.Get("id").String()
			if toolCall.Get("function.name").String() != "lookup" || toolCall.Get("function.arguments").String() != `{"query":"prior"}` || callID == "" || messages[4].Get("tool_call_id").String() != callID {
				t.Fatalf("function replay changed: %s", translated)
			}
			if gjson.GetBytes(translated, "model").String() != "exact-model" || gjson.GetBytes(translated, "stream").Bool() != stream {
				t.Fatalf("model or stream changed: %s", translated)
			}
		})
	}
}

func TestClaudeStopSequencesRejectUnsupportedResponsesAndMalformedControls(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run("responses/stream="+map[bool]string{false: "false", true: "true"}[stream], func(t *testing.T) {
			_, err := RequestForEndpointFrom("claude", "exact-model", []byte(`{"messages":[{"role":"user","content":"keep"}],"stop_sequences":["<END>"]}`), stream, EndpointResponses)
			if err == nil || err.Error() != "Claude stop_sequences cannot be represented by the Copilot Responses endpoint" {
				t.Fatalf("Responses stop sequence error = %v", err)
			}
		})
	}

	for _, endpoint := range []string{EndpointResponses, EndpointChatCompletions} {
		for _, malformed := range []struct {
			name string
			json string
		}{
			{name: "null", json: `null`},
			{name: "scalar", json: `"<END>"`},
			{name: "non-string member", json: `["<END>",1]`},
		} {
			t.Run(endpoint+"/"+malformed.name, func(t *testing.T) {
				body := []byte(`{"messages":[{"role":"user","content":"keep"}],"stop_sequences":` + malformed.json + `}`)
				_, err := RequestForEndpointFrom("claude", "exact-model", body, false, endpoint)
				if err == nil || !strings.Contains(err.Error(), "Claude stop_sequences") {
					t.Fatalf("malformed stop_sequences error = %v", err)
				}
				if strings.Contains(err.Error(), "<END>") {
					t.Fatalf("malformed control value leaked in error: %v", err)
				}
			})
		}
	}
}

func TestClaudeStopSequencesEmptyArrayAndNativeMessages(t *testing.T) {
	for _, endpoint := range []string{EndpointResponses, EndpointChatCompletions} {
		translated, err := RequestForEndpointFrom("claude", "exact-model", []byte(`{"messages":[{"role":"user","content":"keep"}],"stop_sequences":[]}`), false, endpoint)
		if err != nil {
			t.Fatalf("empty stop array to %s: %v", endpoint, err)
		}
		if gjson.GetBytes(translated, "stop_sequences").Exists() || (endpoint == EndpointChatCompletions && gjson.GetBytes(translated, "stop").Exists()) {
			t.Fatalf("empty stop array was emitted by %s translation: %s", endpoint, translated)
		}
	}

	nativeBody := []byte(`{"model":"claude-model","messages":[{"role":"user","content":"keep"}],"stop_sequences":["<END>","STOP"]}`)
	native, err := RequestForEndpointFrom("claude", "claude-model", nativeBody, false, EndpointMessages)
	if err != nil {
		t.Fatalf("native Messages passthrough: %v", err)
	}
	if got := gjson.GetBytes(native, "stop_sequences").Raw; got != `["<END>","STOP"]` {
		t.Fatalf("native stop sequences = %s, want unchanged ordered array", got)
	}
}
