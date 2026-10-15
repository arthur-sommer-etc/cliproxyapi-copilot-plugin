package integration

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

var nativeSeedNames = []string{
	"manifest.json",
	"auth.json",
	"legacy-auth.json",
	"model-catalog.json",
	"responses-request.json",
	"responses-history.json",
	"responses.json",
	"responses.sse",
	"chat.json",
	"chat.sse",
	"messages.json",
	"messages.sse",
	"responses-compaction.json",
	"responses-compaction.sse",
}

type nativeCanonicalRoute struct {
	model string
	path  string
}

var nativeCanonicalRoutes = []nativeCanonicalRoute{
	{model: "gpt-6-luna", path: "/responses"},
	{model: "gpt-6.1-sol", path: "/responses"},
	{model: "mai-code-1.1-flash", path: "/responses"},
	{model: "gemini-3.8-flash", path: "/chat/completions"},
	{model: "claude-sonnet-5.5", path: "/v1/messages"},
	{model: "claude-opus-5.5", path: "/v1/messages"},
	{model: "claude-fable-5.1", path: "/v1/messages"},
	{model: "grok-4.7", path: "/responses"},
}

func readNativeSeed(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "native", "v1", name))
	if err != nil {
		t.Fatalf("read native fixture seed %q: %v", name, err)
	}
	return body
}

func newNativeFixture(t *testing.T) *fixture {
	t.Helper()
	var manifest struct {
		Version int    `json:"version"`
		Kind    string `json:"kind"`
	}
	if err := json.Unmarshal(readNativeSeed(t, "manifest.json"), &manifest); err != nil {
		t.Fatalf("decode native fixture manifest: %v", err)
	}
	if manifest.Version != 1 || manifest.Kind != "synthetic-routing-fixture" {
		t.Fatalf("unexpected native fixture manifest: %+v", manifest)
	}
	seeds := make(map[string][]byte, len(nativeSeedNames))
	for _, name := range nativeSeedNames {
		seeds[name] = readNativeSeed(t, name)
	}
	return &fixture{canceled: make(chan struct{}), seeds: seeds, modelCatalog: seeds["model-catalog.json"], modelTurns: make(map[string]int)}
}

func nativeTemplateConfig(t *testing.T) map[string]any {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "config", "config.yaml"))
	if err != nil {
		t.Fatalf("read checked-in native config template: %v", err)
	}
	config := make(map[string]any)
	if err := yaml.Unmarshal(body, &config); err != nil {
		t.Fatalf("decode checked-in native config template: %v", err)
	}
	return config
}

func nativeConfiguredCompactionModels(t *testing.T) []string {
	t.Helper()
	config := nativeTemplateConfig(t)
	plugins := nativeMap(t, config["plugins"])
	pluginConfigs := nativeMap(t, plugins["configs"])
	plugin := nativeMap(t, pluginConfigs["cliproxyapi-copilot"])
	values, ok := plugin["compaction_models"].([]any)
	if !ok {
		t.Fatalf("config plugin compaction_models has type %T", plugin["compaction_models"])
	}
	models := make([]string, 0, len(values))
	for _, value := range values {
		model, ok := value.(string)
		if !ok || model == "" {
			t.Fatalf("config contains invalid compaction model %v", value)
		}
		models = append(models, model)
	}
	return models
}

func nativeMap(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("config value has type %T, want mapping", value)
	}
	return result
}

func nativeRuntimeConfig(t *testing.T, root string, port int, upstream string, managementSecret string, oauthFragments ...string) ([]byte, string) {
	t.Helper()
	config := nativeTemplateConfig(t)
	server := nativeMap(t, config["server"])
	server["host"] = "127.0.0.1"
	server["port"] = port
	management := nativeMap(t, config["management"])
	management["allow-remote"] = false
	management["disable-control-panel"] = true
	management["secret-key"] = managementSecret
	access := nativeMap(t, config["access"])
	access["api-keys"] = []string{"fixture-client-key"}
	oauth := nativeMap(t, config["oauth"])
	authDir := filepath.Join(root, "auths")
	oauth["auth-dir"] = authDir
	for _, fragment := range oauthFragments {
		if fragment == "" {
			continue
		}
		var parsed struct {
			OAuth map[string]any `yaml:"oauth"`
		}
		if err := yaml.Unmarshal([]byte("oauth:\n"+fragment), &parsed); err != nil {
			t.Fatalf("decode native OAuth override: %v", err)
		}
		for key, value := range parsed.OAuth {
			oauth[key] = value
		}
	}
	plugins := nativeMap(t, config["plugins"])
	plugins["dir"] = filepath.Join(root, "plugins")
	pluginConfigs := nativeMap(t, plugins["configs"])
	plugin := nativeMap(t, pluginConfigs["cliproxyapi-copilot"])
	plugin["allow_insecure_base_urls"] = true
	plugin["github_base_url"] = upstream
	plugin["github_api_url"] = upstream
	plugin["copilot_api_url"] = upstream
	body, err := yaml.Marshal(config)
	if err != nil {
		t.Fatalf("encode native runtime config: %v", err)
	}
	return body, authDir
}

func nativeAuthFixtureJSON(t *testing.T) []byte {
	t.Helper()
	var storage map[string]any
	if err := json.Unmarshal(readNativeSeed(t, "auth.json"), &storage); err != nil {
		t.Fatalf("decode synthetic auth seed: %v", err)
	}
	token := storage["github_access_token"].(string)
	fingerprint := sha256.Sum256([]byte(token))
	keyring := storage["continuity_keyring"].(map[string]any)
	keyring["root_key"] = base64.RawURLEncoding.EncodeToString(bytesOf(0x5a, 32))
	keyring["credential_fingerprint"] = hex.EncodeToString(fingerprint[:])
	data, err := json.Marshal(storage)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func bytesOf(value byte, count int) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return result
}
