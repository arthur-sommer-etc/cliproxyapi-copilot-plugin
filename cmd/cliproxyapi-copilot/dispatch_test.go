package main

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

func TestRegistrationSupportsNativeClientFormats(t *testing.T) {
	result, err := dispatch(pluginabi.MethodPluginRegister, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	registration := result.(registration)
	if registration.SchemaVersion != pluginabi.SchemaVersion || registration.Metadata.Name != "GitHub Copilot subscription provider" || registration.Metadata.Author != "self-owned" || registration.Metadata.GitHubRepository != "https://github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin" {
		t.Fatalf("unexpected registration: %+v", registration)
	}
	for _, format := range []string{"openai-response", "claude", "openai"} {
		if !containsFormat(registration.Capabilities.ExecutorInputFormats, format) || !containsFormat(registration.Capabilities.ExecutorOutputFormats, format) {
			t.Fatalf("missing native client format %s", format)
		}
	}
	result, err = dispatch(pluginabi.MethodExecutorIdentifier, nil)
	if err != nil || result.(identifierResponse).Identifier != "copilot" {
		t.Fatalf("unexpected executor identifier: %+v %v", result, err)
	}
}

func TestRPCErrorRetainsHTTPStatus(t *testing.T) {
	raw, failed := handleMethod("unsupported.operation", nil)
	var result pluginabi.Envelope
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if !failed || result.OK || result.Error == nil || result.Error.HTTPStatus != 501 {
		t.Fatalf("unexpected error envelope: %s", raw)
	}
}

func containsFormat(formats []string, format string) bool {
	for _, candidate := range formats {
		if candidate == format {
			return true
		}
	}
	return false
}
