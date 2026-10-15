package provider

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
	"github.com/tidwall/gjson"
)

type chatRequestParityHost struct {
	*compactionStreamHost
	responseBody  []byte
	doCount       int
	openCount     int
	requests      [][]byte
	modelRequests []transport.Request
}

func (h *chatRequestParityHost) Do(ctx context.Context, callback string, request transport.Request) (transport.Response, error) {
	if request.Method == http.MethodGet {
		return h.compactionStreamHost.Do(ctx, callback, request)
	}
	h.doCount++
	h.requests = append(h.requests, append([]byte(nil), request.Body...))
	request.Body = append([]byte(nil), request.Body...)
	request.Headers = request.Headers.Clone()
	h.modelRequests = append(h.modelRequests, request)
	return transport.Response{StatusCode: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: append([]byte(nil), h.responseBody...)}, nil
}

func TestExecutorForwardsBetaHeadersOnlyOnUnchangedNativeResponses(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", streaming), func(t *testing.T) {
			payload := []byte(`{"model":"gpt-5.6-sol","input":"inspect"}`)
			service, host := newChatRequestParityService(t, translate.EndpointResponses, []byte(`{"id":"resp_beta","object":"response","status":"completed","output":[]}`))
			host.allowStream = streaming
			inbound := http.Header{
				"Openai-Beta":   {"responses=beta", "responses-websocket=beta"},
				"Authorization": {"Bearer client-token"},
				"Cookie":        {"synthetic-cookie"},
			}
			before := inbound.Clone()
			request := compactionStreamRequest(payload, "")
			request.Headers = inbound
			if streaming {
				if _, err := service.ExecuteStream(context.Background(), request); err != nil {
					t.Fatalf("execute native Responses stream: %v", err)
				}
				select {
				case <-host.closed:
				case <-time.After(10 * time.Second):
					t.Fatal("native Responses stream did not close")
				}
			} else if _, err := service.Execute(context.Background(), request); err != nil {
				t.Fatalf("execute native Responses request: %v", err)
			}
			if len(host.modelRequests) != 1 {
				t.Fatalf("model request count = %d, want 1", len(host.modelRequests))
			}
			outbound := host.modelRequests[0].Headers
			if got, want := outbound.Values("OpenAI-Beta"), []string{"responses=beta", "responses-websocket=beta"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("outbound beta flags = %#v, want %#v", got, want)
			}
			if outbound.Get("Authorization") == "Bearer client-token" || len(outbound.Values("Cookie")) != 0 {
				t.Fatal("client authorization or cookie was forwarded to Copilot")
			}
			if !reflect.DeepEqual(inbound, before) {
				t.Fatal("inbound request headers were mutated")
			}
		})
	}
}

func (h *chatRequestParityHost) OpenStream(_ context.Context, _ string, request transport.Request) (transport.Stream, error) {
	h.openCount++
	h.requests = append(h.requests, append([]byte(nil), request.Body...))
	request.Body = append([]byte(nil), request.Body...)
	request.Headers = request.Headers.Clone()
	h.modelRequests = append(h.modelRequests, request)
	return transport.Stream{ID: fmt.Sprintf("chat-parity-%d", h.openCount), StatusCode: http.StatusOK}, nil
}

func newChatRequestParityService(t *testing.T, endpoint string, responseBody []byte) (*Service, *chatRequestParityHost) {
	t.Helper()
	baseHost := newCompactionStreamHost(responseBody)
	host := &chatRequestParityHost{compactionStreamHost: baseHost, responseBody: responseBody}
	service := newCompactionStreamService(t, baseHost)
	service.host = host
	entry := service.tokenEntries["auth-id"]
	if err := service.Configure([]byte("model_endpoint_overrides:\n  gpt-5.6-sol: " + endpoint + "\n")); err != nil {
		t.Fatalf("configure Chat parity endpoint: %v", err)
	}
	_, generation := service.configSnapshot()
	entry.ConfigGeneration = generation
	service.tokenEntries["auth-id"] = entry
	return service, host
}

func chatParityRequest(payload []byte) ExecuteRequest {
	request := compactionStreamRequest(payload, "")
	request.SourceFormat = "openai"
	request.Format = ""
	request.Model = "gpt-5.6-sol"
	request.OriginalRequest = append([]byte(nil), payload...)
	request.Payload = append([]byte(nil), payload...)
	return request
}

func TestChatZeroTemperatureIsPreparedBeforeJSONInference(t *testing.T) {
	for _, test := range []struct {
		name         string
		endpoint     string
		payload      string
		responseBody string
		check        func(*testing.T, []byte)
	}{
		{
			name:         "Messages zero temperature",
			endpoint:     translate.EndpointMessages,
			payload:      `{"temperature":0,"messages":[{"role":"user","content":"hello"}]}`,
			responseBody: `{"id":"msg_chat_parity","type":"message","role":"assistant","model":"claude-sonnet-5.5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
			check: func(t *testing.T, body []byte) {
				t.Helper()
				temperature := gjson.GetBytes(body, "temperature")
				if temperature.Type != gjson.Number || temperature.Float() != 0 {
					t.Fatalf("explicit zero Chat temperature was not prepared for Messages: %s", body)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, host := newChatRequestParityService(t, test.endpoint, []byte(test.responseBody))
			if _, err := service.Execute(context.Background(), chatParityRequest([]byte(test.payload))); err != nil {
				t.Fatalf("execute prepared Chat request: %v", err)
			}
			if host.doCount != 1 || host.openCount != 0 || len(host.requests) != 1 {
				t.Fatalf("inference calls: Do=%d OpenStream=%d bodies=%d", host.doCount, host.openCount, len(host.requests))
			}
			test.check(t, host.requests[0])
		})
	}
}

func TestChatRequestPreparationFailuresHaveZeroInferenceDispatch(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, test := range []struct {
			name     string
			endpoint string
			payload  string
		}{
			{
				name:     "maintained SDK audio refusal",
				endpoint: translate.EndpointMessages,
				payload:  `{"messages":[{"role":"system","content":"policy"},{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"UklGRg==","format":"wav"}}]},{"role":"user","content":"another valid turn"}]}`,
			},
			{
				name:     "missing function arguments",
				endpoint: translate.EndpointResponses,
				payload:  `{"messages":[{"role":"user","content":"lookup"},{"role":"assistant","tool_calls":[{"id":"call-missing-arguments","type":"function","function":{"name":"lookup"}}]}]}`,
			},
			{
				name:     "unpaired function result",
				endpoint: translate.EndpointMessages,
				payload:  `{"messages":[{"role":"tool","tool_call_id":"missing-call","content":"found"}]}`,
			},
			{
				name:     "hosted search cannot reach Messages",
				endpoint: translate.EndpointMessages,
				payload:  `{"messages":[{"role":"user","content":"search"}],"web_search_options":{"search_context_size":"low"}}`,
			},
			{
				name:     "unsupported search option",
				endpoint: translate.EndpointResponses,
				payload:  `{"messages":[{"role":"user","content":"search"}],"web_search_options":{"search_context_size":"low","unsupported_option":true}}`,
			},
			{
				name:     "temperature outside Messages range",
				endpoint: translate.EndpointMessages,
				payload:  `{"temperature":1.5,"messages":[{"role":"user","content":"hello"}]}`,
			},
		} {
			t.Run(fmt.Sprintf("%s/stream=%t", test.name, streaming), func(t *testing.T) {
				service, host := newChatRequestParityService(t, test.endpoint, []byte(`{}`))
				request := chatParityRequest([]byte(test.payload))
				var err error
				if streaming {
					_, err = service.ExecuteStream(context.Background(), request)
				} else {
					_, err = service.Execute(context.Background(), request)
				}
				if err == nil {
					t.Fatal("invalid Chat request reached successful inference")
				}
				if host.doCount != 0 || host.openCount != 0 || len(host.requests) != 0 {
					t.Fatalf("invalid request dispatched: Do=%d OpenStream=%d bodies=%d", host.doCount, host.openCount, len(host.requests))
				}
			})
		}
	}
}

func TestChatOfferedHostedSearchHasZeroJSONAndSSEInferenceDispatch(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, endpoint := range []string{translate.EndpointResponses, translate.EndpointMessages} {
			for _, test := range []struct {
				name   string
				fields string
			}{
				{name: "search only", fields: `"web_search_options":{}`},
				{name: "nested approximate location", fields: `"web_search_options":{"search_context_size":"low","user_location":{"type":"approximate","approximate":{"country":"US","city":"Boston","region":"Massachusetts","timezone":"America/New_York"}}}`},
				{name: "search and functions", fields: `"web_search_options":{},"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]`},
				{name: "auto choice", fields: `"web_search_options":{},"tool_choice":"auto"`},
				{name: "required choice", fields: `"web_search_options":{},"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"tool_choice":"required"`},
				{name: "disabled choice", fields: `"web_search_options":{},"tool_choice":"none"`},
				{name: "function choice", fields: `"web_search_options":{},"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"tool_choice":{"type":"function","function":{"name":"lookup"}}`},
			} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", test.name, endpoint, streaming), func(t *testing.T) {
					service, host := newChatRequestParityService(t, endpoint, []byte(`{}`))
					payload := []byte(`{"messages":[{"role":"user","content":"Search before answering."}],` + test.fields + `}`)
					request := chatParityRequest(payload)
					var err error
					if streaming {
						_, err = service.ExecuteStream(context.Background(), request)
					} else {
						_, err = service.Execute(context.Background(), request)
					}
					if err == nil || !strings.Contains(err.Error(), "Chat hosted web search cannot be represented") || !strings.Contains(err.Error(), "response/history") {
						t.Fatalf("offered search did not fail before inference: %v", err)
					}
					if host.doCount != 0 || host.openCount != 0 || len(host.requests) != 0 {
						t.Fatalf("offered search dispatched: Do=%d OpenStream=%d bodies=%d", host.doCount, host.openCount, len(host.requests))
					}
				})
			}
		}
	}
}

var _ transport.Host = (*chatRequestParityHost)(nil)
