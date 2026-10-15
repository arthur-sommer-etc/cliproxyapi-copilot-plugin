package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/tidwall/gjson"
)

func TestExecutionSessionMetadataProvidesStableAgentScopedIdentity(t *testing.T) {
	t.Parallel()
	metadata := map[string]any{"execution_session_id": "run-1", "agent_id": "agent-a"}
	sessionID, agentID := protocolSessionIdentity(nil, nil, metadata)
	if sessionID != "execution:run-1" || agentID != "agent-a" {
		t.Fatalf("execution session/agent = %q/%q", sessionID, agentID)
	}
	cacheKey := func(sessionID, agentID string) string {
		scope := protocolScopeKey("auth-a", continuityTestStorage("github-token-a"), "model-a", "https://api.githubcopilot.com", translate.EndpointResponses, sessionID, agentID, 0)
		return derivedPromptCacheKey(scope)
	}
	firstKey := cacheKey(sessionID, agentID)
	repeatedSession, repeatedAgent := protocolSessionIdentity(nil, nil, metadata)
	if got := cacheKey(repeatedSession, repeatedAgent); got != firstKey {
		t.Fatalf("execution-session cache key changed: %q != %q", got, firstKey)
	}
	otherAgent := map[string]any{"execution_session_id": "run-1", "agent_id": "agent-b"}
	otherSessionID, otherAgentID := protocolSessionIdentity(nil, nil, otherAgent)
	if got := cacheKey(otherSessionID, otherAgentID); got == firstKey {
		t.Fatal("execution-session cache key crossed agent scope")
	}
	conflictingPayload := []byte(`{"session_id":"payload-session"}`)
	conflictingHeaders := http.Header{"Session-Id": []string{"header-session"}}
	preferredSession, preferredAgent := protocolSessionIdentity(conflictingPayload, conflictingHeaders, metadata)
	if preferredSession != sessionID || preferredAgent != agentID {
		t.Fatalf("execution metadata did not take precedence: got %q/%q, want %q/%q", preferredSession, preferredAgent, sessionID, agentID)
	}
	fallbackSession, _ := protocolSessionIdentity(nil, http.Header{"Session-Id": []string{"header-fallback"}}, map[string]any{"execution_session_id": "invalid\nidentity"})
	if fallbackSession != "header-fallback" {
		t.Fatalf("invalid execution session did not fall back to protocol header: %q", fallbackSession)
	}
}

func TestReasoningReplayUsesExactTranslatedToolCallAnchor(t *testing.T) {
	t.Parallel()
	service := New(nil)
	now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	scope := protocolScopeKey("auth-a", continuityTestStorage("github-token-a"), "model-a", "https://api.githubcopilot.com", translate.EndpointResponses, "session-a", "main", 0)
	response := []byte(`{"status":"completed","output":[{"type":"reasoning","encrypted_content":"signature:opaque","summary":[]},{"type":"function_call","id":"resp_fc_1","call_id":"tool call:/東京","name":"run","arguments":"{\"x\":1}"}]}`)
	claudeResponse, err := translate.ResponseFromEndpoint(context.Background(), translate.EndpointResponses, "claude", "model-a", nil, nil, response)
	if err != nil {
		t.Fatalf("translate Claude response: %v", err)
	}
	var translatedResponse struct {
		Content []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		} `json:"content"`
	}
	if err := json.Unmarshal(claudeResponse, &translatedResponse); err != nil {
		t.Fatalf("decode Claude response: %v", err)
	}
	if len(translatedResponse.Content) != 2 || !strings.HasPrefix(translatedResponse.Content[1].ID, "cpa_tool_v1_") {
		t.Fatalf("Claude response did not carry distinct opaque IDs: %s", claudeResponse)
	}
	original := []byte(fmt.Sprintf(`{"model":"model-a","messages":[{"role":"assistant","content":[{"type":"tool_use","id":%q,"name":"run","input":{"x":1}}]}]}`, translatedResponse.Content[1].ID))
	translated, err := translate.RequestForEndpointFrom("claude", "model-a", original, false, translate.EndpointResponses)
	if err != nil {
		t.Fatalf("translate request: %v", err)
	}
	var request struct {
		Input []struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
		} `json:"input"`
	}
	if err := json.Unmarshal(translated, &request); err != nil {
		t.Fatalf("decode translated request: %v", err)
	}
	if len(request.Input) != 1 || request.Input[0].CallID != "tool call:/東京" {
		t.Fatalf("translated call ID = %#v", request.Input)
	}
	service.recordReasoningReplay(scope, response)
	restored := service.restoreReasoningReplay(scope, original, translated)
	var result struct {
		Input []map[string]json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(restored, &result); err != nil {
		t.Fatalf("decode restored request: %v", err)
	}
	if len(result.Input) != 2 || string(result.Input[0]["encrypted_content"]) != `"signature:opaque"` {
		t.Fatalf("signature was not restored before the exact tool call: %s", restored)
	}
	var call struct {
		CallID string `json:"call_id"`
	}
	if err := json.Unmarshal(result.Input[1]["call_id"], &call.CallID); err != nil || call.CallID != request.Input[0].CallID {
		t.Fatalf("translated call ID changed during replay: %s", restored)
	}
	if unchanged := service.restoreReasoningReplay("other-scope", original, translated); string(unchanged) != string(translated) {
		t.Fatalf("replay crossed scope: %s", unchanged)
	}
	withThinking := []byte(fmt.Sprintf(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"present"},{"type":"tool_use","id":%q,"name":"run","input":{}}]}]}`, translatedResponse.Content[1].ID))
	if string(service.restoreReasoningReplay(scope, withThinking, translated)) != string(translated) {
		t.Fatal("replay replaced thinking that the client already sent")
	}
}

func TestReasoningReplayRequiresSuccessfulUniqueCallsAndExpires(t *testing.T) {
	t.Parallel()
	service := New(nil)
	now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	scope := protocolScopeKey("auth-a", continuityTestStorage("github-token-a"), "model-a", "https://api.githubcopilot.com", translate.EndpointResponses, "session-a", "main", 0)
	service.recordReasoningReplay(scope, []byte(`{"status":"incomplete","output":[{"type":"reasoning","encrypted_content":"sig"},{"type":"function_call","call_id":"call-1"}]}`))
	if got := len(service.replayEntries); got != 0 {
		t.Fatalf("incomplete response created %d replay entries", got)
	}
	service.recordReasoningReplay(scope, []byte(`{"status":"completed","output":[{"type":"reasoning","encrypted_content":"sig"},{"type":"function_call","call_id":"same"},{"type":"function_call","call_id":"same"}]}`))
	if got := len(service.replayEntries); got != 0 {
		t.Fatalf("duplicate calls created %d replay entries", got)
	}
	service.recordReasoningReplay(scope, []byte(`{"status":"completed","output":[{"type":"reasoning","encrypted_content":"sig"},{"type":"function_call","call_id":"call-1"}]}`))
	if got := len(service.replayEntries); got != 1 {
		t.Fatalf("completed response created %d replay entries, want 1", got)
	}
	now = now.Add(reasoningReplayTTL)
	if got := len(service.reasoningReplayForScope(scope)); got != 0 {
		t.Fatalf("expired replay entries = %d, want 0", got)
	}
}

func TestNativeResponsesReplayRestoresOnlyMissingExactAnchoredIDs(t *testing.T) {
	t.Parallel()
	service := New(nil)
	scope := protocolScopeKey("auth-a", continuityTestStorage("github-token-a"), "model-a", "https://api.githubcopilot.com", translate.EndpointResponses, "session-a", "main", 0)
	response := []byte(`{"status":"completed","output":[{"type":"reasoning","id":"rs_upstream","encrypted_content":"opaque-signature","summary":[]},{"type":"function_call","id":"fc_upstream","call_id":"call-1","name":"run","arguments":"{\"x\":1}"}]}`)
	service.recordReasoningReplay(scope, response)
	secondTurn := []byte(`{"model":"model-a","input":[{"type":"reasoning","encrypted_content":"opaque-signature","summary":[]},{"type":"function_call","call_id":"call-1","name":"run","arguments":"{\"x\":1}"}]}`)
	translated, err := translate.RequestForEndpointFrom("openai-response", "model-a", secondTurn, false, translate.EndpointResponses)
	if err != nil {
		t.Fatalf("translate second-turn request: %v", err)
	}
	restored := service.restoreNativeResponsesReplay(scope, translated)
	var result struct {
		Input []struct {
			ID     string `json:"id"`
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Name   string `json:"name"`
			Args   string `json:"arguments"`
		} `json:"input"`
	}
	if err := json.Unmarshal(restored, &result); err != nil {
		t.Fatalf("decode restored native request: %v", err)
	}
	if len(result.Input) != 2 || result.Input[0].ID != "rs_upstream" || result.Input[1].ID != "fc_upstream" || result.Input[1].CallID != "call-1" {
		t.Fatalf("missing upstream IDs were not restored by exact anchors: %s", restored)
	}
	callerID := []byte(`{"model":"model-a","input":[{"type":"function_call","id":"caller-id","call_id":"call-1","name":"run","arguments":"{\"x\":1}"}]}`)
	callerTranslated, err := translate.RequestForEndpointFrom("openai-response", "model-a", callerID, false, translate.EndpointResponses)
	if err != nil {
		t.Fatalf("translate request with caller ID: %v", err)
	}
	callerRestored := service.restoreNativeResponsesReplay(scope, callerTranslated)
	var callerResult struct {
		Input []struct {
			ID string `json:"id"`
		} `json:"input"`
	}
	if err := json.Unmarshal(callerRestored, &callerResult); err != nil || len(callerResult.Input) != 1 || callerResult.Input[0].ID != "caller-id" {
		t.Fatalf("replay overwrote caller item ID: body=%s err=%v", callerRestored, err)
	}
	wrongArguments := []byte(`{"model":"model-a","input":[{"type":"function_call","call_id":"call-1","name":"run","arguments":"{\"x\":2}"}]}`)
	wrongTranslated, err := translate.RequestForEndpointFrom("openai-response", "model-a", wrongArguments, false, translate.EndpointResponses)
	if err != nil {
		t.Fatalf("translate request with different arguments: %v", err)
	}
	if got := service.restoreNativeResponsesReplay(scope, wrongTranslated); string(got) != string(wrongTranslated) {
		t.Fatalf("replay restored an ID for a non-matching call: %s", got)
	}
	otherScope := protocolScopeKey("auth-a", continuityTestStorage("github-token-a"), "model-a", "https://other-api.example", translate.EndpointResponses, "session-a", "main", 0)
	if got := service.restoreNativeResponsesReplay(otherScope, translated); string(got) != string(translated) {
		t.Fatalf("native replay crossed API-origin scope: %s", got)
	}
}

func TestReasoningReplayRejectsResultsFromPriorConfiguration(t *testing.T) {
	t.Parallel()
	service := New(nil)
	oldScope := protocolScopeKey("auth-a", continuityTestStorage("github-token-a"), "model-a", "https://api.githubcopilot.com", translate.EndpointResponses, "session-a", "main", 0)
	if err := service.Configure([]byte("enabled: true\n")); err != nil {
		t.Fatalf("configure: %v", err)
	}
	response := []byte(`{"status":"completed","output":[{"type":"reasoning","id":"rs_old","encrypted_content":"old-signature","summary":[]}]}`)
	service.recordReasoningReplay(oldScope, response)
	if len(service.replayEntries) != 0 {
		t.Fatalf("old in-flight response repopulated replay cache: %d entries", len(service.replayEntries))
	}
}

func TestReasoningReplayCacheIsBounded(t *testing.T) {
	t.Parallel()
	service := New(nil)
	now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	scope := protocolScopeKey("auth-a", continuityTestStorage("github-token-a"), "model-a", "https://api.githubcopilot.com", translate.EndpointResponses, "session-a", "main", 0)
	for index := 0; index < maxReasoningReplayEntries+8; index++ {
		response := fmt.Sprintf(`{"status":"completed","output":[{"type":"reasoning","encrypted_content":"sig-%d"},{"type":"function_call","call_id":"call-%d"}]}`, index, index)
		service.recordReasoningReplay(scope, []byte(response))
		now = now.Add(time.Nanosecond)
	}
	if len(service.replayEntries) != maxReasoningReplayEntries {
		t.Fatalf("cache entry count = %d, want %d", len(service.replayEntries), maxReasoningReplayEntries)
	}
	if service.replayBytes > maxReasoningReplayCache {
		t.Fatalf("cache byte count = %d, exceeds %d", service.replayBytes, maxReasoningReplayCache)
	}
	for _, entry := range service.replayEntries {
		if strings.Contains(entry.ScopeKey, "github-token-a") {
			t.Fatal("cache key exposed credential text")
		}
	}
}

func TestReasoningReplaySurvivesOAuthCredentialRotation(t *testing.T) {
	t.Parallel()
	service := New(nil)
	original := continuityTestStorage("github-access-before-refresh")
	oldScope := protocolScopeKey("auth-a", original, "model-a", "https://api.example", translate.EndpointResponses, "session-a", "main", 0)
	service.recordReasoningReplay(oldScope, []byte(`{"status":"completed","output":[{"type":"reasoning","id":"rs_1","encrypted_content":"opaque","summary":[]},{"type":"function_call","id":"fc_1","call_id":"call-1","name":"run","arguments":"{}"}]}`))
	rotated := original
	rotated.GitHubAccessToken = "github-access-after-refresh"
	rotatedRing := *original.ContinuityKeyring
	rotatedRing.CredentialFingerprint = tokenFingerprint(rotated.GitHubAccessToken)
	rotated.ContinuityKeyring = &rotatedRing
	newScope := protocolScopeKey("auth-a", rotated, "model-a", "https://api.example", translate.EndpointResponses, "session-a", "main", 0)
	if newScope != oldScope {
		t.Fatal("same-account OAuth refresh changed the replay scope")
	}
	translated := []byte(`{"input":[{"type":"reasoning","encrypted_content":"opaque","summary":[]},{"type":"function_call","call_id":"call-1","name":"run","arguments":"{}"}]}`)
	restored := service.restoreNativeResponsesReplay(newScope, translated)
	if gjson.GetBytes(restored, "input.0.id").String() != "rs_1" || gjson.GetBytes(restored, "input.1.id").String() != "fc_1" {
		t.Fatalf("replay cache did not survive authenticated credential rotation: %s", restored)
	}
	newLogin := continuityTestStorage("github-access-after-refresh")
	newLoginRing, err := newContinuityKeyring(newLogin.GitHubUserID, newLogin.GitHubAccessToken)
	if err != nil {
		t.Fatalf("make replacement login keyring: %v", err)
	}
	newLogin.ContinuityKeyring = newLoginRing
	newLoginScope := protocolScopeKey("auth-a", newLogin, "model-a", "https://api.example", translate.EndpointResponses, "session-a", "main", 0)
	if newLoginScope == oldScope {
		t.Fatal("replacement login reused the old replay scope")
	}
}
