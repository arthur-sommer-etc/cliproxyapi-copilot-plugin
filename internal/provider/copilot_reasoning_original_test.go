package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type originalReasoningHost struct {
	mu       sync.Mutex
	requests []transport.Request
	closed   chan struct{}
	response []byte
}

func (h *originalReasoningHost) Do(_ context.Context, _ string, req transport.Request) (transport.Response, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.requests = append(h.requests, req)
	if h.response != nil {
		return transport.Response{StatusCode: http.StatusOK, Body: h.response}, nil
	}
	return transport.Response{StatusCode: http.StatusOK, Body: []byte(`{"id":"chat_test","object":"chat.completion","model":"test-chat","choices":[{"index":0,"message":{"role":"assistant","content":"answer","reasoning_text":"exact thought","reasoning_opaque":"opaque-state"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`)}, nil
}
func (h *originalReasoningHost) OpenStream(_ context.Context, _ string, req transport.Request) (transport.Stream, error) {
	h.mu.Lock()
	h.requests = append(h.requests, req)
	h.mu.Unlock()
	return transport.Stream{ID: "upstream", StatusCode: http.StatusOK}, nil
}
func (*originalReasoningHost) ReadStream(context.Context, string) (transport.StreamChunk, error) {
	return transport.StreamChunk{Payload: []byte("data: {\"id\":\"chat_test\",\"object\":\"chat.completion.chunk\",\"model\":\"test-chat\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chat_test\",\"object\":\"chat.completion.chunk\",\"model\":\"test-chat\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"), Done: true}, nil
}
func (*originalReasoningHost) CloseStream(context.Context, string) error     { return nil }
func (*originalReasoningHost) Emit(context.Context, string, []byte) error    { return nil }
func (h *originalReasoningHost) CloseOutput(context.Context, string, string) { close(h.closed) }

func TestExecuteRestoresAuthenticatedOriginalReasoning(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "JSON", true: "SSE"}[stream], func(t *testing.T) {
			host := &originalReasoningHost{closed: make(chan struct{})}
			s, storage := newReasoningCarrierService(&reasoningCarrierHost{}, "auth-a", "fixture-credential")
			s.host = host
			initial := []byte(`{"model":"test-chat","input":[{"role":"user","content":"hi"}]}`)
			base := ExecuteRequest{ExecutorRequest: pluginapi.ExecutorRequest{AuthID: "auth-a", SourceFormat: "openai-response", Model: "test-chat", Payload: initial, OriginalRequest: initial, StorageJSON: mustMarshalReasoningStorage(t, storage)}}
			first, err := s.Execute(context.Background(), base)
			if err != nil {
				t.Fatalf("initial inference: %v", err)
			}
			reasoning := gjson.GetBytes(first.Payload, "output.#(type==reasoning)")
			message := gjson.GetBytes(first.Payload, "output.#(type==message)")
			if reasoning.Get("summary.0.text").String() != "exact thought" || !reasoning.Get("encrypted_content").Exists() || !message.Exists() {
				t.Fatalf("missing signed reasoning: %s", first.Payload)
			}
			original := []byte(`{"model":"test-chat","input":[{"role":"user","content":"hi"}]}`)
			original, err = sjson.SetRawBytes(original, "input.-1", []byte(reasoning.Raw))
			if err != nil {
				t.Fatal(err)
			}
			original, err = sjson.SetRawBytes(original, "input.-1", []byte(message.Raw))
			if err != nil {
				t.Fatal(err)
			}
			original, err = sjson.SetRawBytes(original, "input.-1", []byte(`{"role":"user","content":"continue"}`))
			if err != nil {
				t.Fatal(err)
			}
			payload := []byte(`{"model":"test-chat","prompt_cache_key":"host-rule-key","input":[{"role":"user","content":"hi"}]}`)
			payload, err = sjson.SetRawBytes(payload, "input.-1", []byte(message.Raw))
			if err != nil {
				t.Fatal(err)
			}
			payload, err = sjson.SetRawBytes(payload, "input.-1", []byte(`{"role":"user","content":"continue"}`))
			if err != nil {
				t.Fatal(err)
			}
			follow := base
			follow.Payload = payload
			follow.OriginalRequest = original
			follow.StreamID = "out"
			if stream {
				_, err = s.ExecuteStream(context.Background(), follow)
			} else {
				_, err = s.Execute(context.Background(), follow)
			}
			if err != nil {
				t.Fatalf("continuation: %v", err)
			}
			if stream {
				select {
				case <-host.closed:
				case <-time.After(3 * time.Second):
					t.Fatal("stream did not close")
				}
			}
			host.mu.Lock()
			outbound := host.requests[len(host.requests)-1].Body
			host.mu.Unlock()
			assistant := gjson.GetBytes(outbound, `messages.#(role==assistant)`)
			if assistant.Get("reasoning_text").String() != "exact thought" || assistant.Get("reasoning_opaque").String() != "opaque-state" || assistant.Get("reasoning_content").Exists() || gjson.GetBytes(outbound, "prompt_cache_key").String() != "host-rule-key" {
				t.Fatalf("host-normalized payload or reasoning changed: %s", outbound)
			}
			for _, mutate := range []string{"tamper text", "unsealed", "foreign"} {
				t.Run(mutate, func(t *testing.T) {
					bad := append([]byte(nil), original...)
					switch mutate {
					case "tamper text":
						bad, err = sjson.SetBytes(bad, "input.1.summary.0.text", "changed")
					case "unsealed":
						keys, e := continuityKeyMaterialsFor(storage, "auth-a", "test-chat", translate.EndpointChatCompletions, "https://api.example")
						if e != nil {
							t.Fatal(e)
						}
						scope := reasoningCarrierScopeFor("auth-a", "test-chat", translate.EndpointChatCompletions, "https://api.example", keys)
						inner, e := unsealReasoningCarrier(reasoning.Get("encrypted_content").String(), scope)
						if e != nil {
							t.Fatal(e)
						}
						bad, err = sjson.SetBytes(bad, "input.1.encrypted_content", inner)
					case "foreign":
						bad, err = sjson.SetBytes(bad, "input.1.encrypted_content", "foreign-state")
					}
					if err != nil {
						t.Fatal(err)
					}
					host.mu.Lock()
					before := len(host.requests)
					host.mu.Unlock()
					follow.OriginalRequest = bad
					if stream {
						_, err = s.ExecuteStream(context.Background(), follow)
					} else {
						_, err = s.Execute(context.Background(), follow)
					}
					if err == nil {
						t.Fatalf("%s original accepted", mutate)
					}
					host.mu.Lock()
					after := len(host.requests)
					host.mu.Unlock()
					if after != before {
						t.Fatal("invalid original reached inference")
					}
				})
			}
			if !json.Valid(outbound) {
				t.Fatal("outbound request is not JSON")
			}
		})
	}
}

// Exercise the provider's authenticated carrier boundary, including the host's
// normalized payload and original request both retaining the same carrier.
func TestExecuteAuthenticatedReasoningBothFormats(t *testing.T) {
	for _, source := range []string{"openai-response", "claude"} {
		for _, stream := range []bool{false, true} {
			for _, tools := range []bool{false, true} {
				for _, retained := range []bool{false, true} {
					name := fmt.Sprintf("%s/stream=%t/tools=%t/retained=%t", source, stream, tools, retained)
					t.Run(name, func(t *testing.T) {
						host := &originalReasoningHost{closed: make(chan struct{})}
						if tools {
							host.response = []byte(`{"id":"chat_test","object":"chat.completion","model":"test-chat","choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_text":"exact thought","reasoning_opaque":"opaque-state","tool_calls":[{"id":"call_a","type":"function","function":{"name":"first","arguments":"{\"n\":1}"}},{"id":"call_b","type":"function","function":{"name":"second","arguments":"{\"n\":2}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`)
						} else {
							host.response = []byte(`{"id":"chat_test","object":"chat.completion","model":"test-chat","choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_text":"exact thought"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`)
						}
						svc, storage := newReasoningCarrierService(&reasoningCarrierHost{}, "auth-a", "fixture-credential")
						svc.host = host
						initial := []byte(`{"model":"test-chat","input":[{"role":"user","content":"hi"}]}`)
						if source == "claude" {
							initial = []byte(`{"model":"test-chat","max_tokens":128,"messages":[{"role":"user","content":"hi"}]}`)
						}
						req := ExecuteRequest{ExecutorRequest: pluginapi.ExecutorRequest{AuthID: "auth-a", SourceFormat: source, Model: "test-chat", Payload: initial, OriginalRequest: initial, StorageJSON: mustMarshalReasoningStorage(t, storage)}}
						response, err := svc.Execute(context.Background(), req)
						if err != nil {
							t.Fatalf("initial: %v", err)
						}
						var original, stripped []byte
						var tamperPath string
						if source == "openai-response" {
							var all, without []json.RawMessage
							all = append(all, json.RawMessage(`{"role":"user","content":"hi"}`))
							without = append(without, json.RawMessage(`{"role":"user","content":"hi"}`))
							for _, item := range gjson.GetBytes(response.Payload, "output").Array() {
								if item.Get("type").String() == "reasoning" {
									tamperPath = fmt.Sprintf("input.%d.summary.0.text", len(all))
								} else {
									without = append(without, json.RawMessage(item.Raw))
								}
								all = append(all, json.RawMessage(item.Raw))
							}
							if tools {
								for _, id := range []string{"call_a", "call_b"} {
									item := json.RawMessage(fmt.Sprintf(`{"type":"function_call_output","call_id":%q,"output":"done"}`, id))
									all = append(all, item)
									without = append(without, item)
								}
							} else {
								item := json.RawMessage(`{"role":"user","content":"continue"}`)
								all = append(all, item)
								without = append(without, item)
							}
							a, _ := json.Marshal(all)
							b, _ := json.Marshal(without)
							original, err = sjson.SetRawBytes(initial, "input", a)
							if err != nil {
								t.Fatal(err)
							}
							stripped, err = sjson.SetRawBytes(initial, "input", b)
						} else {
							blocks := gjson.GetBytes(response.Payload, "content").Array()
							var without []json.RawMessage
							for i, block := range blocks {
								if block.Get("type").String() == "thinking" {
									tamperPath = fmt.Sprintf("messages.1.content.%d.thinking", i)
								} else {
									without = append(without, json.RawMessage(block.Raw))
								}
							}
							original = []byte(`{"model":"test-chat","max_tokens":128,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[]},{"role":"user","content":"continue"}]}`)
							original, err = sjson.SetRawBytes(original, "messages.1.content", []byte(gjson.GetBytes(response.Payload, "content").Raw))
							if err != nil {
								t.Fatal(err)
							}
							if tools {
								original, err = sjson.SetRawBytes(original, "messages.2.content", []byte(`[{"type":"tool_result","tool_use_id":"call_a","content":"done"},{"type":"tool_result","tool_use_id":"call_b","content":"done"}]`))
								if err != nil {
									t.Fatal(err)
								}
							}
							if len(without) == 0 {
								stripped, err = sjson.DeleteBytes(original, "messages.1")
							} else {
								b, _ := json.Marshal(without)
								stripped, err = sjson.SetRawBytes(original, "messages.1.content", b)
							}
						}
						if err != nil || tamperPath == "" {
							t.Fatalf("fixture: %v response=%s", err, response.Payload)
						}
						req.OriginalRequest = original
						req.Payload = stripped
						if retained {
							req.Payload = original
						}
						req.StreamID = "out"
						if stream {
							_, err = svc.ExecuteStream(context.Background(), req)
						} else {
							_, err = svc.Execute(context.Background(), req)
						}
						if err != nil {
							t.Fatalf("continuation: %v original=%s payload=%s", err, original, req.Payload)
						}
						if stream {
							select {
							case <-host.closed:
							case <-time.After(3 * time.Second):
								t.Fatal("stream did not close")
							}
						}
						host.mu.Lock()
						outbound := host.requests[len(host.requests)-1].Body
						host.mu.Unlock()
						count := 0
						for _, msg := range gjson.GetBytes(outbound, "messages").Array() {
							if msg.Get("role").String() == "assistant" && msg.Get("reasoning_text").Exists() {
								count++
								if msg.Get("reasoning_text").String() != "exact thought" || msg.Get("reasoning_content").Exists() {
									t.Fatalf("bad reasoning: %s", outbound)
								}
								if tools && (msg.Get("reasoning_opaque").String() != "opaque-state" || len(msg.Get("tool_calls").Array()) != 2) {
									t.Fatalf("bad tools: %s", outbound)
								}
							}
						}
						if count != 1 {
							t.Fatalf("got %d reasoning assistants: %s", count, outbound)
						}
						bad, err := sjson.SetBytes(original, tamperPath, "changed")
						if err != nil {
							t.Fatal(err)
						}
						req.Payload = stripped
						req.OriginalRequest = bad
						host.mu.Lock()
						before := len(host.requests)
						host.mu.Unlock()
						if stream {
							_, err = svc.ExecuteStream(context.Background(), req)
						} else {
							_, err = svc.Execute(context.Background(), req)
						}
						if err == nil {
							t.Fatal("changed original thinking accepted")
						}
						host.mu.Lock()
						after := len(host.requests)
						host.mu.Unlock()
						if before != after {
							t.Fatal("tampered original reached host")
						}
					})
				}
			}
		}
	}
}
