package provider

import (
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

type directInferenceHeaderHost struct {
	*directOAuthTestHost
	endpoint string
	closed   chan string
}

func (h *directInferenceHeaderHost) Do(ctx context.Context, callback string, request transport.Request) (transport.Response, error) {
	response, err := h.directOAuthTestHost.Do(ctx, callback, request)
	if err != nil || request.Method != http.MethodPost || response.StatusCode != http.StatusOK {
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

func (h *directInferenceHeaderHost) ReadStream(ctx context.Context, streamID string) (transport.StreamChunk, error) {
	return (&inferenceHeaderTransportHost{endpoint: h.endpoint}).ReadStream(ctx, streamID)
}

func (h *directInferenceHeaderHost) CloseOutput(_ context.Context, _ string, message string) {
	h.closed <- message
}

func setupDirectInferenceHeaderService(t *testing.T, endpoint string) (*Service, *directInferenceHeaderHost, []byte, string) {
	t.Helper()
	const authID = "direct-test-auth"
	host := &directInferenceHeaderHost{directOAuthTestHost: newDirectOAuthTestHost(), endpoint: endpoint, closed: make(chan string, 1)}
	service := New(host)
	if err := service.Configure([]byte("auth_mode: direct_oauth\n")); err != nil {
		t.Fatal(err)
	}
	storageJSON, storage := directOAuthStorage(t, "synthetic-direct-credential", 0)
	fingerprint := tokenFingerprint(storage.GitHubAccessToken)
	_, generation := service.configSnapshot()
	now := service.now()
	model := capturedCopilotModel(t, "gpt-6-luna")
	model.SupportedEndpoints = []string{endpoint}
	model.Capabilities.Supports.Vision = true
	service.tokenEntries[authID] = copilotTokenEntry{
		Token: storage.GitHubAccessToken, APIBaseURL: "https://api.example", Mode: authModeDirectOAuth,
		ContextExpiresAt: now.Add(time.Hour), Fingerprint: fingerprint, ConfigGeneration: generation,
	}
	service.modelEntries[authID] = modelCacheEntry{
		Fingerprint: fingerprint, APIBaseURL: "https://api.example", Mode: authModeDirectOAuth,
		ConfigGeneration: generation, ExpiresAt: now.Add(time.Hour), Models: []upstreamModel{model},
	}
	return service, host, storageJSON, authID
}

func TestDirectOAuthDerivesInferenceHeadersAtExecutorTransport(t *testing.T) {
	for _, test := range []struct {
		name, source, endpoint, body, initiator, vision string
	}{
		{"chat user", "openai", translate.EndpointChatCompletions, `{"model":"gpt-6-luna","messages":[{"role":"user","content":"hello"}]}`, "user", ""},
		{"chat continuation", "openai", translate.EndpointChatCompletions, `{"model":"gpt-6-luna","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"continuing"}]}`, "agent", ""},
		{"chat image", "openai", translate.EndpointChatCompletions, `{"model":"gpt-6-luna","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2U="}}]}]}`, "user", "true"},
		{"chat image name only in tool arguments", "openai", translate.EndpointChatCompletions, `{"model":"gpt-6-luna","messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"type\":\"image_url\"}"}}]}]}`, "agent", ""},
		{"responses image converted to chat", "openai-response", translate.EndpointChatCompletions, `{"model":"gpt-6-luna","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,aW1hZ2U="}]}]}`, "user", "true"},
		{"responses user", "openai-response", translate.EndpointResponses, `{"model":"gpt-6-luna","input":[{"role":"user","content":"hello"}]}`, "user", ""},
		{"responses continuation", "openai-response", translate.EndpointResponses, `{"model":"gpt-6-luna","input":[{"type":"function_call_output","call_id":"call_1","output":"done"}]}`, "agent", ""},
		{"responses image", "openai-response", translate.EndpointResponses, `{"model":"gpt-6-luna","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,aW1hZ2U="}]}]}`, "user", "true"},
		{"responses tool image", "openai-response", translate.EndpointResponses, `{"model":"gpt-6-luna","input":[{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_image","image_url":"data:image/png;base64,aW1hZ2U="}]}]}`, "agent", "true"},
		{"chat tool result converted to responses", "openai", translate.EndpointResponses, `{"model":"gpt-6-luna","messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"done"}]}`, "agent", ""},
		{"messages user", "claude", translate.EndpointMessages, `{"model":"gpt-6-luna","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`, "user", ""},
		{"messages continuation", "claude", translate.EndpointMessages, `{"model":"gpt-6-luna","max_tokens":64,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"tool_1","name":"read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_1","content":"done"}]}]}`, "agent", ""},
		{"messages image", "claude", translate.EndpointMessages, `{"model":"gpt-6-luna","max_tokens":64,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}}]}]}`, "user", "true"},
		{"messages tool image", "claude", translate.EndpointMessages, `{"model":"gpt-6-luna","max_tokens":64,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"tool_1","name":"read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}}]}]}]}`, "agent", "true"},
	} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", test.name, streaming), func(t *testing.T) {
				service, host, storage, authID := setupDirectInferenceHeaderService(t, test.endpoint)
				payload := []byte(test.body)
				request := ExecuteRequest{ExecutorRequest: pluginapi.ExecutorRequest{
					AuthID: authID, SourceFormat: test.source, Model: "gpt-6-luna", Payload: payload,
					OriginalRequest: append([]byte(nil), payload...), StorageJSON: storage,
					Headers: http.Header{"Authorization": {"Bearer caller-token"}, "Cookie": {"caller-cookie"}, "X-Initiator": {"spoofed"}, "Copilot-Vision-Request": {"spoofed"}, "Editor-Version": {"vscode/spoofed"}},
				}, StreamID: "direct-header-output"}
				var err error
				if streaming {
					_, err = service.ExecuteStream(context.Background(), request)
					if err == nil {
						select {
						case message := <-host.closed:
							if message != "" {
								t.Fatalf("stream closed with error: %s", message)
							}
						case <-time.After(10 * time.Second):
							t.Fatal("stream did not close")
						}
					}
				} else {
					_, err = service.Execute(context.Background(), request)
				}
				if err != nil {
					t.Fatalf("execute: %v", err)
				}
				requests, streams := host.requestSnapshot()
				var outbound transport.Request
				if streaming {
					if len(streams) != 1 || len(requests) != 0 {
						t.Fatalf("requests=%d streams=%d, want 0 and 1", len(requests), len(streams))
					}
					outbound = streams[0]
				} else {
					if len(requests) != 1 || len(streams) != 0 {
						t.Fatalf("requests=%d streams=%d, want 1 and 0", len(requests), len(streams))
					}
					outbound = requests[0]
				}
				if !strings.HasSuffix(outbound.URL, test.endpoint) {
					t.Fatalf("unexpected endpoint %q", outbound.URL)
				}
				switch test.name {
				case "responses image converted to chat":
					if got := gjson.GetBytes(outbound.Body, "messages.0.content.0.type").String(); got != "image_url" {
						t.Fatalf("converted image content type = %q, want image_url", got)
					}
				case "chat tool result converted to responses":
					if got := gjson.GetBytes(outbound.Body, "input.1.output").String(); got != "done" {
						t.Fatalf("converted tool result = %q, want done", got)
					}
				}
				if got := outbound.Headers.Get("X-Initiator"); got != test.initiator {
					t.Fatalf("initiator = %q, want %q", got, test.initiator)
				}
				if got := outbound.Headers.Get("Copilot-Vision-Request"); got != test.vision {
					t.Fatalf("vision = %q, want %q", got, test.vision)
				}
				if outbound.Headers.Get("Authorization") != "Bearer synthetic-direct-credential" || outbound.Headers.Get("User-Agent") != userAgent() {
					t.Fatal("direct OAuth authorization or user agent changed")
				}
				for _, name := range []string{"Cookie", "Copilot-Integration-Id", "Editor-Version", "Editor-Plugin-Version", "OpenAI-Intent", "X-Agent-Task-Id", "X-Interaction-Type", "X-Request-Id"} {
					if got := outbound.Headers.Get(name); got != "" {
						t.Fatalf("unexpected caller/editor identity header %s", name)
					}
				}
			})
		}
	}
}

func TestDirectOAuthHeaderDispatchDoesNotRetryUnauthorized(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("status=%d/stream=%t", status, streaming), func(t *testing.T) {
				service, host, storage, authID := setupDirectInferenceHeaderService(t, translate.EndpointResponses)
				host.responseStatus, host.streamStatus = status, status
				request := directOAuthExecuteRequest(storage, authID, "direct-header-output")
				var err error
				if streaming {
					_, err = service.ExecuteStream(context.Background(), request)
				} else {
					_, err = service.Execute(context.Background(), request)
				}
				statusErr, ok := err.(*StatusError)
				if !ok || statusErr.HTTPStatus != status {
					t.Fatalf("error = %#v, want status %d", err, status)
				}
				requests, streams := host.requestSnapshot()
				if (!streaming && (len(requests) != 1 || len(streams) != 0)) || (streaming && (len(requests) != 0 || len(streams) != 1)) {
					t.Fatalf("direct request retried: HTTP=%d stream=%d", len(requests), len(streams))
				}
				if _, exists := service.tokenEntries[authID]; exists {
					t.Fatal("direct auth cache survived unauthorized response")
				}
				if _, exists := service.modelEntries[authID]; exists {
					t.Fatal("direct model cache survived unauthorized response")
				}
			})
		}
	}
}

func TestDirectOAuthImageWithoutVisionCapabilityOmitsVisionHeader(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", streaming), func(t *testing.T) {
			service, host, storage, authID := setupDirectInferenceHeaderService(t, translate.EndpointChatCompletions)
			cached := service.modelEntries[authID]
			cached.Models[0].Capabilities.Supports.Vision = false
			service.modelEntries[authID] = cached
			payload := []byte(`{"model":"gpt-6-luna","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2U="}}]}]}`)
			request := ExecuteRequest{ExecutorRequest: pluginapi.ExecutorRequest{
				AuthID: authID, SourceFormat: "openai", Model: "gpt-6-luna", Payload: payload,
				OriginalRequest: append([]byte(nil), payload...), StorageJSON: storage,
			}, StreamID: "direct-vision-output"}
			var err error
			if streaming {
				_, err = service.ExecuteStream(context.Background(), request)
				if err == nil {
					select {
					case message := <-host.closed:
						if message != "" {
							t.Fatalf("stream closed with error: %s", message)
						}
					case <-time.After(10 * time.Second):
						t.Fatal("stream did not close")
					}
				}
			} else {
				_, err = service.Execute(context.Background(), request)
			}
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			requests, streams := host.requestSnapshot()
			var outbound transport.Request
			if streaming {
				if len(streams) != 1 {
					t.Fatalf("streams = %d, want 1", len(streams))
				}
				outbound = streams[0]
			} else {
				if len(requests) != 1 {
					t.Fatalf("requests = %d, want 1", len(requests))
				}
				outbound = requests[0]
			}
			if got := outbound.Headers.Get("Copilot-Vision-Request"); got != "" {
				t.Fatalf("vision header = %q without advertised capability", got)
			}
		})
	}
}
