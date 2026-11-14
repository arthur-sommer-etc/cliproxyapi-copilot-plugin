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
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"gopkg.in/yaml.v3"
)

func TestNativeHostResponseInterrupt(t *testing.T) {
	binary := os.Getenv("CPA_BINARY")
	if binary == "" {
		t.Skip("set CPA_BINARY to the prepared CLIProxyAPI v8 server for native host integration")
	}
	runNativeHostedInterruptCases(t, binary)
	t.Run("PluginActiveHTTPStreamAndLateBoundary", func(t *testing.T) {
		state := newNativeFixture(t)
		upstream := httptest.NewServer(state)
		t.Cleanup(upstream.Close)
		base, stop := startNativeInterruptDiagnosticHost(t, binary, upstream.URL, true)
		defer stop()
		connection := dialNativeResponsesWebsocket(t, base)
		recordBody := newNativeInterruptBodyRecorder(t, "interrupt-plugin-wire")
		request := map[string]any{
			"type": "response.create", "model": "gpt-6-luna", "input": []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Wait for the synthetic interrupt"}}}},
			"fixture_cancel": true,
		}
		requestBody, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		recordBody("client-create-active.json", requestBody)
		if err := connection.WriteMessage(websocket.TextMessage, requestBody); err != nil {
			t.Fatal(err)
		}
		created, createdBody := readNativeInterruptEvent(t, connection)
		recordBody("client-response-created.json", createdBody)
		if created["type"] != "response.created" {
			t.Fatalf("active plugin stream event = %+v, want response.created", created)
		}
		createdResponse, ok := created["response"].(map[string]any)
		if !ok || createdResponse["id"] != "resp_cancel" {
			t.Fatalf("active plugin response ID = %v, want resp_cancel", created["response"])
		}
		beforeInterrupt := upstreamRequestCount(state)
		if beforeInterrupt != 1 {
			t.Fatalf("active plugin stream opened %d upstream inference requests, want 1", beforeInterrupt)
		}
		captured, _ := lastUpstreamRequest(t, state)
		if captured["fixture_cancel"] != true {
			t.Fatalf("active plugin request did not reach the blocking fixture: %+v", captured)
		}
		wrongID := []byte(`{"type":"response.interrupt","response_id":"another-response"}`)
		recordBody("client-interrupt-wrong-id.json", wrongID)
		if err := connection.WriteMessage(websocket.TextMessage, wrongID); err != nil {
			t.Fatal(err)
		}
		wrongEvent, wrongBody := readNativeInterruptEvent(t, connection)
		recordBody("client-response-wrong-id.json", wrongBody)
		if wrongEvent["type"] != "error" || wrongEvent["status"] != float64(http.StatusBadRequest) {
			t.Fatalf("mismatched interrupt did not reject without canceling the active stream: %s", wrongBody)
		}
		interrupt := []byte(`{"type":"response.interrupt","response_id":"resp_cancel","mode":"discard_partial_items","extension":{"trace":"keep"}}`)
		recordBody("client-interrupt-active.json", interrupt)
		if err := connection.WriteMessage(websocket.TextMessage, interrupt); err != nil {
			t.Fatal(err)
		}
		interrupted, interruptedBody := readNativeInterruptEvent(t, connection)
		recordBody("client-response-interrupted.json", interruptedBody)
		response, ok := interrupted["response"].(map[string]any)
		if interrupted["type"] != "response.incomplete" || !ok || response["id"] != "resp_cancel" {
			t.Fatalf("active plugin interrupt terminal = %+v, want response.incomplete for resp_cancel", interrupted)
		}
		details, _ := response["incomplete_details"].(map[string]any)
		if details["reason"] != "interrupted" {
			t.Fatalf("active plugin interrupt reason = %v, want interrupted", details["reason"])
		}
		select {
		case <-state.canceled:
		case <-time.After(5 * time.Second):
			t.Fatal("active plugin interrupt did not cancel its upstream HTTP request")
		}
		if after := upstreamRequestCount(state); after != beforeInterrupt {
			t.Fatalf("response.interrupt dispatched %d extra upstream inference requests", after-beforeInterrupt)
		}
		if err := connection.WriteMessage(websocket.TextMessage, interrupt); err != nil {
			t.Fatal(err)
		}
		recordBody("client-interrupt-duplicate.json", interrupt)
		nextRequest := map[string]any{"type": "response.create", "model": "gpt-6-luna", "input": []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Continue on the same socket"}}}}}
		nextRequestBody, err := json.Marshal(nextRequest)
		if err != nil {
			t.Fatal(err)
		}
		recordBody("client-create-after-interrupt.json", nextRequestBody)
		if err := connection.WriteMessage(websocket.TextMessage, nextRequestBody); err != nil {
			t.Fatal(err)
		}
		nextEvents := readPluginResponsesWebsocket(t, connection)
		nextEventsBody, err := json.Marshal(nextEvents)
		if err != nil {
			t.Fatal(err)
		}
		recordBody("client-response-after-interrupt.json", nextEventsBody)
		completed := nativeCompletedResponse(t, nextEvents)
		if completed["status"] != "completed" || completed["model"] != "gpt-6-luna" {
			t.Fatalf("same-socket follow-up response = %+v", completed)
		}
		if after := upstreamRequestCount(state); after != beforeInterrupt+1 {
			t.Fatalf("same-socket follow-up made %d upstream requests, want exactly one", after-beforeInterrupt)
		}
		completedID, ok := completed["id"].(string)
		if !ok || completedID == "" {
			t.Fatal("completed plugin response did not expose its response ID")
		}
		// A queued normalization error proves the previous forwarder has exited.
		// Receiving response.completed alone can race its deferred lifecycle cleanup.
		barrier := []byte(`{"type":"fixture.lifecycle_barrier"}`)
		recordBody("client-lifecycle-barrier.json", barrier)
		if err := connection.WriteMessage(websocket.TextMessage, barrier); err != nil {
			t.Fatal(err)
		}
		barrierEvent, barrierBody := readNativeInterruptEvent(t, connection)
		recordBody("client-response-lifecycle-barrier.json", barrierBody)
		if barrierEvent["type"] != "error" {
			t.Fatalf("lifecycle barrier did not return a normalization error: %s", barrierBody)
		}
		lateInterrupt, err := json.Marshal(map[string]string{"type": "response.interrupt", "response_id": completedID, "mode": "discard_partial_items"})
		if err != nil {
			t.Fatal(err)
		}
		recordBody("client-interrupt-after-completed.json", lateInterrupt)
		if err := connection.WriteMessage(websocket.TextMessage, lateInterrupt); err != nil {
			t.Fatal(err)
		}
		if err := connection.WriteMessage(websocket.TextMessage, barrier); err != nil {
			t.Fatal(err)
		}
		lateEvent, lateEventBody := readNativeInterruptEvent(t, connection)
		recordBody("client-response-after-late-interrupt-barrier.json", lateEventBody)
		lateError, _ := lateEvent["error"].(map[string]any)
		if lateEvent["type"] != "error" || !strings.Contains(stringValue(lateError["message"]), "fixture.lifecycle_barrier") {
			t.Fatalf("known-terminal interrupt emitted an extra event before the queued barrier: %s", lateEventBody)
		}
		unknownID := []byte(`{"type":"response.interrupt","response_id":"never-created"}`)
		if err := connection.WriteMessage(websocket.TextMessage, unknownID); err != nil {
			t.Fatal(err)
		}
		unknownEvent, unknownBody := readNativeInterruptEvent(t, connection)
		recordBody("client-response-unknown-interrupt.json", unknownBody)
		if unknownEvent["type"] != "error" || unknownEvent["status"] != float64(http.StatusBadRequest) {
			t.Fatalf("unknown response ID was accepted: %s", unknownBody)
		}

		if after := upstreamRequestCount(state); after != beforeInterrupt+1 {
			t.Fatalf("late response.interrupt dispatched an upstream inference request: count=%d", after)
		}
	})
	t.Run("ActiveNativeDuplex", func(t *testing.T) {
		requests := make(chan []byte, 4)
		finished := make(chan struct{})
		var finishedOnce sync.Once
		var connections atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			connection, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			connections.Add(1)
			defer finishedOnce.Do(func() { close(finished) })
			defer connection.Close()
			interrupted := false
			for {
				_, request, err := connection.ReadMessage()
				if err != nil {
					return
				}
				requests <- append([]byte(nil), request...)
				var event map[string]any
				if err := json.Unmarshal(request, &event); err != nil {
					return
				}
				if event["type"] == "response.interrupt" {
					interrupted = true
					if err := writeNativeWebsocketJSON(connection, map[string]any{"type": "response.incomplete", "sequence_number": 3, "response": map[string]any{"id": "resp-interrupted", "object": "response", "status": "incomplete", "incomplete_details": map[string]any{"reason": "interrupted"}, "output": []any{}}}); err != nil {
						return
					}
					continue
				}
				if event["type"] != "response.create" {
					return
				}
				responseID := "resp-interrupted"
				if interrupted {
					responseID = "resp-after-interrupt"
				}
				if err := writeNativeWebsocketJSON(connection, map[string]any{"type": "response.created", "sequence_number": 0, "response": map[string]any{"id": responseID, "object": "response", "status": "in_progress", "model": "fixture-summary", "output": []any{}}}); err != nil {
					return
				}
				if responseID == "resp-interrupted" {
					continue
				}
				output := map[string]any{"type": "message", "id": "msg-after-interrupt", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "continued after interrupt"}}}
				if err := writeNativeWebsocketJSON(connection, map[string]any{"type": "response.output_item.done", "sequence_number": 3, "output_index": 0, "response_id": responseID, "item": output}); err != nil {
					return
				}
				if err := writeNativeWebsocketJSON(connection, map[string]any{"type": "response.completed", "sequence_number": 4, "response": map[string]any{"id": responseID, "object": "response", "status": "completed", "model": "fixture-summary", "output": []any{output}, "usage": map[string]any{"input_tokens": 1, "output_tokens": 2, "total_tokens": 3}}}); err != nil {
					return
				}
			}
		}))
		t.Cleanup(upstream.Close)
		base, stop := startNativeInterruptDiagnosticHost(t, binary, upstream.URL, false)
		defer stop()
		connection := dialNativeResponsesWebsocket(t, base)
		recordBody := newNativeInterruptBodyRecorder(t, "interrupt-codex-wire")
		firstCreate := map[string]any{"type": "response.create", "model": "fixture-summary", "input": []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Synthetic interrupt diagnosis."}}}}}
		firstCreateBody, err := json.Marshal(firstCreate)
		if err != nil {
			t.Fatal(err)
		}
		recordBody("client-create-active.json", firstCreateBody)
		if err := connection.WriteMessage(websocket.TextMessage, firstCreateBody); err != nil {
			t.Fatal(err)
		}
		first := receiveNativeWebsocketRequest(t, requests)
		recordBody("upstream-create-active.json", first)
		var firstEvent map[string]any
		if err := json.Unmarshal(first, &firstEvent); err != nil || firstEvent["type"] != "response.create" {
			t.Fatalf("upstream initial frame was not response.create: %s", first)
		}
		created, createdBody := readNativeInterruptEvent(t, connection)
		recordBody("client-response-created.json", createdBody)
		if created["type"] != "response.created" {
			t.Fatalf("initial event = %+v, want response.created", created)
		}
		interrupt := []byte(`{"type":"response.interrupt","response_id":"resp-interrupted","mode":"discard_partial_items","extension":{"keep":"unchanged"}}`)
		recordBody("client-interrupt-active.json", interrupt)
		if err := connection.WriteMessage(websocket.TextMessage, interrupt); err != nil {
			t.Fatal(err)
		}
		forwarded := receiveNativeWebsocketRequest(t, requests)
		recordBody("upstream-interrupt-active.json", forwarded)
		if !bytes.Equal(forwarded, interrupt) {
			t.Fatalf("native upstream interrupt changed: got %s, want %s", forwarded, interrupt)
		}
		interrupted, interruptedBody := readNativeInterruptEvent(t, connection)
		recordBody("client-response-interrupted.json", interruptedBody)
		response, ok := interrupted["response"].(map[string]any)
		details, _ := response["incomplete_details"].(map[string]any)
		if interrupted["type"] != "response.incomplete" || !ok || response["id"] != "resp-interrupted" || details["reason"] != "interrupted" {
			t.Fatalf("native Codex interrupt event = %+v, want interrupted response resp-interrupted", interrupted)
		}
		nextCreate := map[string]any{"type": "response.create", "model": "fixture-summary", "previous_response_id": "resp-interrupted", "input": []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Continue after interrupt."}}}}}
		nextCreateBody, err := json.Marshal(nextCreate)
		if err != nil {
			t.Fatal(err)
		}
		recordBody("client-create-after-interrupt.json", nextCreateBody)
		if err := connection.WriteMessage(websocket.TextMessage, nextCreateBody); err != nil {
			t.Fatal(err)
		}
		upstreamNext := receiveNativeWebsocketRequest(t, requests)
		recordBody("upstream-create-after-interrupt.json", upstreamNext)
		var upstreamNextEvent map[string]any
		if err := json.Unmarshal(upstreamNext, &upstreamNextEvent); err != nil || upstreamNextEvent["type"] != "response.create" {
			t.Fatalf("native upstream continuation frame = %s, want response.create", upstreamNext)
		}
		if upstreamNextEvent["previous_response_id"] != nextCreate["previous_response_id"] {
			t.Fatalf("native continuation lost response identity: %s", upstreamNext)
		}
		inputBody, err := json.Marshal(upstreamNextEvent["input"])
		if err != nil {
			t.Fatal(err)
		}
		wantInputBody, err := json.Marshal(nextCreate["input"])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(inputBody, wantInputBody) {
			t.Fatalf("native continuation changed input: %s", upstreamNext)
		}
		nextEvents := readNativeWebsocketResponse(t, connection)
		nextEventsBody, err := json.Marshal(nextEvents)
		if err != nil {
			t.Fatal(err)
		}
		recordBody("client-response-after-interrupt.json", nextEventsBody)
		completed := nativeCompletedResponse(t, nextEvents)
		if completed["id"] != "resp-after-interrupt" || completed["status"] != "completed" {
			t.Fatalf("native Codex continuation response = %+v", completed)
		}
		if got := connections.Load(); got != 1 {
			t.Fatalf("native Codex interrupt/replay used %d upstream WebSocket connections, want 1", got)
		}
		if err := connection.Close(); err != nil {
			t.Fatal(err)
		}
		stop()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Fatal("synthetic upstream reader did not terminate")
		}
	})
}

func readNativeInterruptEvent(t *testing.T, connection *websocket.Conn) (map[string]any, []byte) {
	t.Helper()
	if deadline, ok := t.Deadline(); ok {
		if err := connection.SetReadDeadline(deadline.Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	_, response, err := connection.ReadMessage()
	if err != nil {
		t.Fatalf("read Responses WebSocket event: %v", err)
	}
	var event map[string]any
	if err := json.Unmarshal(response, &event); err != nil {
		t.Fatalf("decode Responses WebSocket event %s: %v", response, err)
	}
	return event, response
}

func newNativeInterruptBodyRecorder(t *testing.T, prefix string) func(string, []byte) {
	t.Helper()
	directory := liveDebugArtifactDirectory(t, prefix)
	return func(name string, body []byte) {
		if directory == "" {
			return
		}
		if err := os.WriteFile(filepath.Join(directory, name), body, 0600); err != nil {
			t.Errorf("could not retain synthetic interrupt wire body %q: %v", name, err)
			return
		}
		t.Logf("retained synthetic interrupt wire body %q: %s", name, directory)
	}
}

func startNativeInterruptDiagnosticHost(t *testing.T, binary, upstream string, plugin bool) (string, func()) {
	t.Helper()
	root := t.TempDir()
	debugDirectory := liveDebugArtifactDirectory(t, "interrupt-host")
	if debugDirectory == "" {
		debugDirectory = root
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if plugin {
		pluginRoot := strings.TrimSpace(os.Getenv("CPA_LIVE_EXISTING_PLUGIN_DIR"))
		if pluginRoot != "" {
			if err := validateLiveExistingPluginDirectory(pluginRoot); err != nil {
				t.Fatalf("existing interrupt plugin directory is invalid: %v", err)
			}
			if selected := strings.TrimSpace(os.Getenv("CPA_LIVE_PLUGIN_PATH")); selected != "" && filepath.Clean(selected) != filepath.Join(pluginRoot, "cliproxyapi-copilot"+livePluginExtension()) {
				t.Fatal("declared interrupt plugin path does not match the artifact loaded in place")
			}
		} else {
			pluginRoot = filepath.Join(root, "plugins")
			pluginDirectory := filepath.Join(pluginRoot, runtime.GOOS, runtime.GOARCH)
			if err := os.MkdirAll(pluginDirectory, 0700); err != nil {
				t.Fatal(err)
			}
			extension := map[string]string{"darwin": ".dylib", "windows": ".dll"}[runtime.GOOS]
			if extension == "" {
				extension = ".so"
			}
			artifact, err := os.ReadFile(filepath.Join("..", "build", "plugins", runtime.GOOS, runtime.GOARCH, "cliproxyapi-copilot"+extension))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(pluginDirectory, "cliproxyapi-copilot"+extension), artifact, 0600); err != nil {
				t.Fatal(err)
			}
		}
		body, authDirectory := nativeRuntimeConfig(t, root, port, upstream, "")
		if err := os.MkdirAll(authDirectory, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(authDirectory, "fixture.json"), nativeAuthFixtureJSON(t), 0600); err != nil {
			t.Fatal(err)
		}
		if err := yaml.Unmarshal(body, &config); err != nil {
			t.Fatal(err)
		}
		nativeMap(t, config["plugins"])["dir"] = pluginRoot
	} else {
		config = map[string]any{"config-version": 8, "server": map[string]any{"host": "127.0.0.1", "port": port}, "management": map[string]any{"disable-control-panel": true}, "access": map[string]any{"api-keys": []string{"fixture-client-key"}}, "oauth": map[string]any{"auth-dir": filepath.Join(root, "auths")}, "codex-api-key": []any{map[string]any{"api-key": "fixture-codex-upstream", "base-url": upstream, "websockets": true, "models": []any{map[string]any{"name": "fixture-summary", "alias": "fixture-summary"}}}}}
		config["upstream"] = map[string]any{"codex": map[string]any{"response-steering": true, "stream-bootstrap-buffering": false}}
	}
	config["observability"] = map[string]any{"logs": map[string]any{"debug": true, "logging-to-file": true, "request-log": true, "logs-max-total-size-mb": 0, "error-logs-max-files": 0}}
	body, err := yaml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.OpenFile(filepath.Join(debugDirectory, "host.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	command := exec.CommandContext(ctx, binary, "--config", configPath, "--local-model")
	command.Env = append(filteredChildEnvironment(os.Environ()), "WRITABLE_PATH="+debugDirectory)
	command.Dir = root
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		cancel()
		_ = logFile.Close()
		t.Fatal(err)
	}
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			_ = command.Process.Signal(os.Interrupt)
			exited := make(chan error, 1)
			go func() { exited <- command.Wait() }()
			timer := time.NewTimer(5 * time.Second)
			select {
			case <-exited:
			case <-timer.C:
				cancel()
				<-exited
			}
			timer.Stop()
			cancel()
			_ = logFile.Close()
			if err := scrubLiveHostLogs(debugDirectory); err != nil {
				t.Error("could not sanitize interrupt diagnostic host logs")
			}
			t.Logf("retained synthetic interrupt DEBUG/request logs: %s", debugDirectory)
		})
	}
	t.Cleanup(stop)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 3 * time.Second}
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-timer.C:
			t.Fatal("synthetic interrupt host did not expose models; retained host logs available")
		case <-ticker.C:
			request, err := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer fixture-client-key")
			response, err := client.Do(request)
			if err == nil {
				body, err := io.ReadAll(response.Body)
				_ = response.Body.Close()
				startupLog, startupErr := os.ReadFile(filepath.Join(debugDirectory, "logs", "main.log"))
				if err == nil && startupErr == nil && strings.Contains(string(startupLog), "file watcher started") && response.StatusCode == http.StatusOK && (plugin && strings.Contains(string(body), "gpt-6-luna") || !plugin && strings.Contains(string(body), "fixture-summary")) {
					return base, stop
				}
			}
		}
	}
}
