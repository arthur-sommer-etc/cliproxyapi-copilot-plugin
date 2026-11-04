package provider

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

// inferenceHeaderTransportHost returns synthetic successful responses after a
// 401 so the executor must rebuild the request with a refreshed bearer token.
type inferenceHeaderTransportHost struct {
	refreshTestHost
	endpoint string
	closed   chan string
}

func (h *inferenceHeaderTransportHost) Do(ctx context.Context, callbackID string, request transport.Request) (transport.Response, error) {
	response, err := h.refreshTestHost.Do(ctx, callbackID, request)
	if err != nil || response.StatusCode != http.StatusOK || !strings.HasSuffix(request.URL, h.endpoint) {
		return response, err
	}
	switch h.endpoint {
	case translate.EndpointChatCompletions:
		response.Body = []byte(`{"id":"chat_synthetic","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	case translate.EndpointResponses:
		response.Body = []byte(`{"id":"resp_synthetic","object":"response","status":"completed","output":[]}`)
	case translate.EndpointMessages:
		response.Body = []byte(`{"id":"msg_synthetic","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}
	return response, nil
}

func (h *inferenceHeaderTransportHost) ReadStream(context.Context, string) (transport.StreamChunk, error) {
	var body string
	switch h.endpoint {
	case translate.EndpointChatCompletions:
		body = "data: {\"id\":\"chat_synthetic\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	case translate.EndpointResponses:
		body = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_synthetic\",\"status\":\"completed\",\"output\":[]}}\n\n"
	case translate.EndpointMessages:
		body = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_synthetic\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	}
	return transport.StreamChunk{Payload: []byte(body), Done: true}, nil
}

func (h *inferenceHeaderTransportHost) CloseOutput(_ context.Context, _ string, message string) {
	h.closed <- message
}

func TestExecutorDerivesInferenceHeadersFromOutboundBodiesAcrossRefresh(t *testing.T) {
	for _, test := range []struct {
		name, source, endpoint, body, markerPath, markerValue, initiator, vision string
	}{
		{"chat text", "openai", translate.EndpointChatCompletions, `{"messages":[{"role":"user","content":"hello"}]}`, "messages.0.content", "hello", "user", ""},
		{"chat continuation", "openai", translate.EndpointChatCompletions, `{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"continuing"}]}`, "messages.1.content", "continuing", "agent", ""},
		{"chat image", "openai", translate.EndpointChatCompletions, `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2U="}}]}]}`, "messages.0.content.0.type", "image_url", "user", "true"},
		{"responses image converted to chat", "openai-response", translate.EndpointChatCompletions, `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,aW1hZ2U="}]}]}`, "messages.0.content.0.type", "image_url", "user", "true"},
		{"responses text", "openai-response", translate.EndpointResponses, `{"input":[{"role":"user","content":"hello"}]}`, "input.0.content", "hello", "user", ""},
		{"responses continuation", "openai-response", translate.EndpointResponses, `{"input":[{"type":"function_call_output","call_id":"call_1","output":"done"}]}`, "input.0.output", "done", "agent", ""},
		{"chat tool result converted to responses", "openai", translate.EndpointResponses, `{"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"done"}]}`, "input.1.output", "done", "agent", ""},
		{"responses tool image", "openai-response", translate.EndpointResponses, `{"input":[{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_image","image_url":"data:image/png;base64,aW1hZ2U="}]}]}`, "input.0.output.0.type", "input_image", "agent", "true"},
		{"messages text", "claude", translate.EndpointMessages, `{"max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`, "messages.0.content", "hello", "user", ""},
		{"messages continuation", "claude", translate.EndpointMessages, `{"max_tokens":64,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"tool_1","name":"read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_1","content":"done"}]}]}`, "messages.1.content.0.type", "tool_result", "agent", ""},
		{"messages tool image", "claude", translate.EndpointMessages, `{"max_tokens":64,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"tool_1","name":"read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}}]}]}]}`, "messages.1.content.0.content.0.type", "image", "agent", "true"},
	} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", test.name, streaming), func(t *testing.T) {
				model := capturedCopilotModel(t, "gemini-3.8-flash")
				model.SupportedEndpoints = []string{test.endpoint}
				model.Capabilities.Supports.Vision = true
				service, storage, _ := serviceWithCachedModels(t, []upstreamModel{model})
				service.config.ModelEndpointOverrides[model.ID] = test.endpoint
				storage = continuityTestStorage(storage.GitHubAccessToken)
				storage.Type = providerID
				storageJSON, err := marshalStorage(storage)
				if err != nil {
					t.Fatal(err)
				}
				host := &inferenceHeaderTransportHost{refreshTestHost: refreshTestHost{tokenAPIBaseURLs: []string{"https://copilot.example"}}, endpoint: test.endpoint, closed: make(chan string, 1)}
				service.host = host
				if streaming {
					host.streamStatuses = []int{http.StatusUnauthorized, http.StatusOK}
				} else {
					host.modelStatuses = []int{http.StatusUnauthorized, http.StatusOK}
				}
				payload := []byte(test.body)
				request := ExecuteRequest{ExecutorRequest: pluginapi.ExecutorRequest{AuthID: "auth", SourceFormat: test.source, Model: model.ID, Payload: payload, OriginalRequest: append([]byte(nil), payload...), StorageJSON: storageJSON}, StreamID: "inference-header-stream"}
				if streaming {
					if _, err := service.ExecuteStream(context.Background(), request); err != nil {
						t.Fatalf("execute stream: %v", err)
					}
					select {
					case message := <-host.closed:
						if message != "" {
							t.Fatalf("stream closed with error: %s", message)
						}
					case <-time.After(10 * time.Second):
						t.Fatal("stream did not close")
					}
				} else if _, err := service.Execute(context.Background(), request); err != nil {
					t.Fatalf("execute JSON: %v", err)
				}
				host.mu.Lock()
				requests := append([]transport.Request(nil), host.modelRequests...)
				if streaming {
					requests = append([]transport.Request(nil), host.streamRequests...)
				}
				tokenCalls := host.copilotTokenCalls
				host.mu.Unlock()
				if len(requests) != 2 || tokenCalls != 1 {
					t.Fatalf("inference requests = %d and refreshes = %d, want 2 and 1", len(requests), tokenCalls)
				}
				for index, outbound := range requests {
					if !strings.HasSuffix(outbound.URL, test.endpoint) || !bytes.Equal(outbound.Body, requests[0].Body) {
						t.Fatalf("request %d changed endpoint or body during refresh", index)
					}
					if got := gjson.GetBytes(outbound.Body, test.markerPath).String(); got != test.markerValue {
						t.Fatalf("outbound body field %s = %q, want %q", test.markerPath, got, test.markerValue)
					}
					if got := outbound.Headers.Get("X-Initiator"); got != test.initiator {
						t.Fatalf("request %d initiator = %q, want %q", index, got, test.initiator)
					}
					if got := outbound.Headers.Get("Copilot-Vision-Request"); got != test.vision {
						t.Fatalf("request %d vision header = %q, want %q", index, got, test.vision)
					}
					wantToken := "Bearer synthetic-copilot-token"
					if index == 1 {
						wantToken = "Bearer copilot-token-1"
					}
					if got := outbound.Headers.Get("Authorization"); got != wantToken {
						t.Fatalf("request %d authorization = %q, want %q", index, got, wantToken)
					}
				}
			})
		}
	}
}
