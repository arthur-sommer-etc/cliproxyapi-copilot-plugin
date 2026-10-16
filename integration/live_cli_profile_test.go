//go:build !windows

package integration

import (
	_ "embed"
	"fmt"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

//go:embed testdata/codex_gpt_6_luna_responses.json
var liveCodexModelCatalog string

func liveCodexConfiguration(base, catalogPath string) string {
	return fmt.Sprintf("model = \"gpt-6-luna\"\nmodel_provider = \"task_proxy\"\nmodel_catalog_json = %q\nweb_search = \"live\"\n[features]\nmulti_agent_v2 = true\ninstant_interrupt = false\n[agents]\nmax_concurrent_threads_per_session = 2\n[model_providers.task_proxy]\nname = \"Task proxy\"\nbase_url = %q\nenv_key = \"TASK_PROXY_API_KEY\"\nwire_api = \"responses\"\nsupports_websockets = false\nrequest_max_retries = 0\nstream_max_retries = 0\n", catalogPath, base+"/v1")
}

func TestLiveCodexConfiguration(t *testing.T) {
	configuration := liveCodexConfiguration("http://127.0.0.1:12345", "/tmp/isolated-client/models.json")
	var parsed struct {
		Model            string `toml:"model"`
		ModelProvider    string `toml:"model_provider"`
		ModelCatalogJSON string `toml:"model_catalog_json"`
		WebSearch        string `toml:"web_search"`
		Features         struct {
			MultiAgentV2     bool  `toml:"multi_agent_v2"`
			InstantInterrupt *bool `toml:"instant_interrupt"`
		} `toml:"features"`
		Providers map[string]struct {
			BaseURL            string `toml:"base_url"`
			WireAPI            string `toml:"wire_api"`
			SupportsWebsockets *bool  `toml:"supports_websockets"`
		} `toml:"model_providers"`
	}
	if err := toml.Unmarshal([]byte(configuration), &parsed); err != nil {
		t.Fatal(err)
	}
	provider := parsed.Providers["task_proxy"]
	if parsed.Model != "gpt-6-luna" || parsed.ModelProvider != "task_proxy" || parsed.ModelCatalogJSON != "/tmp/isolated-client/models.json" || parsed.WebSearch != "live" || !parsed.Features.MultiAgentV2 {
		t.Fatal("isolated profile lost exact model, local metadata, native search or V2 delegation")
	}
	if provider.BaseURL != "http://127.0.0.1:12345/v1" || provider.WireAPI != "responses" || provider.SupportsWebsockets == nil || *provider.SupportsWebsockets || parsed.Features.InstantInterrupt == nil || *parsed.Features.InstantInterrupt {
		t.Fatal("isolated profile did not explicitly disable unsupported WebSocket and interrupt capabilities")
	}
	if strings.Contains(configuration, "responses_websockets") || strings.Contains(configuration, "model_catalog_url") {
		t.Fatal("isolated profile uses a removed feature or unsupported catalog setting")
	}
}
