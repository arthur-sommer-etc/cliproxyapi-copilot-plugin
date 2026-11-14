package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type nativeToolExclusion struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

func filterUnrepresentableNativeTools(source, endpoint string, body []byte) ([]byte, []nativeToolExclusion, error) {
	if !gjson.ValidBytes(body) {
		return body, nil, nil
	}
	type toolSource struct {
		path    string
		tools   gjson.Result
		kept    []json.RawMessage
		changed bool
	}
	root := gjson.ParseBytes(body)
	sources := []toolSource{{path: "tools", tools: root.Get("tools")}}
	if source == "openai-response" {
		for index, item := range root.Get("input").Array() {
			if item.Get("type").String() == "additional_tools" {
				sources = append(sources, toolSource{path: fmt.Sprintf("input.%d.tools", index), tools: item.Get("tools")})
			}
		}
	}
	kept := make([]json.RawMessage, 0)
	removed := make([]gjson.Result, 0, 2)
	exclusions := make([]nativeToolExclusion, 0, 2)
	for index := range sources {
		declarations := &sources[index]
		if !declarations.tools.IsArray() {
			continue
		}
		declarations.kept = make([]json.RawMessage, 0, len(declarations.tools.Array()))
		for _, tool := range declarations.tools.Array() {
			typ := strings.TrimSpace(tool.Get("type").String())
			unsupported := false
			if source == "openai-response" || source == "claude" {
				if !tool.IsObject() {
					return nil, nil, statusError("invalid_tool_definition", "tool declarations must be objects", http.StatusUnprocessableEntity)
				}
				declaration := make(map[string]any)
				if err := json.Unmarshal([]byte(tool.Raw), &declaration); err != nil {
					return nil, nil, fmt.Errorf("decode tool declaration: %w", err)
				}
				representable, valid := translate.NativeToolRepresentable(source, endpoint, declaration)
				unsupported = valid && !representable
			}
			if unsupported {
				removed = append(removed, tool)
				exclusions = append(exclusions, nativeToolExclusion{Type: typ, Reason: "unrepresentable_by_selected_endpoint"})
				declarations.changed = true
			} else {
				raw := json.RawMessage(tool.Raw)
				declarations.kept = append(declarations.kept, raw)
				kept = append(kept, raw)
			}
		}
	}
	choice := gjson.GetBytes(body, "tool_choice")
	choiceType := choice.String()
	if choice.IsObject() {
		choiceType = choice.Get("type").String()
	}
	for _, tool := range removed {
		if choice.IsObject() && (choiceType == strings.TrimSpace(tool.Get("type").String()) || strings.HasPrefix(choiceType, "web_search") && strings.HasPrefix(tool.Get("type").String(), "web_search")) {
			return nil, nil, nativeToolChoiceError("tool_choice forces an unsupported native " + tool.Get("type").String() + " tool")
		}
		if choice.IsObject() && (choiceType == "tool" || choiceType == "function" || choiceType == "custom") {
			name := choice.Get("name").String()
			if name == "" {
				name = choice.Get("function.name").String()
			}
			toolName := tool.Get("name").String()
			if toolName == "" {
				toolName = tool.Get("type").String()
				if strings.HasPrefix(toolName, "web_search") {
					toolName = "web_search"
				}
			}
			if name == toolName {
				retained := false
				for _, raw := range kept {
					candidate := gjson.ParseBytes(raw)
					if candidate.Get("name").String() == name || candidate.Get("function.name").String() == name {
						retained = true
						break
					}
				}
				if !retained {
					return nil, nil, nativeToolChoiceError("tool_choice forces an unsupported native " + tool.Get("type").String() + " tool")
				}
			}
		}
		if choiceType == "allowed_tools" {
			for _, allowed := range choice.Get("tools").Array() {
				if allowed.Get("type").String() == tool.Get("type").String() {
					return nil, nil, nativeToolChoiceError("tool_choice allowed_tools includes an unsupported native " + tool.Get("type").String() + " tool")
				}
			}
		}
	}
	if len(kept) == 0 && (choiceType == "required" || choiceType == "any") {
		return nil, nil, nativeToolChoiceError("tool_choice requires a tool, but no supported tools are available")
	}
	if len(removed) == 0 {
		return body, nil, nil
	}
	var err error
	for _, declarations := range sources {
		if !declarations.changed {
			continue
		}
		if declarations.path == "tools" && len(declarations.kept) == 0 {
			body, err = sjson.DeleteBytes(body, declarations.path)
		} else {
			body, err = sjson.SetBytes(body, declarations.path, declarations.kept)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("exclude unsupported native tools: %w", err)
		}
	}
	if len(kept) == 0 && choiceType == "auto" {
		body, err = sjson.DeleteBytes(body, "tool_choice")
		if err != nil {
			return nil, nil, fmt.Errorf("exclude unsupported native tools: %w", err)
		}
	}
	return body, exclusions, nil
}

func nativeToolChoiceError(message string) error {
	return statusError("unsupported_native_tool_choice", message, http.StatusUnprocessableEntity)
}

func nativeToolResponseHeaders(headers http.Header, exclusions []nativeToolExclusion) http.Header {
	if len(exclusions) == 0 {
		return headers
	}
	values := make([]string, 0, len(exclusions))
	for _, exclusion := range exclusions {
		values = append(values, exclusion.Type+";reason="+exclusion.Reason)
	}
	headers.Set("X-Copilot-Excluded-Native-Tools", strings.Join(values, ", "))
	return headers
}
