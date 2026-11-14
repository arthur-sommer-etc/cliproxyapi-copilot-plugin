package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

const cachedSearchCaptureModel = "claude-haiku-5.5"

type cachedSearchCaptureHost struct {
	inner         *nativeToolHost
	streamPayload []byte
	mu            sync.Mutex
	calls         []transport.Request
	streamOpens   int
}

func (h *cachedSearchCaptureHost) capture(request transport.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	request.Body = append([]byte(nil), request.Body...)
	request.Headers = request.Headers.Clone()
	h.calls = append(h.calls, request)
}

func (h *cachedSearchCaptureHost) Do(ctx context.Context, callback string, request transport.Request) (transport.Response, error) {
	h.capture(request)
	return h.inner.Do(ctx, callback, request)
}

func (h *cachedSearchCaptureHost) OpenStream(ctx context.Context, callback string, request transport.Request) (transport.Stream, error) {
	h.capture(request)
	h.mu.Lock()
	h.streamOpens++
	h.mu.Unlock()
	return h.inner.OpenStream(ctx, callback, request)
}

func (h *cachedSearchCaptureHost) ReadStream(ctx context.Context, id string) (transport.StreamChunk, error) {
	if len(h.streamPayload) > 0 {
		return transport.StreamChunk{Payload: append([]byte(nil), h.streamPayload...), Done: true}, nil
	}
	return h.inner.ReadStream(ctx, id)
}

func (h *cachedSearchCaptureHost) CloseStream(ctx context.Context, id string) error {
	return h.inner.CloseStream(ctx, id)
}

func (h *cachedSearchCaptureHost) Emit(ctx context.Context, id string, frame []byte) error {
	return h.inner.Emit(ctx, id, frame)
}

func (h *cachedSearchCaptureHost) CloseOutput(ctx context.Context, id, message string) {
	h.inner.CloseOutput(ctx, id, message)
}

func (h *cachedSearchCaptureHost) inferenceCalls() []transport.Request {
	h.mu.Lock()
	defer h.mu.Unlock()
	calls := make([]transport.Request, 0, len(h.calls))
	for _, call := range h.calls {
		if call.Method == http.MethodPost {
			calls = append(calls, call)
		}
	}
	return calls
}

func (h *cachedSearchCaptureHost) streamOpenCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.streamOpens
}

type cachedSearchFunctionCall struct {
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func TestResponsesCachedSearchCapturedBodyExecuteAndStream(t *testing.T) {
	requestBody, err := os.ReadFile("testdata/codex-cached-search/request.json")
	if err != nil {
		t.Fatal(err)
	}
	responseJSON, err := os.ReadFile("testdata/codex-cached-search/response.json")
	if err != nil {
		t.Fatal(err)
	}
	var responseBody string
	if err := json.Unmarshal(responseJSON, &responseBody); err != nil {
		t.Fatal(err)
	}
	capturedResponse := []byte(responseBody)
	originalRequest := gjson.ParseBytes(requestBody)
	if originalRequest.Get("model").String() != cachedSearchCaptureModel || !originalRequest.Get("stream").Bool() || originalRequest.Get("tool_choice").String() != "auto" {
		t.Fatalf("captured request identity changed: model=%q stream=%t tool_choice=%q", originalRequest.Get("model").String(), originalRequest.Get("stream").Bool(), originalRequest.Get("tool_choice").String())
	}
	if originalRequest.Get("tools.#(type==web_search).external_web_access").Exists() == false || originalRequest.Get("tools.#(type==web_search).external_web_access").Bool() {
		t.Fatalf("captured native web_search declaration changed: %s", originalRequest.Get("tools.#(type==web_search)").Raw)
	}
	if originalRequest.Get("tools.#").Int() != 9 {
		t.Fatalf("captured tool declaration count = %d, want 9", originalRequest.Get("tools.#").Int())
	}
	capturedCall := capturedCachedSearchFunctionCall(t, capturedResponse)
	if capturedCall.Name != "exec_command" || !strings.Contains(capturedCall.Arguments, "/workspace/attachments/document.pdf") || !strings.Contains(capturedCall.Arguments, `"workdir": "/workspace/user"`) {
		t.Fatalf("captured function call was not portably preserved: %+v", capturedCall)
	}
	if strings.Contains(string(capturedResponse), "web_search_result") || strings.Contains(string(capturedResponse), "web_search_call") {
		t.Fatal("captured response contains a native search result")
	}
	messagesResponse := capturedFunctionCallAsMessagesResponse(t, capturedCall)

	for _, streaming := range []bool{false, true} {
		name := "execute"
		if streaming {
			name = "execute-stream"
		}
		t.Run(name, func(t *testing.T) {
			inner := &nativeToolHost{
				compactionStreamHost: newCompactionStreamHost(messagesResponse),
				messages:             true,
			}
			host := &cachedSearchCaptureHost{inner: inner, streamPayload: cachedSearchMessagesStream(t, capturedCall)}
			service := newCompactionStreamService(t, inner.compactionStreamHost)
			service.host = host
			token := service.tokenEntries["auth-id"]
			if err := service.Configure([]byte("model_endpoint_overrides:\n  " + cachedSearchCaptureModel + ": /v1/messages\n")); err != nil {
				t.Fatal(err)
			}
			_, generation := service.configSnapshot()
			token.ConfigGeneration = generation
			service.tokenEntries["auth-id"] = token

			request := compactionStreamRequest(requestBody, "")
			request.Model = cachedSearchCaptureModel
			request.SourceFormat = "openai-response"
			request.OriginalRequest = append([]byte(nil), requestBody...)
			request.Payload = append([]byte(nil), requestBody...)
			request.StreamID = "cached-search-capture"

			var headers http.Header
			var response pluginapi.ExecutorResponse
			if streaming {
				var executeErr error
				headers, executeErr = service.ExecuteStream(context.Background(), request)
				if executeErr != nil {
					t.Fatal(executeErr)
				}
				frames, closeMessage := collectCompactionStreamFrames(t, inner.compactionStreamHost)
				if closeMessage != "" {
					t.Fatalf("stream closed with error: %s", closeMessage)
				}
				assertCapturedFunctionCallStream(t, frames, capturedCall)
			} else {
				var executeErr error
				response, executeErr = service.Execute(context.Background(), request)
				if executeErr != nil {
					t.Fatal(executeErr)
				}
				headers = response.Headers
				assertCapturedFunctionCallResponse(t, response.Payload, capturedCall)
				if response.Metadata["copilot_excluded_native_tools"] == nil {
					t.Fatal("Execute response is missing native-tool exclusion metadata")
				}
				metadata, marshalErr := json.Marshal(response.Metadata["copilot_excluded_native_tools"])
				if marshalErr != nil || gjson.GetBytes(metadata, "0.type").String() != "web_search" || gjson.GetBytes(metadata, "0.reason").String() != "unrepresentable_by_selected_endpoint" {
					t.Fatalf("native-tool exclusion metadata = %s, error = %v", metadata, marshalErr)
				}
			}

			if got := headers.Get("X-Copilot-Excluded-Native-Tools"); got != "web_search;reason=unrepresentable_by_selected_endpoint" {
				t.Fatalf("native-tool exclusion header = %q", got)
			}
			calls := host.inferenceCalls()
			if len(calls) != 1 {
				t.Fatalf("inference calls = %d, want exactly one", len(calls))
			}
			assertCachedSearchMessagesRequest(t, requestBody, calls[0].Body)
			if host.streamOpenCount() != boolInt(streaming) {
				t.Fatalf("upstream stream opens = %d, want %d", host.streamOpenCount(), boolInt(streaming))
			}
		})
	}
}

func capturedCachedSearchFunctionCall(t *testing.T, body []byte) cachedSearchFunctionCall {
	t.Helper()
	var terminal map[string]json.RawMessage
	terminalCount := 0
	for _, frame := range strings.Split(strings.TrimSpace(string(body)), "\n\n") {
		var eventName string
		var data []byte
		for _, line := range strings.Split(frame, "\n") {
			if strings.HasPrefix(line, "event: ") {
				eventName = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, "data: ") {
				data = []byte(strings.TrimPrefix(line, "data: "))
			}
		}
		if len(data) == 0 {
			continue
		}
		if err := json.Unmarshal(data, &terminal); err != nil {
			t.Fatalf("decode captured response event: %v", err)
		}
		if eventName == "response.completed" || gjson.GetBytes(data, "type").String() == "response.completed" {
			terminalCount++
			terminal = make(map[string]json.RawMessage)
			if err := json.Unmarshal(data, &terminal); err != nil {
				t.Fatalf("decode captured response terminal: %v", err)
			}
		}
	}
	if terminalCount != 1 {
		t.Fatalf("captured response terminal count = %d, want 1", terminalCount)
	}
	var completed struct {
		Response struct {
			Status string `json:"status"`
			Output []struct {
				Type      string `json:"type"`
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"output"`
		} `json:"response"`
	}
	terminalBytes, err := json.Marshal(terminal)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(terminalBytes, &completed); err != nil {
		t.Fatalf("decode captured response.completed: %v", err)
	}
	if completed.Response.Status != "completed" {
		t.Fatalf("captured response status = %q, want completed", completed.Response.Status)
	}
	var calls []cachedSearchFunctionCall
	for _, item := range completed.Response.Output {
		if item.Type == "function_call" {
			calls = append(calls, cachedSearchFunctionCall{CallID: item.CallID, Name: item.Name, Arguments: item.Arguments})
		}
	}
	if len(calls) != 1 || calls[0].CallID == "" || calls[0].Arguments == "" {
		t.Fatalf("captured client function calls = %+v, want one completed call", calls)
	}
	return calls[0]
}

func capturedFunctionCallAsMessagesResponse(t *testing.T, call cachedSearchFunctionCall) []byte {
	t.Helper()
	var input json.RawMessage
	if err := json.Unmarshal([]byte(call.Arguments), &input); err != nil || len(input) == 0 || !json.Valid(input) {
		t.Fatalf("captured function arguments are invalid JSON: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"id":    "msg_cached_search_fixture",
		"type":  "message",
		"role":  "assistant",
		"model": cachedSearchCaptureModel,
		"content": []any{map[string]any{
			"type":  "tool_use",
			"id":    call.CallID,
			"name":  call.Name,
			"input": input,
		}},
		"stop_reason":   "tool_use",
		"stop_sequence": nil,
		"usage":         map[string]int{"input_tokens": 1, "output_tokens": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func cachedSearchMessagesStream(t *testing.T, call cachedSearchFunctionCall) []byte {
	t.Helper()
	var input json.RawMessage
	if err := json.Unmarshal([]byte(call.Arguments), &input); err != nil || !json.Valid(input) {
		t.Fatalf("captured function arguments are invalid JSON: %v", err)
	}
	var stream strings.Builder
	appendEvent := func(name string, payload any) {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("encode Messages %s event: %v", name, err)
		}
		fmt.Fprintf(&stream, "event: %s\ndata: %s\n\n", name, encoded)
	}
	appendEvent("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            "msg_cached_search_fixture",
			"type":          "message",
			"role":          "assistant",
			"model":         cachedSearchCaptureModel,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]int{"input_tokens": 1, "output_tokens": 0},
		},
	})
	appendEvent("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         0,
		"content_block": map[string]any{"type": "tool_use", "id": call.CallID, "name": call.Name, "input": map[string]any{}},
	})
	appendEvent("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": 0,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": string(input)},
	})
	appendEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	appendEvent("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "tool_use", "stop_sequence": nil},
		"usage": map[string]int{"output_tokens": 1},
	})
	appendEvent("message_stop", map[string]any{"type": "message_stop"})
	return []byte(stream.String())
}

func assertCachedSearchMessagesRequest(t *testing.T, original, translated []byte) {
	t.Helper()
	originalTools := gjson.GetBytes(original, "tools").Array()
	translatedTools := gjson.GetBytes(translated, "tools").Array()
	if len(translatedTools) == 0 {
		t.Fatalf("translated Messages request has no tools: %s", translated)
	}
	for _, tool := range translatedTools {
		if tool.Get("type").String() == "web_search" || tool.Get("type").String() == "web_search_20250305" {
			t.Fatalf("unsupported native web_search reached Messages: %s", tool.Raw)
		}
	}
	declarations := collectCachedSearchFunctionDeclarations(originalTools)
	if len(declarations) == 0 {
		t.Fatal("captured request has no client function declarations")
	}
	matched := make([]bool, len(translatedTools))
	for _, declaration := range declarations {
		found := false
		for index, candidate := range translatedTools {
			if matched[index] || candidate.Get("name").String() == "" || candidate.Get("description").String() != declaration.description {
				continue
			}
			if !jsonValuesEqual(candidate.Get("input_schema").Raw, declaration.schema) {
				continue
			}
			if !declaration.namespace && candidate.Get("name").String() != declaration.name {
				continue
			}
			if declaration.namespace && !strings.HasSuffix(candidate.Get("name").String(), declaration.name) {
				continue
			}
			matched[index] = true
			found = true
			break
		}
		if !found {
			t.Fatalf("captured function schema was not preserved in Messages tools: name=%q namespace=%t description=%q schema=%s translated=%s", declaration.name, declaration.namespace, declaration.description, declaration.schema, translated)
		}
	}
}

type cachedSearchFunctionDeclaration struct {
	name        string
	description string
	schema      string
	namespace   bool
}

func collectCachedSearchFunctionDeclarations(tools []gjson.Result) []cachedSearchFunctionDeclaration {
	var declarations []cachedSearchFunctionDeclaration
	var collect func(gjson.Result, bool)
	collect = func(tool gjson.Result, namespace bool) {
		switch tool.Get("type").String() {
		case "function":
			declarations = append(declarations, cachedSearchFunctionDeclaration{
				name:        tool.Get("name").String(),
				description: tool.Get("description").String(),
				schema:      tool.Get("parameters").Raw,
				namespace:   namespace,
			})
		case "namespace":
			for _, nested := range tool.Get("tools").Array() {
				collect(nested, true)
			}
		}
	}
	for _, tool := range tools {
		collect(tool, false)
	}
	return declarations
}

func jsonValuesEqual(left, right string) bool {
	var leftValue, rightValue any
	if json.Unmarshal([]byte(left), &leftValue) != nil || json.Unmarshal([]byte(right), &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func assertCapturedFunctionCallResponse(t *testing.T, body []byte, expected cachedSearchFunctionCall) {
	t.Helper()
	if !json.Valid(body) {
		t.Fatalf("Execute returned invalid JSON: %s", body)
	}
	assertNoCachedSearchResult(t, body)
	for _, item := range gjson.GetBytes(body, "output").Array() {
		if item.Get("type").String() != "function_call" {
			continue
		}
		if item.Get("name").String() != expected.Name || item.Get("call_id").String() != expected.CallID || !jsonValuesEqual(item.Get("arguments").String(), expected.Arguments) {
			t.Fatalf("Execute function call differs from captured protocol body: %s", item.Raw)
		}
		return
	}
	var outputSummary []string
	for _, item := range gjson.GetBytes(body, "output").Array() {
		outputSummary = append(outputSummary, item.Get("type").String()+":"+item.Get("name").String())
	}
	t.Fatalf("Execute did not return the captured client function call: output=%v", outputSummary)
}

func assertCapturedFunctionCallStream(t *testing.T, frames [][]byte, expected cachedSearchFunctionCall) {
	t.Helper()
	var terminal streamTerminal
	var completedPayload []byte
	for _, frame := range frames {
		done, err := terminal.observe(translate.EndpointResponses, frame, "", "")
		if err != nil {
			t.Fatalf("validate Responses stream frame: %v; frame=%s", err, frame)
		}
		if done {
			completedPayload = append([]byte(nil), terminal.response...)
		}
	}
	if !terminal.completed || len(completedPayload) == 0 {
		t.Fatalf("Responses stream had no valid completed terminal: %q", fmt.Sprint(frames))
	}
	if len(gjson.GetBytes(completedPayload, "output").Array()) == 0 {
		var eventTypes []string
		for _, frame := range frames {
			data := cachedSearchStreamData(frame)
			eventTypes = append(eventTypes, gjson.GetBytes(data, "type").String())
		}
		t.Fatalf("Messages tool-use response translated to an empty Responses output; events=%v status=%q output_count=%d", eventTypes, gjson.GetBytes(completedPayload, "status").String(), len(gjson.GetBytes(completedPayload, "output").Array()))
	}
	assertCapturedFunctionCallResponse(t, completedPayload, expected)
	for _, frame := range frames {
		assertNoCachedSearchResult(t, frame)
	}
}

func cachedSearchStreamData(frame []byte) []byte {
	for _, line := range strings.Split(string(frame), "\n") {
		if strings.HasPrefix(line, "data: ") {
			return []byte(strings.TrimPrefix(line, "data: "))
		}
	}
	return nil
}

func assertNoCachedSearchResult(t *testing.T, body []byte) {
	t.Helper()
	for _, marker := range []string{"web_search_result", "web_search_call", "search_result"} {
		if strings.Contains(string(body), marker) {
			t.Fatalf("executor fabricated cached search result %q: %s", marker, body)
		}
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
