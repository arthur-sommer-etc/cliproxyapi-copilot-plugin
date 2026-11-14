package provider

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
)

const incompleteResponseJSON = `{"id":"resp_partial","object":"response","model":"gpt-6-luna","status":"incomplete","error":null,"incomplete_details":{"reason":"max_output_tokens"},"output":[{"id":"rs_partial","type":"reasoning","encrypted_content":"synthetic-partial-reasoning","summary":[]},{"id":"msg_partial","type":"message","role":"assistant","status":"incomplete","content":[{"type":"output_text","text":"Partial answer","annotations":[]}]}],"usage":{"input_tokens":12,"output_tokens":5,"total_tokens":17,"output_tokens_details":{"reasoning_tokens":2}}}`

func TestStreamTerminalAcceptsValidIncompleteResponse(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{"max_output_tokens", "content_filter"} {
		t.Run(reason, func(t *testing.T) {
			response := strings.ReplaceAll(incompleteResponseJSON, "max_output_tokens", reason)
			frame := []byte("event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"response\":" + response + "}\n\n")
			var terminal streamTerminal
			done, err := terminal.observe(translate.EndpointResponses, frame, "", "")
			if err != nil || !done {
				t.Fatalf("valid incomplete terminal: done=%v error=%v", done, err)
			}
			if terminal.completed {
				t.Fatal("incomplete response was marked successfully completed")
			}
		})
	}
}

func TestPumpStreamPreservesIncompleteNativeResponse(t *testing.T) {
	t.Parallel()
	for _, trailing := range []bool{false, true} {
		t.Run(map[bool]string{false: "framed terminal", true: "EOF terminal"}[trailing], func(t *testing.T) {
			terminal := "event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"response\":" + incompleteResponseJSON + "}"
			payload := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Partial answer\"}\n\n" + terminal
			if !trailing {
				payload += "\n\n"
			}
			host := &sseLineEndingHost{chunk: transport.StreamChunk{Payload: []byte(payload), Done: trailing}}
			service := New(host)
			_, generation := service.configSnapshot()
			scope := protocolScopeKey("synthetic-auth", continuityTestStorage("synthetic-credential"), "gpt-6-luna", "https://api.example", translate.EndpointResponses, "synthetic-session", "main", generation)
			service.pumpStream(context.Background(), "output", translate.EndpointResponses, "openai-response", "gpt-6-luna", nil, nil, transport.Stream{ID: "upstream"}, scope, reasoningCarrierScope{}, "", "")
			if len(host.emitted) != 2 {
				t.Fatalf("emitted %d frames, want text delta and incomplete terminal; close error=%q", len(host.emitted), host.closedOutputMessage)
			}
			_, actual := parseSSEFrame(host.emitted[1])
			want := `{"type":"response.incomplete","response":` + incompleteResponseJSON + `}`
			if actual != want {
				t.Fatalf("incomplete terminal changed: %s", actual)
			}
			if host.closedOutputID != "output" || host.closedOutputMessage != "" || len(host.closedStreamIDs) != 1 || host.closedStreamIDs[0] != "upstream" {
				t.Fatalf("output/upstream closure = %q/%q/%v", host.closedOutputID, host.closedOutputMessage, host.closedStreamIDs)
			}
			if entries := service.reasoningReplayForScope(scope); len(entries) != 0 {
				t.Fatalf("partial reasoning cached as successful replay: %+v", entries)
			}
			service.recordReasoningReplay(scope, bytes.ReplaceAll([]byte(incompleteResponseJSON), []byte(`"incomplete"`), []byte(`"completed"`)))
			if len(service.reasoningReplayForScope(scope)) != 1 {
				t.Fatal("replay assertion did not use a cacheable scope and output")
			}
		})
	}
}

func TestStreamTerminalRejectsMalformedIncompleteResponse(t *testing.T) {
	t.Parallel()
	for _, response := range []string{
		`null`, `[]`, `{}`, `{"status":"incomplete"}`,
		`{"status":"completed","output":[],"incomplete_details":{"reason":"max_output_tokens"}}`,
		`{"status":"incomplete","output":null,"incomplete_details":{"reason":"max_output_tokens"}}`,
		`{"status":"incomplete","output":{},"incomplete_details":{"reason":"max_output_tokens"}}`,
		`{"status":"incomplete","output":[],"incomplete_details":null}`,
		`{"status":"incomplete","output":[],"incomplete_details":{"reason":""}}`,
		`{"status":"incomplete","output":[],"incomplete_details":{"reason":"max_output_tokens"},"error":{"message":"synthetic-error"}}`,
	} {
		t.Run(response, func(t *testing.T) {
			var terminal streamTerminal
			done, err := terminal.observe(translate.EndpointResponses, []byte("event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"response\":"+response+"}\n\n"), "", "")
			if err == nil || done || terminal.completed {
				t.Fatalf("malformed incomplete terminal accepted: done=%v completed=%v error=%v", done, terminal.completed, err)
			}
		})
	}
}

func TestIncompleteResponseRemainsInvalidCompaction(t *testing.T) {
	t.Parallel()
	if frames, err := buildResponsesCompactionFrames([]byte(incompleteResponseJSON)); err == nil || len(frames) != 0 {
		t.Fatalf("incomplete compaction accepted: frames=%d error=%v", len(frames), err)
	}
}

func TestStreamTerminalRejectsIncompleteEnvelopeErrors(t *testing.T) {
	t.Parallel()
	for _, fields := range []string{
		`"type":"response.incomplete","error":{"message":"synthetic-error"}`, `"type":"response.failed"`,
	} {
		t.Run(fields, func(t *testing.T) {
			var terminal streamTerminal
			frame := []byte("event: response.incomplete\ndata: {" + fields + `,"response":` + incompleteResponseJSON + "}\n\n")
			if done, err := terminal.observe(translate.EndpointResponses, frame, "", ""); err == nil || done {
				t.Fatalf("invalid incomplete envelope accepted: done=%v error=%v", done, err)
			}
		})
	}
}

func TestPumpStreamPreservesTranslatedIncompleteResponse(t *testing.T) {
	t.Parallel()
	for _, destination := range []string{"claude", "openai"} {
		t.Run(destination, func(t *testing.T) {
			frame := []byte("event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"response\":" + incompleteResponseJSON + "}\n\n")
			host := &sseLineEndingHost{chunk: transport.StreamChunk{Payload: frame, Done: true}}
			service := New(host)
			_, generation := service.configSnapshot()
			scope := protocolScopeKey("synthetic-auth", continuityTestStorage("synthetic-credential"), "gpt-6-luna", "https://api.example", translate.EndpointResponses, "synthetic-session", "main", generation)
			request := []byte(`{"model":"gpt-6-luna","messages":[{"role":"user","content":"Synthetic prompt"}],"stream":true}`)
			service.pumpStream(context.Background(), "output", translate.EndpointResponses, destination, "gpt-6-luna", request, request, transport.Stream{ID: "upstream"}, scope, reasoningCarrierScope{}, "", "")
			if host.closedOutputMessage != "" || len(host.closedStreamIDs) != 1 {
				t.Fatalf("partial translation closure: %q / %v", host.closedOutputMessage, host.closedStreamIDs)
			}
			output := string(bytes.Join(host.emitted, nil))
			stop := `"stop_reason":"max_tokens"`
			if destination == "openai" {
				stop = `"finish_reason":"length"`
			}
			if !strings.Contains(output, "Partial answer") || !strings.Contains(output, stop) {
				t.Fatalf("partial translation omitted text or partial stop reason: %s", output)
			}
			if entries := service.reasoningReplayForScope(scope); len(entries) != 0 {
				t.Fatalf("translated partial reasoning cached as successful replay: %+v", entries)
			}
		})
	}
}
