package provider

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
)

type sseLineEndingHost struct {
	chunk               transport.StreamChunk
	emitted             [][]byte
	closedStreamIDs     []string
	closedOutputID      string
	closedOutputMessage string
}

func (*sseLineEndingHost) Do(context.Context, string, transport.Request) (transport.Response, error) {
	return transport.Response{}, errors.New("unexpected request")
}

func (*sseLineEndingHost) OpenStream(context.Context, string, transport.Request) (transport.Stream, error) {
	return transport.Stream{}, errors.New("unexpected stream")
}

func (h *sseLineEndingHost) ReadStream(context.Context, string) (transport.StreamChunk, error) {
	return h.chunk, nil
}

func (h *sseLineEndingHost) CloseStream(_ context.Context, streamID string) error {
	h.closedStreamIDs = append(h.closedStreamIDs, streamID)
	return nil
}

func (h *sseLineEndingHost) Emit(_ context.Context, _ string, frame []byte) error {
	h.emitted = append(h.emitted, append([]byte(nil), frame...))
	return nil
}

func (h *sseLineEndingHost) CloseOutput(_ context.Context, outputID, message string) {
	h.closedOutputID = outputID
	h.closedOutputMessage = message
}

func TestPumpStreamPreservesCROnlyTextAndTerminalEvents(t *testing.T) {
	t.Parallel()

	textEvent := `{"type":"response.output_text.delta","delta":"answer"}`
	completedEvent := `{"type":"response.completed","response":{"id":"resp_sse","status":"completed","output":[{"id":"msg_sse","type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}}`
	host := &sseLineEndingHost{chunk: transport.StreamChunk{
		Payload: []byte("event: response.output_text.delta\rdata: " + textEvent + "\r\revent: response.completed\rdata: " + completedEvent + "\r\r"),
		Done:    true,
	}}
	service := New(host)
	service.pumpStream(context.Background(), "output", translate.EndpointResponses, "claude", "model", nil, nil, transport.Stream{ID: "upstream"}, "", reasoningCarrierScope{}, "copilot-token", "github-token")

	var output strings.Builder
	for _, frame := range host.emitted {
		output.Write(frame)
	}
	for _, needle := range []string{"event: content_block_delta", `"text":"answer"`, `"stop_reason":"end_turn"`, "event: message_stop"} {
		if !strings.Contains(output.String(), needle) {
			t.Fatalf("translated output omitted %q: %s", needle, output.String())
		}
	}
	if len(host.closedStreamIDs) != 1 || host.closedStreamIDs[0] != "upstream" {
		t.Fatalf("closed upstream streams = %v, want [upstream]", host.closedStreamIDs)
	}
	if host.closedOutputID != "output" || host.closedOutputMessage != "" {
		t.Fatalf("output closure = %q, %q; want successful close of output", host.closedOutputID, host.closedOutputMessage)
	}
}
