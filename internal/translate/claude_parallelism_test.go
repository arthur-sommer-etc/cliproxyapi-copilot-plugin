package translate

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestClaudeToolParallelismPreservesSelectionAndSystem(t *testing.T) {
	t.Parallel()
	for _, endpoint := range []string{EndpointResponses, EndpointChatCompletions} {
		for _, stream := range []bool{false, true} {
			for _, selection := range []string{"auto", "any", "tool", "none"} {
				for _, parallelism := range []struct {
					name    string
					disable any
				}{
					{name: "omitted"},
					{name: "serial", disable: true},
					{name: "parallel", disable: false},
				} {
					t.Run(endpoint+"/"+selection+"/"+parallelism.name+"/stream="+strconv.FormatBool(stream), func(t *testing.T) {
						t.Parallel()
						choice := map[string]any{"type": selection}
						if selection == "tool" {
							choice["name"] = "inspect"
						}
						if parallelism.disable != nil {
							choice["disable_parallel_tool_use"] = parallelism.disable
						}
						body, err := json.Marshal(map[string]any{
							"system": []any{map[string]any{"type": "text", "text": "Root policy.", "cache_control": map[string]any{"type": "ephemeral"}}},
							"messages": []any{
								map[string]any{"role": "user", "content": "Before."},
								map[string]any{"role": "system", "content": "Ordered policy."},
								map[string]any{"role": "user", "content": "After."},
							},
							"tools":       []any{map[string]any{"name": "inspect", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}}}},
							"tool_choice": choice,
						})
						if err != nil {
							t.Fatal(err)
						}
						translated, err := RequestForEndpointFrom("claude", "exact-model", body, stream, endpoint)
						if err != nil {
							t.Fatal(err)
						}
						parallel := gjson.GetBytes(translated, "parallel_tool_calls")
						if parallelism.disable == nil {
							if parallel.Exists() {
								t.Fatalf("omitted control gained a value: %s", translated)
							}
						} else if !parallel.Exists() || parallel.Bool() == parallelism.disable.(bool) {
							t.Fatalf("parallelism changed: %s", translated)
						}
						wantSelection := selection
						if selection == "any" {
							wantSelection = "required"
						}
						toolChoice := gjson.GetBytes(translated, "tool_choice")
						tool := gjson.GetBytes(translated, "tools.0")
						if endpoint == EndpointChatCompletions {
							tool = tool.Get("function")
							if selection == "tool" {
								toolChoice = toolChoice.Get("function")
							}
						}
						if selection == "tool" {
							if toolChoice.Get("name").String() != "inspect" {
								t.Fatalf("forced selection changed: %s", translated)
							}
						} else if toolChoice.String() != wantSelection {
							t.Fatalf("selection changed: %s", translated)
						}
						if tool.Get("name").String() != "inspect" || tool.Get("parameters.properties.query.type").String() != "string" {
							t.Fatalf("tool schema changed: %s", translated)
						}
						messagePath := "input"
						textPath := "content.0.text"
						if endpoint == EndpointChatCompletions {
							messagePath = "messages"
							textPath = "content"
							if gjson.GetBytes(translated, "messages.0.content").IsArray() {
								textPath = "content.0.text"
							}
						}
						messages := gjson.GetBytes(translated, messagePath).Array()
						if len(messages) != 4 || messages[0].Get("role").String() != "system" || messages[2].Get("role").String() != "system" {
							t.Fatalf("ordered system authority changed: %s", translated)
						}
						if messages[0].Get(textPath).String() != "Root policy." || messages[2].Get(textPath).String() != "Ordered policy." {
							t.Fatalf("system text changed: %s", translated)
						}
						if gjson.GetBytes(translated, "stream").Bool() != stream || gjson.GetBytes(translated, "model").String() != "exact-model" {
							t.Fatalf("model or transport changed: %s", translated)
						}
					})
				}
			}
		}
	}
}

func TestClaudeToolParallelismRejectsMalformedControl(t *testing.T) {
	t.Parallel()
	for _, value := range []string{`null`, `"PRIVATE-CONTROL"`, `0`, `{}`, `[]`} {
		for _, endpoint := range []string{EndpointResponses, EndpointChatCompletions} {
			t.Run(endpoint+"/"+value, func(t *testing.T) {
				t.Parallel()
				body := []byte(`{"messages":[{"role":"user","content":"keep"}],"tool_choice":{"type":"auto","disable_parallel_tool_use":` + value + `}}`)
				_, err := RequestForEndpointFrom("claude", "exact-model", body, false, endpoint)
				if err == nil || !strings.Contains(err.Error(), "disable_parallel_tool_use must be a boolean") || strings.Contains(err.Error(), "PRIVATE-CONTROL") {
					t.Fatalf("malformed control error = %v", err)
				}
			})
		}
	}
}
