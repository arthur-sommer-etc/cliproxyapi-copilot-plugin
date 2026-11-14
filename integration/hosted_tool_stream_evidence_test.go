//go:build !windows

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/sse"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
)

func TestCapturedHostedSearchStreamRepairPassesAcceptance(t *testing.T) {
	directory := filepath.Join("..", "internal", "translate", "testdata", "native-hosted-tool-stream-captures", "gpt-search-stream-before-repair")
	request, err := os.ReadFile(filepath.Join(directory, "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := readHostedStreamJSON(directory)
	if err != nil {
		t.Fatal(err)
	}
	var decoder sse.Decoder
	var state any
	var output bytes.Buffer
	for _, frame := range decoder.Feed(body) {
		frames, err := translate.StreamFromEndpoint(context.Background(), translate.EndpointResponses, "openai-response", "gpt-6-luna", request, request, frame, &state)
		if err != nil {
			t.Fatal(err)
		}
		for _, translated := range frames {
			output.Write(translated)
		}
	}
	events, err := liveHostedToolEvents(output.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	response, err := liveHostedToolSnapshot(t, "gpt-responses-search", events)
	if err != nil {
		t.Fatal(err)
	}
	if err := liveHostedToolEnvelope("gpt-6-luna", response); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	evidence := inspectLiveServerToolResponse("gpt-responses-search", encoded)
	if !evidence.call || !evidence.result || !evidence.content || !evidence.terminal || !evidence.pairedSourceResult || !evidence.pairedCitation {
		t.Fatal("repaired original provider stream lacks real paired search evidence")
	}
}

func TestHostedToolStreamEvidenceRejectsInvalidLifecycle(t *testing.T) {
	for _, capture := range []struct{ family, directory string }{
		{"gpt-responses-search", "gpt-responses-search"},
		{"claude-code-execution", "claude-messages-code-execution"},
	} {
		t.Run(capture.family, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("..", "internal", "translate", "testdata", "native-server-tool-captures", capture.directory, "response.json"))
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]any
			if err := json.Unmarshal(body, &document); err != nil {
				t.Fatal(err)
			}
			events := offlineHostedToolEvents(document, capture.family)
			assembled, err := liveHostedToolSnapshot(t, capture.family, events)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(assembled)
			if err != nil {
				t.Fatal(err)
			}
			evidence := inspectLiveServerToolResponse(capture.family, encoded)
			if !evidence.call || !evidence.result || !evidence.content || !evidence.terminal {
				t.Fatal("captured native body lost its paired server-tool evidence during offline framing")
			}
			for _, mutation := range []struct {
				name   string
				change func([]map[string]any) []map[string]any
			}{
				{"missing terminal", func(events []map[string]any) []map[string]any { return events[:len(events)-1] }},
				{"duplicate start", func(events []map[string]any) []map[string]any {
					return append(events[:1], append([]map[string]any{events[0]}, events[1:]...)...)
				}},
				{"terminal before body", func(events []map[string]any) []map[string]any {
					return append(events[:1], append([]map[string]any{events[len(events)-1]}, events[1:]...)...)
				}},
				{"unknown event", func(events []map[string]any) []map[string]any {
					return append(events[:1], append([]map[string]any{{"type": "unrecognized_provider_event"}}, events[1:]...)...)
				}},
				{"contradictory done item or unknown delta", func(events []map[string]any) []map[string]any {
					for index, event := range events {
						if event["type"] == "response.output_item.done" {
							item, _ := event["item"].(map[string]any)
							copy := cloneJSONMap(item)
							copy["unsupported_terminal_field"] = true
							event["item"] = copy
							return events
						}
						if event["type"] == "content_block_stop" {
							return append(events[:index], append([]map[string]any{{"type": "content_block_delta", "index": event["index"], "delta": map[string]any{"type": "unknown_delta", "text": "ignored"}}}, events[index:]...)...)
						}
					}
					return events
				}},
				{"changed block or item identity", func(events []map[string]any) []map[string]any {
					for _, event := range events {
						if event["type"] == "content_block_stop" {
							event["index"] = float64(999)
							break
						}
						if event["type"] == "response.output_item.done" {
							item, _ := event["item"].(map[string]any)
							copy := cloneJSONMap(item)
							copy["id"] = "offline_mismatched_identity"
							event["item"] = copy
							break
						}
					}
					return events
				}},
			} {
				t.Run(mutation.name, func(t *testing.T) {
					changed := mutation.change(offlineHostedToolEvents(document, capture.family))
					if _, err := liveHostedToolSnapshot(t, capture.family, changed); err == nil {
						t.Fatal("invalid stream lifecycle accepted")
					}
				})
			}
		})
	}
}

func TestHostedToolContinuationRejectsMalformedEnvelopeAndWrongNumber(t *testing.T) {
	for _, capture := range []struct{ model, directory, field string }{
		{"gpt-6-luna", "gpt-responses-search", "output"},
		{"claude-haiku-5.5", "claude-messages-code-execution", "content"},
	} {
		body, err := os.ReadFile(filepath.Join("..", "internal", "translate", "testdata", "native-server-tool-captures", capture.directory, "response.json"))
		if err != nil {
			t.Fatal(err)
		}
		for _, mutation := range []string{"none", "malformed array member", "error", "usage"} {
			var response map[string]any
			if err := json.Unmarshal(body, &response); err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "malformed array member":
				response[capture.field] = append(liveAnySlice(response[capture.field]), "invalid")
			case "error":
				response["error"] = map[string]any{"message": "offline failure"}
			case "usage":
				delete(response, "usage")
			}
			if err := liveHostedToolEnvelope(capture.model, response); (err == nil) != (mutation == "none") {
				t.Fatalf("%s envelope mutation %s: %v", capture.model, mutation, err)
			}
		}
	}
	for _, text := range []string{"1385", "3850", "-385"} {
		if liveHostedToolHas385(text) {
			t.Fatalf("wrong numeric result accepted: %s", text)
		}
	}
	if !liveHostedToolHas385("The prior result was **385**.") {
		t.Fatal("exact prior result rejected")
	}
}

func TestHostedToolPhaseCiphertextRequiresNativeReasoningStrings(t *testing.T) {
	for _, test := range []struct {
		name               string
		kind               string
		observed, terminal any
		accepted           bool
	}{
		{"actual phase-specific opaque strings", "reasoning", "phase_a", "phase_b", true},
		{"empty observed token", "reasoning", "", "phase_b", false},
		{"empty terminal token", "reasoning", "phase_a", "", false},
		{"numeric observed token", "reasoning", 123, "phase_b", false},
		{"numeric terminal token", "reasoning", "phase_a", 123, false},
		{"non-reasoning mismatch", "message", "phase_a", "phase_b", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			observed := map[string]any{"type": test.kind, "encrypted_content": test.observed}
			terminal := map[string]any{"type": test.kind, "encrypted_content": test.terminal}
			if liveHostedToolFieldsMatch(observed, terminal) != test.accepted {
				t.Fatal("opaque-phase evidence classification differs")
			}
		})
	}
}

func TestHostedSearchTerminalSourcesMayOnlyAppend(t *testing.T) {
	first := map[string]any{"type": "url", "url": "https://science.nasa.gov/moon/facts/"}
	second := map[string]any{"type": "url", "url": "https://science.nasa.gov/moon/"}
	observed := map[string]any{"type": "web_search_call", "action": map[string]any{"type": "search", "sources": []any{first}}}
	for _, test := range []struct {
		name     string
		sources  []any
		accepted bool
	}{
		{"same sources", []any{first}, true},
		{"append terminal source", []any{first, second}, true},
		{"replace source", []any{second}, false},
		{"reorder source", []any{second, first}, false},
		{"remove source", []any{}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			terminal := map[string]any{"type": "web_search_call", "action": map[string]any{"type": "search", "sources": test.sources}}
			if liveHostedToolFieldsMatch(observed, terminal) != test.accepted {
				t.Fatal("terminal source reconciliation changed an observed source")
			}
		})
	}
}

func offlineHostedToolEvents(document map[string]any, family string) []map[string]any {
	if family == "gpt-responses-search" {
		events := []map[string]any{{"type": "response.created", "response": map[string]any{"id": document["id"]}}}
		for index, item := range jsonObjects(document["output"]) {
			events = append(events, map[string]any{"type": "response.output_item.added", "output_index": float64(index), "item": item}, map[string]any{"type": "response.output_item.done", "output_index": float64(index), "item": item})
		}
		return append(events, map[string]any{"type": "response.completed", "response": document})
	}
	message := cloneJSONMap(document)
	delete(message, "content")
	message["stop_reason"] = nil
	events := []map[string]any{{"type": "message_start", "message": message}}
	for index, block := range jsonObjects(document["content"]) {
		events = append(events, map[string]any{"type": "content_block_start", "index": float64(index), "content_block": block}, map[string]any{"type": "content_block_stop", "index": float64(index)})
	}
	return append(events, map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": document["stop_reason"]}, "usage": document["usage"]}, map[string]any{"type": "message_stop"})
}

func TestHostedToolSSEFramingRejectsTruncationAndEnvelopeMismatch(t *testing.T) {
	for _, body := range []string{
		"data: {\"type\":\"message_stop\"}",
		"event: message_start\ndata: {\"type\":\"message_stop\"}\n\n",
		"data: [DONE]\n\n",
		"data: {broken}\n\n",
	} {
		if _, err := liveHostedToolEvents([]byte(body)); err == nil {
			t.Fatal("invalid native SSE accepted")
		}
	}
	events, err := liveHostedToolEvents([]byte("event: message_stop\r\ndata: {\"type\":\"message_stop\"}\r\n\r\n"))
	if err != nil || len(events) != 1 || events[0]["type"] != "message_stop" {
		t.Fatal("complete CRLF frame rejected")
	}
}

// This complete actual stream has stable repaired identities, but its citation
// adds a tracking query absent from every result URL. Keep that proof failure.
func TestCapturedHostedSearchKeepsExactCitationPairingFailure(t *testing.T) {
	directory := filepath.Join("..", "internal", "translate", "testdata", "native-hosted-tool-stream-captures", "gpt-search-stream-after-identity-repair")
	request, err := os.ReadFile(filepath.Join(directory, "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := readHostedStreamJSON(directory)
	if err != nil {
		t.Fatal(err)
	}
	var decoder sse.Decoder
	var state any
	var output bytes.Buffer
	for _, frame := range decoder.Feed(body) {
		frames, err := translate.StreamFromEndpoint(context.Background(), translate.EndpointResponses, "openai-response", "gpt-6-luna", request, request, frame, &state)
		if err != nil {
			t.Fatal(err)
		}
		for _, translated := range frames {
			output.Write(translated)
		}
	}
	events, err := liveHostedToolEvents(output.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	response, err := liveHostedToolSnapshot(t, "gpt-responses-search", events)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	evidence := inspectLiveServerToolResponse("gpt-responses-search", encoded)
	if !evidence.call || !evidence.resultContent || !evidence.pairedSourceResult || !evidence.citation || evidence.pairedCitation {
		t.Fatal("actual tracked citation must retain its exact result-pairing failure")
	}
}

func readHostedStreamJSON(directory string) ([]byte, error) {
	stored, err := os.ReadFile(filepath.Join(directory, "response.json"))
	if err != nil {
		return nil, err
	}
	var body string
	if err := json.Unmarshal(stored, &body); err != nil {
		return nil, err
	}
	return []byte(body), nil
}
