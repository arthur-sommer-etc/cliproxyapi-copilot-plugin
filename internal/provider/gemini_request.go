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
// mode for a tool-bearing chat request unless the caller pinned a specific
// function. Captured original-client gemini-3.8-flash traffic dispatches with
// this mode.
func applyGeminiChatFunctionCallingMode(model upstreamModel, endpoint string, body []byte) ([]byte, error) {
	if endpoint != translate.EndpointChatCompletions || !isGemini3FamilyModel(model) {
		return body, nil
	}
	root := gjson.ParseBytes(body)
	if len(root.Get("tools").Array()) == 0 {
		return body, nil
	}
	if root.Get("tool_choice").IsObject() {
		return body, nil
	}
	updated, err := sjson.SetBytes(body, "tool_choice", geminiChatFunctionCallingMode)
	if err != nil {
		return nil, fmt.Errorf("apply gemini function calling mode: %w", err)
	}
	return updated, nil
}
