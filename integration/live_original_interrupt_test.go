//go:build !windows

package integration

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const liveOriginalInterruptCellKey = "I-latest-dependencies/codex-interrupt"
const liveOriginalInterruptFixAttempt = "fix-relay-origin"

func liveOriginalInterruptAttemptPhase(attempt string) (string, error) {
	switch attempt {
	case "", "latest-dependencies":
		return "I-latest-dependencies", nil
	case "native-stream-revalidation":
		return "I-native-stream-revalidation", nil
	case liveOriginalInterruptFixAttempt:
		return "I-fix-relay-origin", nil
	default:
		return "", errors.New("unsupported original interrupt attempt")
	}
}

func liveOriginalInterruptCell(key string) bool {
	return key == liveOriginalInterruptCellKey || key == "I-fix-relay-origin/codex-interrupt" || key == "I-native-stream-revalidation/codex-interrupt"
}

func TestLiveOriginalInterruptClaimCell(t *testing.T) {
	valid := []string{liveOriginalInterruptCellKey, "I-fix-relay-origin/codex-interrupt", "I-native-stream-revalidation/codex-interrupt"}
	for _, key := range valid {
		if !liveOriginalInterruptCell(key) {
			t.Errorf("original Codex interrupt claim cell was rejected: %q", key)
		}
	}
	for _, key := range []string{"", "I-latest-dependencies/codex", "I-latest-dependencies/codex-interrupt-extra", "I-fix-relay-origin/codex-interrupt-extra", "I-unknown/codex-interrupt", "L-latest-dependencies/codex-interrupt"} {
		if liveOriginalInterruptCell(key) {
			t.Errorf("unexpected original Codex interrupt claim cell accepted: %q", key)
		}
	}
}

func TestLiveOriginalInterruptAttemptPhase(t *testing.T) {
	tests := []struct {
		attempt string
		phase   string
		wantErr bool
	}{
		{attempt: "", phase: "I-latest-dependencies"},
		{attempt: "latest-dependencies", phase: "I-latest-dependencies"},
		{attempt: liveOriginalInterruptFixAttempt, phase: "I-fix-relay-origin"},
		{attempt: "native-stream-revalidation", phase: "I-native-stream-revalidation"},
		{attempt: "fix-relay-origin-extra", wantErr: true},
		{attempt: "Fix-relay-origin", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.attempt, func(t *testing.T) {
			phase, err := liveOriginalInterruptAttemptPhase(test.attempt)
			if (err != nil) != test.wantErr {
				t.Fatalf("liveOriginalInterruptAttemptPhase(%q) error = %v", test.attempt, err)
			}
			if phase != test.phase {
				t.Fatalf("liveOriginalInterruptAttemptPhase(%q) = %q, want %q", test.attempt, phase, test.phase)
			}
		})
	}
}

type originalInterruptOutcome struct {
	RunDir                        string `json:"run_dir"`
	Initialize                    bool   `json:"initialize"`
	ThreadStart                   bool   `json:"thread_start"`
	ActiveAgentMessageStarted     bool   `json:"active_agent_message_started"`
	InterruptRPC                  bool   `json:"interrupt_rpc"`
	InterruptedTurnCompleted      bool   `json:"interrupted_turn_completed"`
	SameThreadFollowupCompleted   bool   `json:"same_thread_followup_completed"`
	SameThreadFollowupAnswer      bool   `json:"same_thread_followup_answer"`
	OriginalWireResponseInterrupt string `json:"original_wire_response_interrupt"`
	ErrorCode                     string `json:"error_code"`
}

type originalInterruptWireEvent struct {
	Sequence        uint64   `json:"sequence"`
	TimeUTC         string   `json:"time_utc"`
	Kind            string   `json:"kind"`
	RequestID       uint64   `json:"request_id,omitempty"`
	Direction       string   `json:"direction,omitempty"`
	Method          string   `json:"method,omitempty"`
	URLPath         string   `json:"url_path,omitempty"`
	HeaderNames     []string `json:"header_names,omitempty"`
	Status          int      `json:"status,omitempty"`
	Subprotocol     string   `json:"subprotocol,omitempty"`
	Opcode          int      `json:"opcode,omitempty"`
	Control         string   `json:"control,omitempty"`
	ControlCode     int      `json:"control_code,omitempty"`
	Complete        bool     `json:"complete,omitempty"`
	PayloadBase64   string   `json:"payload_base64,omitempty"`
	PayloadEncoding string   `json:"payload_encoding,omitempty"`
	ErrorClass      string   `json:"error_class,omitempty"`
}

type originalInterruptRelayLog struct {
	mu       sync.Mutex
	file     *os.File
	sequence uint64
	err      error
}

func newOriginalInterruptRelayLog(path string) (*originalInterruptRelayLog, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &originalInterruptRelayLog{file: file}, nil
}

func (l *originalInterruptRelayLog) record(event originalInterruptWireEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil || l.file == nil {
		return
	}
	l.sequence++
	event.Sequence = l.sequence
	event.TimeUTC = time.Now().UTC().Format(time.RFC3339Nano)
	encoded, err := json.Marshal(event)
	if err == nil {
		_, err = l.file.Write(append(encoded, '\n'))
	}
	if err == nil {
		err = l.file.Sync()
	}
	if err != nil {
		l.err = errors.New("private original-client ingress log write failed")
	}
}

func (l *originalInterruptRelayLog) failure() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

func (l *originalInterruptRelayLog) close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return l.err
	}
	err := l.file.Close()
	l.file = nil
	if err != nil && l.err == nil {
		l.err = errors.New("private original-client ingress log close failed")
	}
	return l.err
}

type originalInterruptRelay struct {
	origin      *url.URL
	listener    net.Listener
	server      *http.Server
	proxy       *httputil.ReverseProxy
	transport   *http.Transport
	log         *originalInterruptRelayLog
	requestID   atomic.Uint64
	mu          sync.Mutex
	connections map[net.Conn]struct{}
	workers     sync.WaitGroup
	stopOnce    sync.Once
}

type originalInterruptRequestIDKey struct{}

func startOriginalInterruptRelay(t *testing.T, upstreamBase, artifactDir string) (*originalInterruptRelay, string, func() error) {
	t.Helper()
	base, err := url.Parse(upstreamBase)
	if err != nil || base.Scheme != "http" || !isLoopbackHost(base.Hostname()) || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Path != "/v1" {
		t.Fatal("original-client relay requires an owned loopback CPA /v1 origin")
	}
	origin := *base
	origin.Path = ""
	origin.RawPath = ""
	log, err := newOriginalInterruptRelayLog(filepath.Join(artifactDir, "original-client-ingress.jsonl"))
	if err != nil {
		t.Fatal("could not create private original-client request/response log")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = log.close()
		t.Fatal("could not start loopback original-client ingress relay")
	}
	relay := &originalInterruptRelay{origin: &origin, listener: listener, log: log, connections: make(map[net.Conn]struct{})}
	target := origin
	relay.proxy = httputil.NewSingleHostReverseProxy(&target)
	relay.transport = &http.Transport{Proxy: nil}
	relay.proxy.Transport = relay.transport
	previousDirector := relay.proxy.Director
	relay.proxy.Director = func(request *http.Request) {
		previousDirector(request)
		request.Host = origin.Host
	}
	relay.proxy.FlushInterval = -1
	relay.proxy.ModifyResponse = func(response *http.Response) error {
		var requestID uint64
		if response.Request != nil {
			requestID, _ = response.Request.Context().Value(originalInterruptRequestIDKey{}).(uint64)
		}
		relay.log.record(originalInterruptWireEvent{Kind: "http_response", RequestID: requestID, Direction: "cpa_to_original_client", Status: response.StatusCode, HeaderNames: sortedHeaderNames(response.Header)})
		response.Body = newOriginalInterruptBodyCapture(response.Body, relay.log, requestID, "http_response_body", "cpa_to_original_client")
		return nil
	}
	relay.proxy.ErrorHandler = func(writer http.ResponseWriter, request *http.Request, proxyErr error) {
		requestID, _ := request.Context().Value(originalInterruptRequestIDKey{}).(uint64)
		relay.log.record(originalInterruptWireEvent{Kind: "http_proxy_error", RequestID: requestID, Direction: "relay", ErrorClass: fmt.Sprintf("%T", proxyErr)})
		http.Error(writer, "original-client relay upstream failure", http.StatusBadGateway)
	}
	relay.server = &http.Server{Handler: relay}
	go func() { _ = relay.server.Serve(listener) }()
	relayURL := "http://" + listener.Addr().String() + "/v1"
	stop := func() error {
		var stopErr error
		relay.stopOnce.Do(func() {
			stopErr = relay.server.Close()
			relay.closeConnections()
			relay.workers.Wait()
			relay.transport.CloseIdleConnections()
			if closeErr := relay.log.close(); stopErr == nil {
				stopErr = closeErr
			}
		})
		if errors.Is(stopErr, http.ErrServerClosed) {
			stopErr = nil
		}
		return stopErr
	}
	return relay, relayURL, stop
}

func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "127.0.0.1", "localhost", "::1":
		return true
	default:
		return false
	}
}

func originalInterruptRelayBaseURL(hostRoot string) (string, error) {
	base, err := url.Parse(hostRoot)
	if err != nil || base.Scheme != "http" || base.Host == "" || !isLoopbackHost(base.Hostname()) || base.User != nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" || base.RawFragment != "" || base.Opaque != "" || (base.Path != "" && base.Path != "/") || base.RawPath != "" {
		return "", errors.New("original interrupt relay requires a loopback host root URL")
	}
	base.Path = "/v1"
	return base.String(), nil
}

func TestOriginalInterruptRelayBaseURL(t *testing.T) {
	for _, test := range []struct {
		name string
		root string
		want string
	}{
		{name: "root origin", root: "http://127.0.0.1:43123", want: "http://127.0.0.1:43123/v1"},
		{name: "root origin with slash", root: "http://localhost:43123/", want: "http://localhost:43123/v1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := originalInterruptRelayBaseURL(test.root)
			if err != nil {
				t.Fatalf("originalInterruptRelayBaseURL(%q): %v", test.root, err)
			}
			if got != test.want {
				t.Fatalf("originalInterruptRelayBaseURL(%q) = %q, want %q", test.root, got, test.want)
			}
		})
	}
	for _, root := range []string{
		"https://127.0.0.1:43123",
		"http://example.com:43123",
		"http://127.0.0.1:43123/v1",
		"http://127.0.0.1:43123/other",
		"http://127.0.0.1:43123?token=x",
		"http://127.0.0.1:43123#fragment",
		"http://user@127.0.0.1:43123",
	} {
		if got, err := originalInterruptRelayBaseURL(root); err == nil {
			t.Errorf("originalInterruptRelayBaseURL(%q) = %q, want rejection", root, got)
		}
	}
}

func sortedHeaderNames(header http.Header) []string {
	names := make([]string, 0, len(header))
	for name := range header {
		names = append(names, http.CanonicalHeaderKey(name))
	}
	sort.Strings(names)
	return names
}

func (r *originalInterruptRelay) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	requestID := r.requestID.Add(1)
	r.workers.Add(1)
	defer r.workers.Done()
	if websocket.IsWebSocketUpgrade(request) {
		r.serveWebSocket(writer, request, requestID)
		return
	}
	r.log.record(originalInterruptWireEvent{Kind: "http_request", RequestID: requestID, Direction: "original_client_to_cpa", Method: request.Method, URLPath: request.URL.EscapedPath(), HeaderNames: sortedHeaderNames(request.Header)})
	if request.Body != nil {
		request.Body = newOriginalInterruptBodyCapture(request.Body, r.log, requestID, "http_request_body", "original_client_to_cpa")
	}
	request = request.WithContext(context.WithValue(request.Context(), originalInterruptRequestIDKey{}, requestID))
	r.proxy.ServeHTTP(writer, request)
}

func (r *originalInterruptRelay) serveWebSocket(writer http.ResponseWriter, request *http.Request, requestID uint64) {
	path := request.URL.EscapedPath()
	if request.URL.RawQuery != "" {
		path += "?query_keys=" + strings.Join(queryKeys(request.URL.Query()), ",")
	}
	r.log.record(originalInterruptWireEvent{Kind: "websocket_handshake_request", RequestID: requestID, Direction: "original_client_to_cpa", Method: request.Method, URLPath: path, HeaderNames: sortedHeaderNames(request.Header)})
	target := *r.origin
	target.Scheme = "ws"
	target.Path = request.URL.Path
	target.RawPath = request.URL.RawPath
	target.RawQuery = request.URL.RawQuery
	if request.URL.RawQuery != "" {
		target.RawQuery = request.URL.RawQuery
	}
	header := request.Header.Clone()
	for name := range header {
		if strings.EqualFold(name, "Connection") || strings.EqualFold(name, "Upgrade") || strings.HasPrefix(strings.ToLower(name), "sec-websocket-") {
			header.Del(name)
		}
	}
	dialer := websocket.Dialer{Proxy: func(*http.Request) (*url.URL, error) { return nil, nil }, Subprotocols: websocket.Subprotocols(request), HandshakeTimeout: 0}
	upstream, response, err := dialer.DialContext(request.Context(), target.String(), header)
	if response != nil {
		r.log.record(originalInterruptWireEvent{Kind: "websocket_upstream_handshake", RequestID: requestID, Direction: "cpa_to_relay", Status: response.StatusCode, HeaderNames: sortedHeaderNames(response.Header), Subprotocol: response.Header.Get("Sec-WebSocket-Protocol")})
	}
	if err != nil {
		r.log.record(originalInterruptWireEvent{Kind: "websocket_dial_error", RequestID: requestID, Direction: "relay_to_cpa", ErrorClass: fmt.Sprintf("%T", err)})
		if response != nil {
			copyHeadersWithoutHop(writer.Header(), response.Header)
			writer.WriteHeader(response.StatusCode)
			if response.Body != nil {
				body, readErr := io.ReadAll(response.Body)
				event := originalInterruptWireEvent{Kind: "websocket_handshake_error_body", RequestID: requestID, Direction: "cpa_to_original_client", Status: response.StatusCode, Complete: readErr == nil, PayloadBase64: base64.StdEncoding.EncodeToString(body), PayloadEncoding: "base64"}
				if readErr != nil {
					event.ErrorClass = fmt.Sprintf("%T", readErr)
				}
				r.log.record(event)
				_, _ = writer.Write(body)
				_ = response.Body.Close()
			}
		} else {
			http.Error(writer, "original-client relay websocket upstream failure", http.StatusBadGateway)
		}
		return
	}
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	protocols := []string(nil)
	if protocol := upstream.Subprotocol(); protocol != "" {
		protocols = []string{protocol}
	}
	upgrader := websocket.Upgrader{Subprotocols: protocols, CheckOrigin: func(*http.Request) bool { return true }}
	client, err := upgrader.Upgrade(writer, request, nil)
	if err != nil {
		r.log.record(originalInterruptWireEvent{Kind: "websocket_client_upgrade_error", RequestID: requestID, Direction: "relay", ErrorClass: fmt.Sprintf("%T", err)})
		_ = upstream.Close()
		return
	}
	r.log.record(originalInterruptWireEvent{Kind: "websocket_client_handshake_response", RequestID: requestID, Direction: "cpa_to_original_client", Status: http.StatusSwitchingProtocols, HeaderNames: []string{"Connection", "Sec-Websocket-Accept", "Upgrade"}, Subprotocol: upstream.Subprotocol()})
	r.addConnection(client.UnderlyingConn())
	r.addConnection(upstream.UnderlyingConn())
	defer func() {
		r.removeConnection(client.UnderlyingConn())
		r.removeConnection(upstream.UnderlyingConn())
		_ = client.Close()
		_ = upstream.Close()
		r.log.record(originalInterruptWireEvent{Kind: "websocket_connection_ended", RequestID: requestID, Direction: "relay"})
	}()
	client.SetPingHandler(func(payload string) error {
		r.recordControl(requestID, "original_client_to_cpa", "ping", 0, []byte(payload))
		return upstream.WriteControl(websocket.PingMessage, []byte(payload), time.Time{})
	})
	client.SetPongHandler(func(payload string) error {
		r.recordControl(requestID, "original_client_to_cpa", "pong", 0, []byte(payload))
		return upstream.WriteControl(websocket.PongMessage, []byte(payload), time.Time{})
	})
	client.SetCloseHandler(func(code int, text string) error {
		payload := websocket.FormatCloseMessage(code, text)
		r.recordControl(requestID, "original_client_to_cpa", "close", code, payload)
		_ = upstream.WriteControl(websocket.CloseMessage, payload, time.Time{})
		return &websocket.CloseError{Code: code, Text: text}
	})
	upstream.SetPingHandler(func(payload string) error {
		r.recordControl(requestID, "cpa_to_original_client", "ping", 0, []byte(payload))
		return client.WriteControl(websocket.PingMessage, []byte(payload), time.Time{})
	})
	upstream.SetPongHandler(func(payload string) error {
		r.recordControl(requestID, "cpa_to_original_client", "pong", 0, []byte(payload))
		return client.WriteControl(websocket.PongMessage, []byte(payload), time.Time{})
	})
	upstream.SetCloseHandler(func(code int, text string) error {
		payload := websocket.FormatCloseMessage(code, text)
		r.recordControl(requestID, "cpa_to_original_client", "close", code, payload)
		_ = client.WriteControl(websocket.CloseMessage, payload, time.Time{})
		return &websocket.CloseError{Code: code, Text: text}
	})
	type pumpResult struct {
		err error
	}
	results := make(chan pumpResult, 2)
	pump := func(source, destination *websocket.Conn, direction string) {
		for {
			opcode, payload, readErr := source.ReadMessage()
			if readErr != nil {
				var closeErr *websocket.CloseError
				if errors.As(readErr, &closeErr) {
					r.log.record(originalInterruptWireEvent{Kind: "websocket_read_end", RequestID: requestID, Direction: direction, Control: "close", ControlCode: closeErr.Code, ErrorClass: "close_frame"})
				} else {
					r.log.record(originalInterruptWireEvent{Kind: "websocket_read_end", RequestID: requestID, Direction: direction, ErrorClass: fmt.Sprintf("%T", readErr)})
				}
				results <- pumpResult{err: readErr}
				return
			}
			r.log.record(originalInterruptWireEvent{Kind: "websocket_message", RequestID: requestID, Direction: direction, Opcode: opcode, PayloadBase64: base64.StdEncoding.EncodeToString(payload), PayloadEncoding: "base64"})
			if writeErr := destination.WriteMessage(opcode, payload); writeErr != nil {
				r.log.record(originalInterruptWireEvent{Kind: "websocket_write_error", RequestID: requestID, Direction: direction, ErrorClass: fmt.Sprintf("%T", writeErr)})
				results <- pumpResult{err: writeErr}
				return
			}
		}
	}
	go pump(client, upstream, "original_client_to_cpa")
	go pump(upstream, client, "cpa_to_original_client")
	first := <-results
	_ = first
	_ = client.Close()
	_ = upstream.Close()
	<-results
}

func queryKeys(values url.Values) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func copyHeadersWithoutHop(target, source http.Header) {
	for name, values := range source {
		if strings.EqualFold(name, "Connection") || strings.EqualFold(name, "Upgrade") || strings.EqualFold(name, "Transfer-Encoding") {
			continue
		}
		for _, value := range values {
			target.Add(name, value)
		}
	}
}

func (r *originalInterruptRelay) recordControl(requestID uint64, direction, control string, code int, payload []byte) {
	r.log.record(originalInterruptWireEvent{Kind: "websocket_control", RequestID: requestID, Direction: direction, Control: control, ControlCode: code, PayloadBase64: base64.StdEncoding.EncodeToString(payload), PayloadEncoding: "base64"})
}

func (r *originalInterruptRelay) addConnection(connection net.Conn) {
	r.mu.Lock()
	r.connections[connection] = struct{}{}
	r.mu.Unlock()
}

func (r *originalInterruptRelay) removeConnection(connection net.Conn) {
	r.mu.Lock()
	delete(r.connections, connection)
	r.mu.Unlock()
}

func (r *originalInterruptRelay) closeConnections() {
	r.mu.Lock()
	connections := make([]net.Conn, 0, len(r.connections))
	for connection := range r.connections {
		connections = append(connections, connection)
	}
	r.mu.Unlock()
	for _, connection := range connections {
		_ = connection.Close()
	}
}

type originalInterruptBodyCapture struct {
	underlying io.ReadCloser
	log        *originalInterruptRelayLog
	requestID  uint64
	kind       string
	direction  string
	mu         sync.Mutex
	body       bytes.Buffer
	complete   bool
	closed     bool
}

func newOriginalInterruptBodyCapture(body io.ReadCloser, log *originalInterruptRelayLog, requestID uint64, kind, direction string) *originalInterruptBodyCapture {
	return &originalInterruptBodyCapture{underlying: body, log: log, requestID: requestID, kind: kind, direction: direction}
}

func (c *originalInterruptBodyCapture) Read(p []byte) (int, error) {
	n, err := c.underlying.Read(p)
	c.mu.Lock()
	if n > 0 {
		_, _ = c.body.Write(p[:n])
	}
	if err == io.EOF {
		c.complete = true
	}
	c.mu.Unlock()
	if err != nil {
		c.finish()
	}
	return n, err
}

func (c *originalInterruptBodyCapture) Close() error {
	err := c.underlying.Close()
	c.finish()
	return err
}

func (c *originalInterruptBodyCapture) finish() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	body := append([]byte(nil), c.body.Bytes()...)
	complete := c.complete
	c.mu.Unlock()
	c.log.record(originalInterruptWireEvent{Kind: c.kind, RequestID: c.requestID, Direction: c.direction, Complete: complete, PayloadBase64: base64.StdEncoding.EncodeToString(body), PayloadEncoding: "base64"})
}

type originalInterruptCatalog struct {
	Models []struct {
		Slug                     string `json:"slug"`
		PreferWebSockets         bool   `json:"prefer_websockets"`
		SupportedInAPI           bool   `json:"supported_in_api"`
		SupportedReasoningLevels []struct {
			Effort string `json:"effort"`
		} `json:"supported_reasoning_levels"`
	} `json:"models"`
}

func validateOriginalInterruptCatalog(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("exact Codex model catalog is unavailable")
	}
	var catalog originalInterruptCatalog
	if json.Unmarshal(body, &catalog) != nil {
		return "", errors.New("exact Codex model catalog is invalid")
	}
	matches := 0
	medium := false
	for _, model := range catalog.Models {
		if model.Slug != "gpt-6-luna" {
			continue
		}
		matches++
		if !model.PreferWebSockets || !model.SupportedInAPI {
			return "", errors.New("exact gpt-6-luna catalog row does not select Responses WebSocket")
		}
		for _, level := range model.SupportedReasoningLevels {
			medium = medium || level.Effort == "medium"
		}
	}
	if matches != 1 || !medium {
		return "", errors.New("exact gpt-6-luna catalog row or medium reasoning support is missing")
	}
	digest := fmt.Sprintf("%x", sha256Sum(body))
	return digest, nil
}

func sha256Sum(body []byte) [32]byte {
	return sha256.Sum256(body)
}

func TestLiveOriginalCodexInterrupt(t *testing.T) {
	if os.Getenv("CPA_LIVE_ORIGINAL_INTERRUPT") != "1" {
		t.Skip("set CPA_LIVE_ORIGINAL_INTERRUPT=1 to run the original Codex app-server interrupt harness")
	}
	phase, err := liveOriginalInterruptAttemptPhase(os.Getenv("CPA_LIVE_ORIGINAL_INTERRUPT_ATTEMPT"))
	if err != nil {
		t.Fatal(err)
	}
	cellKey := phase + "/codex-interrupt"
	if !liveOriginalInterruptCell(cellKey) {
		t.Fatal("original interrupt attempt did not resolve to the fixed Codex claim cell")
	}
	debugRoot := strings.TrimSpace(os.Getenv("CPA_LIVE_COPILOT_DEBUG_DIR"))
	if debugRoot == "" || !filepath.IsAbs(debugRoot) {
		t.Fatal("original Codex interrupt harness requires a private absolute diagnostics directory")
	}
	if err := os.MkdirAll(debugRoot, 0700); err != nil {
		t.Fatal("original Codex interrupt diagnostics root is unavailable")
	}
	if err := os.Chmod(debugRoot, 0700); err != nil {
		t.Fatal("original Codex interrupt diagnostics root is not private")
	}
	artifactDir, err := os.MkdirTemp(debugRoot, "original-interrupt-")
	if err != nil {
		t.Fatal("could not create private original Codex interrupt artifact directory")
	}
	if err := os.Chmod(artifactDir, 0700); err != nil {
		t.Fatal("could not restrict original Codex interrupt artifact directory")
	}
	stage := "preflight"
	summary := map[string]any{
		"claim_cell":                  cellKey,
		"original_wire_interrupt":     "not_observed",
		"driver_outcome":              "not_started",
		"actual_model_effort_checked": false,
		"artifact_directory":          artifactDir,
		"relay_log":                   filepath.Join(artifactDir, "original-client-ingress.jsonl"),
	}
	defer func() {
		summary["last_stage"] = stage
		if err := writeOriginalInterruptJSON(filepath.Join(artifactDir, "interrupt-validation-summary.json"), summary); err != nil {
			t.Errorf("could not persist private original interrupt summary")
		}
	}()
	_ = livePacketPreflight(t)
	expectedVersion := os.Getenv("CPA_LIVE_CODEX_EXPECTED_VERSION")
	if expectedVersion != "0.162.1" {
		t.Fatal("original Codex interrupt harness requires the verified 0.162.1 app-server")
	}
	var versionOutput bytes.Buffer
	_, versionErr := runLiveClient(t, liveClientStartupTimeout, os.Getenv("CPA_LIVE_CODEX_BINARY"), []string{"--version"}, func(command *exec.Cmd) {
		command.Stdout = &versionOutput
	})
	if versionErr != nil || strings.TrimSpace(versionOutput.String()) != "codex-cli "+expectedVersion {
		t.Fatal("original Codex interrupt client does not match the declared version")
	}
	summary["cli_version"] = strings.TrimSpace(versionOutput.String())
	for _, name := range []string{"CPA_LIVE_CODEX_BINARY", "CPA_LIVE_CODEX_CATALOG", "CPA_LIVE_CODEX_SANDBOX", "CPA_LIVE_CODEX_INTERRUPT_DRIVER"} {
		path := strings.TrimSpace(os.Getenv(name))
		info, statErr := os.Lstat(path)
		if path == "" || statErr != nil || !info.Mode().IsRegular() {
			summary["failure"] = "required private client input missing: " + name
			t.Fatalf("original Codex interrupt harness requires an existing regular file in %s", name)
		}
		if name == "CPA_LIVE_CODEX_INTERRUPT_DRIVER" && info.Mode().Perm()&0077 != 0 {
			t.Fatal("copied private Codex interrupt driver must have mode 0600")
		}
	}
	driverPath := strings.TrimSpace(os.Getenv("CPA_LIVE_CODEX_INTERRUPT_DRIVER"))
	probePath := filepath.Join(filepath.Dir(driverPath), "codex_interrupt_probe.py")
	probeInfo, probeErr := os.Lstat(probePath)
	if probeErr != nil || !probeInfo.Mode().IsRegular() || probeInfo.Mode().Perm()&0077 != 0 {
		t.Fatal("copied private Codex interrupt helper must exist beside the driver with mode 0600")
	}
	sandboxPath := strings.TrimSpace(os.Getenv("CPA_LIVE_CODEX_SANDBOX"))
	sandboxProfile, err := os.ReadFile(sandboxPath)
	if err != nil || !strings.Contains(string(sandboxProfile), "(deny network*)") || !strings.Contains(string(sandboxProfile), `(allow network-outbound (remote ip "localhost:*"))`) {
		t.Fatal("original Codex child sandbox must be restricted to loopback networking")
	}
	catalogPath := strings.TrimSpace(os.Getenv("CPA_LIVE_CODEX_CATALOG"))
	catalogHash, err := validateOriginalInterruptCatalog(catalogPath)
	if err != nil {
		summary["failure"] = "exact Codex catalog validation failed"
		t.Fatal("original Codex interrupt harness requires the exact GPT-6-Luna Responses WebSocket catalog")
	}
	summary["catalog_sha256"] = catalogHash
	pythonPath, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("python3 is required for the original Codex app-server driver")
	}
	var dependencyOutput bytes.Buffer
	_, dependencyErr := runLiveClient(t, liveClientStartupTimeout, pythonPath, []string{"-c", "import websockets; print(websockets.__version__)"}, func(command *exec.Cmd) {
		command.Stdout = &dependencyOutput
	})
	if dependencyErr != nil {
		t.Fatal("the existing Python websockets dependency is unavailable")
	}
	summary["python_websockets_version"] = strings.TrimSpace(dependencyOutput.String())

	stage = "captured-host-start"
	gate, base, stopHost := startLiveCapturedHarness(t, nil, liveEndpointOverrides(nil))
	defer stopHost()
	if !liveCapturedCatalogAdvertisesEndpoint(gate, "gpt-6-luna", liveCopilotRoutes["gpt-6-luna"]) {
		summary["failure"] = "fresh Copilot catalog did not advertise the exact GPT-6-Luna Responses route"
		t.Fatal("fresh captured catalog did not advertise gpt-6-luna on Responses")
	}
	if err := gate.setPhaseCell(phase, "codex-interrupt"); err != nil {
		summary["failure"] = "gate rejected the original interrupt phase/cell"
		t.Fatal("captured host did not accept the original interrupt claim cell")
	}
	gate.setFreshClaim("inference", cellKey)
	t.Cleanup(func() {
		gate.freezeCell(cellKey)
		gate.clearFreshInferenceClaim(cellKey)
	})

	stage = "relay-start"
	relayBase, err := originalInterruptRelayBaseURL(base)
	if err != nil {
		summary["failure"] = "captured host returned an invalid loopback root URL"
		t.Fatal("captured host did not provide a valid loopback root URL for the original-client relay")
	}
	relay, relayURL, stopRelay := startOriginalInterruptRelay(t, relayBase, artifactDir)
	defer func() {
		if err := stopRelay(); err != nil {
			t.Errorf("original-client relay could not flush its private frame log")
		}
	}()
	summary["relay_base_url"] = relayURL

	stage = "codex-app-server"
	stdoutPath := filepath.Join(artifactDir, "driver-stdout.jsonl")
	stderrPath := filepath.Join(artifactDir, "driver-stderr.txt")
	stdout, err := os.OpenFile(stdoutPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal("could not create private Codex driver stdout capture")
	}
	stderr, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		_ = stdout.Close()
		t.Fatal("could not create private Codex driver stderr capture")
	}
	_, runErr := runLiveClient(t, liveClientTimeout, pythonPath, []string{driverPath, "--base-url", relayURL, "--output-root", artifactDir}, func(command *exec.Cmd) {
		command.Dir = artifactDir
		command.Env = append(os.Environ(), "TASK_PROXY_API_KEY="+liveCopilotClientKey)
		command.Stdout = stdout
		command.Stderr = stderr
	})
	_ = stdout.Sync()
	_ = stderr.Sync()
	_ = stdout.Close()
	_ = stderr.Close()
	if runErr != nil {
		summary["driver_exit_error"] = fmt.Sprintf("%T", runErr)
	}
	if err := stopRelay(); err != nil {
		summary["relay_log_error"] = "private ingress log did not close cleanly"
		summary["failure"] = "original-client ingress log could not be finalized"
		t.Fatal("original-client request/response log could not be finalized")
	}
	if relay.log.failure() != nil {
		t.Fatal("original Codex ingress request/response capture failed")
	}
	stdoutBody, stdoutErr := os.ReadFile(stdoutPath)
	if stdoutErr != nil {
		t.Fatal("private Codex driver stdout capture could not be read")
	}
	outcome, outcomeErr := parseOriginalInterruptOutcome(stdoutBody)
	if outcomeErr == nil {
		summary["driver_outcome"] = outcome
	} else {
		summary["driver_outcome"] = "missing_or_invalid_json"
	}
	if outcomeErr != nil {
		summary["failure"] = "Codex driver did not return a structured outcome"
		t.Fatal("original Codex app-server did not return a structured interrupt outcome")
	}
	if runErr != nil {
		summary["failure"] = "Codex app-server driver exited unsuccessfully"
		t.Fatal("original Codex app-server driver did not exit successfully")
	}
	if err := writeOriginalInterruptJSON(filepath.Join(artifactDir, "interrupt-validation-summary.json"), summary); err != nil {
		t.Fatal("could not persist private interrupt result before assertions")
	}

	stage = "wire-evidence-validation"
	wireEvents, err := readOriginalInterruptWireLog(filepath.Join(artifactDir, "original-client-ingress.jsonl"))
	if err != nil {
		summary["failure"] = "original client ingress frame log was incomplete"
		t.Fatal("original Codex request/response frame log is unavailable")
	}
	wireEvidence := inspectOriginalInterruptWireEvents(wireEvents)
	summary["original_client_wire_evidence"] = wireEvidence
	summary["wire_capture_layer"] = "base64 of unmodified WebSocket application-message payloads; WebSocket mask bits and fragmentation boundaries are not exposed by gorilla/websocket"
	if err := validateOriginalInterruptWireEvidence(wireEvidence); err != nil {
		summary["failure"] = "original Codex WebSocket create frames did not prove GPT-6-Luna medium requests"
		t.Fatal("captured original Codex requests did not all show GPT-6-Luna medium Responses turns")
	}
	if !outcome.Initialize || !outcome.ThreadStart || !outcome.ActiveAgentMessageStarted || !outcome.InterruptRPC || !outcome.InterruptedTurnCompleted || !outcome.SameThreadFollowupCompleted || !outcome.SameThreadFollowupAnswer || outcome.ErrorCode != "" {
		summary["failure"] = "original Codex app-server interrupt/follow-up outcome was incomplete"
		t.Fatal("original Codex app-server did not interrupt the active turn and complete the same-thread follow-up")
	}
	if wireEvidence.ResponseInterruptFrames > 0 {
		summary["original_wire_interrupt"] = "observed_as_actual_client_to_cpa_websocket_message"
	} else {
		summary["original_wire_interrupt"] = "not_observed; app-server RPC interruption and captured close/cancellation remain separate evidence"
	}
	if wireEvidence.ResponseIncompleteFrames > 0 {
		summary["cpa_response_incomplete_frames"] = wireEvidence.ResponseIncompleteFrames
	} else {
		summary["cpa_response_incomplete_frames"] = 0
	}

	stage = "upstream-gate-validation"
	gateEvidence, err := inspectOriginalInterruptGate(t, gate, cellKey)
	if err != nil {
		summary["failure"] = "fresh gate did not retain exact model/effort upstream request and response"
		t.Fatal("captured upstream request/response did not prove GPT-6-Luna medium Responses traffic")
	}
	summary["upstream_gate_evidence"] = gateEvidence
	summary["actual_model_effort_checked"] = true
	if _, inferenceCount, _, _, captureErr := gate.countsFor(cellKey); captureErr != nil || inferenceCount < 2 || livePacketDeniedCount(gate) != 0 {
		summary["failure"] = "captured inference count or dispatch gate validation failed"
		t.Fatal("captured original Codex interrupt traffic did not complete two allowed inference dispatches")
	}
	stage = "complete"
	t.Logf("original Codex interrupt RPC and same-thread follow-up completed; client response.interrupt frames observed=%d, close frames=%d, upstream captures=%d", wireEvidence.ResponseInterruptFrames, wireEvidence.CloseControlFrames, gateEvidence.Captures)
}

func parseOriginalInterruptOutcome(body []byte) (originalInterruptOutcome, error) {
	var outcome originalInterruptOutcome
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if err := json.Unmarshal(line, &outcome); err == nil {
			return outcome, nil
		}
	}
	return outcome, errors.New("Codex interrupt driver returned no valid JSON result")
}

func writeOriginalInterruptJSON(path string, value any) error {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return errors.New("private interrupt evidence could not be encoded")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("private interrupt evidence could not be created")
	}
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(body)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return errors.New("private interrupt evidence could not be persisted")
	}
	return nil
}

func readOriginalInterruptWireLog(path string) ([]originalInterruptWireEvent, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var events []originalInterruptWireEvent
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 32<<20)
	for scanner.Scan() {
		var event originalInterruptWireEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

type originalInterruptWireEvidence struct {
	WebSocketMessages          int `json:"websocket_messages"`
	ClientCreateFrames         int `json:"client_response_create_frames"`
	ResponsesRouteCreateFrames int `json:"responses_route_create_frames"`
	ExactModelMediumFrames     int `json:"exact_gpt_6_luna_medium_create_frames"`
	MismatchedCreateFrames     int `json:"mismatched_create_frames"`
	UnparseableClientMessages  int `json:"unparseable_client_messages"`
	ResponseInterruptFrames    int `json:"actual_client_response_interrupt_frames"`
	ResponseIncompleteFrames   int `json:"actual_cpa_response_incomplete_frames"`
	CloseControlFrames         int `json:"websocket_close_control_frames"`
	ClientReadEnds             int `json:"original_client_websocket_read_ends"`
	CPAReadEnds                int `json:"cpa_websocket_read_ends"`
	WebSocketConnectionEnds    int `json:"websocket_connection_end_events"`
	HTTPRequests               int `json:"http_requests"`
	HTTPResponses              int `json:"http_responses"`
}

func inspectOriginalInterruptWireEvents(events []originalInterruptWireEvent) originalInterruptWireEvidence {
	var evidence originalInterruptWireEvidence
	responseRoutes := make(map[uint64]bool)
	for _, event := range events {
		if event.Kind == "websocket_handshake_request" && event.Direction == "original_client_to_cpa" && event.Method == http.MethodGet && event.URLPath == "/v1/responses" {
			responseRoutes[event.RequestID] = true
		}
	}
	for _, event := range events {
		switch event.Kind {
		case "websocket_message":
			evidence.WebSocketMessages++
			payload, err := base64.StdEncoding.DecodeString(event.PayloadBase64)
			if err != nil {
				if event.Direction == "original_client_to_cpa" {
					evidence.UnparseableClientMessages++
				}
				continue
			}
			var frame map[string]any
			if json.Unmarshal(payload, &frame) != nil {
				if event.Direction == "original_client_to_cpa" {
					evidence.UnparseableClientMessages++
				}
				continue
			}
			frameType, _ := frame["type"].(string)
			if event.Direction == "original_client_to_cpa" && frameType == "response.create" {
				evidence.ClientCreateFrames++
				if responseRoutes[event.RequestID] {
					evidence.ResponsesRouteCreateFrames++
				}
				if responseRoutes[event.RequestID] && frame["model"] == "gpt-6-luna" && liveOriginalFrameEffort(frame) == "medium" {
					evidence.ExactModelMediumFrames++
				} else {
					evidence.MismatchedCreateFrames++
				}
			}
			if event.Direction == "original_client_to_cpa" && frameType == "response.interrupt" {
				evidence.ResponseInterruptFrames++
			}
			if event.Direction == "cpa_to_original_client" && frameType == "response.incomplete" {
				evidence.ResponseIncompleteFrames++
			}
		case "websocket_control":
			if event.Control == "close" {
				evidence.CloseControlFrames++
			}
		case "websocket_read_end":
			switch event.Direction {
			case "original_client_to_cpa":
				evidence.ClientReadEnds++
			case "cpa_to_original_client":
				evidence.CPAReadEnds++
			}
		case "websocket_connection_ended":
			evidence.WebSocketConnectionEnds++
		case "http_request":
			evidence.HTTPRequests++
		case "http_response":
			evidence.HTTPResponses++
		}
	}
	return evidence
}

func validateOriginalInterruptWireEvidence(evidence originalInterruptWireEvidence) error {
	if evidence.ClientCreateFrames < 2 || evidence.ExactModelMediumFrames != evidence.ClientCreateFrames || evidence.ResponsesRouteCreateFrames != evidence.ClientCreateFrames || evidence.MismatchedCreateFrames != 0 || evidence.UnparseableClientMessages != 0 {
		return errors.New("original Codex client frames did not all use GPT-6-Luna medium on the Responses route")
	}
	return nil
}

func liveOriginalFrameEffort(frame map[string]any) string {
	if effort, ok := frame["effort"].(string); ok {
		return effort
	}
	if effort, ok := frame["reasoning_effort"].(string); ok {
		return effort
	}
	if reasoning, ok := frame["reasoning"].(map[string]any); ok {
		if effort, ok := reasoning["effort"].(string); ok {
			return effort
		}
	}
	return ""
}

type originalInterruptGateEvidence struct {
	Captures                 int   `json:"inference_captures"`
	ExactModelMediumRequests int   `json:"exact_gpt_6_luna_medium_requests"`
	ResponsesEndpoint        int   `json:"responses_endpoint_requests"`
	MismatchedRequests       int   `json:"mismatched_requests"`
	ResponseStatuses         []int `json:"upstream_response_statuses"`
}

func inspectOriginalInterruptGate(t *testing.T, gate *liveServerToolGate, key string) (originalInterruptGateEvidence, error) {
	t.Helper()
	captures, err := readLiveGateCaptures(gate.directory)
	if err != nil {
		return originalInterruptGateEvidence{}, err
	}
	return inspectOriginalInterruptGateCaptures(captures, key)
}

func readLiveGateCaptures(directory string) ([]liveServerToolCapture, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	captures := make([]liveServerToolCapture, 0, len(entries))
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "gate-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		var capture liveServerToolCapture
		if err := json.Unmarshal(body, &capture); err != nil {
			return nil, err
		}
		captures = append(captures, capture)
	}
	return captures, nil
}

func inspectOriginalInterruptGateCaptures(captures []liveServerToolCapture, key string) (originalInterruptGateEvidence, error) {
	var evidence originalInterruptGateEvidence
	for _, capture := range captures {
		if capture.Category != "inference" || capture.PhaseCell != key || !capture.Dispatched {
			continue
		}
		evidence.Captures++
		evidence.ResponseStatuses = append(evidence.ResponseStatuses, capture.Response.Status)
		valid := true
		parsedURL, err := url.Parse(capture.PublicRequest.URL)
		if err != nil || !parsedURL.IsAbs() || parsedURL.Host == "" || parsedURL.Path != "/responses" {
			valid = false
		} else {
			evidence.ResponsesEndpoint++
		}
		var request map[string]any
		if json.Unmarshal([]byte(capture.PublicRequest.Body), &request) != nil || request["model"] != "gpt-6-luna" || liveOriginalFrameEffort(request) != "medium" {
			valid = false
		} else {
			evidence.ExactModelMediumRequests++
		}
		if !valid {
			evidence.MismatchedRequests++
		}
	}
	if evidence.Captures < 2 || evidence.ResponsesEndpoint != evidence.Captures || evidence.ExactModelMediumRequests != evidence.Captures || evidence.MismatchedRequests != 0 {
		return evidence, errors.New("actual public requests did not retain exact model, effort, and Responses route")
	}
	for _, status := range evidence.ResponseStatuses {
		if status < http.StatusOK || status >= http.StatusMultipleChoices {
			return evidence, errors.New("an actual public response was unsuccessful")
		}
	}
	return evidence, nil
}

func TestOriginalInterruptWireEvidenceRejectsExtraMismatches(t *testing.T) {
	validFrame := func(model, effort string) string {
		body, err := json.Marshal(map[string]any{"type": "response.create", "model": model, "reasoning": map[string]any{"effort": effort}})
		if err != nil {
			t.Fatal(err)
		}
		return base64.StdEncoding.EncodeToString(body)
	}
	baseline := []originalInterruptWireEvent{
		{Kind: "websocket_handshake_request", RequestID: 1, Direction: "original_client_to_cpa", Method: http.MethodGet, URLPath: "/v1/responses"},
		{Kind: "websocket_handshake_request", RequestID: 2, Direction: "original_client_to_cpa", Method: http.MethodGet, URLPath: "/v1/responses"},
		{Kind: "websocket_message", RequestID: 1, Direction: "original_client_to_cpa", PayloadBase64: validFrame("gpt-6-luna", "medium")},
		{Kind: "websocket_message", RequestID: 2, Direction: "original_client_to_cpa", PayloadBase64: validFrame("gpt-6-luna", "medium")},
	}
	if err := validateOriginalInterruptWireEvidence(inspectOriginalInterruptWireEvents(baseline)); err != nil {
		t.Fatalf("minimum two valid Responses frames were rejected: %v", err)
	}
	tests := []struct {
		name        string
		frameModel  string
		frameEffort string
		routePath   string
	}{
		{name: "model", frameModel: "gpt-6-luna-wrong", frameEffort: "medium", routePath: "/v1/responses"},
		{name: "effort", frameModel: "gpt-6-luna", frameEffort: "high", routePath: "/v1/responses"},
		{name: "path", frameModel: "gpt-6-luna", frameEffort: "medium", routePath: "/v1/chat/completions"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := []originalInterruptWireEvent{
				{Kind: "websocket_handshake_request", RequestID: 1, Direction: "original_client_to_cpa", Method: http.MethodGet, URLPath: "/v1/responses"},
				{Kind: "websocket_handshake_request", RequestID: 2, Direction: "original_client_to_cpa", Method: http.MethodGet, URLPath: "/v1/responses"},
				{Kind: "websocket_handshake_request", RequestID: 3, Direction: "original_client_to_cpa", Method: http.MethodGet, URLPath: test.routePath},
				{Kind: "websocket_message", RequestID: 1, Direction: "original_client_to_cpa", PayloadBase64: validFrame("gpt-6-luna", "medium")},
				{Kind: "websocket_message", RequestID: 2, Direction: "original_client_to_cpa", PayloadBase64: validFrame("gpt-6-luna", "medium")},
				{Kind: "websocket_message", RequestID: 3, Direction: "original_client_to_cpa", PayloadBase64: validFrame(test.frameModel, test.frameEffort)},
			}
			evidence := inspectOriginalInterruptWireEvents(events)
			if evidence.ClientCreateFrames != 3 || evidence.ExactModelMediumFrames != 2 || evidence.MismatchedCreateFrames != 1 {
				t.Fatalf("unexpected frame evidence: %+v", evidence)
			}
			if err := validateOriginalInterruptWireEvidence(evidence); err == nil {
				t.Fatal("evidence accepted a third mismatching response.create frame")
			}
		})
	}
}

func TestOriginalInterruptGateEvidenceRejectsExtraMismatches(t *testing.T) {
	valid := func(path, model, effort string) liveServerToolCapture {
		body, err := json.Marshal(map[string]any{"model": model, "reasoning": map[string]any{"effort": effort}})
		if err != nil {
			t.Fatal(err)
		}
		return liveServerToolCapture{
			Category:   "inference",
			PhaseCell:  liveOriginalInterruptCellKey,
			Dispatched: true,
			PublicRequest: liveHTTPLog{
				URL:  "https://api.githubcopilot.com" + path,
				Body: string(body),
			},
			Response: liveHTTPLog{Status: http.StatusOK},
		}
	}
	if _, err := inspectOriginalInterruptGateCaptures([]liveServerToolCapture{
		valid("/responses", "gpt-6-luna", "medium"),
		valid("/responses", "gpt-6-luna", "medium"),
	}, liveOriginalInterruptCellKey); err != nil {
		t.Fatalf("minimum two valid Responses captures were rejected: %v", err)
	}
	tests := []struct {
		name          string
		third         liveServerToolCapture
		expectedExact int
		expectedRoute int
	}{
		{name: "model", third: valid("/responses", "gpt-6-luna-wrong", "medium"), expectedExact: 2, expectedRoute: 3},
		{name: "effort", third: valid("/responses", "gpt-6-luna", "high"), expectedExact: 2, expectedRoute: 3},
		{name: "path", third: valid("/chat/completions", "gpt-6-luna", "medium"), expectedExact: 3, expectedRoute: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			captures := []liveServerToolCapture{
				valid("/responses", "gpt-6-luna", "medium"),
				valid("/responses", "gpt-6-luna", "medium"),
				test.third,
			}
			evidence, err := inspectOriginalInterruptGateCaptures(captures, liveOriginalInterruptCellKey)
			if err == nil {
				t.Fatal("evidence accepted a third mismatching dispatched inference capture")
			}
			if evidence.Captures != 3 || evidence.ExactModelMediumRequests != test.expectedExact || evidence.ResponsesEndpoint != test.expectedRoute || evidence.MismatchedRequests != 1 {
				t.Fatalf("unexpected gate evidence: %+v", evidence)
			}
		})
	}
}
