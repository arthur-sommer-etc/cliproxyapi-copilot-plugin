package provider

import (
	"context"
	"errors"
	"net/http"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
)

func (s *Service) finalizeModelRequest(ctx context.Context, callbackID, endpoint string, body []byte) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	protocol, ok := payloadProtocolForEndpoint(endpoint)
	if !ok {
		return nil, statusError("payload_finalization_unavailable", "CPA payload configuration cannot be applied to the selected upstream endpoint", http.StatusServiceUnavailable)
	}

	finalized, errFinalize := transport.FinalizePayload(ctx, s.host, callbackID, protocol, body)
	if errFinalize == nil {
		return finalized, nil
	}
	if errContext := ctx.Err(); errContext != nil {
		return nil, errContext
	}
	if errors.Is(errFinalize, transport.ErrPayloadFinalizationUnavailable) {
		return nil, statusError("payload_finalization_unavailable", "Update CLIProxyAPI to a version that supports final payload configuration before using this plugin", http.StatusServiceUnavailable)
	}
	return nil, statusError("payload_finalization_failed", "CLIProxyAPI could not apply payload configuration to the upstream request", http.StatusServiceUnavailable)
}

func payloadProtocolForEndpoint(endpoint string) (string, bool) {
	switch endpoint {
	case translate.EndpointResponses:
		return "openai-response", true
	case translate.EndpointChatCompletions:
		return "openai", true
	case translate.EndpointMessages:
		return "claude", true
	default:
		return "", false
	}
}
