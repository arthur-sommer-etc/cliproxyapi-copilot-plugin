package translate

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
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
	BufferAfterSearch bool
	SearchItems       map[int]*responsesClaudeSearchItem
	BufferedText      map[string][]string
	BufferedCitations map[string][]any
	TextParts         map[string]map[string]any
	ToolOutputIndexes map[int]struct{}
	Blocks            map[string]*responsesClaudeBlock
}

type responsesClaudeSearchItem struct {
	Added map[string]any
	Done  map[string]any
}

type responsesClaudeBlock struct {
	Index           int
	Kind            string
	Open            bool
	SawDelta        bool
	Accumulated     string
	TextAccumulated string
	Citations       []any
	TextFinished    bool
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
	if streamState.Stopped {
		return nil, fmt.Errorf("Copilot Responses stream continued after its terminal response")
	}
	switch event {
	case "error", "response.failed":
		return nil, fmt.Errorf("Copilot Responses stream failed")
	}
	if (streamState.BufferAfterTool || streamState.BufferAfterSearch) && event != "response.completed" && event != "response.incomplete" {
		if err := streamState.trackBufferedEvent(event, payload); err != nil {
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
	case "response.content_part.added":
		part := objectValue(payload["part"])
		if typ := stringValue(part["type"]); typ == "output_text" || typ == "text" || typ == "refusal" {
			block := streamState.block(contentKey(payload), "text")
			if block.TextFinished || block.Open {
				return nil, fmt.Errorf("Copilot Responses stream repeated a text part addition")
			}
			out = append(out, streamState.openText(block)...)
			out = append(out, streamState.delta(block, "text_delta", "text", firstRawString(part, "text", "refusal"))...)
			out = append(out, streamState.textCitations(block, arrayValue(part["annotations"]))...)
		}
	case "response.output_text.delta", "response.refusal.delta":
		block := streamState.block(contentKey(payload), "text")
		if block.TextFinished {
			return nil, fmt.Errorf("Copilot Responses text delta followed its part completion")
		}
		out = append(out, streamState.openText(block)...)
		out = append(out, streamState.delta(block, "text_delta", "text", rawStringValue(payload["delta"]))...)
	case "response.output_text.annotation.added":
		block := streamState.block(contentKey(payload), "text")
		if block.TextFinished || intValue(payload["annotation_index"]) != len(block.Citations) {
			return nil, fmt.Errorf("Copilot Responses citation has no unique open text position")
		}
		out = append(out, streamState.openText(block)...)
		out = append(out, streamState.textCitations(block, []any{payload["annotation"]})...)
	case "response.content_part.done":
		part := objectValue(payload["part"])
		if typ := stringValue(part["type"]); typ == "output_text" || typ == "text" || typ == "refusal" {
			if err := streamState.trackTextPart(payload); err != nil {
				return nil, err
			}
			fragments := []string{}
			frames, err := streamState.reconcileTextPart(contentKey(payload), firstRawString(part, "text", "refusal"), arrayValue(part["annotations"]), &fragments)
			if err != nil {
				return nil, err
			}
			out = append(out, frames...)
		}
	case "response.output_item.added":
		item := objectValue(payload["item"])
		key := itemKey(payload)
		switch stringValue(item["type"]) {
		case "web_search_call":
			if err := streamState.trackSearchItem(event, payload); err != nil {
				return nil, err
			}
		case "reasoning":
			block := streamState.block(key, "thinking")
			block.Encrypted = rawStringValue(item["encrypted_content"])
			out = append(out, streamState.openThinking(block)...)
		case "function_call", "custom_tool_call":
			if err := streamState.markToolOutputIndex(payload); err != nil {
				return nil, err
			}
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
	case "response.output_item.done":
		item := objectValue(payload["item"])
		key := itemKey(payload)
		if stringValue(item["type"]) == "web_search_call" {
			if err := streamState.trackSearchItem(event, payload); err != nil {
				return nil, err
			}
		} else if stringValue(item["type"]) == "function_call" || stringValue(item["type"]) == "custom_tool_call" {
			if err := streamState.markToolOutputIndex(payload); err != nil {
				return nil, err
			}
		} else if stringValue(item["type"]) == "message" {
			for index, rawPart := range arrayValue(item["content"]) {
				part := objectValue(rawPart)
				fragments := []string{}
				frames, err := streamState.reconcileTextPart(fmt.Sprintf("%s:content:%d", key, index), firstRawString(part, "text", "refusal"), arrayValue(part["annotations"]), &fragments)
				if err != nil {
					return nil, err
				}
				out = append(out, frames...)
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
	case "response.completed", "response.incomplete":
		if event == "response.completed" && stringValue(responseObject["status"]) != "completed" && (len(streamState.SearchItems) > 0 || hasMeaningfulValue(responseObject["status"])) {
			return nil, fmt.Errorf("Copilot Responses stream ended with an invalid completed response")
		}
		if errFailure := responsesFailure(responseObject); errFailure != nil {
			return nil, errFailure
		}
		if event == "response.incomplete" {
			if stringValue(responseObject["status"]) != "incomplete" || responseObject["error"] != nil || payload["error"] != nil {
				return nil, fmt.Errorf("Copilot Responses stream ended with an invalid incomplete response")
			}
			switch stringValue(objectValue(responseObject["incomplete_details"])["reason"]) {
			case "max_output_tokens", "max_tokens", "content_filter":
			default:
				return nil, fmt.Errorf("Copilot Responses incomplete reason cannot be represented by Messages")
			}
		}
		terminalFrames, errTerminal := streamState.reconcileTerminalOutput(responseObject)
		if errTerminal != nil {
			return nil, errTerminal
		}
		out = append(out, terminalFrames...)
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
	items, ok := response["output"].([]any)
	if !ok {
		return nil, fmt.Errorf("Copilot Responses completed without an output array")
	}
	if err := s.validateTerminalSearches(items); err != nil {
		return nil, err
	}
	if err := s.validateTerminalText(items); err != nil {
		return nil, err
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
				if okPart && (stringValue(part["type"]) == "output_text" || stringValue(part["type"]) == "text" || stringValue(part["type"]) == "refusal") {
					text := firstRawString(part, "text", "refusal")
					contentKey := fmt.Sprintf("%s:content:%d", key, contentIndex)
					idKey := fmt.Sprintf("item:%s:content:%d", rawStringValue(item["id"]), contentIndex)
					if s.Blocks[contentKey] == nil && (s.Blocks[idKey] != nil || s.BufferedText[idKey] != nil || s.TextParts[idKey] != nil) {
						contentKey = idKey
					}
					fragments := append([]string(nil), s.BufferedText[contentKey]...)
					frames, err := s.reconcileTextPart(contentKey, text, arrayValue(part["annotations"]), &fragments)
					if err != nil {
						return nil, err
					}
					out = append(out, frames...)
				}
			}
		case "web_search_call":
			if status := stringValue(response["status"]); status != "completed" && status != "incomplete" {
				return nil, fmt.Errorf("Copilot Responses terminal search has an ambiguous response status")
			}
			toolID := strings.TrimPrefix(rawStringValue(item["id"]), "ws_")
			query := rawStringValue(objectValue(item["action"])["query"])
			use := map[string]any{"type": "server_tool_use", "id": toolID, "name": "web_search", "input": map[string]any{"query": query}}
			result := map[string]any{"type": "web_search_tool_result", "tool_use_id": toolID, "content": item["results"]}
			out = append(out, claudeSSE("content_block_start", map[string]any{"type": "content_block_start", "index": s.NextBlock, "content_block": use}))
			useIndex := s.NextBlock
			s.NextBlock++
			out = append(out, claudeSSE("content_block_stop", map[string]any{"type": "content_block_stop", "index": useIndex}))
			out = append(out, claudeSSE("content_block_start", map[string]any{"type": "content_block_start", "index": s.NextBlock, "content_block": result}))
			resultIndex := s.NextBlock
			s.NextBlock++
			out = append(out, claudeSSE("content_block_stop", map[string]any{"type": "content_block_stop", "index": resultIndex}))
		case "reasoning":
			out = append(out, s.reconcileReasoning(key, item)...)
		case "function_call", "custom_tool_call":
			terminalToolIndexes[outputIndex] = struct{}{}
			if stringValue(response["status"]) == "incomplete" && stringValue(item["type"]) == "function_call" && strings.TrimSpace(rawStringValue(item["arguments"])) == "" {
				return nil, fmt.Errorf("Copilot Responses incomplete tool arguments are missing or empty")
			}
			if stringValue(response["status"]) == "incomplete" && stringValue(item["type"]) == "custom_tool_call" {
				if input, ok := item["input"].(string); ok && strings.TrimSpace(input) == "" {
					return nil, fmt.Errorf("Copilot Responses incomplete custom tool input is empty")
				}
			}
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

func (s *responsesClaudeStreamState) trackBufferedEvent(event string, payload map[string]any) error {
	switch event {
	case "response.content_part.added":
		part := objectValue(payload["part"])
		text := firstRawString(part, "text", "refusal")
		if s.BufferedText == nil {
			s.BufferedText = make(map[string][]string)
		}
		if text != "" {
			s.BufferedText[contentKey(payload)] = append(s.BufferedText[contentKey(payload)], text)
		}
		if s.BufferedCitations == nil {
			s.BufferedCitations = make(map[string][]any)
		}
		s.BufferedCitations[contentKey(payload)] = append(s.BufferedCitations[contentKey(payload)], arrayValue(part["annotations"])...)
	case "response.output_text.delta", "response.refusal.delta":
		if s.BufferedText == nil {
			s.BufferedText = make(map[string][]string)
		}
		if text := rawStringValue(payload["delta"]); text != "" {
			s.BufferedText[contentKey(payload)] = append(s.BufferedText[contentKey(payload)], text)
		}
	case "response.output_text.annotation.added":
		if s.BufferedCitations == nil {
			s.BufferedCitations = make(map[string][]any)
		}
		key := contentKey(payload)
		index := len(s.BufferedCitations[key])
		if block := s.Blocks[key]; block != nil {
			index += len(block.Citations)
		}
		if intValue(payload["annotation_index"]) != index {
			return fmt.Errorf("Copilot Responses buffered citation has no unique text position")
		}
		s.BufferedCitations[key] = append(s.BufferedCitations[key], payload["annotation"])
	case "response.content_part.done":
		return s.trackTextPart(payload)
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		return s.markToolOutputIndex(payload)
	case "response.output_item.added", "response.output_item.done":
		item := objectValue(payload["item"])
		if index, err := strconv.Atoi(numberKey(payload["output_index"])); err == nil && s.SearchItems[index] != nil && stringValue(item["type"]) != "web_search_call" {
			return fmt.Errorf("Copilot Responses search stream changed its item type")
		}
		if stringValue(item["type"]) == "web_search_call" {
			return s.trackSearchItem(event, payload)
		}
		if stringValue(item["type"]) == "function_call" || stringValue(item["type"]) == "custom_tool_call" {
			return s.markToolOutputIndex(payload)
		}
	}
	return nil
}

func (s *responsesClaudeStreamState) trackSearchItem(event string, payload map[string]any) error {
	index, err := strconv.Atoi(numberKey(payload["output_index"]))
	if err != nil || index < 0 {
		return fmt.Errorf("Copilot Responses search stream event has an invalid output index")
	}
	item := objectValue(payload["item"])
	id := rawStringValue(item["id"])
	if payloadID := rawStringValue(payload["item_id"]); payloadID != "" && payloadID != id {
		return fmt.Errorf("Copilot Responses search stream event has conflicting item IDs")
	}
	if s.SearchItems == nil {
		s.SearchItems = make(map[int]*responsesClaudeSearchItem)
	}
	call := s.SearchItems[index]
	if event == "response.output_item.added" {
		if call != nil {
			return fmt.Errorf("Copilot Responses search stream repeated an addition")
		}
		for _, previous := range s.SearchItems {
			if rawStringValue(previous.Added["id"]) == id {
				return fmt.Errorf("Copilot Responses search stream reused an item ID")
			}
		}
		s.SearchItems[index] = &responsesClaudeSearchItem{Added: item}
	} else {
		if call == nil || call.Done != nil || rawStringValue(call.Added["id"]) != id {
			return fmt.Errorf("Copilot Responses search completion has no unique matching addition")
		}
		query := rawStringValue(objectValue(call.Added["action"])["query"])
		if query != "" && query != rawStringValue(objectValue(item["action"])["query"]) {
			return fmt.Errorf("Copilot Responses search completion changed its initial query")
		}
		call.Done = item
	}
	s.BufferAfterSearch = true
	return nil
}

func (s *responsesClaudeStreamState) validateTerminalSearches(items []any) error {
	for index, call := range s.SearchItems {
		if index >= len(items) || call.Done == nil || !reflect.DeepEqual(call.Done, objectValue(items[index])) {
			return fmt.Errorf("Copilot Responses terminal search does not match its streamed completion at output index %d", index)
		}
	}
	return nil
}

func (s *responsesClaudeStreamState) validateTerminalText(items []any) error {
	terminalParts := make(map[string]map[string]any)
	terminalOwners := make(map[string]string)
	var terminalKeys []string
	for index, rawItem := range items {
		item := objectValue(rawItem)
		if stringValue(item["type"]) != "message" {
			continue
		}
		for contentIndex, rawPart := range arrayValue(item["content"]) {
			part := objectValue(rawPart)
			key := fmt.Sprintf("item:%d:content:%d", index, contentIndex)
			terminalParts[key] = part
			terminalOwners[key] = key
			if id := rawStringValue(item["id"]); id != "" {
				idKey := fmt.Sprintf("item:%s:content:%d", id, contentIndex)
				terminalParts[idKey] = part
				terminalOwners[idKey] = key
			}
			if typ := stringValue(part["type"]); typ == "output_text" || typ == "text" || typ == "refusal" {
				terminalKeys = append(terminalKeys, key)
			}
		}
	}
	anonymousKeys := make(map[string]struct{})
	for key, block := range s.Blocks {
		if block.Kind == "text" && strings.HasPrefix(key, "item::content:") {
			anonymousKeys[key] = struct{}{}
		}
	}
	for key := range s.TextParts {
		if strings.HasPrefix(key, "item::content:") {
			anonymousKeys[key] = struct{}{}
		}
	}
	for key := range s.BufferedText {
		if strings.HasPrefix(key, "item::content:") {
			anonymousKeys[key] = struct{}{}
		}
	}
	for key := range s.BufferedCitations {
		if strings.HasPrefix(key, "item::content:") {
			anonymousKeys[key] = struct{}{}
		}
	}
	bindings := make(map[string]string)
	for key := range anonymousKeys {
		prefix := ""
		var citations []any
		if block := s.Blocks[key]; block != nil {
			prefix = block.TextAccumulated
			citations = append(citations, block.Citations...)
		}
		prefix += strings.Join(s.BufferedText[key], "")
		citations = append(citations, s.BufferedCitations[key]...)
		contentIndex := strings.TrimPrefix(key, "item::content:")
		matched := ""
		for _, candidate := range terminalKeys {
			if contentIndex != "" && !strings.HasSuffix(candidate, ":content:"+contentIndex) {
				continue
			}
			part := terminalParts[candidate]
			text := firstRawString(part, "text", "refusal")
			annotations := arrayValue(part["annotations"])
			if !strings.HasPrefix(text, prefix) || len(annotations) < len(citations) || !slices.EqualFunc(annotations[:len(citations)], citations, reflect.DeepEqual) {
				continue
			}
			if completed := s.TextParts[key]; completed != nil && (firstRawString(completed, "text", "refusal") != text || !slices.EqualFunc(arrayValue(completed["annotations"]), annotations, reflect.DeepEqual)) {
				continue
			}
			if block := s.Blocks[key]; block != nil && block.TextFinished && (block.TextAccumulated != text || len(block.Citations) != len(annotations)) {
				continue
			}
			if matched != "" {
				return fmt.Errorf("Copilot Responses anonymous text has ambiguous terminal ownership")
			}
			matched = candidate
		}
		if matched == "" {
			return fmt.Errorf("Copilot Responses anonymous text has no matching terminal part")
		}
		for source, target := range bindings {
			if target == matched && source != key {
				return fmt.Errorf("Copilot Responses anonymous text shares a terminal part")
			}
		}
		for source, owner := range terminalOwners {
			if owner != matched {
				continue
			}
			if s.Blocks[source] != nil || s.TextParts[source] != nil || s.BufferedText[source] != nil || s.BufferedCitations[source] != nil {
				return fmt.Errorf("Copilot Responses anonymous text conflicts with an identified stream part")
			}
		}
		bindings[key] = matched
	}
	for key, matched := range bindings {
		block := s.Blocks[key]
		if block == nil {
			continue
		}
		for index, rawItem := range items {
			if strings.HasPrefix(matched, fmt.Sprintf("item:%d:content:", index)) {
				break
			}
			item := objectValue(rawItem)
			if stringValue(item["type"]) == "message" {
				continue
			}
			previous := s.Blocks[fmt.Sprintf("item:%d", index)]
			if previous == nil && rawStringValue(item["id"]) != "" {
				previous = s.Blocks["item:"+rawStringValue(item["id"])]
			}
			if previous == nil || previous.Open || previous.Index >= block.Index {
				return fmt.Errorf("Copilot Responses anonymous text cannot precede a terminal tool or reasoning item")
			}
		}
		for _, candidate := range terminalKeys {
			if candidate == matched {
				break
			}
			var previous *responsesClaudeBlock
			for source, owner := range terminalOwners {
				if owner == candidate && s.Blocks[source] != nil {
					previous = s.Blocks[source]
				}
			}
			for source, owner := range bindings {
				if owner == candidate && s.Blocks[source] != nil {
					previous = s.Blocks[source]
				}
			}
			if previous == nil || !previous.TextFinished || previous.Index >= block.Index {
				return fmt.Errorf("Copilot Responses anonymous text cannot preserve terminal part ordering")
			}
		}
	}
	for source, target := range bindings {
		if block := s.Blocks[source]; block != nil {
			s.Blocks[target] = block
			delete(s.Blocks, source)
		}
		if part := s.TextParts[source]; part != nil {
			s.TextParts[target] = part
			delete(s.TextParts, source)
		}
		if text, ok := s.BufferedText[source]; ok {
			s.BufferedText[target] = text
			delete(s.BufferedText, source)
		}
		if citations, ok := s.BufferedCitations[source]; ok {
			s.BufferedCitations[target] = citations
			delete(s.BufferedCitations, source)
		}
	}
	for key, part := range s.TextParts {
		terminal := terminalParts[key]
		if terminal == nil || firstRawString(part, "text", "refusal") != firstRawString(terminal, "text", "refusal") || !slices.EqualFunc(arrayValue(part["annotations"]), arrayValue(terminal["annotations"]), reflect.DeepEqual) {
			return fmt.Errorf("Copilot Responses terminal text part does not match its completed stream part")
		}
	}
	for key, block := range s.Blocks {
		if block.Kind != "text" {
			continue
		}
		part := terminalParts[key]
		if part == nil || !strings.HasPrefix(firstRawString(part, "text", "refusal"), block.TextAccumulated) {
			return fmt.Errorf("Copilot Responses terminal text changed its streamed part boundaries")
		}
	}
	for key, streamed := range s.BufferedText {
		prefix := ""
		if block := s.Blocks[key]; block != nil {
			prefix = block.TextAccumulated
		}
		part := terminalParts[key]
		if part == nil || !strings.HasPrefix(firstRawString(part, "text", "refusal"), prefix+strings.Join(streamed, "")) {
			return fmt.Errorf("Copilot Responses terminal text does not match its stream")
		}
	}
	for key, citations := range s.BufferedCitations {
		part := terminalParts[key]
		allCitations := []any{}
		if block := s.Blocks[key]; block != nil {
			allCitations = append(allCitations, block.Citations...)
		}
		allCitations = append(allCitations, citations...)
		terminal := arrayValue(part["annotations"])
		if part == nil || len(terminal) < len(allCitations) || !slices.EqualFunc(terminal[:len(allCitations)], allCitations, reflect.DeepEqual) {
			return fmt.Errorf("Copilot Responses terminal citations do not match their streamed part")
		}
	}
	return nil
}

func (s *responsesClaudeStreamState) trackTextPart(payload map[string]any) error {
	if s.TextParts == nil {
		s.TextParts = make(map[string]map[string]any)
	}
	key := contentKey(payload)
	if s.TextParts[key] != nil {
		return fmt.Errorf("Copilot Responses stream repeated a text part completion")
	}
	s.TextParts[key] = objectValue(payload["part"])
	return nil
}

func (s *responsesClaudeStreamState) reconcileTextPart(key, text string, citations []any, fragments *[]string) ([][]byte, error) {
	target := s.block(key, "text")
	if !strings.HasPrefix(text, target.TextAccumulated) || len(citations) < len(target.Citations) || !slices.EqualFunc(citations[:len(target.Citations)], target.Citations, reflect.DeepEqual) {
		return nil, fmt.Errorf("Copilot Responses completed text or citations do not match their streamed part")
	}
	text = strings.TrimPrefix(text, target.TextAccumulated)
	citations = citations[len(target.Citations):]
	if target.TextFinished {
		if text != "" || len(citations) != 0 {
			return nil, fmt.Errorf("Copilot Responses text changed after its part completion")
		}
		return nil, nil
	}
	out := s.openText(target)
	for text != "" && len(*fragments) > 0 {
		fragment := (*fragments)[0]
		length := min(len(fragment), len(text))
		out = append(out, s.delta(target, "text_delta", "text", fragment[:length])...)
		text = text[length:]
		if length == len(fragment) {
			*fragments = (*fragments)[1:]
		} else {
			(*fragments)[0] = fragment[length:]
		}
	}
	out = append(out, s.delta(target, "text_delta", "text", text)...)
	out = append(out, s.textCitations(target, citations)...)
	out = append(out, s.closeBlock(key)...)
	target.TextFinished = true
	return out, nil
}

func (s *responsesClaudeStreamState) textCitations(target *responsesClaudeBlock, citations []any) [][]byte {
	var out [][]byte
	for _, citation := range citations {
		out = append(out, claudeSSE("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": target.Index,
			"delta": map[string]any{"type": "citations_delta", "citation": citation},
		}))
		target.Citations = append(target.Citations, citation)
	}
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
	key := itemKey(payload)
	index := numberKey(payload["content_index"])
	if key != "item:" && index == "" {
		index = "0"
	}
	return key + ":content:" + index
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
