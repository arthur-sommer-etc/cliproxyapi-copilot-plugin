package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/sse"
	"github.com/gorilla/websocket"
)

func runNativeHostedInterruptCases(t *testing.T, binary string) {
	t.Helper()
	t.Run("HostedTerminalIdentityAndNextTurnInterrupt", func(t *testing.T) {
		fixture := newNativeHostedInterruptFixture(t, false)
		upstream := httptest.NewServer(fixture)
		t.Cleanup(upstream.Close)
		base, stop := startNativeInterruptDiagnosticHost(t, binary, upstream.URL, true)
		defer stop()
		connection := dialNativeResponsesWebsocket(t, base)
		if err := connection.WriteJSON(fixture.websocketRequest(t)); err != nil {
			t.Fatal(err)
		}
		events := readPluginResponsesWebsocket(t, connection)
		completed := nativeCompletedResponse(t, events)
		terminal, ok := fixture.events[len(fixture.events)-1]["response"].(map[string]any)
		if !ok || !reflect.DeepEqual(completed, terminal) {
			t.Fatal("matched host changed the captured authoritative terminal")
		}
		upstreamCreated, _ := fixture.events[0]["response"].(map[string]any)
		upstreamID, terminalID := stringValue(upstreamCreated["id"]), stringValue(terminal["id"])
		if upstreamID == "" || terminalID == "" || upstreamID == terminalID {
			t.Fatal("captured fixture does not exercise a changing response identity")
		}
		for _, event := range events {
			if response, ok := event["response"].(map[string]any); ok && response["id"] != terminalID {
				t.Fatal("an upstream response identity escaped the hosted lifecycle barrier")
			}
		}
		if err := connection.WriteJSON(map[string]any{"type": "response.create", "model": "gpt-6-luna", "input": []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Keep the next synthetic turn active"}}}}, "tools": []any{}, "tool_choice": "auto", "fixture_cancel": true}); err != nil {
			t.Fatal(err)
		}
		created, _ := readNativeInterruptEvent(t, connection)
		current, ok := created["response"].(map[string]any)
		if created["type"] != "response.created" || !ok || current["id"] != "resp_cancel" {
			t.Fatal("next turn did not become active after buffered completion")
		}
		if err := connection.WriteJSON(map[string]any{"type": "response.interrupt", "response_id": terminalID}); err != nil {
			t.Fatal(err)
		}
		if err := connection.WriteJSON(map[string]any{"type": "response.interrupt", "response_id": upstreamID}); err != nil {
			t.Fatal(err)
		}
		rejected, _ := readNativeInterruptEvent(t, connection)
		if rejected["type"] != "error" || rejected["status"] != float64(http.StatusBadRequest) {
			t.Fatal("late terminal ID disturbed the next turn or unseen upstream ID was accepted")
		}
		select {
		case <-fixture.fixture.canceled:
			t.Fatal("late or unseen response identity canceled the active next turn")
		default:
		}
		if err := connection.WriteJSON(map[string]any{"type": "response.interrupt", "response_id": current["id"]}); err != nil {
			t.Fatal(err)
		}
		interrupted, _ := readNativeInterruptEvent(t, connection)
		response, ok := interrupted["response"].(map[string]any)
		if interrupted["type"] != "response.incomplete" || !ok || response["id"] != current["id"] {
			t.Fatal("the actual current-turn interrupt was lost after late-ID handling")
		}
		details, _ := response["incomplete_details"].(map[string]any)
		if details["reason"] != "interrupted" {
			t.Fatal("current-turn interrupt did not retain its termination reason")
		}
		waitNativeHostedSignal(t, fixture.fixture.canceled, "current-turn upstream cancellation")
		if count := upstreamRequestCount(fixture.fixture); count != 2 {
			t.Fatalf("interrupt controls caused extra inference: count=%d", count)
		}
	})

	for _, mode := range []string{"WebsocketDisconnect", "HTTPClientCancel"} {
		t.Run("HostedBuffered"+mode, func(t *testing.T) {
			fixture := newNativeHostedInterruptFixture(t, true)
			upstream := httptest.NewServer(fixture)
			t.Cleanup(upstream.Close)
			base, stop := startNativeInterruptDiagnosticHost(t, binary, upstream.URL, true)
			defer stop()
			if mode == "WebsocketDisconnect" {
				connection := dialNativeResponsesWebsocket(t, base)
				if err := connection.WriteJSON(fixture.websocketRequest(t)); err != nil {
					t.Fatal(err)
				}
				waitNativeHostedSignal(t, fixture.prefixSent, "buffered WebSocket upstream prefix")
				created, _ := fixture.events[0]["response"].(map[string]any)
				if err := connection.WriteJSON(map[string]any{"type": "response.interrupt", "response_id": created["id"]}); err != nil {
					t.Fatal(err)
				}
				event, _ := readNativeInterruptEvent(t, connection)
				if event["type"] != "error" || event["status"] != float64(http.StatusBadRequest) {
					t.Fatal("buffered response emitted a created/terminal event or accepted an unseen ID")
				}
				if err := connection.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")); err != nil {
					t.Fatal(err)
				}
				for {
					_, body, err := connection.ReadMessage()
					if err != nil {
						break
					}
					var event map[string]any
					if json.Unmarshal(body, &event) != nil || event["type"] != "error" {
						t.Fatal("disconnect leaked a buffered response or invented a terminal identity")
					}
				}
			} else {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				body, err := json.Marshal(fixture.request)
				if err != nil {
					t.Fatal(err)
				}
				request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/responses", bytes.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Authorization", "Bearer fixture-client-key")
				request.Header.Set("Content-Type", "application/json")
				type outcome struct {
					body []byte
					err  error
				}
				result := make(chan outcome, 1)
				go func() {
					response, err := http.DefaultClient.Do(request)
					if err != nil {
						result <- outcome{err: err}
						return
					}
					body, err := io.ReadAll(response.Body)
					if errClose := response.Body.Close(); err == nil {
						err = errClose
					}
					result <- outcome{body: body, err: err}
				}()
				select {
				case <-fixture.prefixSent:
				case early := <-result:
					t.Fatalf("HTTP request ended before reaching the buffered prefix: %v", early.err)
				case <-time.After(5 * time.Second):
					t.Fatal("HTTP request did not reach the buffered upstream prefix")
				}
				cancel()
				var observed outcome
				select {
				case observed = <-result:
				case <-time.After(5 * time.Second):
					t.Fatal("canceled HTTP client did not finish reading")
				}
				if !errors.Is(observed.err, context.Canceled) || len(observed.body) != 0 {
					t.Fatal("cancellation leaked buffered events or fabricated a completed response")
				}
			}
			waitNativeHostedSignal(t, fixture.canceled, "buffered upstream cancellation")
			if count := upstreamRequestCount(fixture.fixture); count != 1 {
				t.Fatalf("buffered cancellation dispatched extra inference: count=%d", count)
			}
		})
	}
}

type nativeHostedInterruptFixture struct {
	t          *testing.T
	fixture    *fixture
	request    map[string]any
	events     []map[string]any
	frames     [][]byte
	hold       bool
	prefixSent chan struct{}
	canceled   chan struct{}
}

func newNativeHostedInterruptFixture(t *testing.T, hold bool) *nativeHostedInterruptFixture {
	t.Helper()
	directory := filepath.Join("..", "internal", "translate", "testdata", "native-hosted-tool-stream-captures", "gpt-search-stream-before-repair")
	body, err := readHostedStreamJSON(directory)
	if err != nil {
		t.Fatal(err)
	}
	events, err := liveHostedToolEvents(body)
	if err != nil || len(events) < 2 || events[0]["type"] != "response.created" || events[len(events)-1]["type"] != "response.completed" {
		t.Fatal("portable actual hosted stream lacks its complete lifecycle")
	}
	requestBody, err := os.ReadFile(filepath.Join(directory, "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var request map[string]any
	if err := json.Unmarshal(requestBody, &request); err != nil {
		t.Fatal(err)
	}
	request["fixture_hosted"] = true
	var decoder sse.Decoder
	frames := decoder.Feed(body)
	if len(decoder.Flush()) != 0 || len(frames) != len(events) {
		t.Fatal("portable actual hosted stream framing is incomplete")
	}
	return &nativeHostedInterruptFixture{t: t, fixture: newNativeFixture(t), request: request, events: events, frames: frames, hold: hold, prefixSent: make(chan struct{}), canceled: make(chan struct{})}
}

func (f *nativeHostedInterruptFixture) websocketRequest(t *testing.T) map[string]any {
	t.Helper()
	text, ok := f.request["input"].(string)
	if !ok || text == "" {
		t.Fatal("captured HTTP fixture has no original input text")
	}
	request := maps.Clone(f.request)
	request["type"] = "response.create"
	request["input"] = []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": text}}}}
	return request
}

func waitNativeHostedSignal(t *testing.T, signal <-chan struct{}, stage string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("synthetic hosted fixture did not finish %s", stage)
	}
}

func (f *nativeHostedInterruptFixture) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/responses" {
		f.fixture.ServeHTTP(writer, request)
		return
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		f.t.Error(err)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		f.t.Error(err)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if payload["fixture_hosted"] != true {
		request.Body = io.NopCloser(bytes.NewReader(body))
		f.fixture.ServeHTTP(writer, request)
		return
	}
	if payload["model"] != "gpt-6-luna" || payload["stream"] != true || !reflect.DeepEqual(payload["tools"], f.request["tools"]) {
		f.t.Error("hosted synthetic route or declaration changed before upstream dispatch")
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	f.fixture.mu.Lock()
	f.fixture.requests = append(f.fixture.requests, payload)
	f.fixture.paths = append(f.fixture.paths, request.URL.Path)
	f.fixture.mu.Unlock()
	writer.Header().Set("Content-Type", "text/event-stream")
	frames := f.frames
	if f.hold {
		frames = frames[:len(frames)-1]
	}
	for _, frame := range frames {
		if _, err := writer.Write(frame); err != nil {
			f.t.Error(err)
			return
		}
		writer.(http.Flusher).Flush()
	}
	close(f.prefixSent)
	if f.hold {
		<-request.Context().Done()
		close(f.canceled)
	}
}
