package translate

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

func TestResponsesRequestPreflightRejectsDroppedBlocks(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		body     string
	}{
		{
			name:     "unknown Chat message block",
			endpoint: EndpointChatCompletions,
			body:     `{"input":[{"type":"message","role":"user","content":[{"type":"input_audio","data":"payload"}]}]}`,
		},
		{
			name:     "unsupported Responses item",
			endpoint: EndpointChatCompletions,
			body:     `{"input":[{"type":"computer_call","action":{"x":1}}]}`,
		},
		{
			name:     "object image URL would be dropped by Chat translator",
			endpoint: EndpointChatCompletions,
			body:     `{"input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":{"url":"https://example.test/image.png"}}]}]}`,
		},
		{
			name:     "unknown custom tool output block",
			endpoint: EndpointChatCompletions,
			body:     `{"input":[{"type":"custom_tool_call_output","call_id":"call_1","output":[{"type":"audio","data":"payload"}]}]}`,
		},
		{
			name:     "Claude file URL is not translated by SDK",
			endpoint: EndpointMessages,
			body:     `{"input":[{"type":"message","role":"user","content":[{"type":"input_file","file_url":"https://example.test/file.pdf"}]}]}`,
		},
		{
			name:     "unknown Claude message block",
			endpoint: EndpointMessages,
			body:     `{"input":[{"type":"message","role":"user","content":[{"type":"future_modal_part","payload":"value"}]}]}`,
		},
		{
			name:     "unsupported citation annotation",
			endpoint: EndpointMessages,
			body:     `{"input":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"cited","annotations":[{"type":"url_citation","url":"https://example.test"}]}]}]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := RequestForEndpointFrom("openai-response", "gpt-test", []byte(test.body), false, test.endpoint); err == nil {
				t.Fatalf("unsupported block was silently dropped for %s", test.endpoint)
			}
		})
	}
}

func TestResponsesPreviousResponseIDIsRejectedForCrossFormatRequests(t *testing.T) {
	for _, test := range []struct {
		name     string
		endpoint string
		stream   bool
	}{
		{name: "Chat JSON", endpoint: EndpointChatCompletions},
		{name: "Chat SSE", endpoint: EndpointChatCompletions, stream: true},
		{name: "Messages JSON", endpoint: EndpointMessages},
		{name: "Messages SSE", endpoint: EndpointMessages, stream: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := RequestForEndpointFrom("openai-response", "gpt-test", []byte(`{"previous_response_id":"resp_previous"}`), test.stream, test.endpoint); err == nil {
				t.Fatal("history-only previous_response_id was silently dropped")
			}
		})
	}
}

func TestResponsesPreviousResponseIDEmptyAndNullRemainCompatible(t *testing.T) {
	for _, endpoint := range []string{EndpointChatCompletions, EndpointMessages} {
		for _, value := range []string{`""`, "null"} {
			request := []byte(`{"previous_response_id":` + value + `,"input":"keep this prompt"}`)
			if _, err := RequestForEndpointFrom("openai-response", "gpt-test", request, false, endpoint); err != nil {
				t.Fatalf("empty/null previous_response_id rejected for %s: %v", endpoint, err)
			}
		}
	}
}

func TestNativeResponsesPreservesPreviousResponseID(t *testing.T) {
	request := []byte(`{"model":"old","previous_response_id":"resp_previous","input":[]}`)
	translated, err := RequestForEndpointFrom("openai-response", "gpt-test", request, false, EndpointResponses)
	if err != nil {
		t.Fatalf("translate native Responses request: %v", err)
	}
	if got := gjson.GetBytes(translated, "previous_response_id").String(); got != "resp_previous" {
		t.Fatalf("native previous_response_id = %q; request=%s", got, translated)
	}
}

func TestResponsesRequestPreflightKeepsSupportedToolOutputImages(t *testing.T) {
	chatBody := []byte(`{"input":[{"type":"custom_tool_call_output","call_id":"call_1","output":[{"type":"input_text","text":"result"},{"type":"input_image","image_url":"https://example.test/image.png"}]}]}`)
	chat, err := RequestForEndpointFrom("openai-response", "gpt-test", chatBody, false, EndpointChatCompletions)
	if err != nil {
		t.Fatalf("translate supported Chat tool output image: %v", err)
	}
	if !gjson.GetBytes(chat, `messages.#(role==user).content.#(type==image_url)`).Exists() {
		t.Fatalf("Chat tool output image was not preserved: %s", chat)
	}
	claudeBody := []byte(`{"input":[{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_text","text":"result"},{"type":"input_image","image_url":"data:image/png;base64,YWJj"}]}]}`)
	claude, err := RequestForEndpointFrom("openai-response", "gpt-test", claudeBody, false, EndpointMessages)
	if err != nil {
		t.Fatalf("translate supported Claude tool output image: %v", err)
	}
	if !gjson.GetBytes(claude, `messages.#(role==user).content.#(type==image)`).Exists() {
		t.Fatalf("Claude tool output image was not preserved: %s", claude)
	}
}

func TestResponsesClaudeResponsePreflightRejectsDroppedItemsAndParts(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown output item", body: `{"status":"completed","output":[{"type":"image_generation_call","result":"image"}]}`},
		{name: "unknown message part", body: `{"status":"completed","output":[{"type":"message","content":[{"type":"output_audio","data":"payload"}]}]}`},
		{name: "unrepresentable citation annotation", body: `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"cited","annotations":[{"type":"url_citation","url":"https://example.test"}]}]}]}`},
		{name: "function output in assistant response", body: `{"status":"completed","output":[{"type":"function_call_output","call_id":"call_1","output":"result"}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ResponseFromEndpoint(context.Background(), EndpointResponses, "claude", "gpt-test", nil, nil, []byte(test.body)); err == nil {
				t.Fatal("unsupported Responses output was silently discarded")
			}
		})
	}
}

func TestResponsesStreamPreflightRejectsDroppedItemsAndParts(t *testing.T) {
	tests := []struct {
		name  string
		frame string
	}{
		{
			name:  "unknown early item",
			frame: "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"image_generation_call\",\"result\":\"image\"}}\n\n",
		},
		{
			name:  "unknown early content part",
			frame: "event: response.content_part.added\ndata: {\"type\":\"response.content_part.added\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_audio\",\"data\":\"payload\"}}\n\n",
		},
		{
			name:  "unknown terminal-only item",
			frame: "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"future_item\",\"payload\":\"value\"}]}}\n\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var state any
			if _, err := StreamFromEndpoint(context.Background(), EndpointResponses, "claude", "gpt-test", nil, nil, []byte(test.frame), &state); err == nil {
				t.Fatal("unsupported Responses stream item was silently discarded")
			}
		})
	}
}

func TestClaudeStreamPreflightRejectsUnknownAssistantBlocks(t *testing.T) {
	frames := [][]byte{
		[]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"future_block\",\"payload\":\"value\"}}\n\n"),
		[]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"future_delta\",\"payload\":\"value\"}}\n\n"),
	}
	for _, frame := range frames {
		var state any
		if _, err := StreamFromEndpoint(context.Background(), EndpointMessages, "openai-response", "gpt-test", nil, nil, frame, &state); err == nil {
			t.Fatal("unknown Claude assistant content block was silently discarded")
		}
	}
}

func TestClaudeResponseCustomToolInputRemainsFreeform(t *testing.T) {
	body := []byte(`{"id":"resp_1","status":"completed","output":[{"id":"ctc_item","type":"custom_tool_call","call_id":"ctc_call","name":"exec_command","input":"echo hello"}]}`)
	response, err := ResponseFromEndpoint(context.Background(), EndpointResponses, "claude", "gpt-test", nil, nil, body)
	if err != nil {
		t.Fatalf("translate custom tool response: %v", err)
	}
	if got := gjson.GetBytes(response, "content.0.input.input").String(); got != "echo hello" {
		t.Fatalf("Claude freeform tool input = %q; response=%s", got, response)
	}
}

func TestClaudeMessagesResponseToolInputFeedsResponsesCustomTools(t *testing.T) {
	for _, test := range []struct {
		name      string
		input     map[string]any
		wantInput string
	}{
		{name: "custom_tool", input: map[string]any{"input": "payload"}, wantInput: "payload"},
		{name: "apply_patch", input: map[string]any{"input": "*** Begin Patch\n*** End Patch"}, wantInput: "*** Begin Patch\n*** End Patch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{
				"type": "message", "id": "msg_1", "model": "claude-test", "role": "assistant",
				"content":     []any{map[string]any{"type": "tool_use", "id": "toolu_1", "name": test.name, "input": test.input}},
				"stop_reason": "tool_use", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
			})
			if err != nil {
				t.Fatalf("encode Claude response: %v", err)
			}
			translatedRequest, err := json.Marshal(map[string]any{"tools": []any{map[string]any{"type": "custom", "name": test.name}}})
			if err != nil {
				t.Fatalf("encode Responses request: %v", err)
			}
			response, err := claudeMessageResponseToResponses(context.Background(), "gpt-test", nil, translatedRequest, body)
			if err != nil {
				t.Fatalf("translate Claude tool response: %v", err)
			}
			if got := gjson.GetBytes(response, "output.0.type").String(); got != "custom_tool_call" {
				t.Fatalf("Responses tool type = %q; response=%s", got, response)
			}
			if got := gjson.GetBytes(response, "output.0.input").String(); got != test.wantInput {
				t.Fatalf("Responses custom tool input = %q, want exact raw input %q; response=%s", got, test.wantInput, response)
			}
		})
	}
}
