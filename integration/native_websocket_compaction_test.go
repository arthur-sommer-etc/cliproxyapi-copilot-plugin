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
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestNativeHostResponsesWebsocketCompactionReplay(t *testing.T) {
	binary := os.Getenv("CPA_BINARY")
	if binary == "" {
		t.Skip("set CPA_BINARY to a built CLIProxyAPI v8 server for native host integration")
	}
	upstream := &codexWebsocketFixture{requests: make(chan []byte, 2)}
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	base, stop := startNativeCodexWebsocketHost(t, binary, server.URL)
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	header := http.Header{"Authorization": []string{"Bearer fixture-client-key"}}
	conn, response, err := websocket.DefaultDialer.DialContext(ctx, "ws"+strings.TrimPrefix(base, "http")+"/v1/responses", header)
	if err != nil {
		if response != nil {
			t.Fatalf("connect native Responses WebSocket: status=%d", response.StatusCode)
		}
		t.Fatalf("connect native Responses WebSocket: %v", err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(map[string]any{"type": "response.create", "model": "fixture-summary", "input": nativeCompactionInput("Preserve the native WebSocket context.", "trigger-first")}); err != nil {
		t.Fatalf("send native summary request: %v", err)
	}
	firstEvents := readNativeWebsocketResponse(t, conn)
	firstRequest := receiveNativeWebsocketRequest(t, upstream.requests)
	assertNativeWebsocketSummaryRequest(t, firstRequest, "Preserve the native WebSocket context.", "trigger-first")
	capsule := assertNativeWebsocketCompactionResponse(t, firstEvents, "native WebSocket summary exact")
	capsuleItem, err := json.Marshal(map[string]string{"type": "compaction", "encrypted_content": capsule})
	if err != nil {
		t.Fatal(err)
	}
	var capsuleValue map[string]any
	if err := json.Unmarshal(capsuleItem, &capsuleValue); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(map[string]any{"type": "response.create", "model": "fixture-summary", "input": []any{
		map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Continue the native WebSocket task."}}},
		capsuleValue,
		map[string]any{"type": "compaction_trigger", "id": "trigger-next", "opaque": map[string]any{"keep": true}},
	}}); err != nil {
		t.Fatalf("send native capsule replay: %v", err)
	}
	secondRequest := receiveNativeWebsocketRequest(t, upstream.requests)
	assertNativeWebsocketReplayRequest(t, secondRequest, capsule, "Continue the native WebSocket task.", "trigger-next")
	secondEvents := readNativeWebsocketResponse(t, conn)
	assertNativeWebsocketCompactionResponse(t, secondEvents, "native WebSocket summary exact")
	if connections := upstream.connectionCount(); connections != 1 {
		t.Fatalf("native WebSocket replay opened %d upstream connections, want one", connections)
	}
}

type codexWebsocketFixture struct {
	mu          sync.Mutex
	requests    chan []byte
	connections int
}

func (f *codexWebsocketFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/responses" || r.Header.Get("Authorization") != "Bearer fixture-codex-upstream" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	f.connections++
	f.mu.Unlock()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	for turn := 1; ; turn++ {
		_, request, err := conn.ReadMessage()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.requests <- append([]byte(nil), request...)
		f.mu.Unlock()
		responseID := fmt.Sprintf("native-summary-%d", turn)
		if err := writeNativeWebsocketJSON(conn, map[string]any{"type": "response.created", "sequence_number": 0, "response": map[string]any{"id": responseID, "object": "response", "status": "in_progress", "model": "fixture-summary", "output": []any{}}}); err != nil {
			return
		}
		if err := writeNativeWebsocketJSON(conn, map[string]any{"type": "response.output_item.done", "sequence_number": 3, "output_index": 0, "response_id": responseID, "item": map[string]any{"type": "message", "id": "summary-item", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "native WebSocket summary exact"}}}}); err != nil {
			return
		}
		if err := writeNativeWebsocketJSON(conn, map[string]any{"type": "response.completed", "sequence_number": 4, "response": map[string]any{"id": responseID, "object": "response", "status": "completed", "model": "fixture-summary", "output": []any{}, "usage": map[string]any{"input_tokens": 17, "output_tokens": 9, "total_tokens": 26}}}); err != nil {
			return
		}
	}
}

func startNativeCodexWebsocketHost(t *testing.T, binary, upstream string) (string, func()) {
	t.Helper()
	root := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	config := fmt.Sprintf("config-version: 8\nserver:\n  host: 127.0.0.1\n  port: %d\nmanagement:\n  disable-control-panel: true\naccess:\n  api-keys: [fixture-client-key]\ncodex-api-key:\n  - api-key: fixture-codex-upstream\n    base-url: %q\n    websockets: true\n    models:\n      - name: fixture-summary\n        alias: fixture-summary\n        use-v1-compaction: true\n", port, upstream)
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.Create(filepath.Join(root, "host.log"))
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
			t.Fatal("native Codex WebSocket host did not expose its synthetic model")
		case <-ticker.C:
			request, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
			request.Header.Set("Authorization", "Bearer fixture-client-key")
			response, err := http.DefaultClient.Do(request)
			if err == nil {
				body, _ := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK && strings.Contains(string(body), "fixture-summary") {
					return base, stop
				}
			}
		}
	}
}

func nativeCompactionInput(text, triggerID string) []any {
	return []any{
		map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": text}}},
		map[string]any{"type": "compaction_trigger", "id": triggerID, "opaque": map[string]any{"keep": true}},
	}
}

func writeNativeWebsocketJSON(conn *websocket.Conn, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, body)
}

func readNativeWebsocketResponse(t *testing.T, conn *websocket.Conn) []map[string]any {
	t.Helper()
	var events []map[string]any
	for {
		_, body, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read native WebSocket response: %v", err)
		}
		var event map[string]any
		if err := json.Unmarshal(body, &event); err != nil {
			t.Fatalf("decode native WebSocket event: %v", err)
		}
		events = append(events, event)
		if event["type"] == "response.completed" {
			return events
		}
	}
}

func receiveNativeWebsocketRequest(t *testing.T, requests <-chan []byte) []byte {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-time.After(10 * time.Second):
		t.Fatal("mock Codex WebSocket received no create request")
		return nil
	}
}

func (f *codexWebsocketFixture) connectionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connections
}

func assertNativeWebsocketSummaryRequest(t *testing.T, body []byte, original, triggerID string) {
	t.Helper()
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatalf("decode upstream native summary request: %v", err)
	}
	if request["type"] != "response.create" {
		t.Fatalf("upstream native summary request type = %v", request["type"])
	}
	input := jsonObjects(request["input"])
	if len(input) != 3 || !containsJSONScalar(input[0], original) || input[1]["type"] != "message" || input[1]["role"] != "user" || input[2]["type"] != "compaction_trigger" || input[2]["id"] != triggerID {
		t.Fatalf("native summary request lost original input, instruction, or trigger: %s", body)
	}
	if !strings.Contains(string(body), "Summarize the conversation so far") || !hasKeepMetadata(input[2]) {
		t.Fatalf("native summary request lost its instruction or trigger metadata: %s", body)
	}
}

func assertNativeWebsocketCompactionResponse(t *testing.T, events []map[string]any, summary string) string {
	t.Helper()
	var completed map[string]any
	var addedCapsule map[string]any
	var doneCapsule map[string]any
	var outputItemDone int
	for _, event := range events {
		switch event["type"] {
		case "response.output_item.added":
			item, _ := event["item"].(map[string]any)
			if item["type"] == "compaction" {
				addedCapsule = item
			}
		case "response.output_item.done":
			outputItemDone++
			item, _ := event["item"].(map[string]any)
			if item["type"] == "compaction" {
				doneCapsule = item
			}
		case "response.completed":
			completed, _ = event["response"].(map[string]any)
		}
	}
	if completed == nil {
		t.Fatalf("native WebSocket response has no completed event: %+v", events)
	}
	output := jsonObjects(completed["output"])
	var capsules []map[string]any
	var summaryFound bool
	for _, item := range output {
		if item["type"] == "compaction" {
			capsules = append(capsules, item)
		}
		if containsJSONScalar(item, summary) {
			summaryFound = true
		}
	}
	if len(capsules) != 1 || !summaryFound || outputItemDone == 0 {
		t.Fatalf("native WebSocket response lost summary or did not emit one capsule: %+v", completed)
	}
	capsule := stringValue(capsules[0]["encrypted_content"])
	if !strings.HasPrefix(capsule, "cpa-responses-v1-compaction-v1.") || addedCapsule == nil || doneCapsule == nil || addedCapsule["encrypted_content"] != capsule || doneCapsule["encrypted_content"] != capsule {
		t.Fatalf("native WebSocket compaction events do not match the completed capsule")
	}
	usage, _ := completed["usage"].(map[string]any)
	if usage["total_tokens"] != float64(26) {
		t.Fatalf("native WebSocket completion lost terminal usage: %+v", usage)
	}
	return capsule
}

func assertNativeWebsocketReplayRequest(t *testing.T, body []byte, capsule, continuation, triggerID string) {
	t.Helper()
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatalf("decode upstream native replay request: %v", err)
	}
	input, _ := json.Marshal(request["input"])
	items := jsonObjects(request["input"])
	if bytes.Contains(input, []byte(capsule)) || bytes.Contains(input, []byte("cpa-responses-v1-compaction-v1.")) {
		t.Fatalf("native replay leaked its opaque capsule upstream: %s", input)
	}
	var continuationFound, triggerFound bool
	summaryFound := strings.Contains(string(input), "native WebSocket summary exact")
	instructionFound := strings.Contains(string(input), "Summarize the conversation so far")
	for _, item := range items {
		if containsJSONScalar(item, continuation) {
			continuationFound = true
		}
		if item["type"] == "compaction_trigger" && item["id"] == triggerID && hasKeepMetadata(item) {
			triggerFound = true
		}
	}
	if !summaryFound || !instructionFound || !continuationFound || !triggerFound {
		t.Fatalf("native replay lost summary context, instruction, continuation, or trigger: %s", input)
	}
}

func hasKeepMetadata(item map[string]any) bool {
	opaque, _ := item["opaque"].(map[string]any)
	keep, _ := opaque["keep"].(bool)
	return keep
}
