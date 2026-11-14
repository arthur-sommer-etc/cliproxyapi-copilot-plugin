package translate

import (
	"strings"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// NativeToolRepresentable reports whether a tool declaration can be translated
// between the source schema and the selected Copilot endpoint. The second
// result is false when the declaration has a malformed type or shell environment.
// Full validation of supported tool options remains the request preflight's job.
func NativeToolRepresentable(source, endpoint string, declaration map[string]any) (bool, bool) {
	if declaration == nil {
		return false, false
	}
	typ := ""
	if rawType, exists := declaration["type"]; exists {
		value, ok := rawType.(string)
		if !ok {
			return false, false
		}
		typ = strings.TrimSpace(value)
	}
	from := sdktranslator.FromString(source)
	to, err := endpointFormat(endpoint)
	if err != nil || from == "" {
		return false, false
	}
	if from == sdktranslator.FormatClaude {
		if to == sdktranslator.FormatClaude {
			return true, true
		}
		return IsClaudeClientToolType(typ), true
	}
	if from != sdktranslator.FormatOpenAIResponse {
		return false, false
	}
	if to == sdktranslator.FormatOpenAIResponse {
		return true, true
	}
	switch typ {
	case "", "function", "custom", "namespace":
		return true, true
	case "web_search", "web_search_preview":
		if to != sdktranslator.FormatClaude {
			return false, true
		}
		if rawExternalAccess, exists := declaration["external_web_access"]; exists {
			externalAccess, ok := rawExternalAccess.(bool)
			if !ok {
				return true, true
			}
			if !externalAccess {
				return false, true
			}
		}
		return true, true
	case "shell":
		environment, exists := declaration["environment"]
		if exists {
			fields, ok := environment.(map[string]any)
			if !ok {
				return false, false
			}
			if rawEnvironmentType, exists := fields["type"]; exists {
				if _, ok := rawEnvironmentType.(string); !ok {
					return false, false
				}
			}
		}
		return to == sdktranslator.FormatOpenAI && stringValue(objectValue(environment)["type"]) == "local", true
	default:
		return false, true
	}
}
