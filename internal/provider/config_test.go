package provider

import "testing"

func TestParseConfigNormalizesProtocolOptions(t *testing.T) {
	config, err := ParseConfig([]byte(`
model_endpoint_overrides:
  GPT-Model: responses
  chat-model: chat
compaction_models: [ GPT-Model, " chat-model ", GPT-Model ]
`))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if config.ModelEndpointOverrides["gpt-model"] != "/responses" || config.ModelEndpointOverrides["chat-model"] != "/chat/completions" {
		t.Fatalf("endpoint overrides = %#v", config.ModelEndpointOverrides)
	}
	if len(config.CompactionModels) != 2 || config.CompactionModels[0] != "gpt-model" || config.CompactionModels[1] != "chat-model" {
		t.Fatalf("compaction models = %#v", config.CompactionModels)
	}
	if !config.PromptCacheKey || !config.ReasoningReplay {
		t.Fatalf("prompt cache/reasoning replay defaults = %v/%v", config.PromptCacheKey, config.ReasoningReplay)
	}
}

func TestDefaultConfigEnablesPromptCacheKey(t *testing.T) {
	if !DefaultConfig().PromptCacheKey {
		t.Fatal("default config must enable prompt cache keys")
	}
	if !New(nil).Config().PromptCacheKey {
		t.Fatal("new service must enable prompt cache keys by default")
	}
}

func TestParseConfigCanDisablePromptCacheKey(t *testing.T) {
	config, err := ParseConfig([]byte("support-prompt-cache-key: false\n"))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if config.PromptCacheKey {
		t.Fatal("explicit false must disable prompt cache keys")
	}
}

func TestParseConfigRejectsInvalidEndpointOverride(t *testing.T) {
	if _, err := ParseConfig([]byte("model_endpoint_overrides:\n  model-a: /embeddings\n")); err == nil {
		t.Fatal("expected invalid endpoint override to be rejected")
	}
}

func TestParseConfigRejectsBaseURLUserinfo(t *testing.T) {
	if _, err := ParseConfig([]byte("github_base_url: https://user:password@github.com\n")); err == nil {
		t.Fatal("expected base URL userinfo to be rejected")
	}
}
