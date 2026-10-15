//go:build !windows

package integration

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/redact"
	"golang.org/x/net/http2"
)

const liveMoonTask = "Use web search to find NASA's official Moon facts page. Give one fact about the Moon and the source URL from the search result. If web search is unavailable, report that failure."

const liveServerToolTransportCoverage = "http1_only_one_use_transport_http2_parity_unverified"

var liveServerToolPhaseCells = map[string]struct{}{
	"A/codex":                                  {},
	"A/claude":                                 {},
	"B/gemini-chat-search":                     {},
	"B/gpt-responses-search":                   {},
	"B/claude-web-fetch":                       {},
	"B/claude-code-execution":                  {},
	"B/gpt-code-interpreter":                   {},
	"S/gpt-responses-search":                   {},
	"S/claude-code-execution":                  {},
	"C/gpt-responses-search":                   {},
	"C/claude-code-execution":                  {},
	"S-native-identity/gpt-responses-search":   {},
	"C-native-identity/gpt-responses-search":   {},
	"C-captured-identity/gpt-responses-search": {},
}

type liveServerToolGate struct {
	mu                             sync.Mutex
	server                         *httptest.Server
	client                         *http.Client
	directory                      string
	ledgerPath                     string
	secrets                        []string
	publicAPI                      *url.URL
	phaseCell                      string
	counts                         map[string]int
	inferenceCount                 int
	authCount                      int
	catalogCount                   int
	deniedCount                    int
	captureCount                   int
	captureErr                     error
	freshClaims                    map[string]bool
	attachmentFixtures             map[string]string
	freshOperationArmed            bool
	freshCatalogUnauthorizedSeen   bool
	freshInferenceUnauthorizedSeen bool
}

type liveDispatchLedger struct {
	Counts    map[string]int  `json:"counts"`
	Inference int             `json:"inference"`
	Auth      int             `json:"auth"`
	Catalog   int             `json:"catalog"`
	Frozen    map[string]bool `json:"frozen"`
}

type liveServerToolCapture struct {
	Sequence                int         `json:"sequence"`
	Category                string      `json:"category"`
	PhaseCell               string      `json:"phase_cell,omitempty"`
	Dispatched              bool        `json:"dispatched"`
	OriginalPublicOrigin    string      `json:"original_public_origin,omitempty"`
	PublicTransportCoverage string      `json:"public_transport_coverage,omitempty"`
	Request                 liveHTTPLog `json:"test_hop_request"`
	PublicRequest           liveHTTPLog `json:"public_request"`
	Response                liveHTTPLog `json:"public_response"`
	LocalResponse           liveHTTPLog `json:"test_hop_response"`
	ErrorClass              string      `json:"error_class,omitempty"`
	StreamReadErrorKind     string      `json:"stream_read_error_kind,omitempty"`
	StreamReadErrorDetail   string      `json:"stream_read_error_detail,omitempty"`
	DownstreamContextState  string      `json:"downstream_context_state,omitempty"`
	OutboundContextState    string      `json:"outbound_context_state,omitempty"`
	StreamTerminalComplete  bool        `json:"stream_terminal_complete,omitempty"`
	StreamTerminalForwarded bool        `json:"stream_terminal_forwarded,omitempty"`
	StreamTerminalBytes     int         `json:"stream_terminal_bytes,omitempty"`
	StreamForwardedBytes    int         `json:"stream_forwarded_bytes,omitempty"`
	StreamFlushedBytes      int         `json:"stream_flushed_bytes,omitempty"`
	StreamContextAtTerminal string      `json:"stream_context_at_terminal,omitempty"`
	StreamOutcome           string      `json:"stream_outcome,omitempty"`
	CaptureTruncated        bool        `json:"capture_truncated,omitempty"`
}

type liveHTTPLog struct {
	Method              string      `json:"method,omitempty"`
	URL                 string      `json:"url,omitempty"`
	Host                string      `json:"host,omitempty"`
	Proto               string      `json:"proto,omitempty"`
	NegotiatedProtocol  string      `json:"negotiated_protocol,omitempty"`
	ObservedHeaders     http.Header `json:"observed_header_fields,omitempty"`
	HeaderTraceCoverage string      `json:"header_trace_coverage,omitempty"`
	FramingCoverage     string      `json:"framing_coverage,omitempty"`
	Headers             http.Header `json:"headers,omitempty"`
	ContentEncoding     string      `json:"content_encoding,omitempty"`
	ContentLength       int64       `json:"content_length,omitempty"`
	TransferEncoding    []string    `json:"transfer_encoding,omitempty"`
	Status              int         `json:"status,omitempty"`
	Body                string      `json:"body,omitempty"`
}

func newLiveServerToolGate(t *testing.T, directory string, secrets ...string) *liveServerToolGate {
	return newLiveServerToolGateWithLedger(t, directory, directory, secrets...)
}

func newLiveServerToolGateWithLedger(t *testing.T, directory, ledgerDirectory string, secrets ...string) *liveServerToolGate {
	t.Helper()
	t.Log("Public dispatches use HTTP/1.1 with a fresh transport and connection per request; HTTP/2 parity remains unverified.")
	for _, path := range []string{directory, ledgerDirectory} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal("could not create private server-tool diagnostics directory")
		}
		if err := os.Chmod(path, 0700); err != nil {
			t.Fatal("could not restrict private server-tool diagnostics directory")
		}
	}
	gate := &liveServerToolGate{
		client: &http.Client{
			Timeout:       95 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Transport:     liveOneShotTransport{},
		},
		directory:          directory,
		ledgerPath:         filepath.Join(ledgerDirectory, "dispatch-ledger.json"),
		secrets:            append([]string{liveCopilotClientKey}, secrets...),
		counts:             make(map[string]int),
		freshClaims:        make(map[string]bool),
		attachmentFixtures: make(map[string]string),
	}
	if err := gate.withLedger(func(ledger *liveDispatchLedger) error { return nil }); err != nil {
		t.Fatal("private dispatch ledger could not be initialized")
	}
	gate.server = httptest.NewServer(http.HandlerFunc(gate.serveHTTP))
	t.Cleanup(gate.server.Close)
	return gate
}

func (g *liveServerToolGate) setPhaseCell(phase, cell string) error {
	key := phase + "/" + cell
	if _, ok := liveServerToolPhaseCells[key]; !ok && !liveAttachmentGateCell(key) && !liveDependencyMatrixCell(key) && !liveOriginalCLIClaimCell(key) && !liveOriginalInterruptCell(key) && !liveOriginalHTTPSImageCell(key) {
		return errors.New("unknown server-tool phase cell")
	}
	g.mu.Lock()
	g.phaseCell = key
	g.mu.Unlock()
	return nil
}

func (g *liveServerToolGate) setAttachmentFixture(cell, path string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.attachmentFixtures == nil {
		g.attachmentFixtures = make(map[string]string)
	}
	g.attachmentFixtures[cell] = path
}

func liveAttachmentGateCell(key string) bool {
	phase, name, ok := strings.Cut(key, "/")
	if !ok {
		return false
	}
	for _, candidate := range liveAttachmentCases() {
		if candidate.cell != name {
			continue
		}
		if phase == "X" {
			return true
		}
		if strings.HasPrefix(phase, "X-") {
			derived, err := liveAttachmentOperationCell(candidate, strings.TrimPrefix(phase, "X-"))
			return err == nil && derived == key
		}
	}
	return false
}

func (g *liveServerToolGate) setFreshClaim(category, cell string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.freshClaims[category+"/"+cell] = true
	if category == "inference" {
		g.freshInferenceUnauthorizedSeen = false
	}
}

func (g *liveServerToolGate) clearFreshInferenceClaim(cell string) {
	g.mu.Lock()
	delete(g.freshClaims, "inference/"+cell)
	g.freshInferenceUnauthorizedSeen = false
	g.mu.Unlock()
}

func (g *liveServerToolGate) freshUnauthorizedRepeated(category string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if category == "catalog" {
		repeated := g.freshCatalogUnauthorizedSeen
		g.freshCatalogUnauthorizedSeen = true
		return repeated
	}
	repeated := g.freshInferenceUnauthorizedSeen
	g.freshInferenceUnauthorizedSeen = true
	return repeated
}

func (g *liveServerToolGate) freshCatalogRecovered() {
	g.mu.Lock()
	g.freshCatalogUnauthorizedSeen = false
	g.mu.Unlock()
}

func (g *liveServerToolGate) freshInferenceRecovered() {
	g.mu.Lock()
	g.freshInferenceUnauthorizedSeen = false
	g.mu.Unlock()
}

func (g *liveServerToolGate) armFreshOperation() {
	g.mu.Lock()
	g.freshOperationArmed = true
	g.mu.Unlock()
}

func (g *liveServerToolGate) stopFreshOperationOnFailure(category string) {
	if os.Getenv("CPA_LIVE_CLIENT_ATTACHMENTS") != "1" || category != "auth" && category != "catalog" {
		return
	}
	g.mu.Lock()
	if g.captureErr == nil {
		g.captureErr = errors.New("fresh SDK discovery failed; automatic retry denied")
	}
	g.mu.Unlock()
}

func (g *liveServerToolGate) reserve(path string) (string, string, string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.captureErr != nil {
		g.deniedCount++
		return "unknown", g.phaseCell, "", false
	}
	if g.publicAPI != nil {
		prefix := strings.TrimRight(g.publicAPI.Path, "/")
		if prefix != "" && strings.HasPrefix(path, prefix+"/") {
			path = strings.TrimPrefix(path, prefix)
		}
	}
	category, key, origin, allowed := "unknown", g.phaseCell, "", false
	if os.Getenv("CPA_LIVE_CLIENT_ATTACHMENTS") == "1" {
		claimCategory := "unknown"
		switch path {
		case "/copilot_internal/v2/token":
			claimCategory = "auth"
		case "/models":
			claimCategory = "catalog"
		case "/chat/completions", "/responses", "/v1/messages":
			claimCategory = "inference"
		}
		claimKey := claimCategory + "/"
		if claimCategory == "inference" {
			claimKey += key
		}
		if claimCategory == "auth" || claimCategory == "catalog" {
			if !g.freshOperationArmed {
				g.deniedCount++
				return claimCategory, key, "", false
			}
		} else {
			if !g.freshClaims[claimKey] {
				g.deniedCount++
				return claimCategory, key, "", false
			}
		}
	}
	err := g.withLedger(func(ledger *liveDispatchLedger) error {
		switch path {
		case "/copilot_internal/v2/token":
			category = "auth"
			ledger.Auth++
			origin = "https://api.github.com"
			allowed = true
		case "/models":
			category = "catalog"
			if g.publicAPI != nil {
				ledger.Catalog++
				origin = g.publicAPI.Scheme + "://" + g.publicAPI.Host
				allowed = true
			}
		case "/chat/completions", "/responses", "/v1/messages":
			category = "inference"
			_, configured := liveServerToolPhaseCells[key]
			if liveAttachmentGateCell(key) || liveDependencyMatrixCell(key) || liveOriginalCLIClaimCell(key) || liveOriginalInterruptCell(key) || liveOriginalHTTPSImageCell(key) {
				configured = true
			}
			if configured && !ledger.Frozen[key] && g.publicAPI != nil {
				ledger.Counts[key]++
				ledger.Inference++
				origin = g.publicAPI.Scheme + "://" + g.publicAPI.Host
				allowed = true
			}
		}
		return nil
	})
	if err != nil {
		g.captureErr = errors.New("private dispatch ledger could not be persisted")
	}
	if err != nil || !allowed {
		g.deniedCount++
		return category, key, "", false
	}
	return category, key, origin, true
}

func (g *liveServerToolGate) withLedger(update func(*liveDispatchLedger) error) error {
	lock, err := os.OpenFile(g.ledgerPath+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = lock.Chmod(0600); err != nil {
		return err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	data, err := os.ReadFile(g.ledgerPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(data) > 1<<20 {
		return errors.New("private dispatch ledger exceeded size limit")
	}
	ledger := liveDispatchLedger{Counts: make(map[string]int), Frozen: make(map[string]bool)}
	if len(data) > 0 {
		if err = json.Unmarshal(data, &ledger); err != nil {
			return err
		}
		if ledger.Counts == nil {
			ledger.Counts = make(map[string]int)
		}
		if ledger.Frozen == nil {
			ledger.Frozen = make(map[string]bool)
		}
	}
	if err = update(&ledger); err != nil {
		return err
	}
	data, err = json.Marshal(ledger)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(g.ledgerPath), ".dispatch-ledger-")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	if err = temporary.Chmod(0600); err != nil {
		return err
	}
	if _, err = temporary.Write(data); err != nil {
		return err
	}
	if err = temporary.Sync(); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	if err = os.Rename(temporary.Name(), g.ledgerPath); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(g.ledgerPath))
	if err != nil {
		return err
	}
	defer directory.Close()
	if err = directory.Sync(); err != nil {
		return err
	}
	g.counts, g.inferenceCount, g.authCount, g.catalogCount = ledger.Counts, ledger.Inference, ledger.Auth, ledger.Catalog
	return nil
}

func (g *liveServerToolGate) freezeCell(cell string) {
	_, configured := liveServerToolPhaseCells[cell]
	if !configured && !strings.HasPrefix(cell, "A/") && !strings.HasPrefix(cell, "B/") && !liveAttachmentGateCell(cell) && !liveDependencyMatrixCell(cell) && !liveOriginalCLIClaimCell(cell) && !liveOriginalInterruptCell(cell) && !liveOriginalHTTPSImageCell(cell) {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.withLedger(func(ledger *liveDispatchLedger) error { ledger.Frozen[cell] = true; return nil }); err != nil {
		g.captureErr = errors.New("private dispatch ledger could not be frozen")
	}
}

func (g *liveServerToolGate) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	const maxBody = 16 << 20
	body, err := io.ReadAll(io.LimitReader(request.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		g.writeLocalFailure(writer, request, body, "request_body_unavailable", http.StatusRequestEntityTooLarge)
		return
	}
	g.mu.Lock()
	preparedCell := g.phaseCell
	preparedFixture := g.attachmentFixtures[preparedCell]
	g.mu.Unlock()
	if liveAttachmentGateCell(preparedCell) && !liveAttachmentPreparedPublicRequestForFixture(preparedCell, request.URL.Path, body, preparedFixture) {
		g.mu.Lock()
		if g.captureErr == nil {
			g.captureErr = errors.New("original client attempted an unprepared model, effort, or endpoint")
		}
		g.mu.Unlock()
		g.writeLocalFailure(writer, request, body, "attachment_request_unprepared", http.StatusBadRequest)
		return
	}
	if preparedCell == liveOriginalClaudeURLImageKey && request.URL.Path != "/copilot_internal/v2/token" && request.URL.Path != "/models" && (request.URL.Path != "/v1/messages" || !originalClaudeURLImageRequest(body, os.Getenv("CPA_LIVE_IMAGE_URL"))) {
		g.writeLocalFailure(writer, request, body, "original_url_image_request_unprepared", http.StatusBadRequest)
		return
	}
	category, phaseCell, origin, allowed := g.reserve(request.URL.Path)
	if !allowed {
		g.writeLocalFailure(writer, request, body, "gate_dispatch_denied", http.StatusTooManyRequests)
		return
	}
	upstreamURL := origin + request.URL.RequestURI()
	upstreamRequest, err := http.NewRequestWithContext(request.Context(), request.Method, upstreamURL, bytes.NewReader(body))
	if err != nil {
		g.freezeCell(phaseCell)
		g.writeLocalFailure(writer, request, body, "upstream_request_invalid", http.StatusBadGateway)
		return
	}
	upstreamRequest.Header = request.Header.Clone()
	removeHopHeaders(upstreamRequest.Header)
	upstreamRequest.Host = upstreamRequest.URL.Host
	upstreamRequest.TransferEncoding = append([]string(nil), request.TransferEncoding...)
	capture := liveServerToolCapture{Category: category, PhaseCell: phaseCell, Dispatched: true, OriginalPublicOrigin: origin, PublicTransportCoverage: liveServerToolTransportCoverage, Request: g.requestLog(request, request.URL.String(), body, category), PublicRequest: g.requestLog(upstreamRequest, upstreamURL, body, category)}
	capture.Request.FramingCoverage = "parsed_from_test_hop_request"
	capture.PublicRequest.FramingCoverage = "header_trace_fields_only"
	var traceMu sync.Mutex
	observed := make(http.Header)
	negotiated := "unobserved"
	upstreamRequest = upstreamRequest.WithContext(httptrace.WithClientTrace(upstreamRequest.Context(), &httptrace.ClientTrace{
		WroteHeaderField: func(name string, values []string) {
			traceMu.Lock()
			defer traceMu.Unlock()
			for _, value := range values {
				observed.Add(name, g.redactedHeaders(http.Header{name: []string{value}}).Get(name))
			}
		},
		TLSHandshakeDone: func(state tls.ConnectionState, _ error) {
			traceMu.Lock()
			negotiated = state.NegotiatedProtocol
			traceMu.Unlock()
		},
	}))
	response, err := g.client.Do(upstreamRequest)
	traceMu.Lock()
	capture.PublicRequest.ObservedHeaders = observed.Clone()
	capture.PublicRequest.NegotiatedProtocol = negotiated
	if len(observed) == 0 {
		capture.PublicRequest.HeaderTraceCoverage = "unobserved"
	} else {
		capture.PublicRequest.HeaderTraceCoverage = "reported_fields_only"
	}
	traceMu.Unlock()
	if err != nil {
		capture.ErrorClass = "upstream_transport_error"
		g.record(capture)
		g.stopFreshOperationOnFailure(category)
		g.freezeCell(phaseCell)
		g.writeJSONFailure(writer, "upstream_transport_error", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	capture.Response = g.responseLog(response, nil, category)
	if category == "inference" {
		if os.Getenv("CPA_LIVE_CLIENT_ATTACHMENTS") == "1" && response.StatusCode == http.StatusUnauthorized {
			if g.freshUnauthorizedRepeated(category) {
				g.freezeCell(phaseCell)
			}
		} else if os.Getenv("CPA_LIVE_CLIENT_ATTACHMENTS") == "1" && response.StatusCode >= 200 && response.StatusCode < 300 {
			g.freshInferenceRecovered()
		} else if os.Getenv("CPA_LIVE_CLIENT_ATTACHMENTS") != "1" && response.StatusCode >= 400 && response.StatusCode != http.StatusUnauthorized || os.Getenv("CPA_LIVE_CLIENT_ATTACHMENTS") == "1" && (response.StatusCode < 200 || response.StatusCode >= 300) {
			g.freezeCell(phaseCell)
		}
	}
	if category != "inference" {
		if os.Getenv("CPA_LIVE_CLIENT_ATTACHMENTS") == "1" && category == "catalog" && response.StatusCode == http.StatusUnauthorized {
			if g.freshUnauthorizedRepeated(category) {
				g.stopFreshOperationOnFailure(category)
			}
		} else if response.StatusCode < 200 || response.StatusCode >= 300 {
			g.stopFreshOperationOnFailure(category)
		} else if category == "catalog" && os.Getenv("CPA_LIVE_CLIENT_ATTACHMENTS") == "1" {
			g.freshCatalogRecovered()
		}
		responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
		capture.Response = g.responseLog(response, responseBody, category)
		if readErr != nil || len(responseBody) > maxBody {
			capture.ErrorClass = "upstream_response_unavailable"
			capture.CaptureTruncated = len(responseBody) > maxBody
			g.record(capture)
			g.stopFreshOperationOnFailure(category)
			g.writeJSONFailure(writer, "upstream_response_unavailable", http.StatusBadGateway)
			return
		}
		forwardBody := responseBody
		if category == "auth" && response.StatusCode >= 200 && response.StatusCode < 300 {
			forwardBody, err = g.rewriteTokenEndpoint(responseBody)
			if err != nil {
				capture.ErrorClass = "token_endpoint_invalid"
				g.record(capture)
				g.stopFreshOperationOnFailure(category)
				g.writeJSONFailure(writer, "token_endpoint_invalid", http.StatusBadGateway)
				return
			}
		}
		copyHTTPHeaders(writer.Header(), response.Header)
		removeHopHeaders(writer.Header())
		writer.Header().Del("Content-Length")
		writer.WriteHeader(response.StatusCode)
		capture.LocalResponse = liveHTTPLog{Proto: request.Proto, Status: response.StatusCode, Headers: g.redactedHeaders(writer.Header()), ContentLength: int64(len(forwardBody)), ContentEncoding: writer.Header().Get("Content-Encoding"), FramingCoverage: "outgoing_framing_unobserved"}
		_, err = writer.Write(forwardBody)
		if err != nil {
			capture.ErrorClass = "test_hop_write_error"
			g.stopFreshOperationOnFailure(category)
		}
		g.record(capture)
		return
	}
	copyHTTPHeaders(writer.Header(), response.Header)
	removeHopHeaders(writer.Header())
	writer.Header().Del("Content-Length")
	writer.WriteHeader(response.StatusCode)
	capture.LocalResponse = liveHTTPLog{Proto: request.Proto, Status: response.StatusCode, Headers: g.redactedHeaders(writer.Header()), ContentLength: -1, ContentEncoding: writer.Header().Get("Content-Encoding"), FramingCoverage: "outgoing_framing_unobserved"}
	var retained bytes.Buffer
	terminalTracker := liveResponseTerminalTracker{expectedModel: liveAttachmentExpectedModel(phaseCell)}
	chunk := make([]byte, 32<<10)
	streamEndedCleanly := false
	forwardedBytes, flushedBytes := 0, 0
	for {
		n, readErr := response.Body.Read(chunk)
		if n > 0 {
			remaining := maxBody - retained.Len()
			forwardN := min(n, remaining)
			if forwardN > 0 {
				retained.Write(chunk[:forwardN])
				terminalTracker.Feed(chunk[:forwardN])
			}
			if n > remaining {
				capture.CaptureTruncated = true
				capture.ErrorClass = "capture_truncated"
				g.mu.Lock()
				if g.captureErr == nil {
					g.captureErr = errors.New("private gate capture was truncated")
				}
				g.mu.Unlock()
			}
			if forwardN > 0 {
				written, writeErr := writer.Write(chunk[:forwardN])
				forwardedBytes += written
				if writeErr != nil || written != forwardN {
					capture.ErrorClass = "test_hop_write_error"
					break
				}
				if flusher, ok := writer.(http.Flusher); ok {
					flusher.Flush()
					flushedBytes = forwardedBytes
				}
				if terminalTracker.Complete() {
					capture.StreamTerminalComplete = true
					if capture.StreamTerminalBytes == 0 {
						capture.StreamTerminalBytes = retained.Len()
						capture.StreamContextAtTerminal = liveSafeStreamReadErrorKind(request.Context().Err())
					}
					if capture.StreamContextAtTerminal == "active" && flushedBytes >= capture.StreamTerminalBytes && !capture.CaptureTruncated {
						capture.StreamTerminalForwarded = true
					}
				}
			}
			if capture.CaptureTruncated {
				break
			}
		}
		if readErr == io.EOF {
			streamEndedCleanly = true
			break
		}
		if readErr != nil {
			capture.ErrorClass = "upstream_response_read_error"
			capture.StreamReadErrorKind = liveSafeStreamReadErrorKind(readErr)
			capture.StreamReadErrorDetail = g.redactedBody([]byte(fmt.Sprintf("%T: %s", readErr, readErr.Error())))
			capture.DownstreamContextState = liveSafeStreamReadErrorKind(request.Context().Err())
			capture.OutboundContextState = liveSafeStreamReadErrorKind(upstreamRequest.Context().Err())
			break
		}
	}
	capture.StreamTerminalComplete = terminalTracker.Complete()
	capture.StreamForwardedBytes = forwardedBytes
	capture.StreamFlushedBytes = flushedBytes
	capture.StreamTerminalForwarded = capture.StreamTerminalForwarded && capture.StreamTerminalComplete && forwardedBytes >= capture.StreamTerminalBytes && flushedBytes >= capture.StreamTerminalBytes && capture.ErrorClass != "test_hop_write_error" && !capture.CaptureTruncated
	switch {
	case streamEndedCleanly && capture.ErrorClass == "" && !capture.CaptureTruncated:
		capture.StreamOutcome = "clean_eof"
	case liveSemanticDownstreamCancellation(capture, request.Context().Err(), upstreamRequest.Context().Err()):
		capture.StreamOutcome = "semantic_complete_downstream_cancelled"
	default:
		capture.StreamOutcome = "unconfirmed_stream_failure"
	}
	capture.Response = g.responseLog(response, retained.Bytes(), category)
	g.record(capture)
}

type liveResponseTerminalTracker struct {
	expectedModel string
	frame         []byte
	lastTwo       [2]byte
	lastTwoCount  int
	pendingCR     bool
	terminal      bool
}

// Feed keeps only the current SSE frame and checks it when its delimiter
// arrives. This makes terminal evidence linear in the streamed bytes while
// preserving the whole response only for the bounded capture itself.
func (tracker *liveResponseTerminalTracker) Feed(chunk []byte) {
	if tracker.pendingCR && len(chunk) > 0 {
		tracker.pendingCR = false
		if chunk[0] == '\n' {
			tracker.feedNormalizedBytes(chunk[:1])
			chunk = chunk[1:]
		} else {
			tracker.feedNormalizedByte('\r')
		}
	}
	for len(chunk) > 0 {
		carriageReturn := bytes.IndexByte(chunk, '\r')
		if carriageReturn < 0 {
			tracker.feedNormalizedBytes(chunk)
			return
		}
		tracker.feedNormalizedBytes(chunk[:carriageReturn])
		chunk = chunk[carriageReturn+1:]
		if len(chunk) == 0 {
			tracker.pendingCR = true
			return
		}
		if chunk[0] == '\n' {
			tracker.feedNormalizedBytes(chunk[:1])
			chunk = chunk[1:]
		} else {
			tracker.feedNormalizedByte('\r')
		}
	}
}

func (tracker *liveResponseTerminalTracker) feedNormalizedByte(value byte) {
	var single [1]byte
	single[0] = value
	tracker.feedNormalizedBytes(single[:])
}

func (tracker *liveResponseTerminalTracker) feedNormalizedBytes(chunk []byte) {
	for len(chunk) > 0 {
		if len(tracker.frame) > 0 && tracker.frame[len(tracker.frame)-1] == '\n' && chunk[0] == '\n' {
			tracker.frame = append(tracker.frame, '\n')
			tracker.updateLastTwo(chunk[:1])
			tracker.finishFrame()
			chunk = chunk[1:]
			continue
		}

		frameEnd := bytes.Index(chunk, []byte("\n\n"))
		if frameEnd < 0 {
			tracker.frame = append(tracker.frame, chunk...)
			tracker.updateLastTwo(chunk)
			return
		}
		frameEnd += 2
		tracker.frame = append(tracker.frame, chunk[:frameEnd]...)
		tracker.updateLastTwo(chunk[:frameEnd])
		tracker.finishFrame()
		chunk = chunk[frameEnd:]
	}
}

func (tracker *liveResponseTerminalTracker) updateLastTwo(chunk []byte) {
	switch len(chunk) {
	case 0:
		return
	case 1:
		if tracker.lastTwoCount == 0 {
			tracker.lastTwo[0] = chunk[0]
			tracker.lastTwoCount = 1
		} else if tracker.lastTwoCount == 1 {
			tracker.lastTwo[1] = chunk[0]
			tracker.lastTwoCount = 2
		} else {
			tracker.lastTwo[0] = tracker.lastTwo[1]
			tracker.lastTwo[1] = chunk[0]
		}
	default:
		copy(tracker.lastTwo[:], chunk[len(chunk)-len(tracker.lastTwo):])
		tracker.lastTwoCount = len(tracker.lastTwo)
	}
}

func (tracker *liveResponseTerminalTracker) finishFrame() {
	frame := tracker.frame
	if len(bytes.TrimSpace(frame[:len(frame)-2])) > 0 {
		tracker.terminal = liveCompleteResponseTerminal(frame, tracker.expectedModel)
	}
	tracker.frame = frame[:0]
}

func (tracker *liveResponseTerminalTracker) Complete() bool {
	return !tracker.pendingCR && tracker.terminal && tracker.lastTwoCount == len(tracker.lastTwo) && tracker.lastTwo[0] == '\n' && tracker.lastTwo[1] == '\n'
}

func TestLiveResponseTerminalTrackerMatchesFinalFrameAcrossChunkBoundaries(t *testing.T) {
	terminal := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-6-luna\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1},\"output\":[{\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n"
	progress := "event: response.in_progress\ndata: {\"type\":\"response.in_progress\"}\n\n"
	cases := []struct {
		name          string
		body          string
		expectedModel string
		want          bool
	}{
		{name: "terminal", body: terminal, expectedModel: "gpt-6-luna", want: true},
		{name: "terminal with CRLF", body: strings.ReplaceAll(terminal, "\n", "\r\n"), expectedModel: "gpt-6-luna", want: true},
		{name: "terminal then blank frames", body: terminal + "\n \n\n", expectedModel: "gpt-6-luna", want: true},
		{name: "final nonterminal frame", body: terminal + progress, expectedModel: "gpt-6-luna", want: false},
		{name: "final partial nonterminal frame", body: terminal + "event: response.in_progress", expectedModel: "gpt-6-luna", want: false},
		{name: "later terminal frame", body: progress + terminal, expectedModel: "gpt-6-luna", want: true},
		{name: "unexpected model", body: terminal, expectedModel: "gpt-6-astra", want: false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			want := liveCompleteResponseTerminal([]byte(test.body), test.expectedModel)
			if want != test.want {
				t.Fatalf("reference terminal result = %t, want %t", want, test.want)
			}
			body := []byte(test.body)
			for split := 0; split <= len(body); split++ {
				var tracker liveResponseTerminalTracker
				tracker.expectedModel = test.expectedModel
				tracker.Feed(body[:split])
				tracker.Feed(body[split:])
				if got := tracker.Complete(); got != want {
					t.Fatalf("split at byte %d terminal result = %t, want %t", split, got, want)
				}
			}
			var fragmented liveResponseTerminalTracker
			fragmented.expectedModel = test.expectedModel
			for _, value := range body {
				fragmented.Feed([]byte{value})
			}
			if got := fragmented.Complete(); got != want {
				t.Fatalf("one-byte chunks terminal result = %t, want %t", got, want)
			}
		})
	}
}

func liveCompleteResponseTerminal(body []byte, expectedModel ...string) bool {
	normalized := bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
	if !bytes.HasSuffix(normalized, []byte("\n\n")) {
		return false
	}
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
	var eventName, data string
	for _, line := range bytes.Split(frames[lastFrame], []byte("\n")) {
		if bytes.HasPrefix(line, []byte("event:")) {
			eventName = strings.TrimSpace(string(bytes.TrimPrefix(line, []byte("event:"))))
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			data = strings.TrimSpace(string(bytes.TrimPrefix(line, []byte("data:"))))
		}
	}
	if eventName != "response.completed" || data == "" {
		return false
	}
	var event map[string]any
	if json.Unmarshal([]byte(data), &event) != nil || event["type"] != "response.completed" || event["error"] != nil {
		return false
	}
	response, _ := event["response"].(map[string]any)
	usage, _ := response["usage"].(map[string]any)
	if response["object"] != "response" || response["status"] != "completed" || response["error"] != nil || response["incomplete_details"] != nil || attachmentString(response, "model") == "" || !attachmentTokenCount(usage["input_tokens"], false) || !attachmentTokenCount(usage["output_tokens"], true) {
		return false
	}
	if len(expectedModel) > 0 && expectedModel[0] != "" && response["model"] != expectedModel[0] {
		return false
	}
	output, _ := response["output"].([]any)
	meaningful := false
	for _, raw := range output {
		item, _ := raw.(map[string]any)
		switch item["type"] {
		case "reasoning":
			continue
		case "function_call":
			var arguments map[string]any
			if item["status"] != "completed" || attachmentString(item, "call_id") == "" || attachmentString(item, "name") == "" || json.Unmarshal([]byte(attachmentString(item, "arguments")), &arguments) != nil || arguments == nil {
				return false
			}
			meaningful = true
		case "message":
			if item["status"] != "completed" || item["role"] != "assistant" {
				return false
			}
			foundText := false
			walkAttachmentJSON(item["content"], func(part map[string]any) {
				foundText = foundText || part["type"] == "output_text" && strings.TrimSpace(attachmentString(part, "text")) != ""
			})
			if !foundText {
				return false
			}
			meaningful = true
		default:
			return false
		}
	}
	return meaningful
}

func liveSemanticDownstreamCancellation(capture liveServerToolCapture, downstream, outbound error) bool {
	return capture.ErrorClass == "upstream_response_read_error" && capture.StreamReadErrorKind == "context_canceled" && errors.Is(downstream, context.Canceled) && errors.Is(outbound, context.Canceled) && capture.StreamContextAtTerminal == "active" && capture.StreamTerminalComplete && capture.StreamTerminalForwarded && capture.StreamTerminalBytes > 0 && capture.StreamForwardedBytes >= capture.StreamTerminalBytes && capture.StreamFlushedBytes >= capture.StreamTerminalBytes && !capture.CaptureTruncated
}

func liveSafeStreamReadErrorKind(err error) string {
	switch {
	case err == nil:
		return "active"
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "context_deadline_exceeded"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "unexpected_eof"
	default:
		return "other_read_error"
	}
}

// HTTP/1-only single-use connections prevent hidden HTTP/2 retries from bypassing
// the dispatch ledger; native HTTP/2 parity is outside this bounded capture.
type liveOneShotTransport struct{ base *http.Transport }

func (oneShot liveOneShotTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	base := oneShot.base
	if base == nil {
		base = http.DefaultTransport.(*http.Transport)
	}
	transport := base.Clone()
	transport.DisableCompression = true
	transport.DisableKeepAlives = true
	transport.Protocols = new(http.Protocols)
	transport.Protocols.SetHTTP1(true)
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = new(tls.Config)
	}
	transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
	response, err := transport.RoundTrip(request)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, err
	}
	response.Body = &liveClosingBody{ReadCloser: response.Body, done: transport.CloseIdleConnections}
	return response, nil
}

type liveClosingBody struct {
	io.ReadCloser
	done func()
}

func (body *liveClosingBody) Close() error { err := body.ReadCloser.Close(); body.done(); return err }

func (g *liveServerToolGate) rewriteTokenEndpoint(body []byte) ([]byte, error) {
	var token struct {
		Token     string            `json:"token"`
		Endpoints map[string]string `json:"endpoints"`
	}
	if err := json.Unmarshal(body, &token); err != nil {
		return nil, errors.New("token exchange response was invalid")
	}
	original, err := url.Parse(token.Endpoints["api"])
	if err != nil || original.Scheme != "https" || original.User != nil || original.RawQuery != "" || original.Fragment != "" || !isLiveCopilotHost(original.Hostname()) {
		return nil, errors.New("token exchange did not return a trusted Copilot API endpoint")
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, errors.New("token exchange response was invalid")
	}
	endpoints, ok := document["endpoints"].(map[string]any)
	if !ok {
		return nil, errors.New("token exchange endpoint map was invalid")
	}
	endpoints["api"] = g.server.URL + strings.TrimRight(original.Path, "/")
	g.mu.Lock()
	if g.publicAPI != nil && g.publicAPI.String() != original.String() {
		g.mu.Unlock()
		return nil, errors.New("token exchange changed the public Copilot API origin")
	}
	g.publicAPI = original
	if token.Token != "" {
		g.secrets = append(g.secrets, token.Token)
	}
	g.mu.Unlock()
	return json.Marshal(document)
}

func isLiveCopilotHost(host string) bool {
	host = strings.ToLower(host)
	return host == "githubcopilot.com" || strings.HasSuffix(host, ".githubcopilot.com")
}

func (g *liveServerToolGate) writeLocalFailure(writer http.ResponseWriter, request *http.Request, body []byte, code string, status int) {
	g.record(liveServerToolCapture{Category: "local", Dispatched: false, Request: g.requestLog(request, request.URL.String(), body, "inference"), LocalResponse: liveHTTPLog{Proto: request.Proto, Status: status}, ErrorClass: code})
	g.writeJSONFailure(writer, code, status)
}

func (*liveServerToolGate) writeJSONFailure(writer http.ResponseWriter, code string, status int) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = io.WriteString(writer, `{"error":{"code":"`+code+`"}}`)
}

func (g *liveServerToolGate) requestLog(request *http.Request, upstreamURL string, body []byte, category string) liveHTTPLog {
	log := liveHTTPLog{Method: request.Method, URL: upstreamURL, Host: request.Host, Proto: request.Proto, Headers: g.redactedHeaders(request.Header), ContentEncoding: request.Header.Get("Content-Encoding"), ContentLength: request.ContentLength, TransferEncoding: append([]string(nil), request.TransferEncoding...)}
	if category == "inference" {
		log.Body = g.redactedBody(body)
	} else if category == "auth" {
		log.Body = "[AUTHENTICATION BODY OMITTED]"
	} else if category == "catalog" {
		log.Body = g.redactedBody(body)
	}
	return log
}

func (g *liveServerToolGate) responseLog(response *http.Response, body []byte, category string) liveHTTPLog {
	log := liveHTTPLog{Proto: response.Proto, Status: response.StatusCode, Headers: g.redactedHeaders(response.Header), ContentEncoding: response.Header.Get("Content-Encoding"), ContentLength: response.ContentLength, TransferEncoding: append([]string(nil), response.TransferEncoding...), FramingCoverage: "parsed_from_public_response"}
	if category == "auth" {
		log.Body = "[AUTHENTICATION BODY OMITTED]"
	} else {
		log.Body = g.redactedBody(body)
	}
	return log
}

func (g *liveServerToolGate) redactedHeaders(headers http.Header) http.Header {
	g.mu.Lock()
	secrets := append([]string(nil), g.secrets...)
	g.mu.Unlock()
	redacted := make(http.Header, len(headers))
	for name, values := range headers {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "authorization") || strings.Contains(lower, "token") || strings.Contains(lower, "api-key") || strings.Contains(lower, "cookie") || strings.Contains(lower, "secret") {
			redacted[name] = []string{"[REDACTED]"}
			continue
		}
		for _, value := range values {
			redacted.Add(name, redact.Text(value, secrets...))
		}
	}
	return redacted
}

func (g *liveServerToolGate) redactedBody(body []byte) string {
	g.mu.Lock()
	secrets := append([]string(nil), g.secrets...)
	g.mu.Unlock()
	return redact.Text(string(body), secrets...)
}

func (g *liveServerToolGate) record(capture liveServerToolCapture) {
	g.mu.Lock()
	g.captureCount++
	capture.Sequence = g.captureCount
	g.mu.Unlock()
	body, err := json.MarshalIndent(capture, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(g.directory, fmt.Sprintf("gate-%03d.json", capture.Sequence)), body, 0600)
	}
	if err != nil {
		g.mu.Lock()
		g.captureErr = errors.New("could not write private gate capture")
		g.mu.Unlock()
	} else if capture.CaptureTruncated {
		g.mu.Lock()
		g.captureErr = errors.New("private gate capture was truncated")
		g.mu.Unlock()
	}
}

func (g *liveServerToolGate) countsFor(phaseCell string) (int, int, int, int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.counts[phaseCell], g.inferenceCount, g.authCount, g.catalogCount, g.captureErr
}

func copyHTTPHeaders(target, source http.Header) {
	for name, values := range source {
		for _, value := range values {
			target.Add(name, value)
		}
	}
}

func removeHopHeaders(headers http.Header) {
	for _, name := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "TE", "Trailer", "Transfer-Encoding", "Upgrade"} {
		headers.Del(name)
	}
}

func startLiveServerToolHarness(t *testing.T) (*liveServerToolGate, string, func()) {
	t.Helper()
	binary := strings.TrimSpace(os.Getenv("CPA_BINARY"))
	if binary == "" {
		t.Fatal("CPA_BINARY is required for the server-tool harness")
	}
	authPath := strings.TrimSpace(os.Getenv("CPA_LIVE_COPILOT_AUTH_FILE"))
	if authPath == "" || os.Getenv("CPA_LIVE_COPILOT_AUTH_MODE") != "token_exchange" {
		t.Fatal("a copied Copilot auth file and explicit token_exchange mode are required")
	}
	storageJSON, err := readLiveCopilotAuthFile(authPath)
	if err != nil {
		t.Fatal("copied Copilot auth file was unavailable or invalid")
	}
	var storage struct {
		GitHubAccessToken string `json:"github_access_token"`
	}
	if err := json.Unmarshal(storageJSON, &storage); err != nil || storage.GitHubAccessToken == "" {
		t.Fatal("copied Copilot auth file lacked a GitHub credential")
	}
	diagnostics := strings.TrimSpace(os.Getenv("CPA_LIVE_COPILOT_DEBUG_DIR"))
	if diagnostics == "" || !filepath.IsAbs(diagnostics) {
		t.Fatal("CPA_LIVE_COPILOT_DEBUG_DIR must name an absolute private diagnostics root")
	}
	if err := os.MkdirAll(diagnostics, 0700); err != nil {
		t.Fatal("could not create private server-tool diagnostics root")
	}
	if err := os.Chmod(diagnostics, 0700); err != nil {
		t.Fatal("could not restrict private server-tool diagnostics root")
	}
	directory, err := os.MkdirTemp(diagnostics, "gate-")
	if err != nil {
		t.Fatal("could not create private forwarding-gate directory")
	}
	gate := newLiveServerToolGateWithLedger(t, directory, diagnostics, storage.GitHubAccessToken)
	base, stop := startLiveNativeHostWithGate(t, binary, storageJSON, "token_exchange", liveEndpointOverrides(nil), gate.server.URL)
	return gate, base, stop
}

func TestLiveServerToolsPhaseA(t *testing.T) {
	if os.Getenv("CPA_LIVE_SERVER_TOOLS_A") != "1" {
		t.Skip("set CPA_LIVE_SERVER_TOOLS_A=1 for bounded native CLI search baselines")
	}
	gate, base, stop := startLiveServerToolHarness(t)
	defer stop()
	for _, test := range []struct {
		client  string
		version string
		model   string
	}{
		{client: "codex", version: "codex-cli 0.161.0", model: "gpt-6-luna"},
		{client: "claude", version: "2.1.295 (Claude Code)", model: "claude-haiku-5.5"},
	} {
		t.Run(test.client, func(t *testing.T) {
			path, err := exec.LookPath(test.client)
			if err != nil {
				t.Errorf("client=%s status=NOTRUN reason=cli_missing", test.client)
				return
			}
			version, err := exec.Command(path, "--version").Output()
			if err != nil || strings.TrimSpace(string(version)) != test.version {
				t.Errorf("client=%s status=NOTRUN reason=version_mismatch", test.client)
				return
			}
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal("could not restrict temporary CLI home")
			}
			if test.client == "codex" {
				catalogPath := filepath.Join(root, "models.json")
				if err := os.WriteFile(catalogPath, []byte(liveCodexModelCatalog), 0600); err != nil {
					t.Fatal("could not write temporary Codex model catalog")
				}
				if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte(liveCodexConfiguration(base, catalogPath)), 0600); err != nil {
					t.Fatal("could not write temporary Codex config")
				}
			}
			if err := gate.setPhaseCell("A", test.client); err != nil {
				t.Fatal(err)
			}
			before, _, _, _, captureErr := gate.countsFor("A/" + test.client)
			if captureErr != nil {
				t.Fatal("private gate capture failed before CLI run")
			}
			result := runLiveCLIWithEffort(t, path, test.client, base, root, liveMoonTask, "medium")
			logLiveCLIResult(t, test.client, "web_search", result)
			after, total, auth, catalog, captureErr := gate.countsFor("A/" + test.client)
			observedEffort := gate.observedMediumEffort("A/"+test.client, test.client)
			t.Logf("phase=A client=%s model=%s requested_effort=medium observed_effort_medium=%t physical_inference=%d total_inference=%d auth=%d catalog=%d", test.client, test.model, observedEffort, after-before, total, auth, catalog)
			if captureErr != nil {
				t.Error("private gate capture failed")
			}
			if after <= before || !observedEffort || result.exitCode != 0 || !result.final || !liveSearchCompleted(test.client, result) {
				t.Errorf("client=%s search_status=FAIL error_code=%s", test.client, result.errorCode)
			}
		})
	}
}

func (g *liveServerToolGate) observedMediumEffort(cell, client string) bool {
	entries, err := os.ReadDir(g.directory)
	if err != nil {
		return false
	}
	seen := false
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "gate-") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(g.directory, entry.Name()))
		if err != nil {
			return false
		}
		var capture liveServerToolCapture
		if json.Unmarshal(data, &capture) != nil || capture.PhaseCell != cell || capture.Category != "inference" || !capture.Dispatched {
			continue
		}
		seen = true
		var payload map[string]any
		if json.Unmarshal([]byte(capture.PublicRequest.Body), &payload) != nil {
			return false
		}
		field := "reasoning"
		if client == "claude" {
			field = "output_config"
		}
		config, _ := payload[field].(map[string]any)
		if config["effort"] != "medium" {
			return false
		}
	}
	return seen
}

func TestLiveServerToolsPhaseB(t *testing.T) {
	if os.Getenv("CPA_LIVE_SERVER_TOOLS_B") != "1" {
		t.Skip("set CPA_LIVE_SERVER_TOOLS_B=1 for bounded native server-tool probes")
	}
	gate, base, stop := startLiveServerToolHarness(t)
	defer stop()
	for _, candidate := range liveServerToolCandidates() {
		t.Run(candidate.name, func(t *testing.T) {
			if err := gate.setPhaseCell("B", candidate.name); err != nil {
				t.Fatal(err)
			}
			before, _, _, _, captureErr := gate.countsFor("B/" + candidate.name)
			if captureErr != nil {
				t.Fatal("private gate capture failed before native probe")
			}
			response, status, err := postLiveServerToolRequest(base+candidate.path, candidate.payload)
			after, total, auth, catalog, captureErr := gate.countsFor("B/" + candidate.name)
			t.Logf("phase=B family=%s model=%s http=%d physical_inference=%d total_inference=%d auth=%d catalog=%d", candidate.name, candidate.model, status, after-before, total, auth, catalog)
			if captureErr != nil {
				t.Error("private gate capture failed")
			}
			if err != nil || after <= before {
				t.Error("native probe did not complete a physical dispatch")
				return
			}
			if status != http.StatusOK {
				t.Errorf("native probe was refused: HTTP %d error_class=%s", status, safeLiveErrorClass(response, status))
				return
			}
			evidence := inspectLiveServerToolResponse(candidate.name, response)
			t.Logf("phase=B family=%s server_call=%t matched_result=%t relevant_content=%t source=%t terminal=%t", candidate.name, evidence.call, evidence.result, evidence.content, evidence.source, evidence.terminal)
			gptSearchProof := candidate.name != "gpt-responses-search" || evidence.resultContent && evidence.actionSource && evidence.citation && evidence.pairedSourceResult && evidence.pairedCitation
			if !evidence.call || !evidence.result || !evidence.content || !evidence.terminal || !gptSearchProof || candidate.requiresSource && !evidence.source {
				t.Error("native response lacked required server call, result, or relevant content evidence")
			}
		})
	}
}

type liveServerToolCandidate struct {
	name           string
	model          string
	path           string
	payload        map[string]any
	requiresSource bool
}

func liveServerToolCandidates() []liveServerToolCandidate {
	moon := "Use the offered server tool to find NASA's Moon Facts page, then give one fact with its source URL."
	return []liveServerToolCandidate{
		{name: "gemini-chat-search", model: "gemini-3.8-flash", path: "/v1/chat/completions", requiresSource: true, payload: map[string]any{"model": "gemini-3.8-flash", "stream": false, "messages": []any{map[string]any{"role": "user", "content": moon}}, "web_search_options": map[string]any{"search_context_size": "low"}}},
		{name: "gpt-responses-search", model: "gpt-6-luna", path: "/v1/responses", requiresSource: true, payload: map[string]any{"model": "gpt-6-luna", "stream": false, "store": false, "input": moon, "tools": []any{map[string]any{"type": "web_search", "search_context_size": "low"}}, "tool_choice": "required", "include": []string{"web_search_call.action.sources", "web_search_call.results"}}},
		{name: "claude-web-fetch", model: "claude-haiku-5.5", path: "/v1/messages", requiresSource: true, payload: map[string]any{"model": "claude-haiku-5.5", "stream": false, "max_tokens": 512, "messages": []any{map[string]any{"role": "user", "content": "Fetch https://science.nasa.gov/moon/facts/ with the web_fetch server tool and give the page title."}}, "tools": []any{map[string]any{"type": "web_fetch_20250910", "name": "web_fetch", "max_uses": 1}}}},
		{name: "claude-code-execution", model: "claude-haiku-5.5", path: "/v1/messages", payload: map[string]any{"model": "claude-haiku-5.5", "stream": false, "max_tokens": 512, "messages": []any{map[string]any{"role": "user", "content": "Use the code_execution server tool to run Python that computes sum(i*i for i in range(1, 11)). Report the printed result."}}, "tools": []any{map[string]any{"type": "code_execution_20250825", "name": "code_execution"}}}},
		{name: "gpt-code-interpreter", model: "gpt-6-luna", path: "/v1/responses", payload: map[string]any{"model": "gpt-6-luna", "stream": false, "store": false, "input": "Use the python tool to compute sum(i*i for i in range(1, 11)). Print and report the result.", "tools": []any{map[string]any{"type": "code_interpreter", "container": map[string]any{"type": "auto"}}}, "tool_choice": "required"}},
	}
}

func postLiveServerToolRequest(endpoint string, payload any) ([]byte, int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, errors.New("native tool request could not be encoded")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, errors.New("native tool request could not be constructed")
	}
	request.Header.Set("Authorization", "Bearer "+liveCopilotClientKey)
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 95 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: liveOneShotTransport{}}
	response, err := client.Do(request)
	if err != nil {
		return nil, 0, errors.New("native tool request failed before receiving a response")
	}
	defer response.Body.Close()
	result, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return nil, response.StatusCode, errors.New("native tool response could not be read")
	}
	return result, response.StatusCode, nil
}

type liveServerToolEvidence struct {
	call               bool
	result             bool
	content            bool
	source             bool
	terminal           bool
	resultContent      bool
	actionSource       bool
	citation           bool
	pairedSourceResult bool
	pairedCitation     bool
}

func inspectLiveServerToolResponse(family string, body []byte) liveServerToolEvidence {
	var document map[string]any
	if json.Unmarshal(body, &document) != nil {
		return liveServerToolEvidence{}
	}
	expectedModel := map[string]string{
		"gemini-chat-search":    "gemini-3.8-flash",
		"gpt-responses-search":  "gpt-6-luna",
		"claude-web-fetch":      "claude-haiku-5.5",
		"claude-code-execution": "claude-haiku-5.5",
		"gpt-code-interpreter":  "gpt-6-luna",
	}[family]
	returnedModel, _ := document["model"].(string)
	if expectedModel == "" || !liveMatrixResponseModelMatches(expectedModel, returnedModel) {
		return liveServerToolEvidence{}
	}
	switch {
	case family == "gemini-chat-search" && document["object"] != "chat.completion":
		return liveServerToolEvidence{}
	case strings.HasPrefix(family, "gpt-") && document["object"] != "response":
		return liveServerToolEvidence{}
	case strings.HasPrefix(family, "claude-") && (document["type"] != "message" || document["role"] != "assistant"):
		return liveServerToolEvidence{}
	}
	var evidence liveServerToolEvidence
	switch family {
	case "gemini-chat-search":
		evidence = inspectLiveGeminiSearch(document)
		choices := liveAnySlice(document["choices"])
		if len(choices) == 1 {
			choice, _ := choices[0].(map[string]any)
			evidence.terminal = choice["finish_reason"] == "stop"
		}
	case "gpt-responses-search":
		evidence = inspectLiveGPTSearch(document)
	case "claude-web-fetch":
		evidence = inspectLiveClaudeServerTool(document, "web_fetch")
	case "claude-code-execution":
		evidence = inspectLiveClaudeServerTool(document, "code_execution")
	case "gpt-code-interpreter":
		evidence = inspectLiveGPTCodeInterpreter(document)
	default:
		return liveServerToolEvidence{}
	}
	if strings.HasPrefix(family, "gpt-") {
		evidence.terminal = document["status"] == "completed" && document["incomplete_details"] == nil && document["error"] == nil
	}
	if strings.HasPrefix(family, "claude-") {
		evidence.terminal = document["stop_reason"] == "end_turn"
	}
	return evidence
}

func inspectLiveGeminiSearch(document map[string]any) liveServerToolEvidence {
	var evidence liveServerToolEvidence
	for _, rawChoice := range liveAnySlice(document["choices"]) {
		choice, _ := rawChoice.(map[string]any)
		message, _ := choice["message"].(map[string]any)
		if message["role"] != "assistant" {
			continue
		}
		text, _ := message["content"].(string)
		evidence.content = evidence.content || strings.Contains(strings.ToLower(text), "moon")
		for _, rawAnnotation := range liveAnySlice(message["annotations"]) {
			annotation, _ := rawAnnotation.(map[string]any)
			urlValue, _ := annotation["url"].(string)
			if annotation["type"] == "url_citation" && isNASAURL(urlValue) {
				evidence.source = true
			}
		}
		uses := make(map[string]bool)
		for _, rawBlock := range liveAnySlice(message["server_tool_events"]) {
			block, _ := rawBlock.(map[string]any)
			id, _ := block["id"].(string)
			if block["type"] == "server_tool_use" && block["name"] == "web_search" && id != "" {
				uses[id] = true
				evidence.call = true
			}
			if block["type"] == "web_search_tool_result" && uses[fmt.Sprint(block["tool_use_id"])] && block["error"] == nil {
				content, _ := block["content"].(string)
				if strings.TrimSpace(content) != "" {
					evidence.result = true
				}
			}
		}
	}
	return evidence
}

func inspectLiveGPTSearch(document map[string]any) liveServerToolEvidence {
	var evidence liveServerToolEvidence
	pairedURLs := make(map[string]bool)
	for _, rawItem := range liveAnySlice(document["output"]) {
		item, _ := rawItem.(map[string]any)
		if item["type"] == "web_search_call" {
			id, _ := item["id"].(string)
			action, _ := item["action"].(map[string]any)
			if id != "" && action["type"] == "search" {
				evidence.call = true
				actionURLs := make(map[string]bool)
				for _, rawSource := range liveAnySlice(action["sources"]) {
					source, _ := rawSource.(map[string]any)
					if urlValue, ok := source["url"].(string); ok && isNASAURL(urlValue) {
						actionURLs[urlValue] = true
						evidence.source = true
						evidence.actionSource = true
					}
				}
				if item["status"] == "completed" {
					for _, rawResult := range liveAnySlice(item["results"]) {
						result, _ := rawResult.(map[string]any)
						if result != nil {
							resultURL, _ := result["url"].(string)
							if resultURL == "" {
								resultURL, _ = result["source_website_url"].(string)
							}
							for _, contentField := range []string{"title", "snippet", "content", "description"} {
								text, _ := result[contentField].(string)
								if strings.TrimSpace(resultURL) != "" && strings.TrimSpace(text) != "" {
									evidence.result = true
									evidence.resultContent = true
									if actionURLs[resultURL] {
										pairedURLs[resultURL] = true
										evidence.pairedSourceResult = true
									}
								}
							}
						}
					}
				}
				for _, rawSource := range liveAnySlice(item["results"]) {
					source, _ := rawSource.(map[string]any)
					for _, field := range []string{"url", "source_website_url"} {
						if urlValue, ok := source[field].(string); ok && isNASAURL(urlValue) {
							evidence.source = true
						}
					}
				}
			}
		}
		if item["type"] == "message" && item["role"] == "assistant" {
			for _, rawPart := range liveAnySlice(item["content"]) {
				part, _ := rawPart.(map[string]any)
				if part["type"] != "output_text" {
					continue
				}
				text, _ := part["text"].(string)
				if strings.Contains(strings.ToLower(text), "moon") {
					for _, rawCitation := range liveAnySlice(part["annotations"]) {
						citation, _ := rawCitation.(map[string]any)
						if urlValue, ok := citation["url"].(string); ok && citation["type"] == "url_citation" && isNASAURL(urlValue) {
							evidence.content = true
							evidence.citation = true
							if pairedURLs[urlValue] {
								evidence.pairedCitation = true
							}
						}
					}
				}
			}
		}
	}
	return evidence
}

func inspectLiveClaudeServerTool(document map[string]any, name string) liveServerToolEvidence {
	var evidence liveServerToolEvidence
	uses := make(map[string]string)
	for _, rawBlock := range liveAnySlice(document["content"]) {
		block, _ := rawBlock.(map[string]any)
		id, _ := block["id"].(string)
		callName := name
		if name == "code_execution" && block["name"] == "bash_code_execution" {
			callName = "bash_code_execution"
		}
		if block["type"] == "server_tool_use" && block["name"] == callName && id != "" {
			uses[id] = callName
			evidence.call = true
		}
		resultType := name + "_tool_result"
		if name == "code_execution" && block["type"] == "bash_code_execution_tool_result" {
			resultType = "bash_code_execution_tool_result"
		}
		if block["type"] == resultType && uses[fmt.Sprint(block["tool_use_id"])] == strings.TrimSuffix(resultType, "_tool_result") {
			content, _ := block["content"].(map[string]any)
			if name == "web_fetch" && content["type"] == "web_fetch_result" {
				urlValue, _ := content["url"].(string)
				document, _ := content["content"].(map[string]any)
				source, _ := document["source"].(map[string]any)
				data, _ := source["data"].(string)
				evidence.result = true
				evidence.source = isNASAURL(urlValue)
				evidence.content = strings.TrimSpace(data) != ""
			}
			if name == "code_execution" && (content["type"] == "bash_code_execution_result" || content["type"] == "code_execution_result") {
				returnCode, codePresent := content["return_code"].(float64)
				stdout, stdoutPresent := content["stdout"].(string)
				if !codePresent || returnCode != 0 || !stdoutPresent || strings.TrimSpace(stdout) != "385" {
					continue
				}
				evidence.result = true
				evidence.content = true
			}
		}
	}
	return evidence
}

func inspectLiveGPTCodeInterpreter(document map[string]any) liveServerToolEvidence {
	var evidence liveServerToolEvidence
	toolResult := false
	messageResult := false
	for _, rawItem := range liveAnySlice(document["output"]) {
		item, _ := rawItem.(map[string]any)
		if item["type"] == "code_interpreter_call" {
			id, _ := item["id"].(string)
			containerID, _ := item["container_id"].(string)
			if strings.TrimSpace(id) != "" && strings.TrimSpace(containerID) != "" {
				evidence.call = true
				if item["status"] != "completed" {
					continue
				}
				evidence.result = true
				for _, rawOutput := range liveAnySlice(item["outputs"]) {
					output, _ := rawOutput.(map[string]any)
					if logs, ok := output["logs"].(string); ok && output["type"] == "logs" && strings.TrimSpace(logs) == "385" {
						toolResult = true
					}
				}
			}
		}
		if item["type"] == "message" && item["role"] == "assistant" {
			for _, rawPart := range liveAnySlice(item["content"]) {
				part, _ := rawPart.(map[string]any)
				if part["type"] != "output_text" {
					continue
				}
				if text, ok := part["text"].(string); ok && liveReportsExactArithmeticResult(text) {
					messageResult = true
				}
			}
		}
	}
	evidence.content = toolResult && messageResult
	return evidence
}

func liveReportsExactArithmeticResult(text string) bool {
	text = strings.TrimSuffix(strings.TrimSpace(text), ".")
	switch strings.ToLower(text) {
	case "385", "the arithmetic result is 385", "the result is 385", "the sum is 385", "the answer is 385":
		return true
	default:
		return false
	}
}

func liveAnySlice(value any) []any {
	items, _ := value.([]any)
	return items
}

func isNASAURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && (parsed.Hostname() == "nasa.gov" || strings.HasSuffix(parsed.Hostname(), ".nasa.gov"))
}

func TestLiveServerToolTransportUsesHTTP1WithoutDisconnectRetries(t *testing.T) {
	for _, test := range []struct {
		name, method, path, body string
	}{
		{"inference POST", http.MethodPost, "/responses", `{"model":"gpt-6-luna","input":"local fixture"}`},
		{"auth GET", http.MethodGet, "/copilot_internal/v2/token", ""},
		{"catalog GET", http.MethodGet, "/models", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			var connections, requests, dialAttempts atomic.Int32
			var observedMu sync.Mutex
			var observedProtocol, observedBody, negotiated string
			peerErrors := make(chan error, 16)
			peer := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests.Add(1)
				body, err := io.ReadAll(request.Body)
				if err != nil {
					peerErrors <- err
					return
				}
				observedMu.Lock()
				observedProtocol, observedBody = request.Proto, string(body)
				observedMu.Unlock()
				connection, _, err := writer.(http.Hijacker).Hijack()
				if err != nil {
					peerErrors <- err
					return
				}
				if err := connection.Close(); err != nil {
					peerErrors <- err
				}
			}))
			peer.EnableHTTP2 = true
			peer.TLS = &tls.Config{NextProtos: []string{"h2", "http/1.1"}}
			peer.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateNew {
					connections.Add(1)
				}
			}
			peer.Config.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){
				"h2": func(_ *http.Server, connection *tls.Conn, _ http.Handler) {
					defer connection.Close()
					preface := make([]byte, len(http2.ClientPreface))
					if _, err := io.ReadFull(connection, preface); err != nil {
						return
					}
					framer := http2.NewFramer(connection, connection)
					if err := framer.WriteSettings(); err != nil {
						return
					}
					for {
						frame, err := framer.ReadFrame()
						if err != nil {
							return
						}
						if headers, ok := frame.(*http2.HeadersFrame); ok {
							requests.Add(1)
							if err := framer.WriteRSTStream(headers.StreamID, http2.ErrCodeRefusedStream); err != nil {
								return
							}
						}
					}
				},
			}
			peer.StartTLS()
			t.Cleanup(func() { peer.CloseClientConnections(); peer.Close() })
			roots := x509.NewCertPool()
			roots.AddCert(peer.Certificate())
			base := http.DefaultTransport.(*http.Transport).Clone()
			base.Proxy = nil
			base.TLSClientConfig = &tls.Config{RootCAs: roots}
			base.DialContext = liveServerToolFixtureDialer(peer)
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			trace := &httptrace.ClientTrace{
				ConnectStart: func(_, _ string) { dialAttempts.Add(1) },
				TLSHandshakeDone: func(state tls.ConnectionState, err error) {
					if err == nil {
						observedMu.Lock()
						negotiated = state.NegotiatedProtocol
						observedMu.Unlock()
					}
				},
			}
			var body io.Reader
			if test.body != "" {
				body = strings.NewReader(test.body)
			}
			request, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), test.method, peer.URL+test.path, body)
			if err != nil {
				t.Fatal(err)
			}
			if test.body != "" && request.GetBody == nil {
				t.Fatal("POST fixture must retain its replayable body to exercise hidden retries")
			}
			client := &http.Client{Transport: liveOneShotTransport{base: base}}
			response, err := client.Do(request)
			if response != nil {
				if closeErr := response.Body.Close(); closeErr != nil {
					t.Error(closeErr)
				}
			}
			if err == nil {
				t.Fatal("disconnect before response headers must return a transport error")
			}
			observedMu.Lock()
			protocol, receivedBody, alpn := observedProtocol, observedBody, negotiated
			observedMu.Unlock()
			if protocol != "HTTP/1.1" || alpn != "http/1.1" || receivedBody != test.body {
				t.Errorf("TLS peer observed protocol=%q ALPN=%q body=%q", protocol, alpn, receivedBody)
			}
			if connections.Load() != 1 || requests.Load() != 1 || dialAttempts.Load() != 1 {
				t.Errorf("disconnect caused connections=%d requests=%d dial_attempts=%d, want one each", connections.Load(), requests.Load(), dialAttempts.Load())
			}
			t.Logf("local TLS peer: ALPN=%s protocol=%s requests=%d connections=%d dial_attempts=%d; HTTP/2 parity unverified", alpn, protocol, requests.Load(), connections.Load(), dialAttempts.Load())
			select {
			case err := <-peerErrors:
				t.Error(err)
			default:
			}
		})
	}
}

func liveServerToolFixtureDialer(peer *httptest.Server, aliases ...string) func(context.Context, string, string) (net.Conn, error) {
	peerAddress := peer.Listener.Addr().String()
	return func(ctx context.Context, network, requestedAddress string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(peerAddress)
		if err != nil || !net.ParseIP(host).IsLoopback() {
			return nil, errors.New("fixture transport requires an observed loopback peer")
		}
		allowed := requestedAddress == peerAddress
		for _, alias := range aliases {
			allowed = allowed || requestedAddress == alias
		}
		if !allowed || network != "tcp" {
			return nil, errors.New("fixture transport denied an unregistered destination")
		}
		return (&net.Dialer{}).DialContext(ctx, network, peerAddress)
	}
}

func TestLiveServerToolFixtureDialerRejectsPublicDestinations(t *testing.T) {
	peer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("denied fixture destination reached the TLS peer")
	}))
	t.Cleanup(peer.Close)
	dial := liveServerToolFixtureDialer(peer)
	for _, destination := range []string{"api.github.com:443", "api.githubcopilot.com:443", "127.0.0.2:443"} {
		connection, err := dial(context.Background(), "tcp", destination)
		if connection != nil {
			if closeErr := connection.Close(); closeErr != nil {
				t.Error(closeErr)
			}
		}
		if err == nil || !strings.Contains(err.Error(), "denied an unregistered destination") {
			t.Errorf("fixture dial to %s was not denied: %v", destination, err)
		}
	}
}

func TestLiveServerToolGateStopsAfterCaptureWriteFailure(t *testing.T) {
	var dispatches atomic.Int32
	peer := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		dispatches.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(writer, `{"ok":true}`); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(peer.Close)
	directory := t.TempDir()
	gate := newLiveServerToolGate(t, directory)
	base := peer.Client().Transport.(*http.Transport).Clone()
	base.Proxy = nil
	base.TLSClientConfig.ServerName = "127.0.0.1"
	base.DialContext = liveServerToolFixtureDialer(peer, "api.github.com:443")
	gate.client.Transport = liveOneShotTransport{base: base}
	var err error
	gate.publicAPI, err = url.Parse(peer.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.setPhaseCell("A", "codex"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, "gate-001.json"), 0700); err != nil {
		t.Fatal(err)
	}
	localTransport := http.DefaultTransport.(*http.Transport).Clone()
	localTransport.Proxy = nil
	localTransport.DialContext = liveServerToolFixtureDialer(gate.server)
	client := &http.Client{Timeout: 3 * time.Second, Transport: localTransport}
	t.Cleanup(localTransport.CloseIdleConnections)
	response, err := client.Post(gate.server.URL+"/responses", "application/json", strings.NewReader(`{"model":"gpt-6-luna"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("already-dispatched request status = %d", response.StatusCode)
	}
	_, inference, auth, catalog, captureErr := gate.countsFor("A/codex")
	if captureErr == nil || inference != 1 || auth != 0 || catalog != 0 {
		t.Fatalf("capture failure lost first reservation: inference=%d auth=%d catalog=%d capture_error=%v", inference, auth, catalog, captureErr)
	}
	for _, path := range []string{"/responses", "/models", "/copilot_internal/v2/token"} {
		response, err := client.Post(gate.server.URL+path, "application/json", strings.NewReader(`{"model":"gpt-6-luna"}`))
		if err != nil {
			t.Fatal(err)
		}
		if err := response.Body.Close(); err != nil {
			t.Error(err)
		}
		if response.StatusCode != http.StatusTooManyRequests {
			t.Errorf("request after capture failure to %s status = %d", path, response.StatusCode)
		}
	}
	_, inference, auth, catalog, captureErr = gate.countsFor("A/codex")
	if dispatches.Load() != 1 || inference != 1 || auth != 0 || catalog != 0 || captureErr == nil {
		t.Errorf("capture failure did not close dispatch gate: dispatches=%d inference=%d auth=%d catalog=%d capture_error=%v", dispatches.Load(), inference, auth, catalog, captureErr)
	}
}

func TestLiveServerToolCodeExecutionRequiresSuccessfulStdout(t *testing.T) {
	for _, test := range []struct {
		name, content string
		want          bool
	}{
		{"successful bash", `{"type":"bash_code_execution_result","return_code":0,"stdout":"385\n","stderr":""}`, true},
		{"successful Python", `{"type":"code_execution_result","return_code":0,"stdout":"385\n","stderr":""}`, true},
		{"failed with matching stderr", `{"type":"bash_code_execution_result","return_code":1,"stdout":"","stderr":"error 385"}`, false},
		{"failed with matching stdout", `{"type":"bash_code_execution_result","return_code":1,"stdout":"385","stderr":"failed"}`, false},
		{"matching unrelated field", `{"type":"bash_code_execution_result","return_code":0,"stdout":"other","metadata":"385"}`, false},
		{"error result", `{"type":"code_execution_tool_result_error","return_code":0,"stdout":"385"}`, false},
		{"unknown result type", `{"type":"unknown_result","return_code":0,"stdout":"385"}`, false},
		{"missing return code", `{"type":"bash_code_execution_result","stdout":"385"}`, false},
		{"unrelated stdout", `{"type":"bash_code_execution_result","return_code":0,"stdout":"1385"}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(`{"type":"message","role":"assistant","model":"claude-haiku-5.5","content":[{"type":"server_tool_use","id":"srv_1","name":"code_execution"},{"type":"code_execution_tool_result","tool_use_id":"srv_1","content":` + test.content + `}]}`)
			evidence := inspectLiveServerToolResponse("claude-code-execution", body)
			if !evidence.call || evidence.result != test.want || evidence.content != test.want {
				t.Errorf("code result evidence = %+v, want successful stdout evidence=%t", evidence, test.want)
			}
		})
	}
}

func TestLiveServerToolMatrixMessagesFixture(t *testing.T) {
	body, err := os.ReadFile("testdata/native/v1/messages.sse")
	if err != nil {
		t.Fatal(err)
	}
	text, model, schema, terminal := validateLiveResponse("Claude Messages", true, "text/event-stream", body)
	if text != "synthetic Claude response" || model != "fixture-model" || !schema || !terminal {
		t.Errorf("Messages fixture: text=%q model=%q schema=%t terminal=%t", text, model, schema, terminal)
	}
}

func TestLiveServerToolGateRejectsSupersededPhaseF(t *testing.T) {
	gate := newLiveServerToolGate(t, t.TempDir())
	for _, model := range []string{"gpt-6-luna", "claude-haiku-5.5"} {
		for _, api := range []string{"chat", "responses", "messages"} {
			for _, format := range []string{"json", "sse"} {
				cell := model + "-" + api + "-" + format
				if err := gate.setPhaseCell("F", cell); err == nil {
					t.Errorf("superseded protocol cell %s acquired a phase F lease", cell)
				}
			}
		}
	}
	_, inference, auth, catalog, captureErr := gate.countsFor("")
	if inference != 0 || auth != 0 || catalog != 0 || captureErr != nil {
		t.Errorf("superseded phase F changed dispatch counts: inference=%d auth=%d catalog=%d capture_error=%v", inference, auth, catalog, captureErr)
	}
}

func TestLiveServerToolCodeInterpreterBindsSuccessfulLogs(t *testing.T) {
	for _, test := range []struct {
		name, items string
		want        bool
	}{
		{"completed arithmetic logs", `{"type":"code_interpreter_call","id":"ci_1","container_id":"cntr_1","status":"completed","outputs":[{"type":"logs","logs":"385\n"}]}`, true},
		{"error mentions arithmetic", `{"type":"code_interpreter_call","id":"ci_1","container_id":"cntr_1","status":"completed","outputs":[{"type":"logs","logs":"error at line385"}]}`, false},
		{"unrecognized output type", `{"type":"code_interpreter_call","id":"ci_1","container_id":"cntr_1","status":"completed","outputs":[{"type":"unknown","logs":"385"}]}`, false},
		{"failed call has matching logs", `{"type":"code_interpreter_call","id":"ci_1","container_id":"cntr_1","status":"failed","outputs":[{"type":"logs","logs":"385"}]}`, false},
		{"incomplete call has matching logs", `{"type":"code_interpreter_call","id":"ci_1","container_id":"cntr_1","status":"incomplete","outputs":[{"type":"logs","logs":"385"}]}`, false},
		{"missing item identity", `{"type":"code_interpreter_call","container_id":"cntr_1","status":"completed","outputs":[{"type":"logs","logs":"385"}]}`, false},
		{"missing container identity", `{"type":"code_interpreter_call","id":"ci_1","status":"completed","outputs":[{"type":"logs","logs":"385"}]}`, false},
		{"assistant alone explains arithmetic", `{"type":"code_interpreter_call","id":"ci_1","container_id":"cntr_1","status":"completed","outputs":[]}`, false},
		{"failed logs cannot combine with completed call", `{"type":"code_interpreter_call","id":"ci_failed","container_id":"cntr_failed","status":"failed","outputs":[{"type":"logs","logs":"385"}]},{"type":"code_interpreter_call","id":"ci_completed","container_id":"cntr_completed","status":"completed","outputs":[{"type":"logs","logs":"other output"}]}`, false},
		{"incomplete logs cannot combine with completed call", `{"type":"code_interpreter_call","id":"ci_incomplete","container_id":"cntr_incomplete","status":"incomplete","outputs":[{"type":"logs","logs":"385"}]},{"type":"code_interpreter_call","id":"ci_completed","container_id":"cntr_completed","status":"completed","outputs":[]}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(`{"object":"response","model":"gpt-6-luna","output":[` + test.items + `,{"type":"message","role":"assistant","content":[{"type":"output_text","text":"The arithmetic result is 385."}]}]}`)
			evidence := inspectLiveServerToolResponse("gpt-code-interpreter", body)
			if evidence.content != test.want || test.want && (!evidence.call || !evidence.result) {
				t.Errorf("code interpreter evidence = %+v, want arithmetic tool content=%t", evidence, test.want)
			}
		})
	}
	tool := `{"type":"code_interpreter_call","id":"ci_1","container_id":"cntr_1","status":"completed","outputs":[{"type":"logs","logs":"385"}]}`
	for _, answer := range []string{"1385", "The arithmetic result is not 385."} {
		t.Run(answer, func(t *testing.T) {
			encodedAnswer, err := json.Marshal(answer)
			if err != nil {
				t.Fatal(err)
			}
			body := []byte(`{"object":"response","model":"gpt-6-luna","output":[` + tool + `,{"type":"message","role":"assistant","content":[{"type":"output_text","text":` + string(encodedAnswer) + `}]}]}`)
			if evidence := inspectLiveServerToolResponse("gpt-code-interpreter", body); evidence.content {
				t.Fatalf("incorrect assistant answer %q was accepted with matching tool logs", answer)
			}
		})
	}
}

type liveToolRoundTrip func(*http.Request) (*http.Response, error)

func (roundTrip liveToolRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestLiveServerToolGateAccountingAndRedaction(t *testing.T) {
	const githubSecret = "github-private-credential-value"
	const copilotSecret = "copilot-private-credential-value"
	directory := t.TempDir()
	gate := newLiveServerToolGate(t, directory, githubSecret)
	var upstream []string
	providerFailure := false
	gate.client.Transport = liveToolRoundTrip(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, errors.New("fixture request body could not be read")
		}
		upstream = append(upstream, request.URL.String())
		if request.URL.Host == "api.github.com" {
			return liveToolFixtureResponse(http.StatusOK, `{"token":"`+copilotSecret+`","endpoints":{"api":"https://api.githubcopilot.com"}}`), nil
		}
		if request.URL.Host != "api.githubcopilot.com" {
			t.Errorf("forwarded to unexpected public host %q", request.URL.Host)
		}
		if request.URL.Path == "/models" {
			return liveToolFixtureResponse(http.StatusOK, `{"data":[]}`), nil
		}
		if !bytes.Equal(body, []byte(`{"model":"gpt-6-luna","web_search_options":{"search_context_size":"low"},"private":"`+githubSecret+`"}`)) {
			t.Error("inference body changed at the forwarding gate")
		}
		if request.Header.Get("X-Semantic") != "preserved" || request.Header.Get("Authorization") != "Bearer "+copilotSecret {
			t.Error("inference header semantics changed at the forwarding gate")
		}
		if providerFailure {
			return liveToolFixtureResponse(http.StatusBadRequest, `{"error":{"type":"invalid_request_error","message":"`+copilotSecret+`"}}`), nil
		}
		return liveToolFixtureResponse(http.StatusOK, `{"ok":true}`), nil
	})
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	authRequest, err := http.NewRequest(http.MethodGet, gate.server.URL+"/copilot_internal/v2/token", nil)
	if err != nil {
		t.Fatal(err)
	}
	authRequest.Header.Set("Authorization", "token "+githubSecret)
	authResponse, err := client.Do(authRequest)
	if err != nil {
		t.Fatal("fixture token exchange did not reach the gate")
	}
	authBody, err := io.ReadAll(authResponse.Body)
	_ = authResponse.Body.Close()
	if err != nil || authResponse.StatusCode != http.StatusOK || !bytes.Contains(authBody, []byte(gate.server.URL)) || bytes.Contains(authBody, []byte("api.githubcopilot.com")) {
		t.Fatal("copied token exchange response did not point back to the task gate")
	}
	for i := 0; i < 5; i++ {
		response, err := client.Get(gate.server.URL + "/models")
		if err != nil {
			t.Fatal("fixture model request failed")
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Errorf("catalog request %d status = %d", i, response.StatusCode)
		}
	}
	if err := gate.setPhaseCell("A", "codex"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		status := sendLiveGateFixtureInference(t, client, gate.server.URL, githubSecret, copilotSecret)
		if status != http.StatusOK {
			t.Errorf("phase A request %d status = %d", i, status)
		}
	}
	for _, name := range []string{"gemini-chat-search", "gpt-responses-search", "claude-web-fetch", "claude-code-execution", "gpt-code-interpreter"} {
		if err := gate.setPhaseCell("B", name); err != nil {
			t.Fatal(err)
		}
		if status := sendLiveGateFixtureInference(t, client, gate.server.URL, githubSecret, copilotSecret); status != http.StatusOK {
			t.Errorf("phase B %s first status = %d", name, status)
		}
		if status := sendLiveGateFixtureInference(t, client, gate.server.URL, githubSecret, copilotSecret); status != http.StatusOK {
			t.Errorf("phase B %s subsequent status = %d", name, status)
		}
	}
	if err := gate.setPhaseCell("A", "claude"); err != nil {
		t.Fatal(err)
	}
	providerFailure = true
	if status := sendLiveGateFixtureInference(t, client, gate.server.URL, githubSecret, copilotSecret); status != http.StatusBadRequest {
		t.Errorf("provider refusal status = %d", status)
	}
	if status := sendLiveGateFixtureInference(t, client, gate.server.URL, githubSecret, copilotSecret); status != http.StatusTooManyRequests {
		t.Errorf("frozen provider cell status = %d", status)
	}
	providerFailure = false
	for i := 1; i < 9; i++ {
		request, err := http.NewRequest(http.MethodGet, gate.server.URL+"/copilot_internal/v2/token", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "token "+githubSecret)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("fixture auth request failed")
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Errorf("auth request %d status = %d", i, response.StatusCode)
		}
	}
	_, inferenceCount, authCount, catalogCount, captureErr := gate.countsFor("A/codex")
	if inferenceCount != 16 || authCount != 9 || catalogCount != 5 || captureErr != nil || len(upstream) != 30 {
		t.Errorf("physical dispatch counts = inference:%d auth:%d catalog:%d transport:%d capture_error:%t", inferenceCount, authCount, catalogCount, len(upstream), captureErr != nil)
	}
	if upstream[0] != "https://api.github.com/copilot_internal/v2/token" || upstream[6] != "https://api.githubcopilot.com/responses" {
		t.Error("forwarding gate lost the original authenticated public origin")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) == 0 {
		t.Fatal("private gate captures were unavailable")
	}
	foundSemanticBody := false
	foundFailure := false
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "gate-") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Error("private gate capture permissions were not 0600")
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal("private gate capture could not be read")
		}
		if bytes.Contains(body, []byte(githubSecret)) || bytes.Contains(body, []byte(copilotSecret)) || bytes.Contains(body, []byte(liveCopilotClientKey)) {
			t.Fatal("private gate capture contained a credential")
		}
		var capture liveServerToolCapture
		if err := json.Unmarshal(body, &capture); err != nil {
			t.Fatal("private gate capture was not valid JSON")
		}
		if strings.Contains(capture.Request.Body, `"web_search_options"`) && capture.Request.Headers.Get("X-Semantic") == "preserved" {
			foundSemanticBody = true
		}
		if capture.Dispatched && capture.PublicTransportCoverage != liveServerToolTransportCoverage {
			t.Error("capture omitted the HTTP/1-only transport coverage limitation")
		}
		if capture.Response.Status == http.StatusBadRequest && strings.Contains(capture.Response.Body, "invalid_request_error") {
			foundFailure = true
		}
	}
	if !foundSemanticBody || !foundFailure {
		t.Error("private captures lost semantic request fields or the provider refusal")
	}
	reconstructed := newLiveServerToolGateWithLedger(t, t.TempDir(), directory, githubSecret)
	reconstructed.publicAPI, _ = url.Parse("https://api.githubcopilot.com")
	reconstructed.client.Transport = gate.client.Transport
	if err := reconstructed.setPhaseCell("A", "codex"); err != nil {
		t.Fatal(err)
	}
	if status := sendLiveGateFixtureInference(t, client, reconstructed.server.URL, githubSecret, copilotSecret); status != http.StatusOK {
		t.Errorf("reconstructed gate did not continue physical accounting: %d", status)
	}
	_, restartedInference, restartedAuth, restartedCatalog, restartedError := reconstructed.countsFor("A/codex")
	if restartedInference != 17 || restartedAuth != 9 || restartedCatalog != 5 || restartedError != nil {
		t.Error("reconstructed gate did not retain physical dispatch ledger")
	}
	if err := reconstructed.setPhaseCell("A", "claude"); err != nil {
		t.Fatal(err)
	}
	if status := sendLiveGateFixtureInference(t, client, reconstructed.server.URL, githubSecret, copilotSecret); status != http.StatusTooManyRequests {
		t.Errorf("reconstructed gate reused frozen cell: %d", status)
	}
}

func liveToolFixtureResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}
}

func sendLiveGateFixtureInference(t *testing.T, client *http.Client, base, githubSecret, copilotSecret string) int {
	t.Helper()
	body := []byte(`{"model":"gpt-6-luna","web_search_options":{"search_context_size":"low"},"private":"` + githubSecret + `"}`)
	request, err := http.NewRequest(http.MethodPost, base+"/responses", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+copilotSecret)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Semantic", "preserved")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("fixture inference request failed before gate response")
	}
	_ = response.Body.Close()
	return response.StatusCode
}

func TestLiveServerToolEvidenceRejectsUnprovenResults(t *testing.T) {
	tests := []struct {
		name   string
		family string
		body   string
		want   liveServerToolEvidence
	}{
		{
			name: "GPT search call with a source and citation", family: "gpt-responses-search",
			body: `{"object":"response","model":"gpt-6-luna","status":"completed","output":[{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"NASA Moon","sources":[{"url":"https://science.nasa.gov/moon/facts/"}]},"results":[{"url":"https://science.nasa.gov/moon/facts/","title":"Moon Facts"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"The Moon orbits Earth.","annotations":[{"type":"url_citation","url":"https://science.nasa.gov/moon/facts/"}]}]}]}`,
			want: liveServerToolEvidence{call: true, result: true, content: true, source: true, terminal: true, resultContent: true, actionSource: true, citation: true, pairedSourceResult: true, pairedCitation: true},
		},
		{
			name: "GPT source-only search has no full result", family: "gpt-responses-search",
			body: `{"object":"response","model":"gpt-6-luna","status":"completed","output":[{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"NASA Moon","sources":[{"url":"https://science.nasa.gov/moon/facts/"}]}},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Moon fact"}]}]}`,
			want: liveServerToolEvidence{call: true, source: true, terminal: true, actionSource: true},
		},
		{
			name: "GPT URL-only result is not content evidence", family: "gpt-responses-search",
			body: `{"object":"response","model":"gpt-6-luna","status":"completed","output":[{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","sources":[{"url":"https://science.nasa.gov/moon/facts/"}]},"results":[{"url":"https://science.nasa.gov/moon/facts/"}]}]}`,
			want: liveServerToolEvidence{call: true, source: true, terminal: true, actionSource: true},
		},
		{
			name: "GPT failed NASA call cannot borrow unrelated result", family: "gpt-responses-search",
			body: `{"object":"response","model":"gpt-6-luna","status":"completed","output":[{"type":"web_search_call","id":"ws_failed","status":"failed","action":{"type":"search","sources":[{"url":"https://science.nasa.gov/moon/facts/"}]}},{"type":"web_search_call","id":"ws_other","status":"completed","action":{"type":"search","sources":[{"url":"https://example.org/other"}]},"results":[{"url":"https://example.org/other","title":"Other page"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Moon fact","annotations":[{"type":"url_citation","url":"https://science.nasa.gov/moon/facts/"}]}]}]}`,
			want: liveServerToolEvidence{call: true, result: true, content: true, source: true, terminal: true, resultContent: true, actionSource: true, citation: true},
		},
		{
			name: "Claude fetch matching result", family: "claude-web-fetch",
			body: `{"type":"message","role":"assistant","model":"claude-haiku-5-5","stop_reason":"end_turn","content":[{"type":"server_tool_use","id":"srv_1","name":"web_fetch"},{"type":"web_fetch_tool_result","tool_use_id":"srv_1","content":{"type":"web_fetch_result","url":"https://science.nasa.gov/moon/facts/","content":{"type":"document","source":{"data":"Moon Facts"}}}}]}`,
			want: liveServerToolEvidence{call: true, result: true, content: true, source: true, terminal: true},
		},
		{
			name: "Claude fetch error is not a result", family: "claude-web-fetch",
			body: `{"type":"message","role":"assistant","model":"claude-haiku-5-5","content":[{"type":"server_tool_use","id":"srv_1","name":"web_fetch"},{"type":"web_fetch_tool_result","tool_use_id":"srv_1","content":{"type":"web_fetch_tool_result_error","error_code":"url_not_accessible"}}]}`,
			want: liveServerToolEvidence{call: true},
		},
		{
			name: "Claude code execution matching result", family: "claude-code-execution",
			body: `{"type":"message","role":"assistant","model":"claude-haiku-5-5","stop_reason":"end_turn","content":[{"type":"server_tool_use","id":"srv_2","name":"code_execution"},{"type":"code_execution_tool_result","tool_use_id":"srv_2","content":{"type":"bash_code_execution_result","return_code":0,"stdout":"385\n","stderr":""}}]}`,
			want: liveServerToolEvidence{call: true, result: true, content: true, terminal: true},
		},
		{
			name: "Claude native bash code execution matching result", family: "claude-code-execution",
			body: `{"type":"message","role":"assistant","model":"claude-haiku-5-5","stop_reason":"end_turn","content":[{"type":"server_tool_use","id":"srv_3","name":"bash_code_execution"},{"type":"bash_code_execution_tool_result","tool_use_id":"srv_3","content":{"type":"bash_code_execution_result","return_code":0,"stdout":"385\n","stderr":""}}]}`,
			want: liveServerToolEvidence{call: true, result: true, content: true, terminal: true},
		},
		{
			name: "GPT code interpreter matching result", family: "gpt-code-interpreter",
			body: `{"object":"response","model":"gpt-6-luna","status":"completed","output":[{"type":"code_interpreter_call","id":"ci_1","status":"completed","container_id":"cntr_1","outputs":[{"type":"logs","logs":"385"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"385"}]}]}`,
			want: liveServerToolEvidence{call: true, result: true, content: true, terminal: true},
		},
		{
			name: "GPT incomplete despite tool result", family: "gpt-code-interpreter",
			body: `{"object":"response","model":"gpt-6-luna","status":"incomplete","output":[{"type":"code_interpreter_call","id":"ci_1","status":"completed","container_id":"cntr_1","outputs":[{"type":"logs","logs":"385"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"385"}]}]}`,
			want: liveServerToolEvidence{call: true, result: true, content: true},
		},
		{
			name: "Claude unfinished despite fetch result", family: "claude-web-fetch",
			body: `{"type":"message","role":"assistant","model":"claude-haiku-5-5","stop_reason":"max_tokens","content":[{"type":"server_tool_use","id":"srv_1","name":"web_fetch"},{"type":"web_fetch_tool_result","tool_use_id":"srv_1","content":{"type":"web_fetch_result","url":"https://science.nasa.gov/moon/facts/","content":{"type":"document","source":{"data":"Moon Facts"}}}}]}`,
			want: liveServerToolEvidence{call: true, result: true, content: true, source: true},
		},
		{
			name: "Gemini citation alone is not tool execution", family: "gemini-chat-search",
			body: `{"object":"chat.completion","model":"gemini-3.8-flash","choices":[{"message":{"role":"assistant","content":"The Moon orbits Earth.","annotations":[{"type":"url_citation","url":"https://science.nasa.gov/moon/facts/"}]}}]}`,
			want: liveServerToolEvidence{content: true, source: true},
		},
		{
			name: "Gemini paired empty result is not evidence", family: "gemini-chat-search",
			body: `{"object":"chat.completion","model":"gemini-3.8-flash","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Moon fact","server_tool_events":[{"type":"server_tool_use","name":"web_search","id":"srv_1"},{"type":"web_search_tool_result","tool_use_id":"srv_1","content":""}]}}]}`,
			want: liveServerToolEvidence{call: true, content: true, terminal: true},
		},
		{
			name: "Gemini paired text result is evidence", family: "gemini-chat-search",
			body: `{"object":"chat.completion","model":"gemini-3.8-flash","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Moon fact","server_tool_events":[{"type":"server_tool_use","name":"web_search","id":"srv_1"},{"type":"web_search_tool_result","tool_use_id":"srv_1","content":"NASA Moon Facts"}]}}]}`,
			want: liveServerToolEvidence{call: true, result: true, content: true, terminal: true},
		},
		{
			name: "wrong Responses object fails schema", family: "gpt-responses-search",
			body: `{"object":"chat.completion","model":"gpt-6-luna","status":"completed","output":[]}`,
			want: liveServerToolEvidence{},
		},
	}
	for _, positive := range tests {
		if !strings.HasPrefix(positive.family, "gpt-") || !positive.want.content {
			continue
		}
		for _, discriminator := range []string{"refusal", "unknown"} {
			negative := positive
			negative.name += " rejects " + discriminator + " assistant part"
			negative.body = strings.Replace(negative.body, `"type":"output_text"`, `"type":"`+discriminator+`"`, 1)
			negative.want.content = false
			negative.want.citation = false
			negative.want.pairedCitation = false
			tests = append(tests, negative)
		}
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := inspectLiveServerToolResponse(test.family, []byte(test.body))
			if got != test.want {
				t.Errorf("tool evidence = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestLiveServerToolGateStreamsAndPreservesPartialFailure(t *testing.T) {
	directory := t.TempDir()
	gate := newLiveServerToolGate(t, directory)
	gate.publicAPI, _ = url.Parse("https://api.githubcopilot.com")
	if err := gate.setPhaseCell("B", "gpt-responses-search"); err != nil {
		t.Fatal(err)
	}
	upstreamReader, upstreamWriter := io.Pipe()
	defer upstreamWriter.CloseWithError(errors.New("fixture stopped"))
	gate.client.Transport = liveToolRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{Proto: "HTTP/2.0", StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": []string{"text/event-stream"}, "Content-Encoding": []string{"identity"}}, Body: upstreamReader, ContentLength: -1}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, gate.server.URL+"/responses", strings.NewReader(`{"model":"gpt-6-luna"}`))
	if err != nil {
		t.Fatal(err)
	}
	responses := make(chan *http.Response, 1)
	errorsFromClient := make(chan error, 1)
	go func() {
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			errorsFromClient <- err
			return
		}
		responses <- response
	}()
	first := "event: response.failed\ndata: {\"error\":\"provider refused\"}\n\n"
	writeDone := make(chan error, 1)
	go func() { _, err := io.WriteString(upstreamWriter, first); writeDone <- err }()
	var response *http.Response
	select {
	case response = <-responses:
	case <-errorsFromClient:
		t.Fatal("streaming fixture failed to receive headers")
	case <-ctx.Done():
		t.Fatal("streaming fixture timed out before headers")
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	got := make([]byte, len(first))
	if _, err := io.ReadFull(reader, got); err != nil {
		t.Fatal("first SSE chunk was not forwarded before upstream completion")
	}
	if err := <-writeDone; err != nil {
		t.Fatal("upstream first chunk could not be written")
	}
	if string(got) != first {
		t.Error("first SSE chunk changed during forwarding")
	}
	_ = upstreamWriter.CloseWithError(errors.New("fixture upstream read failed"))
	_, _ = io.ReadAll(reader)
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "gate-") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var capture liveServerToolCapture
		if json.Unmarshal(data, &capture) != nil {
			t.Fatal("partial failure capture was invalid")
		}
		if capture.Category == "inference" {
			found = true
			if capture.Response.Proto != "HTTP/2.0" || capture.Response.ContentEncoding != "identity" || capture.ErrorClass != "upstream_response_read_error" || capture.Response.Body != first {
				t.Error("partial provider failure or upstream framing was lost")
			}
		}
	}
	if !found {
		t.Error("partial response was not captured")
	}
}

func TestLiveServerToolGateRejectsOriginRefresh(t *testing.T) {
	gate := newLiveServerToolGate(t, t.TempDir())
	first := []byte(`{"token":"t1","endpoints":{"api":"https://api.githubcopilot.com"}}`)
	second := []byte(`{"token":"t2","endpoints":{"api":"https://proxy.githubcopilot.com"}}`)
	if _, err := gate.rewriteTokenEndpoint(first); err != nil {
		t.Fatal("first public origin was rejected")
	}
	if _, err := gate.rewriteTokenEndpoint(second); err == nil {
		t.Error("changed public origin was silently accepted")
	}
}

func TestLiveServerToolGateRejectsUnconfiguredDispatch(t *testing.T) {
	for _, test := range []struct {
		name, path   string
		origin, cell bool
	}{
		{"unknown phase", "/responses", true, false},
		{"missing inference origin", "/responses", false, true},
		{"missing catalog origin", "/models", false, true},
		{"unknown path", "/unexpected", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			gate := newLiveServerToolGate(t, t.TempDir())
			if test.origin {
				gate.publicAPI, _ = url.Parse("https://api.githubcopilot.com")
			}
			if test.cell {
				if err := gate.setPhaseCell("A", "codex"); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, _, allowed := gate.reserve(test.path); allowed {
				t.Error("unconfigured dispatch was permitted")
			}
			count, inference, auth, catalog, err := gate.countsFor("A/codex")
			if err != nil || count != 0 || inference != 0 || auth != 0 || catalog != 0 {
				t.Error("unconfigured reservation changed physical dispatch accounting")
			}
		})
	}
}

func TestLiveServerToolGateTruncatedCaptureStopsFurtherDispatch(t *testing.T) {
	gate := newLiveServerToolGate(t, t.TempDir())
	gate.publicAPI, _ = url.Parse("https://api.githubcopilot.com")
	if err := gate.setPhaseCell("B", "gpt-responses-search"); err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(released) }) }
	defer release()
	var upstreamCalls atomic.Int32
	gate.client.Transport = liveToolRoundTrip(func(request *http.Request) (*http.Response, error) {
		upstreamCalls.Add(1)
		reader, writer := io.Pipe()
		go func() {
			_, _ = io.WriteString(writer, strings.Repeat("x", (16<<20)+1))
			<-released
			_ = writer.Close()
		}()
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: reader, ContentLength: -1}, nil
	})
	request, err := http.NewRequest(http.MethodPost, gate.server.URL+"/responses", strings.NewReader(`{"model":"gpt-6-luna"}`))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	first, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Body.Close()
	if n, err := io.CopyN(io.Discard, first.Body, (16<<20)+1); err != io.EOF || n != 16<<20 {
		t.Fatalf("truncated stream forwarded %d bytes with error %v, want bounded prefix and EOF", n, err)
	}
	// The upstream is paused before EOF, so no completed capture exists yet.
	if status := sendLiveGateFixtureInference(t, client, gate.server.URL, "synthetic-github-token", "synthetic-copilot-token"); status != http.StatusTooManyRequests {
		t.Fatalf("request while truncated stream is open status = %d", status)
	}
	cell, total, _, _, captureErr := gate.countsFor("B/gpt-responses-search")
	if cell != 1 || total != 1 || captureErr == nil || upstreamCalls.Load() != 1 {
		t.Fatal("known truncation did not stop dispatch before stream completion")
	}
	release()
	if _, err := io.Copy(io.Discard, first.Body); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	var capture liveServerToolCapture
	for {
		paths, err := filepath.Glob(filepath.Join(gate.directory, "gate-*.json"))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, path := range paths {
			body, err := os.ReadFile(path)
			if err == nil && json.Unmarshal(body, &capture) == nil && capture.Dispatched && capture.Category == "inference" {
				found = true
				break
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("partial capture was not retained after EOF")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !capture.CaptureTruncated || len(capture.Response.Body) != 16<<20 {
		t.Fatal("partial response and truncation marker were not retained")
	}
}

func TestLiveServerToolGateCountsSDKAuthenticationRecovery(t *testing.T) {
	gate := newLiveServerToolGate(t, t.TempDir())
	gate.publicAPI, _ = url.Parse("https://api.githubcopilot.com")
	if err := gate.setPhaseCell("B", "gpt-code-interpreter"); err != nil {
		t.Fatal(err)
	}
	inferenceCalls := 0
	gate.client.Transport = liveToolRoundTrip(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/copilot_internal/v2/token" {
			return liveToolFixtureResponse(http.StatusOK, `{"token":"synthetic-refreshed-token","endpoints":{"api":"https://api.githubcopilot.com"}}`), nil
		}
		inferenceCalls++
		if inferenceCalls == 1 {
			return liveToolFixtureResponse(http.StatusUnauthorized, `{"error":{"code":"invalid_token"}}`), nil
		}
		if inferenceCalls == 2 {
			return liveToolFixtureResponse(http.StatusOK, `{"ok":true}`), nil
		}
		return liveToolFixtureResponse(http.StatusForbidden, `{"error":{"code":"refused"}}`), nil
	})
	client := &http.Client{}
	for index, want := range []int{http.StatusUnauthorized, http.StatusOK, http.StatusForbidden, http.StatusTooManyRequests} {
		if status := sendLiveGateFixtureInference(t, client, gate.server.URL, "synthetic-github-token", "synthetic-copilot-token"); status != want {
			t.Fatalf("inference step %d status = %d, want %d", index, status, want)
		}
		if index == 0 {
			response, err := client.Get(gate.server.URL + "/copilot_internal/v2/token")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("authentication recovery status = %d", response.StatusCode)
			}
		}
	}
	cell, total, auth, catalog, captureErr := gate.countsFor("B/gpt-code-interpreter")
	if cell != 3 || total != 3 || auth != 1 || catalog != 0 || captureErr != nil || inferenceCalls != 3 {
		t.Fatalf("recovery accounting = cell %d total %d auth %d catalog %d capture_error %t", cell, total, auth, catalog, captureErr != nil)
	}
	body, err := os.ReadFile(filepath.Join(gate.directory, "gate-001.json"))
	if err != nil {
		t.Fatal(err)
	}
	var capture liveServerToolCapture
	if err := json.Unmarshal(body, &capture); err != nil {
		t.Fatal(err)
	}
	if capture.Response.Status != http.StatusUnauthorized || !strings.Contains(capture.Response.Body, "invalid_token") {
		t.Fatal("authentication failure body was lost after successful recovery")
	}
}

func TestLiveServerToolGateHistoricalPhysicalAccountingAndPersistence(t *testing.T) {
	ledgerDirectory := t.TempDir()
	gate := newLiveServerToolGateWithLedger(t, t.TempDir(), ledgerDirectory)
	gate.publicAPI, _ = url.Parse("https://api.githubcopilot.com")
	var dispatched atomic.Int32
	transport := liveToolRoundTrip(func(request *http.Request) (*http.Response, error) {
		dispatched.Add(1)
		if request.URL.Path == "/copilot_internal/v2/token" {
			return liveToolFixtureResponse(http.StatusOK, `{"token":"offline","endpoints":{"api":"https://api.githubcopilot.com"}}`), nil
		}
		return liveToolFixtureResponse(http.StatusOK, `{"ok":true}`), nil
	})
	gate.client.Transport = transport
	if err := gate.withLedger(func(ledger *liveDispatchLedger) error {
		ledger.Inference, ledger.Auth, ledger.Catalog = 43, 8, 4
		ledger.Counts["A/codex"], ledger.Counts["B/gpt-responses-search"] = 4, 1
		return nil
	}); err != nil {
		t.Fatal("could not seed offline historical dispatch ledger")
	}
	client := &http.Client{}
	for _, cell := range []struct{ phase, name string }{{"A", "codex"}, {"B", "gpt-responses-search"}} {
		if err := gate.setPhaseCell(cell.phase, cell.name); err != nil {
			t.Fatal(err)
		}
		if status := sendLiveGateFixtureInference(t, client, gate.server.URL, "offline", "offline"); status != http.StatusOK {
			t.Errorf("dispatch beyond historical counts status = %d", status)
		}
	}
	for _, path := range []string{"/copilot_internal/v2/token", "/models"} {
		response, err := client.Get(gate.server.URL + path)
		if err != nil {
			t.Fatal("offline counted request did not reach the gate")
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Errorf("dispatch beyond historical counts for %s status = %d", path, response.StatusCode)
		}
	}
	reconstructed := newLiveServerToolGateWithLedger(t, t.TempDir(), ledgerDirectory)
	reconstructed.publicAPI, _ = url.Parse("https://api.githubcopilot.com")
	reconstructed.client.Transport = transport
	if err := reconstructed.setPhaseCell("B", "gpt-responses-search"); err != nil {
		t.Fatal(err)
	}
	if status := sendLiveGateFixtureInference(t, client, reconstructed.server.URL, "offline", "offline"); status != http.StatusOK {
		t.Errorf("reconstructed counted dispatch status = %d", status)
	}
	for _, invalid := range []struct{ phase, cell string }{{"unknown", "codex"}, {"A", "unknown"}, {"", ""}} {
		if err := reconstructed.setPhaseCell(invalid.phase, invalid.cell); err == nil {
			t.Error("unknown phase cell was accepted")
		}
	}
	response, err := client.Get(reconstructed.server.URL + "/unexpected")
	if err != nil {
		t.Fatal("offline unknown-path request did not reach the gate")
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusTooManyRequests {
		t.Errorf("unknown path was dispatched with status = %d", response.StatusCode)
	}
	count, total, auth, catalog, captureErr := reconstructed.countsFor("B/gpt-responses-search")
	if captureErr != nil || count != 3 || total != 46 || auth != 9 || catalog != 5 || dispatched.Load() != 5 {
		t.Errorf("persisted accounting = cell:%d inference:%d auth:%d catalog:%d transport:%d capture_error:%t", count, total, auth, catalog, dispatched.Load(), captureErr != nil)
	}
}
