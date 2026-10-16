package provider

import (
	"context"
	"testing"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

func TestApplyGeminiChatFunctionCallingMode(t *testing.T) {
	gemini := capturedCopilotModel(t, "gemini-3.8-flash")
	other := capturedCopilotModel(t, "gpt-6-luna")
	tools := `"tools":[{"type":"function","function":{"name":"Read","parameters":{"type":"object"}}}]`
	for _, test := range []struct {
		name       string
		model      upstreamModel
		endpoint   string
		body       string
		want       string
		wantObject bool
	}{
		{name: "gemini chat with tools selects validated mode", model: gemini, endpoint: translate.EndpointChatCompletions, body: `{"model":"gemini-3.8-flash",` + tools + `}`, want: "validated"},
		{name: "string tool choice is replaced", model: gemini, endpoint: translate.EndpointChatCompletions, body: `{"model":"gemini-3.8-flash","tool_choice":"none",` + tools + `}`, want: "validated"},
		{name: "pinned function selection is preserved", model: gemini, endpoint: translate.EndpointChatCompletions, body: `{"model":"gemini-3.8-flash","tool_choice":{"type":"function","function":{"name":"Read"}},` + tools + `}`, wantObject: true},
		{name: "toolless request is unchanged", model: gemini, endpoint: translate.EndpointChatCompletions, body: `{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"hello"}]}`, want: ""},
		{name: "other model is unchanged", model: other, endpoint: translate.EndpointChatCompletions, body: `{"model":"gpt-6-luna",` + tools + `}`, want: ""},
		{name: "responses endpoint is unchanged", model: gemini, endpoint: translate.EndpointResponses, body: `{"model":"gemini-3.8-flash",` + tools + `}`, want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			updated, err := applyGeminiChatFunctionCallingMode(test.model, test.endpoint, []byte(test.body))
			if err != nil {
				t.Fatal(err)
			}
			choice := gjson.GetBytes(updated, "tool_choice")
			if test.wantObject {
				if !choice.IsObject() {
					t.Fatalf("tool_choice = %s, want the preserved function selection", choice.Raw)
				}
				return
			}
			if got := choice.String(); got != test.want {
				t.Fatalf("tool_choice = %q, want %q", got, test.want)
			}
		})
	}
}

func TestExecuteGeminiChatImageDispatchDerivesHeadersAndFunctionCallingMode(t *testing.T) {
	firstTurn := `{"model":"gemini-3.8-flash","max_tokens":64,` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"Describe the image."},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}}]}],` +
		`"tools":[{"name":"describe","description":"Report colors.","input_schema":{"type":"object","properties":{"top":{"type":"string"}}}}]}`
	continuation := `{"model":"gemini-3.8-flash","max_tokens":64,` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"Describe the image."}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"Reading."},{"type":"tool_use","id":"toolu_1","name":"describe","input":{"top":"?"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}}]}]}],` +
		`"tools":[{"name":"describe","description":"Report colors.","input_schema":{"type":"object","properties":{"top":{"type":"string"}}}}]}`
	for _, test := range []struct {
		name          string
		payload       string
		wantInitiator string
	}{
		{name: "first user turn", payload: firstTurn, wantInitiator: "user"},
		{name: "tool result continuation", payload: continuation, wantInitiator: "agent"},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := capturedCopilotModel(t, "gemini-3.8-flash")
			host := newAttachmentRoutingExecutorHost()
			service, storage, _ := serviceWithCachedModels(t, []upstreamModel{model})
			service.host = host
			storage = continuityTestStorage(storage.GitHubAccessToken)
			storage.Type = providerID
			storageJSON, err := marshalStorage(storage)
			if err != nil {
				t.Fatal(err)
			}
			request := ExecuteRequest{
				ExecutorRequest: pluginapi.ExecutorRequest{
					AuthID: "auth", SourceFormat: "claude", Model: model.ID,
					Payload: []byte(test.payload), OriginalRequest: []byte(test.payload), StorageJSON: storageJSON,
				},
				StreamID: "gemini-image-output",
			}
			if _, err := service.Execute(context.Background(), request); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			requests, _ := host.snapshots()
			if len(requests) != 1 {
				t.Fatalf("model request count = %d, want one", len(requests))
			}
			outbound := requests[0]
			if outbound.URL != "https://copilot.example"+translate.EndpointChatCompletions {
				t.Fatalf("outbound route = %s, want the gemini chat endpoint", outbound.URL)
			}
			if got := outbound.Headers.Get("X-Initiator"); got != test.wantInitiator {
				t.Fatalf("X-Initiator = %q, want %q", got, test.wantInitiator)
			}
			if got := outbound.Headers.Get("Copilot-Vision-Request"); got != "true" {
				t.Fatal("image-bearing gemini request omitted Copilot-Vision-Request")
			}
			if got := gjson.GetBytes(outbound.Body, "tool_choice").String(); got != "validated" {
				t.Fatalf("tool_choice = %q, want the gemini function calling mode", got)
			}
			if !requestCarriesImageDataURL(outbound.Body) {
				t.Fatalf("outbound chat body lost the inline image carrier: %s", outbound.Body)
			}
		})
	}
}

func requestCarriesImageDataURL(body []byte) bool {
	for _, message := range gjson.GetBytes(body, "messages").Array() {
		for _, part := range message.Get("content").Array() {
			if part.Get("type").String() == "image_url" && part.Get("image_url.url").String() == "data:image/png;base64,aW1hZ2U=" {
				return true
			}
		}
	}
	return false
}
