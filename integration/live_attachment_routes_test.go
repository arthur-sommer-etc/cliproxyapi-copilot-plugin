//go:build !windows

package integration

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAttachmentToolTerminalAtOriginalClientHTTPBoundary(t *testing.T) {
	messages := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-haiku-5.5\",\"usage\":{\"input_tokens\":12}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call-render\",\"name\":\"exec_command\",\"input\":{}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"cmd\\\":\\\"printf CPA_CONTINUATION_OK\\\"}\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":5}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	thinking := "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"synthetic thought\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"synthetic-owned\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n"
	signedMessages := strings.Replace(messages, "event: message_delta", thinking+"event: message_delta", 1)
	signatureOnly := strings.Replace(signedMessages, `"thinking":"synthetic thought"`, `"thinking":""`, 1)
	if liveCompleteMessagesTerminal([]byte(strings.Replace(signatureOnly, `"signature":"synthetic-owned"`, `"signature":""`, 1)), "claude-haiku-5.5") {
		t.Fatal("empty thinking without a meaningful signature established stream proof")
	}
	responses := "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"status\":\"completed\",\"id\":\"fc_call-render\",\"call_id\":\"call-render\",\"name\":\"exec_command\",\"arguments\":\"{\\\"cmd\\\":\\\"printf CPA_CONTINUATION_OK\\\"}\"}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-6-luna\",\"usage\":{\"input_tokens\":12,\"output_tokens\":5},\"output\":[{\"type\":\"function_call\",\"status\":\"completed\",\"id\":\"fc_call-render\",\"call_id\":\"call-render\",\"name\":\"exec_command\",\"arguments\":\"{\\\"cmd\\\":\\\"printf CPA_CONTINUATION_OK\\\"}\"}]}}\n\n"
	for _, fixture := range []struct{ client, path, model, body string }{
		{"claude", "/v1/messages", "claude-haiku-5.5", messages},
		{"claude", "/v1/messages", "claude-haiku-5.5", signedMessages},
		{"claude", "/v1/messages", "claude-haiku-5.5", signatureOnly},
		{"codex", "/v1/responses", "gpt-6-luna", responses},
	} {
		t.Run(fixture.client, func(t *testing.T) {
			localHost := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(writer, fixture.body)
			}))
			defer localHost.Close()
			gate := newLiveServerToolGate(t, t.TempDir())
			ingress := newLiveAttachmentIngress(t, gate, localHost.URL, t.TempDir(), fixture.client)
			defer ingress.Close()
			requestBody := fmt.Sprintf(`{"model":%q,"stream":true}`, fixture.model)
			for turn := 0; turn < 2; turn++ {
				response, err := http.Post(ingress.URL()+fixture.path, "application/json", strings.NewReader(requestBody))
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(response.Body)
				closeErr := response.Body.Close()
				if err != nil || closeErr != nil || response.StatusCode != http.StatusOK || string(body) != fixture.body {
					t.Fatal("complete tool terminal was not forwarded intact")
				}
				var captures []liveAttachmentIngressCapture
				deadline := time.Now().Add(2 * time.Second)
				for time.Now().Before(deadline) {
					captures, err = ingress.captures()
					if err == nil && len(captures) == turn+1 && captures[turn].ResponseBodyComplete {
						break
					}
					time.Sleep(time.Millisecond)
				}
				if err != nil || len(captures) != turn+1 {
					t.Fatal("tool response capture was not durable")
				}
				capture := captures[turn]
				denials, failure := ingress.state()
				if !capture.StreamTerminalComplete || !capture.StreamTerminalFunction || !capture.StreamTerminalForwarded || !capture.ResponseBodyComplete || denials != 0 || failure != nil {
					t.Fatal("tool terminal lost readiness or completion/carrier observations")
				}
				if fixture.client == "codex" {
					call := liveAttachmentOriginalToolCalls(captures)["call-render"]
					if call.ItemID != "fc_call-render" || call.CallID != "call-render" || call.Name != "exec_command" || call.Arguments["cmd"] != "printf CPA_CONTINUATION_OK" {
						t.Fatal("original-format Responses carrier identity or arguments were lost")
					}
				}
			}
		})
	}
}

func TestAttachmentHaikuPDFRendererAdmissionAtPublicHTTPBoundary(t *testing.T) {
	candidate := liveAttachmentCases()[3]
	candidate.fixture = filepath.Join(t.TempDir(), "document.pdf")
	var dispatched atomic.Int32
	peer := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		dispatched.Add(1)
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"id\":\"chat-render\",\"object\":\"chat.completion.chunk\",\"model\":\"claude-haiku-5.5\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-view\",\"type\":\"function\",\"function\":{\"name\":\"view_image\",\"arguments\":\"{\\\"path\\\":\\\"page-1.png\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":5,\"total_tokens\":17}}\n\ndata: [DONE]\n\n")
	}))
	defer peer.Close()
	gate := newLiveServerToolGate(t, t.TempDir())
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
	if err := gate.setPhaseCell("X-fixture", candidate.cell); err != nil {
		t.Fatal(err)
	}
	gate.setAttachmentFixture("X-fixture/"+candidate.cell, candidate.fixture)
	result := map[string]any{"role": "tool", "tool_call_id": "call-render", "content": "Chunk ID: fixture\nWall time: 1 seconds\nProcess exited with code 0\nOutput:\npage-1.png\n"}
	arguments, err := json.Marshal(map[string]any{"cmd": "pdftoppm -png " + candidate.fixture + " page && ls"})
	if err != nil {
		t.Fatal(err)
	}
	request := map[string]any{
		"model": candidate.model, "stream": true, "reasoning_effort": "medium",
		"messages": []any{
			map[string]any{"role": "user", "content": "<cwd>" + filepath.Dir(candidate.fixture) + "</cwd>\n" + liveAttachmentPrompt(candidate)},
			map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "call-render", "type": "function", "function": map[string]any{"name": "exec_command", "arguments": string(arguments)}}}},
			result,
		},
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "exec_command"}}, map[string]any{"type": "function", "function": map[string]any{"name": "view_image"}}},
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	for turn, status := range []int{http.StatusOK, http.StatusBadRequest, http.StatusTooManyRequests} {
		if turn == 1 {
			result["tool_call_id"] = "foreign-call"
		}
		if turn == 2 {
			result["tool_call_id"] = "call-render"
		}
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Post(gate.server.URL+"/chat/completions", "application/json", strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		_, readErr := io.Copy(io.Discard, response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil || response.StatusCode != status {
			t.Fatalf("HTTP route admission turn %d: status=%d want=%d", turn, response.StatusCode, status)
		}
	}
	if dispatched.Load() != 1 || liveAttachmentReadinessError(gate) == nil {
		t.Fatal("uncorrelated renderer result did not stop subsequent public dispatch")
	}
}

func TestAttachmentGeminiPDFChatPreparationUsesStagedSource(t *testing.T) {
	var candidate liveAttachmentCase
	for _, current := range liveAttachmentCases() {
		if current.cell == "codex-gemini-pdf" {
			candidate = current
		}
	}
	candidate.fixture = filepath.Join(t.TempDir(), "document.pdf")
	request := map[string]any{
		"model": candidate.model, "stream": true, "reasoning_effort": "medium",
		"messages": []any{map[string]any{"role": "user", "content": "<cwd>" + filepath.Dir(candidate.fixture) + "</cwd>\n" + liveAttachmentPrompt(candidate)}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "exec_command"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "view_image"}},
		},
	}
	for _, continuation := range []bool{false, true} {
		if continuation {
			arguments, err := json.Marshal(map[string]any{"cmd": "pdftoppm -png " + candidate.fixture + " page && ls"})
			if err != nil {
				t.Fatal(err)
			}
			request["messages"] = append(request["messages"].([]any),
				map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "call-render", "type": "function", "function": map[string]any{"name": "exec_command", "arguments": string(arguments)}}}},
				map[string]any{"role": "tool", "tool_call_id": "call-render", "content": "Chunk ID: fixture\nWall time: 1 seconds\nProcess exited with code 0\nOutput:\npage-1.png\n"},
			)
		}
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		if !liveAttachmentPreparedPublicRequestForFixture("X-final/"+candidate.cell, "/chat/completions", body, candidate.fixture) {
			t.Fatal("exact-source Gemini PDF preparation denied before view_image")
		}
		if liveAttachmentPreparedPublicRequestForFixture("X-final/"+candidate.cell, "/chat/completions", body, candidate.fixture+"-other") {
			t.Fatal("foreign PDF source admitted")
		}
	}
}

func TestAttachmentFinalCompletionUsesObservedProtocol(t *testing.T) {
	for _, candidate := range liveAttachmentCases() {
		if candidate.modelPath == "/chat/completions" {
			continue
		}
		t.Run(candidate.cell, func(t *testing.T) {
			body := fmt.Sprintf("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"object\":\"response\",\"status\":\"completed\",\"model\":%q,\"usage\":{\"input_tokens\":12,\"output_tokens\":5},\"output\":[{\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"The top is red and the bottom is blue.\"}]}]}}\n\n", candidate.model)
			if candidate.modelPath == "/v1/messages" {
				body = fmt.Sprintf(`{"type":"message","role":"assistant","model":%q,"stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":5},"content":[{"type":"text","text":"The top is red and the bottom is blue."}]}`, candidate.model)
			}
			if !liveAttachmentCapturedResponseCompleted(body, candidate, candidate.modelPath) {
				t.Fatal("complete final text was rejected based on client rather than observed protocol")
			}
			if liveAttachmentCapturedResponseCompleted(body, candidate, "/chat/completions") {
				t.Fatal("foreign-protocol final response was accepted")
			}
		})
	}
}

func TestAttachmentAdmissionUsesPublicProtocolRatherThanClient(t *testing.T) {
	for _, cell := range []string{"claude-claude-png", "claude-claude-pdf", "codex-gpt-png", "codex-gpt-pdf"} {
		t.Run(cell, func(t *testing.T) {
			var candidate liveAttachmentCase
			for _, current := range liveAttachmentCases() {
				if current.cell == cell {
					candidate = current
				}
			}
			if candidate.cell == "" {
				t.Fatal("required qualification cell missing")
			}
			effort := `"reasoning":{"effort":"medium"}`
			wrongEffort := `"output_config":{"effort":"medium"}`
			if candidate.modelPath == "/v1/messages" {
				effort, wrongEffort = wrongEffort, effort
			}
			body := []byte(fmt.Sprintf(`{"model":%q,%s}`, candidate.model, effort))
			if !liveAttachmentPreparedPublicRequest("X-final/"+cell, candidate.modelPath, body) {
				t.Fatal("correct native protocol and effort denied")
			}
			for _, invalid := range []string{
				fmt.Sprintf(`{"model":%q,%s}`, candidate.model, wrongEffort),
				fmt.Sprintf(`{"model":"wrong-model",%s}`, effort),
				fmt.Sprintf(`{"model":%q,"reasoning":{"effort":"low"},"output_config":{"effort":"low"}}`, candidate.model),
			} {
				if liveAttachmentPreparedPublicRequest("X-final/"+cell, candidate.modelPath, []byte(invalid)) {
					t.Fatal("wrong model or effort admitted")
				}
			}
			endpoints := liveAttachmentExpectedPublicEndpoints(candidate)
			if len(endpoints) != 1 || endpoints[0] != candidate.modelPath {
				t.Fatalf("unexpected native route claim: %v", endpoints)
			}
		})
	}
}
