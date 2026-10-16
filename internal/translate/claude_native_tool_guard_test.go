package translate

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestClaudeNativeToolRequestGuards(t *testing.T) {
	for _, typ := range []string{"web_fetch_20250910", "code_execution_20250522", "computer_20250124", "bash_20250124", "text_editor_20250728", "web_search_20250305"} {
		for _, endpoint := range []string{EndpointResponses, EndpointChatCompletions, EndpointMessages} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", typ, endpoint, stream), func(t *testing.T) {
					body := []byte(fmt.Sprintf(`{"model":"native","messages":[{"role":"user","content":"Inspect"}],"tools":[{"type":%q,"name":"native_tool","max_uses":2}],"tool_choice":{"type":"tool","name":"native_tool"}}`, typ))
					translated, err := RequestForEndpointFrom("claude", "native", body, stream, endpoint)
					if endpoint == EndpointMessages {
						if err != nil || gjson.GetBytes(translated, "tools").Raw != gjson.GetBytes(body, "tools").Raw || gjson.GetBytes(translated, "tool_choice").Raw != gjson.GetBytes(body, "tool_choice").Raw {
							t.Fatalf("native declaration changed: %s %v", translated, err)
						}
					} else if err == nil || !strings.Contains(err.Error(), typ) {
						t.Fatalf("native declaration became a client function: %s %v", translated, err)
					}
				})
			}
		}
	}
}

func TestClaudeServerToolHistoryRequestGuard(t *testing.T) {
	for _, name := range []string{"web_fetch", "code_execution", "web_search"} {
		for _, endpoint := range []string{EndpointResponses, EndpointChatCompletions, EndpointMessages} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", name, endpoint, stream), func(t *testing.T) {
					body := []byte(fmt.Sprintf(`{"messages":[{"role":"assistant","content":[{"type":"server_tool_use","id":"srvtoolu_guard","name":%q,"input":{"query":"canary"}}]},{"role":"user","content":"Continue"}]}`, name))
					translated, err := RequestForEndpointFrom("claude", "native", body, stream, endpoint)
					if endpoint == EndpointMessages {
						if err != nil || gjson.GetBytes(translated, "messages").Raw != gjson.GetBytes(body, "messages").Raw {
							t.Fatalf("native server history changed: %s %v", translated, err)
						}
					} else if err == nil || !strings.Contains(err.Error(), "server_tool_use") {
						t.Fatalf("server history became client execution: %s %v", translated, err)
					}
				})
			}
		}
	}
}

func TestClaudeNativeLookingClientToolNamesStayClientTools(t *testing.T) {
	for _, typ := range []string{"", "custom", "function", " function "} {
		for _, endpoint := range []string{EndpointResponses, EndpointChatCompletions} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", typ, endpoint, stream), func(t *testing.T) {
					body := []byte(fmt.Sprintf(`{"tools":[{"type":%q,"name":"code_execution","input_schema":{"type":"object"}}],"tool_choice":{"type":"tool","name":"code_execution"},"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_guard","name":"code_execution","input":{"code":"canary"}}]}]}`, typ))
					translated, err := RequestForEndpointFrom("claude", "native", body, stream, endpoint)
					namePath, choicePath := "tools.0.name", "tool_choice.name"
					if endpoint == EndpointChatCompletions {
						namePath, choicePath = "tools.0.function.name", "tool_choice.function.name"
					}
					if err != nil || gjson.GetBytes(translated, namePath).String() != "code_execution" || gjson.GetBytes(translated, choicePath).String() != "code_execution" {
						t.Fatalf("ordinary client tool collision changed: %s %v", translated, err)
					}
				})
			}
		}
	}
}

func TestClaudeUnknownAndMalformedToolTypeGuards(t *testing.T) {
	for _, typ := range []string{`"new_native_20990101"`, `" web_fetch_20250910 "`, `null`, `false`, `7`, `{}`, `[]`} {
		for _, endpoint := range []string{EndpointResponses, EndpointChatCompletions, EndpointMessages} {
			body := []byte(fmt.Sprintf(`{"tools":[{"type":%s,"name":"lookup","input_schema":{"type":"object"}}]}`, typ))
			translated, err := RequestForEndpointFrom("claude", "native", body, false, endpoint)
			if endpoint == EndpointMessages {
				if err != nil || gjson.GetBytes(translated, "tools").Raw != gjson.GetBytes(body, "tools").Raw {
					t.Fatalf("native declaration validation was changed: %s %v", translated, err)
				}
			} else if err == nil {
				t.Fatalf("unknown or malformed type %s became an ordinary function toward %s: %s", typ, endpoint, translated)
			}
		}
	}
}
