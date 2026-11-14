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

func TestResponsesClaudeRequestRestoresCarrierCallIDOnReplay(t *testing.T) {
	const itemID = "fc_fixture-messages-tool-1/+"
	const claudeID = "fixture-messages-tool-1/+"
	const carrier = "cpa_tool_v1_eyJpIjoiZmNfZml4dHVyZS1tZXNzYWdlcy10b29sLTEvKyIsImMiOiJmaXh0dXJlLW1lc3NhZ2VzLXRvb2wtMS8rIn0"
	const result = "synthetic canonical tool result after turn 1"
	request := []byte(`{"input":[{"type":"function_call","id":"` + itemID + `","call_id":"` + carrier + `","name":"inspect","arguments":"{\"query\":\"provider-value\"}"},{"type":"function_call_output","call_id":"` + carrier + `","output":"` + result + `"},{"type":"message","role":"user","content":[{"type":"input_text","text":"Continue the synthetic conversation."}]}]}`)
	translated, err := RequestForEndpointFrom("openai-response", "claude-sonnet-5.5", request, false, EndpointMessages)
	if err != nil {
		t.Fatalf("translate Responses replay to Claude: %v", err)
	}
	var toolUseID, toolResultID, resultText string
	for _, message := range gjson.GetBytes(translated, "messages").Array() {
		for _, block := range message.Get("content").Array() {
			switch block.Get("type").String() {
			case "tool_use":
				toolUseID = block.Get("id").String()
				if block.Get("input.query").String() != "provider-value" {
					t.Fatalf("tool input changed: %s", block)
				}
			case "tool_result":
				toolResultID = block.Get("tool_use_id").String()
				resultText = block.Get("content").String()
			}
		}
	}
	if toolUseID != claudeID || toolResultID != claudeID || resultText != result {
		t.Fatalf("Claude replay IDs/result = (%q, %q, %q), want (%q, %q, %q); request=%s", toolUseID, toolResultID, resultText, claudeID, claudeID, result, translated)
	}
}

func TestResponsesClaudeRequestRestoresCanonicalSDKFunctionCallID(t *testing.T) {
	const callID = "fixture-sdk-tool-1/+"
	request := []byte(`{"input":[{"type":"function_call","id":"fc_` + callID + `","call_id":"` + callID + `","name":"inspect","arguments":"{}"},{"type":"function_call_output","call_id":"` + callID + `","output":"inspection complete"}]}`)
	translated, err := RequestForEndpointFrom("openai-response", "claude-sonnet-5.5", request, false, EndpointMessages)
	if err != nil {
		t.Fatalf("translate canonical Responses tool replay to Claude: %v", err)
	}
	var toolUseID, toolResultID string
	for _, message := range gjson.GetBytes(translated, "messages").Array() {
		for _, block := range message.Get("content").Array() {
			switch block.Get("type").String() {
			case "tool_use":
				toolUseID = block.Get("id").String()
			case "tool_result":
				toolResultID = block.Get("tool_use_id").String()
			}
		}
	}
	if toolUseID != callID || toolResultID != callID {
		t.Fatalf("canonical Claude tool IDs = (%q, %q), want (%q, %q); request=%s", toolUseID, toolResultID, callID, callID, translated)
	}
}

func TestResponsesClaudeRequestRestoresCanonicalSDKCustomToolCallID(t *testing.T) {
	const callID = "fixture-sdk-custom-1/+"
	request := []byte(`{"input":[{"type":"custom_tool_call","id":"ctc_` + callID + `","call_id":"` + callID + `","name":"write_file","input":"payload"},{"type":"custom_tool_call_output","call_id":"` + callID + `","output":"file written"}]}`)
	translated, err := RequestForEndpointFrom("openai-response", "claude-sonnet-5.5", request, false, EndpointMessages)
	if err != nil {
		t.Fatalf("translate canonical Responses custom tool replay to Claude: %v", err)
	}
	var toolUseID, toolResultID string
	for _, message := range gjson.GetBytes(translated, "messages").Array() {
		for _, block := range message.Get("content").Array() {
			switch block.Get("type").String() {
			case "tool_use":
				toolUseID = block.Get("id").String()
				if block.Get("input.input").String() != "payload" {
					t.Fatalf("custom tool input changed: %s", block)
				}
			case "tool_result":
				toolResultID = block.Get("tool_use_id").String()
			}
		}
	}
	if toolUseID != callID || toolResultID != callID {
		t.Fatalf("canonical Claude custom tool IDs = (%q, %q), want (%q, %q); request=%s", toolUseID, toolResultID, callID, callID, translated)
	}
}

func TestResponsesClaudeRequestPreservesDistinctCustomToolIDs(t *testing.T) {
	const itemID = "ctc_distinct-custom-item"
	const callID = "native-custom-call"
	request := []byte(`{"input":[{"type":"custom_tool_call","id":"` + itemID + `","call_id":"` + callID + `","name":"write_file","input":"payload"},{"type":"custom_tool_call_output","call_id":"` + callID + `","output":"file written"}]}`)
	translated, err := RequestForEndpointFrom("openai-response", "claude-sonnet-5.5", request, false, EndpointMessages)
	if err != nil {
		t.Fatalf("translate distinct Responses custom tool replay to Claude: %v", err)
	}
	var toolUseID, toolResultID string
	for _, message := range gjson.GetBytes(translated, "messages").Array() {
		for _, block := range message.Get("content").Array() {
			switch block.Get("type").String() {
			case "tool_use":
				toolUseID = block.Get("id").String()
			case "tool_result":
				toolResultID = block.Get("tool_use_id").String()
			}
		}
	}
	decodedItemID, decodedCallID, ok := DecodeClaudeToolIDs(toolUseID)
	if !ok || decodedItemID != itemID || decodedCallID != callID || toolResultID != toolUseID {
		t.Fatalf("distinct Claude custom tool IDs = (%q, %q); decoded=(%q, %q, %t), want (%q, %q, true)", toolUseID, toolResultID, decodedItemID, decodedCallID, ok, itemID, callID)
	}
}

func TestResponsesClaudeRequestRejectsRestoredClaudeIDCollision(t *testing.T) {
	const claudeID = "shared-native-call"
	for _, test := range []struct {
		name        string
		typeName    string
		prefix      string
		payloadJSON string
	}{
		{name: "function call", typeName: "function_call", prefix: "fc_", payloadJSON: `"arguments":"{}"`},
		{name: "custom tool call", typeName: "custom_tool_call", prefix: "ctc_", payloadJSON: `"input":"payload"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			carrierItemID := test.prefix + "distinct-item"
			carrier := encodeClaudeToolID(carrierItemID, claudeID)
			request := []byte(`{"input":[{"type":"` + test.typeName + `","id":"` + test.prefix + claudeID + `","call_id":"` + claudeID + `","name":"first",` + test.payloadJSON + `},{"type":"` + test.typeName + `","id":"` + carrierItemID + `","call_id":"` + carrier + `","name":"second",` + test.payloadJSON + `}]}`)
			if _, err := RequestForEndpointFrom("openai-response", "claude-sonnet-5.5", request, false, EndpointMessages); err == nil {
				t.Fatal("distinct Responses call IDs that restore to one Claude tool ID were accepted")
			}
		})
	}
}

func TestResponsesClaudeRequestRejectsInvalidCarrierCallIDs(t *testing.T) {
	const itemID = "fc_fixture-messages-tool-1/+"
	const claudeID = "fixture-messages-tool-1/+"
	for _, test := range []struct {
		name    string
		carrier string
	}{
		{name: "malformed", carrier: "cpa_tool_v1_invalid"},
		{name: "unknown version", carrier: "cpa_tool_v2_future"},
		{name: "foreign item", carrier: encodeClaudeToolID("fc_foreign-item", claudeID)},
		{name: "nested reserved carrier", carrier: encodeClaudeToolID(itemID, encodeClaudeToolID(itemID, claudeID))},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := []byte(`{"input":[{"type":"function_call","id":"` + itemID + `","call_id":"` + test.carrier + `","name":"inspect","arguments":"{}"}]}`)
			if _, err := RequestForEndpointFrom("openai-response", "claude-sonnet-5.5", request, false, EndpointMessages); err == nil {
				t.Fatalf("invalid tool carrier %q was accepted", test.carrier)
			}
		})
	}
}
