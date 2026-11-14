package provider

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestClaudeStopSequencesResponsesFailBeforeInferenceDispatch(t *testing.T) {
	model := capturedCopilotModel(t, "claude-haiku-5.5")
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "JSON", true: "SSE"}[streaming], func(t *testing.T) {
			host := newAttachmentRoutingExecutorHost()
			service, storage, _ := serviceWithCachedModels(t, []upstreamModel{model})
			service.host = host
			service.config.ModelEndpointOverrides[model.ID] = translate.EndpointResponses
			storage = continuityTestStorage(storage.GitHubAccessToken)
			storage.Type = providerID
			storageJSON, err := marshalStorage(storage)
			if err != nil {
				t.Fatalf("marshal synthetic storage: %v", err)
			}

			payload := []byte(`{"max_tokens":32,"messages":[{"role":"user","content":"keep this request"}],"stop_sequences":["<END>"]}`)
			request := ExecuteRequest{ExecutorRequest: pluginapi.ExecutorRequest{
				AuthID:          "auth",
				SourceFormat:    "claude",
				Model:           model.ID,
				OriginalRequest: append([]byte(nil), payload...),
				Payload:         append([]byte(nil), payload...),
				StorageJSON:     storageJSON,
			}}
			if streaming {
				request.StreamID = "stop-sequence-stream"
				_, err = service.ExecuteStream(context.Background(), request)
			} else {
				_, err = service.Execute(context.Background(), request)
			}
			var statusErr *StatusError
			if !errors.As(err, &statusErr) || statusErr.Code != "translation_error" || statusErr.HTTPStatus != http.StatusUnprocessableEntity {
				t.Fatalf("translation error = %#v, want 422 before inference", err)
			}
			if statusErr.Message != "Claude stop_sequences cannot be represented by the Copilot Responses endpoint" {
				t.Fatalf("translation message = %q", statusErr.Message)
			}
			requests, streamRequest := host.snapshots()
			if len(requests) != 0 || streamRequest.URL != "" {
				t.Fatalf("unsupported stop sequences reached inference: requests=%d stream=%s", len(requests), streamRequest.URL)
			}
			if !bytes.Equal(request.Payload, payload) || !bytes.Equal(request.OriginalRequest, payload) {
				t.Fatal("rejected request payload or original history changed")
			}
		})
	}
}
