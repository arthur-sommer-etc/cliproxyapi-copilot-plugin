package provider

import (
	"testing"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
)

func TestSelectEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		model     upstreamModel
		want      string
		wantError bool
	}{
		{
			name:  "responses preferred",
			model: upstreamModel{ID: "model-a", SupportedEndpoints: []string{"/chat/completions", "/responses"}},
			want:  translate.EndpointResponses,
		},
		{
			name:  "messages fallback",
			model: upstreamModel{ID: "model-b", SupportedEndpoints: []string{"messages"}},
			want:  translate.EndpointMessages,
		},
		{
			name:  "sol forced to responses",
			model: upstreamModel{ID: "gpt-5.6-sol", SupportedEndpoints: []string{"/chat/completions"}},
			want:  translate.EndpointResponses,
		},
		{
			name:  "terra forced to responses",
			model: upstreamModel{ID: "GPT-5.6-TERRA"},
			want:  translate.EndpointResponses,
		},
		{
			name:      "unsupported",
			model:     upstreamModel{ID: "embedding-model", SupportedEndpoints: []string{"/embeddings"}},
			wantError: true,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := selectEndpoint(test.model)
			if test.wantError {
				if err == nil {
					t.Fatal("expected an endpoint selection error")
				}
				return
			}
			if err != nil {
				t.Fatalf("select endpoint: %v", err)
			}
			if got != test.want {
				t.Fatalf("endpoint = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNormalizeModelsAddsResponsesMetadata(t *testing.T) {
	t.Parallel()

	models := normalizeModels([]upstreamModel{
		{
			ID:                 "gpt-5.6-sol",
			SupportedEndpoints: []string{"/chat/completions"},
			Capabilities: modelCapabilities{
				Supports: modelSupports{Streaming: true, ToolCalls: true, Vision: true},
				Limits:   modelLimits{MaxPromptTokens: 100, MaxOutputTokens: 20},
			},
		},
	})
	if len(models) != 1 || !contains(models[0].SupportedEndpoints, translate.EndpointResponses) {
		t.Fatalf("responses endpoint was not added: %#v", models)
	}
	info := modelInfos(models)[0]
	if !contains(info.SupportedGenerationMethods, translate.EndpointResponses) {
		t.Fatalf("model metadata omits responses endpoint: %#v", info.SupportedGenerationMethods)
	}
	if !contains(info.SupportedInputModalities, "IMAGE") {
		t.Fatalf("model metadata omits image support: %#v", info.SupportedInputModalities)
	}
}
