package translate

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestResponsesClaudeRequestPreservesOpaqueThinkingAndToolHistory(t *testing.T) {
	const itemID = "fc-initial/+opaque-1"
	const callID = "call-initial/+opaque-1"
	const signature = "initial-signature/+ exact\n\t "
	const redacted = "initial-redacted/+ exact\n\t "
	request := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"matrix user history exact"}]},{"type":"reasoning","summary":[{"type":"summary_text","text":"initial private reasoning"}],"encrypted_content":"initial-signature/+ exact\n\t "},{"type":"reasoning","summary":[],"encrypted_content":"claude-redacted-thinking:initial-redacted/+ exact\n\t "},{"type":"function_call","id":"fc-initial/+opaque-1","call_id":"call-initial/+opaque-1","name":"inspect","arguments":"{\"query\":\"initial-value\"}"},{"type":"custom_tool_call","id":"call-shared-1","call_id":"call-shared-1","name":"write_file","input":"payload"},{"type":"function_call_output","call_id":"call-initial/+opaque-1","output":"inspection complete"},{"type":"custom_tool_call_output","call_id":"call-shared-1","output":"file written"},{"type":"message","role":"user","content":[{"type":"input_text","text":"continue with the result"}]}],"reasoning":{"effort":"high"}}`)
	translated, err := RequestForEndpointFrom("openai-response", "gpt-test", request, false, EndpointMessages)
	if err != nil {
		t.Fatalf("translate Responses request to Claude: %v", err)
	}
	var thinkingSignature, thinkingText, redactedData string
	toolIDs := make(map[string]string)
	toolResults := make(map[string]string)
	for _, message := range gjson.GetBytes(translated, "messages").Array() {
		for _, block := range message.Get("content").Array() {
			switch block.Get("type").String() {
			case "thinking":
				thinkingSignature = block.Get("signature").String()
				thinkingText = block.Get("thinking").String()
			case "redacted_thinking":
				redactedData = block.Get("data").String()
			case "tool_use":
				switch block.Get("name").String() {
				case "inspect":
					if block.Get("input.query").String() != "initial-value" {
						t.Fatalf("function tool input changed: %s", block)
					}
					toolIDs["inspect"] = block.Get("id").String()
				case "write_file":
					if block.Get("input.input").String() != "payload" {
						t.Fatalf("custom tool input changed: %s", block)
					}
					toolIDs["write_file"] = block.Get("id").String()
				}
			case "tool_result":
				toolResults[block.Get("tool_use_id").String()] = block.Get("tool_use_id").String()
			}
		}
	}
	if thinkingSignature != signature || thinkingText != "initial private reasoning" {
		t.Fatalf("thinking block changed: signature=%q text=%q; request=%s", thinkingSignature, thinkingText, translated)
	}
	if redactedData != redacted {
		t.Fatalf("redacted thinking data = %q, want exact value %q; request=%s", redactedData, redacted, translated)
	}
	if toolIDs["inspect"] == "" || toolIDs["write_file"] == "" {
		t.Fatalf("tool call was lost or changed: %s", translated)
	}
	decodedItemID, decodedCallID, ok := DecodeClaudeToolIDs(toolIDs["inspect"])
	if !ok || decodedItemID != itemID || decodedCallID != callID {
		t.Fatalf("tool identity = (%q, %q, %t), want (%q, %q, true)", decodedItemID, decodedCallID, ok, itemID, callID)
	}
	if toolIDs["write_file"] != "call-shared-1" {
		t.Fatalf("identical safe tool ID changed to %q", toolIDs["write_file"])
	}
	foundContinuation := false
	for _, message := range gjson.GetBytes(translated, "messages").Array() {
		for _, block := range message.Get("content").Array() {
			foundContinuation = foundContinuation || block.Get("type").String() == "text" && block.Get("text").String() == "continue with the result"
		}
	}
	if toolResults[toolIDs["inspect"]] == "" || toolResults[toolIDs["write_file"]] == "" || !foundContinuation {
		t.Fatalf("tool result or following user message was lost: %s", translated)
	}
}

func TestResponsesClaudeRequestPreservesWhitespacePrefixedOpaqueSignature(t *testing.T) {
	const signature = " claude-redacted-thinking:opaque-signature "
	request := []byte(`{"input":[{"type":"reasoning","summary":[{"type":"summary_text","text":"keep as signed thinking"}],"encrypted_content":" claude-redacted-thinking:opaque-signature "},{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}]}`)
	translated, err := RequestForEndpointFrom("openai-response", "gpt-test", request, false, EndpointMessages)
	if err != nil {
		t.Fatalf("translate Responses reasoning to Claude: %v", err)
	}
	if got := gjson.GetBytes(translated, "messages.0.content.0.type").String(); got != "thinking" {
		t.Fatalf("whitespace-prefixed opaque value became %q, want thinking; request=%s", got, translated)
	}
	if got := gjson.GetBytes(translated, "messages.0.content.0.signature").String(); got != signature {
		t.Fatalf("opaque signature = %q, want exact value %q; request=%s", got, signature, translated)
	}
}
