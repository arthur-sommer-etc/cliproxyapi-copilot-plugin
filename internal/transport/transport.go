package transport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ErrPayloadFinalizationUnavailable reports that a CPA callback is required
// but the configured host does not provide payload finalization.
var ErrPayloadFinalizationUnavailable = errors.New("host payload finalization is unavailable")

type Request struct {
	Method  string
	URL     string
	Headers http.Header
	Body    []byte
}

type Response struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

type Stream struct {
	StatusCode int
	Headers    http.Header
	ID         string
}

type StreamChunk struct {
	Payload []byte
	Error   string
	Done    bool
}

type Host interface {
	Do(context.Context, string, Request) (Response, error)
	OpenStream(context.Context, string, Request) (Stream, error)
	ReadStream(context.Context, string) (StreamChunk, error)
	CloseStream(context.Context, string) error
	Emit(context.Context, string, []byte) error
	CloseOutput(context.Context, string, string)
}

// PayloadFinalizer applies host payload rules to a fully prepared provider body.
type PayloadFinalizer interface {
	FinalizePayload(context.Context, string, string, []byte) ([]byte, error)
}

// FinalizePayload applies the optional host finalizer for CPA-managed requests.
// Standalone requests without a callback ID retain their supplied body.
func FinalizePayload(ctx context.Context, host Host, callbackID, protocol string, body []byte) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(callbackID) == "" {
		return append([]byte(nil), body...), nil
	}
	if strings.TrimSpace(protocol) == "" {
		return nil, fmt.Errorf("payload finalization protocol is required")
	}
	finalizer, ok := host.(PayloadFinalizer)
	if !ok {
		return nil, ErrPayloadFinalizationUnavailable
	}
	finalized, errFinalize := finalizer.FinalizePayload(ctx, callbackID, protocol, append([]byte(nil), body...))
	if errFinalize != nil {
		if errors.Is(errFinalize, ErrPayloadFinalizationUnavailable) {
			return nil, ErrPayloadFinalizationUnavailable
		}
		return nil, fmt.Errorf("finalize host payload: %w", errFinalize)
	}
	return append([]byte(nil), finalized...), nil
}
