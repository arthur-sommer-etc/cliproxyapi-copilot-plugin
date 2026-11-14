package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
	"github.com/tidwall/gjson"
)

var droppedResponsesNativeDeclarations = []string{
	`{"type":"file_search","vector_store_ids":["vs_test"],"max_num_results":5}`,
	`{"type":"code_interpreter","container":{"type":"auto"}}`,
	`{"type":"computer_use_preview","display_width":1024,"display_height":768,"environment":"browser"}`,
	`{"type":"mcp","server_label":"docs","server_url":"https://example.test/mcp"}`,
	`{"type":"tool_search"}`,
	`{"type":"apply_patch"}`,
}

func TestClaudeNativeDeclarationFilterGuards(t *testing.T) {
	for _, typ := range []string{"web_fetch_20250910", "code_execution_20250522", "computer_20250124", "bash_20250124", "text_editor_20250728", "new_native_20990101", " web_fetch_20250910 "} {
		for _, endpoint := range []string{translate.EndpointResponses, translate.EndpointChatCompletions, translate.EndpointMessages} {
			for _, choice := range []string{`{"type":"auto"}`, `{"type":"any"}`, `{"type":"tool","name":"code_execution"}`} {
				for _, client := range []string{"", `,{"type":"function","name":"code_execution","input_schema":{"type":"object"}}`, `,{"type":"custom","name":"code_execution","input_schema":{"type":"object"}}`, `,{"name":"code_execution","input_schema":{"type":"object"}}`} {
					t.Run(fmt.Sprintf("%s/%s/%s/client=%t", typ, endpoint, choice, client != ""), func(t *testing.T) {
						body := []byte(fmt.Sprintf(`{"tools":[{"type":%q,"name":"code_execution","max_uses":2}%s],"tool_choice":%s}`, typ, client, choice))
						filtered, exclusions, err := filterUnrepresentableNativeTools("claude", endpoint, body)
						if endpoint == translate.EndpointMessages {
							if err != nil || len(exclusions) != 0 || string(filtered) != string(body) {
								t.Fatalf("native declaration changed: %s %v %v", filtered, exclusions, err)
							}
							return
						}
						if client == "" && choice != `{"type":"auto"}` {
							var status *StatusError
							if !errors.As(err, &status) || status.Code != "unsupported_native_tool_choice" || status.HTTPStatus != http.StatusUnprocessableEntity {
								t.Fatalf("forced native declaration did not fail coherently: %v", err)
							}
							return
						}
						if err != nil || len(exclusions) != 1 || exclusions[0].Type != strings.TrimSpace(typ) || nativeToolResponseHeaders(http.Header{}, exclusions).Get("X-Copilot-Excluded-Native-Tools") != strings.TrimSpace(typ)+";reason=unrepresentable_by_selected_endpoint" {
							t.Fatalf("missing exclusion contract: %s %v %v", filtered, exclusions, err)
						}
						if client != "" && (len(gjson.GetBytes(filtered, "tools").Array()) != 1 || gjson.GetBytes(filtered, "tools.0").Raw != strings.TrimPrefix(client, ",")) || client == "" && (gjson.GetBytes(filtered, "tools").Exists() || gjson.GetBytes(filtered, "tool_choice").Exists()) {
							t.Fatalf("client collision or empty auto handling changed: %s", filtered)
						}
					})
				}
			}
		}
	}
}

func TestClaudeMalformedToolTypeFilterGuards(t *testing.T) {
	for _, typ := range []string{`null`, `false`, `7`, `{}`, `[]`} {
		for _, endpoint := range []string{translate.EndpointResponses, translate.EndpointChatCompletions} {
			for _, choice := range []string{`{"type":"auto"}`, `{"type":"tool","name":"lookup"}`} {
				body := []byte(fmt.Sprintf(`{"tools":[{"type":%s,"name":"lookup","input_schema":{"type":"object"}}],"tool_choice":%s}`, typ, choice))
				filtered, exclusions, err := filterUnrepresentableNativeTools("claude", endpoint, body)
				if err != nil || len(exclusions) != 0 || string(filtered) != string(body) {
					t.Fatalf("malformed declaration was filtered before validation: %s %v %v", filtered, exclusions, err)
				}
				if _, err := translate.RequestForEndpointFrom("claude", "gpt-test", filtered, false, endpoint); err == nil {
					t.Fatalf("malformed type %s was accepted for choice %s", typ, choice)
				}
			}
		}
	}
}

func TestCapturedCodexCachedSearchFilterPreservesClientToolDeclarations(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "codex-cached-search", "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	filtered, exclusions, err := filterUnrepresentableNativeTools("openai-response", translate.EndpointMessages, body)
	if err != nil || len(exclusions) != 1 || exclusions[0].Type != "web_search" {
		t.Fatalf("captured optional native tool was not excluded: exclusions=%v error=%v", exclusions, err)
	}
	originalTools := gjson.GetBytes(body, "tools").Array()
	filteredTools := gjson.GetBytes(filtered, "tools").Array()
	if len(originalTools) != len(filteredTools)+1 || len(filteredTools) != 8 {
		t.Fatalf("captured tool count changed unexpectedly: original=%d filtered=%d", len(originalTools), len(filteredTools))
	}
	for originalIndex, filteredIndex := 0, 0; originalIndex < len(originalTools); originalIndex++ {
		if originalTools[originalIndex].Get("type").String() == "web_search" {
			continue
		}
		var originalDeclaration, filteredDeclaration map[string]any
		if err := json.Unmarshal([]byte(originalTools[originalIndex].Raw), &originalDeclaration); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(filteredTools[filteredIndex].Raw), &filteredDeclaration); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(originalDeclaration, filteredDeclaration) {
			t.Fatalf("captured client tool changed at original index %d", originalIndex)
		}
		filteredIndex++
	}
	if gjson.GetBytes(filtered, "tools.4.type").String() != "namespace" || nativeToolResponseHeaders(http.Header{}, exclusions).Get("X-Copilot-Excluded-Native-Tools") != "web_search;reason=unrepresentable_by_selected_endpoint" {
		t.Fatalf("captured namespace or exclusion disclosure changed: %s", filtered)
	}
}

func TestClaudeNativeToolExecutorGuards(t *testing.T) {
	for _, endpoint := range []string{translate.EndpointResponses, translate.EndpointChatCompletions} {
		for _, stream := range []bool{false, true} {
			for _, test := range []struct {
				name, payload string
				wantError     bool
			}{
				{"optional", `{"messages":[{"role":"user","content":"Inspect"}],"tools":[{"type":"web_fetch_20250910","name":"web_fetch"},{"type":"code_execution_20250522","name":"code_execution"},{"name":"lookup","input_schema":{"type":"object"}}],"tool_choice":{"type":"auto"}}`, false},
				{"forced", `{"messages":[{"role":"user","content":"Inspect"}],"tools":[{"type":"code_execution_20250522","name":"code_execution"}],"tool_choice":{"type":"tool","name":"code_execution"}}`, true},
				{"required", `{"messages":[{"role":"user","content":"Inspect"}],"tools":[{"type":"code_execution_20250522","name":"code_execution"}],"tool_choice":{"type":"any"}}`, true},
				{"server history", `{"messages":[{"role":"assistant","content":[{"type":"server_tool_use","id":"srvtoolu_guard","name":"code_execution","input":{"code":"canary"}}]},{"role":"user","content":"Continue"}]}`, true},
			} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", endpoint, stream, test.name), func(t *testing.T) {
					responseBody := compactionStreamResponse("completed")
					if endpoint == translate.EndpointChatCompletions {
						responseBody = []byte(`{"id":"chat_guard","model":"gpt-5.6-sol","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}]}`)
					}
					host := &nativeToolHost{compactionStreamHost: newCompactionStreamHost(responseBody), chat: endpoint == translate.EndpointChatCompletions}
					service := newCompactionStreamService(t, host.compactionStreamHost)
					service.host = host
					token := service.tokenEntries["auth-id"]
					if err := service.Configure([]byte("model_endpoint_overrides:\n  gpt-5.6-sol: " + endpoint + "\n")); err != nil {
						t.Fatal(err)
					}
					_, generation := service.configSnapshot()
					token.ConfigGeneration = generation
					service.tokenEntries["auth-id"] = token
					request := compactionStreamRequest([]byte(test.payload), "")
					request.SourceFormat = "claude"
					var headers http.Header
					var err error
					if stream {
						headers, err = service.ExecuteStream(context.Background(), request)
					} else {
						response, executeErr := service.Execute(context.Background(), request)
						headers, err = response.Headers, executeErr
						if !test.wantError && err == nil && response.Metadata["copilot_excluded_native_tools"] == nil {
							t.Fatal("optional server declarations lost exclusion metadata")
						}
					}
					if test.wantError {
						if err == nil || len(host.bodies) != 0 {
							t.Fatalf("guard allowed upstream inference: %v calls=%d", err, len(host.bodies))
						}
						return
					}
					if err != nil || len(host.bodies) != 1 || len(gjson.GetBytes(host.bodies[0], "tools").Array()) != 1 || !strings.Contains(headers.Get("X-Copilot-Excluded-Native-Tools"), "web_fetch_20250910;reason=unrepresentable_by_selected_endpoint") || !strings.Contains(headers.Get("X-Copilot-Excluded-Native-Tools"), "code_execution_20250522;reason=unrepresentable_by_selected_endpoint") {
						t.Fatalf("optional exclusion failed: %v headers=%v bodies=%s", err, headers, host.bodies)
					}
					if stream {
						if _, message := collectCompactionStreamFrames(t, host.compactionStreamHost); message != "" {
							t.Fatal(message)
						}
					}
				})
			}
		}
	}
}

func responsesNativeToolRequest(declarations, choice string, nested bool) []byte {
	input := `"input":"Inspect the supplied material","tools":` + declarations
	if nested {
		input = `"input":[{"type":"message","role":"user","content":"Inspect the supplied material"},{"type":"additional_tools","tools":` + declarations + `}]`
	}
	if choice != "" {
		input += `,"tool_choice":` + choice
	}
	return []byte(`{` + input + `}`)
}

func TestDroppedResponsesNativeToolFilter(t *testing.T) {
	for _, declaration := range droppedResponsesNativeDeclarations {
		typ := gjson.Get(declaration, "type").String()
		for _, endpoint := range []string{translate.EndpointChatCompletions, translate.EndpointMessages} {
			for _, nested := range []bool{false, true} {
				for _, test := range []struct {
					name, choice, retained string
					wantError              bool
				}{
					{name: "optional"},
					{name: "auto", choice: `"auto"`},
					{name: "none", choice: `"none"`},
					{name: "required", choice: `"required"`, wantError: true},
					{name: "forced native", choice: `{"type":"` + typ + `"}`, wantError: true},
					{name: "allowed native", choice: `{"type":"allowed_tools","mode":"auto","tools":[{"type":"` + typ + `"}]}`, wantError: true},
					{name: "forced absent function", choice: `{"type":"function","name":"` + typ + `"}`, wantError: true},
					{name: "forced absent custom", choice: `{"type":"custom","name":"` + typ + `"}`, wantError: true},
					{name: "required with function", choice: `"required"`, retained: `{"type":"function","name":"` + typ + `","description":"ordinary function","parameters":{"type":"object"}}`},
					{name: "forced retained function", choice: `{"type":"function","name":"` + typ + `"}`, retained: `{"type":"function","name":"` + typ + `","parameters":{"type":"object"}}`},
					{name: "forced retained custom", choice: `{"type":"custom","name":"` + typ + `"}`, retained: `{"type":"custom","name":"` + typ + `","format":{"type":"text"}}`},
				} {
					t.Run(fmt.Sprintf("%s/%s/nested=%t/%s", typ, endpoint, nested, test.name), func(t *testing.T) {
						declarations := declaration
						if test.retained != "" {
							declarations += "," + test.retained
						}
						request := responsesNativeToolRequest("["+declarations+"]", test.choice, nested)
						filtered, exclusions, err := filterUnrepresentableNativeTools("openai-response", endpoint, request)
						if (err != nil) != test.wantError {
							t.Fatalf("error=%v, wantError=%t; filtered=%s", err, test.wantError, filtered)
						}
						if err != nil {
							var status *StatusError
							if !errors.As(err, &status) || status.Code != "unsupported_native_tool_choice" || status.HTTPStatus != http.StatusUnprocessableEntity {
								t.Fatalf("incoherent forced choice error: %v", err)
							}
							return
						}
						if len(exclusions) != 1 || exclusions[0].Type != typ || exclusions[0].Reason != "unrepresentable_by_selected_endpoint" {
							t.Fatalf("exclusions=%v; filtered=%s", exclusions, filtered)
						}
						path := "tools"
						if nested {
							path = "input.1.tools"
						}
						tools := gjson.GetBytes(filtered, path).Array()
						if test.retained != "" {
							if len(tools) != 1 || tools[0].Raw != test.retained {
								t.Fatalf("ordinary declaration changed: %s", filtered)
							}
						} else if len(tools) != 0 || test.choice == `"auto"` && gjson.GetBytes(filtered, "tool_choice").Exists() {
							t.Fatalf("unsupported declaration or empty auto choice remains: %s", filtered)
						}
						if got := nativeToolResponseHeaders(http.Header{}, exclusions).Get("X-Copilot-Excluded-Native-Tools"); got != typ+";reason=unrepresentable_by_selected_endpoint" {
							t.Fatalf("exclusion header=%q", got)
						}
						if _, err := translate.RequestForEndpointFrom("openai-response", "gpt-test", filtered, false, endpoint); err != nil {
							t.Fatalf("filtered request is not translatable: %v; body=%s", err, filtered)
						}
					})
				}
			}
		}
	}
}

func TestDroppedResponsesNativeToolsStayNative(t *testing.T) {
	for _, declaration := range droppedResponsesNativeDeclarations {
		typ := gjson.Get(declaration, "type").String()
		for _, nested := range []bool{false, true} {
			request := responsesNativeToolRequest("["+declaration+"]", `{"type":"`+typ+`"}`, nested)
			filtered, exclusions, err := filterUnrepresentableNativeTools("openai-response", translate.EndpointResponses, request)
			if err != nil || len(exclusions) != 0 || string(filtered) != string(request) {
				t.Fatalf("native request changed: body=%s exclusions=%v error=%v", filtered, exclusions, err)
			}
		}
	}
}

func TestResponsesNativeShellCompatibilityFilter(t *testing.T) {
	for _, endpoint := range []string{translate.EndpointResponses, translate.EndpointChatCompletions, translate.EndpointMessages} {
		for _, environment := range []string{`{"type":"local"}`, `{"type":"container_auto"}`, `{"type":"container_reference","container_id":"cntr_test"}`} {
			for _, choice := range []string{`"auto"`, `{"type":"shell"}`} {
				for _, nested := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%s/nested=%t", endpoint, environment, choice, nested), func(t *testing.T) {
						request := responsesNativeToolRequest(`[{"type":"shell","environment":`+environment+`}]`, choice, nested)
						unsupported := endpoint == translate.EndpointMessages || endpoint == translate.EndpointChatCompletions && gjson.Get(environment, "type").String() != "local"
						filtered, exclusions, err := filterUnrepresentableNativeTools("openai-response", endpoint, request)
						if unsupported && choice != `"auto"` {
							if err == nil {
								t.Fatalf("unsupported forced shell accepted: %s", filtered)
							}
							return
						}
						if err != nil {
							t.Fatal(err)
						}
						if unsupported {
							if len(exclusions) != 1 || exclusions[0].Type != "shell" || gjson.GetBytes(filtered, "tool_choice").Exists() {
								t.Fatalf("unsupported shell not excluded: body=%s exclusions=%v", filtered, exclusions)
							}
						} else if len(exclusions) != 0 || string(filtered) != string(request) {
							t.Fatalf("supported shell changed: body=%s exclusions=%v", filtered, exclusions)
						}
						translated, err := translate.RequestForEndpointFrom("openai-response", "gpt-test", filtered, false, endpoint)
						if err != nil {
							t.Fatal(err)
						}
						if endpoint == translate.EndpointChatCompletions && !unsupported && (gjson.GetBytes(translated, "tools.0.function.name").String() != "__cpa_local_shell" || choice != `"auto"` && gjson.GetBytes(translated, "tool_choice.function.name").String() != "__cpa_local_shell") {
							t.Fatalf("SDK local shell mapping lost: %s", translated)
						}
					})
				}
			}
		}
	}
}

func TestResponsesNativeFilterKeepsMappedClaudeWebSearch(t *testing.T) {
	request := []byte(`{"input":"inspect","tools":[{"type":"web_search","max_uses":2,"filters":{"allowed_domains":["docs.example.test"]}},{"type":"file_search","vector_store_ids":["vs_test"]},{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"edit","format":{"type":"text"}}]}],"tool_choice":"required"}`)
	filtered, exclusions, err := filterUnrepresentableNativeTools("openai-response", translate.EndpointMessages, request)
	if err != nil || len(exclusions) != 1 || exclusions[0].Type != "file_search" || gjson.GetBytes(filtered, "tools.0").Raw != gjson.GetBytes(request, "tools.0").Raw || gjson.GetBytes(filtered, "tools.1").Raw != gjson.GetBytes(request, "tools.2").Raw {
		t.Fatalf("supported declarations changed: body=%s exclusions=%v error=%v", filtered, exclusions, err)
	}
	translated, err := translate.RequestForEndpointFrom("openai-response", "gpt-test", filtered, false, translate.EndpointMessages)
	if err != nil || gjson.GetBytes(translated, "tools.0.type").String() != "web_search_20250305" || gjson.GetBytes(translated, "tools.0.max_uses").Int() != 2 || gjson.GetBytes(translated, "tools.0.allowed_domains.0").String() != "docs.example.test" || gjson.GetBytes(translated, "tools.1.name").String() != "functions__edit" || gjson.GetBytes(translated, "tool_choice.type").String() != "any" {
		t.Fatalf("SDK supported mapping changed: output=%s error=%v", translated, err)
	}
}

func TestDroppedResponsesNativeToolExecuteMetadata(t *testing.T) {
	for _, declaration := range droppedResponsesNativeDeclarations {
		typ := gjson.Get(declaration, "type").String()
		for _, endpoint := range []string{translate.EndpointChatCompletions, translate.EndpointMessages} {
			for _, nested := range []bool{false, true} {
				for _, stream := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/nested=%t/stream=%t", typ, endpoint, nested, stream), func(t *testing.T) {
						responseBody := []byte(`{"id":"msg_test","type":"message","role":"assistant","model":"gpt-5.6-sol","content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
						if endpoint == translate.EndpointChatCompletions {
							responseBody = []byte(`{"id":"chat_test","object":"chat.completion","model":"gpt-5.6-sol","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
						}
						host := &nativeToolHost{compactionStreamHost: newCompactionStreamHost(responseBody), messages: endpoint == translate.EndpointMessages, chat: endpoint == translate.EndpointChatCompletions}
						service := newCompactionStreamService(t, host.compactionStreamHost)
						service.host = host
						token := service.tokenEntries["auth-id"]
						if err := service.Configure([]byte("model_endpoint_overrides:\n  gpt-5.6-sol: " + endpoint + "\n")); err != nil {
							t.Fatal(err)
						}
						_, generation := service.configSnapshot()
						token.ConfigGeneration = generation
						service.tokenEntries["auth-id"] = token
						request := compactionStreamRequest(responsesNativeToolRequest("["+declaration+`,{"type":"function","name":"lookup","parameters":{"type":"object"}}]`, `"auto"`, nested), "")
						var headers http.Header
						if stream {
							var err error
							headers, err = service.ExecuteStream(context.Background(), request)
							if err != nil {
								t.Fatal(err)
							}
							if _, message := collectCompactionStreamFrames(t, host.compactionStreamHost); message != "" {
								t.Fatalf("stream failure: %s", message)
							}
							host.mu.Lock()
							closed := len(host.closedStreams)
							host.mu.Unlock()
							if closed != 1 {
								t.Fatalf("closed streams=%d", closed)
							}
						} else {
							response, err := service.Execute(context.Background(), request)
							if err != nil {
								t.Fatal(err)
							}
							headers = response.Headers
							metadata, err := json.Marshal(response.Metadata["copilot_excluded_native_tools"])
							if err != nil || gjson.GetBytes(metadata, "0.type").String() != typ || gjson.GetBytes(metadata, "0.reason").String() != "unrepresentable_by_selected_endpoint" {
								t.Fatalf("metadata=%s error=%v", metadata, err)
							}
						}
						if headers.Get("X-Copilot-Excluded-Native-Tools") != typ+";reason=unrepresentable_by_selected_endpoint" || len(host.bodies) != 1 || len(gjson.GetBytes(host.bodies[0], "tools").Array()) != 1 {
							t.Fatalf("headers=%v dispatched=%s", headers, host.bodies)
						}
					})
				}
			}
		}
	}
}

type nativeToolHost struct {
	*compactionStreamHost
	errors        []string
	bodies        [][]byte
	closedStreams []string
	messages      bool
	chat          bool
}

func (h *nativeToolHost) Do(ctx context.Context, callback string, request transport.Request) (transport.Response, error) {
	if request.Method == http.MethodGet {
		return h.compactionStreamHost.Do(ctx, callback, request)
	}
	h.bodies = append(h.bodies, append([]byte(nil), request.Body...))
	if len(h.bodies) <= len(h.errors) {
		return transport.Response{StatusCode: http.StatusBadRequest, Body: []byte(h.errors[len(h.bodies)-1])}, nil
	}
	return transport.Response{StatusCode: http.StatusOK, Body: h.responseBody}, nil
}

func (h *nativeToolHost) OpenStream(_ context.Context, _ string, request transport.Request) (transport.Stream, error) {
	h.bodies = append(h.bodies, append([]byte(nil), request.Body...))
	status := http.StatusOK
	if len(h.bodies) <= len(h.errors) {
		status = http.StatusBadRequest
	}
	return transport.Stream{ID: fmt.Sprintf("native-%d", len(h.bodies)), StatusCode: status}, nil
}

func (h *nativeToolHost) ReadStream(_ context.Context, id string) (transport.StreamChunk, error) {
	var index int
	if _, err := fmt.Sscanf(id, "native-%d", &index); err != nil {
		return transport.StreamChunk{}, err
	}
	if index <= len(h.errors) {
		return transport.StreamChunk{Payload: []byte(h.errors[index-1]), Done: true}, nil
	}
	if h.chat {
		return transport.StreamChunk{Payload: []byte("data: {\"id\":\"chat_test\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-5.6-sol\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chat_test\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-5.6-sol\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"), Done: true}, nil
	}
	if h.messages {
		return transport.StreamChunk{Payload: []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{" + strings.TrimPrefix(string(h.responseBody), "{") + "}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"), Done: true}, nil
	}
	return transport.StreamChunk{Payload: []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{" + strings.TrimPrefix(string(h.responseBody), "{") + "}\n\n"), Done: true}, nil
}

func (h *nativeToolHost) CloseStream(_ context.Context, id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closedStreams = append(h.closedStreams, id)
	return nil
}

func unsupportedNativeTool(index int) string {
	return fmt.Sprintf(`{"error":{"code":"unsupported_value","type":"invalid_request_error","param":"tools[%d].type","message":"Unsupported tool type"}}`, index)
}

func TestNativeToolUpstreamDoesNotRedispatch(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, test := range []struct {
			name         string
			tools        string
			choice       string
			errors       []string
			wantCalls    int
			wantError    bool
			wantExcluded bool
		}{
			{name: "required without tools fails before inference", tools: `[]`, choice: `,"tool_choice":"required"`, wantCalls: 0, wantError: true},
			{name: "any without tools fails before inference", tools: `[]`, choice: `,"tool_choice":{"type":"any"}`, wantCalls: 0, wantError: true},
			{name: "supported pass through", tools: `[{"type":"web_search","max_uses":2},{"type":"image_generation","size":"1024x1024"}]`, wantCalls: 1},
			{name: "attested unsupported tool does not retry", tools: `[{"type":"web_search"},{"type":"image_generation"},{"type":"function","name":"web_search","parameters":{"type":"object"}}]`, errors: []string{unsupportedNativeTool(0), unsupportedNativeTool(0)}, wantCalls: 1, wantError: true},
			{name: "unknown error unchanged", tools: `[{"type":"web_search"}]`, errors: []string{`{"error":{"code":"invalid_value","param":"tools[0].type"}}`}, wantCalls: 1, wantError: true},
			{name: "unrelated option unchanged", tools: `[{"type":"web_search"}]`, errors: []string{`{"error":{"code":"unsupported_value","param":"tools[0].max_uses"}}`}, wantCalls: 1, wantError: true},
			{name: "forced native fails", tools: `[{"type":"web_search"}]`, choice: `,"tool_choice":{"type":"web_search"}`, errors: []string{unsupportedNativeTool(0)}, wantCalls: 1, wantError: true},
			{name: "required empty fails", tools: `[{"type":"web_search"}]`, choice: `,"tool_choice":"required"`, errors: []string{unsupportedNativeTool(0)}, wantCalls: 1, wantError: true},
			{name: "automatic unsupported does not retry", tools: `[{"type":"web_search"}]`, choice: `,"tool_choice":"auto"`, errors: []string{unsupportedNativeTool(0)}, wantCalls: 1, wantError: true},
			{name: "multiple unsupported tools do not retry", tools: `[{"type":"web_search"},{"type":"web_search_preview"},{"type":"image_generation"}]`, errors: []string{unsupportedNativeTool(0), unsupportedNativeTool(0), unsupportedNativeTool(0)}, wantCalls: 1, wantError: true},
			{name: "function attestation never excludes", tools: `[{"type":"function","name":"web_search","parameters":{"type":"object"}}]`, errors: []string{unsupportedNativeTool(0)}, wantCalls: 1, wantError: true},
		} {
			t.Run(fmt.Sprintf("%s/stream=%t", test.name, streaming), func(t *testing.T) {
				host := &nativeToolHost{compactionStreamHost: newCompactionStreamHost(compactionStreamResponse("completed")), errors: test.errors}
				base := newCompactionStreamService(t, host.compactionStreamHost)
				base.host = host
				request := compactionStreamRequest([]byte(`{"input":"Hello","tools":`+test.tools+test.choice+`}`), "")
				var headers http.Header
				var err error
				if streaming {
					headers, err = base.ExecuteStream(context.Background(), request)
				} else {
					response, executeErr := base.Execute(context.Background(), request)
					headers, err = response.Headers, executeErr
					if err == nil && test.wantExcluded && response.Metadata["copilot_excluded_native_tools"] == nil {
						t.Fatal("missing exclusion metadata")
					}
					if err == nil && string(response.Payload) != string(host.responseBody) {
						t.Fatalf("fabricated response: %s", response.Payload)
					}
				}
				if (err != nil) != test.wantError {
					t.Fatalf("error=%v, wantError=%t", err, test.wantError)
				}
				if len(host.bodies) != test.wantCalls {
					t.Fatalf("calls=%d want %d", len(host.bodies), test.wantCalls)
				}
				if err == nil && test.wantExcluded && headers.Get("X-Copilot-Excluded-Native-Tools") == "" {
					t.Fatal("missing exclusion header")
				}
				if streaming && err == nil {
					_, message := collectCompactionStreamFrames(t, host.compactionStreamHost)
					if message != "" {
						t.Fatalf("stream failure: %s", message)
					}
				}
				if streaming {
					host.mu.Lock()
					closed := len(host.closedStreams)
					host.mu.Unlock()
					if closed != test.wantCalls {
						t.Fatalf("closed %d streams for %d calls", closed, test.wantCalls)
					}
				}
				if test.name == "supported pass through" && gjson.GetBytes(host.bodies[0], "tools").Raw != test.tools {
					t.Fatalf("supported tools changed: %s", host.bodies[0])
				}
			})
		}
	}
}

func TestNativeToolRepresentabilityFilter(t *testing.T) {
	for _, test := range []struct {
		source, endpoint, body string
		want                   int
		wantError              bool
	}{
		{"openai-response", translate.EndpointChatCompletions, `{"tools":[{"type":"web_search"},{"type":"image_generation"},{"type":"function","name":"image_generation"}]}`, 1, false},
		{"openai-response", translate.EndpointMessages, `{"tools":[{"type":"web_search","max_uses":2},{"type":"image_generation"}]}`, 1, false},
		{"openai-response", translate.EndpointMessages, `{"tools":[{"type":"web_search","external_web_access":false}],"tool_choice":"auto"}`, 0, false},
		{"openai-response", translate.EndpointMessages, `{"tools":[{"type":"web_search","external_web_access":false},{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`, 1, false},
		{"openai-response", translate.EndpointMessages, `{"tools":[{"type":"web_search","external_web_access":true}]}`, 1, false},
		{"openai-response", translate.EndpointMessages, `{"tools":[{"type":"web_search","external_web_access":false}],"tool_choice":{"type":"web_search"}}`, 0, true},
		{"openai-response", translate.EndpointMessages, `{"tools":[{"type":"web_search","external_web_access":false}],"tool_choice":{"type":"allowed_tools","mode":"auto","tools":[{"type":"web_search"}]}}`, 0, true},
		{"openai-response", translate.EndpointMessages, `{"tools":[{"type":"web_search","external_web_access":false}],"tool_choice":"required"}`, 0, true},
		{"openai-response", translate.EndpointResponses, `{"tools":[{"type":"web_search"},{"type":"image_generation"}]}`, 2, false},
		{"claude", translate.EndpointResponses, `{"tools":[{"type":"web_search_20250305","name":"web_search"},{"name":"web_search","input_schema":{"type":"object"}}],"tool_choice":{"type":"tool","name":"web_search"}}`, 1, false},
		{"openai-response", translate.EndpointChatCompletions, `{"tools":[{"type":"web_search"}],"tool_choice":"auto"}`, 0, false},
		{"openai-response", translate.EndpointChatCompletions, `{"tools":[{"type":"web_search"}],"tool_choice":"required"}`, 0, true},
		{"openai-response", translate.EndpointChatCompletions, `{"tools":[{"type":"web_search"}],"tool_choice":{"type":"web_search"}}`, 0, true},
		{"openai-response", translate.EndpointChatCompletions, `{"tools":[{"type":"image_generation"},{"type":"function","name":"image_generation","parameters":{"type":"object"}}],"tool_choice":{"type":"function","name":"image_generation"}}`, 1, false},
		{"claude", translate.EndpointChatCompletions, `{"tools":[{"type":"web_search_20250305","name":"web_search"}],"tool_choice":{"type":"tool","name":"web_search"}}`, 0, true},
	} {
		t.Run(test.source+test.endpoint+test.body, func(t *testing.T) {
			body, exclusions, err := filterUnrepresentableNativeTools(test.source, test.endpoint, []byte(test.body))
			if (err != nil) != test.wantError {
				t.Fatalf("error=%v", err)
			}
			if err == nil && len(gjson.GetBytes(body, "tools").Array()) != test.want {
				t.Fatalf("filtered request=%s", body)
			}
			if err == nil && test.want == 0 && gjson.GetBytes(body, "tool_choice").String() == "auto" {
				t.Fatalf("empty automatic choice: %s", body)
			}
			if err == nil && len(exclusions) > 0 {
				headers := nativeToolResponseHeaders(http.Header{}, exclusions)
				if !strings.Contains(headers.Get("X-Copilot-Excluded-Native-Tools"), "reason=unrepresentable_by_selected_endpoint") {
					t.Fatalf("missing explicit reason: %v", headers)
				}
			}
		})
	}
}

func TestNativeToolExclusionExecuteAndStream(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, choice := range []string{`{"type":"auto"}`, `{"type":"any"}`, `{"type":"tool","name":"web_search"}`} {
			t.Run(fmt.Sprintf("stream=%t/choice=%s", streaming, choice), func(t *testing.T) {
				host := &nativeToolHost{compactionStreamHost: newCompactionStreamHost(compactionStreamResponse("completed"))}
				service := newCompactionStreamService(t, host.compactionStreamHost)
				service.host = host
				payload := []byte(`{"messages":[{"role":"user","content":"Hello"}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":2},{"type":"image_generation","name":"image_generation"},{"name":"lookup","input_schema":{"type":"object"}}],"tool_choice":` + choice + `}`)
				request := compactionStreamRequest(payload, "")
				request.SourceFormat = "claude"
				var headers http.Header
				var err error
				if streaming {
					headers, err = service.ExecuteStream(context.Background(), request)
				} else {
					response, executeErr := service.Execute(context.Background(), request)
					headers, err = response.Headers, executeErr
					if err == nil && response.Metadata["copilot_excluded_native_tools"] == nil {
						t.Fatal("missing response exclusion metadata")
					}
					if strings.Contains(string(response.Payload), "web_search_result") || strings.Contains(string(response.Payload), "image_generation_call") {
						t.Fatalf("fabricated native result: %s", response.Payload)
					}
				}
				if choice == `{"type":"tool","name":"web_search"}` {
					if err == nil || !strings.Contains(err.Error(), "forces an unsupported native") || len(host.bodies) != 0 {
						t.Fatalf("forced choice error=%v calls=%d", err, len(host.bodies))
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(host.bodies) != 1 || gjson.GetBytes(host.bodies[0], "tools.0.name").String() != "lookup" || len(gjson.GetBytes(host.bodies[0], "tools").Array()) != 1 {
					t.Fatalf("ordinary tool preservation failed: %s", host.bodies)
				}
				if !strings.Contains(headers.Get("X-Copilot-Excluded-Native-Tools"), "web_search_20250305;reason=unrepresentable_by_selected_endpoint") || !strings.Contains(headers.Get("X-Copilot-Excluded-Native-Tools"), "image_generation;reason=unrepresentable_by_selected_endpoint") {
					t.Fatalf("missing explicit exclusions: %v", headers)
				}
				if streaming {
					frames, message := collectCompactionStreamFrames(t, host.compactionStreamHost)
					if message != "" || len(frames) == 0 {
						t.Fatalf("stream termination: message=%q frames=%d", message, len(frames))
					}
					host.mu.Lock()
					closed := len(host.closedStreams)
					host.mu.Unlock()
					if closed != 1 {
						t.Fatalf("closed %d upstream streams", closed)
					}
				}
			})
		}
	}
}

func TestAdditionalNativeToolsExecuteAndStream(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, choice := range []string{`"auto"`, `"required"`, `{"type":"image_generation"}`, `{"type":"function","name":"image_generation"}`} {
			t.Run(fmt.Sprintf("stream=%t/choice=%s", streaming, choice), func(t *testing.T) {
				host := &nativeToolHost{compactionStreamHost: newCompactionStreamHost([]byte(`{"id":"msg_test","type":"message","role":"assistant","model":"gpt-5.6-sol","content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)), messages: true}
				service := newCompactionStreamService(t, host.compactionStreamHost)
				service.host = host
				token := service.tokenEntries["auth-id"]
				if err := service.Configure([]byte("model_endpoint_overrides:\n  gpt-5.6-sol: /v1/messages\n")); err != nil {
					t.Fatal(err)
				}
				_, generation := service.configSnapshot()
				token.ConfigGeneration = generation
				service.tokenEntries["auth-id"] = token
				payload := []byte(`{"input":[{"type":"message","role":"user","content":"Hello"},{"type":"additional_tools","tools":[{"type":"image_generation","size":"1024x1024"},{"type":"function","name":"image_generation","description":"ordinary function","parameters":{"type":"object"}}]}],"tool_choice":` + choice + `}`)
				request := compactionStreamRequest(payload, "")
				var headers http.Header
				var err error
				if streaming {
					headers, err = service.ExecuteStream(context.Background(), request)
				} else {
					response, executeErr := service.Execute(context.Background(), request)
					headers, err = response.Headers, executeErr
					if err == nil && response.Metadata["copilot_excluded_native_tools"] == nil {
						t.Fatal("missing nested exclusion metadata")
					}
				}
				if choice == `{"type":"image_generation"}` {
					if err == nil || !strings.Contains(err.Error(), "forces an unsupported native") || len(host.bodies) != 0 {
						t.Fatalf("forced nested choice error=%v calls=%d", err, len(host.bodies))
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(host.bodies) != 1 || len(gjson.GetBytes(host.bodies[0], "tools").Array()) != 1 || gjson.GetBytes(host.bodies[0], "tools.0.name").String() != "image_generation" || gjson.GetBytes(host.bodies[0], "tools.0.description").String() != "ordinary function" {
					t.Fatalf("nested ordinary function was lost: %s", host.bodies)
				}
				if !strings.Contains(headers.Get("X-Copilot-Excluded-Native-Tools"), "image_generation;reason=unrepresentable_by_selected_endpoint") {
					t.Fatalf("nested exclusion not disclosed: %v", headers)
				}
				if streaming {
					_, message := collectCompactionStreamFrames(t, host.compactionStreamHost)
					if message != "" {
						t.Fatalf("nested stream failure: %s", message)
					}
					host.mu.Lock()
					closed := len(host.closedStreams)
					host.mu.Unlock()
					if closed != 1 {
						t.Fatalf("closed %d nested upstream streams", closed)
					}
				}
			})
		}
	}
}

func TestAdditionalNativeToolAggregateChoice(t *testing.T) {
	for _, test := range []struct {
		name, body string
		wantError  bool
	}{
		{"absent required", `{"tool_choice":"required"}`, true},
		{"empty any", `{"tools":[],"tool_choice":{"type":"any"}}`, true},
		{"nested required empties", `{"input":[{"type":"additional_tools","tools":[{"type":"image_generation"}]}],"tool_choice":"required"}`, true},
		{"nested auto empties", `{"input":[{"type":"additional_tools","tools":[{"type":"image_generation"}]}],"tool_choice":"auto"}`, false},
		{"top removed nested retained", `{"tools":[{"type":"image_generation"}],"input":[{"type":"additional_tools","tools":[{"type":"function","name":"image_generation","parameters":{"type":"object"}}]}],"tool_choice":{"type":"function","name":"image_generation"}}`, false},
		{"nested removed top retained", `{"tools":[{"type":"function","name":"image_generation","parameters":{"type":"object"}}],"input":[{"type":"additional_tools","tools":[{"type":"image_generation"}]}],"tool_choice":{"type":"function","name":"image_generation"}}`, false},
		{"mixed supported options and order", `{"tools":[{"type":"web_search","max_uses":2},{"type":"function","name":"top","parameters":{"type":"object"}}],"input":[{"type":"additional_tools","tools":[{"type":"function","name":"first","parameters":{"type":"object"}},{"type":"image_generation"},{"type":"function","name":"second","parameters":{"type":"object"}}]}],"tool_choice":"required"}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, exclusions, err := filterUnrepresentableNativeTools("openai-response", translate.EndpointMessages, []byte(test.body))
			if (err != nil) != test.wantError {
				t.Fatalf("choice error=%v, wantError=%t", err, test.wantError)
			}
			if err != nil {
				return
			}
			if len(exclusions) != 1 || exclusions[0].Type != "image_generation" {
				t.Fatalf("exclusions=%v", exclusions)
			}
			if test.name == "nested auto empties" && gjson.GetBytes(body, "tool_choice").Exists() {
				t.Fatalf("empty nested auto choice remained: %s", body)
			}
			if test.name == "mixed supported options and order" && (gjson.GetBytes(body, "tools").Raw != gjson.Get(test.body, "tools").Raw || gjson.GetBytes(body, "input.0.tools.0.name").String() != "first" || gjson.GetBytes(body, "input.0.tools.1.name").String() != "second") {
				t.Fatalf("supported declarations/options/order changed: %s", body)
			}
		})
	}
	for _, body := range []string{`{"tool_choice":"required"}`, `{"tools":[],"tool_choice":"required"}`} {
		if _, _, err := filterUnrepresentableNativeTools("openai-response", translate.EndpointResponses, []byte(body)); err == nil {
			t.Fatalf("native Responses impossible choice accepted: %s", body)
		}
	}
	validNative := `{"tools":[{"type":"web_search","max_uses":2}],"input":[{"type":"additional_tools","tools":[{"type":"image_generation","size":"1024x1024"}]}],"tool_choice":"required"}`
	unchanged, exclusions, err := filterUnrepresentableNativeTools("openai-response", translate.EndpointResponses, []byte(validNative))
	if err != nil || len(exclusions) != 0 || string(unchanged) != validNative {
		t.Fatalf("valid native Responses declarations changed: body=%s exclusions=%v error=%v", unchanged, exclusions, err)
	}
}
