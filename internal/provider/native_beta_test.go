package provider

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestNativeResponseBetaRequiresUnchangedNativeResponsesRoute(t *testing.T) {
	for _, test := range []struct {
		name, source, endpoint, original string
		want                             []string
	}{
		{"native route", "openai-response", translate.EndpointResponses, `{"model":"gpt-test","input":"inspect"}`, []string{"responses=beta", "other=beta"}},
		{"Responses to Chat", "openai-response", translate.EndpointChatCompletions, `{"model":"gpt-test"}`, nil},
		{"Responses to Messages", "openai-response", translate.EndpointMessages, `{"model":"gpt-test"}`, nil},
		{"transformed model", "openai-response", translate.EndpointResponses, `{"model":"client-alias"}`, nil},
		{"missing original request", "openai-response", translate.EndpointResponses, "", nil},
		{"missing original model", "openai-response", translate.EndpointResponses, `{"input":"inspect"}`, nil},
		{"other source format", "claude", translate.EndpointResponses, `{"model":"gpt-test"}`, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			headers := http.Header{"Openai-Beta": {"responses=beta", "other=beta"}}
			req := ExecuteRequest{ExecutorRequest: executorRequestForBetaTest("gpt-test", test.original, headers)}
			got := nativeResponseBeta(req, test.source, test.endpoint)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("native beta flags = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestCopilotInferenceHeadersForwardOnlySelectedNativeBetaValues(t *testing.T) {
	token := copilotTokenEntry{Token: "synthetic-token"}
	header := copilotInferenceHeaders(token, false, []string{"responses=beta", "other=beta"})
	if got, want := header.Values("OpenAI-Beta"), []string{"responses=beta", "other=beta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("OpenAI-Beta values = %#v, want %#v", got, want)
	}
	if header.Get("Authorization") != "Bearer synthetic-token" {
		t.Fatal("inference authorization was not retained")
	}
}

func executorRequestForBetaTest(model, original string, headers http.Header) pluginapi.ExecutorRequest {
	return pluginapi.ExecutorRequest{Model: model, OriginalRequest: []byte(original), Headers: headers}
}
