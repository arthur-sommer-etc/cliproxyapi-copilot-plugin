//go:build !windows

package integration

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAttachmentHaikuPDFChatBootstrapAtOwnedBoundaries(t *testing.T) {
	binary := os.Getenv("CPA_BINARY")
	if binary == "" {
		t.Skip("set CPA_BINARY to the matched native host for owned boundary replay")
	}
	if attachmentMaintainedPoppler == "" {
		t.Skip("set CPA_ATTACHMENT_PDF_RENDERER to the installed renderer for source-bound PDF replay")
	}
	candidate := liveAttachmentCases()[3]
	source, err := os.ReadFile(candidate.fixture)
	if err != nil {
		t.Fatal(err)
	}
	candidate.fixture = filepath.Join(t.TempDir(), "document.pdf")
	if err := os.WriteFile(candidate.fixture, source, 0600); err != nil {
		t.Fatal(err)
	}
	render := exec.Command(attachmentMaintainedPoppler, "-png", candidate.fixture, "page")
	render.Dir = filepath.Dir(candidate.fixture)
	if body, err := render.CombinedOutput(); err != nil {
		t.Fatalf("exact-source offline render failed: %v: %s", err, body)
	}
	pagePath := filepath.Join(filepath.Dir(candidate.fixture), "page-1.png")
	page, err := os.ReadFile(pagePath)
	if err != nil {
		t.Fatal(err)
	}
	candidate.expectedPageSHA256 = livePacketFileSHA256(t, pagePath)
	state := newNativeFixture(t)
	state.modelCatalog = bytes.ReplaceAll(state.modelCatalog, []byte("bridge-sonnet-5.5"), []byte(candidate.model))
	var dispatched atomic.Int32
	peer := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/copilot_internal/v2/token" {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"token":"fixture-copilot-token","expires_at":4102444800,"endpoints":{"api":"https://api.githubcopilot.com"}}`)
			return
		}
		if request.URL.Path != "/chat/completions" {
			state.ServeHTTP(writer, request)
			return
		}
		turn := dispatched.Add(1)
		writer.Header().Set("Content-Type", "text/event-stream")
		name, id, finish := "exec_command", "render-owned", "tool_calls"
		arguments := map[string]any{"cmd": "pdftoppm -png " + candidate.fixture + " page && ls"}
		if turn == 2 {
			name, id = "view_image", "view-owned"
			arguments = map[string]any{"path": pagePath}
		}
		encoded, _ := json.Marshal(arguments)
		delta := map[string]any{"role": "assistant", "reasoning_opaque": "synthetic-chat-owned-" + id, "tool_calls": []any{map[string]any{"index": 0, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(encoded)}}}}
		if turn == 3 {
			delta = map[string]any{"role": "assistant", "content": "The printed code is Q7B9 and the element is blue."}
			finish = "stop"
		}
		frame := map[string]any{"id": "chat-" + id, "object": "chat.completion.chunk", "model": candidate.model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}, "usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 5, "total_tokens": 17}}
		choices := frame["choices"].([]any)
		choices[0].(map[string]any)["finish_reason"] = nil
		usage := frame["usage"]
		delete(frame, "usage")
		_, _ = io.WriteString(writer, liveChatTestFrame(t, frame))
		frame["choices"] = []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}
		frame["usage"] = usage
		_, _ = io.WriteString(writer, liveChatTestFrame(t, frame)+"data: [DONE]\n\n")
	}))
	defer peer.Close()
	gate := newLiveServerToolGate(t, t.TempDir())
	transport := peer.Client().Transport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig.ServerName = "127.0.0.1"
	transport.DialContext = liveServerToolFixtureDialer(peer, "api.github.com:443", "api.githubcopilot.com:443")
	gate.client.Transport = liveOneShotTransport{base: transport}
	gate.publicAPI, err = url.Parse("https://api.githubcopilot.com")
	if err != nil {
		t.Fatal(err)
	}
	base := startProxyWithConfiguredEndpointOverride(t, binary, gate.server.URL, candidate.model, "/chat/completions")
	cell := "X-owned-chat/" + candidate.cell
	if err := gate.setPhaseCell("X-owned-chat", candidate.cell); err != nil {
		t.Fatal(err)
	}
	gate.setAttachmentFixture(cell, candidate.fixture)
	ingress := newLiveAttachmentIngress(t, gate, base, t.TempDir(), "codex")
	payload := map[string]any{
		"model": candidate.model, "stream": true, "reasoning": map[string]any{"effort": "medium"},
		"input": []any{map[string]any{"role": "user", "content": "<cwd>" + filepath.Dir(candidate.fixture) + "</cwd>\n" + liveAttachmentPrompt(candidate)}},
		"tools": []any{
			map[string]any{"type": "function", "name": "exec_command", "parameters": map[string]any{"type": "object", "properties": map[string]any{"cmd": map[string]any{"type": "string"}}}},
			map[string]any{"type": "function", "name": "view_image", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}},
		},
	}
	originalReasoning := liveAttachmentReasoningTrace{}
	for turn := 1; turn <= 3; turn++ {
		requestBody, _ := json.Marshal(payload)
		status, body := postProxyWithSession(t, ingress.URL()+"/v1/responses", payload, "synthetic-owned-pdf")
		if status != http.StatusOK || !liveCompleteResponseTerminal(body, candidate.model) || dispatched.Load() != int32(turn) {
			t.Fatalf("owned PDF turn %d failed at ingress/host/gate: status=%d dispatched=%d terminal=%t", turn, status, dispatched.Load(), liveCompleteResponseTerminal(body, candidate.model))
		}
		originalReasoning.observe(string(requestBody), string(body), "/responses")
		var output []any
		for _, event := range attachmentStreamObjects(string(body)) {
			if event["type"] == "response.completed" {
				response, _ := event["response"].(map[string]any)
				output, _ = response["output"].([]any)
			}
		}
		if turn == 3 {
			if !originalReasoning.complete() || !strings.Contains(string(body), "Q7B9") || !strings.Contains(string(body), "blue") {
				t.Fatal("cumulative owned reasoning or meaningful final PDF answer was lost")
			}
			break
		}
		inputs := append(payload["input"].([]any), output...)
		for _, raw := range output {
			item, _ := raw.(map[string]any)
			if item["type"] != "function_call" {
				continue
			}
			wantName, wantID := "exec_command", "render-owned"
			var result any = "Chunk ID: fixture\nWall time: 1 seconds\nProcess exited with code 0\nOutput:\npage-1.png\n"
			if turn == 2 {
				wantName, wantID = "view_image", "view-owned"
				result = []any{map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(page), "detail": "high"}}
			}
			if item["name"] != wantName || item["call_id"] != wantID || item["id"] != "fc_"+wantID {
				t.Fatal("original Responses tool identity changed")
			}
			inputs = append(inputs, map[string]any{"type": "function_call_output", "call_id": wantID, "output": result})
		}
		payload["input"] = inputs
	}
	captures, err := ingress.captures()
	if err != nil || len(captures) != 3 {
		t.Fatal("full multi-turn ingress captures were not durable")
	}
	evidence := inspectLiveAttachmentEvidence(t, gate, candidate, cell, liveAttachmentCLI{args: []string{"--cd", filepath.Dir(candidate.fixture)}}, captures)
	inspectLiveOriginalClientAttachment(candidate, captures, &evidence)
	if !evidence.OriginalReasoningReplay || !evidence.IngressPrepared || !evidence.PublicModel || !evidence.PublicEffort || !evidence.PublicEndpoint || !liveAttachmentMediaPrepared(candidate, evidence) || !evidence.PublicReasoningReplay || !evidence.PublicToolResult || !evidence.PublicTerminal || !evidence.PublicStreamProven || evidence.PublicMediaSHA256 != candidate.expectedPageSHA256 || evidence.PhysicalInference != 3 {
		t.Fatalf("composed public proof incomplete: reasoning=%t tools=%t terminal=%t stream=%t media=%t dispatches=%d", evidence.PublicReasoningReplay, evidence.PublicToolResult, evidence.PublicTerminal, evidence.PublicStreamProven, evidence.PublicMediaSHA256 == candidate.expectedPageSHA256, evidence.PhysicalInference)
	}
	for _, mutation := range []string{"tampered", "native-foreign", "wrong-model", "misanchored-tool"} {
		t.Run(mutation, func(t *testing.T) {
			encoded, _ := json.Marshal(payload)
			var changed map[string]any
			if err := json.Unmarshal(encoded, &changed); err != nil {
				t.Fatal(err)
			}
			walkAttachmentJSON(changed, func(item map[string]any) {
				if item["type"] == "reasoning" {
					switch mutation {
					case "tampered":
						item["encrypted_content"] = attachmentString(item, "encrypted_content") + "tampered"
					case "native-foreign":
						item["encrypted_content"] = "synthetic-native-messages-signature"
					}
				}
				if mutation == "misanchored-tool" && item["type"] == "function_call" {
					item["arguments"] = `{}`
				}
			})
			if mutation == "wrong-model" {
				changed["model"] = "gpt-6-luna"
			}
			negativeIngress := newLiveAttachmentIngress(t, gate, base, t.TempDir(), "codex")
			status, _ := postProxyWithSession(t, negativeIngress.URL()+"/v1/responses", changed, "synthetic-owned-pdf")
			if status != http.StatusUnprocessableEntity || dispatched.Load() != 3 {
				t.Fatalf("invalid owned-history mutation did not fail closed before dispatch: status=%d dispatches=%d", status, dispatched.Load())
			}
		})
	}
}

func TestAttachmentInstalledClientOwnedBoundaryReplay(t *testing.T) {
	if os.Getenv("CPA_ATTACHMENT_OWNED_CLIENT_REPLAY") != "1" {
		t.Skip("set CPA_ATTACHMENT_OWNED_CLIENT_REPLAY=1 for installed-client loopback-only boundary replay")
	}
	binary := os.Getenv("CPA_BINARY")
	if binary == "" || attachmentMaintainedPoppler == "" || attachmentMaintainedFonts == "" {
		t.Fatal("owned replay requires the matched host and installed renderer/font configuration")
	}
	selected, err := liveAttachmentSelectedCases(os.Getenv("CPA_ATTACHMENT_OWNED_REPLAY_CASES"))
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range selected {
		t.Run(candidate.cell, func(t *testing.T) {
			root := t.TempDir()
			if output := os.Getenv("CPA_ATTACHMENT_OWNED_REPLAY_OUTPUT_DIR"); output != "" {
				info, err := os.Stat(output)
				if err != nil || !filepath.IsAbs(output) || !info.IsDir() || info.Mode().Perm() != 0700 {
					t.Fatal("owned replay evidence requires an existing private absolute directory")
				}
				root, err = os.MkdirTemp(output, candidate.cell+"-")
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := liveAttachmentFixtureValid(candidate); err != nil {
				t.Fatal(err)
			}
			rendererHash := liveAttachmentPopplerPreflight(t, candidate, root)
			if candidate.client == "codex" && candidate.media == "pdf" {
				candidate.expectedPageSHA256 = livePacketFileSHA256(t, filepath.Join(root, "offline-render-probe-codex", "page-1.png"))
			}
			state := newNativeFixture(t)
			state.modelCatalog = bytes.ReplaceAll(state.modelCatalog, []byte("bridge-sonnet-5.5"), []byte("claude-haiku-5.5"))
			var gate *liveServerToolGate
			var dispatched atomic.Int32
			peer := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/copilot_internal/v2/token" {
					writer.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(writer, `{"token":"fixture-copilot-token","expires_at":4102444800,"endpoints":{"api":"https://api.githubcopilot.com"}}`)
					return
				}
				if request.URL.Path == "/models" || request.URL.Path == "/user" {
					state.ServeHTTP(writer, request)
					return
				}
				if request.URL.Path != candidate.modelPath {
					http.Error(writer, "unexpected synthetic protocol", http.StatusBadRequest)
					return
				}
				turn := int(dispatched.Add(1))
				if turn > 4 {
					http.Error(writer, "offline replay dispatch bound exceeded", http.StatusTooManyRequests)
					return
				}
				gate.mu.Lock()
				fixture := gate.attachmentFixtures[gate.phaseCell]
				gate.mu.Unlock()
				current := candidate
				current.fixture = fixture
				writeAttachmentOwnedFixtureStream(t, writer, current, turn)
			}))
			defer peer.Close()
			gate = newLiveServerToolGate(t, filepath.Join(root, "gate"))
			transport := peer.Client().Transport.(*http.Transport).Clone()
			transport.Proxy = nil
			transport.TLSClientConfig.ServerName = "127.0.0.1"
			transport.DialContext = liveServerToolFixtureDialer(peer, "api.github.com:443", "api.githubcopilot.com:443")
			gate.client.Transport = liveOneShotTransport{base: transport}
			gate.publicAPI, err = url.Parse("https://api.githubcopilot.com")
			if err != nil {
				t.Fatal(err)
			}
			base, stop := startLiveNativeHostWithGate(t, binary, nativeAuthFixtureJSON(t), "token_exchange", map[string]string{candidate.model: candidate.modelPath}, gate.server.URL)
			defer stop()
			cell := "X-owned-client/" + candidate.cell
			if err := gate.withLedger(func(ledger *liveDispatchLedger) error {
				if ledger.Budgets == nil {
					ledger.Budgets = make(map[string]liveDispatchBudget)
				}
				ledger.Budgets["owned-client"] = liveDispatchBudget{Cells: map[string]int{cell: 4}, Limit: 4, Provenance: "offline installed-client native-host loopback fixture"}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			liveRunAttachmentCase(t, gate, base, root, candidate, cell, "owned-client", rendererHash)
			if t.Failed() {
				paths, _ := filepath.Glob(filepath.Join(root, candidate.cell+"-*", "result.json"))
				for _, path := range paths {
					body, _ := os.ReadFile(path)
					var result map[string]any
					if json.Unmarshal(body, &result) != nil {
						continue
					}
					flags := make(map[string]bool)
					evidence, _ := result["evidence"].(map[string]any)
					for key, value := range evidence {
						if flag, ok := value.(bool); ok {
							flags[key] = flag
						}
					}
					t.Logf("owned replay acceptance flags: exit=%v error=%v flags=%v", result["exit_code"], result["error_class"], flags)
				}
				paths, _ = filepath.Glob(filepath.Join(gate.directory, "gate-*.json"))
				known := make(map[string]string)
				for _, path := range paths {
					body, _ := os.ReadFile(path)
					var capture liveServerToolCapture
					if json.Unmarshal(body, &capture) != nil || capture.Category != "inference" {
						continue
					}
					request, valid := liveAttachmentChatRequest([]byte(capture.PublicRequest.Body), candidate)
					for _, result := range request.ToolResults {
						t.Logf("owned Chat result: request_valid=%t known_name=%q has_image=%t text_bytes=%d failed=%t", valid, known[result.ID], result.HasImage, len(result.Content), result.Failed)
					}
					if response, valid := liveChatCompletionStream([]byte(capture.Response.Body), candidate.model); valid {
						for _, call := range response.ToolCalls {
							known[call.ID] = call.Name
						}
					}
				}
			}
			if dispatched.Load() < 2 || dispatched.Load() > 3 || liveAttachmentReadinessError(gate) != nil {
				t.Fatal("installed-client replay did not retain bounded multi-turn readiness")
			}
		})
	}
}

func TestAttachmentRetainedSignatureOnlyTransactionAtIngress(t *testing.T) {
	path := os.Getenv("CPA_ATTACHMENT_RETAINED_MESSAGES_CAPTURE")
	if path == "" {
		t.Skip("set CPA_ATTACHMENT_RETAINED_MESSAGES_CAPTURE for read-only failed-transaction ingress replay")
	}
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("retained failed transaction is unavailable")
	}
	var retained liveAttachmentIngressCapture
	if json.Unmarshal(source, &retained) != nil || !retained.Dispatched || !retained.BodyComplete || !retained.ResponseBodyComplete || retained.Response.Status != http.StatusOK || retained.StreamOutcome != "unconfirmed_stream_failure" {
		t.Fatal("retained transaction is not the complete failed ingress response")
	}
	var original map[string]any
	if json.Unmarshal([]byte(retained.Request.Body), &original) != nil || original["model"] != "gemini-3.8-flash" {
		t.Fatal("retained transaction has a different original model or protocol")
	}
	peer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil || request.URL.Path != "/v1/messages" || string(body) != retained.Request.Body {
			http.Error(writer, "retained original request context changed", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, retained.Response.Body)
	}))
	defer peer.Close()
	gate := newLiveServerToolGate(t, t.TempDir())
	ingress := newLiveAttachmentIngress(t, gate, peer.URL, t.TempDir(), "claude")
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	response, err := client.Post(ingress.URL()+"/v1/messages", "application/json", strings.NewReader(retained.Request.Body))
	if err != nil {
		t.Fatal("retained transaction could not reach the owned ingress")
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK || string(body) != retained.Response.Body {
		t.Fatal("retained exact transaction was not forwarded intact")
	}
	var captures []liveAttachmentIngressCapture
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		captures, err = ingress.captures()
		if err == nil && len(captures) == 1 && captures[0].ResponseBodyComplete {
			break
		}
		time.Sleep(time.Millisecond)
	}
	denials, failure := ingress.state()
	if err != nil || len(captures) != 1 || !captures[0].StreamTerminalComplete || !captures[0].StreamTerminalFunction || !captures[0].StreamTerminalForwarded || denials != 0 || failure != nil || liveAttachmentReadinessError(gate) != nil {
		t.Fatal("actual retained signature-only tool terminal still loses ingress readiness")
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(source, unchanged) {
		t.Fatal("historical failed transaction was changed during read-only replay")
	}
}

func writeAttachmentOwnedFixtureStream(t *testing.T, writer http.ResponseWriter, candidate liveAttachmentCase, turn int) {
	t.Helper()
	name, id, toolTurns := "Read", fmt.Sprintf("owned-tool-%d", turn), 1
	arguments := map[string]any{"file_path": candidate.fixture}
	if candidate.media == "pdf" {
		arguments["pages"] = "1"
	}
	if candidate.client == "codex" {
		name = "exec_command"
		arguments = map[string]any{"cmd": "printf CPA_CONTINUATION_OK"}
		if candidate.media == "pdf" {
			toolTurns = 2
			arguments = map[string]any{"cmd": "pdftoppm -png " + candidate.fixture + " page && ls", "workdir": filepath.Dir(candidate.fixture)}
			if turn == 2 {
				name = "view_image"
				arguments = map[string]any{"path": filepath.Join(filepath.Dir(candidate.fixture), "page-1.png")}
			}
		}
	}
	final := turn > toolTurns
	text := "The top is red and the bottom is blue. CPA_CONTINUATION_OK"
	if candidate.media == "pdf" {
		text = "The printed code is Q7B9 and the element is blue."
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	encodedArguments, _ := json.Marshal(arguments)
	write := func(event map[string]any) {
		_, _ = fmt.Fprintf(writer, "event: %s\n", event["type"])
		writeEvent(writer, event)
	}
	switch candidate.modelPath {
	case "/chat/completions":
		finish := "tool_calls"
		delta := map[string]any{"role": "assistant", "reasoning_opaque": "synthetic-owned-chat-" + id, "tool_calls": []any{map[string]any{"index": 0, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(encodedArguments)}}}}
		if final {
			delta = map[string]any{"role": "assistant", "content": text}
			finish = "stop"
		}
		frame := map[string]any{"id": "chat-" + id, "object": "chat.completion.chunk", "model": candidate.model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}}
		_, _ = io.WriteString(writer, liveChatTestFrame(t, frame))
		frame["choices"] = []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}
		frame["usage"] = map[string]any{"prompt_tokens": 12, "completion_tokens": 5, "total_tokens": 17}
		_, _ = io.WriteString(writer, liveChatTestFrame(t, frame)+"data: [DONE]\n\n")
	case "/responses":
		output := []any{map[string]any{"type": "reasoning", "id": "rs_" + id, "summary": []any{}, "encrypted_content": "synthetic-native-responses-" + id}, map[string]any{"type": "function_call", "id": "fc_" + id, "call_id": id, "name": name, "arguments": string(encodedArguments), "status": "completed"}}
		if final {
			output = []any{map[string]any{"type": "message", "id": "msg_" + id, "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": text}}}}
		}
		response := map[string]any{"id": "resp_" + id, "object": "response", "status": "completed", "model": candidate.model, "output": output, "usage": map[string]any{"input_tokens": 12, "output_tokens": 5, "total_tokens": 17}}
		write(map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_" + id, "object": "response", "model": candidate.model, "status": "in_progress", "output": []any{}}})
		for index, item := range output {
			write(map[string]any{"type": "response.output_item.added", "output_index": index, "item": item})
			write(map[string]any{"type": "response.output_item.done", "output_index": index, "item": item})
		}
		write(map[string]any{"type": "response.completed", "response": response})
	case "/v1/messages":
		write(map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_" + id, "type": "message", "role": "assistant", "model": candidate.model, "content": []any{}, "stop_reason": nil, "usage": map[string]any{"input_tokens": 12, "output_tokens": 0}}})
		if !final {
			write(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "thinking", "thinking": ""}})
			write(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "signature_delta", "signature": "synthetic-native-messages-" + id}})
			write(map[string]any{"type": "content_block_stop", "index": 0})
			write(map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{}}})
			write(map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(encodedArguments)}})
			write(map[string]any{"type": "content_block_stop", "index": 1})
		} else {
			write(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
			write(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": text}})
			write(map[string]any{"type": "content_block_stop", "index": 0})
		}
		stop := "tool_use"
		if final {
			stop = "end_turn"
		}
		write(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop}, "usage": map[string]any{"output_tokens": 5}})
		write(map[string]any{"type": "message_stop"})
	}
}
