package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
)

func TestNativeHostCanonicalResponsesRouting(t *testing.T) {
	binary := os.Getenv("CPA_BINARY")
	if binary == "" {
		t.Skip("set CPA_BINARY to the prepared CLIProxyAPI v8 server for native host integration")
	}
	state := newNativeFixture(t)
	upstream := httptest.NewServer(state)
	t.Cleanup(upstream.Close)
	base := startProxy(t, binary, upstream.URL)
	catalogModels := nativeCatalogModelIDs(t, state.modelCatalog)
	for _, route := range nativeCanonicalRoutes {
		route := route
		t.Run(route.model, func(t *testing.T) {
			if _, ok := catalogModels[route.model]; !ok {
				t.Fatalf("synthetic catalog seed does not contain %q", route.model)
			}
			for _, stream := range []bool{false, true} {
				stream := stream
				t.Run(fmt.Sprintf("Stream%v", stream), func(t *testing.T) {
					session := fmt.Sprintf("native-canonical-%s-%t", route.model, stream)
					request := nativeResponsesProbe(t, route, stream)
					before := upstreamRequestCount(state)
					status, response := postProxyWithSession(t, base+"/v1/responses", request, session)
					if status != http.StatusOK {
						t.Fatalf("canonical request for %q returned status %d after %d synthetic upstream requests: %s", route.model, status, upstreamRequestCount(state)-before, response)
					}
					captured, path := lastUpstreamRequest(t, state)
					if path != route.path || captured["model"] != route.model {
						t.Fatalf("Responses request for %q routed to %q with model %v", route.model, path, captured["model"])
					}
					assertNativeCanonicalHistory(t, route, captured)
					var result map[string]any
					if stream {
						events := parseSSEDataEvents(t, response)
						terminal := events[len(events)-1]
						assertEventType(t, terminal, "response.completed")
						result, _ = terminal["response"].(map[string]any)
					} else if err := json.Unmarshal(response, &result); err != nil {
						t.Fatalf("decode synthetic Responses result: %v", err)
					}
					if result == nil || result["model"] != route.model {
						t.Fatalf("Responses result model = %v, want original ID %q", result["model"], route.model)
					}
					turn := nativeFixtureModelTurn(t, state, route.model)
					itemID, transportCallID, callID := assertNativeCanonicalOutput(t, route, result, turn)
					if route.model == "gpt-6-luna" || route.model == "gemini-3.8-flash" || route.model == "claude-sonnet-5.5" {
						assertNativeCanonicalReplay(t, base, state, route, request, result, session, itemID, transportCallID, callID, turn)
					}
				})
			}
		})
	}
	t.Run("CustomTool/claude-sonnet-5.5", func(t *testing.T) {
		route := nativeCanonicalRoute{model: "claude-sonnet-5.5", path: "/v1/messages"}
		setNativeCustomToolResponse(t, state)
		for _, stream := range []bool{false, true} {
			stream := stream
			t.Run(fmt.Sprintf("Stream%v", stream), func(t *testing.T) {
				session := fmt.Sprintf("native-canonical-custom-%t", stream)
				request := nativeCustomToolProbe(t, route, stream)
				status, response := postProxyWithSession(t, base+"/v1/responses", request, session)
				if status != http.StatusOK {
					t.Fatalf("custom-tool request returned status %d: %s", status, response)
				}
				captured, path := lastUpstreamRequest(t, state)
				if path != route.path || captured["model"] != route.model || !containsJSONScalar(captured, "synthetic custom tool routing request") {
					t.Fatalf("custom-tool request changed its model, route, or input: path=%q request=%+v", path, captured)
				}
				var result map[string]any
				if stream {
					events := parseSSEDataEvents(t, response)
					terminal := events[len(events)-1]
					assertEventType(t, terminal, "response.completed")
					result, _ = terminal["response"].(map[string]any)
				} else if err := json.Unmarshal(response, &result); err != nil {
					t.Fatalf("decode synthetic custom-tool Responses result: %v", err)
				}
				if result == nil || result["model"] != route.model {
					t.Fatalf("custom-tool result model = %v, want %q", result["model"], route.model)
				}
				turn := nativeFixtureModelTurn(t, state, route.model)
				callID := nativeCanonicalCallID(route, turn)
				customItemID := assertNativeCustomToolOutput(t, result, callID)
				assertNativeCustomToolReplay(t, base, state, route, request, result, session, customItemID, callID)
			})
		}
	})
}

func TestNativeHostConfiguredCanonicalResponsesCompaction(t *testing.T) {
	binary := os.Getenv("CPA_BINARY")
	if binary == "" {
		t.Skip("set CPA_BINARY to the prepared CLIProxyAPI v8 server for native host integration")
	}
	models := nativeConfiguredCompactionModels(t)
	wantModels := []string{"gpt-6-luna", "gpt-6.1-sol", "mai-code-1.1-flash"}
	assertEqualJSON(t, models, wantModels)
	state := newNativeFixture(t)
	upstream := httptest.NewServer(state)
	t.Cleanup(upstream.Close)
	base := startProxy(t, binary, upstream.URL)
	for _, model := range models {
		model := model
		t.Run(model, func(t *testing.T) {
			history := matrixCompactionHistory(t)
			bufferedInput := append(append([]any(nil), history...), map[string]any{"type": "compaction_trigger"})
			buffered := callProxy(t, base+"/v1/responses", map[string]any{"model": model, "input": bufferedInput, "prompt_cache_key": matrixCompactCacheKey})
			bufferedRequest, bufferedPath := lastUpstreamRequest(t, state)
			assertCompactionRequest(t, bufferedRequest, bufferedPath)
			if bufferedRequest["model"] != model {
				t.Fatalf("buffered compaction model = %v, want %q", bufferedRequest["model"], model)
			}
			bufferedCapsule := assertSingleCompaction(t, buffered)
			assertCompactionReplay(t, base, state, bufferedCapsule, model, "Replay buffered summary.", false)
			streamInput := append(append([]any(nil), history...), map[string]any{"type": "compaction_trigger"})
			stream := callProxy(t, base+"/v1/responses", map[string]any{"model": model, "stream": true, "input": streamInput, "prompt_cache_key": matrixCompactCacheKey})
			streamRequest, streamPath := lastUpstreamRequest(t, state)
			assertCompactionRequest(t, streamRequest, streamPath)
			if streamRequest["model"] != model {
				t.Fatalf("streamed compaction model = %v, want %q", streamRequest["model"], model)
			}
			events := parseSSEDataEvents(t, stream)
			if len(events) == 0 {
				t.Fatal("streamed compaction returned no events")
			}
			terminal := events[len(events)-1]
			assertEventType(t, terminal, "response.completed")
			completed, ok := terminal["response"].(map[string]any)
			if !ok {
				t.Fatalf("streamed compaction has no completed response: %+v", terminal)
			}
			completedBody, err := json.Marshal(completed)
			if err != nil {
				t.Fatal(err)
			}
			streamCapsule := assertSingleCompaction(t, completedBody)
			assertCompactionReplay(t, base, state, streamCapsule, model, "Replay streamed summary.", false)
			compact := callProxy(t, base+"/v1/responses/compact", map[string]any{"model": model, "input": history, "prompt_cache_key": matrixCompactCacheKey})
			compactRequest, compactPath := lastUpstreamRequest(t, state)
			assertCompactionRequest(t, compactRequest, compactPath)
			if compactRequest["model"] != model {
				t.Fatalf("compact route model = %v, want %q", compactRequest["model"], model)
			}
			compactCapsule := assertSingleCompaction(t, compact)
			assertCompactionReplay(t, base, state, compactCapsule, model, "Replay compact route summary.", false)
		})
	}
	unsupported := []string{"gpt-6-sol"}
	for _, route := range nativeCanonicalRoutes {
		if !containsString(models, route.model) {
			unsupported = append(unsupported, route.model)
		}
	}
	for _, model := range unsupported {
		model := model
		t.Run("Unsupported/"+model, func(t *testing.T) {
			before := upstreamRequestCount(state)
			input := append(append([]any(nil), matrixCompactionHistory(t)...), map[string]any{"type": "compaction_trigger"})
			status, body := postProxyWithSession(t, base+"/v1/responses", map[string]any{"model": model, "input": input}, "unsupported-v2-"+model)
			wantStatus := http.StatusUnprocessableEntity
			if model == "gpt-6-sol" {
				wantStatus = http.StatusBadRequest
				if !strings.Contains(string(body), "model_not_found") {
					t.Fatalf("excluded model error omitted model_not_found: %s", body)
				}
			}
			if status != wantStatus {
				t.Fatalf("unconfigured V2 compaction status = %d, want %d: %s", status, wantStatus, body)
			}
			status, body = postProxyWithSession(t, base+"/v1/responses/compact", map[string]any{"model": model, "input": matrixCompactionHistory(t)}, "unsupported-compact-"+model)
			if status != wantStatus {
				t.Fatalf("unconfigured compact-route status = %d, want %d: %s", status, wantStatus, body)
			}
			if model == "gpt-6-sol" && !strings.Contains(string(body), "model_not_found") {
				t.Fatalf("excluded model compact error omitted model_not_found: %s", body)
			}
			if after := upstreamRequestCount(state); after != before {
				t.Fatalf("unconfigured compaction reached model endpoint: before=%d after=%d", before, after)
			}
		})
	}
}

func nativeResponsesProbe(t *testing.T, route nativeCanonicalRoute, stream bool) map[string]any {
	t.Helper()
	var request map[string]any
	if err := json.Unmarshal(readNativeSeed(t, "responses-request.json"), &request); err != nil {
		t.Fatalf("decode synthetic Responses request: %v", err)
	}
	request["model"] = route.model
	request["instructions"] = "Summarize the provided synthetic conversation as ordinary prose."
	request["tools"] = []any{map[string]any{"type": "function", "name": "inspect", "description": "Synthetic fixture tool", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}}}
	if route.path == "/responses" {
		var history []any
		if err := json.Unmarshal(readNativeSeed(t, "responses-history.json"), &history); err != nil {
			t.Fatalf("decode synthetic Responses history: %v", err)
		}
		request["input"] = history
	}
	request["stream"] = stream
	return request
}

func nativeCustomToolProbe(t *testing.T, route nativeCanonicalRoute, stream bool) map[string]any {
	t.Helper()
	request := nativeResponsesProbe(t, route, stream)
	request["input"] = []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "synthetic custom tool routing request"}}}}
	request["tools"] = []any{map[string]any{"type": "custom", "name": "inspect", "description": "Synthetic freeform tool"}}
	return request
}

func setNativeCustomToolResponse(t *testing.T, state *fixture) {
	t.Helper()
	var response map[string]any
	if err := json.Unmarshal(state.seeds["messages.json"], &response); err != nil {
		t.Fatalf("decode synthetic custom-tool Messages seed: %v", err)
	}
	content, ok := response["content"].([]any)
	if !ok {
		t.Fatalf("synthetic Messages content has type %T", response["content"])
	}
	toolUses := 0
	for _, rawBlock := range content {
		block, ok := rawBlock.(map[string]any)
		if ok && block["type"] == "tool_use" {
			block["input"] = map[string]any{"input": "synthetic custom input"}
			toolUses++
		}
	}
	if toolUses != 1 {
		t.Fatalf("synthetic Messages seed has %d tool calls, want one", toolUses)
	}
	updated, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("encode synthetic custom-tool Messages seed: %v", err)
	}
	state.seeds["messages.json"] = updated
	old := []byte(`"partial_json":"{\"query\":\"provider-value\"}"`)
	newValue := []byte(`"partial_json":"{\"input\":\"synthetic custom input\"}"`)
	stream := state.seeds["messages.sse"]
	if bytes.Count(stream, old) != 1 {
		t.Fatalf("synthetic Messages stream seed has %d tool input fragments, want one", bytes.Count(stream, old))
	}
	state.seeds["messages.sse"] = bytes.Replace(stream, old, newValue, 1)
}

func assertNativeCustomToolOutput(t *testing.T, response map[string]any, callID string) string {
	t.Helper()
	items := jsonObjects(response["output"])
	var signedThinking, redactedThinking, responseText bool
	var itemID string
	customCalls := 0
	for _, item := range items {
		switch item["type"] {
		case "reasoning":
			encrypted := stringValue(item["encrypted_content"])
			signedThinking = signedThinking || encrypted == matrixSignature
			redactedThinking = redactedThinking || encrypted == "claude-redacted-thinking:"+matrixRedacted
		case "message":
			responseText = responseText || containsJSONScalar(item["content"], "synthetic Claude response")
		case "custom_tool_call":
			customCalls++
			itemID = stringValue(item["id"])
			if item["call_id"] != callID || item["name"] != "inspect" || item["input"] != "synthetic custom input" {
				t.Fatalf("custom tool output changed native identity or input: %+v", item)
			}
		}
	}
	if !signedThinking || !redactedThinking || !responseText || customCalls != 1 || itemID != "ctc_"+callID {
		t.Fatalf("custom-tool Responses output lost blocks or ID: signed=%v redacted=%v text=%v calls=%d itemID=%q want=%q output=%+v", signedThinking, redactedThinking, responseText, customCalls, itemID, "ctc_"+callID, items)
	}
	return itemID
}

func assertNativeCustomToolReplay(t *testing.T, base string, state *fixture, route nativeCanonicalRoute, initialRequest, response map[string]any, session, itemID, callID string) {
	t.Helper()
	priorInput, ok := initialRequest["input"].([]any)
	if !ok {
		t.Fatalf("initial custom-tool input has type %T", initialRequest["input"])
	}
	input := append([]any(nil), priorInput...)
	for _, item := range jsonObjects(response["output"]) {
		input = append(input, item)
	}
	toolResult := "synthetic custom tool result"
	input = append(input, map[string]any{"type": "custom_tool_call_output", "call_id": callID, "output": toolResult})
	input = append(input, map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Continue the synthetic custom-tool conversation."}}})
	request := map[string]any{"model": route.model, "input": input, "instructions": "Continue the synthetic custom-tool conversation."}
	status, body := postProxyWithSession(t, base+"/v1/responses", request, session)
	if status != http.StatusOK {
		t.Fatalf("custom-tool replay returned status %d: %s", status, body)
	}
	captured, path := lastUpstreamRequest(t, state)
	if path != route.path || captured["model"] != route.model || !containsJSONScalar(captured, "synthetic custom tool routing request") || !containsJSONScalar(captured, "Continue the synthetic custom-tool conversation.") {
		t.Fatalf("custom-tool replay changed full history, model, or route: path=%q request=%+v", path, captured)
	}
	var signedThinking, redactedThinking, assistantText, toolUse, toolResultFound bool
	var toolUseID, toolResultID string
	for _, message := range jsonObjects(captured["messages"]) {
		for _, block := range jsonObjects(message["content"]) {
			switch block["type"] {
			case "text":
				assistantText = assistantText || message["role"] == "assistant" && block["text"] == "synthetic Claude response"
			case "thinking":
				signedThinking = signedThinking || block["signature"] == matrixSignature
			case "redacted_thinking":
				redactedThinking = redactedThinking || block["data"] == matrixRedacted
			case "tool_use":
				toolUseID = stringValue(block["id"])
				toolUse = toolUse || toolUseID == callID && block["name"] == "inspect" && containsJSONScalar(block["input"], "synthetic custom input")
			case "tool_result":
				toolResultID = stringValue(block["tool_use_id"])
				toolResultFound = toolResultFound || toolResultID == callID && containsJSONScalar(block["content"], toolResult)
			}
		}
	}
	if !signedThinking || !redactedThinking || !assistantText || !toolUse || !toolResultFound || toolUseID != toolResultID || itemID != "ctc_"+callID {
		t.Fatalf("custom-tool replay changed assistant text, thinking, or native identity: text=%v signed=%v redacted=%v tool_use=%v tool_result=%v IDs=(%q,%q) Responses item=%q", assistantText, signedThinking, redactedThinking, toolUse, toolResultFound, toolUseID, toolResultID, itemID)
	}
}

func assertNativeCanonicalHistory(t *testing.T, route nativeCanonicalRoute, request map[string]any) {
	t.Helper()
	serialized, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(serialized, []byte("compaction_trigger")) || bytes.Contains(serialized, []byte(`"type":"compaction"`)) {
		t.Fatalf("ordinary summary request reached Copilot with a compaction marker: %s", serialized)
	}
	if bytes.Contains(serialized, []byte("cpa-copilot-bridge:compaction:")) {
		t.Fatalf("ordinary summary request reached Copilot with an alternate compaction marker: %s", serialized)
	}
	if !bytes.Contains(serialized, []byte("Summarize the provided synthetic conversation as ordinary prose.")) {
		t.Fatalf("ordinary Responses summary instruction was omitted: %s", serialized)
	}
	if route.path == "/responses" {
		assertNativeResponsesHistory(t, request["input"])
		return
	}
	for _, expected := range []string{"synthetic native routing request", "fixture result", "inspect", "Summarize the provided synthetic conversation as ordinary prose."} {
		if !bytes.Contains(serialized, []byte(expected)) {
			t.Fatalf("translated %s request omitted %q: %s", route.path, expected, serialized)
		}
	}
}

func assertNativeResponsesHistory(t *testing.T, value any) {
	t.Helper()
	items := jsonObjects(value)
	var userMessage, reasoning, functionCall, functionOutput bool
	for _, item := range items {
		switch item["type"] {
		case "message":
			userMessage = item["role"] == "user" && containsJSONScalar(item["content"], "Remember the active goal: finish the native host bridge.")
		case "reasoning":
			reasoning = item["id"] == matrixCompactReasoningID && item["encrypted_content"] == matrixCompactOpaque
		case "function_call":
			functionCall = item["id"] == matrixCompactFunctionID && item["call_id"] == matrixCompactCallID && item["name"] == "inspect"
		case "function_call_output":
			functionOutput = item["call_id"] == matrixCompactCallID && item["output"] == "original result"
		}
	}
	if !userMessage || !reasoning || !functionCall || !functionOutput {
		t.Fatalf("Responses history changed a seeded block or tool ID: %+v", value)
	}
}

func nativeFixtureModelTurn(t *testing.T, state *fixture, model string) int {
	t.Helper()
	state.mu.Lock()
	defer state.mu.Unlock()
	turn := state.modelTurns[model]
	if turn == 0 {
		t.Fatalf("synthetic provider did not record a request for %q", model)
	}
	return turn
}

func assertNativeCanonicalOutput(t *testing.T, route nativeCanonicalRoute, response map[string]any, turn int) (string, string, string) {
	t.Helper()
	items := jsonObjects(response["output"])
	if len(items) == 0 {
		t.Fatalf("canonical %s response has no output blocks: %+v", route.model, response)
	}
	serialized, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	wantCallID := nativeCanonicalCallID(route, turn)
	wantItemID := ""
	switch route.path {
	case "/responses":
		wantItemID = fmt.Sprintf("fixture-function-item-%d/+", turn)
		if !nativeReasoningIDFound(items, fmt.Sprintf("reasoning_fixture_%d/+", turn), "fixture-encrypted-content/+") {
			t.Fatalf("Responses result dropped encrypted reasoning: %s", serialized)
		}
	case "/chat/completions":
		if !nativeCanonicalOpaqueContains(items, matrixSignature) || !strings.Contains(string(serialized), "synthetic chat response") {
			t.Fatalf("Chat result dropped its opaque reasoning or response text: %s", serialized)
		}
	case "/v1/messages":
		if !nativeCanonicalOpaqueContains(items, matrixSignature) || !nativeCanonicalOpaqueContains(items, matrixRedacted) || !strings.Contains(string(serialized), "synthetic Claude response") {
			t.Fatalf("Messages result dropped signed/redacted thinking or response text: %s", serialized)
		}
	}
	var itemID, callID string
	for _, item := range items {
		if item["type"] == "function_call" || item["type"] == "custom_tool_call" {
			itemID = stringValue(item["id"])
			callID = firstStringValue(item, "call_id", "id")
			if item["name"] != "inspect" {
				t.Fatalf("canonical %s result changed its tool name: %+v", route.model, item)
			}
			if route.path != "/responses" && !strings.Contains(string(serialized), "provider-value") {
				t.Fatalf("canonical %s result changed its tool arguments: %+v", route.model, item)
			}
			break
		}
	}
	if itemID == "" || callID == "" {
		t.Fatalf("canonical %s result omitted its tool IDs: %+v", route.model, items)
	}
	transportCallID := callID
	decodedItemID, decodedCallID, composite := translate.DecodeClaudeToolIDs(callID)
	if composite {
		itemID = decodedItemID
		callID = decodedCallID
	}
	if callID != wantCallID {
		t.Fatalf("canonical %s result call ID = %q, want original %q", route.model, callID, wantCallID)
	}
	if wantItemID != "" && itemID != wantItemID {
		t.Fatalf("canonical %s result item ID = %q, want original %q", route.model, itemID, wantItemID)
	}
	return itemID, transportCallID, callID
}

func nativeCanonicalOpaqueContains(items []map[string]any, expected string) bool {
	for _, item := range items {
		if matrixOpaqueContains(item, expected) {
			return true
		}
	}
	return false
}

func nativeCanonicalCallID(route nativeCanonicalRoute, turn int) string {
	switch route.path {
	case "/responses":
		return fmt.Sprintf("fixture-call-%d/+", turn)
	case "/chat/completions":
		return fmt.Sprintf("fixture-chat-call-%d/+", turn)
	case "/v1/messages":
		return fmt.Sprintf("fixture-messages-tool-%d/+", turn)
	default:
		return ""
	}
}

func assertNativeCanonicalReplay(t *testing.T, base string, state *fixture, route nativeCanonicalRoute, initialRequest, response map[string]any, session, itemID, transportCallID, callID string, turn int) {
	t.Helper()
	priorInput, ok := initialRequest["input"].([]any)
	if !ok {
		t.Fatalf("initial canonical request input has type %T", initialRequest["input"])
	}
	output := jsonObjects(response["output"])
	input := make([]any, 0, len(priorInput)+len(output)+2)
	input = append(input, priorInput...)
	for _, item := range output {
		input = append(input, item)
	}
	toolResult := fmt.Sprintf("synthetic canonical tool result after turn %d", turn)
	continuation := fmt.Sprintf("Continue the %s synthetic conversation.", route.model)
	input = append(input, map[string]any{"type": "function_call_output", "call_id": transportCallID, "output": toolResult})
	input = append(input, map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": continuation}}})
	request := map[string]any{"model": route.model, "input": input, "instructions": "Continue the same synthetic conversation."}
	before := upstreamRequestCount(state)
	status, responseBody := postProxyWithSession(t, base+"/v1/responses", request, session)
	if status != http.StatusOK {
		t.Fatalf("canonical replay for %q returned status %d after %d synthetic upstream requests: %s", route.model, status, upstreamRequestCount(state)-before, responseBody)
	}
	captured, path := lastUpstreamRequest(t, state)
	if path != route.path || captured["model"] != route.model {
		t.Fatalf("same-model replay for %q used path/model %q/%v", route.model, path, captured["model"])
	}
	serialized, err := json.Marshal(captured)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(serialized, []byte(toolResult)) || !bytes.Contains(serialized, []byte(continuation)) || bytes.Contains(serialized, []byte("cpa-copilot-bridge:compaction:")) {
		t.Fatalf("same-model replay dropped tool output or added a compaction marker: %s", serialized)
	}
	switch route.path {
	case "/responses":
		if !nativeReasoningIDFound(jsonObjects(captured["input"]), fmt.Sprintf("reasoning_fixture_%d/+", turn), "fixture-encrypted-content/+") || !nativeToolIDsInHistory(captured["input"], itemID, callID, toolResult) {
			t.Fatalf("Responses replay changed thinking or original tool IDs: %+v", captured["input"])
		}
	case "/chat/completions":
		messages := jsonObjects(captured["messages"])
		var reasoning, responseText, toolCall, toolOutput bool
		for _, message := range messages {
			reasoning = reasoning || message["role"] == "assistant" && matrixOpaqueContains(message["reasoning_opaque"], matrixSignature)
			responseText = responseText || message["role"] == "assistant" && containsJSONScalar(message["content"], "synthetic chat response")
			for _, call := range jsonObjects(message["tool_calls"]) {
				id := stringValue(call["id"])
				decodedItemID, decodedCallID, composite := translate.DecodeClaudeToolIDs(id)
				toolCall = toolCall || id == callID && itemID == callID || composite && decodedItemID == itemID && decodedCallID == callID
			}
			outputID := stringValue(message["tool_call_id"])
			decodedItemID, decodedCallID, composite := translate.DecodeClaudeToolIDs(outputID)
			outputMatches := outputID == callID && itemID == callID || composite && decodedItemID == itemID && decodedCallID == callID
			toolOutput = toolOutput || message["role"] == "tool" && outputMatches && containsJSONScalar(message["content"], toolResult)
		}
		if !reasoning || !responseText || !toolCall || !toolOutput {
			t.Fatalf("Chat replay dropped signed reasoning or original tool correlation: reasoning=%v text=%v call=%v output=%v", reasoning, responseText, toolCall, toolOutput)
		}
	case "/v1/messages":
		messages := jsonObjects(captured["messages"])
		var thinkingText, signature, redacted, assistantText, toolUse, toolOutput bool
		var actualToolUseID, actualToolResultID string
		for _, message := range messages {
			for _, block := range jsonObjects(message["content"]) {
				switch block["type"] {
				case "text":
					assistantText = assistantText || message["role"] == "assistant" && block["text"] == "synthetic Claude response"
				case "thinking":
					thinkingText = thinkingText || containsJSONScalar(block["thinking"], "provider reasoning exact")
					signature = signature || block["signature"] == matrixSignature
				case "redacted_thinking":
					redacted = redacted || block["data"] == matrixRedacted
				case "tool_use":
					actualToolUseID = stringValue(block["id"])
					toolUse = toolUse || block["id"] == callID
				case "tool_result":
					actualToolResultID = stringValue(block["tool_use_id"])
					toolOutput = toolOutput || block["tool_use_id"] == callID && containsJSONScalar(block["content"], toolResult)
				}
			}
		}
		if !thinkingText || !signature || !redacted || !assistantText || !toolUse || !toolOutput {
			t.Fatalf("Messages replay dropped assistant text, signed/redacted thinking, or original tool correlation: text=%v thinking=%v signature=%v redacted=%v tool_use.id=%q tool_result.tool_use_id=%q want=%q", assistantText, thinkingText, signature, redacted, actualToolUseID, actualToolResultID, callID)
		}
	}
}

func nativeToolIDsInHistory(value any, itemID, callID, result string) bool {
	items := jsonObjects(value)
	var functionFound, outputFound bool
	for _, item := range items {
		functionFound = functionFound || item["type"] == "function_call" && item["id"] == itemID && item["call_id"] == callID
		outputFound = outputFound || item["type"] == "function_call_output" && item["call_id"] == callID && item["output"] == result
	}
	return functionFound && outputFound
}

func nativeReasoningIDFound(items []map[string]any, id, encryptedContent string) bool {
	for _, item := range items {
		if item["type"] == "reasoning" && item["id"] == id && item["encrypted_content"] == encryptedContent {
			return true
		}
	}
	return false
}

func nativeCatalogModelIDs(t *testing.T, catalog []byte) map[string]struct{} {
	t.Helper()
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(catalog, &result); err != nil {
		t.Fatalf("decode synthetic model catalog: %v", err)
	}
	models := make(map[string]struct{}, len(result.Data))
	for _, model := range result.Data {
		models[model.ID] = struct{}{}
	}
	return models
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
