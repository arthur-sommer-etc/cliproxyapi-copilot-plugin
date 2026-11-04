package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
)

type reasoningEOFHost struct {
	sseLineEndingHost
	chunks           []transport.StreamChunk
	readError        error
	closeError       error
	readIndex        int
	closedBeforeEmit bool
}

func (h *reasoningEOFHost) ReadStream(context.Context, string) (transport.StreamChunk, error) {
	if h.readIndex < len(h.chunks) {
		chunk := h.chunks[h.readIndex]
		h.readIndex++
		return chunk, nil
	}
	if h.readError != nil {
		return transport.StreamChunk{}, h.readError
	}
	return transport.StreamChunk{Done: true}, nil
}
func (h *reasoningEOFHost) CloseStream(_ context.Context, streamID string) error {
	h.closedStreamIDs = append(h.closedStreamIDs, streamID)
	return h.closeError
}
func (h *reasoningEOFHost) Emit(_ context.Context, _ string, frame []byte) error {
	if len(h.closedStreamIDs) == 0 && (bytes.Contains(frame, []byte(`"type":"response.completed"`)) || bytes.Contains(frame, []byte(`"type":"reasoning"`)) || bytes.Contains(frame, []byte(`"type":"thinking_delta"`)) || bytes.Contains(frame, []byte(`"type":"signature_delta"`))) {
		h.closedBeforeEmit = true
	}
	h.emitted = append(h.emitted, append([]byte(nil), frame...))
	return nil
}

func TestChatReasoningStreamWaitsForEOFAndSuccessfulClose(t *testing.T) {
	const model = "gemini-3.8-flash"
	payload := []byte("data: {\"id\":\"chat_stream\",\"object\":\"chat.completion.chunk\",\"created\":7,\"model\":\"gemini-3.8-flash\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning_text\":\"thought\",\"content\":\"answer\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chat_stream\",\"object\":\"chat.completion.chunk\",\"created\":7,\"model\":\"gemini-3.8-flash\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"chat_stream\",\"object\":\"chat.completion.chunk\",\"created\":7,\"model\":\"gemini-3.8-flash\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_opaque\":\"late-state\"},\"finish_reason\":null}]}\n\n" +
		"data: [DONE]\n\n")
	for _, tc := range []struct {
		name              string
		readErr, closeErr error
	}{
		{name: "success"}, {name: "EOF read failure", readErr: errors.New("synthetic EOF failure")}, {name: "close failure", closeErr: errors.New("synthetic close failure")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := &reasoningEOFHost{chunks: []transport.StreamChunk{{Payload: payload}}, readError: tc.readErr, closeError: tc.closeErr}
			s := New(host)
			request := []byte(`{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"start"}],"stream":true}`)
			s.pumpStream(context.Background(), "output", translate.EndpointChatCompletions, "openai-response", model, nil, request, transport.Stream{ID: "upstream"}, "", testReasoningCarrierScope(), "", "")
			output := string(bytes.Join(host.emitted, nil))
			if host.closedBeforeEmit {
				t.Fatalf("emitted reasoning before upstream closed: %s", output)
			}
			if tc.readErr != nil || tc.closeErr != nil {
				if host.closedOutputMessage == "" || strings.Contains(output, `"type":"response.completed"`) {
					t.Fatalf("failure was reported as success: message=%q output=%s", host.closedOutputMessage, output)
				}
			} else if host.closedOutputMessage != "" || !strings.Contains(output, "thought") || !strings.Contains(output, `"type":"response.completed"`) {
				t.Fatalf("successful stream lost thinking or completion: message=%q output=%s", host.closedOutputMessage, output)
			}
			if len(host.closedStreamIDs) != 1 {
				t.Fatal(fmt.Sprintf("closed upstream %d times", len(host.closedStreamIDs)))
			}
		})
	}
}

func TestChatReasoningStreamTerminalBoundaries(t *testing.T) {
	first := "data: {\"id\":\"chat\",\"object\":\"chat.completion.chunk\",\"created\":7,\"model\":\"gemini-3.8-flash\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning_text\":\"thought\",\"content\":\"visible text\"},\"finish_reason\":null}]}\n\n"
	finish := "data: {\"id\":\"chat\",\"object\":\"chat.completion.chunk\",\"created\":7,\"model\":\"gemini-3.8-flash\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"
	late := "data: {\"id\":\"chat\",\"object\":\"chat.completion.chunk\",\"created\":7,\"model\":\"gemini-3.8-flash\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_opaque\":\"late\"},\"finish_reason\":null}]}\n\n"
	done := "data: [DONE]\n\n"
	for _, target := range []string{"claude", "openai-response"} {
		for _, tc := range []struct {
			name, body        string
			readErr, closeErr error
			wantSuccess       bool
		}{
			{"success", first + finish + late + done, nil, nil, true}, {"no_done", first + finish + late, nil, nil, false}, {"post_done", first + finish + done + late, nil, nil, false}, {"read_error", first + finish + late + done, errors.New("read failed"), nil, false}, {"close_error", first + finish + late + done, nil, errors.New("close failed"), false},
		} {
			t.Run(target+"/"+tc.name, func(t *testing.T) {
				host := &reasoningEOFHost{chunks: []transport.StreamChunk{{Payload: []byte(tc.body)}}, readError: tc.readErr, closeError: tc.closeErr}
				request := []byte(`{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"start"}],"stream":true}`)
				New(host).pumpStream(context.Background(), "out", translate.EndpointChatCompletions, target, "gemini-3.8-flash", nil, request, transport.Stream{ID: "upstream"}, "", testReasoningCarrierScope(), "", "")
				out := string(bytes.Join(host.emitted, nil))
				terminal := strings.Contains(out, `"type":"response.completed"`) || strings.Contains(out, `"type":"message_stop"`)
				if !strings.Contains(out, "visible text") {
					t.Errorf("visible text absent: %s", out)
				}
				if tc.wantSuccess != terminal || tc.wantSuccess != (host.closedOutputMessage == "") {
					t.Errorf("success=%t terminal=%t error=%q output=%s", tc.wantSuccess, terminal, host.closedOutputMessage, out)
				}
			})
		}
	}
}

type beforeEOFHost struct {
	sseLineEndingHost
	n           int
	allowEOF    chan struct{}
	emittedText chan struct{}
}

func (h *beforeEOFHost) ReadStream(ctx context.Context, _ string) (transport.StreamChunk, error) {
	h.n++
	if h.n == 1 {
		return transport.StreamChunk{Payload: []byte("data: {\"id\":\"chat\",\"object\":\"chat.completion.chunk\",\"created\":7,\"model\":\"gemini-3.8-flash\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning_text\":\"thinking\"},\"finish_reason\":null}]}\n\n")}, nil
	}
	if h.n == 2 {
		return transport.StreamChunk{Payload: []byte("data: {\"id\":\"chat\",\"object\":\"chat.completion.chunk\",\"created\":7,\"model\":\"gemini-3.8-flash\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"first visible text\"},\"finish_reason\":null}]}\n\n")}, nil
	}
	if h.n == 3 {
		select {
		case <-h.allowEOF:
		case <-ctx.Done():
			return transport.StreamChunk{}, ctx.Err()
		}
		return transport.StreamChunk{Payload: []byte("data: {\"id\":\"chat\",\"object\":\"chat.completion.chunk\",\"created\":7,\"model\":\"gemini-3.8-flash\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"), Done: true}, nil
	}
	return transport.StreamChunk{Done: true}, nil
}
func (h *beforeEOFHost) Emit(_ context.Context, _ string, b []byte) error {
	if strings.Contains(string(b), "first visible text") {
		select {
		case h.emittedText <- struct{}{}:
		default:
		}
	}
	return nil
}
func TestChatReasoningStreamEmitsVisibleTextBeforeEOF(t *testing.T) {
	h := &beforeEOFHost{allowEOF: make(chan struct{}), emittedText: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	defer cancel()
	defer close(h.allowEOF)
	go func() {
		New(h).pumpStream(ctx, "out", translate.EndpointChatCompletions, "openai-response", "gemini-3.8-flash", nil, []byte(`{"messages":[{"role":"user","content":"hi"}]}`), transport.Stream{ID: "upstream"}, "", testReasoningCarrierScope(), "", "")
		close(done)
	}()
	select {
	case <-h.emittedText:
	case <-time.After(time.Second):
		t.Fatal("client did not receive the first visible text while the upstream stream remained open")
	}
}
