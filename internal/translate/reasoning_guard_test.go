package translate

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestOpaqueReasoningRejectsLossyProtocolRoutes(t *testing.T) {
	signedClaude := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"opaque-signature"}]}]}`)
	if _, err := RequestForEndpointFrom("claude", "gpt-test", signedClaude, false, EndpointChatCompletions); err == nil || !strings.Contains(err.Error(), "foreign encrypted Responses reasoning") {
		t.Fatalf("signed Claude request error = %v", err)
	}
	if _, err := RequestForEndpointFrom("claude", "gpt-test", signedClaude, false, EndpointResponses); err != nil {
		t.Fatalf("custom Claude-to-Responses request rejected signed reasoning: %v", err)
	}
	redactedClaude := []byte(`{"messages":[{"role":"assistant","content":[{"type":"redacted_thinking","data":"opaque-data"}]}]}`)
	if _, err := RequestForEndpointFrom("claude", "gpt-test", redactedClaude, false, EndpointChatCompletions); err == nil {
		t.Fatal("redacted Claude reasoning was silently converted to Chat Completions")
	}
	responsesHistory := []byte(`{"input":[{"type":"reasoning","summary":[],"encrypted_content":"opaque-signature"}]}`)
	if _, err := RequestForEndpointFrom("openai-response", "gpt-test", responsesHistory, false, EndpointChatCompletions); err == nil {
		t.Fatal("encrypted Responses history was silently converted to Chat Completions")
	}
	responsesCacheControl := []byte(`{"model":"gpt-test","input":[{"role":"user","content":[{"type":"input_text","text":"keep","cache_control":{"type":"ephemeral"}}]}]}`)
	if _, err := RequestForEndpointFrom("openai-response", "gpt-test", responsesCacheControl, false, EndpointResponses); err == nil {
		t.Fatal("unsupported native Responses cache_control was passed through")
	}
	responsesSchema := []byte(`{"model":"gpt-test","tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"cache_control":{"type":"string"}}}}],"input":[{"role":"user","content":"keep"}]}`)
	if _, err := RequestForEndpointFrom("openai-response", "gpt-test", responsesSchema, false, EndpointResponses); err != nil {
		t.Fatalf("tool schema cache_control property was misclassified as a protocol hint: %v", err)
	}
	plainClaude := []byte(`{"messages":[{"role":"assistant","content":[{"type":"text","text":"plain answer"}]}]}`)
	if _, err := RequestForEndpointFrom("claude", "gpt-test", plainClaude, false, EndpointChatCompletions); err != nil {
		t.Fatalf("plain Claude content was rejected: %v", err)
	}
	responsesOutput := []byte(`{"id":"resp_1","status":"completed","output":[{"type":"reasoning","summary":[],"encrypted_content":"opaque-signature"}]}`)
	if _, err := ResponseFromEndpoint(context.Background(), EndpointResponses, "openai", "gpt-test", nil, nil, responsesOutput); err == nil {
		t.Fatal("encrypted Responses output was silently converted to Chat Completions")
	}
	if _, err := ResponseFromEndpoint(context.Background(), EndpointResponses, "claude", "gpt-test", nil, nil, responsesOutput); err != nil {
		t.Fatalf("custom Responses-to-Claude response rejected opaque reasoning: %v", err)
	}
	claudeOutput := []byte(`{"type":"message","content":[{"type":"thinking","thinking":"reason","signature":"opaque-signature"}]}`)
	claudeOutput = []byte(`{"type":"message","id":"msg_1","model":"gpt-test","role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"opaque-signature"},{"type":"redacted_thinking","data":"opaque-data"},{"type":"text","text":"answer"}],"usage":{"input_tokens":2,"output_tokens":1}}`)
	responsesOutput, err := ResponseFromEndpoint(context.Background(), EndpointMessages, "openai-response", "gpt-test", nil, nil, claudeOutput)
	if err != nil {
		t.Fatalf("signed and redacted Claude output was rejected on the reversible Responses route: %v", err)
	}
	if got := gjson.GetBytes(responsesOutput, "output.0.encrypted_content").String(); got != "opaque-signature" {
		t.Fatalf("Responses reasoning signature = %q; response=%s", got, responsesOutput)
	}
	if got := gjson.GetBytes(responsesOutput, "output.1.encrypted_content").String(); got != redactedThinkingPrefix+"opaque-data" {
		t.Fatalf("Responses redacted reasoning = %q; response=%s", got, responsesOutput)
	}
	if got := gjson.GetBytes(responsesOutput, "output.2.content.0.text").String(); got != "answer" {
		t.Fatalf("Responses text = %q; response=%s", got, responsesOutput)
	}
}

func TestOpaqueReasoningRejectsLossyStreamingRoutes(t *testing.T) {
	claudeFrame := []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"opaque-signature\"}}\n\n")
	var claudeState any
	claudeFrames := [][]byte{
		[]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"gpt-test\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"),
		[]byte("data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n"),
		[]byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"reason\"}}\n\n"),
		claudeFrame,
		[]byte("data: {\"type\":\"content_block_stop\",\"index\":0}\n\n"),
		[]byte("data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"redacted_thinking\",\"data\":\"opaque-data\"}}\n\n"),
		[]byte("data: {\"type\":\"content_block_stop\",\"index\":1}\n\n"),
		[]byte("data: {\"type\":\"message_stop\"}\n\n"),
	}
	var translated [][]byte
	for _, frame := range claudeFrames {
		frames, err := StreamFromEndpoint(context.Background(), EndpointMessages, "openai-response", "gpt-test", nil, nil, frame, &claudeState)
		if err != nil {
			t.Fatalf("translate signed/redacted Claude SSE: %v", err)
		}
		translated = append(translated, frames...)
	}
	var signed, redacted string
	for _, frame := range translated {
		_, data, _, err := parseSSEFrame(frame)
		if err != nil || len(data) == 0 {
			continue
		}
		payload, err := decodeObject(data)
		if err != nil {
			continue
		}
		if stringValue(payload["type"]) == "response.output_item.done" {
			item := objectValue(payload["item"])
			switch stringValue(item["type"]) {
			case "reasoning":
				if encrypted := stringValue(item["encrypted_content"]); encrypted == "opaque-signature" {
					signed = encrypted
				} else if encrypted == redactedThinkingPrefix+"opaque-data" {
					redacted = encrypted
				}
			}
		}
	}
	if signed != "opaque-signature" || redacted != redactedThinkingPrefix+"opaque-data" {
		t.Fatalf("Claude SSE reasoning data was not preserved: signed=%q redacted=%q frames=%q", signed, redacted, translated)
	}
	responsesFrame := []byte("event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"encrypted_content\":\"opaque-signature\"}}\n\n")
	var responsesState any
	if _, err := StreamFromEndpoint(context.Background(), EndpointResponses, "openai", "gpt-test", nil, nil, responsesFrame, &responsesState); err == nil {
		t.Fatal("Responses signature stream was silently converted")
	}
}
