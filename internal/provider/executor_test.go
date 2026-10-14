package provider

import (
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

func TestCopilotHeadersUseRecognizedIntegration(t *testing.T) {
	headers := copilotHeaders("test-token", false)

	expected := map[string]string{
		"Copilot-Integration-Id": "vscode-chat",
		"Editor-Plugin-Version":  "copilot-chat/0.35.0",
		"Editor-Version":         "vscode/1.107.0",
		"OpenAI-Intent":          "conversation-edits",
		"User-Agent":             "GitHubCopilotChat/0.35.0",
		"X-GitHub-Api-Version":   "2025-04-01",
	}

	for name, want := range expected {
		if got := headers.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestClaudeTokenSegmentsPreserveOrderAndIgnoreMedia(t *testing.T) {
	root := gjson.Parse(`{
		"system": ["system one", {"type":"text","text":"system two"}, {"type":"image","text":"ignored"}],
		"messages": [{"role":"user","content":[
			"plain text",
			{"type":"thinking","thinking":"reasoning"},
			{"type":"image","source":{"data":"ignored"}},
			{"type":"tool_result","tool_use_id":"call","content":[{"type":"text","text":"result"}]},
			{"type":"document","title":"title","context":"context","source":{"type":"text","data":"document"}},
			{"type":"tool_use","id":"next","name":"lookup","input":{"q": "query"}}
		]}],
		"tools":[{"name":"lookup","description":"description","input_schema":{"type": "object"}}],
		"tool_choice":{"type":"tool","name":"lookup"}
	}`)
	var segments []string
	collectSystemTokenSegments(root.Get("system"), &segments)
	collectMessageTokenSegments(root.Get("messages"), &segments)
	collectToolTokenSegments(root.Get("tools"), &segments)
	collectToolChoiceTokenSegments(root.Get("tool_choice"), &segments)
	want := []string{"system one", "system two", "user", "plain text", "reasoning", "call", "result", "title", "context", "document", "next", "lookup", `{"q":"query"}`, "lookup", "description", `{"type":"object"}`, "tool", "lookup"}
	if !reflect.DeepEqual(segments, want) {
		t.Errorf("segments = %q, want %q", segments, want)
	}
	var absent []string
	collectSystemTokenSegments(gjson.Result{}, &absent)
	collectMessageTokenSegments(gjson.Result{}, &absent)
	collectToolTokenSegments(gjson.Result{}, &absent)
	collectToolChoiceTokenSegments(gjson.Result{}, &absent)
	if absent != nil {
		t.Errorf("missing fields produced segments: %q", absent)
	}
}

func TestCountTokensReturnsClaudeInputTokens(t *testing.T) {
	t.Parallel()

	resp, err := (&Service{}).CountTokens(ExecuteRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			SourceFormat:    "claude",
			OriginalRequest: []byte(`{"model":"gpt-5.6-sol","system":"Be concise.","messages":[{"role":"user","content":"hello world"}]}`),
		},
	})
	if err != nil {
		t.Fatalf("count tokens: %v", err)
	}
	if got := gjson.GetBytes(resp.Payload, "input_tokens").Int(); got <= 0 {
		t.Fatalf("input_tokens = %d; response=%s", got, resp.Payload)
	}
}
