package translate

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestResponsesClaudeNormalizesLegacyWebSearchPreview(t *testing.T) {
	request := []byte(`{"input":"Find the weather forecast.","tools":[{"type":"web_search_preview","user_location":{"type":"approximate","country":"US"}}]}`)
	translated, err := RequestForEndpointFrom("openai-response", "gpt-test", request, false, EndpointMessages)
	if err != nil {
		t.Fatalf("translate legacy web_search_preview to Claude: %v", err)
	}
	if got := gjson.GetBytes(translated, "tools.0.type").String(); got != "web_search_20250305" {
		t.Fatalf("Claude web search type = %q; request=%s", got, translated)
	}
	if got := gjson.GetBytes(translated, "tools.0.user_location.country").String(); got != "US" {
		t.Fatalf("Claude web search user location country = %q; request=%s", got, translated)
	}
}

func TestResponsesClaudePreservesMappedWebSearchOptions(t *testing.T) {
	request := []byte(`{"input":"search","tools":[{"type":"web_search","max_uses":2,"filters":{"allowed_domains":["docs.example.test"]},"user_location":{"type":"approximate","country":"US"}}]}`)
	translated, err := RequestForEndpointFrom("openai-response", "gpt-test", request, false, EndpointMessages)
	if err != nil {
		t.Fatalf("translate supported native web search options: %v", err)
	}
	if got := gjson.GetBytes(translated, "tools.0.type").String(); got != "web_search_20250305" {
		t.Fatalf("Claude web search type = %q; request=%s", got, translated)
	}
	if got := gjson.GetBytes(translated, "tools.0.max_uses").Int(); got != 2 {
		t.Fatalf("Claude web search max_uses = %d; request=%s", got, translated)
	}
	if got := gjson.GetBytes(translated, "tools.0.allowed_domains.0").String(); got != "docs.example.test" {
		t.Fatalf("Claude web search allowed domain = %q; request=%s", got, translated)
	}
	if got := gjson.GetBytes(translated, "tools.0.user_location.country").String(); got != "US" {
		t.Fatalf("Claude web search user location country = %q; request=%s", got, translated)
	}
}

func TestResponsesRequestPreflightRejectsDroppedNativeTools(t *testing.T) {
	for _, test := range []struct {
		name     string
		endpoint string
		body     string
	}{
		{
			name:     "disabled native web search to Claude",
			endpoint: EndpointMessages,
			body:     `{"input":"search","tools":[{"type":"web_search","external_web_access":false}]}`,
		},
		{
			name:     "native web search to Chat Completions",
			endpoint: EndpointChatCompletions,
			body:     `{"input":"search","tools":[{"type":"web_search"}]}`,
		},
		{
			name:     "legacy preview search to Chat Completions",
			endpoint: EndpointChatCompletions,
			body:     `{"input":"search","tools":[{"type":"web_search_preview"}]}`,
		},
		{
			name:     "image generation to Claude",
			endpoint: EndpointMessages,
			body:     `{"input":"draw","tools":[{"type":"image_generation"}]}`,
		},
		{
			name:     "image generation to Chat Completions",
			endpoint: EndpointChatCompletions,
			body:     `{"input":"draw","tools":[{"type":"image_generation"}]}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := RequestForEndpointFrom("openai-response", "gpt-test", []byte(test.body), false, test.endpoint); err == nil {
				t.Fatalf("unsupported native tool was silently dropped for %s", test.endpoint)
			}
		})
	}
}

func TestResponsesClaudePreflightRejectsUnmappedSearchOptionsAndChoices(t *testing.T) {
	for _, body := range []string{
		`{"input":"search","tools":[{"type":"web_search_preview","search_context_size":"medium"}]}`,
		`{"input":"search","tools":[{"type":"web_search"}],"tool_choice":"none"}`,
		`{"input":"search","tools":[{"type":"web_search"}],"tool_choice":{"type":"web_search"}}`,
		`{"input":"search","tools":[{"type":"web_search"}],"tool_choice":{"type":"function","name":"web_search"}}`,
		`{"input":"search","tools":[{"type":"web_search"}],"tool_choice":{"type":"allowed_tools","tools":[{"type":"web_search"}]}}`,
		`{"input":"search","tool_choice":{"type":"allowed_tools","tools":[{"type":"web_search_preview"}]}}`,
	} {
		if _, err := RequestForEndpointFrom("openai-response", "gpt-test", []byte(body), false, EndpointMessages); err == nil {
			t.Fatalf("unrepresentable native web search option or choice was accepted: %s", body)
		}
	}
}

func TestResponsesClaudeRequiredChoiceNeedsTranslatedTools(t *testing.T) {
	for _, body := range []string{
		`{"input":"do something","tool_choice":"required"}`,
		`{"input":"do something","tools":[],"tool_choice":"required"}`,
		`{"input":[{"type":"additional_tools","tools":[]}],"tools":[],"tool_choice":"required"}`,
		`{"input":[{"type":"additional_tools","tools":[]}],"tool_choice":"required"}`,
	} {
		if _, err := RequestForEndpointFrom("openai-response", "gpt-test", []byte(body), false, EndpointMessages); err == nil {
			t.Fatalf("required tool_choice without translated tools was accepted: %s", body)
		} else if !strings.Contains(err.Error(), "tool_choice required") || !strings.Contains(err.Error(), "translated tools") {
			t.Fatalf("required tool_choice error = %v; want a precise missing-translated-tools error", err)
		}
	}
}

func TestResponsesClaudeRequiredChoiceWithTranslatedTools(t *testing.T) {
	for _, test := range []struct {
		name    string
		request string
	}{
		{name: "top-level function", request: `{"input":"inspect","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"tool_choice":"required"}`},
		{name: "top-level custom", request: `{"input":"inspect","tools":[{"type":"custom","name":"lookup","format":{"type":"text"}}],"tool_choice":"required"}`},
		{name: "additional function", request: `{"input":[{"type":"additional_tools","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}],"tool_choice":"required"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			translated, err := RequestForEndpointFrom("openai-response", "gpt-test", []byte(test.request), false, EndpointMessages)
			if err != nil {
				t.Fatalf("translate required choice with a %s: %v", test.name, err)
			}
			if got := gjson.GetBytes(translated, "tool_choice.type").String(); got != "any" {
				t.Fatalf("Claude tool_choice type = %q; request=%s", got, translated)
			}
			if got := gjson.GetBytes(translated, "tools.0.name").String(); got != "lookup" {
				t.Fatalf("translated tool name = %q; request=%s", got, translated)
			}
		})
	}
}

func TestResponsesClaudeRequiredChoiceRejectsDroppedCustomNameTool(t *testing.T) {
	request := []byte(`{"input":"inspect","tools":[{"type":"custom","custom":{"name":"lookup","input_schema":{"type":"object"}}}],"tool_choice":"required"}`)
	if _, err := RequestForEndpointFrom("openai-response", "gpt-test", request, false, EndpointMessages); err == nil {
		t.Fatalf("required choice was accepted when the custom-only name is dropped: %s", request)
	} else if !strings.Contains(err.Error(), "tool_choice required") || !strings.Contains(err.Error(), "translated tools") {
		t.Fatalf("required custom-only tool error = %v; want a precise missing-translated-tools error", err)
	}
}

func TestResponsesCrossFormatRejectsAllowedToolsChoiceForFunctions(t *testing.T) {
	request := []byte(`{"input":"inspect","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"tool_choice":{"type":"allowed_tools","tools":[{"type":"function","name":"lookup"}]}}`)
	for _, endpoint := range []string{EndpointMessages, EndpointChatCompletions} {
		if _, err := RequestForEndpointFrom("openai-response", "gpt-test", request, false, endpoint); err == nil {
			t.Fatalf("unrepresentable allowed_tools choice was accepted by %s", endpoint)
		} else if !strings.Contains(err.Error(), "allowed_tools") || !strings.Contains(err.Error(), "cannot be represented") {
			t.Fatalf("allowed_tools error for %s = %v; want a precise representation error", endpoint, err)
		}
	}
}

func TestResponsesNativeToolsRemainUntouchedAndNamedFunctionsRemainFunctions(t *testing.T) {
	native := []byte(`{"input":"search","tools":[{"type":"web_search_preview","search_context_size":"medium"},{"type":"image_generation","size":"large"}]}`)
	translated, err := RequestForEndpointFrom("openai-response", "gpt-test", native, false, EndpointResponses)
	if err != nil {
		t.Fatalf("preserve native Responses tools: %v", err)
	}
	if got := gjson.GetBytes(translated, "tools").Raw; got != gjson.GetBytes(native, "tools").Raw {
		t.Fatalf("native Responses tools changed from %s to %s", gjson.GetBytes(native, "tools").Raw, got)
	}

	functions := []byte(`{"input":"inspect","tools":[{"type":"function","name":"web_search","parameters":{"type":"object"}},{"type":"function","name":"image_generation","parameters":{"type":"object"}}],"tool_choice":{"type":"function","name":"web_search"}}`)
	for _, test := range []struct {
		endpoint string
		path     string
	}{
		{endpoint: EndpointMessages, path: "tools"},
		{endpoint: EndpointChatCompletions, path: "tools"},
	} {
		converted, err := RequestForEndpointFrom("openai-response", "gpt-test", functions, false, test.endpoint)
		if err != nil {
			t.Fatalf("translate named functions to %s: %v", test.endpoint, err)
		}
		toolNames := make(map[string]bool)
		for _, tool := range gjson.GetBytes(converted, test.path).Array() {
			name := tool.Get("name").String()
			if name == "" {
				name = tool.Get("function.name").String()
			}
			toolNames[name] = true
		}
		if !toolNames["web_search"] || !toolNames["image_generation"] {
			t.Fatalf("ordinary named functions were lost for %s: %s", test.endpoint, converted)
		}
	}
}

func TestResponsesUnsupportedNativeToolErrorsIdentifyTarget(t *testing.T) {
	_, err := RequestForEndpointFrom("openai-response", "gpt-test", []byte(`{"input":"draw","tools":[{"type":"image_generation"}]}`), false, EndpointMessages)
	if err == nil || !strings.Contains(err.Error(), "Claude Messages") {
		t.Fatalf("tool translation error = %v; want an error identifying Claude Messages", err)
	}
}
