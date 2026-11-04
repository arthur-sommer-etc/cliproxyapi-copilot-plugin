package provider

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
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
	header := copilotInferenceHeaders(token, translate.EndpointResponses, []byte(`{"model":"gpt-test","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"inspect"}]}]}`), false, false, []string{"responses=beta", "other=beta"})
	if got, want := header.Values("OpenAI-Beta"), []string{"responses=beta", "other=beta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("OpenAI-Beta values = %#v, want %#v", got, want)
	}
	if header.Get("Authorization") != "Bearer synthetic-token" {
		t.Fatal("inference authorization was not retained")
	}
}

func TestCopilotInferenceHeadersDeriveInitiatorAndVisionFromOutboundBody(t *testing.T) {
	for _, test := range []struct {
		name, endpoint, mode string
		body                 string
		visionSupported      bool
		wantInitiator        string
		wantVision           string
	}{
		{
			name: "chat first user turn", endpoint: translate.EndpointChatCompletions, mode: authModeTokenExchange,
			body:          `{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"Reply with ok."}]}`,
			wantInitiator: "user",
		},
		{
			name: "chat trailing tool result is agent continuation", endpoint: translate.EndpointChatCompletions, mode: authModeTokenExchange,
			body:          `{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"Inspect"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"Read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":[{"type":"text","text":"done"}]}]}`,
			wantInitiator: "agent",
		},
		{
			name: "chat follow-up user turn stays user initiated", endpoint: translate.EndpointChatCompletions, mode: authModeTokenExchange,
			body:          `{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"One"},{"role":"assistant","content":"Two"},{"role":"user","content":"Three"}]}`,
			wantInitiator: "user",
		},
		{
			name: "chat image request carries vision flag", endpoint: translate.EndpointChatCompletions, mode: authModeTokenExchange,
			body:            `{"model":"gemini-3.8-flash","messages":[{"role":"user","content":[{"type":"text","text":"Describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2U=","detail":"high"}}]}]}`,
			visionSupported: true, wantInitiator: "user", wantVision: "true",
		},
		{
			name: "responses user message with omitted type", endpoint: translate.EndpointResponses, mode: authModeTokenExchange,
			body:          `{"input":[{"role":"user","content":"hello"}]}`,
			wantInitiator: "user",
		},
		{
			name: "responses explicit user message", endpoint: translate.EndpointResponses, mode: authModeTokenExchange,
			body:          `{"input":[{"type":"message","role":"user","content":"hello"}]}`,
			wantInitiator: "user",
		},
		{
			name: "responses reasoning continuation is agent", endpoint: translate.EndpointResponses, mode: authModeTokenExchange,
			body:          `{"model":"gpt-6-luna","input":[{"type":"reasoning","encrypted_content":"cpa-copilot-reasoning:v1:e30"},{"type":"function_call","call_id":"call_1","name":"Read","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"done"}]}`,
			wantInitiator: "agent",
		},
		{
			name: "responses tool output image carries vision flag", endpoint: translate.EndpointResponses, mode: authModeTokenExchange,
			body:            `{"model":"gpt-6-luna","input":[{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_image","image_url":"data:image/png;base64,aW1hZ2U="}]}]}`,
			visionSupported: true, wantInitiator: "agent", wantVision: "true",
		},
		{
			name: "messages tool result image carries vision flag", endpoint: translate.EndpointMessages, mode: authModeTokenExchange,
			body:            `{"model":"claude-haiku-5.5","messages":[{"role":"user","content":[{"type":"text","text":"Inspect"}]},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"a.png"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}}]}]}]}`,
			visionSupported: true, wantInitiator: "agent", wantVision: "true",
		},
		{
			name: "vision capability gates the vision flag", endpoint: translate.EndpointChatCompletions, mode: authModeTokenExchange,
			body:          `{"model":"gemini-3.8-flash","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2U="}}]}]}`,
			wantInitiator: "user",
		},
		{
			name: "direct oauth keeps its minimal header policy", endpoint: translate.EndpointChatCompletions, mode: authModeDirectOAuth,
			body: `{"model":"gemini-3.8-flash","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2U="}}]},{"role":"tool","content":"done"}]}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			token := copilotTokenEntry{Mode: test.mode, Token: "synthetic-token"}
			headers := copilotInferenceHeaders(token, test.endpoint, []byte(test.body), false, test.visionSupported, nil)
			if got := headers.Get("X-Initiator"); got != test.wantInitiator {
				t.Fatalf("X-Initiator = %q, want %q", got, test.wantInitiator)
			}
			if got := headers.Get("Copilot-Vision-Request"); got != test.wantVision {
				t.Fatalf("Copilot-Vision-Request = %q, want %q", got, test.wantVision)
			}
		})
	}
}

func TestCopilotInferenceDispatchKeepsDerivedHeadersAcrossRefresh(t *testing.T) {
	for _, operation := range []string{"json", "stream"} {
		t.Run(operation, func(t *testing.T) {
			host := &refreshTestHost{}
			service := New(host)
			storage := authStorage{GitHubAccessToken: "synthetic-github-token"}
			token := copilotTokenEntry{Mode: authModeTokenExchange, Token: "copilot-initial", APIBaseURL: "https://api.example", ExpiresAt: time.Now().Add(time.Hour), Fingerprint: tokenFingerprint(storage.GitHubAccessToken)}
			body := []byte(`{"model":"gemini-3.8-flash","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2U="}}]},{"role":"assistant","content":"reading"},{"role":"tool","tool_call_id":"call_1","content":[{"type":"text","text":"done"}]}]}`)
			if operation == "stream" {
				host.streamStatuses = []int{http.StatusUnauthorized, http.StatusOK}
				if _, _, err := service.openModelStream(context.Background(), "callback", "auth-id", storage, token, translate.EndpointChatCompletions, body, true); err != nil {
					t.Fatal(err)
				}
			} else {
				host.modelStatuses = []int{http.StatusUnauthorized, http.StatusOK}
				if _, _, err := service.doModelRequest(context.Background(), "callback", "auth-id", storage, token, translate.EndpointChatCompletions, body, false, true); err != nil {
					t.Fatal(err)
				}
			}
			requests := host.modelRequests
			if operation == "stream" {
				requests = host.streamRequests
			}
			if len(requests) != 2 {
				t.Fatalf("dispatched %d requests, want the initial call and one refresh retry", len(requests))
			}
			for index, request := range requests {
				if request.Headers.Get("X-Initiator") != "agent" || request.Headers.Get("Copilot-Vision-Request") != "true" {
					t.Fatalf("request %d derived headers = X-Initiator %q vision %q", index, request.Headers.Get("X-Initiator"), request.Headers.Get("Copilot-Vision-Request"))
				}
				if !bytes.Equal(request.Body, body) {
					t.Fatalf("request %d body changed during dispatch", index)
				}
			}
		})
	}
}

type nativeBetaRefreshHost struct {
	refreshTestHost
	closed chan string
}

func (h *nativeBetaRefreshHost) Do(ctx context.Context, callback string, request transport.Request) (transport.Response, error) {
	if request.Method == http.MethodGet && strings.HasSuffix(request.URL, "/models") {
		return transport.Response{StatusCode: http.StatusOK, Body: []byte(`{"data":[{"id":"gpt-5.6-sol","model_picker_enabled":true,"policy":{"state":"enabled"},"capabilities":{"type":"chat"},"supported_endpoints":["/responses"]}]}`)}, nil
	}
	response, err := h.refreshTestHost.Do(ctx, callback, request)
	if err == nil && response.StatusCode == http.StatusOK && strings.HasSuffix(request.URL, "/responses") {
		response.Body = []byte(`{"id":"resp_beta","object":"response","status":"completed","output":[]}`)
	}
	return response, err
}

func (h *nativeBetaRefreshHost) ReadStream(context.Context, string) (transport.StreamChunk, error) {
	return transport.StreamChunk{
		Payload: []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_beta\",\"status\":\"completed\",\"output\":[]}}\n\n"),
		Done:    true,
	}, nil
}

func (h *nativeBetaRefreshHost) CloseOutput(_ context.Context, _ string, message string) {
	h.closed <- message
}

func TestExecutorPreservesEligibleBetaHeadersAcrossTokenRefresh(t *testing.T) {
	const payload = `{"model":"gpt-5.6-sol","input":"inspect"}`
	for _, streaming := range []bool{false, true} {
		for _, test := range []struct {
			name     string
			source   string
			payload  string
			original string
			wantBeta []string
		}{
			{name: "unchanged native model", original: payload, wantBeta: []string{"responses=beta", "responses-websocket=beta", "responses=beta"}},
			{name: "transformed model", original: `{"model":"client-alias","input":"inspect"}`},
			{name: "missing original request"},
			{name: "Messages to Responses", source: "claude", payload: `{"model":"gpt-5.6-sol","max_tokens":32,"messages":[{"role":"user","content":"inspect"}]}`, original: `{"model":"gpt-5.6-sol","max_tokens":32,"messages":[{"role":"user","content":"inspect"}]}`},
		} {
			t.Run(fmt.Sprintf("%s/stream=%t", test.name, streaming), func(t *testing.T) {
				host := &nativeBetaRefreshHost{closed: make(chan string, 1)}
				service := newCompactionStreamService(t, newCompactionStreamHost(nil))
				service.host = host
				inbound := http.Header{
					"Openai-Beta":           {"responses=beta", "responses-websocket=beta", "responses=beta"},
					"Anthropic-Beta":        {"synthetic-claude-beta"},
					"Authorization":         {"Bearer synthetic-client-token"},
					"Cookie":                {"synthetic-cookie"},
					"X-Github-Token":        {"synthetic-github-header"},
					"Copilot-Session-Token": {"synthetic-session-token"},
					"Session-Id":            {"synthetic-client-session"},
					"X-Agent-Task-Id":       {"synthetic-client-task"},
					"X-Request-Id":          {"synthetic-client-request"},
				}
				before := inbound.Clone()
				requestPayload := payload
				if test.payload != "" {
					requestPayload = test.payload
				}
				request := compactionStreamRequest([]byte(requestPayload), "")
				if test.source != "" {
					request.SourceFormat = test.source
				}
				request.Headers = inbound
				request.OriginalRequest = []byte(test.original)
				if streaming {
					host.streamStatuses = []int{http.StatusUnauthorized, http.StatusOK}
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
				} else {
					host.modelStatuses = []int{http.StatusUnauthorized, http.StatusOK}
					if _, err := service.Execute(context.Background(), request); err != nil {
						t.Fatalf("execute JSON: %v", err)
					}
				}
				host.mu.Lock()
				requests := append([]transport.Request(nil), host.modelRequests...)
				if streaming {
					requests = append([]transport.Request(nil), host.streamRequests...)
				}
				tokenCalls := host.copilotTokenCalls
				host.mu.Unlock()
				if len(requests) != 2 || tokenCalls != 1 {
					t.Fatalf("model requests = %d, token exchanges = %d; want 2 and 1", len(requests), tokenCalls)
				}
				for index, outbound := range requests {
					if !strings.HasSuffix(outbound.URL, "/responses") || !bytes.Equal(outbound.Body, requests[0].Body) {
						t.Fatalf("request %d changed endpoint or body during token refresh", index)
					}
					if got := outbound.Headers.Values("OpenAI-Beta"); !reflect.DeepEqual(got, test.wantBeta) {
						t.Fatalf("request %d beta flags = %#v, want %#v", index, got, test.wantBeta)
					}
					for _, name := range []string{"Anthropic-Beta", "Cookie", "X-Github-Token", "Copilot-Session-Token", "Session-Id"} {
						if len(outbound.Headers.Values(name)) != 0 {
							t.Fatalf("request %d forwarded inbound %s", index, name)
						}
					}
					for _, name := range []string{"X-Agent-Task-Id", "X-Request-Id"} {
						if got := outbound.Headers.Get(name); got == "" || got == inbound.Get(name) {
							t.Fatalf("request %d %s was not generated independently of the caller", index, name)
						}
					}
				}
				if got := requests[0].Headers.Get("Authorization"); got != "Bearer copilot-token" {
					t.Fatalf("initial authorization = %q, want Copilot token", got)
				}
				if got := requests[1].Headers.Get("Authorization"); got != "Bearer copilot-token-1" {
					t.Fatalf("retry authorization = %q, want refreshed Copilot token", got)
				}
				if !reflect.DeepEqual(inbound, before) {
					t.Fatal("inbound request headers were mutated")
				}
			})
		}
	}
}

func executorRequestForBetaTest(model, original string, headers http.Header) pluginapi.ExecutorRequest {
	return pluginapi.ExecutorRequest{Model: model, OriginalRequest: []byte(original), Headers: headers}
}
