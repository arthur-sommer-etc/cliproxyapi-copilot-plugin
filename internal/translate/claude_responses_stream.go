package translate

import (
	"encoding/json"
	"fmt"
	"strings"
)

type responsesClaudeStreamState struct {
	Started   bool
	Stopped   bool
	MessageID string
	Model     string
	NextBlock int
	Blocks    map[string]*responsesClaudeBlock
}

type responsesClaudeBlock struct {
	Index        int
	Kind         string
	Open         bool
	SawDelta     bool
	Accumulated  string
	Encrypted    string
	FunctionID   string
	FunctionName string
}

func responsesStreamToClaude(model string, frame []byte, state *any) ([][]byte, error) {
	if state == nil {
		return nil, fmt.Errorf("Responses-to-Claude stream translation requires state")
	}
	streamState, ok := (*state).(*responsesClaudeStreamState)
	if !ok {
		streamState = &responsesClaudeStreamState{
			Model:  model,
			Blocks: make(map[string]*responsesClaudeBlock),
		}
		*state = streamState
	}
	event, data, done, err := parseSSEFrame(frame)
	if err != nil {
		return nil, err
	}
	if done {
		if streamState.Stopped {
			return nil, nil
		}
		return nil, fmt.Errorf("Responses stream ended before a terminal response event")
	}
	if len(data) == 0 {
		return nil, nil
	}
	payload, err := decodeObject(data)
	if err != nil {
		return nil, fmt.Errorf("decode Responses SSE event %q: %w", event, err)
	}
	if event == "" {
		event = stringValue(payload["type"])
	}

	switch event {
	case "error", "response.failed":
		errorObject := objectValue(payload["error"])
		if response, okResponse := payload["response"].(map[string]any); okResponse {
			if nested := objectValue(response["error"]); len(nested) > 0 {
				errorObject = nested
			}
		}
		message := firstNonEmptyString(stringValue(errorObject["message"]), stringValue(payload["message"]), "unknown upstream stream error")
		return nil, fmt.Errorf("Copilot Responses stream failed: %s", message)
	}

	var out [][]byte
	responseObject := objectValue(payload["response"])
	if !streamState.Started && (event == "response.created" || event == "response.in_progress" || len(responseObject) > 0) {
		out = append(out, streamState.start(responseObject, payload)...)
	}
	if !streamState.Started && strings.HasPrefix(event, "response.") {
		out = append(out, streamState.start(responseObject, payload)...)
	}

	switch event {
	case "response.output_item.added":
		item := objectValue(payload["item"])
		key := itemKey(payload)
		switch stringValue(item["type"]) {
		case "reasoning":
			block := streamState.block(key, "thinking")
			block.Encrypted = stringValue(item["encrypted_content"])
			out = append(out, streamState.openThinking(block)...)
		case "function_call", "custom_tool_call":
			block := streamState.block(key, "tool_use")
			block.FunctionID = firstString(item, "call_id", "id")
			block.FunctionName = stringValue(item["name"])
			out = append(out, streamState.openTool(block)...)
		}
	case "response.content_part.added":
		part := objectValue(payload["part"])
		switch stringValue(part["type"]) {
		case "output_text", "text", "refusal":
			block := streamState.block(contentKey(payload), "text")
			out = append(out, streamState.openText(block)...)
			if text := firstRawString(part, "text", "refusal"); text != "" {
				out = append(out, streamState.delta(block, "text_delta", "text", text)...)
			}
		}
	case "response.output_text.delta", "response.refusal.delta":
		block := streamState.block(contentKey(payload), "text")
		out = append(out, streamState.openText(block)...)
		if delta := rawStringValue(payload["delta"]); delta != "" {
			out = append(out, streamState.delta(block, "text_delta", "text", delta)...)
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		block := streamState.block(itemKey(payload), "thinking")
		out = append(out, streamState.openThinking(block)...)
		if delta := rawStringValue(payload["delta"]); delta != "" {
			out = append(out, streamState.delta(block, "thinking_delta", "thinking", delta)...)
		}
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		block := streamState.block(itemKey(payload), "tool_use")
		if block.FunctionID == "" {
			block.FunctionID = firstNonEmptyString(stringValue(payload["call_id"]), stringValue(payload["item_id"]))
		}
		if block.FunctionName == "" {
			block.FunctionName = stringValue(payload["name"])
		}
		out = append(out, streamState.openTool(block)...)
		if delta := rawStringValue(payload["delta"]); delta != "" {
			out = append(out, streamState.delta(block, "input_json_delta", "partial_json", delta)...)
		}
	case "response.content_part.done":
		out = append(out, streamState.closeBlock(contentKey(payload))...)
	case "response.output_item.done":
		item := objectValue(payload["item"])
		key := itemKey(payload)
		block := streamState.Blocks[key]
		if block == nil {
			switch stringValue(item["type"]) {
			case "reasoning":
				block = streamState.block(key, "thinking")
				out = append(out, streamState.openThinking(block)...)
			case "function_call", "custom_tool_call":
				block = streamState.block(key, "tool_use")
				block.FunctionID = firstString(item, "call_id", "id")
				block.FunctionName = stringValue(item["name"])
				out = append(out, streamState.openTool(block)...)
			}
		}
		if block != nil {
			switch block.Kind {
			case "thinking":
				fullText := responsesReasoningText(item)
				if !block.SawDelta && fullText != "" {
					out = append(out, streamState.delta(block, "thinking_delta", "thinking", fullText)...)
				}
				block.Encrypted = firstNonEmptyString(stringValue(item["encrypted_content"]), block.Encrypted)
				if block.Encrypted != "" && !strings.HasPrefix(block.Encrypted, redactedThinkingPrefix) {
					out = append(out, streamState.delta(block, "signature_delta", "signature", block.Encrypted)...)
				}
			case "tool_use":
				arguments := firstRawString(item, "arguments", "input")
				if !block.SawDelta && arguments != "" {
					out = append(out, streamState.delta(block, "input_json_delta", "partial_json", arguments)...)
				}
			}
			out = append(out, streamState.closeBlock(key)...)
		}
	case "response.completed", "response.incomplete":
		if event == "response.completed" {
			if errFailure := responsesFailure(responseObject); errFailure != nil {
				return nil, errFailure
			}
		}
		out = append(out, streamState.finish(responseObject)...)
	}
	return out, nil
}

func (s *responsesClaudeStreamState) start(response, payload map[string]any) [][]byte {
	if s.Started {
		return nil
	}
	s.Started = true
	s.MessageID = firstNonEmptyString(stringValue(response["id"]), stringValue(payload["response_id"]), "msg_copilot")
	s.Model = firstNonEmptyString(stringValue(response["model"]), s.Model)
	usage := objectValue(response["usage"])
	message := map[string]any{
		"id":            s.MessageID,
		"type":          "message",
		"role":          "assistant",
		"model":         s.Model,
		"content":       []any{},
		"stop_reason":   nil,
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  numberValue(usage["input_tokens"]),
			"output_tokens": 0,
		},
	}
	return [][]byte{claudeSSE("message_start", map[string]any{"type": "message_start", "message": message})}
}

func (s *responsesClaudeStreamState) block(key, kind string) *responsesClaudeBlock {
	if key == "" {
		key = fmt.Sprintf("%s:%d", kind, len(s.Blocks))
	}
	if block := s.Blocks[key]; block != nil {
		return block
	}
	block := &responsesClaudeBlock{Index: s.NextBlock, Kind: kind}
	s.NextBlock++
	s.Blocks[key] = block
	return block
}

func (s *responsesClaudeStreamState) openText(block *responsesClaudeBlock) [][]byte {
	if block.Open {
		return nil
	}
	block.Open = true
	return [][]byte{claudeSSE("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         block.Index,
		"content_block": map[string]any{"type": "text", "text": ""},
	})}
}

func (s *responsesClaudeStreamState) openThinking(block *responsesClaudeBlock) [][]byte {
	if block.Open {
		return nil
	}
	block.Open = true
	contentBlock := map[string]any{"type": "thinking", "thinking": ""}
	if strings.HasPrefix(block.Encrypted, redactedThinkingPrefix) {
		contentBlock = map[string]any{
			"type": "redacted_thinking",
			"data": strings.TrimPrefix(block.Encrypted, redactedThinkingPrefix),
		}
	}
	return [][]byte{claudeSSE("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         block.Index,
		"content_block": contentBlock,
	})}
}

func (s *responsesClaudeStreamState) openTool(block *responsesClaudeBlock) [][]byte {
	if block.Open {
		return nil
	}
	block.Open = true
	return [][]byte{claudeSSE("content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": block.Index,
		"content_block": map[string]any{
			"type":  "tool_use",
			"id":    block.FunctionID,
			"name":  block.FunctionName,
			"input": map[string]any{},
		},
	})}
}

func (s *responsesClaudeStreamState) delta(block *responsesClaudeBlock, deltaType, field, value string) [][]byte {
	if value == "" {
		return nil
	}
	block.SawDelta = true
	block.Accumulated += value
	return [][]byte{claudeSSE("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": block.Index,
		"delta": map[string]any{"type": deltaType, field: value},
	})}
}

func (s *responsesClaudeStreamState) closeBlock(key string) [][]byte {
	block := s.Blocks[key]
	if block == nil || !block.Open {
		return nil
	}
	block.Open = false
	return [][]byte{claudeSSE("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": block.Index,
	})}
}

func (s *responsesClaudeStreamState) finish(response map[string]any) [][]byte {
	if s.Stopped {
		return nil
	}
	var out [][]byte
	hasToolUse := false
	for key, block := range s.Blocks {
		if block.Kind == "tool_use" {
			hasToolUse = true
		}
		out = append(out, s.closeBlock(key)...)
	}
	usage := objectValue(response["usage"])
	out = append(out, claudeSSE("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   responsesStopReason(response, hasToolUse),
			"stop_sequence": nil,
		},
		"usage": map[string]any{"output_tokens": numberValue(usage["output_tokens"])},
	}))
	out = append(out, claudeSSE("message_stop", map[string]any{"type": "message_stop"}))
	s.Stopped = true
	return out
}

func parseSSEFrame(frame []byte) (event string, data []byte, done bool, err error) {
	normalized := strings.ReplaceAll(string(frame), "\r\n", "\n")
	var dataLines []string
	for _, line := range strings.Split(normalized, "\n") {
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if len(dataLines) == 0 {
		return event, nil, false, nil
	}
	joined := strings.Join(dataLines, "\n")
	if joined == "[DONE]" {
		return event, nil, true, nil
	}
	if !json.Valid([]byte(joined)) {
		return "", nil, false, fmt.Errorf("Responses SSE data is invalid JSON")
	}
	return event, []byte(joined), false, nil
}

func claudeSSE(event string, payload map[string]any) []byte {
	data, _ := json.Marshal(payload)
	return []byte("event: " + event + "\ndata: " + string(data) + "\n\n")
}

func itemKey(payload map[string]any) string {
	if index := numberKey(payload["output_index"]); index != "" {
		return "item:" + index
	}
	return "item:" + firstNonEmptyString(stringValue(payload["item_id"]), stringValue(objectValue(payload["item"])["id"]))
}

func contentKey(payload map[string]any) string {
	return itemKey(payload) + ":content:" + firstNonEmptyString(numberKey(payload["content_index"]), "0")
}

func numberKey(value any) string {
	switch number := value.(type) {
	case json.Number:
		return number.String()
	case float64:
		return fmt.Sprintf("%.0f", number)
	case int:
		return fmt.Sprintf("%d", number)
	case int64:
		return fmt.Sprintf("%d", number)
	default:
		return ""
	}
}
