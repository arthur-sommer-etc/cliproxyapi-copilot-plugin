package translate

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type responsesClaudeStreamState struct {
	Started           bool
	Stopped           bool
	MessageID         string
	Model             string
	NextBlock         int
	BufferAfterTool   bool
	ToolOutputIndexes map[int]struct{}
	Blocks            map[string]*responsesClaudeBlock
}

type responsesClaudeBlock struct {
	Index           int
	Kind            string
	Open            bool
	SawDelta        bool
	Accumulated     string
	TextAccumulated string
	SignatureSent   bool
	RedactedSent    bool
	Encrypted       string
	FunctionID      string
	FunctionItemID  string
	FunctionCallID  string
	FunctionName    string
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
		return nil, fmt.Errorf("Copilot Responses stream failed")
	}
	if streamState.BufferAfterTool && event != "response.completed" && event != "response.incomplete" {
		if err := streamState.trackBufferedToolEvent(event, payload); err != nil {
			return nil, err
		}
		return nil, nil
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
			block.Encrypted = rawStringValue(item["encrypted_content"])
			out = append(out, streamState.openThinking(block)...)
		case "function_call", "custom_tool_call":
			if err := streamState.markToolOutputIndex(payload); err != nil {
				return nil, err
			}
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
		if err := streamState.markToolOutputIndex(payload); err != nil {
			return nil, err
		}
	case "response.content_part.done":
		out = append(out, streamState.closeBlock(contentKey(payload))...)
	case "response.output_item.done":
		item := objectValue(payload["item"])
		key := itemKey(payload)
		if stringValue(item["type"]) == "function_call" || stringValue(item["type"]) == "custom_tool_call" {
			if err := streamState.markToolOutputIndex(payload); err != nil {
				return nil, err
			}
		} else {
			block := streamState.Blocks[key]
			if block == nil && stringValue(item["type"]) == "reasoning" {
				block = streamState.block(key, "thinking")
				block.Encrypted = rawStringValue(item["encrypted_content"])
				out = append(out, streamState.openThinking(block)...)
			}
			if block != nil {
				if block.Kind == "thinking" {
					out = append(out, streamState.openThinking(block)...)
					fullText := responsesReasoningText(item)
					if !block.SawDelta && fullText != "" {
						out = append(out, streamState.delta(block, "thinking_delta", "thinking", fullText)...)
					}
					if encrypted := rawStringValue(item["encrypted_content"]); encrypted != "" {
						block.Encrypted = encrypted
					}
					if block.Encrypted != "" && !strings.HasPrefix(block.Encrypted, redactedThinkingPrefix) && !block.SignatureSent {
						out = append(out, streamState.delta(block, "signature_delta", "signature", block.Encrypted)...)
					}
					out = append(out, streamState.closeBlock(key)...)
				}
			}
		}

	case "response.completed":
		if errFailure := responsesFailure(responseObject); errFailure != nil {
			return nil, errFailure
		}
		terminalFrames, errTerminal := streamState.reconcileTerminalOutput(responseObject)
		if errTerminal != nil {
			return nil, errTerminal
		}
		out = append(out, terminalFrames...)
		out = append(out, streamState.finish(responseObject)...)
	case "response.incomplete":
		return nil, fmt.Errorf("Copilot Responses stream ended with an incomplete response")
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
	if cached, ok := objectValue(usage["input_tokens_details"])["cached_tokens"]; ok {
		message["usage"].(map[string]any)["cache_read_input_tokens"] = numberValue(cached)
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
		block.RedactedSent = true
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
	if deltaType == "text_delta" || deltaType == "thinking_delta" {
		block.TextAccumulated += value
	}
	if deltaType == "signature_delta" {
		block.SignatureSent = true
	}
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

func (s *responsesClaudeStreamState) reconcileTerminalOutput(response map[string]any) ([][]byte, error) {
	if s.Stopped {
		return nil, nil
	}
	items, ok := response["output"].([]any)
	if !ok {
		return nil, fmt.Errorf("Copilot Responses completed without an output array")
	}
	var out [][]byte
	terminalToolIndexes := make(map[int]struct{})
	for outputIndex, rawItem := range items {
		item, okItem := rawItem.(map[string]any)
		if !okItem {
			continue
		}
		key := fmt.Sprintf("item:%d", outputIndex)
		switch stringValue(item["type"]) {
		case "message":
			for contentIndex, rawPart := range arrayValue(item["content"]) {
				part, okPart := rawPart.(map[string]any)
				if !okPart {
					continue
				}
				if stringValue(part["type"]) != "output_text" && stringValue(part["type"]) != "text" && stringValue(part["type"]) != "refusal" {
					continue
				}
				text := firstRawString(part, "text", "refusal")
				contentKey := fmt.Sprintf("%s:content:%d", key, contentIndex)
				out = append(out, s.reconcileText(contentKey, text)...)
			}
		case "reasoning":
			out = append(out, s.reconcileReasoning(key, item)...)
		case "function_call", "custom_tool_call":
			terminalToolIndexes[outputIndex] = struct{}{}
			frames, errFunction := s.reconcileFunction(key, item)
			if errFunction != nil {
				return nil, errFunction
			}
			out = append(out, frames...)
		}
	}
	for outputIndex := range s.ToolOutputIndexes {
		if _, ok := terminalToolIndexes[outputIndex]; !ok {
			return nil, fmt.Errorf("Copilot Responses completed without a terminal tool item at output index %d", outputIndex)
		}
	}
	return out, nil
}

func (s *responsesClaudeStreamState) markToolOutputIndex(payload map[string]any) error {
	indexText := numberKey(payload["output_index"])
	index, err := strconv.Atoi(indexText)
	if err != nil || index < 0 {
		return fmt.Errorf("Copilot Responses tool stream event has an invalid output index")
	}
	if s.ToolOutputIndexes == nil {
		s.ToolOutputIndexes = make(map[int]struct{})
	}
	s.ToolOutputIndexes[index] = struct{}{}
	s.BufferAfterTool = true
	return nil
}

func (s *responsesClaudeStreamState) trackBufferedToolEvent(event string, payload map[string]any) error {
	switch event {
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		return s.markToolOutputIndex(payload)
	case "response.output_item.added", "response.output_item.done":
		item := objectValue(payload["item"])
		if stringValue(item["type"]) == "function_call" || stringValue(item["type"]) == "custom_tool_call" {
			return s.markToolOutputIndex(payload)
		}
	}
	return nil
}

func (s *responsesClaudeStreamState) reconcileText(key, text string) [][]byte {
	block := s.Blocks[key]
	remainder := text
	if block != nil && block.SawDelta {
		if !strings.HasPrefix(text, block.TextAccumulated) {
			return nil
		}
		remainder = strings.TrimPrefix(text, block.TextAccumulated)
	}
	if remainder == "" {
		return nil
	}
	target, targetKey := terminalBlock(s, key, "text", block)
	out := s.openText(target)
	out = append(out, s.delta(target, "text_delta", "text", remainder)...)
	out = append(out, s.closeBlock(targetKey)...)
	return out
}

func (s *responsesClaudeStreamState) reconcileReasoning(key string, item map[string]any) [][]byte {
	block := s.Blocks[key]
	fullText := responsesReasoningText(item)
	textRemainder := fullText
	if block != nil && block.TextAccumulated != "" {
		if !strings.HasPrefix(fullText, block.TextAccumulated) {
			textRemainder = ""
		} else {
			textRemainder = strings.TrimPrefix(fullText, block.TextAccumulated)
		}
	}
	encrypted := rawStringValue(item["encrypted_content"])
	redacted := strings.HasPrefix(encrypted, redactedThinkingPrefix)
	needsRedacted := redacted && (block == nil || !block.RedactedSent)
	needsSignature := encrypted != "" && !redacted && (block == nil || !block.SignatureSent)
	if textRemainder == "" && !needsRedacted && !needsSignature {
		return nil
	}
	target, targetKey := terminalBlock(s, key, "thinking", block)
	if redacted && needsRedacted && block != nil && block.Open && !block.RedactedSent {
		targetKey = key + ":terminal"
		target = s.block(targetKey, "thinking")
	}
	if redacted && !target.RedactedSent {
		target.Encrypted = encrypted
	}
	out := s.openThinking(target)
	out = append(out, s.delta(target, "thinking_delta", "thinking", textRemainder)...)
	if needsSignature && !target.SignatureSent {
		out = append(out, s.delta(target, "signature_delta", "signature", encrypted)...)
	}
	out = append(out, s.closeBlock(targetKey)...)
	return out
}

func (s *responsesClaudeStreamState) reconcileFunction(key string, item map[string]any) ([][]byte, error) {
	if rawStringValue(item["call_id"]) == "" || stringValue(item["name"]) == "" {
		return nil, fmt.Errorf("Copilot Responses terminal tool item lacks its call ID or name")
	}
	arguments, err := responsesFunctionArguments(item)
	if err != nil {
		return nil, err
	}
	block := s.block(key, "tool_use")
	setResponsesFunction(block, item)
	block.FunctionName = stringValue(item["name"])
	out := s.openTool(block)
	if arguments != "" {
		out = append(out, s.delta(block, "input_json_delta", "partial_json", arguments)...)
	}
	out = append(out, s.closeBlock(key)...)
	return out, nil
}

func terminalBlock(s *responsesClaudeStreamState, key, kind string, current *responsesClaudeBlock) (*responsesClaudeBlock, string) {
	if current == nil {
		return s.block(key, kind), key
	}
	if current.Open {
		return current, key
	}
	terminalKey := key + ":terminal"
	return s.block(terminalKey, kind), terminalKey
}

func setResponsesFunction(block *responsesClaudeBlock, item map[string]any) {
	itemID := rawStringValue(item["id"])
	callID := rawStringValue(item["call_id"])
	block.FunctionItemID = itemID
	block.FunctionCallID = callID
	block.FunctionID = encodeClaudeToolID(itemID, callID)
}

func responsesFunctionArguments(item map[string]any) (string, error) {
	arguments := "{}"
	if stringValue(item["type"]) == "custom_tool_call" {
		input, ok := item["input"]
		if !ok {
			return "", fmt.Errorf("Copilot Responses terminal custom tool input is missing")
		}
		var object map[string]any
		switch value := input.(type) {
		case map[string]any:
			object = value
		case string:
			object = map[string]any{"input": value}
		default:
			return "", fmt.Errorf("Copilot Responses terminal custom tool input is not an object or string")
		}
		encoded, err := json.Marshal(object)
		if err != nil {
			return "", fmt.Errorf("encode Copilot Responses terminal custom tool input: %w", err)
		}
		arguments = string(encoded)
	} else if rawArguments, ok := item["arguments"]; ok {
		value, okValue := rawArguments.(string)
		if !okValue {
			return "", fmt.Errorf("Copilot Responses terminal tool arguments are not a string")
		}
		if strings.TrimSpace(value) != "" {
			arguments = value
		}
	} else if input, ok := item["input"]; ok {
		if _, okInput := input.(map[string]any); !okInput {
			return "", fmt.Errorf("Copilot Responses terminal tool input is not an object")
		}
		encoded, err := json.Marshal(input)
		if err != nil {
			return "", fmt.Errorf("encode Copilot Responses terminal custom tool input: %w", err)
		}
		arguments = string(encoded)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(arguments), &decoded); err != nil || decoded == nil {
		return "", fmt.Errorf("Copilot Responses terminal tool arguments are not a JSON object")
	}
	return arguments, nil
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
	finalUsage := map[string]any{"output_tokens": numberValue(usage["output_tokens"])}
	if inputTokens, ok := usage["input_tokens"]; ok {
		finalUsage["input_tokens"] = numberValue(inputTokens)
	}
	if cachedTokens, ok := objectValue(usage["input_tokens_details"])["cached_tokens"]; ok {
		finalUsage["cache_read_input_tokens"] = numberValue(cachedTokens)
	}
	out = append(out, claudeSSE("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   responsesStopReason(response, hasToolUse),
			"stop_sequence": nil,
		},
		"usage": finalUsage,
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
