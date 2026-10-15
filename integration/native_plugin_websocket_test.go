package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/gorilla/websocket"
)

func TestNativeHostPluginResponsesWebsocket(t *testing.T) {
	binary := os.Getenv("CPA_BINARY")
	if binary == "" {
		t.Skip("set CPA_BINARY to the prepared CLIProxyAPI v8 server for native host integration")
	}
	state := newNativeFixture(t)
	upstream := httptest.NewServer(state)
	t.Cleanup(upstream.Close)
	base := startProxy(t, binary, upstream.URL)
	for _, route := range nativeCanonicalRoutes {
		route := route
		t.Run("Normal/"+route.model, func(t *testing.T) {
			connection := dialNativeResponsesWebsocket(t, base)
			request := nativeWebsocketRequest(t, route.model, route.path)
			if err := connection.WriteJSON(request); err != nil {
				t.Fatalf("send synthetic response.create request: %v", err)
			}
			events := readPluginResponsesWebsocket(t, connection)
			completed := nativeCompletedResponse(t, events)
			captured, path := lastUpstreamRequest(t, state)
			if path != route.path || captured["model"] != route.model {
				t.Fatalf("plugin Responses WebSocket routed %q to %q with model %v", route.model, path, captured["model"])
			}
			if completed["model"] != route.model {
				t.Fatalf("WebSocket response model = %v, want original ID %q", completed["model"], route.model)
			}
			if captured["stream"] != true {
				t.Fatalf("WebSocket request was not sent to the plugin as a stream: %+v", captured)
			}
			if route.path == "/responses" {
				assertNativeWebsocketResponsesHistory(t, captured, false)
			} else {
				assertNativeWebsocketConversationHistory(t, captured)
			}
			if route.model == "gpt-6-luna" || route.model == "gemini-3.8-flash" || route.model == "claude-sonnet-5.5" {
				nativeWebsocketThreeTurnReplay(t, connection, route, request, completed, state)
			}
		})
	}
	for _, model := range nativeConfiguredCompactionModels(t) {
		model := model
		t.Run("CompactionReplay/"+model, func(t *testing.T) {
			connection := dialNativeResponsesWebsocket(t, base)
			input := append(matrixCompactionHistory(t), map[string]any{"type": "compaction_trigger", "id": "fixture-ws-trigger"})
			request := map[string]any{"type": "response.create", "model": model, "input": input, "stream": true, "prompt_cache_key": matrixCompactCacheKey}
			if err := connection.WriteJSON(request); err != nil {
				t.Fatalf("send synthetic WebSocket compaction request: %v", err)
			}
			events := readPluginResponsesWebsocket(t, connection)
			completed := nativeCompletedResponse(t, events)
			completedBody, err := json.Marshal(completed)
			if err != nil {
				t.Fatal(err)
			}
			capsule := assertSingleCompaction(t, completedBody)
			captured, path := lastUpstreamRequest(t, state)
			assertCompactionRequest(t, captured, path)
			if captured["model"] != model {
				t.Fatalf("WebSocket compaction model = %v, want %q", captured["model"], model)
			}
			assertNativeWebsocketResponsesHistory(t, captured, true)
			replay := []any{
				map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Continue the synthetic WebSocket task."}}},
				map[string]any{"type": "compaction", "encrypted_content": capsule},
				map[string]any{"type": "compaction_trigger", "id": "fixture-ws-replay-trigger"},
			}
			if err := connection.WriteJSON(map[string]any{"type": "response.create", "model": model, "input": replay, "stream": true, "prompt_cache_key": matrixCompactCacheKey}); err != nil {
				t.Fatalf("send synthetic WebSocket capsule replay: %v", err)
			}
			readPluginResponsesWebsocket(t, connection)
			replayRequest, replayPath := lastUpstreamRequest(t, state)
			if replayPath != "/responses" || replayRequest["model"] != model {
				t.Fatalf("capsule replay routed to %q with model %v", replayPath, replayRequest["model"])
			}
			if replayRequest["prompt_cache_key"] != matrixCompactCacheKey {
				t.Fatalf("capsule replay changed the prompt cache key: %+v", replayRequest)
			}
			replayInput, err := json.Marshal(replayRequest["input"])
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(replayInput), capsule) || strings.Contains(string(replayInput), "compaction_trigger") || !strings.Contains(string(replayInput), "active goal is to finish the native host bridge") || !strings.Contains(string(replayInput), "Continue the synthetic WebSocket task.") {
				t.Fatalf("WebSocket capsule replay changed or omitted history: %s", replayInput)
			}
		})
	}
}

func dialNativeResponsesWebsocket(t *testing.T, base string) *websocket.Conn {
	t.Helper()
	header := http.Header{"Authorization": []string{"Bearer fixture-client-key"}}
	connection, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(base, "http")+"/v1/responses", header)
	if err != nil {
		if response != nil {
			t.Fatalf("connect plugin Responses WebSocket: status=%d", response.StatusCode)
		}
		t.Fatalf("connect plugin Responses WebSocket: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return connection
}

func nativeWebsocketRequest(t *testing.T, model, path string) map[string]any {
	t.Helper()
	request := nativeResponsesProbe(t, nativeCanonicalRoute{model: model, path: path}, true)
	request["type"] = "response.create"
	request["tools"] = []any{map[string]any{"type": "function", "name": "inspect", "description": "Synthetic fixture tool", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}}}
	if path == "/responses" {
		request["reasoning"] = map[string]any{"effort": "high"}
	}
	if model == "claude-sonnet-5.5" {
		request["reasoning_effort"] = "high"
	}
	return request
}

func readPluginResponsesWebsocket(t *testing.T, connection *websocket.Conn) []map[string]any {
	t.Helper()
	if deadline, ok := t.Deadline(); ok {
		if err := connection.SetReadDeadline(deadline.Add(-time.Second)); err != nil {
			t.Fatalf("set test-bound WebSocket read deadline: %v", err)
		}
	}
	events := make([]map[string]any, 0, 8)
	for range 256 {
		_, body, err := connection.ReadMessage()
		if err != nil {
			t.Fatalf("read plugin Responses WebSocket event: %v", err)
		}
		var event map[string]any
		if err := json.Unmarshal(body, &event); err != nil {
			t.Fatalf("decode plugin Responses WebSocket event %s: %v", body, err)
		}
		events = append(events, event)
		if event["type"] == "response.completed" || event["type"] == "error" {
			return events
		}
	}
	t.Fatalf("plugin Responses WebSocket did not complete: %+v", events)
	return nil
}

func nativeCompletedResponse(t *testing.T, events []map[string]any) map[string]any {
	t.Helper()
	for _, event := range events {
		if event["type"] == "error" {
			t.Fatalf("plugin Responses WebSocket returned an error: %+v", event)
		}
		if event["type"] == "response.completed" {
			response, ok := event["response"].(map[string]any)
			if !ok {
				t.Fatalf("completed WebSocket event has response type %T", event["response"])
			}
			return response
		}
	}
	t.Fatalf("plugin Responses WebSocket has no completed response: %+v", events)
	return nil
}

func assertNativeWebsocketConversationHistory(t *testing.T, request map[string]any) {
	t.Helper()
	serialized, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"synthetic native routing request", "fixture result", "inspect", "Summarize the provided synthetic conversation as ordinary prose."} {
		if !strings.Contains(string(serialized), expected) {
			t.Fatalf("translated WebSocket request omitted %q: %s", expected, serialized)
		}
	}
	if strings.Contains(string(serialized), "compaction_trigger") {
		t.Fatalf("ordinary WebSocket summary request included a compaction trigger: %s", serialized)
	}
}

func nativeWebsocketThreeTurnReplay(t *testing.T, connection *websocket.Conn, route nativeCanonicalRoute, initialRequest, firstResponse map[string]any, state *fixture) {
	t.Helper()
	initialInput, ok := initialRequest["input"].([]any)
	if !ok {
		t.Fatalf("initial WebSocket input has type %T", initialRequest["input"])
	}
	history := append([]any(nil), initialInput...)
	response := firstResponse
	for turn := 2; turn <= 3; turn++ {
		output := jsonObjects(response["output"])
		itemID, callID, ok := nativeWebsocketToolCall(t, output)
		if !ok {
			t.Fatalf("WebSocket turn %d did not return a function call: %+v", turn-1, response["output"])
		}
		for _, item := range output {
			history = append(history, item)
		}
		resultText := "synthetic WebSocket tool result turn " + string(rune('0'+turn-1))
		history = append(history, map[string]any{"type": "function_call_output", "call_id": callID, "output": resultText})
		continuation := "Continue the synthetic WebSocket conversation at turn " + string(rune('0'+turn)) + "."
		history = append(history, map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": continuation}}})
		request := cloneJSONMap(initialRequest)
		request["input"] = history
		if err := connection.WriteJSON(request); err != nil {
			t.Fatalf("send WebSocket continuation turn %d: %v", turn, err)
		}
		events := readPluginResponsesWebsocket(t, connection)
		for _, event := range events {
			if event["type"] == "error" {
				t.Fatalf("WebSocket continuation turn %d rejected output=%+v request=%+v event=%+v", turn, firstResponse["output"], request, event)
			}
		}
		response = nativeCompletedResponse(t, events)
		captured, path := lastUpstreamRequest(t, state)
		if path != route.path || captured["model"] != route.model {
			t.Fatalf("WebSocket continuation turn %d used path/model %q/%v", turn, path, captured["model"])
		}
		assertNativeWebsocketContinuation(t, route, captured, itemID, callID, resultText, continuation)
	}
}

func nativeWebsocketToolCall(t *testing.T, output []map[string]any) (string, string, bool) {
	t.Helper()
	for _, item := range output {
		if item["type"] == "function_call" || item["type"] == "custom_tool_call" {
			itemID := stringValue(item["id"])
			callID := stringValue(item["call_id"])
			if itemID != "" && callID != "" {
				return itemID, callID, true
			}
		}
	}
	return "", "", false
}

func assertNativeWebsocketContinuation(t *testing.T, route nativeCanonicalRoute, request map[string]any, itemID, callID, result, continuation string) {
	t.Helper()
	serialized, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(serialized), result) || !strings.Contains(string(serialized), continuation) {
		t.Fatalf("WebSocket continuation dropped prior tool output or user text: %s", serialized)
	}
	if route.path == "/responses" {
		var functionFound, resultFound bool
		for _, item := range jsonObjects(request["input"]) {
			if item["type"] == "function_call" && item["id"] == itemID && item["call_id"] == callID {
				functionFound = true
			}
			if item["type"] == "function_call_output" && item["call_id"] == callID && item["output"] == result {
				resultFound = true
			}
		}
		if !functionFound || !resultFound || !matrixOpaqueContains(request["input"], matrixCompactOpaque) {
			t.Fatalf("Responses WebSocket continuation lost prior thinking or exact tool IDs: %+v", request["input"])
		}
		return
	}
	if !nativeWebsocketToolIdentity(request, itemID, callID) {
		t.Fatalf("translated WebSocket continuation lost prior call ID %q/%q: %+v", itemID, callID, request)
	}
	if route.path == "/chat/completions" && !matrixOpaqueContains(request, matrixSignature) {
		t.Fatalf("Chat WebSocket continuation dropped provider thinking: %+v", request)
	}
	if route.path == "/v1/messages" && (!matrixOpaqueContains(request, matrixSignature) || !matrixOpaqueContains(request, matrixRedacted)) {
		t.Fatalf("Messages WebSocket continuation dropped signed or redacted thinking: %+v", request)
	}
}

func nativeWebsocketToolIdentity(value any, itemID, callID string) bool {
	switch current := value.(type) {
	case string:
		if current == itemID || current == callID {
			return true
		}
		decodedItemID, decodedCallID, ok := translate.DecodeClaudeToolIDs(current)
		return ok && decodedItemID == itemID && decodedCallID == callID
	case []any:
		for _, nested := range current {
			if nativeWebsocketToolIdentity(nested, itemID, callID) {
				return true
			}
		}
	case map[string]any:
		for _, nested := range current {
			if nativeWebsocketToolIdentity(nested, itemID, callID) {
				return true
			}
		}
	}
	return false
}

func assertNativeWebsocketResponsesHistory(t *testing.T, request map[string]any, compaction bool) {
	t.Helper()
	items := jsonObjects(request["input"])
	var reasoning, functionCall, functionOutput bool
	for _, item := range items {
		switch item["type"] {
		case "reasoning":
			reasoning = item["id"] == matrixCompactReasoningID && item["encrypted_content"] == matrixCompactOpaque
		case "function_call":
			functionCall = item["id"] == matrixCompactFunctionID && item["call_id"] == matrixCompactCallID
		case "function_call_output":
			functionOutput = item["call_id"] == matrixCompactCallID && item["output"] == "original result"
		}
	}
	if !reasoning || !functionCall || !functionOutput {
		t.Fatalf("Responses WebSocket lost synthetic thinking or tool history: %+v", request["input"])
	}
	if compaction && request["tool_choice"] != "none" {
		t.Fatalf("Responses WebSocket compaction tool_choice = %v, want none", request["tool_choice"])
	}
}
