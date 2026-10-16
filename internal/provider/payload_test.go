package provider

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
)

type payloadFinalizerHost struct {
	protocol string
	callback string
	body     []byte
	err      error
	calls    int
}

func (h *payloadFinalizerHost) Do(context.Context, string, transport.Request) (transport.Response, error) {
	return transport.Response{}, errors.New("unexpected host request")
}

func (h *payloadFinalizerHost) OpenStream(context.Context, string, transport.Request) (transport.Stream, error) {
	return transport.Stream{}, errors.New("unexpected host stream")
}

func (*payloadFinalizerHost) ReadStream(context.Context, string) (transport.StreamChunk, error) {
	return transport.StreamChunk{}, errors.New("unexpected host stream read")
}

func (*payloadFinalizerHost) CloseStream(context.Context, string) error   { return nil }
func (*payloadFinalizerHost) Emit(context.Context, string, []byte) error  { return nil }
func (*payloadFinalizerHost) CloseOutput(context.Context, string, string) {}

func (h *payloadFinalizerHost) FinalizePayload(_ context.Context, callbackID, protocol string, body []byte) ([]byte, error) {
	h.calls++
	h.callback = callbackID
	h.protocol = protocol
	h.body = append([]byte(nil), body...)
	if h.err != nil {
		return nil, h.err
	}
	return append(append([]byte(nil), body...), []byte("-configured")...), nil
}

func TestFinalizeModelRequestPassesActualEgressProtocolAndUsesReturnedBody(t *testing.T) {
	tests := []struct {
		endpoint string
		protocol string
	}{
		{endpoint: translate.EndpointResponses, protocol: "openai-response"},
		{endpoint: translate.EndpointChatCompletions, protocol: "openai"},
		{endpoint: translate.EndpointMessages, protocol: "claude"},
	}
	for _, test := range tests {
		t.Run(test.endpoint, func(t *testing.T) {
			host := &payloadFinalizerHost{}
			service := New(host)
			input := []byte(`{"model":"test"}`)
			got, errFinalize := service.finalizeModelRequest(context.Background(), "callback-1", test.endpoint, input)
			if errFinalize != nil {
				t.Fatalf("finalizeModelRequest() error = %v", errFinalize)
			}
			if host.calls != 1 || host.callback != "callback-1" || host.protocol != test.protocol || string(host.body) != string(input) {
				t.Fatalf("host finalizer call = calls:%d callback:%q protocol:%q body:%s", host.calls, host.callback, host.protocol, host.body)
			}
			if string(got) != string(input)+"-configured" {
				t.Fatalf("finalized body = %q, want returned finalizer body", got)
			}
		})
	}
}

func TestFinalizeModelRequestKeepsStandalonePayloadWithoutCallback(t *testing.T) {
	service := New(&payloadFinalizerHost{})
	input := []byte(`{"model":"test"}`)
	got, errFinalize := service.finalizeModelRequest(context.Background(), "", translate.EndpointResponses, input)
	if errFinalize != nil {
		t.Fatalf("finalizeModelRequest() error = %v", errFinalize)
	}
	if string(got) != string(input) {
		t.Fatalf("finalized body = %s, want unchanged %s", got, input)
	}
	got[0] = 'x'
	if input[0] == 'x' {
		t.Fatal("standalone finalized body aliases input")
	}
}

func TestFinalizeModelRequestRequiresPayloadFinalizationForCallback(t *testing.T) {
	service := New(&errorStreamHost{})
	_, errFinalize := service.finalizeModelRequest(context.Background(), "callback-1", translate.EndpointResponses, []byte(`{"model":"test"}`))
	var statusErr *StatusError
	if !errors.As(errFinalize, &statusErr) || statusErr.Code != "payload_finalization_unavailable" || statusErr.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("finalization error = %#v, want sanitized payload_finalization_unavailable 503", errFinalize)
	}
	if statusErr.Message == "" || statusErr.Message == transport.ErrPayloadFinalizationUnavailable.Error() {
		t.Fatalf("client error message = %q, want clear actionable message", statusErr.Message)
	}
}

func TestFinalizeModelRequestSanitizesHostFailureAsServiceUnavailable(t *testing.T) {
	service := New(&payloadFinalizerHost{err: errors.New("sensitive host callback detail")})
	_, errFinalize := service.finalizeModelRequest(context.Background(), "callback-1", translate.EndpointMessages, []byte(`{"model":"test"}`))
	var statusErr *StatusError
	if !errors.As(errFinalize, &statusErr) || statusErr.Code != "payload_finalization_failed" || statusErr.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("finalization error = %#v, want payload_finalization_failed 503", errFinalize)
	}
	if statusErr.Message == "" || statusErr.Message == "sensitive host callback detail" {
		t.Fatalf("client error message = %q, want sanitized actionable message", statusErr.Message)
	}
}

func TestFinalizeModelRequestPreservesContextCancellation(t *testing.T) {
	host := &payloadFinalizerHost{}
	service := New(host)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, errFinalize := service.finalizeModelRequest(ctx, "callback-1", translate.EndpointResponses, []byte(`{"model":"test"}`))
	if !errors.Is(errFinalize, context.Canceled) {
		t.Fatalf("finalization error = %v, want context.Canceled", errFinalize)
	}
	if host.calls != 0 {
		t.Fatalf("host finalizer calls = %d, want zero after cancellation", host.calls)
	}
}

func TestFinalizeModelRequestRejectsUnknownEndpoint(t *testing.T) {
	host := &payloadFinalizerHost{}
	service := New(host)
	_, errFinalize := service.finalizeModelRequest(context.Background(), "callback-1", "/unknown", []byte(`{"model":"test"}`))
	var statusErr *StatusError
	if !errors.As(errFinalize, &statusErr) || statusErr.Code != "payload_finalization_unavailable" || statusErr.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("finalization error = %#v, want unsupported protocol 503", errFinalize)
	}
	if host.calls != 0 {
		t.Fatalf("host finalizer calls = %d, want zero for unknown endpoint", host.calls)
	}
}
