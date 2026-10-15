package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
)

type inferenceHeaderHost struct {
	transport.Host
	retry    bool
	requests []transport.Request
	closed   []string
}

func (h *inferenceHeaderHost) Do(_ context.Context, _ string, request transport.Request) (transport.Response, error) {
	if strings.HasSuffix(request.URL, "/copilot_internal/v2/token") {
		return transport.Response{StatusCode: http.StatusOK, Body: []byte(`{"token":"synthetic-refreshed","refresh_in":1200,"endpoints":{"api":"https://api.example.invalid"}}`)}, nil
	}
	h.requests = append(h.requests, request)
	status := http.StatusOK
	if h.retry && len(h.requests) == 1 {
		status = http.StatusUnauthorized
	}
	return transport.Response{StatusCode: status, Body: []byte(`{}`)}, nil
}

func (h *inferenceHeaderHost) OpenStream(ctx context.Context, id string, request transport.Request) (transport.Stream, error) {
	response, err := h.Do(ctx, id, request)
	return transport.Stream{StatusCode: response.StatusCode, ID: fmt.Sprintf("stream_%d", len(h.requests))}, err
}

func (h *inferenceHeaderHost) CloseStream(_ context.Context, id string) error {
	h.closed = append(h.closed, id)
	return nil
}

func TestInferenceDispatchDerivesContinuationAndVisionHeaders(t *testing.T) {
	for _, test := range []struct {
		name      string
		endpoint  string
		body      string
		initiator string
		vision    string
	}{
		{"chat first turn", translate.EndpointChatCompletions, `{"messages":[{"role":"user","content":"inspect"}]}`, "user", ""},
		{"chat continuation", translate.EndpointChatCompletions, `{"messages":[{"role":"assistant","content":"answer"},{"role":"user","content":"continue"}]}`, "agent", ""},
		{"chat tool image", translate.EndpointChatCompletions, `{"messages":[{"role":"tool","tool_call_id":"call_a","content":[{"type":"image_url","image_url":{"url":"https://media.example.invalid/image.png"}}]}]}`, "agent", "true"},
		{"chat image", translate.EndpointChatCompletions, `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://media.example.invalid/image.png"}}]}]}`, "user", "true"},
		{"chat schema", translate.EndpointChatCompletions, `{"messages":[{"role":"user","content":"inspect"}],"tools":[{"type":"function","function":{"name":"inspect","parameters":{"type":"image_url","image_url":{"url":"https://media.example.invalid/image.png"}}}}]}`, "user", ""},
		{"responses first turn", translate.EndpointResponses, `{"input":"inspect"}`, "user", ""},
		{"responses continuation", translate.EndpointResponses, `{"input":[{"role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}`, "agent", ""},
		{"responses image", translate.EndpointResponses, `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"https://media.example.invalid/image.png"}]}]}`, "user", "true"},
		{"responses tool image", translate.EndpointResponses, `{"input":[{"type":"function_call_output","call_id":"call_a","output":[{"type":"input_image","image_url":"https://media.example.invalid/image.png"}]}]}`, "agent", "true"},
		{"responses tool input", translate.EndpointResponses, `{"input":[{"type":"function_call","call_id":"call_a","name":"inspect","arguments":"{\"type\":\"input_image\"}"}]}`, "agent", ""},
		{"messages first turn", translate.EndpointMessages, `{"messages":[{"role":"user","content":"inspect"}]}`, "user", ""},
		{"messages continuation", translate.EndpointMessages, `{"messages":[{"role":"assistant","content":[{"type":"text","text":"answer"}]}]}`, "agent", ""},
		{"messages nested tool image", translate.EndpointMessages, `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_a","content":[{"type":"image","source":{"type":"url","url":"https://media.example.invalid/image.png"}}]}]}]}`, "agent", "true"},
		{"messages tool input", translate.EndpointMessages, `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_a","name":"inspect","input":{"type":"image","source":{"type":"url","url":"https://media.example.invalid/image.png"}}}]}]}`, "agent", ""},
	} {
		for _, stream := range []bool{false, true} {
			for _, retry := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%t/retry=%t", test.name, stream, retry), func(t *testing.T) {
					host := &inferenceHeaderHost{retry: retry}
					service := New(host)
					token := copilotTokenEntry{Token: "synthetic-initial", APIBaseURL: "https://api.example.invalid"}
					storage := authStorage{GitHubAccessToken: "synthetic-persistent"}
					if stream {
						if _, _, err := service.openModelStream(context.Background(), "callback", "account", storage, token, test.endpoint, []byte(test.body)); err != nil {
							t.Fatal(err)
						}
					} else if _, _, err := service.doModelRequest(context.Background(), "callback", "account", storage, token, test.endpoint, []byte(test.body), false); err != nil {
						t.Fatal(err)
					}
					wantAttempts := 1
					if retry {
						wantAttempts = 2
					}
					if len(host.requests) != wantAttempts {
						t.Fatalf("dispatches = %d, want %d", len(host.requests), wantAttempts)
					}
					for index, request := range host.requests {
						wantToken := "Bearer synthetic-initial"
						if index > 0 {
							wantToken = "Bearer synthetic-refreshed"
						}
						wantAccept := "application/json"
						if stream {
							wantAccept = "text/event-stream"
						}
						for name, want := range map[string]string{"X-Initiator": test.initiator, "Copilot-Vision-Request": test.vision, "Authorization": wantToken, "Accept": wantAccept} {
							if got := request.Headers.Get(name); got != want {
								t.Fatalf("attempt %d %s = %q, want %q", index, name, got, want)
							}
						}
						if string(request.Body) != test.body || request.URL != "https://api.example.invalid"+test.endpoint {
							t.Fatalf("outbound body or URL changed: %s %s", request.URL, request.Body)
						}
					}
					wantCloses := 0
					if stream && retry {
						wantCloses = 1
					}
					if len(host.closed) != wantCloses || wantCloses == 1 && host.closed[0] != "stream_1" {
						t.Fatalf("closed streams = %v", host.closed)
					}
				})
			}
		}
	}
}
