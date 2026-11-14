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
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"gopkg.in/yaml.v3"
)

func TestNativeHostPayloadFinalization(t *testing.T) {
	binary := os.Getenv("CPA_BINARY")
	if binary == "" {
		t.Skip("set CPA_BINARY to the matched CLIProxyAPI host for native payload finalization")
	}
	pluginPath := strings.TrimSpace(os.Getenv("CPA_LIVE_PLUGIN_PATH"))
	if pluginPath == "" {
		t.Fatal("set CPA_LIVE_PLUGIN_PATH to the matched Copilot plugin artifact")
	}
	state := newNativeFixture(t)
	upstream := &nativePayloadCapture{fixture: state}
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	base := startProxyWithPayloadRules(t, binary, server.URL)
	t.Run("ClaudeMessagesJSONRetryReusesFinalPayload", func(t *testing.T) {
		request := map[string]any{
			"model":       "bridge-responses",
			"max_tokens":  64,
			"temperature": 0.6,
			"messages":    []any{map[string]any{"role": "user", "content": "Reply with ok."}},
		}
		status, _, response := postNativeCompatibilityRequest(t, base+"/v1/messages", request, "payload-finalization-json-retry")
		if status != http.StatusOK {
			t.Fatalf("Claude Messages request failed: status=%d body=%s", status, response)
		}
		attempts := upstream.snapshot("/responses")
		if len(attempts) != 2 {
			t.Fatalf("Responses egress attempts = %d, want initial 401 plus one retry", len(attempts))
		}
		if !bytes.Equal(attempts[0].body, attempts[1].body) {
			t.Fatalf("retry changed finalized request bytes:\nfirst:  %s\nsecond: %s", attempts[0].body, attempts[1].body)
		}
		assertNativeFinalizedResponsesPayload(t, attempts[1].body, 17, "")
	})

	t.Run("ClaudeMessagesSSEUsesSameFinalRules", func(t *testing.T) {
		request := map[string]any{
			"model":       "bridge-responses",
			"stream":      true,
			"max_tokens":  64,
			"temperature": 0.6,
			"messages":    []any{map[string]any{"role": "user", "content": "Reply with ok."}},
		}
		status, headers, response := postNativeCompatibilityRequest(t, base+"/v1/messages", request, "payload-finalization-sse")
		if status != http.StatusOK || !strings.Contains(headers.Get("Content-Type"), "text/event-stream") || !bytes.Contains(response, []byte("message_stop")) {
			t.Fatalf("Claude Messages SSE response malformed: status=%d content-type=%q body=%s", status, headers.Get("Content-Type"), response)
		}
		attempts := upstream.snapshot("/responses")
		if len(attempts) != 3 {
			t.Fatalf("total Responses egress attempts = %d, want the retried JSON plus one SSE", len(attempts))
		}
		assertNativeFinalizedResponsesPayload(t, attempts[2].body, 17, "")
	})

	t.Run("ResponsesWebsocketUsesResponsesSourceRule", func(t *testing.T) {
		connection := dialNativeResponsesWebsocket(t, base)
		request := nativeWebsocketRequest(t, "bridge-responses", "/responses")
		request["temperature"] = 0.6
		request["max_output_tokens"] = 64
		request["prompt_cache_key"] = "ws-source-cache-key"
		if err := connection.WriteJSON(request); err != nil {
			t.Fatalf("send native response.create request: %v", err)
		}
		events := readPluginResponsesWebsocket(t, connection)
		_ = nativeCompletedResponse(t, events)
		attempts := upstream.snapshot("/responses")
		if len(attempts) != 4 {
			t.Fatalf("total Responses egress attempts = %d, want prior requests plus one WebSocket", len(attempts))
		}
		assertNativeFinalizedResponsesPayload(t, attempts[3].body, 23, "ws-source-cache-key")
	})

	t.Run("ChatProtocolDoesNotReceiveResponsesRules", func(t *testing.T) {
		request := map[string]any{
			"model":       "bridge-chat",
			"temperature": 0.6,
			"messages":    []any{map[string]any{"role": "user", "content": "Reply with ok."}},
		}
		status, _, response := postNativeCompatibilityRequest(t, base+"/v1/chat/completions", request, "payload-finalization-chat")
		if status != http.StatusOK {
			t.Fatalf("Chat request failed: status=%d body=%s", status, response)
		}
		attempts := upstream.snapshot("/chat/completions")
		if len(attempts) != 1 {
			t.Fatalf("Chat egress attempts = %d, want one", len(attempts))
		}
		payload := nativePayloadObject(t, attempts[0].body)
		if _, exists := payload["service_tier"]; exists {
			t.Fatalf("Responses protocol override leaked into Chat payload: %s", attempts[0].body)
		}
		if _, exists := payload["max_output_tokens"]; exists {
			t.Fatalf("Responses source rule leaked into Chat payload: %s", attempts[0].body)
		}
	})

	t.Run("CredentialAndCatalogRequestsRemainUnmodified", func(t *testing.T) {
		requests := upstream.snapshot("")
		var sawCatalog, sawCredential bool
		for _, request := range requests {
			switch request.path {
			case "/models":
				if request.method != http.MethodGet || len(bytes.TrimSpace(request.body)) != 0 {
					t.Fatalf("model catalog request was rewritten: method=%s body=%s", request.method, request.body)
				}
				sawCatalog = true
			case "/copilot_internal/v2/token":
				for _, configuredField := range [][]byte{[]byte("service_tier"), []byte("max_output_tokens")} {
					if bytes.Contains(request.body, configuredField) {
						t.Fatalf("payload rules modified token request: %s", request.body)
					}
				}
				sawCredential = true
			}
		}
		if !sawCatalog || !sawCredential {
			t.Fatalf("expected native catalog and token requests, got catalog=%v credential=%v paths=%v", sawCatalog, sawCredential, upstream.paths())
		}
	})
}

func TestNativeHostPayloadFinalizationPreservesCodexReservedSchemas(t *testing.T) {
	binary := os.Getenv("CPA_BINARY")
	if binary == "" {
		t.Skip("set CPA_BINARY to the matched CLIProxyAPI host for native payload finalization")
	}
	pluginPath := strings.TrimSpace(os.Getenv("CPA_LIVE_PLUGIN_PATH"))
	if pluginPath == "" {
		t.Fatal("set CPA_LIVE_PLUGIN_PATH to the matched Copilot plugin artifact")
	}

	for _, mode := range []string{"HTTPJSON", "HTTPSSE", "ResponsesWebsocket"} {
		t.Run(mode, func(t *testing.T) {
			state := newNativeFixture(t)
			upstream := &nativePayloadCapture{fixture: state}
			server := httptest.NewServer(upstream)
			t.Cleanup(server.Close)
			base := startProxyWithPayloadRules(t, binary, server.URL)

			stream := mode != "HTTPJSON"
			request := nativeCodexResponsesPayload(t, "gpt-6-luna", stream)
			switch mode {
			case "HTTPJSON", "HTTPSSE":
				status, headers, response := postNativeCodexResponses(t, base+"/v1/responses", request, mode)
				if status != http.StatusOK {
					t.Fatalf("native Codex Responses request failed: status=%d body=%s", status, response)
				}
				if stream && (!strings.Contains(headers.Get("Content-Type"), "text/event-stream") || !bytes.Contains(response, []byte("response.completed"))) {
					t.Fatalf("native Codex Responses SSE response malformed: content-type=%q body=%s", headers.Get("Content-Type"), response)
				}
			case "ResponsesWebsocket":
				request["type"] = "response.create"
				connection := dialNativeCodexResponsesWebsocket(t, base)
				if err := connection.WriteJSON(request); err != nil {
					t.Fatalf("send native Codex response.create request: %v", err)
				}
				events := readPluginResponsesWebsocket(t, connection)
				_ = nativeCompletedResponse(t, events)
			}

			attempts := upstream.snapshot("/responses")
			if len(attempts) != 2 {
				t.Fatalf("Responses egress attempts = %d, want initial 401 plus one retry; paths=%v", len(attempts), upstream.paths())
			}
			if !bytes.Equal(attempts[0].body, attempts[1].body) {
				t.Fatalf("retry changed finalized native Codex request bytes:\nfirst:  %s\nsecond: %s", attempts[0].body, attempts[1].body)
			}
			assertNativeCodexReservedSchema(t, attempts[1].body)
		})
	}
}

func nativeCodexResponsesPayload(t *testing.T, model string, stream bool) map[string]any {
	t.Helper()
	reservedTool := map[string]any{
		"type":        "function",
		"name":        "wait_agent",
		"description": "Wait for a mailbox update from any live agent, including queued messages and final-status notifications. The wait also ends early when new user input is steered into the active turn. Does not return the content; returns either a summary of which agents have updates (if any), an interruption summary for steered input, or a timeout summary if no activity arrives before the deadline.",
		"strict":      false,
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"timeout_ms": map[string]any{
					"type":        "number",
					"description": "Timeout in milliseconds. Defaults to 30000, min 10000, max 3600000.",
				},
			},
			"additionalProperties": false,
		},
	}
	namespace := map[string]any{
		"type":        "namespace",
		"name":        "collaboration",
		"description": "Tools for spawning and managing sub-agents.",
		"tools":       []any{reservedTool},
	}
	return map[string]any{
		"model":             model,
		"stream":            stream,
		"max_output_tokens": 64,
		"prompt_cache_key":  "codex-reserved-schema-cache-key",
		"input":             []any{map[string]any{"type": "message", "role": "user", "content": "Wait for the delegated task."}},
		"tools":             []any{namespace},
	}
}

func postNativeCodexResponses(t *testing.T, endpoint string, payload any, session string) (int, http.Header, []byte) {
	t.Helper()
	body, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		t.Fatalf("marshal native Codex Responses request: %v", errMarshal)
	}
	request, errRequest := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, bytes.NewReader(body))
	if errRequest != nil {
		t.Fatalf("create native Codex Responses request: %v", errRequest)
	}
	request.Header.Set("Authorization", "Bearer fixture-client-key")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Session-ID", session)
	request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")
	response, errDo := http.DefaultClient.Do(request)
	if errDo != nil {
		t.Fatalf("send native Codex Responses request: %v", errDo)
	}
	defer func() { _ = response.Body.Close() }()
	responseBody, errRead := io.ReadAll(response.Body)
	if errRead != nil {
		t.Fatalf("read native Codex Responses response: %v", errRead)
	}
	return response.StatusCode, response.Header.Clone(), responseBody
}

func dialNativeCodexResponsesWebsocket(t *testing.T, base string) *websocket.Conn {
	t.Helper()
	header := http.Header{
		"Authorization": {"Bearer fixture-client-key"},
		"User-Agent":    {"codex_cli_rs/0.1.0"},
	}
	connection, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(base, "http")+"/v1/responses", header)
	if err != nil {
		if response != nil {
			t.Fatalf("connect native Codex Responses WebSocket: status=%d", response.StatusCode)
		}
		t.Fatalf("connect native Codex Responses WebSocket: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return connection
}

func assertNativeCodexReservedSchema(t *testing.T, body []byte) {
	t.Helper()
	payload := nativePayloadObject(t, body)
	if payload["service_tier"] != "priority" {
		t.Fatalf("final payload rule was not applied after native translation: %s", body)
	}
	if payload["max_output_tokens"] != float64(23) {
		t.Fatalf("Responses-source payload rule max_output_tokens = %v, want 23: %s", payload["max_output_tokens"], body)
	}
	if payload["prompt_cache_key"] != "codex-reserved-schema-cache-key" {
		t.Fatalf("native Codex payload lost prompt_cache_key: %s", body)
	}
	wantFunction := map[string]any{
		"type":        "function",
		"name":        "wait_agent",
		"description": "Wait for a mailbox update from any live agent, including queued messages and final-status notifications. The wait also ends early when new user input is steered into the active turn. Does not return the content; returns either a summary of which agents have updates (if any), an interruption summary for steered input, or a timeout summary if no activity arrives before the deadline.",
		"strict":      false,
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"timeout_ms": map[string]any{
					"type":        "number",
					"description": "Timeout in milliseconds. Defaults to 30000, min 10000, max 3600000.",
				},
			},
			"additionalProperties": false,
		},
	}
	wantNamespace := map[string]any{
		"type":        "namespace",
		"name":        "collaboration",
		"description": "Tools for spawning and managing sub-agents.",
		"tools":       []any{wantFunction},
	}
	wantBody, errMarshal := json.Marshal(wantNamespace)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	var want any
	if errUnmarshal := json.Unmarshal(wantBody, &want); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	tools, ok := payload["tools"].([]any)
	if !ok || len(tools) != 1 || !reflect.DeepEqual(tools[0], want) {
		t.Fatalf("reserved namespace/function declaration changed: tools=%v body=%s", payload["tools"], body)
	}
}

func assertNativeFinalizedResponsesPayload(t *testing.T, body []byte, maxOutputTokens int, wantPromptCacheKey string) {
	t.Helper()
	payload := nativePayloadObject(t, body)
	if payload["service_tier"] != "priority" {
		t.Fatalf("protocol-specific service_tier = %v, want priority in %s", payload["service_tier"], body)
	}
	if payload["max_output_tokens"] != float64(maxOutputTokens) {
		t.Fatalf("source-specific max_output_tokens = %v, want %d in %s", payload["max_output_tokens"], maxOutputTokens, body)
	}
	if got := stringValue(payload["prompt_cache_key"]); got != wantPromptCacheKey {
		t.Fatalf("prompt_cache_key = %q, want %q in %s", got, wantPromptCacheKey, body)
	}
	if payload["model"] != "bridge-responses" {
		t.Fatalf("finalized model = %v, want bridge-responses in %s", payload["model"], body)
	}
}

func nativePayloadObject(t *testing.T, body []byte) map[string]any {
	t.Helper()
	if len(bytes.TrimSpace(body)) == 0 {
		return map[string]any{}
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode captured native request body %s: %v", body, err)
	}
	return payload
}

func startProxyWithPayloadRules(t *testing.T, binary, upstream string) string {
	t.Helper()
	root := t.TempDir()
	base, stop := startProxyInRoot(t, binary, upstream, root, nil, "")
	stop()
	pluginArtifact, err := os.ReadFile(strings.TrimSpace(os.Getenv("CPA_LIVE_PLUGIN_PATH")))
	if err != nil {
		t.Fatalf("read matched native Copilot plugin: %v", err)
	}
	ext := ".so"
	if runtime.GOOS == "darwin" {
		ext = ".dylib"
	}
	if runtime.GOOS == "windows" {
		ext = ".dll"
	}
	pluginDestination := filepath.Join(root, "plugins", runtime.GOOS, runtime.GOARCH, "cliproxyapi-copilot"+ext)
	if err := os.WriteFile(pluginDestination, pluginArtifact, 0600); err != nil {
		t.Fatalf("install matched native Copilot plugin: %v", err)
	}
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
	config["payload"] = map[string]any{
		"override": []any{
			map[string]any{
				"models": []any{
					map[string]any{"name": "bridge-*", "protocol": "openai-response"},
					map[string]any{"name": "gpt-6-luna", "protocol": "openai-response"},
				},
				"params": map[string]any{"service_tier": "priority"},
			},
			map[string]any{
				"models": []any{
					map[string]any{"name": "bridge-*", "protocol": "openai-response", "from-protocol": "claude"},
					map[string]any{"name": "gpt-6-luna", "protocol": "openai-response", "from-protocol": "claude"},
				},
				"params": map[string]any{"max_output_tokens": 17},
			},
			map[string]any{
				"models": []any{
					map[string]any{"name": "bridge-*", "protocol": "openai-response", "from-protocol": "responses"},
					map[string]any{"name": "gpt-6-luna", "protocol": "openai-response", "from-protocol": "responses"},
				},
				"params": map[string]any{"max_output_tokens": 23},
			},
		},
		"filter": []any{
			map[string]any{
				"models": []any{
					map[string]any{"name": "bridge-*", "protocol": "openai-response", "from-protocol": "claude"},
					map[string]any{"name": "gpt-6-luna", "protocol": "openai-response", "from-protocol": "claude"},
				},
				"params": []string{"prompt_cache_key"},
			},
		},
	}
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
			t.Fatalf("configured native host did not load bridge-responses: %s", log)
		case <-ticker.C:
			request, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
			request.Header.Set("Authorization", "Bearer fixture-client-key")
			response, err := client.Do(request)
			if err != nil {
				continue
			}
			body, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err == nil && response.StatusCode == http.StatusOK && bytes.Contains(body, []byte("bridge-responses")) {
				return base
			}
		}
	}
}

type nativePayloadRequest struct {
	method string
	path   string
	body   []byte
}

type nativePayloadCapture struct {
	fixture *fixture

	mu               sync.Mutex
	requests         []nativePayloadRequest
	responses401Sent bool
}

func (c *nativePayloadCapture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read synthetic request", http.StatusBadRequest)
		return
	}
	request := nativePayloadRequest{method: r.Method, path: r.URL.Path, body: bytes.Clone(body)}
	c.mu.Lock()
	c.requests = append(c.requests, request)
	if r.Method == http.MethodPost && r.URL.Path == "/responses" && !c.responses401Sent {
		c.responses401Sent = true
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"synthetic first-request token refresh"}}`)
		return
	}
	c.mu.Unlock()
	r.Body = io.NopCloser(bytes.NewReader(body))
	c.fixture.ServeHTTP(w, r)
}

func (c *nativePayloadCapture) snapshot(path string) []nativePayloadRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	requests := make([]nativePayloadRequest, 0, len(c.requests))
	for _, request := range c.requests {
		if path == "" || request.path == path {
			requests = append(requests, nativePayloadRequest{method: request.method, path: request.path, body: bytes.Clone(request.body)})
		}
	}
	return requests
}

func (c *nativePayloadCapture) paths() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	paths := make([]string, 0, len(c.requests))
	for _, request := range c.requests {
		paths = append(paths, request.path)
	}
	return paths
}

var _ http.Handler = (*nativePayloadCapture)(nil)
