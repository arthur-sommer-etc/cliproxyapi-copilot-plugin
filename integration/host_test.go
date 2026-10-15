package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
)

type fixture struct {
	mu                sync.Mutex
	requests          []map[string]any
	paths             []string
	canceled          chan struct{}
	oauthRefreshCount int
	githubUserAuth    []string
	seeds             map[string][]byte
	modelCatalog      []byte
	modelTurns        map[string]int
}

const (
	matrixChatModel                  = "bridge-gemini-3.8"
	matrixResponsesModel             = "bridge-gpt-6-luna"
	matrixMessagesModel              = "bridge-sonnet-5.5"
	matrixChatMessagesModel          = "bridge-chat-sonnet-5.5"
	matrixSignature                  = "provider-signature/+ exact\n\t "
	matrixRedacted                   = "provider-redacted/+ exact\n\t "
	matrixReasoning                  = "provider reasoning exact"
	matrixOutputText                 = "provider text exact"
	matrixToolName                   = "inspect"
	matrixToolArguments              = `{"query":"provider-value"}`
	matrixInitialSignature           = "initial-signature/+ exact\n\t "
	matrixInitialRedacted            = "initial-redacted/+ exact\n\t "
	matrixInputMarker                = "matrix user history exact"
	matrixContinuation               = "matrix continuation exact"
	matrixThirdContinuation          = "matrix third continuation exact"
	matrixToolResult                 = "matrix tool result exact"
	matrixCompactReasoningID         = "reasoning_compaction_original/+"
	matrixCompactFunctionID          = "function_compaction_original/+"
	matrixCompactCallID              = "call_compaction_original/+"
	matrixCompactOpaque              = "copilot-opaque/+ encrypted content exact"
	matrixCompactCacheKey            = "compact-history-cache-key"
	matrixChatInitialThinkingEffort  = "low"
	matrixChatFollowupThinkingEffort = "high"
)

var (
	fixtureResponsesFunctionItemID = strings.Repeat("f", 424)
	fixtureResponsesReasoningID    = "reasoning/+" + strings.Repeat("r", 64-len("reasoning/+"))
	fixtureResponsesCallID         = strings.Repeat("c", 29)
	matrixChatCallID               = "chat-call/+" + strings.Repeat("h", 64-len("chat-call/+"))
	matrixResponseItemID           = strings.Repeat("i", 424)
	matrixResponseCallID           = strings.Repeat("c", 29)
	matrixMessagesToolID           = "toolu_provider_" + strings.Repeat("p", 64-len("toolu_provider_"))
	matrixInitialItemID            = strings.Repeat("j", 424)
	matrixInitialCallID            = strings.Repeat("k", 29)
)

type matrixRoute struct {
	name         string
	clientFormat string
	model        string
	upstreamPath string
}

type matrixOutput struct {
	Model         string
	Turn          int
	Signature     string
	Redacted      string
	ChatOpaque    any
	Text          string
	ClaudeToolID  string
	ItemID        string
	CallID        string
	ToolName      string
	ToolArguments string
	ResponseItems []map[string]any
}

type nativeCodexModel struct {
	Slug                     string                     `json:"slug"`
	ContextWindow            int                        `json:"context_window"`
	MaxContextWindow         int                        `json:"max_context_window"`
	SupportedReasoningLevels []nativeCodexThinkingLevel `json:"supported_reasoning_levels"`
}

type nativeCodexThinkingLevel struct {
	Effort string `json:"effort"`
}

func TestNativeHostProtocolRoundTrips(t *testing.T) {
	binary := os.Getenv("CPA_BINARY")
	if binary == "" {
		t.Skip("set CPA_BINARY to a built CLIProxyAPI v8 server for native host integration")
	}
	state := newNativeFixture(t)
	upstream := httptest.NewServer(state)
	t.Cleanup(upstream.Close)
	base := startProxy(t, binary, upstream.URL)
	longID := fixtureResponsesFunctionItemID
	items := []any{
		map[string]any{"type": "reasoning", "id": fixtureResponsesReasoningID, "summary": []any{}, "encrypted_content": "copilot-opaque/+not-fernet"},
		map[string]any{"type": "function_call", "id": longID, "call_id": fixtureResponsesCallID, "name": "inspect", "arguments": "{}"},
		map[string]any{"type": "function_call_output", "call_id": fixtureResponsesCallID, "output": "ok"},
	}
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("ResponsesStream%v", stream), func(t *testing.T) {
			request := map[string]any{"model": "bridge-responses", "stream": stream, "input": items, "store": false, "previous_response_id": "provider_previous/+", "temperature": 0.6, "prompt_cache_key": "caller-key"}
			response := callProxy(t, base+"/v1/responses", request)
			state.mu.Lock()
			captured := state.requests[len(state.requests)-1]
			state.mu.Unlock()
			assertEqualJSON(t, captured["input"], items)
			if captured["previous_response_id"] != request["previous_response_id"] || captured["prompt_cache_key"] != "caller-key" || captured["temperature"] != 0.6 {
				t.Fatalf("native request state changed: %+v", captured)
			}
			if !bytes.Contains(response, []byte("copilot-opaque/+not-fernet")) || !bytes.Contains(response, []byte(longID)) || !bytes.Contains(response, []byte(fixtureResponsesCallID)) {
				t.Fatalf("native output lost opaque state: %s", response)
			}
			if stream && bytes.Count(response, []byte(`"type":"response.completed"`)) != 1 {
				t.Fatalf("expected one terminal event: %s", response)
			}
		})
	}
	t.Run("ResponsesClientRemovedItemIDs", func(t *testing.T) {
		withoutIDs := []any{
			map[string]any{"type": "reasoning", "summary": []any{}, "encrypted_content": "copilot-opaque/+not-fernet"},
			map[string]any{"type": "function_call", "call_id": fixtureResponsesCallID, "name": "inspect", "arguments": "{}"},
			map[string]any{"type": "function_call_output", "call_id": fixtureResponsesCallID, "output": "ok"},
		}
		callProxy(t, base+"/v1/responses", map[string]any{"model": "bridge-responses", "input": withoutIDs})
		state.mu.Lock()
		captured := state.requests[len(state.requests)-1]
		state.mu.Unlock()
		assertEqualJSON(t, captured["input"], items)
	})
	t.Run("ClaudeNativeSignedThinking", func(t *testing.T) {
		messages := []any{map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "thinking": "", "signature": "claude-opaque/+"},
			map[string]any{"type": "redacted_thinking", "data": "redacted/+"},
			map[string]any{"type": "tool_use", "id": "tool_safe", "name": "inspect", "input": map[string]any{}},
		}}, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "tool_safe", "content": "ok"}}}}
		response := callProxy(t, base+"/v1/messages", map[string]any{"model": "bridge-messages", "max_tokens": 128, "messages": messages})
		state.mu.Lock()
		captured := state.requests[len(state.requests)-1]
		path := state.paths[len(state.paths)-1]
		state.mu.Unlock()
		if path != "/v1/messages" {
			t.Fatalf("native Claude request used %s", path)
		}
		assertEqualJSON(t, captured["messages"], messages)
		if !bytes.Contains(response, []byte("claude-opaque/+")) {
			t.Fatalf("Claude signature missing: %s", response)
		}
	})
	t.Run("ChatNative", func(t *testing.T) {
		messages := []any{map[string]any{"role": "user", "content": "hello"}}
		response := callProxy(t, base+"/v1/chat/completions", map[string]any{"model": "bridge-chat", "messages": messages, "prompt_cache_key": "explicit-chat-key"})
		state.mu.Lock()
		captured := state.requests[len(state.requests)-1]
		path := state.paths[len(state.paths)-1]
		state.mu.Unlock()
		if path != "/chat/completions" || captured["prompt_cache_key"] != "explicit-chat-key" {
			t.Fatalf("chat route or cache key changed: %s %+v", path, captured)
		}
		assertEqualJSON(t, captured["messages"], messages)
		if !bytes.Contains(response, []byte("chat.completion")) {
			t.Fatalf("chat response malformed: %s", response)
		}
	})
	t.Run("ClientCancellationClosesUpstream", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		body := []byte(`{"model":"bridge-responses","stream":true,"input":"Wait for cancellation","fixture_cancel":true}`)
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/responses", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer fixture-client-key")
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("stream did not start: status=%d", response.StatusCode)
		}
		if _, err := io.ReadFull(response.Body, make([]byte, 1)); err != nil {
			t.Fatalf("stream produced no frame before cancellation: %v", err)
		}
		cancel()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		select {
		case <-state.canceled:
		case <-deadline.C:
			t.Fatal("native plugin did not cancel the upstream HTTP stream")
		}
	})
	t.Run("ResponsesCompactionTriggerRoundTrip", func(t *testing.T) {
		input := append(matrixCompactionHistory(t), map[string]any{"type": "compaction_trigger"})
		response := callProxy(t, base+"/v1/responses", map[string]any{"model": "gpt-6-luna", "input": input, "prompt_cache_key": matrixCompactCacheKey})
		captured, path := lastUpstreamRequest(t, state)
		assertCompactionRequest(t, captured, path)
		capsule := assertSingleCompaction(t, response)
		assertCompactionReplay(t, base, state, capsule, "gpt-6-luna", "Continue the bridge task.", false)
	})
	t.Run("ResponsesCompactionTriggerStreamingRoundTrip", func(t *testing.T) {
		input := append(matrixCompactionHistory(t), map[string]any{"type": "compaction_trigger"})
		stream := callProxy(t, base+"/v1/responses", map[string]any{"model": "gpt-6-luna", "stream": true, "input": input, "prompt_cache_key": matrixCompactCacheKey})
		captured, path := lastUpstreamRequest(t, state)
		assertCompactionRequest(t, captured, path)
		events := parseSSEDataEvents(t, stream)
		if len(events) < 4 {
			t.Fatalf("compaction stream has too few events: %s", stream)
		}
		assertEventType(t, events[0], "response.created")
		assertEventType(t, events[1], "response.in_progress")
		for i, event := range events {
			assertEqualJSON(t, event["sequence_number"], i)
		}
		terminal := events[len(events)-1]
		assertEventType(t, terminal, "response.completed")
		completed, ok := terminal["response"].(map[string]any)
		if !ok {
			t.Fatalf("completed event has no response object: %+v", terminal)
		}
		if !strings.HasPrefix(stringValue(completed["id"]), "resp_fixture_") || completed["status"] != "completed" {
			t.Fatalf("completed response metadata changed: %+v", completed)
		}
		usage, ok := completed["usage"].(map[string]any)
		if !ok {
			t.Fatalf("completed response usage has type %T", completed["usage"])
		}
		assertEqualJSON(t, usage["input_tokens"], 1)
		assertEqualJSON(t, usage["output_tokens"], 1)
		assertEqualJSON(t, usage["total_tokens"], 2)
		completedBody, err := json.Marshal(completed)
		if err != nil {
			t.Fatal(err)
		}
		capsule := assertSingleCompaction(t, completedBody)
		output, ok := completed["output"].([]any)
		if !ok {
			t.Fatalf("completed response output has type %T", completed["output"])
		}
		if len(events) != 3+2*len(output) {
			t.Fatalf("stream emitted %d events for %d output items: %s", len(events), len(output), stream)
		}
		var summaryFound bool
		var capsuleDoneCount int
		for i, item := range output {
			added := events[2+2*i]
			done := events[3+2*i]
			assertEventType(t, added, "response.output_item.added")
			assertEventType(t, done, "response.output_item.done")
			assertEqualJSON(t, added["output_index"], i)
			assertEqualJSON(t, done["output_index"], i)
			assertEqualJSON(t, added["item"], item)
			assertEqualJSON(t, done["item"], item)
			itemMap, ok := item.(map[string]any)
			if !ok {
				t.Fatalf("completed output item %d has type %T", i, item)
			}
			if strings.HasPrefix(stringValue(itemMap["id"]), "summary_fixture_") {
				summaryFound = true
				assertEqualJSON(t, itemMap["type"], "message")
				assertEqualJSON(t, itemMap["status"], "completed")
				assertEqualJSON(t, itemMap["role"], "assistant")
				assertEqualJSON(t, itemMap["content"], []any{map[string]any{"type": "output_text", "text": "The active goal is to finish the native host bridge. The next step is integration."}})
			}
			if itemMap["type"] == "compaction" {
				capsuleDoneCount++
				if itemMap["encrypted_content"] != capsule || doneItemType(done) != "compaction" {
					t.Fatalf("terminal compaction item differs from its done event: output=%+v done=%+v", itemMap, done)
				}
			}
		}
		if !summaryFound || capsuleDoneCount != 1 {
			t.Fatalf("stream did not preserve the summary and exactly one completed capsule: summary=%v capsules=%d", summaryFound, capsuleDoneCount)
		}
		assertCompactionReplay(t, base, state, capsule, "gpt-6-luna", "Continue the streamed bridge task.", true)
	})
	t.Run("ResponsesCompactRouteRoundTrip", func(t *testing.T) {
		request := map[string]any{
			"model":            "gpt-6-luna",
			"input":            matrixCompactionHistory(t),
			"prompt_cache_key": matrixCompactCacheKey,
		}
		response := callProxy(t, base+"/v1/responses/compact", request)
		captured, path := lastUpstreamRequest(t, state)
		assertCompactionRequest(t, captured, path)
		capsule := assertSingleCompaction(t, response)
		assertCompactionReplay(t, base, state, capsule, "gpt-6-luna", "Continue after the compact route.", false)
	})
	t.Run("ForeignOpaqueReasoningToGeminiChatFailsClosed", func(t *testing.T) {
		cases := []struct {
			name         string
			clientFormat string
			stream       bool
		}{
			{name: "ClaudeBuffered", clientFormat: "claude"},
			{name: "ClaudeStream", clientFormat: "claude", stream: true},
			{name: "ResponsesBuffered", clientFormat: "openai-response"},
			{name: "ResponsesStream", clientFormat: "openai-response", stream: true},
		}
		for _, testCase := range cases {
			testCase := testCase
			t.Run(testCase.name, func(t *testing.T) {
				before := upstreamRequestCount(state)
				status, body := postProxyWithSession(t, base+matrixClientPath(testCase.clientFormat), foreignOpaqueChatRequest(testCase.clientFormat, testCase.stream), "foreign-opaque-"+testCase.name)
				if status != http.StatusUnprocessableEntity {
					t.Fatalf("foreign opaque %s request status = %d, want 422; body=%s", testCase.name, status, body)
				}
				if after := upstreamRequestCount(state); after != before {
					t.Fatalf("foreign opaque %s request reached model endpoint: before=%d after=%d", testCase.name, before, after)
				}
			})
		}
	})
	t.Run("NineClientProviderProtocolRoutes", func(t *testing.T) {
		routes := []matrixRoute{
			{name: "ClaudeToGeminiChat", clientFormat: "claude", model: matrixChatModel, upstreamPath: "/chat/completions"},
			{name: "ResponsesToGeminiChat", clientFormat: "openai-response", model: matrixChatModel, upstreamPath: "/chat/completions"},
			{name: "ChatToGeminiChat", clientFormat: "openai", model: matrixChatModel, upstreamPath: "/chat/completions"},
			{name: "ClaudeToGPT6Luna", clientFormat: "claude", model: matrixResponsesModel, upstreamPath: "/responses"},
			{name: "ResponsesToGPT6Luna", clientFormat: "openai-response", model: matrixResponsesModel, upstreamPath: "/responses"},
			{name: "ChatToGPT6Luna", clientFormat: "openai", model: matrixResponsesModel, upstreamPath: "/responses"},
			{name: "ClaudeToSonnet55", clientFormat: "claude", model: matrixMessagesModel, upstreamPath: "/v1/messages"},
			{name: "ResponsesToSonnet55", clientFormat: "openai-response", model: matrixMessagesModel, upstreamPath: "/v1/messages"},
			{name: "ChatToSonnet55", clientFormat: "openai", model: matrixChatMessagesModel, upstreamPath: "/v1/messages"},
		}
		for _, route := range routes {
			for _, stream := range []bool{false, true} {
				route, stream := route, stream
				t.Run(fmt.Sprintf("%s/Stream%v", route.name, stream), func(t *testing.T) {
					session := fmt.Sprintf("matrix-%s-%t", route.name, stream)
					firstRequest := matrixInitialRequest(route, stream)
					firstResponse := callProxyWithSession(t, base+matrixClientPath(route.clientFormat), firstRequest, session)
					firstCaptured, firstPath := lastUpstreamRequest(t, state)
					assertMatrixRoute(t, route, firstCaptured, firstPath, stream)
					assertMatrixInitialRequest(t, route, firstCaptured)
					output := assertMatrixClientOutput(t, route, firstResponse, stream)
					followup := matrixFollowupRequest(route, output)
					assertChatClientToolResultEcho(t, route, followup, output)
					followupResponse := callProxyWithSession(t, base+matrixClientPath(route.clientFormat), followup, session)
					followupCaptured, followupPath := lastUpstreamRequest(t, state)
					assertMatrixRoute(t, route, followupCaptured, followupPath, false)
					assertMatrixFollowupRequest(t, route, followupCaptured, output)
					if route.name == "ChatToSonnet55" {
						secondOutput := assertMatrixClientOutputTurn(t, route, followupResponse, false, 2)
						thirdRequest := matrixThirdTurnRequest(t, route, output, secondOutput)
						assertChatClientToolResultEcho(t, route, thirdRequest, secondOutput)
						thirdResponse := callProxyWithSession(t, base+matrixClientPath(route.clientFormat), thirdRequest, session)
						thirdCaptured, thirdPath := lastUpstreamRequest(t, state)
						assertMatrixRoute(t, route, thirdCaptured, thirdPath, false)
						assertChatClientThirdTurnRequest(t, thirdCaptured, output, secondOutput)
						assertMatrixClientOutputTurn(t, route, thirdResponse, false, 3)
					}
				})
			}
		}
	})
	t.Run("ClaudeCodeMessagesToChatAndResponses", func(t *testing.T) {
		routes := []matrixRoute{
			{name: "ClaudeCodeToGeminiChat", clientFormat: "claude", model: matrixChatModel, upstreamPath: "/chat/completions"},
			{name: "ClaudeCodeToLunaResponses", clientFormat: "claude", model: matrixResponsesModel, upstreamPath: "/responses"},
		}
		for _, route := range routes {
			for _, stream := range []bool{false, true} {
				route, stream := route, stream
				t.Run(fmt.Sprintf("%s/Stream%v", route.name, stream), func(t *testing.T) {
					response := callProxyWithSession(t, base+"/v1/messages", nativeClaudeCodeRequest(route.model, stream), fmt.Sprintf("claude-code-%s-%t", route.model, stream))
					if stream && !bytes.Contains(response, []byte("message_stop")) {
						t.Fatalf("Claude stream omitted message_stop: %s", response)
					}
					captured, path := lastUpstreamRequest(t, state)
					assertNativeClaudeCodeRequest(t, route, captured, path, stream)
				})
			}
		}
		t.Run("ClaudeCodeFlatParametersToLunaResponses", func(t *testing.T) {
			route := matrixRoute{name: "ClaudeCodeFlatParametersToLunaResponses", clientFormat: "claude", model: matrixResponsesModel, upstreamPath: "/responses"}
			for _, stream := range []bool{false, true} {
				stream := stream
				t.Run(fmt.Sprintf("Stream%v", stream), func(t *testing.T) {
					parameters := map[string]any{
						"type": "object",
						"properties": map[string]any{
							"file_path": map[string]any{"type": "string", "description": "Fixture-relative path."},
							"max_bytes": map[string]any{"type": "integer", "minimum": 1},
						},
						"required":             []any{"file_path"},
						"additionalProperties": false,
					}
					tool := map[string]any{"type": "function", "name": "Read", "description": "Read a synthetic fixture file.", "parameters": parameters}
					request := nativeClaudeCodeRequest(route.model, stream)
					request["tools"] = []any{tool}
					request["tool_choice"] = map[string]any{"type": "tool", "name": "Read"}
					response := callProxyWithSession(t, base+"/v1/messages", request, fmt.Sprintf("claude-code-flat-%t", stream))
					if stream && !bytes.Contains(response, []byte("message_stop")) {
						t.Fatalf("Claude stream omitted message_stop: %s", response)
					}
					captured, path := lastUpstreamRequest(t, state)
					assertMatrixRoute(t, route, captured, path, stream)
					tools := jsonObjects(captured["tools"])
					if len(tools) != 1 {
						t.Fatalf("Responses request changed the caller tool set: %+v", captured["tools"])
					}
					assertEqualJSON(t, tools[0], tool)
					assertEqualJSON(t, captured["tool_choice"], map[string]any{"type": "function", "name": "Read"})
				})
			}
		})
	})
}

func nativeClaudeCodeRequest(model string, stream bool) map[string]any {
	return map[string]any{
		"model":            model,
		"max_tokens":       512,
		"stream":           stream,
		"prompt_cache_key": "claude-code-native-cache-key",
		"system": []any{
			map[string]any{"type": "text", "text": "Synthetic Claude Code base instructions."},
			map[string]any{"type": "text", "text": "Synthetic cached workspace rules.", "cache_control": map[string]any{"type": "ephemeral"}},
		},
		"tools": []any{
			map[string]any{"name": "Read", "description": "Read a synthetic fixture file.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"file_path": map[string]any{"type": "string"}}, "required": []any{"file_path"}}},
			map[string]any{"name": "Bash", "description": "Run a synthetic command.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}, "required": []any{"command"}}},
		},
		"tool_choice": map[string]any{"type": "auto"},
		"messages": []any{
			map[string]any{"role": "user", "content": "Synthetic request before the system update."},
			map[string]any{"role": "system", "content": "Synthetic mid-conversation safety instruction."},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "I will inspect the synthetic fixture."},
				map[string]any{"type": "tool_use", "id": "toolu_read_fixture", "name": "Read", "input": map[string]any{"file_path": "fixture.txt"}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "toolu_read_fixture", "content": "Synthetic fixture contents."},
				map[string]any{"type": "text", "text": "Continue using the fixture contents."},
			}},
		},
	}
}

func assertNativeClaudeCodeRequest(t *testing.T, route matrixRoute, captured map[string]any, path string, stream bool) {
	t.Helper()
	assertMatrixRoute(t, route, captured, path, stream)
	tools := jsonObjects(captured["tools"])
	for _, want := range []struct {
		name     string
		property string
	}{
		{name: "Read", property: "file_path"},
		{name: "Bash", property: "command"},
	} {
		var parameters map[string]any
		for _, tool := range tools {
			if tool["type"] != "function" {
				continue
			}
			if route.upstreamPath == "/responses" && tool["name"] == want.name {
				parameters, _ = tool["parameters"].(map[string]any)
			}
			if route.upstreamPath == "/chat/completions" {
				function, _ := tool["function"].(map[string]any)
				if function["name"] == want.name {
					parameters, _ = function["parameters"].(map[string]any)
				}
			}
		}
		if parameters == nil {
			t.Fatalf("Claude function tool %q was removed: %+v", want.name, captured["tools"])
		}
		if !containsJSONScalar(parameters, want.property) {
			t.Fatalf("Claude tool %q lost its input schema: %+v", want.name, parameters)
		}
	}
	if stringValue(captured["tool_choice"]) != "auto" {
		t.Fatalf("Claude automatic tool choice changed: %+v", captured["tool_choice"])
	}
	if captured["prompt_cache_key"] != "claude-code-native-cache-key" {
		t.Fatalf("Claude prompt cache key changed: %+v", captured["prompt_cache_key"])
	}
	if route.upstreamPath == "/chat/completions" {
		messages := jsonObjects(captured["messages"])
		globalSystem := nativeClaudeMessageIndex(messages, "system", "Synthetic Claude Code base instructions.")
		cachedSystem := nativeClaudeMessageIndex(messages, "system", "Synthetic cached workspace rules.")
		firstUser := nativeClaudeMessageIndex(messages, "user", "Synthetic request before the system update.")
		midSystem := nativeClaudeMessageIndex(messages, "system", "Synthetic mid-conversation safety instruction.")
		assistant := nativeClaudeMessageIndex(messages, "assistant", "I will inspect the synthetic fixture.")
		toolCall, toolCallID := nativeClaudeToolCallIndex(messages, "Read")
		toolResult := nativeClaudeToolResultIndex(messages, toolCallID, "Synthetic fixture contents.")
		continuation := nativeClaudeMessageIndex(messages, "user", "Continue using the fixture contents.")
		if globalSystem < 0 || cachedSystem < 0 || firstUser < 0 || midSystem < 0 || assistant < 0 || toolCall < 0 || toolCallID == "" || toolResult < 0 || continuation < 0 || !(globalSystem < firstUser && firstUser < midSystem && midSystem < assistant && assistant <= toolCall && toolCall < toolResult && toolResult < continuation) {
			t.Fatalf("Claude Chat request changed system-message order or tool history: indexes=%d,%d,%d,%d,%d,%d,%d,%q messages=%+v", globalSystem, cachedSystem, firstUser, midSystem, assistant, toolCall, toolResult, toolCallID, messages)
		}
		if !nativeClaudeCacheControlForText(jsonObjects(messages[globalSystem]["content"]), "Synthetic cached workspace rules.") {
			t.Fatalf("Claude Chat request lost the top-level system cache boundary: %+v", messages[globalSystem])
		}
		return
	}
	items := jsonObjects(captured["input"])
	globalSystem := nativeResponsesMessageIndex(items, "system", "Synthetic Claude Code base instructions.")
	cachedSystem := nativeResponsesMessageIndex(items, "system", "Synthetic cached workspace rules.")
	firstUser := nativeResponsesMessageIndex(items, "user", "Synthetic request before the system update.")
	midSystem := nativeResponsesMessageIndex(items, "system", "Synthetic mid-conversation safety instruction.")
	assistant := nativeResponsesMessageIndex(items, "assistant", "I will inspect the synthetic fixture.")
	toolCall, toolCallID := nativeResponsesToolCallIndex(items, "Read")
	toolResult := nativeResponsesToolResultIndex(items, toolCallID, "Synthetic fixture contents.")
	continuation := nativeResponsesMessageIndex(items, "user", "Continue using the fixture contents.")
	if globalSystem < 0 || cachedSystem < 0 || firstUser < 0 || midSystem < 0 || assistant < 0 || toolCall < 0 || toolCallID == "" || toolResult < 0 || continuation < 0 || !(globalSystem < firstUser && firstUser < midSystem && midSystem < assistant && assistant < toolCall && toolCall < toolResult && toolResult < continuation) {
		t.Fatalf("Claude Responses request changed system-message order or tool history: %+v", items)
	}
}

func nativeClaudeMessageIndex(messages []map[string]any, role, text string) int {
	for index, message := range messages {
		if message["role"] == role && containsJSONScalar(message["content"], text) {
			return index
		}
	}
	return -1
}

func nativeClaudeToolCallIndex(messages []map[string]any, name string) (int, string) {
	for index, message := range messages {
		for _, call := range jsonObjects(message["tool_calls"]) {
			function, _ := call["function"].(map[string]any)
			if function["name"] == name {
				return index, stringValue(call["id"])
			}
		}
	}
	return -1, ""
}

func nativeClaudeToolResultIndex(messages []map[string]any, callID, text string) int {
	for index, message := range messages {
		if message["role"] == "tool" && message["tool_call_id"] == callID && containsJSONScalar(message["content"], text) {
			return index
		}
	}
	return -1
}

func nativeResponsesMessageIndex(items []map[string]any, role, text string) int {
	for index, item := range items {
		if item["type"] == "message" && item["role"] == role && containsJSONScalar(item["content"], text) {
			return index
		}
	}
	return -1
}

func nativeResponsesToolCallIndex(items []map[string]any, name string) (int, string) {
	for index, item := range items {
		if item["type"] == "function_call" && item["name"] == name {
			return index, stringValue(item["call_id"])
		}
	}
	return -1, ""
}

func nativeResponsesToolResultIndex(items []map[string]any, callID, text string) int {
	for index, item := range items {
		if item["type"] == "function_call_output" && item["call_id"] == callID && containsJSONScalar(item["output"], text) {
			return index
		}
	}
	return -1
}

func nativeClaudeCacheControlForText(blocks []map[string]any, text string) bool {
	for _, block := range blocks {
		cacheControl, _ := block["cache_control"].(map[string]any)
		if block["text"] == text && cacheControl["type"] == "ephemeral" {
			return true
		}
	}
	return false
}

func TestNativeHostOAuthExcludedModelsFilterPluginModels(t *testing.T) {
	binary := os.Getenv("CPA_BINARY")
	if binary == "" {
		t.Skip("set CPA_BINARY to a built CLIProxyAPI v8 server for native host integration")
	}
	state := newNativeFixture(t)
	upstream := httptest.NewServer(state)
	defer upstream.Close()
	base, _ := startProxyInRoot(t, binary, upstream.URL, t.TempDir(), nil, "")
	request, err := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer fixture-client-key")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("list native models: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("list native models: status=%d body=%s", response.StatusCode, body)
	}
	var catalog struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&catalog); err != nil {
		t.Fatalf("decode native model catalog: %v", err)
	}
	models := make(map[string]struct{}, len(catalog.Data))
	for _, model := range catalog.Data {
		models[model.ID] = struct{}{}
	}
	for _, excluded := range []string{"gpt-5.5", "gpt-5-copilot-only", "gpt-6-sol", "claude-fable-5", "claude-sonnet-4.8-fast", "claude-sonnet-4-8-fast", "claude-sonnet-5", "claude-opus-4.8-fast", "claude-opus-4-8-fast", "claude-opus-5", "gemini-3.7-flash", "grok-4.5", "grok-4.6"} {
		if _, exists := models[excluded]; exists {
			t.Fatalf("native OAuth exclusions retained %q in the model catalog", excluded)
		}
	}
	for _, removedAlias := range []string{"gpt-6-1-sol", "gemini-3-8-flash", "gemini-flash-3.8", "gemini-flash-3-8", "mai-code-1-1-flash"} {
		if _, exists := models[removedAlias]; exists {
			t.Fatalf("removed model alias %q remains in the model catalog", removedAlias)
		}
	}
	for _, retained := range []string{"gpt-6.1-sol", "gpt-6-luna", "claude-fable-5.1", "claude-fable-5-1", "claude-sonnet-5.5", "claude-sonnet-5-5", "claude-opus-5.5", "claude-opus-5-5", "gemini-3.8-flash", "mai-code-1.1-flash", "grok-4.7"} {
		if _, exists := models[retained]; !exists {
			t.Fatalf("native OAuth exclusions removed unrelated eligible model %q", retained)
		}
	}
	for _, route := range []struct {
		path         string
		model        string
		upstreamID   string
		upstreamPath string
		request      map[string]any
	}{
		{path: "/v1/messages", model: "claude-sonnet-5-5", upstreamID: "claude-sonnet-5.5", upstreamPath: "/v1/messages", request: map[string]any{"max_tokens": 128, "messages": []any{map[string]any{"role": "user", "content": "alias"}}}},
		{path: "/v1/responses", model: "claude-opus-5-5", upstreamID: "claude-opus-5.5", upstreamPath: "/v1/messages", request: map[string]any{"input": "alias"}},
		{path: "/v1/chat/completions", model: "gemini-3.8-flash", upstreamID: "gemini-3.8-flash", upstreamPath: "/chat/completions", request: map[string]any{"messages": []any{map[string]any{"role": "user", "content": "canonical model"}}}},
		{path: "/v1/responses", model: "gemini-3.8-flash", upstreamID: "gemini-3.8-flash", upstreamPath: "/chat/completions", request: map[string]any{"input": "canonical model"}},
		{path: "/v1/responses", model: "mai-code-1.1-flash", upstreamID: "mai-code-1.1-flash", upstreamPath: "/responses", request: map[string]any{"input": "canonical model"}},
		{path: "/v1/responses", model: "gpt-6.1-sol", upstreamID: "gpt-6.1-sol", upstreamPath: "/responses", request: map[string]any{"input": "canonical model"}},
	} {
		request := route.request
		request["model"] = route.model
		callProxy(t, base+route.path, request)
		captured, path := lastUpstreamRequest(t, state)
		if path != route.upstreamPath {
			t.Fatalf("request for model %q used native path %q, want %s", route.model, path, route.upstreamPath)
		}
		if captured["model"] != route.upstreamID {
			t.Fatalf("request for model %q reached Copilot as %v, want %q", route.model, captured["model"], route.upstreamID)
		}
	}
}

func TestNativeHostOAuthSettingsOverrideCopilotModelContext(t *testing.T) {
	binary := os.Getenv("CPA_BINARY")
	if binary == "" {
		t.Skip("set CPA_BINARY to a built CLIProxyAPI v8 server for native host integration")
	}
	state := newNativeFixture(t)
	upstream := httptest.NewServer(state)
	defer upstream.Close()
	baselineBase, _ := startProxyInRoot(t, binary, upstream.URL, t.TempDir(), nil, "", "  settings: {}\n")
	baseline := requestNativeCodexModels(t, baselineBase)
	base, _ := startProxyInRoot(t, binary, upstream.URL, t.TempDir(), nil, "")
	configured := requestNativeCodexModels(t, base)
	for _, modelID := range []string{"gpt-6.1-sol", "gpt-6-luna"} {
		before, beforeExists := baseline[modelID]
		after, afterExists := configured[modelID]
		if !beforeExists || !afterExists {
			t.Fatalf("native Codex catalog must retain %q before and after its context override", modelID)
		}
		if before.ContextWindow != 272000 || after.ContextWindow != 272000 || before.MaxContextWindow == 272000 || after.MaxContextWindow != 272000 {
			t.Errorf("%s context windows = %d/%d before, %d/%d after, want context and max context windows 272000", modelID, before.ContextWindow, before.MaxContextWindow, after.ContextWindow, after.MaxContextWindow)
		}
		assertEqualJSON(t, after.SupportedReasoningLevels, before.SupportedReasoningLevels)
	}
	for _, modelID := range []string{"bridge-sonnet-5.5", "claude-sonnet-5.5"} {
		before, beforeExists := baseline[modelID]
		after, afterExists := configured[modelID]
		if !beforeExists || !afterExists {
			t.Fatalf("native Codex catalog must retain unconfigured model %q", modelID)
		}
		if before.ContextWindow != after.ContextWindow || before.MaxContextWindow != after.MaxContextWindow {
			t.Errorf("unconfigured %s context windows changed from %d/%d to %d/%d", modelID, before.ContextWindow, before.MaxContextWindow, after.ContextWindow, after.MaxContextWindow)
		}
		assertEqualJSON(t, after.SupportedReasoningLevels, before.SupportedReasoningLevels)
	}
}

func requestNativeCodexModels(t *testing.T, base string) map[string]nativeCodexModel {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, base+"/v1/models?client_version=cpa", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer fixture-client-key")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("list native Codex models: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("list native Codex models: status=%d body=%s", response.StatusCode, body)
	}
	var catalog struct {
		Models []nativeCodexModel `json:"models"`
	}
	if err := json.NewDecoder(response.Body).Decode(&catalog); err != nil {
		t.Fatalf("decode native Codex model catalog: %v", err)
	}
	models := make(map[string]nativeCodexModel, len(catalog.Models))
	for _, model := range catalog.Models {
		models[model.Slug] = model
	}
	return models
}

func (f *fixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/copilot_internal/v2/token":
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "fixture-copilot-token", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": "http://" + r.Host}})
	case "/user":
		f.mu.Lock()
		f.githubUserAuth = append(f.githubUserAuth, r.Header.Get("Authorization"))
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 4242, "login": "fixture"})
	case "/login/oauth/access_token":
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		form, err := url.ParseQuery(string(body))
		if err != nil || form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "fixture-refresh-token" || form.Get("client_id") != "fixture-oauth-client-id" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.oauthRefreshCount++
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "fixture-new-github-token", "refresh_token": "fixture-new-refresh-token", "token_type": "bearer", "scope": "read:user user:email", "expires_in": 86400, "refresh_token_expires_in": 2592000})
	case "/models":
		_, _ = w.Write(f.modelCatalog)
	case "/responses", "/v1/messages", "/chat/completions":
		if r.Header.Get("Authorization") != "Bearer fixture-copilot-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.requests = append(f.requests, request)
		f.paths = append(f.paths, r.URL.Path)
		model := stringValue(request["model"])
		f.modelTurns[model]++
		turn := f.modelTurns[model]
		f.mu.Unlock()
		if request["fixture_cancel"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			writeEvent(w, map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_cancel", "status": "in_progress", "output": []any{}}})
			<-r.Context().Done()
			close(f.canceled)
			return
		}
		if isMatrixModel(stringValue(request["model"])) {
			writeMatrixResponse(w, r.URL.Path, request)
			return
		}
		if model != "bridge-responses" && model != "bridge-messages" && model != "bridge-chat" {
			f.writeSeededResponse(w, r.URL.Path, request, model, turn)
			return
		}
		if r.URL.Path == "/v1/messages" {
			_, _ = io.WriteString(w, `{"id":"msg_fixture","type":"message","role":"assistant","model":"bridge-messages","content":[{"type":"thinking","thinking":"","signature":"claude-opaque/+"},{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
			return
		}
		if r.URL.Path == "/chat/completions" {
			_, _ = io.WriteString(w, `{"id":"chat_fixture","object":"chat.completion","model":"bridge-chat","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
			return
		}
		output := []any{
			map[string]any{"type": "reasoning", "id": fixtureResponsesReasoningID, "summary": []any{}, "encrypted_content": "copilot-opaque/+not-fernet"},
			map[string]any{"type": "function_call", "id": fixtureResponsesFunctionItemID, "call_id": fixtureResponsesCallID, "name": "inspect", "arguments": "{}", "status": "completed"},
		}
		if request["tool_choice"] == "none" {
			output = []any{map[string]any{"type": "message", "id": "msg_summary", "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "The active goal is to finish the native host bridge. The next step is integration."}}}}
		}
		response := map[string]any{"id": "resp_fixture", "object": "response", "status": "completed", "model": "bridge-responses", "output": output, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}
		if request["stream"] != true {
			_ = json.NewEncoder(w).Encode(response)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for i, item := range output {
			writeEvent(w, map[string]any{"type": "response.output_item.added", "output_index": i, "item": item})
			writeEvent(w, map[string]any{"type": "response.output_item.done", "output_index": i, "item": item})
		}
		writeEvent(w, map[string]any{"type": "response.completed", "response": response})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fixture) writeSeededResponse(w http.ResponseWriter, path string, request map[string]any, model string, turn int) {
	name := "responses.json"
	if path == "/chat/completions" {
		name = "chat.json"
	}
	if path == "/v1/messages" {
		name = "messages.json"
	}
	if path == "/responses" && request["tool_choice"] == "none" {
		name = "responses-compaction.json"
	}
	if request["stream"] == true {
		name = strings.TrimSuffix(name, ".json") + ".sse"
		if path == "/responses" && request["tool_choice"] == "none" {
			name = "responses-compaction.sse"
		}
		w.Header().Set("Content-Type", "text/event-stream")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	seed, ok := f.seeds[name]
	if !ok {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	seed = bytes.ReplaceAll(seed, []byte("fixture-model"), []byte(model))
	seed = bytes.ReplaceAll(seed, []byte("fixture-response-id"), []byte(fmt.Sprintf("resp_fixture_%d", turn)))
	seed = bytes.ReplaceAll(seed, []byte("fixture-reasoning-id"), []byte(fmt.Sprintf("reasoning_fixture_%d/+", turn)))
	seed = bytes.ReplaceAll(seed, []byte("fixture-summary-id"), []byte(fmt.Sprintf("summary_fixture_%d", turn)))
	seed = bytes.ReplaceAll(seed, []byte("fixture-chat-response-id"), []byte(fmt.Sprintf("chat_fixture_%d", turn)))
	seed = bytes.ReplaceAll(seed, []byte("fixture-chat-chunk-id"), []byte(fmt.Sprintf("chatcmpl_fixture_%d", turn)))
	seed = bytes.ReplaceAll(seed, []byte("fixture-messages-response-id"), []byte(fmt.Sprintf("msg_fixture_%d", turn)))
	seed = bytes.ReplaceAll(seed, []byte("fixture-function-item-id"), []byte(fmt.Sprintf("fixture-function-item-%d/+", turn)))
	seed = bytes.ReplaceAll(seed, []byte("fixture-call-id"), []byte(fmt.Sprintf("fixture-call-%d/+", turn)))
	seed = bytes.ReplaceAll(seed, []byte("fixture-chat-call-id"), []byte(fmt.Sprintf("fixture-chat-call-%d/+", turn)))
	seed = bytes.ReplaceAll(seed, []byte("fixture-messages-tool-id"), []byte(fmt.Sprintf("fixture-messages-tool-%d/+", turn)))
	_, _ = w.Write(seed)
}

func isMatrixModel(model string) bool {
	return model == matrixChatModel || model == matrixResponsesModel || model == matrixMessagesModel || model == matrixChatMessagesModel
}

func writeMatrixResponse(w http.ResponseWriter, path string, request map[string]any) {
	model := stringValue(request["model"])
	turn := 1
	if model == matrixChatMessagesModel {
		turn = matrixPriorToolCallCount(path, request)
	}
	if request["stream"] != true {
		w.Header().Set("Content-Type", "application/json")
		var response any
		switch path {
		case "/chat/completions":
			response = matrixChatResponse(model)
		case "/responses":
			response = matrixResponsesResponse(model)
		case "/v1/messages":
			response = matrixMessagesResponse(model, turn)
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(response)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	switch path {
	case "/chat/completions":
		writeMatrixChatStream(w, model)
	case "/responses":
		writeMatrixResponsesStream(w, model)
	case "/v1/messages":
		writeMatrixMessagesStream(w, model, turn)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func matrixPriorToolCallCount(path string, request map[string]any) int {
	count := 0
	switch path {
	case "/chat/completions":
		for _, message := range jsonObjects(request["messages"]) {
			count += len(jsonObjects(message["tool_calls"]))
		}
	case "/responses":
		for _, item := range jsonObjects(request["input"]) {
			if item["type"] == "function_call" || item["type"] == "custom_tool_call" {
				count++
			}
		}
	case "/v1/messages":
		for _, message := range jsonObjects(request["messages"]) {
			for _, block := range jsonObjects(message["content"]) {
				if block["type"] == "tool_use" {
					count++
				}
			}
		}
	}
	return count
}

func matrixSignatureForTurn(turn int) string {
	if turn <= 1 {
		return matrixSignature
	}
	return fmt.Sprintf("provider-signature-turn-%d/+ exact\n\t ", turn)
}

func matrixRedactedForTurn(turn int) string {
	if turn <= 1 {
		return matrixRedacted
	}
	return fmt.Sprintf("provider-redacted-turn-%d/+ exact\n\t ", turn)
}

func matrixReasoningForTurn(turn int) string {
	if turn <= 1 {
		return matrixReasoning
	}
	return fmt.Sprintf("provider reasoning turn %d exact", turn)
}

func matrixMessagesToolIDForTurn(turn int) string {
	if turn <= 1 {
		return matrixMessagesToolID
	}
	prefix := fmt.Sprintf("toolu_provider_turn_%d_", turn)
	return prefix + strings.Repeat("p", 64-len(prefix))
}

func matrixMessagesItemIDForTurn(turn int) string {
	return "fc_" + matrixMessagesToolIDForTurn(turn)
}

func matrixChatResponse(model string) map[string]any {
	return map[string]any{"id": "chatcmpl_matrix", "object": "chat.completion", "model": model, "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": matrixOutputText, "reasoning_opaque": matrixSignature, "tool_calls": []any{map[string]any{"id": matrixChatCallID, "type": "function", "function": map[string]any{"name": matrixToolName, "arguments": matrixToolArguments}}}}, "finish_reason": "tool_calls"}}, "usage": map[string]int{"prompt_tokens": 4, "completion_tokens": 3, "total_tokens": 7}}
}

func matrixResponsesResponse(model string) map[string]any {
	return map[string]any{"id": "resp_matrix", "object": "response", "status": "completed", "model": model, "output": []any{
		map[string]any{"id": "rs_matrix", "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": matrixReasoning}}, "encrypted_content": matrixSignature},
		map[string]any{"id": "rs_redacted_matrix", "type": "reasoning", "summary": []any{}, "encrypted_content": "claude-redacted-thinking:" + matrixRedacted},
		map[string]any{"id": "msg_matrix", "type": "message", "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": matrixOutputText}}},
		map[string]any{"id": matrixResponseItemID, "type": "function_call", "call_id": matrixResponseCallID, "name": matrixToolName, "arguments": matrixToolArguments, "status": "completed"},
	}, "usage": map[string]any{"input_tokens": 4, "output_tokens": 3, "total_tokens": 7}}
}

func matrixMessagesResponse(model string, turn int) map[string]any {
	return map[string]any{"id": "msg_matrix", "type": "message", "role": "assistant", "model": model, "content": []any{
		map[string]any{"type": "text", "text": matrixOutputText},
		map[string]any{"type": "thinking", "thinking": matrixReasoningForTurn(turn), "signature": matrixSignatureForTurn(turn)},
		map[string]any{"type": "redacted_thinking", "data": matrixRedactedForTurn(turn)},
		map[string]any{"type": "tool_use", "id": matrixMessagesToolIDForTurn(turn), "name": matrixToolName, "input": map[string]any{"query": "provider-value"}},
	}, "stop_reason": "tool_use", "stop_sequence": nil, "usage": map[string]int{"input_tokens": 4, "output_tokens": 3}}
}

func writeMatrixChatStream(w http.ResponseWriter, model string) {
	writeEvent(w, map[string]any{"id": "chatcmpl_matrix", "object": "chat.completion.chunk", "created": 1, "model": model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": matrixOutputText, "reasoning_opaque": matrixSignature, "tool_calls": []any{map[string]any{"index": 0, "id": matrixChatCallID, "type": "function", "function": map[string]any{"name": matrixToolName, "arguments": matrixToolArguments}}}}, "finish_reason": "tool_calls"}}})
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeMatrixResponsesStream(w http.ResponseWriter, model string) {
	response := matrixResponsesResponse(model)
	output := response["output"].([]any)
	writeEvent(w, map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_matrix", "object": "response", "status": "in_progress", "model": model, "output": []any{}}})
	writeEvent(w, map[string]any{"type": "response.in_progress", "response": map[string]any{"id": "resp_matrix", "object": "response", "status": "in_progress", "model": model, "output": []any{}}})
	for index, item := range output {
		writeEvent(w, map[string]any{"type": "response.output_item.added", "output_index": index, "item": item})
		writeEvent(w, map[string]any{"type": "response.output_item.done", "output_index": index, "item": item})
	}
	writeEvent(w, map[string]any{"type": "response.completed", "response": response})
}

func writeMatrixMessagesStream(w http.ResponseWriter, model string, turn int) {
	writeSSEEvent(w, "message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_matrix", "type": "message", "role": "assistant", "model": model, "content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 4, "output_tokens": 0}}})
	writeSSEEvent(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
	writeSSEEvent(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": matrixOutputText}})
	writeSSEEvent(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	writeSSEEvent(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "thinking", "thinking": ""}})
	writeSSEEvent(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "thinking_delta", "thinking": matrixReasoningForTurn(turn)}})
	writeSSEEvent(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "signature_delta", "signature": matrixSignatureForTurn(turn)}})
	writeSSEEvent(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 1})
	writeSSEEvent(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 2, "content_block": map[string]any{"type": "redacted_thinking", "data": matrixRedactedForTurn(turn)}})
	writeSSEEvent(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 2})
	writeSSEEvent(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 3, "content_block": map[string]any{"type": "tool_use", "id": matrixMessagesToolIDForTurn(turn), "name": matrixToolName, "input": map[string]any{}}})
	writeSSEEvent(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 3, "delta": map[string]any{"type": "input_json_delta", "partial_json": matrixToolArguments}})
	writeSSEEvent(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 3})
	writeSSEEvent(w, "message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use", "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 3}})
	writeSSEEvent(w, "message_stop", map[string]any{"type": "message_stop"})
}

func writeSSEEvent(w http.ResponseWriter, name string, event any) {
	body, _ := json.Marshal(event)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, body)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}

func writeEvent(w http.ResponseWriter, event any) {
	body, _ := json.Marshal(event)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", body)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func startProxy(t *testing.T, binary, upstream string) string {
	base, _ := startProxyInRoot(t, binary, upstream, t.TempDir(), nil, "")
	return base
}

func startProxyInRoot(t *testing.T, binary, upstream, root string, authJSON []byte, managementSecret string, oauthConfigYAML ...string) (string, func()) {
	t.Helper()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	pluginDir := filepath.Join(root, "plugins", runtime.GOOS, runtime.GOARCH)
	if err := os.MkdirAll(pluginDir, 0700); err != nil {
		t.Fatal(err)
	}
	ext := ".so"
	if runtime.GOOS == "darwin" {
		ext = ".dylib"
	}
	if runtime.GOOS == "windows" {
		ext = ".dll"
	}
	artifact, err := os.ReadFile(filepath.Join("..", "build", "plugins", runtime.GOOS, runtime.GOARCH, "cliproxyapi-copilot"+ext))
	if err != nil {
		t.Fatalf("build the native plugin before host integration: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "cliproxyapi-copilot"+ext), artifact, 0600); err != nil {
		t.Fatal(err)
	}
	authDir := filepath.Join(root, "auths")
	if err := os.MkdirAll(authDir, 0700); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(authDir, "fixture.json")
	if _, err := os.Stat(authPath); os.IsNotExist(err) {
		if authJSON == nil {
			authJSON = nativeAuthFixtureJSON(t)
		}
		if err := os.WriteFile(authPath, authJSON, 0600); err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	var oauthConfig strings.Builder
	for _, fragment := range oauthConfigYAML {
		oauthConfig.WriteString(fragment)
	}
	config, configuredAuthDir := nativeRuntimeConfig(t, root, port, upstream, managementSecret, oauthConfig.String())
	if configuredAuthDir != authDir {
		t.Fatalf("native template configured auth directory %q, want %q", configuredAuthDir, authDir)
	}
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "host.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { cancel(); _ = command.Wait(); _ = logFile.Close() }) }
	t.Cleanup(stop)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			log, _ := os.ReadFile(logPath)
			t.Fatalf("native host failed to load plugin: %s", log)
		case <-ticker.C:
			request, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
			request.Header.Set("Authorization", "Bearer fixture-client-key")
			response, err := http.DefaultClient.Do(request)
			if err == nil {
				body, _ := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if response.StatusCode == 200 && bytes.Contains(body, []byte("bridge-responses")) {
					return base, stop
				}
			}
		}
	}
}

func callProxy(t *testing.T, url string, payload any) []byte {
	return callProxyWithSession(t, url, payload, "fixture-conversation")
}

func callProxyWithSession(t *testing.T, url string, payload any, session string) []byte {
	t.Helper()
	status, out := postProxyWithSession(t, url, payload, session)
	if status != http.StatusOK {
		t.Fatalf("native request failed: status=%d body=%s", status, out)
	}
	return out
}

func postProxyWithSession(t *testing.T, url string, payload any, session string) (int, []byte) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
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
		t.Fatalf("read native response body: %v", err)
	}
	return response.StatusCode, out
}

func foreignOpaqueChatRequest(clientFormat string, stream bool) map[string]any {
	if clientFormat == "claude" {
		return map[string]any{
			"model":      matrixChatModel,
			"stream":     stream,
			"max_tokens": 256,
			"messages": []any{
				map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "foreign opaque context"}}},
				map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "thinking", "thinking": "foreign reasoning", "signature": matrixInitialSignature},
					map[string]any{"type": "redacted_thinking", "data": matrixInitialRedacted},
				}},
				map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "continue with foreign context"}}},
			},
		}
	}
	return map[string]any{
		"model":  matrixChatModel,
		"stream": stream,
		"store":  false,
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "foreign opaque context"}}},
			map[string]any{"type": "reasoning", "id": "foreign-reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "foreign reasoning"}}, "encrypted_content": matrixInitialSignature},
			map[string]any{"type": "reasoning", "id": "foreign-redacted", "summary": []any{}, "encrypted_content": "claude-redacted-thinking:" + matrixInitialRedacted},
			map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "continue with foreign context"}}},
		},
	}
}

func upstreamRequestCount(state *fixture) int {
	state.mu.Lock()
	defer state.mu.Unlock()
	return len(state.requests)
}

func lastUpstreamRequest(t *testing.T, state *fixture) (map[string]any, string) {
	t.Helper()
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.requests) == 0 || len(state.requests) != len(state.paths) {
		t.Fatalf("fixture captured mismatched request state: requests=%d paths=%d", len(state.requests), len(state.paths))
	}
	last := len(state.requests) - 1
	return state.requests[last], state.paths[last]
}

func matrixCompactionHistory(t *testing.T) []any {
	t.Helper()
	var history []any
	if err := json.Unmarshal(readNativeSeed(t, "responses-history.json"), &history); err != nil {
		t.Fatalf("decode synthetic Responses history: %v", err)
	}
	return history
}

func assertCompactionRequest(t *testing.T, request map[string]any, path string) {
	t.Helper()
	if path != "/responses" {
		t.Fatalf("compaction request used upstream path %q", path)
	}
	if request["tool_choice"] != "none" {
		t.Fatalf("compaction request tool_choice = %v, want none", request["tool_choice"])
	}
	if request["prompt_cache_key"] != matrixCompactCacheKey {
		t.Fatalf("compaction request changed the explicit prompt cache key: %+v", request)
	}
	if _, exists := request["tools"]; exists {
		t.Fatalf("compaction request retained tools: %+v", request)
	}
	input, err := json.Marshal(request["input"])
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(input, []byte("compaction_trigger")) {
		t.Fatalf("raw compaction trigger reached Copilot: %s", input)
	}
	items := jsonObjects(request["input"])
	var reasoningFound, functionFound, outputFound bool
	for _, item := range items {
		switch item["type"] {
		case "reasoning":
			reasoningFound = item["id"] == matrixCompactReasoningID && item["encrypted_content"] == matrixCompactOpaque
		case "function_call":
			functionFound = item["id"] == matrixCompactFunctionID && item["call_id"] == matrixCompactCallID && item["name"] == "inspect" && item["arguments"] == `{"query":"original"}`
		case "function_call_output":
			outputFound = item["call_id"] == matrixCompactCallID && item["output"] == "original result"
		}
	}
	if !reasoningFound || !functionFound || !outputFound {
		t.Fatalf("compaction request changed encrypted thinking or original tool history: %+v", request["input"])
	}
	if !bytes.Contains(input, []byte("transcript data")) {
		t.Fatalf("summary instruction missing from Copilot request: %s", input)
	}
}

func matrixClientPath(format string) string {
	switch format {
	case "claude":
		return "/v1/messages"
	case "openai":
		return "/v1/chat/completions"
	default:
		return "/v1/responses"
	}
}

func matrixInitialRequest(route matrixRoute, stream bool) map[string]any {
	if route.clientFormat == "openai" {
		messages := []any{
			map[string]any{"role": "user", "content": matrixInputMarker},
			map[string]any{"role": "assistant", "content": "prior tool call", "tool_calls": []any{map[string]any{"id": "chat-input-call", "type": "function", "function": map[string]any{"name": matrixToolName, "arguments": `{"query":"initial-value"}`}}}},
			map[string]any{"role": "tool", "tool_call_id": "chat-input-call", "content": matrixToolResult},
			map[string]any{"role": "user", "content": "Continue the protocol matrix."},
		}
		request := map[string]any{"model": route.model, "stream": stream, "messages": messages, "tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": matrixToolName, "description": "Inspect a matrix value.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}}}}}}
		if route.upstreamPath == "/v1/messages" {
			request["reasoning_effort"] = matrixChatInitialThinkingEffort
		}
		return request
	}
	if route.clientFormat == "claude" {
		assistant := []any{}
		if route.upstreamPath != "/chat/completions" {
			assistant = append(assistant, map[string]any{"type": "thinking", "thinking": "initial private reasoning", "signature": matrixInitialSignature})
			assistant = append(assistant, map[string]any{"type": "redacted_thinking", "data": matrixInitialRedacted})
		}
		assistant = append(assistant, map[string]any{"type": "tool_use", "id": "toolu_initial_1", "name": matrixToolName, "input": map[string]any{"query": "initial-value"}})
		messages := []any{
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": matrixInputMarker}}},
			map[string]any{"role": "assistant", "content": assistant},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_initial_1", "content": matrixToolResult}}},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "Continue the protocol matrix."}}},
		}
		request := map[string]any{"model": route.model, "stream": stream, "max_tokens": 256, "tools": []any{map[string]any{"name": matrixToolName, "description": "Inspect a matrix value.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}}}}, "messages": messages}
		if route.upstreamPath == "/v1/messages" {
			request["thinking"] = map[string]any{"type": "adaptive"}
			request["output_config"] = map[string]any{"effort": "high"}
		}
		return request
	}
	input := []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": matrixInputMarker}}}}
	if route.upstreamPath != "/chat/completions" {
		input = append(input,
			map[string]any{"type": "reasoning", "id": "rs-initial/+1", "summary": []any{map[string]any{"type": "summary_text", "text": "initial private reasoning"}}, "encrypted_content": matrixInitialSignature},
			map[string]any{"type": "reasoning", "id": "rs-initial-redacted/+1", "summary": []any{}, "encrypted_content": "claude-redacted-thinking:" + matrixInitialRedacted},
		)
	}
	input = append(input,
		map[string]any{"type": "function_call", "id": matrixInitialItemID, "call_id": matrixInitialCallID, "name": matrixToolName, "arguments": `{"query":"initial-value"}`},
		map[string]any{"type": "function_call_output", "call_id": matrixInitialCallID, "output": matrixToolResult},
		map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Continue the protocol matrix."}}},
	)
	request := map[string]any{"model": route.model, "stream": stream, "store": false, "input": input, "tools": []any{map[string]any{"type": "function", "name": matrixToolName, "description": "Inspect a matrix value.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}}}}}
	if route.model == matrixMessagesModel {
		request["reasoning"] = map[string]any{"effort": "high"}
	}
	return request
}

func assertMatrixRoute(t *testing.T, route matrixRoute, request map[string]any, path string, stream bool) {
	t.Helper()
	if path != route.upstreamPath || request["model"] != route.model || request["stream"] != stream {
		t.Fatalf("route selected path/model/stream %q/%v/%v, want %q/%s/%v", path, request["model"], request["stream"], route.upstreamPath, route.model, stream)
	}
}

func assertMatrixInitialRequest(t *testing.T, route matrixRoute, request map[string]any) {
	t.Helper()
	if !containsJSONScalar(request, matrixInputMarker) || !containsJSONScalar(request, matrixToolResult) {
		t.Fatalf("initial text or tool result was dropped on %s: %+v", route.name, request)
	}
	if route.upstreamPath == "/chat/completions" {
		if route.clientFormat != "openai" && (containsJSONScalar(request, matrixInitialSignature) || containsJSONScalar(request, matrixInitialRedacted)) {
			t.Fatalf("Chat request lost tool identity or accepted unsupported opaque history: %+v", request)
		}
		messages := jsonObjects(request["messages"])
		callID := ""
		if route.clientFormat == "openai-response" {
			for _, message := range messages {
				for _, call := range jsonObjects(message["tool_calls"]) {
					candidate := stringValue(call["id"])
					itemID, decodedCallID, ok := translate.DecodeClaudeToolIDs(candidate)
					if ok && itemID == matrixInitialItemID && decodedCallID == matrixInitialCallID {
						callID = candidate
					}
				}
			}
		} else if route.clientFormat == "openai" {
			for _, message := range messages {
				for _, call := range jsonObjects(message["tool_calls"]) {
					candidate := stringValue(call["id"])
					if candidate == "chat-input-call" {
						callID = candidate
					}
				}
			}
		} else {
			for _, message := range messages {
				for _, call := range jsonObjects(message["tool_calls"]) {
					candidate := stringValue(call["id"])
					_, decodedCallID, ok := translate.DecodeClaudeToolIDs(candidate)
					if candidate == "toolu_initial_1" || ok && decodedCallID == "toolu_initial_1" {
						callID = candidate
					}
				}
			}
		}
		if callID == "" {
			if route.clientFormat == "openai-response" {
				t.Fatalf("Chat request did not encode the distinct long Responses tool IDs: %+v", request["messages"])
			}
			t.Fatalf("Chat request dropped the Claude tool-use ID: %+v", request["messages"])
		}
		var callFound, resultFound bool
		for _, message := range messages {
			for _, call := range jsonObjects(message["tool_calls"]) {
				callFound = callFound || call["id"] == callID
			}
			if message["role"] == "tool" && message["tool_call_id"] == callID && containsJSONScalar(message["content"], matrixToolResult) {
				resultFound = true
			}
		}
		if !callFound || !resultFound {
			t.Fatalf("Chat request lost or mismatched tool call/result ID %q: %+v", callID, request["messages"])
		}
		return
	}
	if route.upstreamPath == "/responses" {
		items := jsonObjects(request["input"])
		var signatureFound, redactedFound, callFound, outputFound, itemIDFound bool
		var toolCallID string
		for _, item := range items {
			switch item["type"] {
			case "reasoning":
				encrypted := stringValue(item["encrypted_content"])
				signatureFound = signatureFound || encrypted == matrixInitialSignature
				redactedFound = redactedFound || encrypted == "claude-redacted-thinking:"+matrixInitialRedacted
			case "function_call":
				toolCallID = stringValue(item["call_id"])
				callFound = callFound || toolCallID == "toolu_initial_1" || toolCallID == matrixInitialCallID || route.clientFormat == "openai" && toolCallID == "chat-input-call"
				itemIDFound = itemIDFound || item["id"] == matrixInitialItemID
			case "function_call_output":
				outputFound = outputFound || item["call_id"] == toolCallID && toolCallID != ""
			}
		}
		if route.clientFormat == "openai-response" {
			if !itemIDFound || !containsJSONScalar(request["input"], matrixInitialCallID) {
				t.Fatalf("native Responses request changed separate function item/call IDs: %+v", request["input"])
			}
		}
		if route.clientFormat == "openai" {
			signatureFound = true
			redactedFound = true
		}
		if !signatureFound || !redactedFound || !callFound || !outputFound {
			t.Fatalf("Responses request dropped signed/redacted history or tool correlation: %+v", request["input"])
		}
		return
	}
	messages := jsonObjects(request["messages"])
	var signatureFound, redactedFound, callFound, outputFound bool
	var toolID string
	for _, message := range messages {
		for _, block := range jsonObjects(message["content"]) {
			switch block["type"] {
			case "thinking":
				signatureFound = signatureFound || block["signature"] == matrixInitialSignature
			case "redacted_thinking":
				redactedFound = redactedFound || block["data"] == matrixInitialRedacted
			case "tool_use":
				toolID = stringValue(block["id"])
				callFound = true
			case "tool_result":
				outputFound = true
				if toolID != "" && block["tool_use_id"] != toolID {
					t.Fatalf("Messages tool result %v does not match tool_use id %q", block["tool_use_id"], toolID)
				}
			}
		}
	}
	if route.clientFormat == "openai" {
		signatureFound = true
		redactedFound = true
	}
	if !signatureFound || !redactedFound || !callFound || !outputFound {
		t.Fatalf("Messages request dropped signed/redacted history or tool correlation: %+v", request["messages"])
	}
	if route.clientFormat == "openai" && toolID != "chat-input-call" {
		t.Fatalf("Chat tool-call ID changed in native Messages history: got %q", toolID)
	}
	effort := "high"
	if route.clientFormat == "openai" {
		effort = matrixChatInitialThinkingEffort
	}
	assertAdaptiveThinkingRequest(t, request, effort)
	if route.clientFormat == "openai-response" {
		itemID, callID, ok := translate.DecodeClaudeToolIDs(toolID)
		if !ok || itemID != matrixInitialItemID || callID != matrixInitialCallID {
			t.Fatalf("Responses tool IDs did not survive the Messages carrier: id=%q decoded=(%q,%q,%v)", toolID, itemID, callID, ok)
		}
	}
}

func containsJSONScalar(value any, expected string) bool {
	switch current := value.(type) {
	case string:
		return current == expected
	case []any:
		for _, item := range current {
			if containsJSONScalar(item, expected) {
				return true
			}
		}
	case map[string]any:
		for _, item := range current {
			if containsJSONScalar(item, expected) {
				return true
			}
		}
	}
	return false
}

func jsonObjects(value any) []map[string]any {
	items, _ := value.([]any)
	objects := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if object, ok := item.(map[string]any); ok {
			objects = append(objects, object)
		}
	}
	return objects
}

func assertMatrixClientOutput(t *testing.T, route matrixRoute, body []byte, stream bool) matrixOutput {
	return assertMatrixClientOutputTurn(t, route, body, stream, 1)
}

func assertMatrixClientOutputTurn(t *testing.T, route matrixRoute, body []byte, stream bool, turn int) matrixOutput {
	t.Helper()
	output := matrixOutput{Turn: turn}
	if route.clientFormat == "claude" {
		var blocks []map[string]any
		if stream {
			events, _ := parseSSEDataEventsWithDone(t, body)
			assertMatrixClaudeStreamComplete(t, events)
			blocks = matrixClaudeBlocksFromEvents(t, events)
		} else {
			var response map[string]any
			if err := json.Unmarshal(body, &response); err != nil {
				t.Fatalf("decode Claude matrix response: %v: %s", err, body)
			}
			blocks = jsonObjects(response["content"])
		}
		for _, block := range blocks {
			switch block["type"] {
			case "text":
				output.Text += stringValue(block["text"])
			case "thinking":
				output.Text += stringValue(block["thinking"])
				output.Signature = stringValue(block["signature"])
			case "redacted_thinking":
				output.Redacted = stringValue(block["data"])
			case "tool_use":
				output.ClaudeToolID = stringValue(block["id"])
				output.ToolName = stringValue(block["name"])
				arguments, _ := json.Marshal(block["input"])
				output.ToolArguments = string(arguments)
				if itemID, callID, ok := translate.DecodeClaudeToolIDs(output.ClaudeToolID); ok {
					output.ItemID = itemID
					output.CallID = callID
				} else {
					output.CallID = output.ClaudeToolID
				}
			}
		}
	} else if route.clientFormat == "openai" {
		var message map[string]any
		if stream {
			events, done := parseSSEDataEventsWithDone(t, body)
			if !done {
				t.Fatalf("Chat matrix stream has no [DONE] event: %s", body)
			}
			var content strings.Builder
			var toolCall map[string]any
			var finishReason string
			for _, event := range events {
				if model := stringValue(event["model"]); model != "" {
					output.Model = model
				}
				for _, choice := range jsonObjects(event["choices"]) {
					delta, _ := choice["delta"].(map[string]any)
					content.WriteString(stringValue(delta["content"]))
					finishReason = firstStringValue(choice, "finish_reason")
					if delta["reasoning_opaque"] != nil {
						message = map[string]any{"reasoning_opaque": delta["reasoning_opaque"]}
					}
					for _, candidate := range jsonObjects(delta["tool_calls"]) {
						if toolCall == nil {
							toolCall = map[string]any{"function": map[string]any{}}
						}
						for _, field := range []string{"id", "type", "index"} {
							if candidate[field] != nil {
								toolCall[field] = candidate[field]
							}
						}
						function, _ := toolCall["function"].(map[string]any)
						candidateFunction, _ := candidate["function"].(map[string]any)
						for _, field := range []string{"name", "arguments"} {
							if value := stringValue(candidateFunction[field]); value != "" {
								function[field] = stringValue(function[field]) + value
							}
						}
						toolCall["function"] = function
					}
				}
			}
			if finishReason != "tool_calls" {
				t.Fatalf("Chat matrix stream finish_reason = %q, want tool_calls: %s", finishReason, body)
			}
			if message == nil {
				message = map[string]any{}
			}
			message["content"] = content.String()
			if toolCall != nil {
				message["tool_calls"] = []any{toolCall}
			}
		} else {
			var response map[string]any
			if err := json.Unmarshal(body, &response); err != nil {
				t.Fatalf("decode Chat matrix response: %v: %s", err, body)
			}
			output.Model = stringValue(response["model"])
			choices := jsonObjects(response["choices"])
			if len(choices) != 1 {
				t.Fatalf("Chat matrix response has %d choices: %s", len(choices), body)
			}
			if choices[0]["finish_reason"] != "tool_calls" {
				t.Fatalf("Chat matrix response finish_reason = %v, want tool_calls: %s", choices[0]["finish_reason"], body)
			}
			message, _ = choices[0]["message"].(map[string]any)
		}
		if message == nil {
			t.Fatalf("Chat matrix response has no assistant message: %s", body)
		}
		output.Text = stringValue(message["content"])
		output.ChatOpaque = message["reasoning_opaque"]
		output.Signature = stringValue(output.ChatOpaque)
		output.Redacted = stringValue(message["reasoning_content"])
		if len(jsonObjects(message["tool_calls"])) != 1 {
			t.Fatalf("Chat matrix response has no single tool call: %+v", message)
		}
		call := jsonObjects(message["tool_calls"])[0]
		output.CallID = stringValue(call["id"])
		output.ItemID = output.CallID
		if itemID, _, ok := translate.DecodeClaudeToolIDs(output.CallID); ok {
			output.ItemID = itemID
		}
		function, _ := call["function"].(map[string]any)
		output.ToolName = stringValue(function["name"])
		output.ToolArguments = stringValue(function["arguments"])
	} else {
		var response map[string]any
		if stream {
			events, _ := parseSSEDataEventsWithDone(t, body)
			for _, event := range events {
				if event["type"] == "response.completed" {
					response, _ = event["response"].(map[string]any)
				}
			}
			if response == nil {
				t.Fatalf("Responses matrix stream has no completed response: %s", body)
			}
			assertMatrixResponseDoneItems(t, events, jsonObjects(response["output"]))
		} else if err := json.Unmarshal(body, &response); err != nil {
			t.Fatalf("decode Responses matrix response: %v: %s", err, body)
		}
		output.ResponseItems = jsonObjects(response["output"])
		for _, item := range output.ResponseItems {
			switch item["type"] {
			case "message":
				for _, part := range jsonObjects(item["content"]) {
					if part["type"] == "output_text" || part["type"] == "text" {
						output.Text += stringValue(part["text"])
					}
				}
			case "reasoning":
				encrypted := stringValue(item["encrypted_content"])
				switch {
				case strings.HasPrefix(encrypted, "cpa-copilot-reasoning-auth:v2:"):
					output.Signature = encrypted
				case strings.HasPrefix(encrypted, "claude-redacted-thinking:"):
					output.Redacted = strings.TrimPrefix(encrypted, "claude-redacted-thinking:")
				case encrypted != "":
					output.Signature = encrypted
				}
			case "function_call", "custom_tool_call":
				output.ItemID = stringValue(item["id"])
				output.CallID = firstStringValue(item, "call_id", "id")
				output.ToolName = firstStringValue(item, "name", "tool_name")
				arguments := item["arguments"]
				if arguments == nil {
					arguments = item["input"]
				}
				if raw, ok := arguments.(string); ok {
					output.ToolArguments = raw
				} else {
					encoded, _ := json.Marshal(arguments)
					output.ToolArguments = string(encoded)
				}
			}
		}
	}
	assertMatrixOutputSemantics(t, route, output)
	return output
}

func matrixClaudeBlocksFromEvents(t *testing.T, events []map[string]any) []map[string]any {
	t.Helper()
	blocks := make(map[int]map[string]any)
	order := make([]int, 0)
	for _, event := range events {
		index, _ := event["index"].(float64)
		switch event["type"] {
		case "content_block_start":
			block, _ := event["content_block"].(map[string]any)
			if block != nil {
				blocks[int(index)] = cloneJSONMap(block)
				order = append(order, int(index))
			}
		case "content_block_delta":
			block := blocks[int(index)]
			delta, _ := event["delta"].(map[string]any)
			if block != nil && delta != nil {
				switch delta["type"] {
				case "text_delta":
					block["text"] = stringValue(block["text"]) + stringValue(delta["text"])
				case "thinking_delta":
					block["thinking"] = stringValue(block["thinking"]) + stringValue(delta["thinking"])
				case "signature_delta":
					block["signature"] = delta["signature"]
				case "input_json_delta":
					block["partial_json"] = stringValue(block["partial_json"]) + stringValue(delta["partial_json"])
				}
			}
		}
	}
	result := make([]map[string]any, 0, len(order))
	for _, index := range order {
		block := blocks[index]
		if partial := stringValue(block["partial_json"]); partial != "" {
			var input map[string]any
			if err := json.Unmarshal([]byte(partial), &input); err != nil {
				t.Fatalf("decode streamed Claude tool input: %v: %s", err, partial)
			}
			block["input"] = input
			delete(block, "partial_json")
		}
		result = append(result, block)
	}
	return result
}

func assertMatrixClaudeStreamComplete(t *testing.T, events []map[string]any) {
	t.Helper()
	var starts, stops, blockStarts, blockStops int
	for _, event := range events {
		switch event["type"] {
		case "message_start":
			starts++
		case "message_stop":
			stops++
		case "content_block_start":
			blockStarts++
		case "content_block_stop":
			blockStops++
		}
	}
	if starts != 1 || stops != 1 || blockStarts == 0 || blockStarts != blockStops {
		t.Fatalf("Claude stream framing is incomplete: starts=%d stops=%d blockStarts=%d blockStops=%d events=%+v", starts, stops, blockStarts, blockStops, events)
	}
}

func cloneJSONMap(value map[string]any) map[string]any {
	copy := make(map[string]any, len(value))
	for key, item := range value {
		copy[key] = item
	}
	return copy
}

func firstStringValue(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if current := stringValue(value[key]); current != "" {
			return current
		}
	}
	return ""
}

func assertMatrixOutputSemantics(t *testing.T, route matrixRoute, output matrixOutput) {
	t.Helper()
	if route.clientFormat == "openai" && output.Model != route.model {
		t.Fatalf("Chat response model = %q on %s, want original upstream model %q", output.Model, route.name, route.model)
	}
	if output.Text == "" || !strings.Contains(output.Text, matrixOutputText) {
		t.Fatalf("output text was dropped on %s: %+v", route.name, output)
	}
	if output.ToolName != matrixToolName || output.ToolArguments != matrixToolArguments {
		t.Fatalf("tool name or arguments changed on %s: %+v", route.name, output)
	}
	switch route.upstreamPath {
	case "/chat/completions":
		if route.clientFormat == "openai" {
			if output.ChatOpaque != matrixSignature || output.CallID != matrixChatCallID {
				t.Fatalf("Chat opaque reasoning or call ID changed on %s: opaque=%v call=%q", route.name, output.ChatOpaque, output.CallID)
			}
			return
		}
		decoded := assertChatReasoningCarrier(t, output.Signature, route.model)
		if decoded != matrixSignature || output.CallID != matrixChatCallID {
			t.Fatalf("Chat opaque reasoning or call ID changed on %s: opaque=%q call=%q", route.name, decoded, output.CallID)
		}
		if output.Redacted != "" {
			t.Fatalf("Chat route unexpectedly returned a redacted thinking block: %+v", output)
		}
	case "/responses":
		if route.clientFormat == "openai" {
			if !matrixOpaqueContains(output.ChatOpaque, matrixSignature) || !matrixOpaqueContains(output.ChatOpaque, matrixRedacted) || output.CallID == "" {
				t.Fatalf("Chat output lost Responses opaque blocks or tool ID on %s: %+v", route.name, output)
			}
			itemID, callID, ok := translate.DecodeClaudeToolIDs(output.CallID)
			if !ok || itemID != matrixResponseItemID || callID != matrixResponseCallID {
				t.Fatalf("Chat output lost distinct Responses tool IDs on %s: carrier=%q decoded=(%q,%q,%v), want (%q,%q)", route.name, output.CallID, itemID, callID, ok, matrixResponseItemID, matrixResponseCallID)
			}
			return
		}
		if output.Signature != matrixSignature || output.Redacted != matrixRedacted || output.ItemID != matrixResponseItemID || output.CallID != matrixResponseCallID {
			t.Fatalf("Responses output lost opaque history or separate tool IDs on %s: %+v", route.name, output)
		}
	case "/v1/messages":
		if route.clientFormat == "openai" {
			signature := matrixSignatureForTurn(output.Turn)
			redacted := matrixRedactedForTurn(output.Turn)
			if !matrixOpaqueContains(output.ChatOpaque, signature) || !matrixOpaqueContains(output.ChatOpaque, redacted) || output.CallID == "" {
				t.Fatalf("Chat output lost Messages thinking blocks or tool ID on %s: %+v", route.name, output)
			}
			if callID := matrixMessagesToolIDFromOutput(t, output); callID != matrixMessagesToolIDForTurn(output.Turn) {
				t.Fatalf("Chat output changed Messages tool ID on %s: got %q, want %q", route.name, callID, matrixMessagesToolIDForTurn(output.Turn))
			}
			return
		}
		if output.Signature != matrixSignature || output.Redacted != matrixRedacted || output.CallID != matrixMessagesToolID {
			t.Fatalf("Messages output lost signed/redacted thinking or tool ID on %s: %+v", route.name, output)
		}
		if route.clientFormat == "openai-response" && output.ItemID == "" {
			t.Fatalf("Responses output omitted its function item ID for Messages tool use: %+v", output)
		}
	}
	if route.clientFormat == "claude" {
		if output.ClaudeToolID == "" {
			t.Fatalf("Claude tool_use block was dropped on %s: %+v", route.name, output)
		}
		if route.upstreamPath == "/responses" {
			itemID, callID, ok := translate.DecodeClaudeToolIDs(output.ClaudeToolID)
			if !ok || itemID != matrixResponseItemID || callID != matrixResponseCallID {
				t.Fatalf("Claude tool ID did not preserve Responses item/call IDs: %q -> %q/%q", output.ClaudeToolID, itemID, callID)
			}
		}
	}
}

func matrixOpaqueContains(value any, expected string) bool {
	switch current := value.(type) {
	case string:
		raw := current
		if raw == expected || strings.Contains(raw, expected) {
			return true
		}
		if strings.HasPrefix(raw, "cpa-copilot-reasoning-auth:v2:") {
			payload, _, ok := strings.Cut(strings.TrimPrefix(raw, "cpa-copilot-reasoning-auth:v2:"), ".")
			decoded, err := base64.RawURLEncoding.DecodeString(payload)
			return ok && err == nil && matrixOpaqueContains(string(decoded), expected)
		}
		if strings.HasPrefix(raw, "cpa-copilot-reasoning:v1:") {
			envelope, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(raw, "cpa-copilot-reasoning:v1:"))
			var carrier struct {
				RawJSONB64 string `json:"raw_json_b64"`
			}
			if err == nil && json.Unmarshal(envelope, &carrier) == nil {
				decoded, err := base64.RawURLEncoding.DecodeString(carrier.RawJSONB64)
				return err == nil && matrixOpaqueContains(string(decoded), expected)
			}
		}
		var decoded any
		if json.Unmarshal([]byte(raw), &decoded) != nil {
			return false
		}
		if decodedString, ok := decoded.(string); ok && decodedString == raw {
			return false
		}
		return matrixOpaqueContains(decoded, expected)
	case []any:
		for _, nested := range current {
			if matrixOpaqueContains(nested, expected) {
				return true
			}
		}
	case map[string]any:
		for _, nested := range current {
			if matrixOpaqueContains(nested, expected) {
				return true
			}
		}
	}
	return false
}

func assertChatReasoningCarrier(t *testing.T, carrier, model string) string {
	t.Helper()
	const authPrefix = "cpa-copilot-reasoning-auth:v2:"
	if !strings.HasPrefix(carrier, authPrefix) {
		t.Fatalf("Chat reasoning carrier missing or malformed: %q", carrier)
	}
	encodedInner, encodedMAC, ok := strings.Cut(strings.TrimPrefix(carrier, authPrefix), ".")
	if !ok || encodedInner == "" || encodedMAC == "" || strings.Contains(encodedMAC, ".") {
		t.Fatalf("Chat reasoning authentication wrapper is malformed: %q", carrier)
	}
	mac, err := base64.RawURLEncoding.DecodeString(encodedMAC)
	if err != nil || len(mac) != 32 {
		t.Fatalf("Chat reasoning authentication tag must be base64url HMAC-SHA256: %q", encodedMAC)
	}
	inner, err := base64.RawURLEncoding.DecodeString(encodedInner)
	if err != nil {
		t.Fatalf("decode authenticated Chat reasoning carrier: %v", err)
	}
	const innerPrefix = "cpa-copilot-reasoning:v1:"
	if !strings.HasPrefix(string(inner), innerPrefix) {
		t.Fatalf("Chat reasoning authentication wrapper has an unexpected inner carrier: %q", inner)
	}
	jsonEnvelope, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(string(inner), innerPrefix))
	if err != nil {
		t.Fatalf("decode Chat reasoning envelope: %v", err)
	}
	var envelope struct {
		Version    int    `json:"v"`
		Provider   string `json:"provider"`
		Endpoint   string `json:"endpoint"`
		Model      string `json:"model"`
		RawJSONB64 string `json:"raw_json_b64"`
		Anchor     struct {
			ToolCallIDs []string `json:"tool_call_ids"`
		} `json:"anchor"`
	}
	if err := json.Unmarshal(jsonEnvelope, &envelope); err != nil {
		t.Fatalf("decode Chat reasoning envelope JSON: %v", err)
	}
	if envelope.Version != 1 || envelope.Provider != "copilot" || envelope.Endpoint != "chat/completions" || envelope.Model != model {
		t.Fatalf("Chat reasoning envelope scope changed: %+v", envelope)
	}
	if len(envelope.Anchor.ToolCallIDs) != 1 || envelope.Anchor.ToolCallIDs[0] != matrixChatCallID {
		t.Fatalf("Chat reasoning envelope tool anchor = %v, want [%s]", envelope.Anchor.ToolCallIDs, matrixChatCallID)
	}
	raw, err := base64.RawURLEncoding.DecodeString(envelope.RawJSONB64)
	if err != nil {
		t.Fatalf("decode Chat opaque JSON value: %v", err)
	}
	expectedRaw, err := json.Marshal(matrixSignature)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, expectedRaw) {
		t.Fatalf("Chat opaque raw JSON changed: got %s, want %s", raw, expectedRaw)
	}
	var opaque string
	if err := json.Unmarshal(raw, &opaque); err != nil {
		t.Fatalf("decode Chat opaque value: %v", err)
	}
	return opaque
}

func matrixFollowupRequest(route matrixRoute, output matrixOutput) map[string]any {
	if route.clientFormat == "openai" {
		initial := matrixInitialRequest(route, false)
		messages, _ := initial["messages"].([]any)
		arguments := output.ToolArguments
		if arguments == "" {
			arguments = matrixToolArguments
		}
		messages = append(messages,
			map[string]any{"role": "assistant", "content": output.Text, "reasoning_opaque": output.ChatOpaque, "tool_calls": []any{map[string]any{"id": output.CallID, "type": "function", "function": map[string]any{"name": output.ToolName, "arguments": arguments}}}},
			map[string]any{"role": "tool", "tool_call_id": output.CallID, "content": matrixToolResult},
			map[string]any{"role": "user", "content": matrixContinuation},
		)
		request := map[string]any{"model": route.model, "stream": false, "messages": messages}
		if route.upstreamPath == "/v1/messages" {
			request["reasoning_effort"] = matrixChatFollowupThinkingEffort
		}
		return request
	}
	if route.clientFormat == "claude" {
		initial := matrixInitialRequest(route, false)
		messages, _ := initial["messages"].([]any)
		content := []any{map[string]any{"type": "text", "text": matrixOutputText}}
		switch route.upstreamPath {
		case "/chat/completions":
			content = append(content, map[string]any{"type": "thinking", "thinking": "", "signature": output.Signature})
		case "/responses", "/v1/messages":
			content = append(content, map[string]any{"type": "thinking", "thinking": matrixReasoning, "signature": output.Signature})
			content = append(content, map[string]any{"type": "redacted_thinking", "data": output.Redacted})
		}
		var toolInput map[string]any
		if err := json.Unmarshal([]byte(output.ToolArguments), &toolInput); err != nil {
			toolInput = map[string]any{"query": "provider-value"}
		}
		content = append(content, map[string]any{"type": "tool_use", "id": output.ClaudeToolID, "name": output.ToolName, "input": toolInput})
		messages = append(messages,
			map[string]any{"role": "assistant", "content": content},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": output.ClaudeToolID, "content": matrixToolResult}}},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": matrixContinuation}}},
		)
		request := map[string]any{"model": route.model, "stream": false, "max_tokens": 256, "tools": []any{map[string]any{"name": matrixToolName, "description": "Inspect a matrix value.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}}}}, "messages": messages}
		if route.upstreamPath == "/v1/messages" {
			request["thinking"] = map[string]any{"type": "adaptive"}
			request["output_config"] = map[string]any{"effort": "high"}
		}
		return request
	}
	initial := matrixInitialRequest(route, false)
	input, _ := initial["input"].([]any)
	for _, rawItem := range output.ResponseItems {
		item := cloneJSONMap(rawItem)
		if route.upstreamPath == "/responses" && item["type"] == "function_call" {
			delete(item, "id")
		}
		input = append(input, item)
	}
	input = append(input,
		map[string]any{"type": "function_call_output", "call_id": output.CallID, "output": matrixToolResult},
		map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": matrixContinuation}}},
	)
	request := map[string]any{"model": route.model, "stream": false, "store": false, "input": input}
	if route.model == matrixMessagesModel {
		request["reasoning"] = map[string]any{"effort": "high"}
	}
	return request
}

func matrixThirdTurnRequest(t *testing.T, route matrixRoute, firstOutput, secondOutput matrixOutput) map[string]any {
	t.Helper()
	if route.clientFormat != "openai" || route.upstreamPath != "/v1/messages" {
		t.Fatalf("third-turn fixture is only defined for Chat to Messages: %+v", route)
	}
	request := matrixFollowupRequest(route, firstOutput)
	messages := request["messages"].([]any)
	arguments := secondOutput.ToolArguments
	if arguments == "" {
		arguments = matrixToolArguments
	}
	messages = append(messages,
		map[string]any{"role": "assistant", "content": secondOutput.Text, "reasoning_opaque": secondOutput.ChatOpaque, "tool_calls": []any{map[string]any{"id": secondOutput.CallID, "type": "function", "function": map[string]any{"name": secondOutput.ToolName, "arguments": arguments}}}},
		map[string]any{"role": "tool", "tool_call_id": secondOutput.CallID, "content": matrixToolResult},
		map[string]any{"role": "user", "content": matrixThirdContinuation},
	)
	request["messages"] = messages
	return request
}

func assertMatrixFollowupRequest(t *testing.T, route matrixRoute, request map[string]any, output matrixOutput) {
	t.Helper()
	if !containsJSONScalar(request, matrixContinuation) || !containsJSONScalar(request, matrixToolResult) {
		t.Fatalf("follow-up text or tool result was dropped on %s: %+v", route.name, request)
	}
	if route.clientFormat == "openai" {
		assertChatClientFollowup(t, route, request, output)
		return
	}
	switch route.upstreamPath {
	case "/chat/completions":
		messages := jsonObjects(request["messages"])
		var opaqueFound, callFound, resultFound bool
		var expectedToolCallID string
		for _, message := range messages {
			if message["role"] == "assistant" && message["reasoning_opaque"] == matrixSignature {
				opaqueFound = true
			}
			for _, call := range jsonObjects(message["tool_calls"]) {
				candidate := stringValue(call["id"])
				itemID, callID, ok := translate.DecodeClaudeToolIDs(candidate)
				if ok && itemID == output.ItemID && callID == output.CallID {
					callFound = true
					expectedToolCallID = candidate
				} else if !ok && candidate == output.CallID && (output.ItemID == "" || output.ItemID == output.CallID) {
					callFound = true
					expectedToolCallID = candidate
				}
			}
			if message["role"] == "tool" && message["tool_call_id"] == expectedToolCallID && expectedToolCallID != "" && containsJSONScalar(message["content"], matrixToolResult) {
				resultFound = true
			}
		}
		if !opaqueFound || !callFound || !resultFound {
			t.Fatalf("Chat replay dropped opaque thinking or tool correlation: %+v", request["messages"])
		}
	case "/responses":
		items := jsonObjects(request["input"])
		var signatureFound, redactedFound, callFound, outputFound, itemIDFound bool
		for _, item := range items {
			switch item["type"] {
			case "reasoning":
				encrypted := stringValue(item["encrypted_content"])
				signatureFound = signatureFound || encrypted == matrixSignature
				redactedFound = redactedFound || encrypted == "claude-redacted-thinking:"+matrixRedacted
			case "function_call":
				callFound = callFound || item["call_id"] == matrixResponseCallID
				itemIDFound = itemIDFound || item["id"] == matrixResponseItemID
			case "function_call_output":
				outputFound = outputFound || item["call_id"] == matrixResponseCallID
			}
		}
		if !signatureFound || !redactedFound || !callFound || !outputFound || !itemIDFound {
			t.Fatalf("Responses replay dropped signed/redacted reasoning or tool correlation: %+v", request["input"])
		}
	case "/v1/messages":
		messages := jsonObjects(request["messages"])
		var signatureFound, redactedFound, callFound, outputFound bool
		var toolID string
		for _, message := range messages {
			for _, block := range jsonObjects(message["content"]) {
				switch block["type"] {
				case "thinking":
					signatureFound = signatureFound || block["signature"] == matrixSignature
				case "redacted_thinking":
					redactedFound = redactedFound || block["data"] == matrixRedacted
				case "tool_use":
					toolID = stringValue(block["id"])
					callFound = true
				case "tool_result":
					outputFound = true
					if toolID != "" && block["tool_use_id"] != toolID {
						t.Fatalf("Messages replay tool_result id %v differs from tool_use id %q", block["tool_use_id"], toolID)
					}
				}
			}
		}
		if !signatureFound || !redactedFound || !callFound || !outputFound {
			t.Fatalf("Messages replay dropped signed/redacted reasoning or tool correlation: %+v", request["messages"])
		}
		assertAdaptiveThinkingRequest(t, request, "high")
		if route.clientFormat == "openai-response" {
			if toolID != output.CallID || output.ItemID != "fc_"+output.CallID {
				itemID, callID, ok := translate.DecodeClaudeToolIDs(toolID)
				if !ok || itemID != output.ItemID || callID != output.CallID {
					t.Fatalf("Responses tool identity changed during Messages replay: id=%q decoded=(%q,%q,%v), want (%q,%q)", toolID, itemID, callID, ok, output.ItemID, output.CallID)
				}
			}
		}
	}
}

func assertChatClientFollowup(t *testing.T, route matrixRoute, request map[string]any, output matrixOutput) {
	t.Helper()
	switch route.upstreamPath {
	case "/chat/completions":
		messages := jsonObjects(request["messages"])
		var opaqueFound, callFound, resultFound bool
		for _, message := range messages {
			opaqueFound = opaqueFound || message["role"] == "assistant" && message["reasoning_opaque"] == matrixSignature
			for _, call := range jsonObjects(message["tool_calls"]) {
				callFound = callFound || call["id"] == output.CallID && call["function"] != nil
			}
			resultFound = resultFound || message["role"] == "tool" && message["tool_call_id"] == output.CallID && containsJSONScalar(message["content"], matrixToolResult)
		}
		if !opaqueFound || !callFound || !resultFound {
			t.Fatalf("Chat replay dropped prior opaque thinking or tool correlation: %+v", request["messages"])
		}
	case "/responses":
		items := jsonObjects(request["input"])
		var signatureFound, redactedFound, callFound, outputFound bool
		itemID, callID, ok := translate.DecodeClaudeToolIDs(output.CallID)
		if !ok || itemID != matrixResponseItemID || callID != matrixResponseCallID {
			t.Fatalf("Chat replay did not receive the composite Responses call ID: %q decoded=(%q,%q,%v)", output.CallID, itemID, callID, ok)
		}
		for _, item := range items {
			switch item["type"] {
			case "reasoning":
				signatureFound = signatureFound || matrixOpaqueContains(item["encrypted_content"], matrixSignature)
				redactedFound = redactedFound || matrixOpaqueContains(item["encrypted_content"], matrixRedacted)
			case "function_call":
				callFound = callFound || item["call_id"] == callID && item["id"] == itemID
			case "function_call_output":
				outputFound = outputFound || item["call_id"] == callID && item["output"] == matrixToolResult
			}
		}
		if !signatureFound || !redactedFound || !callFound || !outputFound {
			t.Fatalf("Responses replay dropped Chat opaque thinking or tool correlation: %+v", request["input"])
		}
	case "/v1/messages":
		messages := jsonObjects(request["messages"])
		var signatureFound, redactedFound, callFound, outputFound bool
		var toolID string
		for _, message := range messages {
			for _, block := range jsonObjects(message["content"]) {
				switch block["type"] {
				case "thinking":
					signatureFound = signatureFound || block["signature"] == matrixSignature
				case "redacted_thinking":
					redactedFound = redactedFound || block["data"] == matrixRedacted
				case "tool_use":
					toolID = stringValue(block["id"])
					callFound = callFound || toolID != ""
				case "tool_result":
					outputFound = outputFound || block["tool_use_id"] == toolID && containsJSONScalar(block["content"], matrixToolResult)
				}
			}
		}
		if !signatureFound || !redactedFound || !callFound || !outputFound {
			t.Fatalf("Messages replay dropped Chat opaque thinking or tool correlation: %+v", request["messages"])
		}
		if expectedCallID := matrixMessagesToolIDFromOutput(t, output); toolID != expectedCallID {
			t.Fatalf("Chat tool-call ID changed in Messages replay: got %q, want %q", toolID, expectedCallID)
		}
		assertAdaptiveThinkingRequest(t, request, matrixChatFollowupThinkingEffort)
	}
}

func assertChatClientThirdTurnRequest(t *testing.T, request map[string]any, firstOutput, secondOutput matrixOutput) {
	t.Helper()
	if request["model"] != matrixChatMessagesModel || !containsJSONScalar(request, matrixThirdContinuation) {
		t.Fatalf("third Chat continuation or model was lost: %+v", request)
	}
	wantSignatures := map[string]bool{matrixSignatureForTurn(1): false, matrixSignatureForTurn(2): false}
	wantRedacted := map[string]bool{matrixRedactedForTurn(1): false, matrixRedactedForTurn(2): false}
	wantThinking := map[string]bool{matrixReasoningForTurn(1): false, matrixReasoningForTurn(2): false}
	wantCalls := map[string]bool{matrixMessagesToolIDFromOutput(t, firstOutput): false, matrixMessagesToolIDFromOutput(t, secondOutput): false}
	toolResults := make(map[string]bool, len(wantCalls))
	for _, message := range jsonObjects(request["messages"]) {
		for _, block := range jsonObjects(message["content"]) {
			switch block["type"] {
			case "thinking":
				if thinking := stringValue(block["thinking"]); thinking != "" {
					if _, exists := wantThinking[thinking]; exists {
						wantThinking[thinking] = true
					}
				}
				if signature := stringValue(block["signature"]); signature != "" {
					if _, exists := wantSignatures[signature]; exists {
						wantSignatures[signature] = true
					}
				}
			case "redacted_thinking":
				if data := stringValue(block["data"]); data != "" {
					if _, exists := wantRedacted[data]; exists {
						wantRedacted[data] = true
					}
				}
			case "tool_use":
				if id := stringValue(block["id"]); id != "" {
					if _, exists := wantCalls[id]; exists {
						wantCalls[id] = true
					}
				}
			case "tool_result":
				id := stringValue(block["tool_use_id"])
				if _, exists := wantCalls[id]; exists && containsJSONScalar(block["content"], matrixToolResult) {
					toolResults[id] = true
				}
			}
		}
	}
	for value, found := range wantSignatures {
		if !found {
			t.Errorf("third Chat call dropped prior thinking signature %q", value)
		}
	}
	for value, found := range wantThinking {
		if !found {
			t.Errorf("third Chat call dropped prior thinking text %q", value)
		}
	}
	for value, found := range wantRedacted {
		if !found {
			t.Errorf("third Chat call dropped prior redacted thinking block %q", value)
		}
	}
	for id, found := range wantCalls {
		if !found || !toolResults[id] {
			t.Errorf("third Chat call dropped tool_use/tool_result ID pair %q: calls=%v results=%v", id, wantCalls, toolResults)
		}
	}
	assertAdaptiveThinkingRequest(t, request, matrixChatFollowupThinkingEffort)
}

func matrixMessagesToolIDFromOutput(t *testing.T, output matrixOutput) string {
	t.Helper()
	itemID, callID, ok := translate.DecodeClaudeToolIDs(output.CallID)
	wantCallID := matrixMessagesToolIDForTurn(output.Turn)
	if ok {
		if itemID != matrixMessagesItemIDForTurn(output.Turn) || callID != wantCallID {
			t.Fatalf("Chat tool carrier changed Messages IDs: carrier=%q decoded=(%q,%q), want (%q,%q)", output.CallID, itemID, callID, matrixMessagesItemIDForTurn(output.Turn), wantCallID)
		}
		return callID
	}
	if output.CallID != wantCallID {
		t.Fatalf("Chat tool ID changed on the wire: got %q, want %q", output.CallID, wantCallID)
	}
	return output.CallID
}

func assertChatClientToolResultEcho(t *testing.T, route matrixRoute, request map[string]any, output matrixOutput) {
	t.Helper()
	if route.clientFormat != "openai" {
		return
	}
	messages := jsonObjects(request["messages"])
	var callID, resultID string
	for _, message := range messages {
		for _, call := range jsonObjects(message["tool_calls"]) {
			callID = stringValue(call["id"])
		}
		if message["role"] == "tool" {
			resultID = stringValue(message["tool_call_id"])
		}
	}
	if callID != output.CallID || resultID != output.CallID || callID != resultID {
		t.Fatalf("Chat tool result did not echo the received call ID on %s: call=%q result=%q received=%q", route.name, callID, resultID, output.CallID)
	}
}

func assertAdaptiveThinkingRequest(t *testing.T, request map[string]any, expectedEffort string) {
	t.Helper()
	thinking, _ := request["thinking"].(map[string]any)
	outputConfig, _ := request["output_config"].(map[string]any)
	if thinking["type"] != "adaptive" || outputConfig["effort"] != expectedEffort {
		t.Fatalf("Messages request did not preserve adaptive thinking at %q effort: %+v", expectedEffort, request)
	}
	if _, exists := thinking["budget_tokens"]; exists {
		t.Fatalf("Messages request used legacy budget_tokens with adaptive thinking: %+v", request)
	}
}

func assertMatrixResponseDoneItems(t *testing.T, events []map[string]any, output []map[string]any) {
	t.Helper()
	done := make(map[string]map[string]any)
	for _, event := range events {
		if event["type"] == "response.output_item.done" {
			item, _ := event["item"].(map[string]any)
			if item == nil {
				t.Fatalf("Responses output_item.done event has no item: %+v", event)
			}
			key := stringValue(item["type"]) + ":" + firstStringValue(item, "id", "call_id")
			done[key] = item
		}
	}
	for _, item := range output {
		key := stringValue(item["type"]) + ":" + firstStringValue(item, "id", "call_id")
		if _, exists := done[key]; !exists {
			t.Fatalf("Responses stream omitted output_item.done for %q: events=%+v", key, events)
		}
	}
}

func assertSingleCompaction(t *testing.T, response []byte) string {
	t.Helper()
	var result struct {
		Status string `json:"status"`
		Output []struct {
			Type             string `json:"type"`
			EncryptedContent string `json:"encrypted_content"`
			Content          []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(response, &result); err != nil {
		t.Fatalf("decode compaction response: %v: %s", err, response)
	}
	if result.Status != "completed" {
		t.Fatalf("compaction response status = %q, want completed", result.Status)
	}
	count := 0
	var capsule string
	var summary string
	for _, item := range result.Output {
		if item.Type == "compaction" {
			count++
			capsule = item.EncryptedContent
		}
		for _, part := range item.Content {
			summary += part.Text
		}
	}
	if count != 1 || capsule == "" {
		t.Fatalf("compaction item count = %d, capsule present=%v: %s", count, capsule != "", response)
	}
	if !strings.Contains(summary, "active goal is to finish the native host bridge") {
		t.Fatalf("ordinary summary output missing or changed: %s", response)
	}
	return capsule
}

func assertCompactionReplay(t *testing.T, base string, state *fixture, capsule, model, continuation string, includeTrigger bool) {
	t.Helper()
	replayInput := []any{
		map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": continuation}}},
		map[string]any{"type": "compaction", "encrypted_content": capsule},
	}
	if includeTrigger {
		replayInput = append(replayInput, map[string]any{"type": "compaction_trigger"})
	}
	request := map[string]any{"model": model, "input": replayInput, "prompt_cache_key": matrixCompactCacheKey}
	callProxy(t, base+"/v1/responses", request)
	captured, path := lastUpstreamRequest(t, state)
	if path != "/responses" {
		t.Fatalf("replay request used upstream path %q", path)
	}
	if captured["model"] != model {
		t.Fatalf("compaction replay model = %v, want %q", captured["model"], model)
	}
	if captured["prompt_cache_key"] != matrixCompactCacheKey {
		t.Fatalf("compaction replay changed the explicit prompt cache key: %+v", captured)
	}
	capturedInput, err := json.Marshal(captured["input"])
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(capturedInput, []byte(capsule)) || bytes.Contains(capturedInput, []byte("cpa-copilot-bridge:compaction:")) || bytes.Contains(capturedInput, []byte(`"type":"compaction"`)) || bytes.Contains(capturedInput, []byte("compaction_trigger")) {
		t.Fatalf("replay request retained opaque capsule or trigger: %s", capturedInput)
	}
	if !bytes.Contains(capturedInput, []byte("The active goal is to finish the native host bridge")) || !bytes.Contains(capturedInput, []byte(continuation)) {
		t.Fatalf("replay request omitted prior summary or continuation: %s", capturedInput)
	}
}

func parseSSEDataEvents(t *testing.T, stream []byte) []map[string]any {
	t.Helper()
	events, done := parseSSEDataEventsWithDone(t, stream)
	if done {
		t.Fatal("Responses V2 compaction stream must end with response.completed, without [DONE]")
	}
	return events
}

func parseSSEDataEventsWithDone(t *testing.T, stream []byte) ([]map[string]any, bool) {
	t.Helper()
	var events []map[string]any
	var done bool
	blocks := strings.Split(strings.ReplaceAll(string(stream), "\r\n", "\n"), "\n\n")
	for _, block := range blocks {
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(line, "data:") {
				data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if data == "[DONE]" {
					done = true
				} else {
					var event map[string]any
					if err := json.Unmarshal([]byte(data), &event); err != nil {
						t.Fatalf("decode SSE data event: %v: %s", err, data)
					}
					events = append(events, event)
				}
			}
		}
	}
	if len(events) == 0 {
		t.Fatalf("stream contained no SSE data events: %s", stream)
	}
	return events, done
}

func assertEventType(t *testing.T, event map[string]any, expected string) {
	t.Helper()
	if event["type"] != expected {
		t.Fatalf("event type = %v, want %s", event["type"], expected)
	}
}

func doneItemType(event map[string]any) string {
	item, _ := event["item"].(map[string]any)
	typeName, _ := item["type"].(string)
	return typeName
}

func assertEqualJSON(t *testing.T, actual, expected any) {
	t.Helper()
	a, _ := json.Marshal(actual)
	b, _ := json.Marshal(expected)
	if !bytes.Equal(a, b) {
		t.Fatalf("protocol payload changed: actual=%s expected=%s", a, b)
	}
}
