package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func claudeMessageResponseToResponses(ctx context.Context, model string, original, translated, body []byte) ([]byte, error) {
	root, err := decodeObject(body)
	if err != nil || stringValue(root["type"]) != "message" {
		return nil, fmt.Errorf("upstream Claude response is not a Messages response")
	}
	frames, err := claudeMessageResponseEvents(root)
	if err != nil {
		return nil, err
	}
	var raw bytes.Buffer
	for _, frame := range frames {
		encoded, err := json.Marshal(frame)
		if err != nil {
			return nil, fmt.Errorf("encode Claude Messages response event")
		}
		raw.WriteString("data: ")
		raw.Write(encoded)
		raw.WriteByte('\n')
	}
	out := registry.TranslateNonStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse, model, original, translated, raw.Bytes(), nil)
	if len(out) == 0 || !json.Valid(out) {
		return nil, fmt.Errorf("official Claude-to-Responses response translation failed")
	}
	return out, nil
}

func claudeMessageResponseEvents(root map[string]any) ([]map[string]any, error) {
	usage := objectValue(root["usage"])
	message := map[string]any{
		"type":  "message",
		"id":    root["id"],
		"model": root["model"],
		"role":  "assistant",
		"usage": usage,
	}
	frames := []map[string]any{{"type": "message_start", "message": message}}
	content := root["content"]
	if text, ok := content.(string); ok {
		content = []any{map[string]any{"type": "text", "text": text}}
	}
	blocks, ok := content.([]any)
	if !ok && hasMeaningfulValue(content) {
		return nil, fmt.Errorf("upstream Claude response content is not a block list")
	}
	serverTools := make(map[string]struct{})
	for index, rawBlock := range blocks {
		block, okBlock := rawBlock.(map[string]any)
		if !okBlock {
			if hasMeaningfulValue(rawBlock) {
				return nil, fmt.Errorf("upstream Claude response contains an unsupported content block")
			}
			continue
		}
		typ := stringValue(block["type"])
		switch typ {
		case "text":
			text, okText := block["text"].(string)
			if !okText {
				return nil, fmt.Errorf("upstream Claude text block has no string value")
			}
			frames = append(frames, claudeBlockEvent("content_block_start", index, map[string]any{"type": "text", "text": ""}))
			if text != "" {
				frames = append(frames, claudeBlockDelta(index, map[string]any{"type": "text_delta", "text": text}))
			}
			for _, citation := range arrayValue(block["citations"]) {
				frames = append(frames, claudeBlockDelta(index, map[string]any{"type": "citations_delta", "citation": citation}))
			}
		case "thinking":
			thinking, okThinking := block["thinking"].(string)
			if !okThinking {
				return nil, fmt.Errorf("upstream Claude thinking block has no string value")
			}
			frames = append(frames, claudeBlockEvent("content_block_start", index, map[string]any{"type": "thinking", "thinking": ""}))
			if thinking != "" {
				frames = append(frames, claudeBlockDelta(index, map[string]any{"type": "thinking_delta", "thinking": thinking}))
			}
			if signature := rawStringValue(block["signature"]); signature != "" {
				frames = append(frames, claudeBlockDelta(index, map[string]any{"type": "signature_delta", "signature": signature}))
			}
		case "redacted_thinking":
			data, okData := block["data"].(string)
			if !okData {
				return nil, fmt.Errorf("upstream Claude redacted reasoning block has no string value")
			}
			frames = append(frames, claudeBlockEvent("content_block_start", index, map[string]any{"type": "redacted_thinking", "data": data}))
		case "tool_use":
			if !validClaudeResponseToolUse(block) {
				return nil, fmt.Errorf("upstream Claude tool block is incomplete")
			}
			frames = append(frames, claudeBlockEvent("content_block_start", index, block))
			encodedInput, err := json.Marshal(block["input"])
			if err != nil {
				return nil, fmt.Errorf("encode Claude tool input")
			}
			frames = append(frames, claudeBlockDelta(index, map[string]any{"type": "input_json_delta", "partial_json": string(encodedInput)}))
		case "server_tool_use":
			if stringValue(block["name"]) != "web_search" || rawStringValue(block["id"]) == "" || !objectMap(block["input"]) {
				return nil, fmt.Errorf("upstream Claude server tool block is unsupported")
			}
			serverTools[rawStringValue(block["id"])] = struct{}{}
			frames = append(frames, claudeBlockEvent("content_block_start", index, block))
		case "web_search_tool_result":
			toolID := rawStringValue(block["tool_use_id"])
			if _, exists := serverTools[toolID]; !exists {
				return nil, fmt.Errorf("upstream Claude web search result has no matching call")
			}
			frames = append(frames, claudeBlockEvent("content_block_start", index, block))
		default:
			if hasMeaningfulClaudePart(block) {
				return nil, fmt.Errorf("upstream Claude response contains an unsupported content block")
			}
			continue
		}
		if typ != "web_search_tool_result" {
			frames = append(frames, claudeBlockEvent("content_block_stop", index, nil))
		}
	}
	frames = append(frames,
		map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": root["stop_reason"], "stop_sequence": root["stop_sequence"]}, "usage": usage},
		map[string]any{"type": "message_stop"},
	)
	return frames, nil
}

func claudeBlockEvent(eventType string, index int, contentBlock map[string]any) map[string]any {
	event := map[string]any{"type": eventType, "index": index}
	if contentBlock != nil {
		event["content_block"] = contentBlock
	}
	return event
}

func claudeBlockDelta(index int, delta map[string]any) map[string]any {
	return map[string]any{"type": "content_block_delta", "index": index, "delta": delta}
}

func validClaudeResponseToolUse(block map[string]any) bool {
	return rawStringValue(block["id"]) != "" && rawStringValue(block["name"]) != "" && objectMap(block["input"])
}

func objectMap(value any) bool {
	_, ok := value.(map[string]any)
	return ok
}
