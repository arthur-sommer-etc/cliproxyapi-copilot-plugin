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
	messages, hasMessages := root["messages"].([]any)
	if rawMessages, exists := root["messages"]; exists && rawMessages != nil && !hasMessages {
		return nil, fmt.Errorf("Claude messages must be an array")
	}
	markerEffort, hasMarkerEffort, err := claudeMessageEffortMarker(messages)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"model":   model,
		"stream":  stream,
		"input":   []any{},
		"store":   false,
		"include": []string{"reasoning.encrypted_content"},
	}
	copyField(out, root, "max_tokens", "max_output_tokens")
	copyField(out, root, "temperature", "temperature")
	copyField(out, root, "top_p", "top_p")
	copyField(out, root, "metadata", "metadata")
	copyField(out, root, "prompt_cache_key", "prompt_cache_key")

	instructions, systemMessage, err := claudeSystemToResponses(root["system"])
	if err != nil {
		return nil, err
	}
	if instructions != "" {
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
			if cacheControl, exists := tool["cache_control"]; exists {
				item["cache_control"] = cacheControl
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
	effort := claudeReasoningEffort(root)
	if _, rootEffortExists := objectValue(root["output_config"])["effort"]; !rootEffortExists && hasMarkerEffort {
		effort = markerEffort
	}
	if effort != "" {
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
	if systemMessage != nil {
		input = append(input, systemMessage)
	}
	for _, rawMessage := range messages {
		message, okMessage := rawMessage.(map[string]any)
		if !okMessage {
			if hasMeaningfulValue(rawMessage) {
				return nil, fmt.Errorf("Claude messages contain an unsupported non-object entry")
			}
			continue
		}
		role := stringValue(message["role"])
		if role == "system" {
			if _, okMarker, errMarker := claudeOutputConfigMarker(message); errMarker != nil {
				return nil, errMarker
			} else if okMarker {
				continue
			}
			return nil, fmt.Errorf("unsupported Claude system message")
		}
		if role != "user" && role != "assistant" {
			if role != "" || hasMeaningfulValue(message["content"]) {
				return nil, fmt.Errorf("unsupported Claude message role %q", role)
			}
			continue
		}
		parts, err := claudeContentPartsStrict(message["content"])
		if err != nil {
			return nil, err
		}
		if cacheControl, exists := message["cache_control"]; exists {
			lastText := -1
			for index := range parts {
				if stringValue(parts[index]["type"]) == "text" {
					lastText = index
				}
			}
			if lastText < 0 {
				return nil, fmt.Errorf("Claude message cache_control has no text block to preserve")
			}
			if _, exists := parts[lastText]["cache_control"]; !exists {
				parts[lastText]["cache_control"] = cacheControl
			}
		}
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
				item := map[string]any{"type": contentType, "text": rawStringValue(part["text"])}
				if cacheControl, exists := part["cache_control"]; exists {
					item["cache_control"] = cacheControl
				}
				pending = append(pending, item)
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
					"summary": []any{map[string]any{"type": "summary_text", "text": rawStringValue(part["thinking"])}},
				}
				if signature := rawStringValue(part["signature"]); signature != "" {
					item["encrypted_content"] = signature
				}
				input = append(input, item)
			case "redacted_thinking":
				flushMessage()
				if data := rawStringValue(part["data"]); data != "" {
					input = append(input, map[string]any{
						"type":              "reasoning",
						"summary":           []any{},
						"encrypted_content": redactedThinkingPrefix + data,
					})
				}
			case "tool_use", "server_tool_use":
				flushMessage()
				toolInput, okInput := part["input"].(map[string]any)
				if !okInput {
					return nil, fmt.Errorf("Claude tool_use input must be an object")
				}
				arguments, errArguments := json.Marshal(toolInput)
				if errArguments != nil {
					return nil, fmt.Errorf("encode Claude tool input: %w", errArguments)
				}
				itemID, callID, errIDs := responsesToolIDsFromClaude(firstRawString(part, "id", "tool_use_id"))
				if errIDs != nil {
					return nil, errIDs
				}
				functionCall := map[string]any{
					"type":      "function_call",
					"call_id":   callID,
					"name":      stringValue(part["name"]),
					"arguments": string(arguments),
				}
				if itemID != "" {
					functionCall["id"] = itemID
				}
				input = append(input, functionCall)
			case "tool_result":
				flushMessage()
				output, errOutput := claudeToolResultOutput(part["content"])
				if errOutput != nil {
					return nil, errOutput
				}
				_, callID, errIDs := responsesToolIDsFromClaude(rawStringValue(part["tool_use_id"]))
				if errIDs != nil {
					return nil, errIDs
				}
				functionOutput := map[string]any{
					"type":    "function_call_output",
					"call_id": callID,
					"output":  output,
				}
				if cacheControl, exists := part["cache_control"]; exists {
					functionOutput["cache_control"] = cacheControl
				}
				input = append(input, functionOutput)
			default:
				if hasMeaningfulClaudePart(part) {
					return nil, fmt.Errorf("unsupported nonempty Claude content block type %q", partType)
				}
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
	if err := validateResponsesOutputForClaude(root); err != nil {
		return nil, err
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
			encrypted := rawStringValue(item["encrypted_content"])
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
		case "function_call":
			arguments := rawStringValue(item["arguments"])
			var input map[string]any
			if strings.TrimSpace(arguments) == "" {
				input = map[string]any{}
			} else if errArguments := json.Unmarshal([]byte(arguments), &input); errArguments != nil {
				return nil, fmt.Errorf("decode Responses tool arguments")
			}
			content = append(content, map[string]any{
				"type":  "tool_use",
				"id":    claudeToolIDFromResponses(item),
				"name":  stringValue(item["name"]),
				"input": input,
			})
			hasToolUse = true
		case "custom_tool_call":
			var input map[string]any
			switch rawInput := item["input"].(type) {
			case string:
				input = map[string]any{"input": rawInput}
			case map[string]any:
				input = rawInput
			default:
				input = map[string]any{}
			}
			content = append(content, map[string]any{
				"type":  "tool_use",
				"id":    claudeToolIDFromResponses(item),
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
	if cached, ok := objectValue(objectValue(root["usage"])["input_tokens_details"])["cached_tokens"]; ok {
		usage["cache_read_input_tokens"] = numberValue(cached)
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
			blocks = append(blocks, rawStringValue(part["text"]))
		}
	}
	return strings.Join(blocks, "\n")
}

func claudeSystemToResponses(value any) (string, any, error) {
	if text, ok := value.(string); ok {
		return text, nil, nil
	}
	if value == nil {
		return "", nil, nil
	}
	blocks, ok := value.([]any)
	if !ok {
		return "", nil, fmt.Errorf("Claude system content must be text or text blocks")
	}
	var texts []string
	content := make([]any, 0, len(blocks))
	hasCacheControl := false
	for _, rawBlock := range blocks {
		block, okBlock := rawBlock.(map[string]any)
		if !okBlock {
			if hasMeaningfulValue(rawBlock) {
				return "", nil, fmt.Errorf("Claude system content contains an unsupported non-object block")
			}
			continue
		}
		if stringValue(block["type"]) != "text" {
			if hasMeaningfulClaudePart(block) {
				return "", nil, fmt.Errorf("unsupported nonempty Claude system block type %q", stringValue(block["type"]))
			}
			continue
		}
		text, okText := block["text"].(string)
		if !okText {
			return "", nil, fmt.Errorf("Claude system text block has no string text")
		}
		texts = append(texts, text)
		item := map[string]any{"type": "input_text", "text": text}
		if cacheControl, exists := block["cache_control"]; exists {
			item["cache_control"] = cacheControl
			hasCacheControl = true
		}
		content = append(content, item)
	}
	if hasCacheControl {
		return "", map[string]any{"type": "message", "role": "system", "content": content}, nil
	}
	return strings.Join(texts, "\n"), nil, nil
}

func claudeReasoningEffort(root map[string]any) string {
	if outputConfig, ok := root["output_config"].(map[string]any); ok {
		switch effort := strings.ToLower(stringValue(outputConfig["effort"])); effort {
		case "low", "medium", "high", "xhigh", "max":
			return effort
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

func claudeMessageEffortMarker(messages []any) (string, bool, error) {
	var effort string
	var found bool
	for _, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok || stringValue(message["role"]) != "system" {
			continue
		}
		markerEffort, okMarker, err := claudeOutputConfigMarker(message)
		if err != nil {
			return "", false, err
		}
		if okMarker {
			effort, found = markerEffort, true
		}
	}
	return effort, found, nil
}

func claudeOutputConfigMarker(message map[string]any) (string, bool, error) {
	if stringValue(message["role"]) != "system" {
		return "", false, nil
	}
	content, okContent := message["content"].([]any)
	config, okConfig := message["output_config"].(map[string]any)
	if !okContent || len(content) != 0 || !okConfig || len(config) != 1 {
		return "", false, fmt.Errorf("unsupported Claude system message")
	}
	if len(message) != 3 {
		return "", false, fmt.Errorf("unsupported Claude system message fields")
	}
	value, exists := config["effort"]
	if !exists {
		return "", false, fmt.Errorf("unsupported Claude system output configuration")
	}
	switch strings.ToLower(rawStringValue(value)) {
	case "none":
		return "", true, nil
	case "low", "medium", "high", "xhigh":
		return strings.ToLower(rawStringValue(value)), true, nil
	case "max":
		return "max", true, nil
	default:
		return "", false, fmt.Errorf("unsupported Claude system reasoning effort")
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

func claudeContentPartsStrict(value any) ([]map[string]any, error) {
	if text, ok := value.(string); ok {
		return []map[string]any{{"type": "text", "text": text}}, nil
	}
	if value == nil {
		return nil, nil
	}
	values, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("Claude content must be text or content blocks")
	}
	parts := make([]map[string]any, 0, len(values))
	for _, rawPart := range values {
		part, okPart := rawPart.(map[string]any)
		if !okPart {
			if hasMeaningfulValue(rawPart) {
				return nil, fmt.Errorf("Claude content contains an unsupported non-object block")
			}
			continue
		}
		parts = append(parts, part)
	}
	return parts, nil
}

func hasMeaningfulClaudePart(part map[string]any) bool {
	for key, value := range part {
		if key != "type" && hasMeaningfulValue(value) {
			return true
		}
	}
	return false
}

func hasMeaningfulValue(value any) bool {
	switch current := value.(type) {
	case nil:
		return false
	case string:
		return current != ""
	case []any:
		return len(current) > 0
	case map[string]any:
		return len(current) > 0
	case bool:
		return current
	default:
		return true
	}
}

func claudeImageToResponses(part map[string]any) (map[string]any, error) {
	source, ok := part["source"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Claude image source is missing")
	}
	switch stringValue(source["type"]) {
	case "base64":
		mediaType := firstNonEmptyString(stringValue(source["media_type"]), "application/octet-stream")
		item := map[string]any{
			"type":      "input_image",
			"image_url": "data:" + mediaType + ";base64," + rawStringValue(source["data"]),
		}
		copyCacheControl(item, part)
		return item, nil
	case "url":
		item := map[string]any{"type": "input_image", "image_url": rawStringValue(source["url"])}
		copyCacheControl(item, part)
		return item, nil
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
		item["file_data"] = "data:" + mediaType + ";base64," + rawStringValue(source["data"])
	case "url":
		item["file_url"] = rawStringValue(source["url"])
	case "text":
		item["file_data"] = rawStringValue(source["data"])
	default:
		return nil, fmt.Errorf("unsupported Claude document source type %q", stringValue(source["type"]))
	}
	if title := rawStringValue(part["title"]); title != "" {
		item["filename"] = title
	}
	copyCacheControl(item, part)
	return item, nil
}

func claudeToolResultOutput(value any) (any, error) {
	if text, ok := value.(string); ok {
		return text, nil
	}
	parts, err := claudeContentPartsStrict(value)
	if err != nil {
		return nil, err
	}
	converted := make([]any, 0)
	for _, part := range parts {
		switch stringValue(part["type"]) {
		case "text":
			item := map[string]any{"type": "input_text", "text": rawStringValue(part["text"])}
			copyCacheControl(item, part)
			converted = append(converted, item)
		case "image":
			image, err := claudeImageToResponses(part)
			if err != nil {
				return nil, err
			}
			converted = append(converted, image)
		case "document":
			document, err := claudeDocumentToResponses(part)
			if err != nil {
				return nil, err
			}
			converted = append(converted, document)
		default:
			if hasMeaningfulClaudePart(part) {
				return nil, fmt.Errorf("unsupported nonempty Claude tool-result block type %q", stringValue(part["type"]))
			}
		}
	}
	if len(converted) == 0 {
		return "", nil
	}
	return converted, nil
}

func copyCacheControl(target, source map[string]any) {
	if cacheControl, exists := source["cache_control"]; exists {
		target["cache_control"] = cacheControl
	}
}

func containsCacheControl(body []byte) bool {
	root, err := decodeObject(body)
	if err != nil {
		return false
	}
	for _, rawTool := range arrayValue(root["tools"]) {
		if _, exists := objectValue(rawTool)["cache_control"]; exists {
			return true
		}
	}
	for _, rawItem := range arrayValue(root["input"]) {
		item := objectValue(rawItem)
		if _, exists := item["cache_control"]; exists || containsCacheControlBlock(item["content"]) || containsCacheControlBlock(item["output"]) {
			return true
		}
	}
	return false
}

func containsCacheControlBlock(value any) bool {
	for _, rawBlock := range arrayValue(value) {
		if _, exists := objectValue(rawBlock)["cache_control"]; exists {
			return true
		}
	}
	return false
}

func stripCacheControlFields(body []byte) ([]byte, error) {
	root, err := decodeObject(body)
	if err != nil {
		return nil, fmt.Errorf("decode translated Responses request")
	}
	stripResponsesRequestCacheControl(root)
	encoded, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("encode normalized Responses request")
	}
	return encoded, nil
}

func stripResponsesRequestCacheControl(root map[string]any) {
	for _, rawTool := range arrayValue(root["tools"]) {
		delete(objectValue(rawTool), "cache_control")
	}
	for _, rawItem := range arrayValue(root["input"]) {
		item := objectValue(rawItem)
		delete(item, "cache_control")
		for _, field := range []string{"content", "output"} {
			for _, rawBlock := range arrayValue(item[field]) {
				delete(objectValue(rawBlock), "cache_control")
			}
		}
	}
}

func responsesReasoningText(item map[string]any) string {
	for _, field := range []string{"summary", "content"} {
		var builder strings.Builder
		for _, rawPart := range arrayValue(item[field]) {
			switch part := rawPart.(type) {
			case string:
				builder.WriteString(part)
			case map[string]any:
				builder.WriteString(rawStringValue(part["text"]))
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
	return fmt.Errorf("Copilot Responses request failed")
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
