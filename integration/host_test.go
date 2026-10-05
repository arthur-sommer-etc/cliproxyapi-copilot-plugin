package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
}

const (
	matrixChatModel        = "bridge-gemini-3.8"
	matrixResponsesModel   = "bridge-gpt-6-luna"
	matrixMessagesModel    = "bridge-sonnet-5.5"
	matrixSignature        = "provider-signature/+ exact\n\t "
	matrixRedacted         = "provider-redacted/+ exact\n\t "
	matrixReasoning        = "provider reasoning exact"
	matrixOutputText       = "provider text exact"
	matrixToolName         = "inspect"
	matrixToolArguments    = `{"query":"provider-value"}`
	matrixInitialSignature = "initial-signature/+ exact\n\t "
	matrixInitialRedacted  = "initial-redacted/+ exact\n\t "
	matrixInputMarker      = "matrix user history exact"
	matrixContinuation     = "matrix continuation exact"
	matrixToolResult       = "matrix tool result exact"
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
	Signature     string
	Redacted      string
	Text          string
	ClaudeToolID  string
	ItemID        string
	CallID        string
	ToolName      string
	ToolArguments string
	ResponseItems []map[string]any
}

func TestNativeHostProtocolRoundTrips(t *testing.T) {
	binary := os.Getenv("CPA_BINARY")
	if binary == "" {
		t.Skip("set CPA_BINARY to a built CLIProxyAPI v8 server for native host integration")
	}
	state := &fixture{canceled: make(chan struct{})}
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
		history := []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Remember the active goal: finish the native host bridge."}}}}
		input := append(append([]any(nil), history...), map[string]any{"type": "compaction_trigger"})
		response := callProxy(t, base+"/v1/responses", map[string]any{"model": "bridge-responses", "input": input})
		captured, path := lastUpstreamRequest(t, state)
		assertCompactionRequest(t, captured, path)
		capsule := assertSingleCompaction(t, response)
		assertCompactionReplay(t, base, state, capsule, "Continue the bridge task.", false)
	})
	t.Run("ResponsesCompactionTriggerStreamingRoundTrip", func(t *testing.T) {
		history := []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Remember the active goal: finish the native host bridge."}}}}
		input := append(append([]any(nil), history...), map[string]any{"type": "compaction_trigger"})
		stream := callProxy(t, base+"/v1/responses", map[string]any{"model": "bridge-responses", "stream": true, "input": input})
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
		if completed["id"] != "resp_fixture" || completed["status"] != "completed" {
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
			if itemMap["id"] == "msg_summary" {
				summaryFound = true
				assertEqualJSON(t, itemMap, map[string]any{"type": "message", "id": "msg_summary", "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "The active goal is to finish the native host bridge. The next step is integration."}}})
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
		assertCompactionReplay(t, base, state, capsule, "Continue the streamed bridge task.", true)
	})
	t.Run("ResponsesCompactRouteRoundTrip", func(t *testing.T) {
		request := map[string]any{
			"model": "bridge-responses",
			"input": []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Summarize the native host bridge task."}}}},
		}
		response := callProxy(t, base+"/v1/responses/compact", request)
		captured, path := lastUpstreamRequest(t, state)
		assertCompactionRequest(t, captured, path)
		capsule := assertSingleCompaction(t, response)
		assertCompactionReplay(t, base, state, capsule, "Continue after the compact route.", false)
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
	t.Run("SixClientProviderProtocolRoutes", func(t *testing.T) {
		routes := []matrixRoute{
			{name: "ClaudeToGeminiChat", clientFormat: "claude", model: matrixChatModel, upstreamPath: "/chat/completions"},
			{name: "ResponsesToGeminiChat", clientFormat: "openai-response", model: matrixChatModel, upstreamPath: "/chat/completions"},
			{name: "ClaudeToGPT6Luna", clientFormat: "claude", model: matrixResponsesModel, upstreamPath: "/responses"},
			{name: "ResponsesToGPT6Luna", clientFormat: "openai-response", model: matrixResponsesModel, upstreamPath: "/responses"},
			{name: "ClaudeToSonnet55", clientFormat: "claude", model: matrixMessagesModel, upstreamPath: "/v1/messages"},
			{name: "ResponsesToSonnet55", clientFormat: "openai-response", model: matrixMessagesModel, upstreamPath: "/v1/messages"},
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
					callProxyWithSession(t, base+matrixClientPath(route.clientFormat), followup, session)
					followupCaptured, followupPath := lastUpstreamRequest(t, state)
					assertMatrixRoute(t, route, followupCaptured, followupPath, false)
					assertMatrixFollowupRequest(t, route, followupCaptured, output)
				})
			}
		}
	})
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
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{
			map[string]any{"id": "bridge-responses", "supported_endpoints": []string{"/responses"}, "capabilities": map[string]any{"type": "chat", "supports": map[string]bool{"streaming": true, "tool_calls": true}}},
			map[string]any{"id": "bridge-messages", "supported_endpoints": []string{"/v1/messages", "/chat/completions"}, "capabilities": map[string]any{"type": "chat"}},
			map[string]any{"id": "bridge-chat", "supported_endpoints": []string{"/chat/completions"}, "capabilities": map[string]any{"type": "chat"}},
			map[string]any{"id": "bridge-gemini-3.8", "vendor": "Google", "supported_endpoints": []string{"/chat/completions"}, "capabilities": map[string]any{"type": "chat", "family": "gemini", "supports": map[string]bool{"streaming": true, "tool_calls": true}}},
			map[string]any{"id": "bridge-gpt-6-luna", "vendor": "OpenAI", "supported_endpoints": []string{"/responses"}, "capabilities": map[string]any{"type": "chat", "family": "gpt", "supports": map[string]bool{"streaming": true, "tool_calls": true}}},
			map[string]any{"id": "bridge-sonnet-5.5", "vendor": "Anthropic", "supported_endpoints": []string{"/chat/completions", "/v1/messages"}, "capabilities": map[string]any{"type": "chat", "family": "claude", "supports": map[string]any{"streaming": true, "tool_calls": true, "adaptive_thinking": true, "reasoning_effort": []string{"low", "medium", "high", "max"}}}},
		}})
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

func isMatrixModel(model string) bool {
	return model == matrixChatModel || model == matrixResponsesModel || model == matrixMessagesModel
}

func writeMatrixResponse(w http.ResponseWriter, path string, request map[string]any) {
	model := stringValue(request["model"])
	if request["stream"] != true {
		w.Header().Set("Content-Type", "application/json")
		var response any
		switch path {
		case "/chat/completions":
			response = matrixChatResponse(model)
		case "/responses":
			response = matrixResponsesResponse(model)
		case "/v1/messages":
			response = matrixMessagesResponse(model)
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
		writeMatrixMessagesStream(w, model)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
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

func matrixMessagesResponse(model string) map[string]any {
	return map[string]any{"id": "msg_matrix", "type": "message", "role": "assistant", "model": model, "content": []any{
		map[string]any{"type": "text", "text": matrixOutputText},
		map[string]any{"type": "thinking", "thinking": matrixReasoning, "signature": matrixSignature},
		map[string]any{"type": "redacted_thinking", "data": matrixRedacted},
		map[string]any{"type": "tool_use", "id": matrixMessagesToolID, "name": matrixToolName, "input": map[string]any{"query": "provider-value"}},
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

func writeMatrixMessagesStream(w http.ResponseWriter, model string) {
	writeSSEEvent(w, "message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_matrix", "type": "message", "role": "assistant", "model": model, "content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 4, "output_tokens": 0}}})
	writeSSEEvent(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
	writeSSEEvent(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": matrixOutputText}})
	writeSSEEvent(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	writeSSEEvent(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "thinking", "thinking": ""}})
	writeSSEEvent(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "thinking_delta", "thinking": matrixReasoning}})
	writeSSEEvent(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "signature_delta", "signature": matrixSignature}})
	writeSSEEvent(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 1})
	writeSSEEvent(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 2, "content_block": map[string]any{"type": "redacted_thinking", "data": matrixRedacted}})
	writeSSEEvent(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 2})
	writeSSEEvent(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 3, "content_block": map[string]any{"type": "tool_use", "id": matrixMessagesToolID, "name": matrixToolName, "input": map[string]any{}}})
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

func startProxyInRoot(t *testing.T, binary, upstream, root string, authJSON []byte, managementSecret string) (string, func()) {
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
	config := fmt.Sprintf("config-version: 8\nserver:\n  host: 127.0.0.1\n  port: %d\nmanagement:\n  disable-control-panel: true\n  secret-key: %q\naccess:\n  api-keys: [fixture-client-key]\noauth:\n  auth-dir: %q\nplugins:\n  enabled: true\n  dir: %q\n  configs:\n    cliproxyapi-copilot:\n      enabled: true\n      allow_insecure_base_urls: true\n      compaction_models: [bridge-responses]\n      reasoning_replay: true\n      github_base_url: %q\n      github_api_url: %q\n      copilot_api_url: %q\n", port, managementSecret, authDir, filepath.Join(root, "plugins"), upstream, upstream, upstream)
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "host.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	command := exec.CommandContext(ctx, binary, "--config", configPath, "--local-model")
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

func nativeAuthFixtureJSON(t *testing.T) []byte {
	t.Helper()
	token := "fixture-github-token"
	fingerprint := sha256.Sum256([]byte(token))
	storage := map[string]any{
		"type":                "copilot",
		"github_access_token": token,
		"github_login":        "fixture",
		"github_user_id":      4242,
		"expires_at":          int64(4102444800),
		"continuity_keyring": map[string]any{
			"version":                1,
			"key_id":                 "00112233445566778899aabbccddeeff",
			"account_id":             4242,
			"root_key":               base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x5a}, 32)),
			"credential_fingerprint": hex.EncodeToString(fingerprint[:]),
		},
	}
	data, err := json.Marshal(storage)
	if err != nil {
		t.Fatal(err)
	}
	return data
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

func assertCompactionRequest(t *testing.T, request map[string]any, path string) {
	t.Helper()
	if path != "/responses" {
		t.Fatalf("compaction request used upstream path %q", path)
	}
	if request["tool_choice"] != "none" {
		t.Fatalf("compaction request tool_choice = %v, want none", request["tool_choice"])
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
	if !bytes.Contains(input, []byte("transcript data")) {
		t.Fatalf("summary instruction missing from Copilot request: %s", input)
	}
}

func matrixClientPath(format string) string {
	if format == "claude" {
		return "/v1/messages"
	}
	return "/v1/responses"
}

func matrixInitialRequest(route matrixRoute, stream bool) map[string]any {
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
		if containsJSONScalar(request, matrixInitialSignature) || containsJSONScalar(request, matrixInitialRedacted) {
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
				callFound = callFound || toolCallID == "toolu_initial_1" || toolCallID == matrixInitialCallID
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
	if !signatureFound || !redactedFound || !callFound || !outputFound {
		t.Fatalf("Messages request dropped signed/redacted history or tool correlation: %+v", request["messages"])
	}
	assertAdaptiveThinkingRequest(t, request)
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
	t.Helper()
	output := matrixOutput{}
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
			if block == nil || delta == nil {
				continue
			}
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
	if output.Text == "" || !strings.Contains(output.Text, matrixOutputText) {
		t.Fatalf("output text was dropped on %s: %+v", route.name, output)
	}
	if output.ToolName != matrixToolName || output.ToolArguments != matrixToolArguments {
		t.Fatalf("tool name or arguments changed on %s: %+v", route.name, output)
	}
	switch route.upstreamPath {
	case "/chat/completions":
		decoded := assertChatReasoningCarrier(t, output.Signature, route.model)
		if decoded != matrixSignature || output.CallID != matrixChatCallID {
			t.Fatalf("Chat opaque reasoning or call ID changed on %s: opaque=%q call=%q", route.name, decoded, output.CallID)
		}
		if output.Redacted != "" {
			t.Fatalf("Chat route unexpectedly returned a redacted thinking block: %+v", output)
		}
	case "/responses":
		if output.Signature != matrixSignature || output.Redacted != matrixRedacted || output.ItemID != matrixResponseItemID || output.CallID != matrixResponseCallID {
			t.Fatalf("Responses output lost opaque history or separate tool IDs on %s: %+v", route.name, output)
		}
	case "/v1/messages":
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

func assertMatrixFollowupRequest(t *testing.T, route matrixRoute, request map[string]any, output matrixOutput) {
	t.Helper()
	if !containsJSONScalar(request, matrixContinuation) || !containsJSONScalar(request, matrixToolResult) {
		t.Fatalf("follow-up text or tool result was dropped on %s: %+v", route.name, request)
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
		assertAdaptiveThinkingRequest(t, request)
		if route.clientFormat == "openai-response" {
			itemID, callID, ok := translate.DecodeClaudeToolIDs(toolID)
			if !ok || itemID != output.ItemID || callID != output.CallID {
				t.Fatalf("Responses tool identity changed during Messages replay: id=%q decoded=(%q,%q,%v), want (%q,%q)", toolID, itemID, callID, ok, output.ItemID, output.CallID)
			}
		}
	}
}

func assertAdaptiveThinkingRequest(t *testing.T, request map[string]any) {
	t.Helper()
	thinking, _ := request["thinking"].(map[string]any)
	outputConfig, _ := request["output_config"].(map[string]any)
	if thinking["type"] != "adaptive" || outputConfig["effort"] != "high" {
		t.Fatalf("Messages request did not preserve adaptive thinking at high effort: %+v", request)
	}
	if _, exists := thinking["budget_tokens"]; exists {
		t.Fatalf("Messages request used legacy budget_tokens with adaptive thinking: %+v", request)
	}
}

func assertMatrixResponseDoneItems(t *testing.T, events []map[string]any, output []map[string]any) {
	t.Helper()
	done := make(map[string]map[string]any)
	for _, event := range events {
		if event["type"] != "response.output_item.done" {
			continue
		}
		item, _ := event["item"].(map[string]any)
		if item == nil {
			t.Fatalf("Responses output_item.done event has no item: %+v", event)
		}
		key := stringValue(item["type"]) + ":" + firstStringValue(item, "id", "call_id")
		done[key] = item
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

func assertCompactionReplay(t *testing.T, base string, state *fixture, capsule, continuation string, includeTrigger bool) {
	t.Helper()
	replayInput := []any{
		map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": continuation}}},
		map[string]any{"type": "compaction", "encrypted_content": capsule},
	}
	if includeTrigger {
		replayInput = append(replayInput, map[string]any{"type": "compaction_trigger"})
	}
	request := map[string]any{"model": "bridge-responses", "input": replayInput}
	callProxy(t, base+"/v1/responses", request)
	captured, path := lastUpstreamRequest(t, state)
	if path != "/responses" {
		t.Fatalf("replay request used upstream path %q", path)
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
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				done = true
				continue
			}
			var event map[string]any
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				t.Fatalf("decode SSE data event: %v: %s", err, data)
			}
			events = append(events, event)
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
