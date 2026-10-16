package transport

import (
	"context"
	"errors"
	"testing"
)

type legacyPayloadHost struct{}

func (legacyPayloadHost) Do(context.Context, string, Request) (Response, error) {
	return Response{}, nil
}

func (legacyPayloadHost) OpenStream(context.Context, string, Request) (Stream, error) {
	return Stream{}, nil
}

func (legacyPayloadHost) ReadStream(context.Context, string) (StreamChunk, error) {
	return StreamChunk{}, nil
}

func (legacyPayloadHost) CloseStream(context.Context, string) error {
	return nil
}

func (legacyPayloadHost) Emit(context.Context, string, []byte) error {
	return nil
}

func (legacyPayloadHost) CloseOutput(context.Context, string, string) {}

type payloadHost struct {
	legacyPayloadHost
	finalize func(context.Context, string, string, []byte) ([]byte, error)
}

func (h payloadHost) FinalizePayload(ctx context.Context, callbackID, protocol string, body []byte) ([]byte, error) {
	return h.finalize(ctx, callbackID, protocol, body)
}

func TestFinalizePayloadRequiresHostSupportForManagedRequests(t *testing.T) {
	body := []byte(`{"model":"m"}`)
	got, errFinalize := FinalizePayload(context.Background(), legacyPayloadHost{}, "", "", body)
	if errFinalize != nil {
		t.Fatalf("FinalizePayload() standalone error = %v", errFinalize)
	}
	if string(got) != string(body) {
		t.Fatalf("FinalizePayload() standalone body = %s, want %s", got, body)
	}

	if _, errFinalize := FinalizePayload(context.Background(), legacyPayloadHost{}, "callback-1", "openai", body); !errors.Is(errFinalize, ErrPayloadFinalizationUnavailable) {
		t.Fatalf("FinalizePayload() without host support error = %v, want ErrPayloadFinalizationUnavailable", errFinalize)
	}
}

func TestFinalizePayloadCallsExplicitHostFinalizer(t *testing.T) {
	var called bool
	host := payloadHost{finalize: func(ctx context.Context, callbackID, protocol string, body []byte) ([]byte, error) {
		called = true
		if callbackID != "callback-2" || protocol != "claude" {
			t.Fatalf("finalizer scope = callback %q protocol %q", callbackID, protocol)
		}
		if string(body) != `{"model":"m"}` {
			t.Fatalf("finalizer body = %s", body)
		}
		return []byte(`{"model":"m","configured":true}`), nil
	}}
	got, errFinalize := FinalizePayload(context.Background(), host, "callback-2", "claude", []byte(`{"model":"m"}`))
	if errFinalize != nil {
		t.Fatalf("FinalizePayload() error = %v", errFinalize)
	}
	if !called || string(got) != `{"model":"m","configured":true}` {
		t.Fatalf("FinalizePayload() called = %t body = %s", called, got)
	}
}

func TestFinalizePayloadPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	host := payloadHost{finalize: func(context.Context, string, string, []byte) ([]byte, error) {
		t.Fatal("finalizer called after cancellation")
		return nil, nil
	}}
	if _, errFinalize := FinalizePayload(ctx, host, "callback-3", "openai-response", []byte(`{}`)); !errors.Is(errFinalize, context.Canceled) {
		t.Fatalf("FinalizePayload() error = %v, want context.Canceled", errFinalize)
	}
}

var _ Host = payloadHost{}
var _ PayloadFinalizer = payloadHost{}
