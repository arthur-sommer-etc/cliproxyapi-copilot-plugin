package provider

import (
	"net/http"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/tidwall/gjson"
)

func inferenceHeaders(token string, stream bool, endpoint string, body []byte) http.Header {
	headers := copilotHeaders(token, stream)
	root := gjson.ParseBytes(body)
	history := root.Get("messages")
	if endpoint == translate.EndpointResponses {
		history = root.Get("input")
	}
	for _, message := range history.Array() {
		role := message.Get("role").String()
		agent := role == "assistant" || role == "tool"
		content := message.Get("content")
		if endpoint == translate.EndpointResponses {
			switch message.Get("type").String() {
			case "function_call", "custom_tool_call":
				agent = true
			case "function_call_output", "custom_tool_call_output":
				agent = true
				content = message.Get("output")
			}
		}
		tool, image := inferenceContentFlags(endpoint, content)
		if agent || tool {
			headers.Set("X-Initiator", "agent")
		}
		if image {
			headers.Set("Copilot-Vision-Request", "true")
		}
	}
	return headers
}

func inferenceContentFlags(endpoint string, content gjson.Result) (agent, image bool) {
	for _, part := range content.Array() {
		kind := part.Get("type").String()
		if endpoint == translate.EndpointChatCompletions && kind == "image_url" || endpoint == translate.EndpointResponses && kind == "input_image" || endpoint == translate.EndpointMessages && kind == "image" {
			image = true
		}
		if endpoint == translate.EndpointMessages && kind == "tool_result" {
			agent = true
			_, nestedImage := inferenceContentFlags(endpoint, part.Get("content"))
			image = image || nestedImage
		}
	}
	return agent, image
}
