package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

type hostTransport struct{}

const methodHostPayloadFinalize = "host.payload.finalize"

type hostHTTPRequest struct {
	HostCallbackID string              `json:"host_callback_id,omitempty"`
	Method         string              `json:"method"`
	URL            string              `json:"url"`
	Headers        map[string][]string `json:"headers,omitempty"`
	Body           []byte              `json:"body,omitempty"`
}

type hostPayloadFinalizeRequest struct {
	HostCallbackID string `json:"host_callback_id,omitempty"`
	Protocol       string `json:"protocol"`
	Body           []byte `json:"body"`
}

type hostPayloadFinalizeResponse struct {
	Body []byte `json:"body"`
}

type hostHTTPResponse struct {
	StatusCode int                 `json:"StatusCode"`
	Headers    map[string][]string `json:"Headers"`
	Body       []byte              `json:"Body"`
}

type hostHTTPStreamResponse struct {
	StatusCode int                 `json:"status_code"`
	Headers    map[string][]string `json:"headers,omitempty"`
	StreamID   string              `json:"stream_id"`
}

type hostHTTPStreamReadRequest struct {
	StreamID string `json:"stream_id"`
}

type hostHTTPStreamReadResponse struct {
	Payload []byte `json:"payload,omitempty"`
	Error   string `json:"error,omitempty"`
	Done    bool   `json:"done,omitempty"`
}

type hostStreamEmitRequest struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
}

type hostStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

func (hostTransport) Do(_ context.Context, callbackID string, req transport.Request) (transport.Response, error) {
	raw, errCall := callHost(pluginabi.MethodHostHTTPDo, hostHTTPRequest{
		HostCallbackID: callbackID,
		Method:         req.Method,
		URL:            req.URL,
		Headers:        req.Headers,
		Body:           req.Body,
	})
	if errCall != nil {
		return transport.Response{}, errCall
	}
	var resp hostHTTPResponse
	if errUnmarshal := json.Unmarshal(raw, &resp); errUnmarshal != nil {
		return transport.Response{}, fmt.Errorf("decode host HTTP response: %w", errUnmarshal)
	}
	return transport.Response{StatusCode: resp.StatusCode, Headers: resp.Headers, Body: resp.Body}, nil
}

func (hostTransport) FinalizePayload(ctx context.Context, callbackID, protocol string, body []byte) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, errCall := callHost(methodHostPayloadFinalize, hostPayloadFinalizeRequest{
		HostCallbackID: callbackID,
		Protocol:       protocol,
		Body:           append([]byte(nil), body...),
	})
	if errCall != nil {
		if isUnsupportedHostPayloadFinalization(errCall) {
			return nil, transport.ErrPayloadFinalizationUnavailable
		}
		return nil, fmt.Errorf("call host payload finalization: %w", errCall)
	}
	var response hostPayloadFinalizeResponse
	if errUnmarshal := json.Unmarshal(raw, &response); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host payload finalization response: %w", errUnmarshal)
	}
	return append([]byte(nil), response.Body...), nil
}

func isUnsupportedHostPayloadFinalization(err error) bool {
	return err != nil && err.Error() == "unsupported host callback "+methodHostPayloadFinalize
}

func (hostTransport) OpenStream(_ context.Context, callbackID string, req transport.Request) (transport.Stream, error) {
	raw, errCall := callHost(pluginabi.MethodHostHTTPDoStream, hostHTTPRequest{
		HostCallbackID: callbackID,
		Method:         req.Method,
		URL:            req.URL,
		Headers:        req.Headers,
		Body:           req.Body,
	})
	if errCall != nil {
		return transport.Stream{}, errCall
	}
	var resp hostHTTPStreamResponse
	if errUnmarshal := json.Unmarshal(raw, &resp); errUnmarshal != nil {
		return transport.Stream{}, fmt.Errorf("decode host HTTP stream response: %w", errUnmarshal)
	}
	if strings.TrimSpace(resp.StreamID) == "" {
		return transport.Stream{}, fmt.Errorf("host HTTP stream response has no stream_id")
	}
	return transport.Stream{StatusCode: resp.StatusCode, Headers: resp.Headers, ID: resp.StreamID}, nil
}

func (hostTransport) ReadStream(_ context.Context, streamID string) (transport.StreamChunk, error) {
	raw, errCall := callHost(pluginabi.MethodHostHTTPStreamRead, hostHTTPStreamReadRequest{StreamID: streamID})
	if errCall != nil {
		return transport.StreamChunk{}, errCall
	}
	var resp hostHTTPStreamReadResponse
	if errUnmarshal := json.Unmarshal(raw, &resp); errUnmarshal != nil {
		return transport.StreamChunk{}, fmt.Errorf("decode host HTTP stream chunk: %w", errUnmarshal)
	}
	return transport.StreamChunk{Payload: resp.Payload, Error: resp.Error, Done: resp.Done}, nil
}

func (hostTransport) CloseStream(_ context.Context, streamID string) error {
	_, errCall := callHost(pluginabi.MethodHostHTTPStreamClose, hostHTTPStreamReadRequest{StreamID: streamID})
	return errCall
}

func (hostTransport) Emit(_ context.Context, streamID string, payload []byte) error {
	_, errCall := callHost(pluginabi.MethodHostStreamEmit, hostStreamEmitRequest{StreamID: streamID, Payload: payload})
	return errCall
}

func (hostTransport) CloseOutput(_ context.Context, streamID, message string) {
	_, _ = callHost(pluginabi.MethodHostStreamClose, hostStreamCloseRequest{StreamID: streamID, Error: message})
}
