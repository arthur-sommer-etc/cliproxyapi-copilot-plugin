package provider

import (
	"testing"

	"github.com/tidwall/gjson"
)

func adaptiveClaudeModel() upstreamModel {
	return upstreamModel{
		ID:     "claude-sonnet-5.5",
		Vendor: "Anthropic",
		Capabilities: modelCapabilities{
			Family: "claude-sonnet-5.5",
			Supports: modelSupports{
				AdaptiveThinking: true,
				ReasoningEffort:  []string{"low", "medium", "high", "xhigh", "max"},
			},
		},
	}
}

func TestNormalizeClaudeMessagesAdaptiveResponsesThinking(t *testing.T) {
	t.Parallel()
	model := adaptiveClaudeModel()
	for _, effort := range []string{"high", "medium"} {
		effort := effort
		t.Run(effort, func(t *testing.T) {
			t.Parallel()
			original := []byte(`{"reasoning":{"effort":"` + effort + `"}}`)
			translated := []byte(`{"thinking":{"type":"enabled","budget_tokens":24576},"max_tokens":4096,"messages":[]}`)
			got, err := normalizeClaudeMessagesRequest(model, "openai-response", original, translated)
			if err != nil {
				t.Fatalf("normalize adaptive thinking: %v", err)
			}
			if gjson.GetBytes(got, "thinking.type").String() != "adaptive" || gjson.GetBytes(got, "thinking.budget_tokens").Exists() {
				t.Fatalf("thinking config = %s, want adaptive without a budget", gjson.GetBytes(got, "thinking"))
			}
			if gotEffort := gjson.GetBytes(got, "output_config.effort").String(); gotEffort != effort {
				t.Fatalf("output_config.effort = %q, want %q; request=%s", gotEffort, effort, got)
			}
		})
	}
}

func TestAdaptiveClaudeEffortPrefersAdvertisedValue(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		requested string
		supported []string
		want      string
	}{
		{requested: "xhigh", supported: []string{"high", "xhigh", "max"}, want: "xhigh"},
		{requested: "xhigh", supported: []string{"high", "max"}, want: "max"},
		{requested: "max", supported: []string{"high"}, want: "high"},
		{requested: "minimal", supported: []string{"low"}, want: "low"},
	} {
		test := test
		t.Run(test.requested+"_"+test.want, func(t *testing.T) {
			t.Parallel()
			got, ok := adaptiveClaudeEffort(test.requested, test.supported)
			if !ok || got != test.want {
				t.Fatalf("adaptiveClaudeEffort(%q, %#v) = %q, %v; want %q, true", test.requested, test.supported, got, ok, test.want)
			}
		})
	}
}

func TestNormalizeClaudeMessagesPerMessageOutputConfig(t *testing.T) {
	t.Parallel()
	model := adaptiveClaudeModel()
	original := []byte(`{"model":"claude-sonnet-5.5"}`)
	translated := []byte(`{"output_config":{"effort":"high","format":{"type":"json_schema"}},"messages":[{"role":"user","content":[{"type":"text","text":"keep user block"}],"output_config":{"effort":"low"}},{"role":"system","content":[],"output_config":{"effort":"medium"}},{"role":"assistant","content":[{"type":"thinking","thinking":"keep reasoning","signature":"sig"}],"output_config":{"effort":"medium"}}]}`)
	got, err := normalizeClaudeMessagesRequest(model, "claude", original, translated)
	if err != nil {
		t.Fatalf("normalize native Claude request: %v", err)
	}
	if gotEffort := gjson.GetBytes(got, "output_config.effort").String(); gotEffort != "high" {
		t.Fatalf("root effort = %q, want existing root effort high", gotEffort)
	}
	if len(gjson.GetBytes(got, "messages").Array()) != 2 || gjson.GetBytes(got, "messages.0.output_config").Exists() || gjson.GetBytes(got, "messages.1.output_config").Exists() {
		t.Fatalf("per-message output_config remains: %s", got)
	}
	if got := gjson.GetBytes(got, "messages.0.content").Raw; got != `[{"type":"text","text":"keep user block"}]` {
		t.Fatalf("user content changed: %s", got)
	}
	if got := gjson.GetBytes(got, "messages.1.content").Raw; got != `[{"type":"thinking","thinking":"keep reasoning","signature":"sig"}]` {
		t.Fatalf("assistant content changed: %s", got)
	}
}

func TestNormalizeClaudeMessagesFailsClosedOnMeaningfulSystemEffortMarker(t *testing.T) {
	t.Parallel()
	model := adaptiveClaudeModel()
	translated := []byte(`{"messages":[{"role":"system","content":[{"type":"text","text":"keep this policy"}],"output_config":{"effort":"medium"}}]}`)
	if _, err := normalizeClaudeMessagesRequest(model, "claude", []byte(`{}`), translated); err == nil {
		t.Fatal("normalizer silently removed meaningful system content")
	}
}

func TestNormalizeClaudeMessagesRejectsUnknownEffortMarkerFields(t *testing.T) {
	t.Parallel()
	model := adaptiveClaudeModel()
	translated := []byte(`{"messages":[{"role":"system","content":[],"output_config":{"effort":"medium"},"cache_control":"preserve"}]}`)
	if _, err := normalizeClaudeMessagesRequest(model, "claude", []byte(`{}`), translated); err == nil {
		t.Fatal("normalizer silently removed an effort marker with an unknown field")
	}
}

func TestNormalizeClaudeMessagesUsesLastMessageEffort(t *testing.T) {
	t.Parallel()
	model := adaptiveClaudeModel()
	original := []byte(`{"model":"claude-sonnet-5.5"}`)
	translated := []byte(`{"messages":[{"role":"user","content":"first","output_config":{"effort":"low"}},{"role":"assistant","content":"second","output_config":{"effort":"medium"}}]}`)
	got, err := normalizeClaudeMessagesRequest(model, "claude", original, translated)
	if err != nil {
		t.Fatalf("normalize native Claude request: %v", err)
	}
	if gotEffort := gjson.GetBytes(got, "output_config.effort").String(); gotEffort != "medium" {
		t.Fatalf("root effort = %q, want last message effort medium", gotEffort)
	}
}

func TestNormalizeClaudeMessagesFailsClosedOnUnknownMessageOptions(t *testing.T) {
	t.Parallel()
	model := adaptiveClaudeModel()
	for _, config := range []string{
		`{"effort":"high","temperature":0.2}`,
		`{"effort":4}`,
	} {
		config := config
		t.Run(config, func(t *testing.T) {
			t.Parallel()
			translated := []byte(`{"messages":[{"role":"user","content":"preserve","output_config":` + config + `}]}`)
			if _, err := normalizeClaudeMessagesRequest(model, "claude", []byte(`{}`), translated); err == nil {
				t.Fatal("normalizer accepted a per-message option it cannot represent")
			}
		})
	}
}

func TestNormalizeClaudeMessagesThinkingDisplay(t *testing.T) {
	t.Parallel()
	model := adaptiveClaudeModel()
	for _, test := range []struct {
		name      string
		display   string
		want      string
		wantError bool
	}{
		{name: "updates maps to summarized", display: "updates", want: "summarized"},
		{name: "summarized remains supported", display: "summarized", want: "summarized"},
		{name: "omitted remains supported", display: "omitted", want: "omitted"},
		{name: "unknown display is rejected", display: "live", wantError: true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			translated := []byte(`{"thinking":{"type":"adaptive","display":"` + test.display + `"},"messages":[]}`)
			got, err := normalizeClaudeMessagesRequest(model, "claude", []byte(`{}`), translated)
			if test.wantError {
				if err == nil {
					t.Fatal("expected unsupported display error")
				}
				return
			}
			if err != nil {
				t.Fatalf("normalize thinking display: %v", err)
			}
			if display := gjson.GetBytes(got, "thinking.display").String(); display != test.want {
				t.Fatalf("thinking.display = %q, want %q", display, test.want)
			}
		})
	}
}

func TestNormalizeClaudeMessagesNoOpContextManagement(t *testing.T) {
	t.Parallel()
	model := adaptiveClaudeModel()
	translated := []byte(`{"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"keep this block","signature":"sig"}]}]}`)
	got, err := normalizeClaudeMessagesRequest(model, "claude", []byte(`{}`), translated)
	if err != nil {
		t.Fatalf("normalize no-op context management: %v", err)
	}
	if gjson.GetBytes(got, "context_management").Exists() {
		t.Fatalf("context_management remains: %s", got)
	}
	if content := gjson.GetBytes(got, "messages.0.content").Raw; content != `[{"type":"thinking","thinking":"keep this block","signature":"sig"}]` {
		t.Fatalf("thinking content changed: %s", content)
	}
}

func TestNormalizeClaudeMessagesRejectsNonNoOpContextManagement(t *testing.T) {
	t.Parallel()
	model := adaptiveClaudeModel()
	for _, field := range []string{
		`{"edits":[{"type":"clear_thinking_20251015","keep":"last_turn"}]}`,
		`{"edits":[{"type":"clear_tool_uses_20250919","keep":"all"}]}`,
		`{"edits":[{"type":"clear_thinking_20251015","keep":"all"},{"type":"clear_thinking_20251015","keep":"all"}]}`,
	} {
		field := field
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			body := []byte(`{"context_management":` + field + `,"messages":[]}`)
			if _, err := normalizeClaudeMessagesRequest(model, "claude", []byte(`{}`), body); err == nil {
				t.Fatal("normalizer accepted unsupported context management")
			}
		})
	}
}

func TestNormalizeClaudeMessagesRejectsNonemptySafeguards(t *testing.T) {
	t.Parallel()
	model := adaptiveClaudeModel()
	body := []byte(`{"safeguards":{"dangerous_tool_use":{"action":"block"}},"messages":[]}`)
	if _, err := normalizeClaudeMessagesRequest(model, "claude", []byte(`{}`), body); err == nil {
		t.Fatal("normalizer silently removed nonempty safeguards")
	}
}

func TestNormalizeClaudeSourceRequestSanitizesOnlySafeContextManagement(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"retain","signature":"sig"}]}]}`)
	original := string(payload)
	got, err := normalizeClaudeSourceRequest("claude", payload)
	if err != nil {
		t.Fatalf("normalize Claude source request: %v", err)
	}
	if gjson.GetBytes(got, "context_management").Exists() || !gjson.GetBytes(got, "messages.0.content.0.signature").Exists() {
		t.Fatalf("source request was not safely normalized: %s", got)
	}
	if string(payload) != original {
		t.Fatalf("normalization changed the original payload: %s", payload)
	}
}

func TestNormalizeClaudeSourceRequestRejectsSafeguards(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"safeguards":{"dangerous_tool_use":{"action":"block"}},"messages":[]}`)
	if _, err := normalizeClaudeSourceRequest("claude", payload); err == nil {
		t.Fatal("normalizer silently discarded Claude safeguards")
	}
	if _, err := normalizeClaudeSourceRequest("openai-response", payload); err != nil {
		t.Fatalf("non-Claude source was modified: %v", err)
	}
}

func TestNormalizeClaudeMessagesLeavesLegacyAndNativeRequestsAlone(t *testing.T) {
	t.Parallel()
	legacyModel := adaptiveClaudeModel()
	legacyModel.Capabilities.Supports.AdaptiveThinking = false
	original := []byte(`{"reasoning":{"effort":"high"}}`)
	translated := []byte(`{"thinking":{"type":"enabled","budget_tokens":24576},"messages":[]}`)
	got, err := normalizeClaudeMessagesRequest(legacyModel, "openai-response", original, translated)
	if err != nil {
		t.Fatalf("normalize legacy Claude model: %v", err)
	}
	if string(got) != string(translated) {
		t.Fatalf("legacy Claude request changed: %s", got)
	}
	nonClaudeModel := adaptiveClaudeModel()
	nonClaudeModel.ID = "model-unknown"
	nonClaudeModel.Vendor = "Other"
	nonClaudeModel.Capabilities.Family = "unknown"
	got, err = normalizeClaudeMessagesRequest(nonClaudeModel, "openai-response", original, translated)
	if err != nil {
		t.Fatalf("normalize non-Claude model: %v", err)
	}
	if string(got) != string(translated) {
		t.Fatalf("non-Claude request changed: %s", got)
	}
}
