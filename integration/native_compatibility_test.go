package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestNativeHostToolAndSystemCompatibility(t *testing.T) {
	binary := os.Getenv("CPA_BINARY")
	if binary == "" {
		t.Skip("set CPA_BINARY to the prepared CLIProxyAPI v8 server for native host integration")
	}
	state := newNativeFixture(t)
	upstream := httptest.NewServer(state)
	t.Cleanup(upstream.Close)
	base := startProxy(t, binary, upstream.URL)
	t.Run("HostedSearchReplay", func(t *testing.T) {
		const query = "  exact fixture query\n\t "
		const searchID = "srvtoolu_fixture_native_replay"
		for _, stream := range []bool{false, true} {
			for _, results := range []any{
				[]any{map[string]any{"type": "web_search_result", "url": "https://example.test/report?source=fixture", "title": "Fixture report", "encrypted_content": "fixture_opaque_replay_token"}},
				[]any{},
				map[string]any{"type": "web_search_tool_result_error", "error_code": "max_uses_exceeded"},
			} {
				t.Run(fmt.Sprintf("Stream%t/%T/%v", stream, results, results), func(t *testing.T) {
					search := map[string]any{"type": "web_search_call", "id": "ws_" + searchID, "status": "completed", "action": map[string]any{"type": "search", "query": query}, "results": results}
					request := map[string]any{"model": "claude-sonnet-5.5", "stream": stream, "input": []any{
						map[string]any{"type": "message", "role": "user", "content": "Search."},
						search,
						map[string]any{"type": "message", "role": "user", "content": "Continue without another search."},
					}}
					before := upstreamRequestCount(state)
					status, response := postProxyWithSession(t, base+"/v1/responses", request, "fixture-hosted-search-replay")
					if status != http.StatusOK || upstreamRequestCount(state) != before+1 {
						t.Fatalf("replay did not reach the fixture exactly once: status=%d body=%s", status, response)
					}
					captured, path := lastUpstreamRequest(t, state)
					wantMessages := []any{
						map[string]any{"role": "user", "content": "Search."},
						map[string]any{"role": "assistant", "content": []any{
							map[string]any{"type": "server_tool_use", "id": searchID, "name": "web_search", "input": map[string]any{"query": query}},
							map[string]any{"type": "web_search_tool_result", "tool_use_id": searchID, "content": results},
						}},
						map[string]any{"role": "user", "content": "Continue without another search."},
					}
					if path != "/v1/messages" || captured["stream"] != stream {
						t.Fatalf("replay destination changed: path=%q request=%v", path, captured)
					}
					assertEqualJSON(t, captured["messages"], wantMessages)
					for _, field := range []string{"id", "status", "action", "results"} {
						original := search[field]
						switch field {
						case "id":
							search[field] = "ws_fixture_native_search"
						case "status":
							search[field] = "in_progress"
						case "action":
							search[field] = map[string]any{"type": "search", "query": query, "queries": []string{"one", "two"}}
						case "results":
							delete(search, field)
						}
						before := upstreamRequestCount(state)
						status, response := postProxyWithSession(t, base+"/v1/responses", request, "fixture-hosted-search-rejected")
						if status != http.StatusUnprocessableEntity || !bytes.Contains(response, []byte("web search")) || upstreamRequestCount(state) != before {
							t.Fatalf("lossy %s dispatched inference: status=%d body=%s", field, status, response)
						}
						search[field] = original
					}
				})
			}
		}
	})
	t.Run("ExclusionHeader", func(t *testing.T) {
		body, err := json.Marshal(map[string]any{"model": "claude-sonnet-5.5", "input": "Say hello.", "tools": []any{map[string]any{"type": "image_generation"}}})
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequest(http.MethodPost, base+"/v1/responses", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer fixture-client-key")
		request.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: 10 * time.Second}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if _, err := io.Copy(io.Discard, response.Body); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK || !strings.Contains(response.Header.Get("X-Copilot-Excluded-Native-Tools"), "image_generation;reason=unrepresentable_by_selected_endpoint") {
			t.Fatalf("degradation disclosure was not forwarded: status=%d headers=%v", response.StatusCode, response.Header)
		}
	})
	t.Run("ClaudeSafeguardsClientError", func(t *testing.T) {
		const opaqueContext = "OPAQUE-CLASSIFIER-CONTEXT-SECRET"
		for _, test := range []struct {
			name   string
			stream bool
		}{
			{name: "JSON"},
			{name: "SSE", stream: true},
		} {
			test := test
			t.Run(test.name, func(t *testing.T) {
				before := upstreamRequestCount(state)
				request := map[string]any{
					"model":      "claude-sonnet-5.5",
					"stream":     test.stream,
					"max_tokens": 64,
					"messages":   []any{map[string]any{"role": "user", "content": "Say hello."}},
					"safeguards": []any{map[string]any{"type": "dangerous_tool_use", "classifier_context": opaqueContext}},
				}
				status, response := postProxyWithSession(t, base+"/v1/messages", request, "claude-safeguards-client-error-"+test.name)
				if status != http.StatusBadRequest || !strings.Contains(strings.ToLower(string(response)), "safeguards") {
					t.Fatalf("Claude safeguard response = %d %s, want static safeguards 400", status, response)
				}
				if bytes.Contains(response, []byte(opaqueContext)) {
					t.Fatalf("Claude safeguard response leaked classifier context: %s", response)
				}
				if after := upstreamRequestCount(state); after != before {
					t.Fatalf("rejected Claude safeguard dispatched %d upstream inference requests", after-before)
				}
			})
		}
	})
	t.Run("ClaudeDisableParallelToolUse", func(t *testing.T) {
		targets := []struct{ name, model, path string }{
			{"Responses", "gpt-6-luna", "/responses"},
			{"Chat", "gemini-3.8-flash", "/chat/completions"},
		}
		choices := []struct{ name, kind string }{{"Any", "any"}, {"Auto", "auto"}, {"Forced", "tool"}}
		parallelModes := []struct {
			name    string
			present bool
			value   bool
		}{{"Disabled", true, true}, {"Enabled", true, false}, {"Omitted", false, false}}
		parameters := map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Lookup key."},
			},
			"required":             []any{"query"},
			"additionalProperties": false,
		}
		for _, target := range targets {
			for _, stream := range []bool{false, true} {
				for _, parallel := range parallelModes {
					for _, choice := range choices {
						t.Run(fmt.Sprintf("%s/%t/%s/%s", target.name, stream, parallel.name, choice.name), func(t *testing.T) {
							toolChoice := map[string]any{"type": choice.kind}
							if choice.kind == "tool" {
								toolChoice["name"] = "lookup"
							}
							if parallel.present {
								toolChoice["disable_parallel_tool_use"] = parallel.value
							}
							request := map[string]any{
								"model":      target.model,
								"stream":     stream,
								"max_tokens": 64,
								"system":     "Synthetic root system instruction.",
								"messages": []any{
									map[string]any{"role": "user", "content": "Synthetic user message before the mid-conversation instruction."},
									map[string]any{"role": "system", "content": "Synthetic mid-conversation system instruction."},
									map[string]any{"role": "user", "content": "Synthetic user message after the mid-conversation instruction."},
								},
								"tools":       []any{map[string]any{"name": "lookup", "description": "Look up a synthetic value.", "input_schema": parameters}},
								"tool_choice": toolChoice,
							}
							before := upstreamRequestCount(state)
							status, _, body := postNativeCompatibilityRequest(t, base+"/v1/messages", request, t.Name())
							if status != http.StatusOK {
								t.Fatalf("valid disable_parallel_tool_use request returned %d: %s", status, body)
							}
							if after := upstreamRequestCount(state); after != before+1 {
								t.Fatalf("valid request dispatched %d synthetic inference requests, want exactly one", after-before)
							}
							captured, path := lastUpstreamRequest(t, state)
							if path != target.path || captured["model"] != target.model || captured["stream"] != stream {
								t.Fatalf("request routed to path=%q model=%v stream=%v, want %q model=%q stream=%t", path, captured["model"], captured["stream"], target.path, target.model, stream)
							}
							if parallel.present {
								if got, ok := captured["parallel_tool_calls"].(bool); !ok || got != !parallel.value {
									t.Fatalf("parallel_tool_calls = %v, want %t for disable_parallel_tool_use=%t", captured["parallel_tool_calls"], !parallel.value, parallel.value)
								}
							} else if _, exists := captured["parallel_tool_calls"]; exists {
								t.Fatalf("omitted disable_parallel_tool_use added parallel_tool_calls=%v", captured["parallel_tool_calls"])
							}
							wantChoice := any("auto")
							switch choice.kind {
							case "any":
								wantChoice = "required"
							case "tool":
								if target.path == "/responses" {
									wantChoice = map[string]any{"type": "function", "name": "lookup"}
								} else {
									wantChoice = map[string]any{"type": "function", "function": map[string]any{"name": "lookup"}}
								}
							}
							if target.model == "gemini-3.8-flash" && choice.kind != "tool" {
								wantChoice = "validated"
							}
							assertEqualJSON(t, captured["tool_choice"], wantChoice)
							tools, ok := captured["tools"].([]any)
							if !ok || len(tools) != 1 {
								t.Fatalf("translated tool declaration changed: %+v", captured["tools"])
							}
							tool := nativeMap(t, tools[0])
							if target.path == "/chat/completions" {
								tool = nativeMap(t, tool["function"])
							}
							if tool["name"] != "lookup" {
								t.Fatalf("translated tool name changed: %+v", tool)
							}
							assertEqualJSON(t, tool["parameters"], parameters)
							if target.path == "/responses" {
								input, ok := captured["input"].([]any)
								if captured["instructions"] != "Synthetic root system instruction." || !ok || len(input) != 3 || nativeMap(t, input[0])["role"] != "user" || !containsJSONScalar(input[0], "Synthetic user message before the mid-conversation instruction.") || nativeMap(t, input[1])["role"] != "system" || !containsJSONScalar(input[1], "Synthetic mid-conversation system instruction.") || nativeMap(t, input[2])["role"] != "user" || !containsJSONScalar(input[2], "Synthetic user message after the mid-conversation instruction.") {
									t.Fatalf("Responses request changed root or ordered mid-conversation instructions: %+v", captured)
								}
							} else {
								messages, ok := captured["messages"].([]any)
								if !ok || len(messages) != 4 || nativeMap(t, messages[0])["role"] != "system" || !containsJSONScalar(messages[0], "Synthetic root system instruction.") || nativeMap(t, messages[1])["role"] != "user" || !containsJSONScalar(messages[1], "Synthetic user message before the mid-conversation instruction.") || nativeMap(t, messages[2])["role"] != "system" || !containsJSONScalar(messages[2], "Synthetic mid-conversation system instruction.") || nativeMap(t, messages[3])["role"] != "user" || !containsJSONScalar(messages[3], "Synthetic user message after the mid-conversation instruction.") {
									t.Fatalf("Chat request changed root or ordered mid-conversation instructions: %+v", captured)
								}
							}
						})
					}
				}
			}
		}
		for _, malformed := range []struct {
			name  string
			value any
		}{
			{"String", "MALFORMED_PARALLEL_STRING_SECRET"},
			{"Null", nil},
			{"Number", 7},
			{"Object", map[string]any{"secret": "MALFORMED_PARALLEL_OBJECT_SECRET"}},
		} {
			for _, stream := range []bool{false, true} {
				for _, target := range targets {
					t.Run(fmt.Sprintf("Malformed/%s/%s/%t", malformed.name, target.name, stream), func(t *testing.T) {
						request := map[string]any{
							"model":      target.model,
							"stream":     stream,
							"max_tokens": 64,
							"system":     "PRIVATE_ROOT_SYSTEM_CONTENT_DO_NOT_LEAK",
							"messages": []any{
								map[string]any{"role": "user", "content": "PRIVATE_USER_CONTENT_DO_NOT_LEAK"},
								map[string]any{"role": "system", "content": "PRIVATE_MID_SYSTEM_CONTENT_DO_NOT_LEAK"},
								map[string]any{"role": "user", "content": "Continue without exposing request contents."},
							},
							"tools":       []any{map[string]any{"name": "lookup", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}}}},
							"tool_choice": map[string]any{"type": "auto", "disable_parallel_tool_use": malformed.value},
						}
						before := upstreamRequestCount(state)
						status, headers, body := postNativeCompatibilityRequest(t, base+"/v1/messages", request, t.Name())
						if after := upstreamRequestCount(state); after != before {
							t.Fatalf("malformed disable_parallel_tool_use dispatched %d synthetic inference requests", after-before)
						}
						assertNativeCompatibilityClientError(t, status, headers, body, "tool_choice", "disable_parallel_tool_use")
						for _, privateContent := range []string{
							"PRIVATE_ROOT_SYSTEM_CONTENT_DO_NOT_LEAK",
							"PRIVATE_USER_CONTENT_DO_NOT_LEAK",
							"PRIVATE_MID_SYSTEM_CONTENT_DO_NOT_LEAK",
							"MALFORMED_PARALLEL_STRING_SECRET",
							"MALFORMED_PARALLEL_OBJECT_SECRET",
						} {
							if bytes.Contains(body, []byte(privateContent)) {
								t.Fatalf("malformed request response leaked content %q: %s", privateContent, body)
							}
						}
					})
				}
			}
		}
	})
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("Stream%v", stream), func(t *testing.T) {
			for _, model := range []string{"claude-sonnet-5.5", "gemini-3.8-flash"} {
				before := upstreamRequestCount(state)
				request := map[string]any{"model": model, "stream": stream, "input": "Draw a circle.", "tools": []any{map[string]any{"type": "image_generation"}}}
				status, response := postProxyWithSession(t, base+"/v1/responses", request, fmt.Sprintf("native-image-unsupported-%s-%t", model, stream))
				if status != http.StatusOK {
					t.Fatalf("unsupported image tool returned %d: %s", status, response)
				}
				if after := upstreamRequestCount(state); after != before+1 {
					t.Fatalf("degraded request dispatched %d inference requests, want exactly one", after-before)
				}
				captured, _ := lastUpstreamRequest(t, state)
				if tools, exists := captured["tools"]; exists {
					array, ok := tools.([]any)
					if !ok || len(array) != 0 {
						t.Fatalf("unrepresentable native image tool reached upstream: %+v", captured)
					}
				}
			}
			before := upstreamRequestCount(state)
			nested := map[string]any{"model": "claude-sonnet-5.5", "stream": stream, "input": []any{
				map[string]any{"type": "message", "role": "user", "content": "Say hello."},
				map[string]any{"type": "additional_tools", "tools": []any{
					map[string]any{"type": "image_generation"},
					map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}},
				}},
			}}
			status, response := postProxyWithSession(t, base+"/v1/responses", nested, fmt.Sprintf("native-nested-tools-%t", stream))
			if status != http.StatusOK {
				t.Fatalf("nested tool degradation returned %d: %s", status, response)
			}
			if after := upstreamRequestCount(state); after != before+1 {
				t.Fatalf("nested degraded request dispatched %d inference requests, want exactly one", after-before)
			}
			captured, path := lastUpstreamRequest(t, state)
			tools, ok := captured["tools"].([]any)
			if path != "/v1/messages" || !ok || len(tools) != 1 || nativeMap(t, tools[0])["name"] != "lookup" {
				t.Fatalf("nested supported function was not preserved: path=%q request=%+v", path, captured)
			}
			preview := map[string]any{"model": "claude-sonnet-5.5", "stream": stream, "input": "Search for a circle.", "tools": []any{map[string]any{"type": "web_search_preview"}}}
			status, response = postProxyWithSession(t, base+"/v1/responses", preview, fmt.Sprintf("native-search-preview-%t", stream))
			if status != http.StatusOK {
				t.Fatalf("legacy search translation returned %d: %s", status, response)
			}
			captured, path = lastUpstreamRequest(t, state)
			tools, ok = captured["tools"].([]any)
			if path != "/v1/messages" || !ok || len(tools) != 1 || nativeMap(t, tools[0])["type"] != "web_search_20250305" {
				t.Fatalf("legacy search tool did not retain native server-tool semantics: path=%q request=%+v", path, captured)
			}
			native := map[string]any{"model": "gpt-6-luna", "stream": stream, "input": "Draw a circle.", "tools": []any{map[string]any{"type": "image_generation", "quality": "low"}}}
			status, response = postProxyWithSession(t, base+"/v1/responses", native, fmt.Sprintf("native-image-pass-through-%t", stream))
			if status != http.StatusOK {
				t.Fatalf("native Responses pass-through returned %d: %s", status, response)
			}
			captured, path = lastUpstreamRequest(t, state)
			if path != "/responses" {
				t.Fatalf("native image tool reached %q, want Responses", path)
			}
			assertEqualJSON(t, captured["tools"], native["tools"])
			request := map[string]any{"model": "gpt-6-luna", "stream": stream, "max_tokens": 64, "messages": []any{map[string]any{"role": "system", "content": "Keep this system instruction.", "output_config": map[string]any{"effort": "high"}}, map[string]any{"role": "user", "content": "Say hello."}}}
			status, response = postProxyWithSession(t, base+"/v1/messages", request, fmt.Sprintf("native-system-effort-%t", stream))
			if status != http.StatusOK {
				t.Fatalf("system content with effort returned %d: %s", status, response)
			}
			captured, path = lastUpstreamRequest(t, state)
			if path != "/responses" || nativeMap(t, captured["reasoning"])["effort"] != "high" {
				t.Fatalf("system effort did not reach Responses: path=%q request=%+v", path, captured)
			}
			input, ok := captured["input"].([]any)
			if !ok || len(input) < 1 || nativeMap(t, input[0])["role"] != "system" || !containsJSONScalar(input[0], "Keep this system instruction.") {
				t.Fatalf("system authority or text was lost: %+v", captured)
			}
		})
	}
	t.Run("ConfiguredAbsentOverrideRoute", func(t *testing.T) {
		model := "fixture-configured-route"
		state := newNativeFixture(t)
		if bytes.Contains(state.modelCatalog, []byte(model)) {
			t.Fatalf("synthetic Copilot catalog unexpectedly contains configured model %q", model)
		}
		upstream := httptest.NewServer(state)
		defer upstream.Close()
		base := startProxyWithConfiguredEndpointOverride(t, binary, upstream.URL, model, "/responses")
		before := upstreamRequestCount(state)
		callProxyWithSession(t, base+"/v1/responses", map[string]any{"model": model, "input": "Use the configured route."}, "configured-absent-override")
		if after := upstreamRequestCount(state); after != before+1 {
			t.Fatalf("configured absent model dispatched %d inference requests, want one", after-before)
		}
		captured, path := lastUpstreamRequest(t, state)
		if path != "/responses" || captured["model"] != model {
			t.Fatalf("configured exact model routed to path=%q model=%v, want /responses and %q", path, captured["model"], model)
		}
	})
	t.Run("ModelSupportCooldown", func(t *testing.T) {
		state := newNativeFixture(t)
		var rejected atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/responses" {
				rejected.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, err := io.WriteString(w, `{"error":{"code":"model_not_supported","param":"model","type":"invalid_request_error","message":"SYNTHETIC_PROVIDER_DETAIL"}}`)
				if err != nil {
					t.Errorf("write synthetic rejection: %v", err)
				}
				return
			}
			state.ServeHTTP(w, r)
		}))
		defer upstream.Close()
		base := startProxy(t, binary, upstream.URL)
		request := map[string]any{"model": "bridge-responses", "input": "Reply with ok."}
		status, response := postProxyWithSession(t, base+"/v1/responses", request, "model-support-cooldown")
		if status != http.StatusBadRequest || !bytes.Contains(response, []byte("The requested model is not supported.")) || bytes.Contains(response, []byte("SYNTHETIC_PROVIDER_DETAIL")) {
			t.Fatalf("model rejection lost its safe classification: status=%d response=%s", status, response)
		}
		status, response = postProxyWithSession(t, base+"/v1/responses", request, "model-support-cooldown")
		if status == http.StatusOK || rejected.Load() != 1 {
			t.Fatalf("CPA retried an unsupported model: status=%d upstream_calls=%d response=%s", status, rejected.Load(), response)
		}
		callProxy(t, base+"/v1/chat/completions", map[string]any{"model": "bridge-chat", "messages": []any{map[string]any{"role": "user", "content": "Reply with ok."}}})
	})
}

func TestNativeHostClaudeUnsupportedServerToolGuards(t *testing.T) {
	binary := os.Getenv("CPA_BINARY")
	if binary == "" {
		t.Skip("set CPA_BINARY to the task-owned pinned CLIProxyAPI candidate for synthetic native-host guards")
	}
	state := newNativeFixture(t)
	upstream := httptest.NewServer(state)
	t.Cleanup(upstream.Close)
	base := startProxy(t, binary, upstream.URL)
	t.Log("These synthetic tests verify translation guards and exclusion disclosure, not hosted provider capability.")
	for _, target := range []struct {
		name, model, path string
	}{
		{"Responses", "gpt-6-luna", "/responses"},
		{"Chat", "gemini-3.8-flash", "/chat/completions"},
	} {
		for _, tool := range []struct {
			name, declarationType string
			input                 map[string]any
		}{
			{"web_fetch", "web_fetch_20250910", map[string]any{"url": "https://example.org/synthetic-native-history"}},
			{"code_execution", "code_execution_20250825", map[string]any{"code": "print(385)"}},
		} {
			for _, stream := range []bool{false, true} {
				format := "JSON"
				if stream {
					format = "SSE"
				}
				t.Run(target.name+"/"+tool.name+"/"+format, func(t *testing.T) {
					request := func() map[string]any {
						return map[string]any{
							"model":      target.model,
							"stream":     stream,
							"max_tokens": 64,
							"messages":   []any{map[string]any{"role": "user", "content": "Reply with a synthetic fixture response."}},
							"tools": []any{
								map[string]any{"type": tool.declarationType, "name": tool.name},
								map[string]any{"name": "inspect", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}}},
							},
						}
					}
					t.Run("AutomaticExclusionDisclosure", func(t *testing.T) {
						payload := request()
						payload["tool_choice"] = map[string]any{"type": "auto"}
						before := upstreamRequestCount(state)
						status, headers, body := postNativeCompatibilityRequest(t, base+"/v1/messages", payload, t.Name())
						if after := upstreamRequestCount(state); after != before+1 {
							t.Fatalf("automatic exclusion dispatched %d synthetic inference requests, want one", after-before)
						}
						if status != http.StatusOK || !strings.Contains(headers.Get("X-Copilot-Excluded-Native-Tools"), tool.declarationType+";reason=unrepresentable_by_selected_endpoint") {
							t.Fatalf("automatic exclusion disclosure: status=%d headers=%v body=%s", status, headers, body)
						}
						_, model, schema, terminal := validateLiveResponse("Claude Messages", stream, headers.Get("Content-Type"), body)
						if !schema || !terminal || !liveMatrixResponseModelMatches(target.model, model) {
							t.Fatalf("automatic exclusion lost its destination response: model=%q schema=%t terminal=%t body=%s", model, schema, terminal, body)
						}
						captured, path := lastUpstreamRequest(t, state)
						tools, ok := captured["tools"].([]any)
						if path != target.path || !ok || len(tools) != 1 {
							t.Fatalf("automatic exclusion changed ordinary tool routing: path=%q request=%+v", path, captured)
						}
						ordinary := nativeMap(t, tools[0])
						name := ordinary["name"]
						if target.name == "Chat" {
							name = nativeMap(t, ordinary["function"])["name"]
						}
						if ordinary["type"] != "function" || name != "inspect" {
							t.Fatalf("hosted declaration became an ordinary function or displaced inspect: %+v", captured)
						}
					})
					t.Run("ForcedChoiceClientError", func(t *testing.T) {
						payload := request()
						payload["tool_choice"] = map[string]any{"type": "tool", "name": tool.name}
						before := upstreamRequestCount(state)
						status, headers, body := postNativeCompatibilityRequest(t, base+"/v1/messages", payload, t.Name())
						if after := upstreamRequestCount(state); after != before {
							t.Fatalf("forced unsupported hosted tool dispatched %d synthetic inference requests", after-before)
						}
						assertNativeCompatibilityClientError(t, status, headers, body, "tool_choice", tool.declarationType)
					})
					t.Run("HostedHistoryTranslationError", func(t *testing.T) {
						payload := request()
						delete(payload, "tools")
						payload["messages"] = []any{
							map[string]any{"role": "user", "content": "Run the synthetic hosted tool."},
							map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "server_tool_use", "id": "synthetic-hosted-call", "name": tool.name, "input": tool.input}}},
							map[string]any{"role": "user", "content": "Summarize the prior hosted result."},
						}
						before := upstreamRequestCount(state)
						status, headers, body := postNativeCompatibilityRequest(t, base+"/v1/messages", payload, t.Name())
						if after := upstreamRequestCount(state); after != before {
							t.Fatalf("unrepresentable hosted history dispatched %d synthetic inference requests", after-before)
						}
						assertNativeCompatibilityClientError(t, status, headers, body, "server_tool_use")
					})
				})
			}
		}
	}
}

func postNativeCompatibilityRequest(t *testing.T, endpoint string, payload any, session string) (int, http.Header, []byte) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer fixture-client-key")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Session-Id", session)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	out, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read native compatibility response: %v", err)
	}
	return response.StatusCode, response.Header.Clone(), out
}

func assertNativeCompatibilityClientError(t *testing.T, status int, headers http.Header, body []byte, details ...string) {
	t.Helper()
	var document map[string]any
	if status != http.StatusUnprocessableEntity || !strings.HasPrefix(headers.Get("Content-Type"), "application/json") || json.Unmarshal(body, &document) != nil {
		t.Fatalf("unsupported hosted tool did not return a static JSON 422: status=%d headers=%v body=%s", status, headers, body)
	}
	message, ok := nativeMap(t, document["error"])["message"].(string)
	if !ok || strings.TrimSpace(message) == "" {
		t.Fatalf("unsupported hosted tool lacked an explicit client error: %s", body)
	}
	for _, detail := range details {
		if !strings.Contains(message, detail) {
			t.Fatalf("unsupported hosted tool error did not identify %q: %s", detail, body)
		}
	}
}

func startProxyWithConfiguredEndpointOverride(t *testing.T, binary, upstream, model, endpoint string) string {
	t.Helper()
	root := t.TempDir()
	base, stop := startProxyInRoot(t, binary, upstream, root, nil, "")
	stop()
	configPath := filepath.Join(root, "config.yaml")
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read native test config: %v", err)
	}
	config := make(map[string]any)
	if err := yaml.Unmarshal(configBytes, &config); err != nil {
		t.Fatalf("decode native test config: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve native host port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release native host port: %v", err)
	}
	server := nativeMap(t, config["server"])
	server["port"] = port
	base = fmt.Sprintf("http://127.0.0.1:%d", port)
	plugins := nativeMap(t, config["plugins"])
	pluginConfigs := nativeMap(t, plugins["configs"])
	plugin := nativeMap(t, pluginConfigs["cliproxyapi-copilot"])
	plugin["model_endpoint_overrides"] = map[string]any{model: endpoint}
	configBytes, err = yaml.Marshal(config)
	if err != nil {
		t.Fatalf("encode native test config: %v", err)
	}
	if err := os.WriteFile(configPath, configBytes, 0600); err != nil {
		t.Fatalf("write native test config: %v", err)
	}
	logPath := filepath.Join(root, "host.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatalf("open native host log: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	command := exec.CommandContext(ctx, binary, "--config", configPath, "--local-model")
	command.Env = filteredChildEnvironment(os.Environ())
	command.Dir = root
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		cancel()
		_ = logFile.Close()
		t.Fatalf("start configured native host: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = command.Wait()
		_ = logFile.Close()
	})
	client := &http.Client{Timeout: 3 * time.Second}
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			log, _ := os.ReadFile(logPath)
			t.Fatalf("configured native host did not register %q: %s", model, log)
		case <-ticker.C:
			request, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
			request.Header.Set("Authorization", "Bearer fixture-client-key")
			response, err := client.Do(request)
			if err != nil {
				continue
			}
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				continue
			}
			var catalog struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if json.Unmarshal(body, &catalog) != nil {
				continue
			}
			for _, entry := range catalog.Data {
				if entry.ID == model {
					return base
				}
			}
		}
	}
}
