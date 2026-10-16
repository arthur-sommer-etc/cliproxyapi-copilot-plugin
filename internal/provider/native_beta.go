package provider

import (
	"net/http"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/tidwall/gjson"
)

// copilotInferenceHeaders builds the inference headers for one translated
// outbound body. The initiator and the vision flag derive from that body, so
// the initial dispatch and the bearer-refresh retry send identical derived
// headers. Direct OAuth keeps its minimal header policy.
func copilotInferenceHeaders(token copilotTokenEntry, endpoint string, body []byte, stream, visionSupported bool, nativeBeta []string) http.Header {
	headers := copilotRequestHeaders(token, stream)
	if effectiveAuthMode(token.Mode) != authModeDirectOAuth {
		headers.Set("X-Initiator", copilotInferenceInitiator(endpoint, body))
		if visionSupported && copilotRequestContainsImage(endpoint, body) {
			headers.Set("Copilot-Vision-Request", "true")
		}
	}
	for _, value := range nativeBeta {
		headers.Add("OpenAI-Beta", value)
	}
	return headers
}

// copilotInferenceInitiator derives the turn origin from the final turn of
// the translated body: a trailing human turn stays user-initiated and every
// other trailing turn is an agent continuation.
func copilotInferenceInitiator(endpoint string, body []byte) string {
	root := gjson.ParseBytes(body)
	if endpoint == translate.EndpointResponses {
		items := root.Get("input").Array()
		if len(items) == 0 {
			return "user"
		}
		last := items[len(items)-1]
		if last.Get("type").String() == "message" && last.Get("role").String() == "user" {
			return "user"
		}
		return "agent"
	}
	messages := root.Get("messages").Array()
	if len(messages) == 0 {
		return "user"
	}
	last := messages[len(messages)-1]
	if last.Get("role").String() != "user" {
		return "agent"
	}
	if endpoint == translate.EndpointMessages && copilotMessagesCarriesToolResult(last) {
		return "agent"
	}
	return "user"
}

// copilotRequestContainsImage reports typed image content in the translated
// body. Tool arguments and schemas are never interpreted as media.
func copilotRequestContainsImage(endpoint string, body []byte) bool {
	root := gjson.ParseBytes(body)
	switch endpoint {
	case translate.EndpointChatCompletions:
		for _, message := range root.Get("messages").Array() {
			for _, part := range message.Get("content").Array() {
				if part.Get("type").String() == "image_url" {
					return true
				}
			}
		}
	case translate.EndpointResponses:
		for _, item := range root.Get("input").Array() {
			for _, part := range item.Get("content").Array() {
				if part.Get("type").String() == "input_image" {
					return true
				}
			}
			for _, part := range item.Get("output").Array() {
				if part.Get("type").String() == "input_image" {
					return true
				}
			}
		}
	case translate.EndpointMessages:
		for _, message := range root.Get("messages").Array() {
			for _, block := range message.Get("content").Array() {
				if copilotMessagesBlockHasImage(block) {
					return true
				}
			}
		}
	}
	return false
}

func copilotMessagesBlockHasImage(block gjson.Result) bool {
	switch block.Get("type").String() {
	case "image":
		return true
	case "tool_result":
		for _, nested := range block.Get("content").Array() {
			if nested.Get("type").String() == "image" {
				return true
			}
		}
	}
	return false
}

func copilotMessagesCarriesToolResult(message gjson.Result) bool {
	for _, block := range message.Get("content").Array() {
		if block.Get("type").String() == "tool_result" {
			return true
		}
	}
	return false
}

func nativeResponseBeta(req ExecuteRequest, source, endpoint string) []string {
	if source != "openai-response" || endpoint != translate.EndpointResponses || len(req.OriginalRequest) == 0 {
		return nil
	}
	model := gjson.GetBytes(req.OriginalRequest, "model")
	if model.Type != gjson.String || model.String() != req.Model {
		return nil
	}
	return append([]string(nil), req.Headers.Values("OpenAI-Beta")...)
}
