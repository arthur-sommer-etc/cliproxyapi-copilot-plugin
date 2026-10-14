package provider

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
)

type scriptedStreamHost struct {
	transport.Host
	chunks       []transport.StreamChunk
	readError    error
	emitError    error
	reads        int
	emitted      []string
	calls        []string
	closeMessage string
	readContext  context.Context
}

func (h *scriptedStreamHost) ReadStream(ctx context.Context, id string) (transport.StreamChunk, error) {
	h.calls = append(h.calls, "read:"+id)
	h.readContext = ctx
	h.reads++
	if h.readError != nil {
		return transport.StreamChunk{}, h.readError
	}
	if h.reads > len(h.chunks) {
		panic("read beyond scripted stream termination")
	}
	return h.chunks[h.reads-1], nil
}

func (h *scriptedStreamHost) CloseStream(ctx context.Context, id string) error {
	if ctx.Err() != nil {
		panic("cleanup used a canceled context")
	}
	h.calls = append(h.calls, "close:"+id)
	return nil
}

func (h *scriptedStreamHost) Emit(_ context.Context, id string, payload []byte) error {
	h.calls = append(h.calls, "emit:"+id)
	h.emitted = append(h.emitted, string(payload))
	return h.emitError
}

func (h *scriptedStreamHost) CloseOutput(_ context.Context, id, message string) {
	h.calls = append(h.calls, "output:"+id)
	h.closeMessage = message
}

func TestPumpStreamTerminationAndCleanup(t *testing.T) {
	frame := "data: {\"type\":\"response.completed\"}\n\n"
	trailing := "data: {\"type\":\"response.completed\"}"
	tests := []struct {
		name      string
		chunks    []transport.StreamChunk
		readError error
		emitError error
		wantCalls []string
		wantEmit  []string
		wantError string
	}{
		{
			name:      "done payload and trailing frame",
			chunks:    []transport.StreamChunk{{Payload: []byte(frame + trailing), Done: true}},
			wantCalls: []string{"read:upstream", "emit:output", "emit:output", "close:upstream", "output:output"},
			wantEmit:  []string{frame, trailing},
		},
		{
			name:      "split frame then done",
			chunks:    []transport.StreamChunk{{Payload: []byte(frame[:7])}, {Payload: []byte(frame[7:]), Done: true}},
			wantCalls: []string{"read:upstream", "read:upstream", "emit:output", "close:upstream", "output:output"},
			wantEmit:  []string{frame},
		},
		{
			name:      "read failure",
			readError: errors.New("read failed"),
			wantCalls: []string{"read:upstream", "close:upstream", "output:output"},
			wantError: "read Copilot stream: read failed",
		},
		{
			name:      "provider error discards payload",
			chunks:    []transport.StreamChunk{{Payload: []byte(frame), Error: "provider failed", Done: true}},
			wantCalls: []string{"read:upstream", "close:upstream", "output:output"},
			wantError: "Copilot stream error: provider failed",
		},
		{
			name:      "emit failure stops before next frame",
			chunks:    []transport.StreamChunk{{Payload: []byte(frame + frame)}},
			emitError: errors.New("emit failed"),
			wantCalls: []string{"read:upstream", "emit:output", "close:upstream", "output:output"},
			wantEmit:  []string{frame},
			wantError: "translate Copilot stream: emit failed",
		},
		{
			name:      "trailing emit failure",
			chunks:    []transport.StreamChunk{{Payload: []byte(trailing), Done: true}},
			emitError: errors.New("emit failed"),
			wantCalls: []string{"read:upstream", "emit:output", "close:upstream", "output:output"},
			wantEmit:  []string{trailing},
			wantError: "translate final Copilot stream frame: emit failed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			host := &scriptedStreamHost{chunks: test.chunks, readError: test.readError, emitError: test.emitError}
			service := &Service{host: host}
			service.pumpStream("output", translate.EndpointResponses, "openai-response", "test-model", nil, nil, transport.Stream{ID: "upstream"})
			if !reflect.DeepEqual(host.calls, test.wantCalls) {
				t.Errorf("calls = %v, want %v", host.calls, test.wantCalls)
			}
			if !reflect.DeepEqual(host.emitted, test.wantEmit) {
				t.Errorf("emitted = %q, want %q", host.emitted, test.wantEmit)
			}
			if host.closeMessage != test.wantError {
				t.Errorf("close message = %q, want %q", host.closeMessage, test.wantError)
			}
		})
	}
}

func TestCollectStreamErrorStopsAndCloses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	host := &scriptedStreamHost{chunks: []transport.StreamChunk{{Payload: []byte("first")}, {Payload: []byte("last"), Done: true}}}
	body, err := (&Service{host: host}).collectStreamError(ctx, transport.Stream{ID: "upstream"})
	if err != nil || string(body) != "firstlast" {
		t.Fatalf("body = %q, error = %v", body, err)
	}
	if host.readContext != ctx {
		t.Error("caller context was not passed to ReadStream")
	}
	if want := []string{"read:upstream", "read:upstream", "close:upstream"}; !reflect.DeepEqual(host.calls, want) {
		t.Errorf("calls = %v, want %v", host.calls, want)
	}
}

func TestCollectStreamErrorPropagatesFailure(t *testing.T) {
	for _, test := range []struct {
		name      string
		readError error
		chunk     transport.StreamChunk
		wantError string
	}{
		{name: "read", readError: errors.New("read failed"), wantError: "read failed"},
		{name: "chunk", chunk: transport.StreamChunk{Payload: []byte("discard"), Error: "provider failed", Done: true}, wantError: "provider failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := &scriptedStreamHost{chunks: []transport.StreamChunk{test.chunk}, readError: test.readError}
			body, err := (&Service{host: host}).collectStreamError(context.Background(), transport.Stream{ID: "upstream"})
			if body != nil || err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("body = %q, error = %v", body, err)
			}
			if want := []string{"read:upstream", "close:upstream"}; !reflect.DeepEqual(host.calls, want) {
				t.Errorf("calls = %v, want %v", host.calls, want)
			}
		})
	}
}
