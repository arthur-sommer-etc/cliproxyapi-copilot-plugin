package integration

import (
	"net/http"
	"os"
	"strings"
	"testing"
)

// This opt-in probe retains both client bodies and the host's upstream debug
// capture before validating a real token-limit terminal event.
func TestLiveCopilotIncompleteResponses(t *testing.T) {
	if os.Getenv("CPA_LIVE_COPILOT_INCOMPLETE") != "1" {
		t.Skip("set CPA_LIVE_COPILOT_INCOMPLETE=1 to run the real Copilot token-limit probe")
	}
	binary := os.Getenv("CPA_BINARY")
	if binary == "" {
		t.Fatal("CPA_BINARY is required for the live native host probe")
	}
	profile := liveProfileFromEnvironment(t)
	base, stop := startLiveNativeHost(t, binary, profile.StorageJSON, profile.AuthMode, liveEndpointOverrides(profile.Catalog))
	defer stop()
	payload := map[string]any{
		"model": "gpt-6-luna", "stream": true, "max_output_tokens": 16,
		"reasoning": map[string]any{"effort": "low"},
		"input":     "Write at least 100 words explaining how a rainbow forms. Do not shorten the answer.",
	}
	body, status, contentType, err := liveProxyCall(base+"/v1/responses", payload)
	captureLiveMatrixBodies(t, payload, body, status, contentType)
	if err != nil {
		t.Fatal("token-limit probe failed before receiving a response")
	}
	if status != http.StatusOK || !strings.Contains(contentType, "text/event-stream") {
		t.Fatalf("token-limit probe returned HTTP %d, error_class=%q", status, safeLiveErrorClass(body, status))
	}
	events, done := parseSSEDataEventsWithDone(t, body)
	if done || len(events) == 0 {
		t.Fatal("token-limit probe did not retain a native Responses terminal")
	}
	for _, event := range events {
		if event["type"] == "error" || event["error"] != nil || event["type"] == "response.completed" {
			t.Fatalf("token-limit probe emitted unexpected event type=%v", event["type"])
		}
	}
	terminal := events[len(events)-1]
	response, _ := terminal["response"].(map[string]any)
	details, _ := response["incomplete_details"].(map[string]any)
	usage, _ := response["usage"].(map[string]any)
	if terminal["type"] != "response.incomplete" || response["status"] != "incomplete" || response["model"] != "gpt-6-luna" || details["reason"] != "max_output_tokens" || usage["output_tokens"] == nil {
		t.Fatal("token-limit probe lost the original model, incomplete reason, or usage")
	}
	t.Logf("validated native response.incomplete; events=%d output_tokens=%v reason=max_output_tokens", len(events), usage["output_tokens"])
}
