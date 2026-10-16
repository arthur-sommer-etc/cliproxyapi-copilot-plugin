package translate

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestResponsesNativePreflightRejectsSDKDroppedTools(t *testing.T) {
	for _, declaration := range []string{
		`{"type":"file_search","vector_store_ids":["vs_test"],"max_num_results":5}`,
		`{"type":"code_interpreter","container":{"type":"auto"}}`,
		`{"type":"computer_use_preview","display_width":1024,"display_height":768,"environment":"browser"}`,
		`{"type":"mcp","server_label":"docs","server_url":"https://example.test/mcp"}`,
		`{"type":"tool_search"}`,
		`{"type":"apply_patch"}`,
	} {
		typ := gjson.Get(declaration, "type").String()
		for _, endpoint := range []string{EndpointChatCompletions, EndpointMessages} {
			for _, nested := range []bool{false, true} {
				for _, stream := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/nested=%t/stream=%t", typ, endpoint, nested, stream), func(t *testing.T) {
						tools := `[` + declaration + `,{"type":"function","name":"lookup","parameters":{"type":"object"}}]`
						body := `{"input":"inspect","tools":` + tools + `}`
						if nested {
							body = `{"input":[{"type":"message","role":"user","content":"inspect"},{"type":"additional_tools","tools":` + tools + `}]}`
						}
						if translated, err := RequestForEndpointFrom("openai-response", "gpt-test", []byte(body), stream, endpoint); err == nil || !strings.Contains(err.Error(), "native "+typ) || !strings.Contains(err.Error(), "cannot be represented") {
							t.Fatalf("native tool silently omitted or imprecise error: output=%s error=%v", translated, err)
						}
						translated, err := RequestForEndpointFrom("openai-response", "gpt-test", []byte(body), stream, EndpointResponses)
						if err != nil {
							t.Fatal(err)
						}
						path := "tools"
						if nested {
							path = "input.1.tools"
						}
						if gjson.GetBytes(translated, path).Raw != tools {
							t.Fatalf("native Responses declarations changed: %s", translated)
						}
					})
				}
			}
		}
	}
}

func TestNativeToolRepresentabilityMatchesTranslationPreflight(t *testing.T) {
	for _, test := range []struct {
		name          string
		source        string
		endpoint      string
		declaration   map[string]any
		wantSupported bool
		wantValid     bool
	}{
		{name: "Responses namespace maps to Messages", source: "openai-response", endpoint: EndpointMessages, declaration: map[string]any{"type": "namespace", "name": "functions"}, wantSupported: true, wantValid: true},
		{name: "Responses web search maps to Messages", source: "openai-response", endpoint: EndpointMessages, declaration: map[string]any{"type": "web_search", "external_web_access": true}, wantSupported: true, wantValid: true},
		{name: "cached search is excluded", source: "openai-response", endpoint: EndpointMessages, declaration: map[string]any{"type": "web_search", "external_web_access": false}, wantSupported: false, wantValid: true},
		{name: "malformed search option reaches preflight", source: "openai-response", endpoint: EndpointMessages, declaration: map[string]any{"type": "web_search", "external_web_access": "false"}, wantSupported: true, wantValid: true},
		{name: "local shell maps to Chat", source: "openai-response", endpoint: EndpointChatCompletions, declaration: map[string]any{"type": "shell", "environment": map[string]any{"type": "local"}}, wantSupported: true, wantValid: true},
		{name: "container shell is unsupported by Chat", source: "openai-response", endpoint: EndpointChatCompletions, declaration: map[string]any{"type": "shell", "environment": map[string]any{"type": "container_auto"}}, wantSupported: false, wantValid: true},
		{name: "malformed shell environment reaches validation", source: "openai-response", endpoint: EndpointChatCompletions, declaration: map[string]any{"type": "shell", "environment": "local"}, wantSupported: false, wantValid: false},
		{name: "Claude native tools stay on Messages", source: "claude", endpoint: EndpointMessages, declaration: map[string]any{"type": "web_fetch_20250910"}, wantSupported: true, wantValid: true},
		{name: "Claude server tools do not become Responses functions", source: "claude", endpoint: EndpointResponses, declaration: map[string]any{"type": "web_fetch_20250910"}, wantSupported: false, wantValid: true},
		{name: "malformed declaration type is retained for validation", source: "claude", endpoint: EndpointResponses, declaration: map[string]any{"type": false}, wantSupported: false, wantValid: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			supported, valid := NativeToolRepresentable(test.source, test.endpoint, test.declaration)
			if supported != test.wantSupported || valid != test.wantValid {
				t.Fatalf("representability=(%t,%t), want (%t,%t)", supported, valid, test.wantSupported, test.wantValid)
			}
		})
	}
}

func TestResponsesNativePreflightRejectsUnsupportedChoicesWithoutDeclarations(t *testing.T) {
	for _, typ := range []string{"file_search", "code_interpreter", "computer_use_preview", "mcp", "tool_search", "apply_patch", "shell"} {
		for _, endpoint := range []string{EndpointChatCompletions, EndpointMessages} {
			body := []byte(`{"input":"inspect","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"tool_choice":{"type":"` + typ + `"}}`)
			if translated, err := RequestForEndpointFrom("openai-response", "gpt-test", body, false, endpoint); err == nil || !strings.Contains(err.Error(), "native tool_choice") || !strings.Contains(err.Error(), typ) {
				t.Fatalf("unsupported native choice preserved incorrectly: output=%s error=%v", translated, err)
			}
		}
	}
}

func TestResponsesNativePreflightRejectsUnrepresentableShell(t *testing.T) {
	for _, endpoint := range []string{EndpointChatCompletions, EndpointMessages} {
		for _, environment := range []string{`{"type":"local"}`, `{"type":"container_auto"}`, `{"type":"container_reference","container_id":"cntr_test"}`} {
			for _, nested := range []bool{false, true} {
				tools := `[{"type":"shell","environment":` + environment + `}]`
				body := `{"input":"inspect","tools":` + tools + `,"tool_choice":{"type":"shell"}}`
				if nested {
					body = `{"input":[{"type":"message","role":"user","content":"inspect"},{"type":"additional_tools","tools":` + tools + `}],"tool_choice":{"type":"shell"}}`
				}
				translated, err := RequestForEndpointFrom("openai-response", "gpt-test", []byte(body), false, endpoint)
				if endpoint == EndpointChatCompletions && gjson.Get(environment, "type").String() == "local" {
					if err != nil || gjson.GetBytes(translated, "tools.0.function.name").String() != "__cpa_local_shell" {
						t.Fatalf("SDK local shell mapping changed: output=%s error=%v", translated, err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "native shell") || !strings.Contains(err.Error(), "cannot be represented") {
					t.Fatalf("unrepresentable shell silently dropped: output=%s error=%v", translated, err)
				}
			}
		}
	}
}

func TestResponsesNativeNamesPreserveOrdinaryTools(t *testing.T) {
	for _, typ := range []string{"file_search", "code_interpreter", "computer_use_preview", "mcp", "tool_search", "apply_patch"} {
		for _, endpoint := range []string{EndpointChatCompletions, EndpointMessages} {
			for _, nested := range []bool{false, true} {
				for _, kind := range []string{"function", "custom"} {
					t.Run(fmt.Sprintf("%s/%s/nested=%t/%s", typ, endpoint, nested, kind), func(t *testing.T) {
						options := `"parameters":{"type":"object"}`
						if kind == "custom" {
							options = `"format":{"type":"text"}`
						}
						tools := `[{"type":"` + kind + `","name":"` + typ + `","description":"ordinary tool",` + options + `}]`
						body := `{"input":"inspect","tools":` + tools + `,"tool_choice":"required"}`
						if nested {
							body = `{"input":[{"type":"message","role":"user","content":"inspect"},{"type":"additional_tools","tools":` + tools + `}],"tool_choice":"required"}`
						}
						translated, err := RequestForEndpointFrom("openai-response", "gpt-test", []byte(body), false, endpoint)
						if err != nil {
							t.Fatal(err)
						}
						namePath := "tools.0.name"
						if endpoint == EndpointChatCompletions {
							namePath = "tools.0.function.name"
							if gjson.GetBytes(translated, "tool_choice").String() != "required" {
								t.Fatalf("required choice changed: %s", translated)
							}
						} else if gjson.GetBytes(translated, "tool_choice.type").String() != "any" {
							t.Fatalf("required choice changed: %s", translated)
						}
						if len(gjson.GetBytes(translated, "tools").Array()) != 1 || gjson.GetBytes(translated, namePath).String() != typ {
							t.Fatalf("ordinary tool omitted: %s", translated)
						}
					})
				}
			}
		}
	}
}
