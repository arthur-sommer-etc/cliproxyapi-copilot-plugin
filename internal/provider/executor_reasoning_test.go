package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

type reasoningHost struct {
	transport.Host
	requests []transport.Request
}

func (h *reasoningHost) Do(_ context.Context, _ string, request transport.Request) (transport.Response, error) {
	h.requests = append(h.requests, request)
	return transport.Response{StatusCode: http.StatusOK, Body: []byte(`{"id":"chat_1","choices":[{"index":0,"message":{"role":"assistant","content":"answer","reasoning_text":"plan","reasoning_opaque":"synthetic-opaque"},"finish_reason":"stop"}]}`)}, nil
}

func reasoningService(host transport.Host) *Service {
	service := New(host)
	fingerprint := tokenFingerprint("synthetic-credential")
	service.tokenEntries["account"] = copilotTokenEntry{Token: "synthetic-bearer", APIBaseURL: "https://api.example.invalid", ExpiresAt: time.Now().Add(time.Hour), Fingerprint: fingerprint}
	service.modelEntries["account"] = modelCacheEntry{Fingerprint: fingerprint, APIBaseURL: "https://api.example.invalid", ExpiresAt: time.Now().Add(time.Hour), Models: []upstreamModel{{ID: "gemini-3.8-flash", SupportedEndpoints: []string{translate.EndpointChatCompletions}}}}
	return service
}

func TestExecuteRecoversOriginalGeminiThinkingHistory(t *testing.T) {
	host := &reasoningHost{}
	service := reasoningService(host)
	initial := []byte(`{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"inspect"}]}`)
	request := ExecuteRequest{ExecutorRequest: pluginapi.ExecutorRequest{AuthID: "account", Model: "gemini-3.8-flash", SourceFormat: "claude", OriginalRequest: initial, Payload: []byte(`{"input":[]}`), StorageJSON: []byte(`{"type":"copilot","github_access_token":"synthetic-credential"}`)}}
	response, err := service.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	var blocks any
	if err := json.Unmarshal([]byte(gjson.GetBytes(response.Payload, "content").Raw), &blocks); err != nil {
		t.Fatal(err)
	}
	next, err := json.Marshal(map[string]any{"model": "gemini-3.8-flash", "messages": []any{map[string]any{"role": "user", "content": "inspect"}, map[string]any{"role": "assistant", "content": blocks}, map[string]any{"role": "user", "content": "follow up"}}})
	if err != nil {
		t.Fatal(err)
	}
	request.OriginalRequest = next
	request.Payload = []byte(`{"input":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}`)
	if _, err := service.Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(host.requests) != 2 {
		t.Fatalf("dispatch count = %d", len(host.requests))
	}
	for _, field := range []struct{ path, want string }{{"messages.0.content", "inspect"}, {"messages.1.reasoning_text", "plan"}, {"messages.1.reasoning_opaque", "synthetic-opaque"}, {"messages.1.content", "answer"}} {
		if got := gjson.GetBytes(host.requests[1].Body, field.path).String(); got != field.want {
			t.Fatalf("%s = %q, want %q", field.path, got, field.want)
		}
	}
	request.OriginalRequest = []byte(strings.ReplaceAll(string(next), "inspect", "changed"))
	_, err = service.Execute(context.Background(), request)
	var status *StatusError
	if !errors.As(err, &status) || status.Code != "translation_error" || status.HTTPStatus != http.StatusUnprocessableEntity || len(host.requests) != 2 {
		t.Fatalf("error = %#v, dispatch count = %d", err, len(host.requests))
	}
}

func TestPumpGeminiReasoningWaitsForEOFAndClosesOnce(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "read failure"}[failure], func(t *testing.T) {
			frames := "data: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning_text\":\"plan\",\"content\":\"answer\"},\"finish_reason\":null}]}\n\n" + "data: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_opaque\":\"synthetic-opaque\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
			host := &scriptedStreamHost{chunks: []transport.StreamChunk{{Payload: []byte(frames), Done: true}}}
			if failure {
				host.chunks[0].Done = false
				host.chunks = append(host.chunks, transport.StreamChunk{Error: "synthetic read error", Done: true})
			}
			replay := translate.NewCopilotReplay("account", "gemini-3.8-flash", translate.EndpointChatCompletions, "https://api.example.invalid", "synthetic-credential")
			request := []byte(`{"messages":[{"role":"user","content":"inspect"}]}`)
			(&Service{host: host}).pumpStream("output", translate.EndpointChatCompletions, "openai-response", "gemini-3.8-flash", nil, request, transport.Stream{ID: "upstream"}, replay)
			closes, terminals := 0, 0
			closed := false
			for _, call := range host.calls {
				if call == "close:upstream" {
					closes++
					closed = true
				} else if call == "emit:output" && !closed {
					t.Fatal("buffered reasoning emitted before transport close")
				}
			}
			for _, frame := range host.emitted {
				for _, line := range strings.Split(frame, "\n") {
					if data, ok := strings.CutPrefix(line, "data: "); ok && gjson.Get(data, "type").String() == "response.completed" {
						terminals++
					}
				}
			}
			wantTerminals := 1
			if failure {
				wantTerminals = 0
			}
			if closes != 1 || terminals != wantTerminals || (host.closeMessage != "") != failure {
				t.Fatalf("closes = %d, terminals = %d, failed = %v", closes, terminals, host.closeMessage != "")
			}
		})
	}
}
