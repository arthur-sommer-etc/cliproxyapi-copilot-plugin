//go:build !windows

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/sse"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
)

func TestLiveHostedToolStreamContinuation(t *testing.T) {
	if os.Getenv("CPA_LIVE_HOSTED_TOOL_CONTINUATION") != "1" {
		t.Skip("set CPA_LIVE_HOSTED_TOOL_CONTINUATION=1 for native hosted-tool streaming and history replay")
	}
	streamPhase, continuationPhase := "S", "C"
	switch os.Getenv("CPA_LIVE_HOSTED_TOOL_ATTEMPT") {
	case "":
	case "native-identity":
		streamPhase, continuationPhase = "S-native-identity", "C-native-identity"
	case "captured-history":
		streamPhase, continuationPhase = "S-captured-identity", "C-captured-identity"
	default:
		t.Fatal("unknown hosted-tool changed-evidence attempt")
	}
	retainedPacket := os.Getenv("CPA_LIVE_HOSTED_STREAM_PACKET")
	if retainedPacket != "" && streamPhase != "S-native-identity" && streamPhase != "S-captured-identity" {
		t.Fatal("retained stream replay requires the explicit changed-identity attempt")
	}
	if streamPhase == "S-captured-identity" && retainedPacket == "" {
		t.Fatal("captured history requires an immutable actual inference packet")
	}
	gate, base, stop := startLiveServerToolHarness(t)
	defer stop()
	gate.client.Timeout = 0
	for _, candidate := range liveServerToolCandidates() {
		if candidate.name != "gpt-responses-search" && candidate.name != "claude-code-execution" {
			continue
		}
		if streamPhase != "S" && candidate.name != "gpt-responses-search" {
			continue
		}
		t.Run(candidate.name, func(t *testing.T) {
			candidate.payload["stream"] = true
			var first []byte
			streamEvidence := "live_inference"
			if retainedPacket == "" {
				first = postLiveHostedToolStep(t, gate, base+candidate.path, streamPhase, candidate.name, candidate.payload)
			} else {
				first = readLiveHostedStreamPacket(t, gate, retainedPacket, candidate.payload, streamPhase == "S-captured-identity")
				streamEvidence = "retained_actual_inference_revalidated"
			}
			events, err := liveHostedToolEvents(first)
			if err != nil {
				t.Fatal(err)
			}
			response, err := liveHostedToolSnapshot(t, candidate.name, events)
			if err != nil {
				t.Fatal(err)
			}
			if err := liveHostedToolEnvelope(candidate.model, response); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			evidence := inspectLiveServerToolResponse(candidate.name, encoded)
			if !evidence.call || !evidence.result || !evidence.content || !evidence.terminal {
				t.Fatal("stream lacks a completed hosted call and paired meaningful result")
			}
			if candidate.requiresSource && (!evidence.resultContent || !evidence.actionSource || !evidence.pairedSourceResult || !evidence.pairedCitation) {
				t.Fatal("stream lacks a result/source/citation pair from the same hosted search")
			}
			if streamPhase == "S-captured-identity" {
				livePacketWriteJSON(t, gate.directory, "captured-terminal-eligibility.json", map[string]any{"terminal_eligible": true, "native_stream_accepted": false, "offline_identity_replay": true, "family": candidate.name, "response": response, "scope": "unchanged actual terminal history from a failed native stream"})
			} else {
				livePacketWriteJSON(t, gate.directory, streamPhase+"-"+candidate.name+"-verdict.json", map[string]any{"accepted": true, "family": candidate.name, "response": response, "scope": "native stream and paired hosted result", "evidence_kind": streamEvidence})
			}
			candidate.payload["stream"] = false
			if candidate.name == "claude-code-execution" {
				if !liveHostedToolHas385(liveHostedToolText(response)) {
					t.Fatal("streamed assistant did not report the observed execution result")
				}
				messages := liveAnySlice(candidate.payload["messages"])
				candidate.payload["messages"] = append(messages, map[string]any{"role": "assistant", "content": response["content"]}, map[string]any{"role": "user", "content": "Report the numeric result printed in the preceding tool result. Use that existing result without running any tools."})
			} else {
				input := []any{map[string]any{"role": "user", "content": candidate.payload["input"]}}
				input = append(input, liveAnySlice(response["output"])...)
				candidate.payload["input"] = append(input, map[string]any{"role": "user", "content": "Give the NASA source URL used in the preceding answer. Use only the preceding search results without searching again."})
				candidate.payload["tool_choice"] = "none"
			}
			followup := postLiveHostedToolStep(t, gate, base+candidate.path, continuationPhase, candidate.name, candidate.payload)
			var continued map[string]any
			if json.Unmarshal(followup, &continued) != nil || !liveMatrixResponseModelMatches(candidate.model, stringValue(continued["model"])) {
				t.Fatal("continuation has an invalid envelope or changed model")
			}
			if err := liveHostedToolEnvelope(candidate.model, continued); err != nil {
				t.Fatal(err)
			}
			if candidate.name == "claude-code-execution" {
				if !liveHostedToolHas385(liveHostedToolText(continued)) {
					t.Fatal("continuation did not complete using the prior execution result")
				}
				for _, block := range jsonObjects(continued["content"]) {
					if block["type"] != "text" && block["type"] != "thinking" && block["type"] != "redacted_thinking" {
						t.Fatal("continuation executed another tool")
					}
				}
			} else {
				if continued["object"] != "response" || continued["status"] != "completed" || continued["error"] != nil || continued["incomplete_details"] != nil {
					t.Fatal("search history continuation did not complete")
				}
				for _, item := range jsonObjects(continued["output"]) {
					if item["type"] != "message" && item["type"] != "reasoning" {
						t.Fatal("continuation executed another tool")
					}
				}
				matched := false
				for _, item := range jsonObjects(response["output"]) {
					if item["type"] == "web_search_call" {
						action, _ := item["action"].(map[string]any)
						for _, source := range jsonObjects(action["sources"]) {
							url := stringValue(source["url"])
							matched = matched || isNASAURL(url) && strings.Contains(liveHostedToolText(continued), url)
						}
					}
				}
				if !matched {
					t.Fatal("continuation did not preserve a NASA source from the actual first response")
				}
			}
			livePacketWriteJSON(t, gate.directory, continuationPhase+"-"+candidate.name+"-verdict.json", map[string]any{"accepted": true, "family": candidate.name, "response": continued, "scope": "native exact-history replay without another hosted call"})
			if streamPhase == "S-captured-identity" {
				t.Log("authentic captured-terminal continuation accepted; original native stream failure retained")
			} else {
				t.Log("native SSE and exact provider-owned history continuation accepted")
			}
		})
	}
}

func postLiveHostedToolStep(t *testing.T, gate *liveServerToolGate, endpoint, phase, family string, payload map[string]any) []byte {
	t.Helper()
	cell := phase + "/" + family
	if err := gate.setPhaseCell(phase, family); err != nil {
		t.Fatal(err)
	}
	defer gate.freezeCell(cell)
	before, _, _, _, captureErr := gate.countsFor(cell)
	if captureErr != nil {
		t.Fatal("capture unavailable before hosted-tool dispatch")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+liveCopilotClientKey)
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: liveOneShotTransport{}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	var received []byte
	status := 0
	if err == nil {
		status = response.StatusCode
		received, err = io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
		if closeErr := response.Body.Close(); err == nil {
			err = closeErr
		}
		if len(received) > 16<<20 {
			err = errors.New("hosted-tool response exceeded capture capacity")
		}
	}
	after, total, auth, catalog, captureErr := gate.countsFor(cell)
	packet := map[string]any{"cell": cell, "request_body": json.RawMessage(body), "response_body": string(received), "http_status": status, "clean_eof": err == nil, "physical_inference": after - before, "total_inference": total, "auth": auth, "catalog": catalog, "capture_available": captureErr == nil}
	livePacketWriteJSON(t, gate.directory, phase+"-"+family+"-client.json", packet)
	packetPath := filepath.Join(gate.directory, phase+"-"+family+"-client.json")
	retained, readErr := os.ReadFile(packetPath)
	copiedAuth, authErr := os.ReadFile(os.Getenv("CPA_LIVE_COPILOT_AUTH_FILE"))
	if readErr != nil || authErr != nil {
		t.Fatal("copied auth and retained packet could not be bound")
	}
	packetDigest, authDigest := sha256.Sum256(retained), sha256.Sum256(copiedAuth)
	livePacketWriteJSON(t, gate.directory, filepath.Base(packetPath)+".auth-binding.json", map[string]any{"packet_sha256": hex.EncodeToString(packetDigest[:]), "copied_auth_sha256": hex.EncodeToString(authDigest[:]), "basis": "copied auth file used by this isolated dispatch"})
	if err != nil || captureErr != nil || status != http.StatusOK || after <= before {
		t.Fatalf("hosted-tool request failed; retained private packet: phase=%s family=%s http=%d physical_inference=%d", phase, family, status, after-before)
	}
	verifyLiveHostedPublicHistory(t, gate, gate.directory, cell, body, family)
	return received
}

func verifyLiveHostedPublicHistory(t *testing.T, gate *liveServerToolGate, directory, cell string, body []byte, family string) []byte {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal("public capture inventory unavailable")
	}
	matched := false
	var publicBody []byte
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "gate-") {
			continue
		}
		captured, readErr := os.ReadFile(filepath.Join(directory, entry.Name()))
		var capture liveServerToolCapture
		if readErr != nil || json.Unmarshal(captured, &capture) != nil {
			t.Fatal("public capture unavailable")
		}
		if capture.PhaseCell != cell || capture.Category != "inference" || !capture.Dispatched {
			continue
		}
		var business map[string]any
		var requested map[string]any
		if json.Unmarshal([]byte(capture.PublicRequest.Body), &business) != nil || json.Unmarshal(body, &requested) != nil {
			t.Fatal("public request history could not be inspected")
		}
		path, history := "/responses", "input"
		if family == "claude-code-execution" {
			path, history = "/v1/messages", "messages"
		}
		gate.mu.Lock()
		origin := gate.publicAPI.Scheme + "://" + gate.publicAPI.Host
		gate.mu.Unlock()
		if matched || capture.OriginalPublicOrigin != origin || !strings.HasSuffix(capture.PublicRequest.URL, path) || business["model"] != requested["model"] || !reflect.DeepEqual(business[history], requested[history]) || capture.CaptureTruncated || capture.ErrorClass != "" || capture.Response.Status != http.StatusOK {
			t.Fatal("public inference lost its native endpoint, model, complete capture, or exact owned history")
		}
		matched = true
		publicBody = []byte(capture.Response.Body)
	}
	if !matched {
		t.Fatal("public request evidence was not retained for the claimed cell")
	}
	return publicBody
}

func readLiveHostedStreamPacket(t *testing.T, gate *liveServerToolGate, path string, payload map[string]any, terminalOnly bool) []byte {
	t.Helper()
	root := os.Getenv("CPA_LIVE_COPILOT_DEBUG_DIR")
	filename, cell := "S-native-identity-gpt-responses-search-client.json", "S-native-identity/gpt-responses-search"
	if terminalOnly {
		filename, cell = "S-gpt-responses-search-client.json", "S/gpt-responses-search"
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || !filepath.IsAbs(path) || strings.HasPrefix(relative, "..") || filepath.Base(path) != filename {
		t.Fatal("retained stream must be the task-owned exact changed-identity packet")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatal("retained stream packet is unavailable or not private")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("retained stream packet could not be read")
	}
	bindingPath := path + ".auth-binding.json"
	bindingInfo, err := os.Lstat(bindingPath)
	if err != nil || !bindingInfo.Mode().IsRegular() || bindingInfo.Mode().Perm() != 0600 {
		t.Fatal("retained stream has no private copied-auth binding")
	}
	bindingData, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal("retained stream auth binding unavailable")
	}
	var binding struct {
		PacketSHA256  string `json:"packet_sha256"`
		AuthSHA256    string `json:"copied_auth_sha256"`
		PublicCapture string `json:"public_capture_name"`
		PublicSHA256  string `json:"public_capture_sha256"`
	}
	auth, authErr := os.ReadFile(os.Getenv("CPA_LIVE_COPILOT_AUTH_FILE"))
	packetDigest, authDigest := sha256.Sum256(data), sha256.Sum256(auth)
	if authErr != nil || json.Unmarshal(bindingData, &binding) != nil || binding.PacketSHA256 != hex.EncodeToString(packetDigest[:]) || binding.AuthSHA256 != hex.EncodeToString(authDigest[:]) {
		t.Fatal("retained stream is not bound to the unchanged copied credential and packet")
	}
	var packet struct {
		Cell             string          `json:"cell"`
		Request          json.RawMessage `json:"request_body"`
		Response         string          `json:"response_body"`
		Status           int             `json:"http_status"`
		CleanEOF         bool            `json:"clean_eof"`
		Physical         int             `json:"physical_inference"`
		CaptureAvailable bool            `json:"capture_available"`
	}
	if json.Unmarshal(data, &packet) != nil || packet.Cell != cell || packet.Status != http.StatusOK || !packet.CleanEOF || !packet.CaptureAvailable || packet.Physical <= 0 {
		t.Fatal("retained stream lacks an actual completed captured dispatch")
	}
	var requested map[string]any
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]any
	if json.Unmarshal(packet.Request, &requested) != nil || json.Unmarshal(encoded, &expected) != nil || !reflect.DeepEqual(requested, expected) {
		t.Fatal("retained stream request differs from the exact native candidate")
	}
	count, _, _, _, captureErr := gate.countsFor(packet.Cell)
	if captureErr != nil || count < packet.Physical {
		t.Fatal("retained inference is not present in the physical ledger")
	}
	public := verifyLiveHostedPublicHistory(t, gate, filepath.Dir(path), packet.Cell, packet.Request, "gpt-responses-search")
	if terminalOnly {
		if binding.PublicCapture != filepath.Base(binding.PublicCapture) || !strings.HasPrefix(binding.PublicCapture, "gate-") {
			t.Fatal("captured history has no bound public inference")
		}
		publicPath := filepath.Join(filepath.Dir(path), binding.PublicCapture)
		publicInfo, infoErr := os.Lstat(publicPath)
		publicData, readErr := os.ReadFile(publicPath)
		publicDigest := sha256.Sum256(publicData)
		var capture liveServerToolCapture
		if infoErr != nil || !publicInfo.Mode().IsRegular() || publicInfo.Mode().Perm() != 0600 || readErr != nil || json.Unmarshal(publicData, &capture) != nil || binding.PublicSHA256 != hex.EncodeToString(publicDigest[:]) || capture.PhaseCell != cell || capture.Response.Body != string(public) {
			t.Fatal("public capture differs from its immutable history binding")
		}
	}

	var decoder sse.Decoder
	var state any
	var repaired bytes.Buffer
	for _, frame := range decoder.Feed(public) {
		frames, err := translate.StreamFromEndpoint(context.Background(), translate.EndpointResponses, "openai-response", "gpt-6-luna", packet.Request, packet.Request, frame, &state)
		if err != nil {
			t.Fatal("retained provider stream no longer matches the repaired native path")
		}
		for _, frame := range frames {
			repaired.Write(frame)
		}
	}
	if len(decoder.Flush()) != 0 {
		t.Fatal("retained public stream was truncated")
	}
	actualEvents, err := liveHostedToolEvents([]byte(packet.Response))
	if err != nil {
		t.Fatal(err)
	}
	replayedEvents, err := liveHostedToolEvents(repaired.Bytes())
	if terminalOnly {
		publicEvents, publicErr := liveHostedToolEvents(public)
		if publicErr != nil || err != nil || len(actualEvents) == 0 || len(publicEvents) == 0 || len(replayedEvents) == 0 {
			t.Fatal("captured history has no complete provider or client terminal")
		}
		actualTerminal, sourceTerminal, repairedTerminal := actualEvents[len(actualEvents)-1], publicEvents[len(publicEvents)-1], replayedEvents[len(replayedEvents)-1]
		if actualTerminal["type"] != "response.completed" || !reflect.DeepEqual(actualTerminal, sourceTerminal) || !reflect.DeepEqual(actualTerminal, repairedTerminal) {
			t.Fatal("captured actual terminal changed before history continuation")
		}
		return repaired.Bytes()
	}
	if err != nil || !reflect.DeepEqual(actualEvents, replayedEvents) {
		t.Fatal("retained client stream differs from its complete public inference")
	}
	return []byte(packet.Response)
}

func liveHostedToolEvents(body []byte) ([]map[string]any, error) {
	var decoder sse.Decoder
	frames := decoder.Feed(body)
	if len(decoder.Flush()) != 0 {
		return nil, errors.New("hosted-tool SSE ended with an incomplete frame")
	}
	var events []map[string]any
	for _, frame := range frames {
		var name string
		var data []string
		for _, line := range strings.Split(string(frame), "\n") {
			if strings.HasPrefix(line, "event:") {
				name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			}
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		if len(data) == 0 {
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(strings.Join(data, "\n")), &event) != nil || stringValue(event["type"]) == "" || name != "" && name != event["type"] {
			return nil, errors.New("hosted-tool SSE has an invalid event envelope")
		}
		events = append(events, event)
	}
	if len(events) == 0 {
		return nil, errors.New("hosted-tool SSE has no events")
	}
	return events, nil
}

func liveHostedToolSnapshot(t *testing.T, family string, events []map[string]any) (map[string]any, error) {
	if family == "gpt-responses-search" {
		if events[0]["type"] != "response.created" || events[len(events)-1]["type"] != "response.completed" {
			return nil, errors.New("Responses search stream lacks its ordered lifecycle")
		}
		response, _ := events[len(events)-1]["response"].(map[string]any)
		initial, _ := events[0]["response"].(map[string]any)
		if stringValue(response["id"]) == "" || initial["id"] != response["id"] {
			return nil, errors.New("Responses search lifecycle changed response identity")
		}
		items := jsonObjects(response["output"])
		added, done := make(map[int]string), make(map[int]string)
		texts := make(map[int]string)
		for position, event := range events {
			kind := stringValue(event["type"])
			if kind == "error" || kind == "response.failed" || kind == "response.incomplete" || kind == "response.created" && position != 0 || kind == "response.completed" && position != len(events)-1 {
				return nil, errors.New("Responses search stream has an error or duplicate lifecycle")
			}
			if object, ok := event["response"].(map[string]any); ok && object["id"] != response["id"] {
				return nil, errors.New("Responses event changed its response identity")
			}
			if kind != "response.output_item.added" && kind != "response.output_item.done" && !strings.HasPrefix(kind, "response.web_search_call.") {
				switch kind {
				case "response.created", "response.in_progress", "response.completed", "response.content_part.added", "response.content_part.done", "response.output_text.annotation.added", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done", "response.reasoning_summary_text.done":
				case "response.output_text.delta", "response.reasoning_summary_text.delta":
					delta, valid := event["delta"].(string)
					index, indexed := event["output_index"].(float64)
					if !valid || !indexed || index < 0 || int(index) >= len(items) || added[int(index)] == "" || done[int(index)] != "" {
						return nil, errors.New("Responses text delta has no active output item")
					}
					if kind == "response.output_text.delta" {
						texts[int(index)] += delta
					}
				case "response.output_text.done":
					index, valid := event["output_index"].(float64)
					if !valid || texts[int(index)] != event["text"] {
						return nil, errors.New("Responses text completion contradicts streamed text")
					}
				default:
					return nil, errors.New("Responses hosted-tool stream has an unsupported event")
				}
				continue
			}
			index, ok := event["output_index"].(float64)
			if !ok || index < 0 || int(index) >= len(items) || float64(int(index)) != index {
				return nil, errors.New("Responses hosted-tool event has an invalid output index")
			}
			i := int(index)
			id := stringValue(items[i]["id"])
			if strings.HasPrefix(kind, "response.web_search_call.") {
				if id == "" || event["item_id"] != id || added[i] != id || done[i] != "" || kind == "response.web_search_call.failed" {
					return nil, errors.New("Responses search event lost its active terminal identity")
				}
				continue
			}
			item, _ := event["item"].(map[string]any)
			if id == "" || item["id"] != id || item["type"] != items[i]["type"] {
				return nil, errors.New("Responses output event differs from its terminal item")
			}
			if kind == "response.output_item.added" {
				if added[i] != "" {
					return nil, errors.New("Responses output item started twice")
				}
				added[i] = id
			} else {
				if added[i] != id || done[i] != "" || item["status"] != nil && item["status"] != "completed" || item["type"] == "web_search_call" && item["status"] != "completed" {
					return nil, errors.New("Responses output item did not complete exactly once")
				}
				if !liveHostedToolFieldsMatch(item, items[i]) {
					return nil, errors.New("Responses completed item contradicts its terminal fields")
				}
				done[i] = id
			}
		}
		if len(done) != len(items) || len(items) == 0 {
			return nil, errors.New("Responses terminal contains unfinished output items")
		}
		for index, text := range texts {
			if text != liveHostedToolText(map[string]any{"output": []any{items[index]}}) {
				return nil, errors.New("Responses terminal text contradicts streamed content")
			}
		}
		return response, nil
	}
	if family != "claude-code-execution" || events[0]["type"] != "message_start" || events[len(events)-1]["type"] != "message_stop" {
		return nil, errors.New("Messages server-tool stream lacks its ordered lifecycle")
	}
	message, _ := events[0]["message"].(map[string]any)
	response := cloneJSONMap(message)
	active, blocks, terminal := -1, 0, false
	for position, event := range events {
		kind := stringValue(event["type"])
		if kind == "message_start" && position == 0 || kind == "ping" && !terminal {
			continue
		}
		switch kind {
		case "content_block_start", "content_block_delta", "content_block_stop":
			index, ok := event["index"].(float64)
			if terminal || !ok || float64(int(index)) != index || index < 0 {
				return nil, errors.New("Messages server-tool block has an invalid lifecycle index")
			}
			if kind == "content_block_start" {
				block, _ := event["content_block"].(map[string]any)
				if active != -1 || int(index) != blocks || stringValue(block["type"]) == "" {
					return nil, errors.New("Messages server-tool block started out of order")
				}
				active, blocks = int(index), blocks+1
			} else if active != int(index) {
				return nil, errors.New("Messages server-tool block changed after stop or before start")
			} else if kind == "content_block_stop" {
				active = -1
			} else {
				delta, _ := event["delta"].(map[string]any)
				field := map[string]string{"text_delta": "text", "input_json_delta": "partial_json", "thinking_delta": "thinking", "signature_delta": "signature"}[stringValue(delta["type"])]
				if _, valid := delta[field].(string); field == "" || !valid {
					return nil, errors.New("Messages hosted-tool block has an unsupported delta")
				}
			}
		case "message_delta":
			if terminal || active != -1 || blocks == 0 {
				return nil, errors.New("Messages server-tool terminal preceded completed blocks")
			}
			delta, _ := event["delta"].(map[string]any)
			if delta["stop_reason"] != "end_turn" {
				return nil, errors.New("Messages hosted-tool response did not finish its turn")
			}
			for key, value := range delta {
				response[key] = value
			}
			if usage, ok := event["usage"].(map[string]any); ok {
				response["usage"] = usage
			}
			if container, ok := event["container"].(map[string]any); ok {
				response["container"] = container
			}
			terminal = true
		case "message_stop":
			if !terminal || position != len(events)-1 {
				return nil, errors.New("Messages server-tool stream stopped before its terminal")
			}
		default:
			return nil, errors.New("Messages hosted-tool stream has an error or unexpected event")
		}
	}
	blocksFromEvents := matrixClaudeBlocksFromEvents(t, events)
	content := make([]any, len(blocksFromEvents))
	for index, block := range blocksFromEvents {
		content[index] = block
	}
	response["content"] = content
	return response, nil
}

func liveHostedToolEnvelope(model string, response map[string]any) error {
	if !liveMatrixResponseModelMatches(model, stringValue(response["model"])) || stringValue(response["id"]) == "" || response["error"] != nil {
		return errors.New("hosted-tool response changed identity or contains an error")
	}
	field := "output"
	if model == "claude-haiku-5.5" {
		field = "content"
		if response["type"] != "message" || response["role"] != "assistant" || response["stop_reason"] != "end_turn" {
			return errors.New("Messages hosted-tool response did not complete")
		}
	} else if response["object"] != "response" || response["status"] != "completed" || response["incomplete_details"] != nil {
		return errors.New("Responses hosted-tool response did not complete")
	}
	items, valid := response[field].([]any)
	if !valid || len(items) == 0 {
		return errors.New("hosted-tool response has no typed output array")
	}
	for _, raw := range items {
		item, valid := raw.(map[string]any)
		if !valid || stringValue(item["type"]) == "" {
			return errors.New("hosted-tool response has a malformed output member")
		}
		if item["type"] == "message" {
			content, valid := item["content"].([]any)
			if !valid || item["role"] != "assistant" || item["status"] != "completed" || len(content) == 0 {
				return errors.New("hosted-tool assistant message is incomplete")
			}
			for _, rawPart := range content {
				part, valid := rawPart.(map[string]any)
				if !valid || part["type"] != "output_text" || stringValue(part["text"]) == "" {
					return errors.New("hosted-tool assistant message has malformed content")
				}
			}
		}
	}
	usage, _ := response["usage"].(map[string]any)
	output, valid := usage["output_tokens"].(float64)
	if !valid || output <= 0 {
		return errors.New("hosted-tool response has no positive output usage")
	}
	return nil
}

func liveHostedToolHas385(text string) bool {
	return regexp.MustCompile(`(^|[^0-9-])385([^0-9]|$)`).MatchString(text)
}

func liveHostedToolFieldsMatch(observed, terminal map[string]any) bool {
	for key, value := range observed {
		if key == "encrypted_content" && observed["type"] == "reasoning" {
			current, currentValid := value.(string)
			final, finalValid := terminal[key].(string)
			if !currentValid || !finalValid || current == "" || final == "" {
				return false
			}
			continue
		}
		if key == "action" && observed["type"] == "web_search_call" {
			current, currentValid := value.(map[string]any)
			final, finalValid := terminal[key].(map[string]any)
			if !currentValid || !finalValid {
				return false
			}
			current = cloneJSONMap(current)
			if rawSources, present := current["sources"]; present {
				sources, sourcesValid := rawSources.([]any)
				finalSources, finalSourcesValid := final["sources"].([]any)
				if !sourcesValid || !finalSourcesValid || len(sources) > len(finalSources) || !reflect.DeepEqual(sources, finalSources[:len(sources)]) {
					return false
				}
				current["sources"] = finalSources
			}
			if !liveHostedToolFieldsMatch(current, final) {
				return false
			}
			continue
		}
		if object, ok := value.(map[string]any); ok {
			final, valid := terminal[key].(map[string]any)
			if !valid || !liveHostedToolFieldsMatch(object, final) {
				return false
			}
		} else if !reflect.DeepEqual(value, terminal[key]) {
			return false
		}
	}
	return true
}

func liveHostedToolText(response map[string]any) string {
	var text strings.Builder
	for _, block := range jsonObjects(response["content"]) {
		if block["type"] == "text" {
			text.WriteString(stringValue(block["text"]))
		}
	}
	for _, item := range jsonObjects(response["output"]) {
		if item["type"] == "message" {
			for _, part := range jsonObjects(item["content"]) {
				if part["type"] == "output_text" {
					text.WriteString(stringValue(part["text"]))
				}
			}
		}
	}
	return text.String()
}
