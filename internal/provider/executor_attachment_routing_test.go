package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

type attachmentRoutingExecutorHost struct {
	mu            sync.Mutex
	modelRequests []transport.Request
	streamRequest transport.Request
	streamID      string
	streamBody    []byte
	closedOutput  chan string
}

func newAttachmentRoutingExecutorHost() *attachmentRoutingExecutorHost {
	return &attachmentRoutingExecutorHost{closedOutput: make(chan string, 1)}
}

func (h *attachmentRoutingExecutorHost) Do(_ context.Context, _ string, request transport.Request) (transport.Response, error) {
	h.mu.Lock()
	request.Body = append([]byte(nil), request.Body...)
	request.Headers = request.Headers.Clone()
	if request.Method == http.MethodPost {
		h.modelRequests = append(h.modelRequests, request)
	}
	h.mu.Unlock()
	if strings.HasSuffix(request.URL, translate.EndpointChatCompletions) {
		return transport.Response{StatusCode: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"id":"chat_attachment_route","object":"chat.completion","model":"claude-haiku-5.5","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)}, nil
	}
	if strings.HasSuffix(request.URL, translate.EndpointMessages) {
		return transport.Response{StatusCode: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"id":"msg_attachment_route","type":"message","role":"assistant","model":"claude-haiku-5.5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`)}, nil
	}
	return transport.Response{}, fmt.Errorf("unexpected endpoint URL %q", request.URL)
}

func (h *attachmentRoutingExecutorHost) OpenStream(_ context.Context, _ string, request transport.Request) (transport.Stream, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	request.Body = append([]byte(nil), request.Body...)
	request.Headers = request.Headers.Clone()
	h.streamRequest = request
	h.streamID = "attachment-routing-stream"
	if strings.HasSuffix(request.URL, translate.EndpointChatCompletions) {
		h.streamBody = []byte("data: {\"id\":\"chat_attachment_route\",\"object\":\"chat.completion.chunk\",\"model\":\"claude-haiku-5.5\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	} else if strings.HasSuffix(request.URL, translate.EndpointMessages) {
		h.streamBody = []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_attachment_route\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-haiku-5.5\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	} else {
		return transport.Stream{}, fmt.Errorf("unexpected stream endpoint URL %q", request.URL)
	}
	return transport.Stream{ID: h.streamID, StatusCode: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}}, nil
}

func (h *attachmentRoutingExecutorHost) ReadStream(_ context.Context, streamID string) (transport.StreamChunk, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if streamID != h.streamID {
		return transport.StreamChunk{}, fmt.Errorf("unexpected stream ID %q", streamID)
	}
	body := append([]byte(nil), h.streamBody...)
	h.streamBody = nil
	return transport.StreamChunk{Payload: body, Done: true}, nil
}

func (*attachmentRoutingExecutorHost) CloseStream(context.Context, string) error { return nil }

func (*attachmentRoutingExecutorHost) Emit(context.Context, string, []byte) error { return nil }

func (h *attachmentRoutingExecutorHost) CloseOutput(_ context.Context, _ string, message string) {
	h.closedOutput <- message
}

func (h *attachmentRoutingExecutorHost) snapshots() ([]transport.Request, transport.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	requests := make([]transport.Request, len(h.modelRequests))
	for index, request := range h.modelRequests {
		requests[index] = request
		requests[index].Body = append([]byte(nil), request.Body...)
		requests[index].Headers = request.Headers.Clone()
	}
	streamRequest := h.streamRequest
	streamRequest.Body = append([]byte(nil), streamRequest.Body...)
	streamRequest.Headers = streamRequest.Headers.Clone()
	return requests, streamRequest
}

func TestExecuteAttachmentRoutingUsesAdvertisedLosslessEndpoint(t *testing.T) {
	for _, test := range []struct {
		name      string
		streaming bool
		payload   []byte
		override  string
		want      string
		detail    string
		wantError bool
		wantHigh  bool
	}{
		{name: "JSON high detail uses advertised Chat", payload: capturedResponsesImageRequest(t, "high"), want: translate.EndpointChatCompletions, wantHigh: true},
		{name: "stream high detail uses advertised Chat", streaming: true, payload: capturedResponsesImageRequest(t, "high"), want: translate.EndpointChatCompletions, wantHigh: true},
		{name: "JSON explicit Messages override returns typed 422", payload: capturedResponsesImageRequest(t, "high"), override: translate.EndpointMessages, want: translate.EndpointMessages, detail: "high", wantError: true},
		{name: "stream explicit Messages override returns typed 422", streaming: true, payload: capturedResponsesImageRequest(t, "high"), override: translate.EndpointMessages, want: translate.EndpointMessages, detail: "high", wantError: true},
		{name: "JSON original detail stays unsupported", payload: capturedResponsesImageRequest(t, "original"), want: translate.EndpointMessages, detail: "original", wantError: true},
		{name: "plain JSON preserves default Messages route", payload: []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`), want: translate.EndpointMessages},
		{name: "plain stream preserves default Messages route", streaming: true, payload: []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`), want: translate.EndpointMessages},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := capturedCopilotModel(t, "claude-haiku-5.5")
			host := newAttachmentRoutingExecutorHost()
			service, storage, _ := serviceWithCachedModels(t, []upstreamModel{model})
			service.host = host
			if test.override != "" {
				service.config.ModelEndpointOverrides[model.ID] = test.override
			}
			storage = continuityTestStorage(storage.GitHubAccessToken)
			storage.Type = providerID
			storageJSON, err := marshalStorage(storage)
			if err != nil {
				t.Fatal(err)
			}
			request := ExecuteRequest{
				ExecutorRequest: pluginapi.ExecutorRequest{
					AuthID: "auth", SourceFormat: "openai-response", Model: model.ID,
					Payload: test.payload, OriginalRequest: append([]byte(nil), test.payload...), StorageJSON: storageJSON,
				},
				StreamID: "attachment-routing-output",
			}
			if test.streaming {
				headers, err := service.ExecuteStream(context.Background(), request)
				if test.wantError {
					assertUnsupportedImageDetail422(t, err, test.want, test.detail)
				} else if err != nil || headers.Get("Content-Type") != "text/event-stream" {
					t.Fatalf("ExecuteStream headers/error = %#v/%v", headers, err)
				}
			} else {
				response, err := service.Execute(context.Background(), request)
				if test.wantError {
					assertUnsupportedImageDetail422(t, err, test.want, test.detail)
				} else if err != nil {
					t.Fatalf("Execute: %v", err)
				} else if response.Metadata["copilot_endpoint"] != test.want {
					t.Fatalf("response endpoint metadata = %#v, want %q", response.Metadata["copilot_endpoint"], test.want)
				}
			}

			requests, streamRequest := host.snapshots()
			if test.wantError {
				if len(requests) != 0 || streamRequest.URL != "" {
					t.Fatalf("incompatible attachment reached inference egress: requests=%d stream=%s", len(requests), streamRequest.URL)
				}
				if !bytes.Equal(request.Payload, test.payload) {
					t.Fatal("rejected request payload changed before returning typed 422")
				}
				return
			}
			var outbound transport.Request
			if test.streaming {
				outbound = streamRequest
			} else if len(requests) == 1 {
				outbound = requests[0]
			} else {
				t.Fatalf("model request count = %d, want one", len(requests))
			}
			if !strings.HasSuffix(outbound.URL, test.want) || gjson.GetBytes(outbound.Body, "model").String() != model.ID {
				t.Fatalf("outbound request route/model = %s/%q, want %s/%q", outbound.URL, gjson.GetBytes(outbound.Body, "model").String(), test.want, model.ID)
			}
			if test.wantHigh && !requestContainsImageDetail(outbound.Body, "high") {
				t.Fatalf("high image detail was not preserved in outbound request: %s", outbound.Body)
			}
			if !test.wantHigh && gjson.GetBytes(outbound.Body, "messages.0.role").String() != "user" {
				t.Fatalf("plain input did not remain a user message: %s", outbound.Body)
			}
			if test.streaming {
				select {
				case message := <-host.closedOutput:
					if message != "" {
						t.Fatalf("stream closed with error: %s", message)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("upstream stream did not finish")
				}
			}
		})
	}
}

func assertUnsupportedImageDetail422(t *testing.T, err error, endpoint, detail string) {
	t.Helper()
	statusErr, ok := err.(*StatusError)
	if !ok || statusErr.Code != "unsupported_image_detail" || statusErr.HTTPStatus != http.StatusUnprocessableEntity {
		t.Fatalf("image-detail translation error = %#v, want 422 unsupported_image_detail", err)
	}
	wantMessage := (&translate.UnsupportedImageDetailError{Endpoint: endpoint, Detail: detail}).Error()
	if statusErr.Message != wantMessage {
		t.Fatalf("image-detail error message = %q, want %q", statusErr.Message, wantMessage)
	}
}

func requestContainsImageDetail(body []byte, detail string) bool {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return false
	}
	messages, ok := root["messages"].([]any)
	if !ok {
		return false
	}
	for _, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := message["content"].([]any)
		if !ok {
			continue
		}
		for _, rawPart := range parts {
			part, ok := rawPart.(map[string]any)
			if !ok || part["type"] != "image_url" {
				continue
			}
			imageURL, ok := part["image_url"].(map[string]any)
			if ok && imageURL["detail"] == detail {
				return true
			}
		}
	}
	return false
}
