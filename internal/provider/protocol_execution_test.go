package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/compact"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
	"github.com/tidwall/gjson"
)

func TestStreamTerminalRequiresSourceSuccessEvent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		endpoint  string
		frame     string
		wantDone  bool
		wantError bool
	}{
		{name: "Responses completed", endpoint: translate.EndpointResponses, frame: "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[]}}\n\n", wantDone: true},
		{name: "Responses incomplete", endpoint: translate.EndpointResponses, frame: "event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\"}}\n\n", wantError: true},
		{name: "Responses failed", endpoint: translate.EndpointResponses, frame: "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\"}}\n\n", wantError: true},
		{name: "Responses error", endpoint: translate.EndpointResponses, frame: "event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"failed\"}}\n\n", wantError: true},
		{name: "Responses DONE is not terminal", endpoint: translate.EndpointResponses, frame: "data: [DONE]\n\n", wantError: true},
		{name: "Chat completion sentinel", endpoint: translate.EndpointChatCompletions, frame: "data: [DONE]\n\n", wantDone: true},
		{name: "Chat error", endpoint: translate.EndpointChatCompletions, frame: "data: {\"error\":{\"message\":\"failed\"}}\n\n", wantError: true},
		{name: "Messages stop", endpoint: translate.EndpointMessages, frame: "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", wantDone: true},
		{name: "Messages error", endpoint: translate.EndpointMessages, frame: "event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"failed\"}}\n\n", wantError: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var terminal streamTerminal
			done, err := terminal.observe(test.endpoint, []byte(test.frame), "secret-copilot", "ghp_123456789012345678901234")
			if (err != nil) != test.wantError {
				t.Fatalf("observe error = %v, wantError=%v", err, test.wantError)
			}
			if done != test.wantDone || terminal.completed != test.wantDone {
				t.Fatalf("done/completed = %v/%v, want %v", done, terminal.completed, test.wantDone)
			}
			if test.endpoint == translate.EndpointResponses && done && !strings.Contains(string(terminal.response), `"id":"resp_1"`) {
				t.Fatalf("terminal response = %s", terminal.response)
			}
		})
	}
}

func TestStreamTerminalErrorsWithholdProviderPayload(t *testing.T) {
	t.Parallel()
	promptSentinel := "operator-prompt-sentinel-73d2"
	secretSentinel := "unrecognized-secret-sentinel-81af"
	tests := []struct {
		name     string
		endpoint string
		frame    string
	}{
		{name: "Responses", endpoint: translate.EndpointResponses, frame: "event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"operator-prompt-sentinel-73d2 unrecognized-secret-sentinel-81af\"}}\n\n"},
		{name: "Chat Completions", endpoint: translate.EndpointChatCompletions, frame: "data: {\"error\":{\"message\":\"operator-prompt-sentinel-73d2 unrecognized-secret-sentinel-81af\"}}\n\n"},
		{name: "Messages", endpoint: translate.EndpointMessages, frame: "event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"operator-prompt-sentinel-73d2 unrecognized-secret-sentinel-81af\"}}\n\n"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			var terminal streamTerminal
			_, err := terminal.observe(test.endpoint, []byte(test.frame), "copilot-token", "github-token")
			if err == nil || !strings.Contains(err.Error(), "upstream stream error details withheld") {
				t.Fatalf("terminal error = %v, want generic provider error", err)
			}
			if strings.Contains(err.Error(), promptSentinel) || strings.Contains(err.Error(), secretSentinel) {
				t.Fatalf("terminal error exposed provider payload: %v", err)
			}
		})
	}
}

func TestPumpStreamCloseOutputWithholdsProviderPayload(t *testing.T) {
	promptSentinel := "operator-prompt-sentinel-73d2"
	secretSentinel := "unrecognized-secret-sentinel-81af"
	frame := []byte("event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"" + promptSentinel + " " + secretSentinel + "\"}}\n\n")
	host := &errorStreamHost{chunk: transport.StreamChunk{Payload: frame}}
	service := New(host)
	service.pumpStream(context.Background(), "output", translate.EndpointResponses, "openai-response", "model", nil, nil, transport.Stream{ID: "upstream"}, "scope", reasoningCarrierScope{}, "copilot-token", "github-token")
	if host.closedOutputMessage == "" || !strings.Contains(host.closedOutputMessage, "upstream stream error details withheld") {
		t.Fatalf("close output error = %q, want generic provider error", host.closedOutputMessage)
	}
	if strings.Contains(host.closedOutputMessage, promptSentinel) || strings.Contains(host.closedOutputMessage, secretSentinel) {
		t.Fatalf("close output exposed provider payload: %q", host.closedOutputMessage)
	}
}

func TestProtocolSessionIdentityAndPromptCacheScope(t *testing.T) {
	t.Parallel()
	headers := http.Header{"session_id": []string{"codex-session-7"}}
	session, agent := protocolSessionIdentity(nil, headers, nil)
	if session != "codex-session-7" || agent != "main" {
		t.Fatalf("session/agent = %q/%q", session, agent)
	}
	scope := protocolScopeKey("auth-a", continuityTestStorage("github-token-a"), "model-a", "https://api.githubcopilot.com", translate.EndpointResponses, session, agent, 0)
	if got := derivedPromptCacheKey(scope); got == "" || got != derivedPromptCacheKey(protocolScopeKey("auth-a", continuityTestStorage("github-token-a"), "model-a", "https://api.githubcopilot.com", translate.EndpointResponses, session, agent, 0)) {
		t.Fatalf("derived cache key is not deterministic: %q", got)
	}
	for _, changed := range []string{
		protocolScopeKey("auth-b", continuityTestStorage("github-token-a"), "model-a", "https://api.githubcopilot.com", translate.EndpointResponses, session, agent, 0),
		protocolScopeKey("auth-a", continuityTestStorage("github-token-a"), "model-b", "https://api.githubcopilot.com", translate.EndpointResponses, session, agent, 0),
		protocolScopeKey("auth-a", continuityTestStorage("github-token-a"), "model-a", "https://api.githubcopilot.com", translate.EndpointChatCompletions, session, agent, 0),
	} {
		if derivedPromptCacheKey(changed) == derivedPromptCacheKey(scope) {
			t.Fatal("cache key crossed credential, model, or endpoint scope")
		}
	}
	rotatedStorage := continuityTestStorage("github-token-b")
	if rotated := protocolScopeKey("auth-a", rotatedStorage, "model-a", "https://api.githubcopilot.com", translate.EndpointResponses, session, agent, 0); rotated != scope {
		t.Fatalf("credential rotation changed the continuity cache scope: %q != %q", rotated, scope)
	}
	if got := derivedPromptCacheKey(protocolScopeKey("auth-a", continuityTestStorage("github-token-a"), "model-a", "https://api.githubcopilot.com", translate.EndpointResponses, "", agent, 0)); got != "" {
		t.Fatalf("cache key without session identity = %q", got)
	}
}

func TestCompactionCapsuleSurvivesSameCredentialReconfigure(t *testing.T) {
	t.Parallel()
	service := New(nil)
	if err := service.Configure([]byte("compaction_models:\n  - gpt-5.6-sol\n")); err != nil {
		t.Fatalf("configure first generation: %v", err)
	}
	_, firstGeneration := service.configSnapshot()
	firstScope, firstSecret := compactionKeyMaterial("auth-a", "github-credential-a", "gpt-5.6-sol", translate.EndpointResponses, "https://api.example")
	completed, err := compact.Complete([]byte(`{"id":"resp_1","status":"completed","output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Keep the active goal and decision."}]}]}`), compact.KeyMaterial{Scope: firstScope, Secret: firstSecret}, nil)
	if err != nil {
		t.Fatalf("complete summary: %v", err)
	}
	capsule := gjson.GetBytes(completed, "output.1.encrypted_content").String()
	if capsule == "" {
		t.Fatalf("completed result has no capsule: %s", completed)
	}
	input, err := json.Marshal([]any{
		map[string]any{"type": "message", "role": "user", "content": "Continue the task."},
		map[string]string{"type": "compaction", "encrypted_content": capsule},
		map[string]string{"type": "compaction_trigger"},
	})
	if err != nil {
		t.Fatalf("encode replay input: %v", err)
	}
	replayRequest := append([]byte(`{"input":`), input...)
	replayRequest = append(replayRequest, '}')
	if err := service.Configure([]byte("compaction_models:\n  - gpt-5.6-sol\nreasoning_replay: false\n")); err != nil {
		t.Fatalf("configure reloaded generation: %v", err)
	}
	_, secondGeneration := service.configSnapshot()
	if secondGeneration <= firstGeneration {
		t.Fatalf("config generation did not advance: %d to %d", firstGeneration, secondGeneration)
	}
	secondScope, secondSecret := compactionKeyMaterial("auth-a", "github-credential-a", "gpt-5.6-sol", translate.EndpointResponses, "https://api.example")
	if firstScope != secondScope || string(firstSecret) != string(secondSecret) {
		t.Fatal("stable compaction key material changed across configuration reload")
	}
	prepared, requested, err := compact.Prepare(replayRequest, compact.KeyMaterial{Scope: secondScope, Secret: secondSecret}, nil)
	if err != nil || !requested {
		t.Fatalf("replay after reload: requested=%v error=%v", requested, err)
	}
	if bytes.Contains(prepared, []byte("compaction_trigger")) || bytes.Contains(prepared, []byte(capsule)) || !bytes.Contains(prepared, []byte("Keep the active goal and decision.")) {
		t.Fatalf("replay did not expand and remove the opaque capsule: %s", prepared)
	}
	changedScope, changedSecret := compactionKeyMaterial("auth-a", "github-credential-b", "gpt-5.6-sol", translate.EndpointResponses, "https://api.example")
	if _, _, err := compact.Prepare(replayRequest, compact.KeyMaterial{Scope: changedScope, Secret: changedSecret}, nil); err == nil {
		t.Fatal("capsule decrypted under a changed credential")
	}
}

func TestExplicitPromptCacheKeyWinsAndCompactionTriggerHelpers(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"prompt_cache_key":"caller-key","input":[{"type":"message","role":"user","content":"hi"}]}`)
	if got := explicitPromptCacheKey(payload, map[string]any{"prompt_cache_key": "metadata-key"}); got != "caller-key" {
		t.Fatalf("explicit caller key = %q", got)
	}
	if hasCompactionTrigger(payload) {
		t.Fatal("ordinary Responses input contained a compaction trigger")
	}
	compactBody, err := addCompactionTrigger(payload)
	if err != nil {
		t.Fatalf("add trigger: %v", err)
	}
	if !hasCompactionTrigger(compactBody) {
		t.Fatalf("compaction trigger missing from %s", compactBody)
	}
	stringInput, err := addCompactionTrigger([]byte(`{"input":"earlier messages"}`))
	if err != nil || !hasCompactionTrigger(stringInput) {
		t.Fatalf("add trigger to string input: body=%s error=%v", stringInput, err)
	}
}
