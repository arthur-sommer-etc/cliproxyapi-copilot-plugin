package provider

import (
	"fmt"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// geminiChatFunctionCallingMode mirrors the function calling mode the
// official Copilot clients apply to gemini-3 chat requests that declare
// tools.
const geminiChatFunctionCallingMode = "validated"

// applyGeminiChatFunctionCallingMode selects the gemini-3 function calling
// mode for automatic selection in a tool-bearing chat request. Explicit
// prohibition, mandatory selection and pinned functions remain unchanged.
func applyGeminiChatFunctionCallingMode(model upstreamModel, endpoint string, body []byte) ([]byte, error) {
	if endpoint != translate.EndpointChatCompletions || !isGemini3FamilyModel(model) {
		return body, nil
	}
	root := gjson.ParseBytes(body)
	if len(root.Get("tools").Array()) == 0 {
		return body, nil
	}
	choice := root.Get("tool_choice")
	if choice.Exists() && choice.String() != "auto" {
		return body, nil
	}
	updated, err := sjson.SetBytes(body, "tool_choice", geminiChatFunctionCallingMode)
	if err != nil {
		return nil, fmt.Errorf("apply gemini function calling mode: %w", err)
	}
	return updated, nil
}
