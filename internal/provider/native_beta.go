package provider

import (
	"net/http"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/tidwall/gjson"
)

func copilotInferenceHeaders(token copilotTokenEntry, stream bool, nativeBeta []string) http.Header {
	headers := copilotRequestHeaders(token, stream)
	for _, value := range nativeBeta {
		headers.Add("OpenAI-Beta", value)
	}
	return headers
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
