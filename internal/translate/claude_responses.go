package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const redactedThinkingPrefix = "claude-redacted-thinking:"

func claudeRequestToResponses(model string, body []byte, stream bool) ([]byte, error) {
	root, err := decodeObject(body)
	if err != nil {
		return nil, fmt.Errorf("decode Claude request: %w", err)
	}
	out := map[string]any{
		"model":  model,
		"stream": stream,
		"input":  []any{},
	}
	copyField(out, root, "max_tokens", "max_output_tokens")
	copyField(out, root, "temperature", "temperature")
	copyField(out, root, "top_p", "top_p")
	copyField(out, root, "metadata", "metadata")

	if instructions := claudeSystemText(root["system"]); instructions != "" {
		out["instructions"] = instructions
	}
	if tools, ok := root["tools"].([]any); ok {
		converted := make([]any, 0, len(tools))
		for _, rawTool := range tools {
			tool, okTool := rawTool.(map[string]any)
			if !okTool || stringValue(tool["name"]) == "" {
				continue
			}
			item := map[string]any{
				"type":       "function",
				"name":       stringValue(tool["name"]),
				"parameters": objectValue(tool["input_schema"]),
			}
			if description := stringValue(tool["description"]); description != "" {
				item["description"] = description
			}
			converted = append(converted, item)
		}
		if len(converted) > 0 {
			out["tools"] = converted
		}
	}
	if choice, ok := root["tool_choice"].(map[string]any); ok {
		switch stringValue(choice["type"]) {
		case "auto":
			out["tool_choice"] = "auto"
		case "any":
			out["tool_choice"] = "required"
		case "none":
			out["tool_choice"] = "none"
		case "tool":
			if name := stringValue(choice["name"]); name != "" {
				out["tool_choice"] = map[string]any{"type": "function", "name": name}
			}
		}
	}
	if effort := claudeReasoningEffort(root); effort != "" {
		out["reasoning"] = map[string]any{"effort": effort, "summary": "auto"}
	}
	if outputConfig, ok := root["output_config"].(map[string]any); ok {
		if format, okFormat := outputConfig["format"].(map[string]any); okFormat {
			responsesFormat := make(map[string]any, len(format)+1)
			for key, value := range format {
				responsesFormat[key] = value
			}
			if stringValue(responsesFormat["type"]) == "json_schema" && stringValue(responsesFormat["name"]) == "" {
				responsesFormat["name"] = "claude_structured_output"
			}
			out["text"] = map[string]any{"format": responsesFormat}
		}
	}

	input := make([]any, 0)
	messages, _ := root["messages"].([]any)
	for _, rawMessage := range messages {
		message, okMessage := rawMessage.(map[string]any)
		if !okMessage {
			continue
		}
		role := stringValue(message["role"])
		if role != "user" && role != "assistant" {
			continue
		}
		parts := claudeContentParts(message["content"])
		pending := make([]any, 0)
		flushMessage := func() {
			if len(pending) == 0 {
				return
			}
			input = append(input, map[string]any{
				"type":    "message",
				"role":    role,
				"content": pending,
			})
			pending = nil
		}
		for _, part := range parts {
			partType := stringValue(part["type"])
			switch partType {
			case "text":
				contentType := "input_text"
				if role == "assistant" {
					contentType = "output_text"
				}
				pending = append(pending, map[string]any{"type": contentType, "text": stringValue(part["text"])})
			case "image":
				image, errImage := claudeImageToResponses(part)
				if errImage != nil {
					return nil, errImage
				}
				pending = append(pending, image)
			case "document":
				document, errDocument := claudeDocumentToResponses(part)
				if errDocument != nil {
					return nil, errDocument
				}
				pending = append(pending, document)
			case "thinking":
				flushMessage()
				item := map[string]any{
					"type":    "reasoning",
					"summary": []any{map[string]any{"type": "summary_text", "text": stringValue(part["thinking"])}},
				}
				if signature := stringValue(part["signature"]); signature != "" {
					item["encrypted_content"] = signature
				}
				input = append(input, item)
			case "redacted_thinking":
				flushMessage()
				if data := stringValue(part["data"]); data != "" {
					input = append(input, map[string]any{
						"type":              "reasoning",
						"summary":           []any{},
						"encrypted_content": redactedThinkingPrefix + data,
					})
				}
			case "tool_use", "server_tool_use":
				flushMessage()
				arguments, errArguments := json.Marshal(objectValue(part["input"]))
				if errArguments != nil {
					return nil, fmt.Errorf("encode Claude tool input: %w", errArguments)
				}
				input = append(input, map[string]any{
					"type":      "function_call",
					"call_id":   firstString(part, "id", "tool_use_id"),
					"name":      stringValue(part["name"]),
					"arguments": string(arguments),
				})
			case "tool_result":
				flushMessage()
				output, errOutput := claudeToolResultOutput(part["content"])
				if errOutput != nil {
					return nil, errOutput
				}
				input = append(input, map[string]any{
					"type":    "function_call_output",
					"call_id": stringValue(part["tool_use_id"]),
					"output":  output,
				})
			}
		}
		flushMessage()
	}
	out["input"] = input
	return json.Marshal(out)
}

func responsesResponseToClaude(model string, body []byte) ([]byte, error) {
	root, err := decodeObject(body)
	if err != nil {
		return nil, fmt.Errorf("decode Responses response: %w", err)
	}
	if errResponse := responsesFailure(root); errResponse != nil {
		return nil, errResponse
	}
	content := make([]any, 0)
	hasToolUse := false
	for _, rawItem := range arrayValue(root["output"]) {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		switch stringValue(item["type"]) {
		case "message":
			for _, rawPart := range arrayValue(item["content"]) {
				part, okPart := rawPart.(map[string]any)
				if !okPart {
					continue
				}
				switch stringValue(part["type"]) {
				case "output_text", "text", "refusal":
					content = append(content, map[string]any{"type": "text", "text": firstString(part, "text", "refusal")})
				}
			}
		case "reasoning":
			thinking := responsesReasoningText(item)
			encrypted := stringValue(item["encrypted_content"])
			if strings.HasPrefix(encrypted, redactedThinkingPrefix) {
				content = append(content, map[string]any{
					"type": "redacted_thinking",
					"data": strings.TrimPrefix(encrypted, redactedThinkingPrefix),
				})
			} else if thinking != "" || encrypted != "" {
				block := map[string]any{"type": "thinking", "thinking": thinking}
				if encrypted != "" {
					block["signature"] = encrypted
				}
				content = append(content, block)
			}
		case "function_call", "custom_tool_call":
			arguments := firstString(item, "arguments", "input")
			var input map[string]any
			if strings.TrimSpace(arguments) == "" {
				input = map[string]any{}
			} else if errArguments := json.Unmarshal([]byte(arguments), &input); errArguments != nil {
				return nil, fmt.Errorf("decode Responses tool arguments: %w", errArguments)
			}
			content = append(content, map[string]any{
				"type":  "tool_use",
				"id":    firstString(item, "call_id", "id"),
				"name":  stringValue(item["name"]),
				"input": input,
			})
			hasToolUse = true
		}
	}
	stopReason := responsesStopReason(root, hasToolUse)
	usage := map[string]any{
		"input_tokens":  numberValue(objectValue(root["usage"])["input_tokens"]),
		"output_tokens": numberValue(objectValue(root["usage"])["output_tokens"]),
	}
	if cached := numberValue(objectValue(objectValue(root["usage"])["input_tokens_details"])["cached_tokens"]); cached != nil {
		usage["cache_read_input_tokens"] = cached
	}
	out := map[string]any{
		"id":            firstString(root, "id"),
		"type":          "message",
		"role":          "assistant",
		"model":         firstNonEmptyString(stringValue(root["model"]), model),
		"content":       content,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage":         usage,
	}
	return json.Marshal(out)
}

func claudeSystemText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	var blocks []string
	for _, rawPart := range arrayValue(value) {
		part, ok := rawPart.(map[string]any)
		if ok && stringValue(part["type"]) == "text" {
			blocks = append(blocks, stringValue(part["text"]))
		}
	}
	return strings.Join(blocks, "\n")
}

func claudeReasoningEffort(root map[string]any) string {
	if outputConfig, ok := root["output_config"].(map[string]any); ok {
		switch effort := strings.ToLower(stringValue(outputConfig["effort"])); effort {
		case "low", "medium", "high":
			return effort
		case "max":
			return "high"
		}
	}
	thinking, _ := root["thinking"].(map[string]any)
	if stringValue(thinking["type"]) != "enabled" && stringValue(thinking["type"]) != "adaptive" {
		return ""
	}
	budget := int64Value(thinking["budget_tokens"])
	switch {
	case budget == 0:
		return "medium"
	case budget <= 2048:
		return "low"
	case budget <= 8192:
		return "medium"
	default:
		return "high"
	}
}

func claudeContentParts(value any) []map[string]any {
	if text, ok := value.(string); ok {
		return []map[string]any{{"type": "text", "text": text}}
	}
	var out []map[string]any
	for _, rawPart := range arrayValue(value) {
		if part, ok := rawPart.(map[string]any); ok {
			out = append(out, part)
		}
	}
	return out
}

func claudeImageToResponses(part map[string]any) (map[string]any, error) {
	source, ok := part["source"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Claude image source is missing")
	}
	switch stringValue(source["type"]) {
	case "base64":
		mediaType := firstNonEmptyString(stringValue(source["media_type"]), "application/octet-stream")
		return map[string]any{
			"type":      "input_image",
			"image_url": "data:" + mediaType + ";base64," + stringValue(source["data"]),
		}, nil
	case "url":
		return map[string]any{"type": "input_image", "image_url": stringValue(source["url"])}, nil
	default:
		return nil, fmt.Errorf("unsupported Claude image source type %q", stringValue(source["type"]))
	}
}

func claudeDocumentToResponses(part map[string]any) (map[string]any, error) {
	source, ok := part["source"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Claude document source is missing")
	}
	item := map[string]any{"type": "input_file"}
	switch stringValue(source["type"]) {
	case "base64":
		mediaType := firstNonEmptyString(stringValue(source["media_type"]), "application/octet-stream")
		item["file_data"] = "data:" + mediaType + ";base64," + stringValue(source["data"])
	case "url":
		item["file_url"] = stringValue(source["url"])
	case "text":
		item["file_data"] = stringValue(source["data"])
	default:
		return nil, fmt.Errorf("unsupported Claude document source type %q", stringValue(source["type"]))
	}
	if title := stringValue(part["title"]); title != "" {
		item["filename"] = title
	}
	return item, nil
}

func claudeToolResultOutput(value any) (any, error) {
	if text, ok := value.(string); ok {
		return text, nil
	}
	parts := make([]any, 0)
	for _, part := range claudeContentParts(value) {
		switch stringValue(part["type"]) {
		case "text":
			parts = append(parts, map[string]any{"type": "input_text", "text": stringValue(part["text"])})
		case "image":
			image, err := claudeImageToResponses(part)
			if err != nil {
				return nil, err
			}
			parts = append(parts, image)
		}
	}
	if len(parts) == 0 {
		return "", nil
	}
	return parts, nil
}

func responsesReasoningText(item map[string]any) string {
	for _, field := range []string{"summary", "content"} {
		var builder strings.Builder
		for _, rawPart := range arrayValue(item[field]) {
			switch part := rawPart.(type) {
			case string:
				builder.WriteString(part)
			case map[string]any:
				builder.WriteString(stringValue(part["text"]))
			}
		}
		if builder.Len() > 0 {
			return builder.String()
		}
	}
	return ""
}

func responsesFailure(root map[string]any) error {
	status := strings.ToLower(stringValue(root["status"]))
	if status != "failed" && status != "cancelled" {
		return nil
	}
	errorObject := objectValue(root["error"])
	message := firstNonEmptyString(stringValue(errorObject["message"]), stringValue(root["error"]), "unknown upstream error")
	return fmt.Errorf("Copilot Responses request %s: %s", status, message)
}

func responsesStopReason(root map[string]any, hasToolUse bool) string {
	reason := strings.ToLower(stringValue(objectValue(root["incomplete_details"])["reason"]))
	switch reason {
	case "max_output_tokens", "max_tokens":
		return "max_tokens"
	case "content_filter":
		return "refusal"
	}
	if hasToolUse {
		return "tool_use"
	}
	return "end_turn"
}

func decodeObject(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var out map[string]any
	if err := decoder.Decode(&out); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("expected a JSON object")
	}
	return out, nil
}

func copyField(out, in map[string]any, source, destination string) {
	if value, ok := in[source]; ok {
		out[destination] = value
	}
}

func arrayValue(value any) []any {
	out, _ := value.([]any)
	return out
}

func objectValue(value any) map[string]any {
	out, _ := value.(map[string]any)
	if out == nil {
		return map[string]any{}
	}
	return out
}

func stringValue(value any) string {
	valueString, _ := value.(string)
	return strings.TrimSpace(valueString)
}

func rawStringValue(value any) string {
	valueString, _ := value.(string)
	return valueString
}

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringValue(values[key]); value != "" {
			return value
		}
	}
	return ""
}

func firstRawString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := rawStringValue(values[key]); value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func numberValue(value any) any {
	switch value.(type) {
	case json.Number, float64, float32, int, int32, int64, uint, uint32, uint64:
		return value
	default:
		return 0
	}
}

func int64Value(value any) int64 {
	switch number := value.(type) {
	case json.Number:
		result, _ := number.Int64()
		return result
	case float64:
		return int64(number)
	case int64:
		return number
	case int:
		return int64(number)
	default:
		return 0
	}
}
