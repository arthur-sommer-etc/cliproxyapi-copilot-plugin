//go:build !windows

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type liveAttachmentIngressCapture struct {
	Sequence                int         `json:"sequence"`
	Dispatched              bool        `json:"dispatched"`
	Request                 liveHTTPLog `json:"original_client_request"`
	Response                liveHTTPLog `json:"local_host_response"`
	ErrorClass              string      `json:"error_class,omitempty"`
	BodyComplete            bool        `json:"body_complete"`
	ResponseBodyComplete    bool        `json:"response_body_complete,omitempty"`
	StreamReadErrorKind     string      `json:"stream_read_error_kind,omitempty"`
	DownstreamContextState  string      `json:"downstream_context_state,omitempty"`
	OutboundContextState    string      `json:"outbound_context_state,omitempty"`
	StreamTerminalComplete  bool        `json:"stream_terminal_complete,omitempty"`
	StreamTerminalFunction  bool        `json:"stream_terminal_function_call,omitempty"`
	StreamTerminalForwarded bool        `json:"stream_terminal_forwarded,omitempty"`
	StreamTerminalBytes     int         `json:"stream_terminal_bytes,omitempty"`
	StreamForwardedBytes    int         `json:"stream_forwarded_bytes,omitempty"`
	StreamFlushedBytes      int         `json:"stream_flushed_bytes,omitempty"`
	StreamContextAtTerminal string      `json:"stream_context_at_terminal,omitempty"`
	StreamOutcome           string      `json:"stream_outcome,omitempty"`
	CaptureTruncated        bool        `json:"capture_truncated,omitempty"`
}

type liveAttachmentIngress struct {
	mu             sync.Mutex
	server         *httptest.Server
	client         *http.Client
	gate           *liveServerToolGate
	directory      string
	upstream       string
	clientID       string
	sequence       int
	denials        int
	failure        error
	captureFailure bool
	refusalStatus  int
}

func newLiveAttachmentIngress(t *testing.T, gate *liveServerToolGate, base, directory, clientID string) *liveAttachmentIngress {
	t.Helper()
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Path != "" || parsed.User != nil {
		t.Fatal("original-client ingress must forward only to the owned local CPA host")
	}
	if err := os.MkdirAll(directory, 0700); err != nil || os.Chmod(directory, 0700) != nil {
		t.Fatal("could not create private original-client ingress capture root")
	}
	ingress := &liveAttachmentIngress{client: &http.Client{Timeout: 120 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: liveOneShotTransport{}}, gate: gate, directory: directory, upstream: base, clientID: clientID}
	ingress.server = httptest.NewServer(http.HandlerFunc(ingress.serveHTTP))
	t.Cleanup(ingress.server.Close)
	return ingress
}

func (i *liveAttachmentIngress) URL() string { return i.server.URL }

func (i *liveAttachmentIngress) Close() { i.server.Close() }

func (i *liveAttachmentIngress) state() (int, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.denials, i.failure
}

func (i *liveAttachmentIngress) failureState() (bool, int) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.captureFailure, i.refusalStatus
}

func (i *liveAttachmentIngress) fail(err error) {
	i.mu.Lock()
	i.captureFailure = true
	if i.failure == nil {
		i.failure = err
	}
	i.mu.Unlock()
}

func (i *liveAttachmentIngress) refuse(status int) {
	i.mu.Lock()
	i.refusalStatus = status
	if i.failure == nil {
		i.failure = errors.New("original client local host returned a refusal")
	}
	i.mu.Unlock()
}

func (i *liveAttachmentIngress) reserve(method, path string) (int, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.sequence++
	allowedPath := method == http.MethodPost && (i.clientID == "claude" && path == "/v1/messages" || i.clientID == "codex" && path == "/v1/responses")
	if i.failure != nil || !allowedPath {
		i.denials++
		return i.sequence, false
	}
	return i.sequence, true
}

func (i *liveAttachmentIngress) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	const maxBody = 16 << 20
	sequence, allowed := i.reserve(request.Method, request.URL.Path)
	if !allowed {
		body, readErr := io.ReadAll(io.LimitReader(request.Body, maxBody+1))
		complete := readErr == nil && len(body) <= maxBody
		retained := body[:min(len(body), maxBody)]
		responseBody := []byte("original client path was not prepared\n")
		capture := liveAttachmentIngressCapture{Sequence: sequence, Dispatched: false, Request: liveHTTPLog{Method: request.Method, URL: request.URL.String(), Host: request.Host, Proto: request.Proto, Headers: i.gate.redactedHeaders(request.Header), Body: i.gate.redactedBody(retained), ContentLength: request.ContentLength, ContentEncoding: request.Header.Get("Content-Encoding"), TransferEncoding: append([]string(nil), request.TransferEncoding...)}, Response: liveHTTPLog{Status: http.StatusForbidden, Body: i.gate.redactedBody(responseBody)}, ErrorClass: "original_client_path_not_prepared", BodyComplete: complete, ResponseBodyComplete: true}
		if !complete {
			i.fail(errors.New("original client denied request body capture was incomplete"))
		}
		if err := i.persist(capture); err != nil {
			i.fail(errors.New("original client denial capture was not durable"))
		}
		http.Error(writer, "original client path was not prepared", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		retained := body[:min(len(body), maxBody)]
		capture := liveAttachmentIngressCapture{Sequence: sequence, Dispatched: false, Request: liveHTTPLog{Method: request.Method, URL: request.URL.String(), Host: request.Host, Proto: request.Proto, Headers: i.gate.redactedHeaders(request.Header), Body: i.gate.redactedBody(retained), ContentLength: request.ContentLength, ContentEncoding: request.Header.Get("Content-Encoding"), TransferEncoding: append([]string(nil), request.TransferEncoding...)}, Response: liveHTTPLog{Status: http.StatusRequestEntityTooLarge}, ErrorClass: "original_client_request_unavailable", BodyComplete: false}
		if persistErr := i.persist(capture); persistErr != nil {
			i.fail(errors.New("original client partial request capture was not durable"))
		}
		i.fail(errors.New("original client request capture was unavailable"))
		http.Error(writer, "original client request was unavailable", http.StatusRequestEntityTooLarge)
		return
	}
	capture := liveAttachmentIngressCapture{Sequence: sequence, Request: liveHTTPLog{Method: request.Method, URL: request.URL.String(), Host: request.Host, Proto: request.Proto, Headers: i.gate.redactedHeaders(request.Header), Body: i.gate.redactedBody(body), ContentLength: request.ContentLength, ContentEncoding: request.Header.Get("Content-Encoding"), TransferEncoding: append([]string(nil), request.TransferEncoding...)}, BodyComplete: true}
	if err := i.persist(capture); err != nil {
		i.fail(errors.New("original client pre-dispatch capture was not durable"))
		http.Error(writer, "original client capture failed", http.StatusBadGateway)
		return
	}
	upstreamURL := i.upstream + request.URL.RequestURI()
	forward, err := http.NewRequestWithContext(request.Context(), request.Method, upstreamURL, bytes.NewReader(body))
	if err != nil {
		i.fail(errors.New("original client local forward could not be prepared"))
		http.Error(writer, "original client local forward failed", http.StatusBadGateway)
		return
	}
	forward.Header = request.Header.Clone()
	removeHopHeaders(forward.Header)
	forward.Host = forward.URL.Host
	capture.Dispatched = true
	response, err := i.client.Do(forward)
	if err != nil {
		capture.ErrorClass = "local_host_transport_error"
		i.fail(errors.New("original client local host transport failed"))
		_ = i.persist(capture)
		http.Error(writer, "local host transport failed", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		i.refuse(response.StatusCode)
	}
	copyHTTPHeaders(writer.Header(), response.Header)
	removeHopHeaders(writer.Header())
	writer.Header().Del("Content-Length")
	writer.WriteHeader(response.StatusCode)
	capture.Response = liveHTTPLog{Proto: response.Proto, Status: response.StatusCode, Headers: i.gate.redactedHeaders(response.Header), ContentLength: response.ContentLength, ContentEncoding: response.Header.Get("Content-Encoding"), TransferEncoding: append([]string(nil), response.TransferEncoding...)}
	var retained bytes.Buffer
	chunk := make([]byte, 32<<10)
	var requestModel struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &requestModel)
	streamEndedCleanly := false
	forwardedBytes, flushedBytes := 0, 0
	for {
		n, readErr := response.Body.Read(chunk)
		if n > 0 {
			remaining := maxBody - retained.Len()
			if remaining > 0 {
				retained.Write(chunk[:min(n, remaining)])
			}
			if n > remaining {
				capture.CaptureTruncated = true
				i.fail(errors.New("original client response capture was truncated"))
			}
			if !capture.CaptureTruncated && requestModel.Model != "" {
				complete, functionCall := liveAttachmentIngressCompleteTerminal(retained.Bytes(), requestModel.Model)
				if complete {
					capture.StreamTerminalComplete = true
					capture.StreamTerminalFunction = functionCall
					if capture.StreamTerminalBytes == 0 {
						capture.StreamTerminalBytes = retained.Len()
						capture.StreamContextAtTerminal = liveSafeStreamReadErrorKind(request.Context().Err())
					}
				}
			}
			written, writeErr := writer.Write(chunk[:n])
			forwardedBytes += written
			if writeErr != nil || written != n {
				capture.ErrorClass = "original_client_response_write_error"
				i.fail(errors.New("original client response write failed"))
				break
			}
			if flusher, ok := writer.(http.Flusher); ok {
				flusher.Flush()
				flushedBytes = forwardedBytes
			}
			if capture.StreamTerminalComplete && capture.StreamContextAtTerminal == "active" && flushedBytes >= capture.StreamTerminalBytes && !capture.CaptureTruncated {
				capture.StreamTerminalForwarded = true
			}
		}
		if readErr == io.EOF {
			streamEndedCleanly = true
			break
		}
		if readErr != nil {
			capture.ErrorClass = "local_host_response_read_error"
			capture.StreamReadErrorKind = liveSafeStreamReadErrorKind(readErr)
			capture.DownstreamContextState = liveSafeStreamReadErrorKind(request.Context().Err())
			capture.OutboundContextState = liveSafeStreamReadErrorKind(forward.Context().Err())
			break
		}
	}
	capture.ResponseBodyComplete = streamEndedCleanly && !capture.CaptureTruncated
	capture.StreamForwardedBytes = forwardedBytes
	capture.StreamFlushedBytes = flushedBytes
	capture.StreamTerminalForwarded = capture.StreamTerminalForwarded && capture.StreamTerminalComplete && forwardedBytes >= capture.StreamTerminalBytes && flushedBytes >= capture.StreamTerminalBytes && !capture.CaptureTruncated
	liveAttachmentIngressConfirmFinalTerminal(&capture, retained.Bytes(), requestModel.Model)
	capture.Response.Body = i.gate.redactedBody(retained.Bytes())
	switch {
	case capture.ErrorClass == "local_host_response_read_error" && liveAttachmentIngressSemanticCancellation(capture, request.Context().Err(), forward.Context().Err()):
		capture.StreamOutcome = "semantic_complete_function_call_downstream_cancelled"
	case capture.ErrorClass == "local_host_response_read_error" && liveAttachmentIngressTextTerminalCancellation(capture, request.Context().Err(), forward.Context().Err()):
		capture.StreamOutcome = "semantic_complete_text_terminal_downstream_cancelled"
	case streamEndedCleanly && capture.ErrorClass == "" && !capture.CaptureTruncated:
		capture.StreamOutcome = "clean_eof"
	case capture.ErrorClass != "":
		capture.StreamOutcome = "unconfirmed_stream_failure"
		i.fail(errors.New("original client local host response read failed"))
	case capture.CaptureTruncated:
		capture.StreamOutcome = "unconfirmed_stream_failure"
	default:
		capture.StreamOutcome = "unconfirmed_stream_failure"
		i.fail(errors.New("original client local host response did not reach a confirmed terminal"))
	}
	if err := i.persist(capture); err != nil {
		i.fail(errors.New("original client response capture was not durable"))
	}
}

func liveAttachmentIngressConfirmFinalTerminal(capture *liveAttachmentIngressCapture, body []byte, expectedModel string) {
	if capture == nil {
		return
	}
	if capture.CaptureTruncated {
		capture.StreamTerminalComplete = false
		capture.StreamTerminalFunction = false
		capture.StreamTerminalForwarded = false
		capture.StreamTerminalBytes = 0
		capture.StreamContextAtTerminal = ""
		return
	}
	complete, functionCall := liveAttachmentIngressCompleteTerminal(body, expectedModel)
	if !complete {
		capture.StreamTerminalComplete = false
		capture.StreamTerminalFunction = false
		capture.StreamTerminalForwarded = false
		capture.StreamTerminalBytes = 0
		capture.StreamContextAtTerminal = ""
		return
	}
	capture.StreamTerminalComplete = true
	capture.StreamTerminalFunction = functionCall
}

func liveAttachmentIngressCompleteTerminal(body []byte, expectedModel string) (complete bool, functionCall bool) {
	if liveCompleteResponseFunctionCallTerminal(body, expectedModel) {
		return true, true
	}
	if liveCompleteResponseTextTerminal(body, expectedModel) {
		return true, false
	}
	return false, false
}

func liveCompleteResponseTextTerminal(body []byte, expectedModel string) bool {
	if expectedModel == "" || !liveCompleteResponseTerminal(body, expectedModel) || liveCompleteResponseFunctionCallTerminal(body, expectedModel) {
		return false
	}
	normalized := bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
	frames := bytes.Split(bytes.TrimRight(normalized, "\n"), []byte("\n\n"))
	if len(frames) == 0 {
		return false
	}
	lastFrame := len(frames) - 1
	for lastFrame >= 0 && len(bytes.TrimSpace(frames[lastFrame])) == 0 {
		lastFrame--
	}
	if lastFrame < 0 {
		return false
	}
	var data string
	for _, line := range bytes.Split(frames[lastFrame], []byte("\n")) {
		if bytes.HasPrefix(line, []byte("data:")) {
			data = strings.TrimSpace(string(bytes.TrimPrefix(line, []byte("data:"))))
		}
	}
	var event struct {
		Response struct {
			Output []struct {
				Type    string `json:"type"`
				Role    string `json:"role"`
				Status  string `json:"status"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"output"`
		} `json:"response"`
	}
	if json.Unmarshal([]byte(data), &event) != nil {
		return false
	}
	for _, item := range event.Response.Output {
		if item.Type != "message" || item.Status != "completed" || item.Role != "assistant" {
			continue
		}
		for _, content := range item.Content {
			if content.Type == "output_text" && strings.TrimSpace(content.Text) != "" {
				return true
			}
		}
	}
	return false
}

func liveAttachmentIngressSemanticCancellation(capture liveAttachmentIngressCapture, downstream, outbound error) bool {
	if capture.ErrorClass != "local_host_response_read_error" || !capture.BodyComplete || capture.ResponseBodyComplete || !capture.StreamTerminalFunction || capture.Response.Status < 200 || capture.Response.Status >= 300 || capture.DownstreamContextState != "context_canceled" || capture.OutboundContextState != "context_canceled" {
		return false
	}
	proof := liveServerToolCapture{
		ErrorClass:              "upstream_response_read_error",
		StreamReadErrorKind:     capture.StreamReadErrorKind,
		DownstreamContextState:  capture.DownstreamContextState,
		OutboundContextState:    capture.OutboundContextState,
		StreamTerminalComplete:  capture.StreamTerminalComplete,
		StreamTerminalForwarded: capture.StreamTerminalForwarded,
		StreamTerminalBytes:     capture.StreamTerminalBytes,
		StreamForwardedBytes:    capture.StreamForwardedBytes,
		StreamFlushedBytes:      capture.StreamFlushedBytes,
		StreamContextAtTerminal: capture.StreamContextAtTerminal,
		CaptureTruncated:        capture.CaptureTruncated,
	}
	return liveSemanticDownstreamCancellation(proof, downstream, outbound)
}

func liveAttachmentIngressTextTerminalCancellation(capture liveAttachmentIngressCapture, downstream, outbound error) bool {
	var request struct {
		Model string `json:"model"`
	}
	if json.Unmarshal([]byte(capture.Request.Body), &request) != nil || request.Model == "" || capture.ErrorClass != "local_host_response_read_error" || !capture.BodyComplete || capture.ResponseBodyComplete || !capture.StreamTerminalComplete || capture.StreamTerminalFunction || !capture.StreamTerminalForwarded || capture.StreamTerminalBytes <= 0 || capture.Response.Status < 200 || capture.Response.Status >= 300 || capture.StreamReadErrorKind != "context_canceled" || capture.DownstreamContextState != "context_canceled" || capture.OutboundContextState != "context_canceled" || capture.StreamContextAtTerminal != "active" || capture.CaptureTruncated {
		return false
	}
	body := []byte(capture.Response.Body)
	if !liveCompleteResponseTextTerminal(body, request.Model) || capture.StreamForwardedBytes < len(body) || capture.StreamFlushedBytes < len(body) {
		return false
	}
	proof := liveServerToolCapture{
		ErrorClass:              "upstream_response_read_error",
		StreamReadErrorKind:     capture.StreamReadErrorKind,
		DownstreamContextState:  capture.DownstreamContextState,
		OutboundContextState:    capture.OutboundContextState,
		StreamTerminalComplete:  capture.StreamTerminalComplete,
		StreamTerminalForwarded: capture.StreamTerminalForwarded,
		StreamTerminalBytes:     capture.StreamTerminalBytes,
		StreamForwardedBytes:    capture.StreamForwardedBytes,
		StreamFlushedBytes:      capture.StreamFlushedBytes,
		StreamContextAtTerminal: capture.StreamContextAtTerminal,
		CaptureTruncated:        capture.CaptureTruncated,
	}
	return liveSemanticDownstreamCancellation(proof, downstream, outbound)
}

func TestLiveAttachmentIngressCompletedTextTerminalCancellationRequiresFullForwardedProof(t *testing.T) {
	body := []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"object\":\"response\",\"status\":\"completed\",\"model\":\"claude-haiku-5.5\",\"usage\":{\"input_tokens\":12,\"output_tokens\":4},\"output\":[{\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"The top half is red and the bottom half is blue.\"}]}]}}\n\n")
	capture := liveAttachmentIngressCapture{
		Dispatched: true,
		Request:    liveHTTPLog{Body: `{"model":"claude-haiku-5.5"}`},
		Response:   liveHTTPLog{Status: http.StatusOK, Body: string(body)},
		ErrorClass: "local_host_response_read_error", BodyComplete: true, ResponseBodyComplete: false,
		StreamReadErrorKind: "context_canceled", DownstreamContextState: "context_canceled", OutboundContextState: "context_canceled",
		StreamTerminalComplete: true, StreamTerminalForwarded: true, StreamTerminalBytes: len(body),
		StreamForwardedBytes: len(body), StreamFlushedBytes: len(body), StreamContextAtTerminal: "active",
		StreamOutcome: "semantic_complete_text_terminal_downstream_cancelled",
	}
	if !liveCompleteResponseTextTerminal(body, "claude-haiku-5.5") || !liveAttachmentIngressTextTerminalCancellation(capture, context.Canceled, context.Canceled) {
		t.Fatal("complete native Responses text terminal canceled after full delivery was not recognized")
	}
	for name, alter := range map[string]func(*liveAttachmentIngressCapture){
		"wrong-model": func(value *liveAttachmentIngressCapture) {
			value.Response.Body = strings.Replace(value.Response.Body, `"model":"claude-haiku-5.5"`, `"model":"other"`, 1)
		},
		"missing-usage": func(value *liveAttachmentIngressCapture) {
			value.Response.Body = strings.Replace(value.Response.Body, `"usage":{"input_tokens":12,"output_tokens":4}`, `"usage":{}`, 1)
		},
		"truncated": func(value *liveAttachmentIngressCapture) {
			value.Response.Body = strings.TrimSuffix(value.Response.Body, "\n\n")
		},
		"nonterminal": func(value *liveAttachmentIngressCapture) {
			value.Response.Body = strings.ReplaceAll(value.Response.Body, "response.completed", "response.incomplete")
		},
		"tool-calls-not-text": func(value *liveAttachmentIngressCapture) {
			value.Response.Body = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"object\":\"response\",\"status\":\"completed\",\"model\":\"claude-haiku-5.5\",\"usage\":{\"input_tokens\":12,\"output_tokens\":4},\"output\":[{\"type\":\"function_call\",\"status\":\"completed\",\"call_id\":\"call-exec\",\"name\":\"exec_command\",\"arguments\":\"{}\"}]}}\n\n"
		},
		"short-forward": func(value *liveAttachmentIngressCapture) { value.StreamForwardedBytes-- },
		"short-flush":   func(value *liveAttachmentIngressCapture) { value.StreamFlushedBytes-- },
		"wrong-read-error": func(value *liveAttachmentIngressCapture) {
			value.StreamReadErrorKind = "unexpected_eof"
		},
		"not-downstream-cancel": func(value *liveAttachmentIngressCapture) {
			value.DownstreamContextState = "active"
		},
		"capture-truncated":        func(value *liveAttachmentIngressCapture) { value.CaptureTruncated = true },
		"wrong-status":             func(value *liveAttachmentIngressCapture) { value.Response.Status = http.StatusBadRequest },
		"function-terminal-marker": func(value *liveAttachmentIngressCapture) { value.StreamTerminalFunction = true },
	} {
		t.Run(name, func(t *testing.T) {
			changed := capture
			alter(&changed)
			before, _ := json.Marshal(capture)
			after, _ := json.Marshal(changed)
			if bytes.Equal(before, after) {
				t.Fatal("negative cancellation fixture did not mutate the captured proof")
			}
			if liveAttachmentIngressTextTerminalCancellation(changed, context.Canceled, context.Canceled) {
				t.Fatal("truncated, unproven, mismatched, or tool-call response was treated as a text terminal")
			}
		})
	}
}

func TestAttachmentPacketRetainedPNGResponseTextTerminalReplay(t *testing.T) {
	caseDirectory := os.Getenv("CPA_ATTACHMENT_REPLAY_CASE_DIR")
	if caseDirectory == "" {
		t.Skip("set CPA_ATTACHMENT_REPLAY_CASE_DIR to inspect the retained original Codex PNG response body")
	}
	body, err := os.ReadFile(filepath.Join(caseDirectory, "original-client-hop", "client-hop-001.json"))
	if err != nil {
		t.Fatal("retained original-client response capture is unavailable")
	}
	var capture liveAttachmentIngressCapture
	if json.Unmarshal(body, &capture) != nil || !liveCompleteResponseTextTerminal([]byte(capture.Response.Body), "claude-haiku-5.5") {
		t.Fatal("actual captured Codex PNG completed text response did not match its native Responses terminal schema")
	}
	mutations := map[string]func(map[string]any){
		"wrong-model":        func(response map[string]any) { response["model"] = "claude-haiku-5.5-low" },
		"missing-usage":      func(response map[string]any) { delete(response, "usage") },
		"nonterminal-status": func(response map[string]any) { response["status"] = "incomplete" },
		"tool-calls-not-text": func(response map[string]any) {
			response["output"] = []any{map[string]any{"type": "function_call", "status": "completed", "call_id": "call-exec", "name": "exec_command", "arguments": "{}"}}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := liveAttachmentMutateFinalResponsesResponse(t, capture.Response.Body, mutate)
			if liveCompleteResponseTextTerminal(changed, "claude-haiku-5.5") {
				t.Fatal("mutated actual captured Responses terminal was still recognized")
			}
		})
	}
	if liveCompleteResponseTextTerminal([]byte(strings.TrimSuffix(capture.Response.Body, "\n\n")), "claude-haiku-5.5") {
		t.Fatal("truncated actual captured Responses stream was recognized as complete")
	}
}

func liveAttachmentMutateFinalResponsesResponse(t *testing.T, body string, mutate func(map[string]any)) []byte {
	t.Helper()
	normalized := bytes.ReplaceAll([]byte(body), []byte("\r\n"), []byte("\n"))
	frames := bytes.Split(normalized, []byte("\n\n"))
	for index := len(frames) - 1; index >= 0; index-- {
		frame := frames[index]
		if len(bytes.TrimSpace(frame)) == 0 {
			continue
		}
		lines := bytes.Split(frame, []byte("\n"))
		for lineIndex, line := range lines {
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			var event map[string]any
			if json.Unmarshal(bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:"))), &event) != nil {
				t.Fatal("captured terminal event did not decode")
			}
			response, ok := event["response"].(map[string]any)
			if !ok {
				t.Fatal("captured terminal did not contain a response object")
			}
			mutate(response)
			encoded, err := json.Marshal(event)
			if err != nil {
				t.Fatal("mutated captured terminal could not be encoded")
			}
			lines[lineIndex] = append([]byte("data: "), encoded...)
			frames[index] = bytes.Join(lines, []byte("\n"))
			return bytes.Join(frames, []byte("\n\n"))
		}
	}
	t.Fatal("captured response terminal data event was not found")
	return nil
}

func liveCompleteResponseFunctionCallTerminal(body []byte, expectedModel string) bool {
	if expectedModel == "" || !liveCompleteResponseTerminal(body, expectedModel) {
		return false
	}
	normalized := bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
	frames := bytes.Split(bytes.TrimRight(normalized, "\n"), []byte("\n\n"))
	if len(frames) == 0 {
		return false
	}
	lastFrame := len(frames) - 1
	for lastFrame >= 0 && len(bytes.TrimSpace(frames[lastFrame])) == 0 {
		lastFrame--
	}
	if lastFrame < 0 {
		return false
	}
	var data string
	for _, line := range bytes.Split(frames[lastFrame], []byte("\n")) {
		if bytes.HasPrefix(line, []byte("data:")) {
			data = strings.TrimSpace(string(bytes.TrimPrefix(line, []byte("data:"))))
		}
	}
	var event struct {
		Response struct {
			Output []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"output"`
		} `json:"response"`
	}
	if json.Unmarshal([]byte(data), &event) != nil {
		return false
	}
	for _, item := range event.Response.Output {
		if item.Type == "function_call" && item.Status == "completed" {
			return true
		}
	}
	return false
}

func (i *liveAttachmentIngress) persist(capture liveAttachmentIngressCapture) error {
	content, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(i.directory, fmt.Sprintf("client-hop-%03d.json", capture.Sequence))
	temporary, err := os.CreateTemp(i.directory, ".client-hop-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if err := temporary.Chmod(0600); err != nil {
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return err
	}
	directory, err := os.Open(i.directory)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (i *liveAttachmentIngress) captures() ([]liveAttachmentIngressCapture, error) {
	paths, err := filepath.Glob(filepath.Join(i.directory, "client-hop-*.json"))
	if err != nil {
		return nil, err
	}
	result := make([]liveAttachmentIngressCapture, 0, len(paths))
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var capture liveAttachmentIngressCapture
		if err := json.Unmarshal(body, &capture); err != nil {
			return nil, err
		}
		result = append(result, capture)
	}
	return result, nil
}

func TestAttachmentIngressRejectsUnpreparedPathsBeforeForwarding(t *testing.T) {
	gate := newLiveServerToolGate(t, t.TempDir())
	ingress := newLiveAttachmentIngress(t, gate, "http://127.0.0.1:1", t.TempDir(), "claude")
	requestBody := `{"model":"gpt-6-luna","tools":[{"type":"file_search"}]}`
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ingress.URL()+"/v1/responses", strings.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatal("unprepared original-client path was forwarded")
	}
	responseBody, err := io.ReadAll(response.Body)
	if err != nil || string(responseBody) != "original client path was not prepared\n" {
		t.Fatal("unprepared original-client denial response body was not preserved")
	}
	denials, _ := ingress.state()
	if denials != 1 {
		t.Fatal("unprepared original-client path was not counted")
	}
	captures, err := ingress.captures()
	if err != nil || len(captures) != 1 || captures[0].Dispatched || !captures[0].BodyComplete || !captures[0].ResponseBodyComplete || captures[0].Request.Body != gate.redactedBody([]byte(requestBody)) || captures[0].Response.Body != gate.redactedBody(responseBody) {
		t.Fatal("denied original-client request and response bodies were not fully retained")
	}
}

func TestAttachmentIngressContinuesAfterForwardedFunctionCallTerminalCancellation(t *testing.T) {
	gate := newLiveServerToolGate(t, t.TempDir())
	ingress := newLiveAttachmentIngress(t, gate, "http://127.0.0.1:1", t.TempDir(), "codex")
	terminal := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"object\":\"response\",\"status\":\"completed\",\"model\":\"claude-haiku-5.5\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1},\"output\":[{\"type\":\"function_call\",\"status\":\"completed\",\"name\":\"Read\",\"call_id\":\"fixture-call\",\"arguments\":\"{\\\"file_path\\\":\\\"fixture.pdf\\\"}\"}]}}\n\n"
	var calls atomic.Int32
	ingress.client.Transport = liveToolRoundTrip(func(request *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			reader, writer := io.Pipe()
			go func() {
				if _, err := io.WriteString(writer, terminal); err != nil {
					return
				}
				<-request.Context().Done()
				_ = writer.CloseWithError(request.Context().Err())
			}()
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader, ContentLength: -1}, nil
		}
		return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody, ContentLength: 0}, nil
	})
	firstBody := `{"model":"claude-haiku-5.5","stream":true}`
	ctx, cancel := context.WithCancel(context.Background())
	firstRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, ingress.URL()+"/v1/responses", strings.NewReader(firstBody))
	if err != nil {
		t.Fatal(err)
	}
	firstResponse, err := http.DefaultClient.Do(firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	forwardedTerminal := make([]byte, len(terminal))
	if _, err := io.ReadFull(firstResponse.Body, forwardedTerminal); err != nil || string(forwardedTerminal) != terminal {
		cancel()
		_ = firstResponse.Body.Close()
		t.Fatal("function_call terminal was not fully forwarded before cancellation")
	}
	cancel()
	_ = firstResponse.Body.Close()

	var captures []liveAttachmentIngressCapture
	var captureErr error
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		captures, captureErr = ingress.captures()
		if captureErr == nil && len(captures) == 1 && captures[0].ErrorClass == "local_host_response_read_error" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if captureErr != nil || len(captures) != 1 {
		t.Fatal("cancelled function_call response was not captured")
	}
	first := captures[0]
	if !first.Dispatched || !first.BodyComplete || first.ResponseBodyComplete || first.ErrorClass != "local_host_response_read_error" || first.StreamReadErrorKind != "context_canceled" || first.StreamOutcome != "semantic_complete_function_call_downstream_cancelled" || !first.StreamTerminalComplete || !first.StreamTerminalFunction || !first.StreamTerminalForwarded || first.StreamContextAtTerminal != "active" || first.Response.Body != gate.redactedBody([]byte(terminal)) {
		t.Fatal("function_call cancellation was mislabeled as clean EOF or its full body was not retained")
	}
	denials, failure := ingress.state()
	if denials != 0 || failure != nil {
		t.Fatal("proven function_call continuation was blocked")
	}

	secondRequest, err := http.NewRequest(http.MethodPost, ingress.URL()+"/v1/responses", strings.NewReader(firstBody))
	if err != nil {
		t.Fatal(err)
	}
	secondResponse, err := http.DefaultClient.Do(secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = secondResponse.Body.Close()
	if secondResponse.StatusCode != http.StatusNoContent || calls.Load() != 2 {
		t.Fatal("second original-client turn did not reach the local host")
	}
	captures, captureErr = ingress.captures()
	if captureErr != nil || len(captures) != 2 || !captures[1].Dispatched || !captures[1].BodyComplete || !captures[1].ResponseBodyComplete || captures[1].ErrorClass != "" {
		t.Fatal("second original-client turn was not retained as a complete local-host response")
	}
}

func TestAttachmentIngressSemanticCancellationRejectsUnknownOrPartialFailures(t *testing.T) {
	good := liveAttachmentIngressCapture{
		BodyComplete: true, ErrorClass: "local_host_response_read_error", StreamReadErrorKind: "context_canceled",
		DownstreamContextState: "context_canceled", OutboundContextState: "context_canceled", StreamTerminalComplete: true,
		StreamTerminalFunction: true, StreamTerminalForwarded: true, StreamTerminalBytes: 10, StreamForwardedBytes: 10,
		StreamFlushedBytes: 10, StreamContextAtTerminal: "active", Response: liveHTTPLog{Status: http.StatusOK},
	}
	if !liveAttachmentIngressSemanticCancellation(good, context.Canceled, context.Canceled) {
		t.Fatal("fully framed and forwarded function_call cancellation was rejected")
	}
	for _, test := range []struct {
		name       string
		capture    liveAttachmentIngressCapture
		downstream error
		outbound   error
	}{
		{"partial", func() liveAttachmentIngressCapture { c := good; c.StreamTerminalComplete = false; return c }(), context.Canceled, context.Canceled},
		{"not-function-call", func() liveAttachmentIngressCapture { c := good; c.StreamTerminalFunction = false; return c }(), context.Canceled, context.Canceled},
		{"not-forwarded", func() liveAttachmentIngressCapture { c := good; c.StreamTerminalForwarded = false; return c }(), context.Canceled, context.Canceled},
		{"truncated", func() liveAttachmentIngressCapture { c := good; c.CaptureTruncated = true; return c }(), context.Canceled, context.Canceled},
		{"response-body-complete", func() liveAttachmentIngressCapture { c := good; c.ResponseBodyComplete = true; return c }(), context.Canceled, context.Canceled},
		{"request-body-incomplete", func() liveAttachmentIngressCapture { c := good; c.BodyComplete = false; return c }(), context.Canceled, context.Canceled},
		{"timeout", func() liveAttachmentIngressCapture {
			c := good
			c.StreamReadErrorKind = "context_deadline_exceeded"
			return c
		}(), context.DeadlineExceeded, context.DeadlineExceeded},
		{"unexpected-eof", func() liveAttachmentIngressCapture { c := good; c.StreamReadErrorKind = "unexpected_eof"; return c }(), io.ErrUnexpectedEOF, io.ErrUnexpectedEOF},
		{"other-read-error", func() liveAttachmentIngressCapture { c := good; c.StreamReadErrorKind = "other_read_error"; return c }(), errors.New("fixture"), errors.New("fixture")},
		{"downstream-active", good, nil, context.Canceled},
		{"outbound-active", good, context.Canceled, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			if liveAttachmentIngressSemanticCancellation(test.capture, test.downstream, test.outbound) {
				t.Fatal("partial, unknown, timeout, or contradictory stream failure was accepted")
			}
		})
	}
}

func TestAttachmentIngressCancellationRequiresStrictFinalFunctionCallTerminal(t *testing.T) {
	terminal := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"object\":\"response\",\"status\":\"completed\",\"model\":\"claude-haiku-5.5\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1},\"output\":[{\"type\":\"function_call\",\"status\":\"completed\",\"name\":\"Read\",\"call_id\":\"fixture-call\",\"arguments\":\"{\\\"file_path\\\":\\\"fixture.pdf\\\"}\"}]}}\n\n"
	incomplete := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"object\":\"response\",\"status\":\"incomplete\",\"model\":\"claude-haiku-5.5\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1},\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"output\":[]}}\n\n"
	for _, test := range []struct {
		name string
		tail string
	}{
		{name: "error-event", tail: "event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"fixture\"}}\n\n"},
		{name: "incomplete-terminal", tail: incomplete},
		{name: "partial-trailing-frame", tail: "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"object\":\"response\",\"status\":\"completed\"}"},
	} {
		t.Run(test.name, func(t *testing.T) {
			gate := newLiveServerToolGate(t, t.TempDir())
			ingress := newLiveAttachmentIngress(t, gate, "http://127.0.0.1:1", t.TempDir(), "codex")
			var calls atomic.Int32
			ingress.client.Transport = liveToolRoundTrip(func(request *http.Request) (*http.Response, error) {
				if calls.Add(1) != 1 {
					return nil, errors.New("unexpected local host request")
				}
				reader, writer := io.Pipe()
				go func() {
					if _, err := io.WriteString(writer, terminal); err != nil {
						return
					}
					if _, err := io.WriteString(writer, test.tail); err != nil {
						return
					}
					<-request.Context().Done()
					_ = writer.CloseWithError(request.Context().Err())
				}()
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader, ContentLength: -1}, nil
			})
			requestBody := `{"model":"claude-haiku-5.5","stream":true}`
			ctx, cancel := context.WithCancel(context.Background())
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, ingress.URL()+"/v1/responses", strings.NewReader(requestBody))
			if err != nil {
				t.Fatal(err)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			wantBody := terminal + test.tail
			forwarded := make([]byte, len(wantBody))
			if _, err := io.ReadFull(response.Body, forwarded); err != nil || string(forwarded) != wantBody {
				cancel()
				_ = response.Body.Close()
				t.Fatal("fixture terminal and trailing bytes were not fully forwarded")
			}
			cancel()
			_ = response.Body.Close()

			var captures []liveAttachmentIngressCapture
			var captureErr error
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				captures, captureErr = ingress.captures()
				if captureErr == nil && len(captures) == 1 && captures[0].ErrorClass == "local_host_response_read_error" {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if captureErr != nil || len(captures) != 1 {
				t.Fatal("cancelled response with trailing bytes was not captured")
			}
			capture := captures[0]
			if capture.StreamOutcome != "unconfirmed_stream_failure" || capture.StreamReadErrorKind != "context_canceled" || capture.ResponseBodyComplete || capture.StreamTerminalComplete || capture.StreamTerminalFunction || capture.StreamTerminalForwarded || capture.StreamTerminalBytes != 0 || capture.StreamContextAtTerminal != "" || capture.Response.Body != gate.redactedBody([]byte(wantBody)) {
				t.Fatal("a terminal followed by error, incomplete, or partial trailing data retained sticky terminal proof")
			}
			denials, failure := ingress.state()
			if denials != 0 || failure == nil {
				t.Fatal("invalid trailing stream was allowed to continue")
			}

			second, err := http.NewRequest(http.MethodPost, ingress.URL()+"/v1/responses", strings.NewReader(requestBody))
			if err != nil {
				t.Fatal(err)
			}
			secondResponse, err := http.DefaultClient.Do(second)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, secondResponse.Body)
			_ = secondResponse.Body.Close()
			if secondResponse.StatusCode != http.StatusForbidden || calls.Load() != 1 {
				t.Fatal("invalid trailing stream did not fail closed before the next local host dispatch")
			}
		})
	}
}

func TestAttachmentIngressPersistenceFailureStopsLocalForward(t *testing.T) {
	var forwarded int
	localHost := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		forwarded++
		writer.WriteHeader(http.StatusOK)
	}))
	defer localHost.Close()
	gate := newLiveServerToolGate(t, t.TempDir())
	ingress := newLiveAttachmentIngress(t, gate, localHost.URL, t.TempDir(), "claude")
	blockedDirectory := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedDirectory, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	ingress.directory = blockedDirectory
	request, err := http.NewRequest(http.MethodPost, ingress.URL()+"/v1/messages", strings.NewReader(`{"model":"gpt-6-luna"}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	denials, failure := ingress.state()
	if response.StatusCode != http.StatusBadGateway || forwarded != 0 || failure == nil || denials != 0 {
		t.Fatal("undurable original-client request was forwarded")
	}
	secondRequest, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ingress.URL()+"/v1/messages", strings.NewReader(`{"model":"gpt-6-luna"}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := http.DefaultClient.Do(secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	denials, _ = ingress.state()
	if second.StatusCode != http.StatusForbidden || forwarded != 0 || denials != 1 {
		t.Fatal("original-client capture failure did not stay fail-closed")
	}
}

func TestAttachmentIngressLocalConversionRefusalIsNotCaptureFailure(t *testing.T) {
	localHost := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = writer.Write([]byte(`{"error":{"type":"unsupported_feature"}}`))
	}))
	defer localHost.Close()
	gate := newLiveServerToolGate(t, t.TempDir())
	ingress := newLiveAttachmentIngress(t, gate, localHost.URL, t.TempDir(), "codex")
	request, err := http.NewRequest(http.MethodPost, ingress.URL()+"/v1/responses", strings.NewReader(`{"model":"claude-haiku-5.5"}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	denials, failure := ingress.state()
	captureFailed, refusalStatus := ingress.failureState()
	var captures []liveAttachmentIngressCapture
	var readErr error
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		captures, readErr = ingress.captures()
		if readErr == nil && len(captures) == 1 && captures[0].Response.Status == http.StatusUnprocessableEntity {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("local refusal: status=%d refusal=%d capture_failed=%t failure_present=%t read_error=%t denials=%d captures=%d", response.StatusCode, refusalStatus, captureFailed, failure != nil, readErr != nil, denials, len(captures))
	if len(captures) == 1 {
		t.Logf("local refusal capture: dispatched=%t complete=%t status=%d error_class=%s", captures[0].Dispatched, captures[0].BodyComplete, captures[0].Response.Status, captures[0].ErrorClass)
	}
	if response.StatusCode != http.StatusUnprocessableEntity || refusalStatus != http.StatusUnprocessableEntity || captureFailed || failure == nil || readErr != nil || denials != 0 || len(captures) != 1 || !captures[0].Dispatched || !captures[0].BodyComplete || captures[0].Response.Status != http.StatusUnprocessableEntity {
		t.Fatal("local conversion refusal was mislabeled or not retained")
	}
}

func TestAttachmentIngressOversizedRequestRetainsPartialCapture(t *testing.T) {
	var forwarded atomic.Int32
	localHost := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		forwarded.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer localHost.Close()
	gate := newLiveServerToolGate(t, t.TempDir())
	ingress := newLiveAttachmentIngress(t, gate, localHost.URL, t.TempDir(), "claude")
	request, err := http.NewRequest(http.MethodPost, ingress.URL()+"/v1/messages", strings.NewReader(strings.Repeat("x", (16<<20)+1)))
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	captures, err := ingress.captures()
	if err != nil {
		t.Fatal(err)
	}
	_, failure := ingress.state()
	if response.StatusCode != http.StatusRequestEntityTooLarge || forwarded.Load() != 0 || failure == nil || len(captures) != 1 {
		t.Fatal("oversized original-client request did not fail closed with one retained capture")
	}
	capture := captures[0]
	if capture.Dispatched || capture.BodyComplete || capture.ErrorClass != "original_client_request_unavailable" || capture.Response.Status != http.StatusRequestEntityTooLarge || len(capture.Request.Body) != 16<<20 {
		t.Fatal("oversized original-client request did not retain its bounded prefix and failure status")
	}
}

func TestAttachmentPacketTruncationStopsConcurrentDispatchBeforeEOF(t *testing.T) {
	t.Setenv("CPA_LIVE_CLIENT_ATTACHMENTS", "1")
	gate := newLiveServerToolGate(t, t.TempDir())
	gate.publicAPI, _ = url.Parse("https://api.githubcopilot.com")
	if err := gate.setPhaseCell("X", "claude-gpt-png"); err != nil {
		t.Fatal(err)
	}
	gate.setFreshClaim("inference", "X/claude-gpt-png")
	released := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(released) }) }
	defer release()
	var upstreamCalls atomic.Int32
	gate.client.Transport = liveToolRoundTrip(func(*http.Request) (*http.Response, error) {
		upstreamCalls.Add(1)
		reader, writer := io.Pipe()
		go func() {
			_, _ = io.WriteString(writer, strings.Repeat("x", (16<<20)+1))
			<-released
			_ = writer.Close()
		}()
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: reader, ContentLength: -1}, nil
	})
	request, err := http.NewRequest(http.MethodPost, gate.server.URL+"/responses", strings.NewReader(`{"model":"gpt-6-luna","reasoning":{"effort":"medium"}}`))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if _, err := io.CopyN(io.Discard, response.Body, 16<<20); err != nil {
		t.Fatal(err)
	}
	var captureErr error
	deadline := time.Now().Add(2 * time.Second)
	for captureErr == nil && time.Now().Before(deadline) {
		_, _, _, _, captureErr = gate.countsFor("X/claude-gpt-png")
		if captureErr == nil {
			time.Sleep(time.Millisecond)
		}
	}
	if captureErr == nil {
		t.Fatal("capture overflow was not made sticky while the upstream stream remained open")
	}
	if _, _, _, allowed := gate.reserve("/responses"); allowed || upstreamCalls.Load() != 1 {
		t.Fatal("concurrent inference escaped after capture overflow but before EOF")
	}
	release()
	additional, _ := io.Copy(io.Discard, response.Body)
	if additional != 0 {
		t.Fatal("over-limit upstream bytes were forwarded after capture failure")
	}
}

func TestAttachmentPacket401RecoveryResetsAfterSuccessfulToolTurn(t *testing.T) {
	t.Setenv("CPA_LIVE_CLIENT_ATTACHMENTS", "1")
	gate := newLiveServerToolGate(t, t.TempDir())
	gate.publicAPI, _ = url.Parse("https://api.githubcopilot.com")
	if err := gate.setPhaseCell("X", "claude-gpt-pdf"); err != nil {
		t.Fatal(err)
	}
	gate.setFreshClaim("inference", "X/claude-gpt-pdf")
	statuses := []int{http.StatusUnauthorized, http.StatusOK, http.StatusUnauthorized, http.StatusOK}
	var calls atomic.Int32
	gate.client.Transport = liveToolRoundTrip(func(*http.Request) (*http.Response, error) {
		index := int(calls.Add(1)) - 1
		if index >= len(statuses) {
			return nil, errors.New("unexpected upstream request")
		}
		return &http.Response{StatusCode: statuses[index], Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"status":"fixture"}`)), ContentLength: -1}, nil
	})
	for index, want := range statuses {
		request, err := http.NewRequest(http.MethodPost, gate.server.URL+"/responses", strings.NewReader(`{"model":"gpt-6-luna","reasoning":{"effort":"medium"}}`))
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("logical request %d status = %d, want %d", index+1, response.StatusCode, want)
		}
	}
	count, total, _, _, captureErr := gate.countsFor("X/claude-gpt-pdf")
	if count != 4 || total != 4 || captureErr != nil || calls.Load() != 4 {
		t.Fatal("distinct recovered tool turns were blocked or not durably counted")
	}
}

func TestAttachmentPacketStreamReadFailureClassesRemainNonAccepting(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{nil, "active"},
		{context.Canceled, "context_canceled"},
		{context.DeadlineExceeded, "context_deadline_exceeded"},
		{io.ErrUnexpectedEOF, "unexpected_eof"},
		{errors.New("fixture"), "other_read_error"},
	} {
		if got := liveSafeStreamReadErrorKind(test.err); got != test.want {
			t.Fatalf("safe stream read classification = %q, want %q", got, test.want)
		}
	}
}

func TestAttachmentPacketDownstreamCancelRequiresCompleteForwardedTerminal(t *testing.T) {
	terminal := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-6-luna\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1},\"output\":[{\"type\":\"function_call\",\"status\":\"completed\",\"name\":\"Read\",\"call_id\":\"call-a\",\"arguments\":\"{\\\"file_path\\\":\\\"fixture.png\\\"}\"}]}}\n\n"
	if !liveCompleteResponseTerminal([]byte(terminal), "gpt-6-luna") || !liveCompleteResponseTerminal([]byte(terminal+"\n \n\n"), "gpt-6-luna") || liveCompleteResponseTerminal([]byte(strings.TrimSuffix(terminal, "\n"))) || liveCompleteResponseTerminal([]byte(strings.Replace(terminal, `"status":"completed"`, `"status":"incomplete"`, 1))) {
		t.Fatal("complete framed successful function-call terminal was not distinguished from a partial or incomplete stream")
	}
	for _, changed := range []string{
		strings.Replace(terminal, `"response":{"object"`, `"error":{"message":"fixture"},"response":{"object"`, 1),
		strings.Replace(terminal, `"status":"completed","model"`, `"status":"completed","error":{"message":"fixture"},"model"`, 1),
		strings.Replace(terminal, `"status":"completed","model"`, `"status":"completed","incomplete_details":{"reason":"max_output_tokens"},"model"`, 1),
		strings.Replace(terminal, `"arguments":"{\"file_path\":\"fixture.png\"}"`, `"arguments":"null"`, 1),
		strings.Replace(terminal, `"model":"gpt-6-luna"`, `"model":"other"`, 1),
	} {
		if liveCompleteResponseTerminal([]byte(changed), "gpt-6-luna") {
			t.Fatal("contradictory completed terminal was accepted")
		}
	}
	good := liveServerToolCapture{ErrorClass: "upstream_response_read_error", StreamReadErrorKind: "context_canceled", StreamTerminalComplete: true, StreamTerminalForwarded: true, StreamTerminalBytes: len(terminal), StreamForwardedBytes: len(terminal), StreamFlushedBytes: len(terminal), StreamContextAtTerminal: "active"}
	if !liveSemanticDownstreamCancellation(good, context.Canceled, context.Canceled) {
		t.Fatal("confirmed downstream cancellation after a forwarded terminal was rejected")
	}
	good.StreamOutcome = "semantic_complete_downstream_cancelled"
	good.DownstreamContextState = "context_canceled"
	good.OutboundContextState = "context_canceled"
	good.Response.Body = terminal
	good.Response.Status = 200
	good.PublicRequest.URL = "https://api.githubcopilot.com/responses"
	if !liveCaptureStreamProven(good, "gpt-6-luna") {
		t.Fatal("captured confirmed downstream cancellation was rejected")
	}
	contradictory := good
	contradictory.CaptureTruncated = true
	if liveCaptureStreamProven(contradictory, "gpt-6-luna") {
		t.Fatal("contradictory truncated capture was accepted")
	}
	contradictory = good
	contradictory.StreamReadErrorKind = "context_deadline_exceeded"
	if liveCaptureStreamProven(contradictory, "gpt-6-luna") {
		t.Fatal("contradictory timeout capture was accepted")
	}
	contradictory = good
	contradictory.StreamTerminalForwarded = false
	if liveCaptureStreamProven(contradictory, "gpt-6-luna") {
		t.Fatal("contradictory unforwarded terminal was accepted")
	}
	for _, test := range []struct {
		name       string
		capture    liveServerToolCapture
		downstream error
		outbound   error
	}{
		{"early-cancel", liveServerToolCapture{ErrorClass: good.ErrorClass, StreamReadErrorKind: good.StreamReadErrorKind, StreamTerminalForwarded: true}, context.Canceled, context.Canceled},
		{"timeout", liveServerToolCapture{ErrorClass: good.ErrorClass, StreamReadErrorKind: "context_deadline_exceeded", StreamTerminalComplete: true, StreamTerminalForwarded: true}, context.DeadlineExceeded, context.DeadlineExceeded},
		{"truncated", liveServerToolCapture{ErrorClass: good.ErrorClass, StreamReadErrorKind: good.StreamReadErrorKind, StreamTerminalComplete: true, StreamTerminalForwarded: true, CaptureTruncated: true}, context.Canceled, context.Canceled},
		{"not-forwarded", liveServerToolCapture{ErrorClass: good.ErrorClass, StreamReadErrorKind: good.StreamReadErrorKind, StreamTerminalComplete: true}, context.Canceled, context.Canceled},
		{"downstream-active", good, nil, context.Canceled},
		{"outbound-active", good, context.Canceled, nil},
	} {
		if liveSemanticDownstreamCancellation(test.capture, test.downstream, test.outbound) {
			t.Fatalf("%s was accepted as a confirmed downstream cancellation", test.name)
		}
	}
}
