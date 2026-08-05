package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/provider"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var pluginService = provider.New(hostTransport{})
var pluginVersion = "0.3.1"

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	ModelProvider         bool                         `json:"model_provider"`
	AuthProvider          bool                         `json:"auth_provider"`
	Executor              bool                         `json:"executor"`
	ExecutorModelScope    pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats  []string                     `json:"executor_input_formats"`
	ExecutorOutputFormats []string                     `json:"executor_output_formats"`
}

type identifierResponse struct {
	Identifier string `json:"identifier"`
}

type rpcAuthLoginStartRequest struct {
	pluginapi.AuthLoginStartRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcAuthLoginPollRequest struct {
	pluginapi.AuthLoginPollRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcAuthRefreshRequest struct {
	pluginapi.AuthRefreshRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcAuthModelRequest struct {
	pluginapi.AuthModelRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

func handleMethod(method string, request []byte) ([]byte, bool) {
	result, errHandle := dispatch(method, request)
	if errHandle != nil {
		var statusErr *provider.StatusError
		if errors.As(errHandle, &statusErr) {
			return errorEnvelope(statusErr.Code, statusErr.Message, statusErr.HTTPStatus, statusErr.Retryable), true
		}
		return errorEnvelope("plugin_error", errHandle.Error(), http.StatusInternalServerError, false), true
	}
	raw, errEnvelope := okEnvelope(result)
	if errEnvelope != nil {
		return errorEnvelope("encoding_error", errEnvelope.Error(), http.StatusInternalServerError, false), true
	}
	return raw, false
}

func dispatch(method string, request []byte) (any, error) {
	ctx := context.Background()
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		var req lifecycleRequest
		if len(request) > 0 {
			if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
				return nil, errUnmarshal
			}
		}
		if errConfigure := pluginService.Configure(req.ConfigYAML); errConfigure != nil {
			return nil, errConfigure
		}
		return pluginRegistration(), nil
	case pluginabi.MethodModelStatic:
		return pluginService.StaticModels(), nil
	case pluginabi.MethodModelForAuth:
		var req rpcAuthModelRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return pluginService.ModelsForAuth(ctx, req.HostCallbackID, req.AuthModelRequest)
	case pluginabi.MethodAuthIdentifier, pluginabi.MethodExecutorIdentifier:
		return identifierResponse{Identifier: "copilot"}, nil
	case pluginabi.MethodAuthParse:
		var req pluginapi.AuthParseRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return pluginService.ParseAuth(req)
	case pluginabi.MethodAuthLoginStart:
		var req rpcAuthLoginStartRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return pluginService.StartLogin(ctx, req.HostCallbackID)
	case pluginabi.MethodAuthLoginPoll:
		var req rpcAuthLoginPollRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return pluginService.PollLogin(ctx, req.HostCallbackID, req.State)
	case pluginabi.MethodAuthRefresh:
		var req rpcAuthRefreshRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return pluginService.RefreshAuth(ctx, req.HostCallbackID, req.AuthRefreshRequest)
	case pluginabi.MethodExecutorExecute:
		var req provider.ExecuteRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return pluginService.Execute(ctx, req)
	case pluginabi.MethodExecutorExecuteStream:
		var req provider.ExecuteRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		headers, errStream := pluginService.ExecuteStream(ctx, req)
		if errStream != nil {
			return nil, errStream
		}
		return map[string]any{"headers": headers}, nil
	case pluginabi.MethodExecutorCountTokens:
		return nil, pluginService.CountTokens()
	case pluginabi.MethodExecutorHTTPRequest:
		var req provider.HTTPRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return pluginService.HTTP(ctx, req)
	default:
		return nil, &provider.StatusError{
			Code:       "unknown_method",
			Message:    "unknown plugin method: " + method,
			HTTPStatus: http.StatusNotImplemented,
		}
	}
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "GitHub Copilot subscription provider",
			Version:          pluginVersion,
			Author:           "self-owned",
			GitHubRepository: "https://github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "github_client_id", Type: pluginapi.ConfigFieldTypeString, Description: "Public GitHub OAuth application client identifier used for device flow."},
				{Name: "github_scope", Type: pluginapi.ConfigFieldTypeString, Description: "Space-delimited GitHub OAuth scopes; defaults to the least-privilege read:user scope."},
				{Name: "github_base_url", Type: pluginapi.ConfigFieldTypeString, Description: "GitHub web OAuth base URL."},
				{Name: "github_api_url", Type: pluginapi.ConfigFieldTypeString, Description: "GitHub REST API base URL used for identity and Copilot token exchange."},
				{Name: "copilot_api_url", Type: pluginapi.ConfigFieldTypeString, Description: "Fallback Copilot API base URL when the token response has no API endpoint."},
				{Name: "oauth_timeout_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "Maximum device-code lifetime accepted by the plugin."},
				{Name: "model_cache_ttl_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "In-memory Copilot model catalog cache lifetime."},
				{Name: "token_expiry_buffer_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "Refresh Copilot API tokens this long before expiration."},
			},
		},
		Capabilities: registrationCapability{
			ModelProvider:         true,
			AuthProvider:          true,
			Executor:              true,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeOAuth,
			ExecutorInputFormats:  []string{"openai-response", "claude"},
			ExecutorOutputFormats: []string{"openai-response", "claude"},
		},
	}
}

func okEnvelope(value any) ([]byte, error) {
	result, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(pluginabi.Envelope{OK: true, Result: result})
}

func errorEnvelope(code, message string, status int, retryable bool) []byte {
	raw, _ := json.Marshal(pluginabi.Envelope{
		OK: false,
		Error: &pluginabi.Error{
			Code:       code,
			Message:    message,
			HTTPStatus: status,
			Retryable:  retryable,
		},
	})
	return raw
}
