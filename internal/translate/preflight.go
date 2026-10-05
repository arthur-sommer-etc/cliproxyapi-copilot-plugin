package translate

import (
	"fmt"
	"strings"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func validateResponsesRequestForTarget(body []byte, target sdktranslator.Format) error {
	root, err := decodeObject(body)
	if err != nil {
		return fmt.Errorf("decode Responses request for translation")
	}
	if previousResponseID, exists := root["previous_response_id"]; exists && previousResponseID != nil {
		value, isString := previousResponseID.(string)
		if !isString || value != "" {
			return fmt.Errorf("Responses previous_response_id cannot be translated to %s", target)
		}
	}
	input := root["input"]
	if input == nil || !hasMeaningfulValue(input) {
		return nil
	}
	if _, ok := input.(string); ok {
		return nil
	}
	items, ok := input.([]any)
	if !ok {
		return fmt.Errorf("Responses input has an unsupported shape")
	}
	for _, rawItem := range items {
		item, okItem := rawItem.(map[string]any)
		if !okItem {
			if hasMeaningfulValue(rawItem) {
				return fmt.Errorf("Responses input contains an unsupported non-object item")
			}
			continue
		}
		typ := stringValue(item["type"])
		if typ == "" && stringValue(item["role"]) != "" {
			typ = "message"
		}
		var err error
		switch typ {
		case "message", "":
			err = validateResponsesMessage(item, target)
		case "reasoning":
			err = validateResponsesReasoning(item)
		case "function_call":
			err = validateResponsesFunctionCall(item)
		case "custom_tool_call":
			if value, exists := item["input"]; exists {
				if _, ok := value.(string); !ok && hasMeaningfulValue(value) {
					err = fmt.Errorf("Responses custom tool input has an unsupported shape")
				}
			}
		case "web_search_call":
			if target == sdktranslator.FormatClaude {
				err = validateResponsesWebSearchCall(item)
			} else {
				err = fmt.Errorf("Responses web search history cannot be represented by Chat Completions")
			}
		case "function_call_output", "custom_tool_call_output":
			err = validateResponsesToolOutput(item["output"], target)
		case "additional_tools":
			if target == sdktranslator.FormatOpenAI && hasMeaningfulValue(item) {
				err = fmt.Errorf("Responses additional_tools items cannot be translated to Chat Completions")
			}
		default:
			if typ != "" || hasMeaningfulValue(item) {
				err = fmt.Errorf("unsupported nonempty Responses input item type %q", typ)
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func validateResponsesMessage(item map[string]any, target sdktranslator.Format) error {
	role := stringValue(item["role"])
	if role != "" && role != "user" && role != "assistant" && role != "system" && role != "developer" {
		return fmt.Errorf("unsupported Responses message role")
	}
	content := item["content"]
	if content == nil || !hasMeaningfulValue(content) {
		return nil
	}
	if _, ok := content.(string); ok {
		return nil
	}
	parts, ok := content.([]any)
	if !ok {
		return fmt.Errorf("Responses message content has an unsupported shape")
	}
	for _, rawPart := range parts {
		part, okPart := rawPart.(map[string]any)
		if !okPart {
			if hasMeaningfulValue(rawPart) {
				return fmt.Errorf("Responses message contains an unsupported non-object content part")
			}
			continue
		}
		typ := stringValue(part["type"])
		if typ == "" {
			typ = "input_text"
		}
		var err error
		switch typ {
		case "input_text", "output_text":
			if _, ok := part["text"].(string); !ok && hasMeaningfulValue(part["text"]) {
				err = fmt.Errorf("Responses text content has an unsupported value")
			}
		case "refusal":
			if _, ok := part["refusal"].(string); !ok && hasMeaningfulValue(part["refusal"]) {
				err = fmt.Errorf("Responses refusal content has an unsupported value")
			}
		case "input_image":
			err = validateResponsesImage(part)
		case "input_file":
			if target == sdktranslator.FormatClaude {
				fileData, ok := part["file_data"].(string)
				if !ok || fileData == "" {
					err = fmt.Errorf("Responses file content requires file_data for Claude Messages")
				}
			}
		case "input_video", "video_url":
			if target != sdktranslator.FormatOpenAI {
				err = fmt.Errorf("Responses video content cannot be represented by Claude Messages")
			}
		default:
			if typ != "" || hasMeaningfulValue(part) {
				err = fmt.Errorf("unsupported nonempty Responses content part type %q", typ)
			}
		}
		if err == nil {
			err = validateResponsesAnnotations(part, target)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func validateResponsesImage(part map[string]any) error {
	url, ok := part["image_url"].(string)
	if !ok {
		url, ok = part["url"].(string)
	}
	if !ok || strings.TrimSpace(url) == "" {
		return fmt.Errorf("Responses image content has no usable URL")
	}
	if strings.HasPrefix(url, "data:") {
		encoded := strings.SplitN(strings.TrimPrefix(url, "data:"), ";base64,", 2)
		if len(encoded) != 2 || encoded[1] == "" {
			return fmt.Errorf("Responses data image is not valid base64 content")
		}
	}
	return nil
}

func validateResponsesAnnotations(part map[string]any, target sdktranslator.Format) error {
	value := part["annotations"]
	if !hasMeaningfulValue(value) {
		return nil
	}
	if target != sdktranslator.FormatClaude {
		return fmt.Errorf("Responses annotations cannot be represented by the selected endpoint")
	}
	annotations, ok := value.([]any)
	if !ok {
		return fmt.Errorf("Responses annotations have an unsupported shape")
	}
	for _, rawAnnotation := range annotations {
		annotation, ok := rawAnnotation.(map[string]any)
		if !ok || stringValue(annotation["encrypted_index"]) == "" {
			return fmt.Errorf("Responses annotation cannot be replayed by Claude Messages")
		}
	}
	return nil
}

func validateResponsesFunctionCall(item map[string]any) error {
	if name, ok := item["name"].(string); !ok || name == "" {
		return fmt.Errorf("Responses function call has no name")
	}
	arguments, exists := item["arguments"]
	if !exists || arguments == "" {
		return nil
	}
	raw, ok := arguments.(string)
	if !ok {
		return fmt.Errorf("Responses function arguments are not a string")
	}
	parsed, err := decodeObject([]byte(raw))
	if err != nil || parsed == nil {
		return fmt.Errorf("Responses function arguments are not a JSON object")
	}
	return nil
}

func validateResponsesReasoning(item map[string]any) error {
	if encrypted, exists := item["encrypted_content"]; exists {
		if _, ok := encrypted.(string); !ok && hasMeaningfulValue(encrypted) {
			return fmt.Errorf("Responses encrypted reasoning has an unsupported value")
		}
	}
	for _, field := range []string{"summary", "content"} {
		value := item[field]
		if value == nil || !hasMeaningfulValue(value) {
			continue
		}
		parts, ok := value.([]any)
		if !ok {
			return fmt.Errorf("Responses reasoning %s has an unsupported shape", field)
		}
		for _, rawPart := range parts {
			part, ok := rawPart.(map[string]any)
			if !ok {
				if hasMeaningfulValue(rawPart) {
					return fmt.Errorf("Responses reasoning contains an unsupported non-object part")
				}
				continue
			}
			typ := stringValue(part["type"])
			if typ != "summary_text" && typ != "reasoning_text" && typ != "text" {
				if typ != "" || hasMeaningfulValue(part) {
					return fmt.Errorf("unsupported nonempty Responses reasoning part type %q", typ)
				}
			}
			if text, exists := part["text"]; exists {
				if _, ok := text.(string); !ok && hasMeaningfulValue(text) {
					return fmt.Errorf("Responses reasoning text has an unsupported value")
				}
			}
			if hasMeaningfulValue(part["annotations"]) {
				return fmt.Errorf("Responses reasoning annotations cannot be represented by the selected endpoint")
			}
		}
	}
	return nil
}

func validateResponsesWebSearchCall(item map[string]any) error {
	if _, ok := item["id"].(string); !ok || stringValue(item["id"]) == "" {
		return fmt.Errorf("Responses web search history has no item ID")
	}
	action, ok := item["action"].(map[string]any)
	if !ok {
		return fmt.Errorf("Responses web search history has an unsupported action")
	}
	query := stringValue(action["query"])
	if query == "" {
		query = stringValue(action["url"])
	}
	if query == "" {
		if queries, ok := action["queries"].([]any); ok && len(queries) > 0 {
			query = stringValue(queries[0])
		}
	}
	if query == "" {
		return fmt.Errorf("Responses web search history has no query")
	}
	if results, exists := item["results"]; exists && hasMeaningfulValue(results) {
		entries, ok := results.([]any)
		if !ok {
			return fmt.Errorf("Responses web search results have an unsupported shape")
		}
		for _, rawEntry := range entries {
			entry, okEntry := rawEntry.(map[string]any)
			if !okEntry {
				if hasMeaningfulValue(rawEntry) {
					return fmt.Errorf("Responses web search results contain an unsupported entry")
				}
				continue
			}
			if stringValue(entry["type"]) != "web_search_tool_result_error" && stringValue(entry["encrypted_content"]) == "" {
				return fmt.Errorf("Responses web search result lacks its replay token")
			}
		}
	}
	return nil
}

func validateResponsesToolOutput(value any, target sdktranslator.Format) error {
	if value == nil || !hasMeaningfulValue(value) {
		return nil
	}
	if _, ok := value.(string); ok {
		return nil
	}
	parts, ok := value.([]any)
	if !ok {
		return nil
	}
	for _, rawPart := range parts {
		part, okPart := rawPart.(map[string]any)
		if !okPart {
			if _, ok := rawPart.(string); ok {
				continue
			}
			if hasMeaningfulValue(rawPart) {
				return fmt.Errorf("Responses tool output contains an unsupported non-object block")
			}
			continue
		}
		typ := stringValue(part["type"])
		if typ == "" {
			if _, ok := part["text"].(string); ok {
				continue
			}
			if hasMeaningfulValue(part) {
				return fmt.Errorf("Responses tool output contains an untyped block")
			}
			continue
		}
		switch typ {
		case "input_text", "output_text", "text":
			if _, ok := part["text"].(string); !ok && hasMeaningfulValue(part["text"]) {
				return fmt.Errorf("Responses tool output text has an unsupported value")
			}
		case "input_image", "image_url":
			if target == sdktranslator.FormatClaude {
				if typ == "image_url" {
					return fmt.Errorf("Chat image_url output cannot be represented by Claude Messages")
				}
				if err := validateResponsesImage(part); err != nil {
					return err
				}
			} else if typ == "image_url" {
				image := objectValue(part["image_url"])
				if url, ok := image["url"].(string); !ok || strings.TrimSpace(url) == "" {
					return fmt.Errorf("Responses tool output image cannot be represented by Chat Completions")
				}
			} else if err := validateResponsesImage(part); err != nil {
				return err
			}
		case "input_file":
			if target == sdktranslator.FormatClaude {
				if fileData, ok := part["file_data"].(string); !ok || fileData == "" {
					return fmt.Errorf("Responses tool output file requires file_data for Claude Messages")
				}
			} else {
				return fmt.Errorf("Responses tool output files cannot be represented by Chat Completions")
			}
		default:
			return fmt.Errorf("unsupported nonempty Responses tool output block type %q", typ)
		}
		if hasMeaningfulValue(part["annotations"]) {
			return fmt.Errorf("Responses tool output annotations cannot be represented by the selected endpoint")
		}
	}
	return nil
}

func validateResponsesOutputForClaude(root map[string]any) error {
	output, exists := root["output"]
	if !exists || output == nil {
		return nil
	}
	items, ok := output.([]any)
	if !ok {
		if hasMeaningfulValue(output) {
			return fmt.Errorf("Responses output has an unsupported shape")
		}
		return nil
	}
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			if hasMeaningfulValue(rawItem) {
				return fmt.Errorf("Responses output contains an unsupported non-object item")
			}
			continue
		}
		typ := stringValue(item["type"])
		var err error
		switch typ {
		case "message":
			err = validateClaudeResponseMessage(item)
		case "reasoning":
			err = validateResponsesReasoning(item)
		case "function_call":
			err = validateResponsesFunctionCall(item)
		case "custom_tool_call":
			if name, ok := item["name"].(string); !ok || name == "" {
				err = fmt.Errorf("Responses custom tool call has no name")
			}
			if input, exists := item["input"]; exists {
				_, isString := input.(string)
				_, isObject := input.(map[string]any)
				if !isString && !isObject && hasMeaningfulValue(input) {
					err = fmt.Errorf("Responses custom tool input is not a string")
				}
			}
		case "web_search_call":
			err = fmt.Errorf("Responses web search output cannot be represented by this Claude response adapter")
		case "function_call_output", "custom_tool_call_output":
			err = fmt.Errorf("Responses tool output item cannot appear in an assistant response")
		default:
			if typ != "" || hasMeaningfulValue(item) {
				err = fmt.Errorf("unsupported nonempty Responses output item type %q", typ)
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func validateResponsesOutputForChat(root map[string]any) error {
	output, exists := root["output"]
	if !exists || output == nil {
		return nil
	}
	items, ok := output.([]any)
	if !ok {
		if hasMeaningfulValue(output) {
			return fmt.Errorf("Responses output has an unsupported shape")
		}
		return nil
	}
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			if hasMeaningfulValue(rawItem) {
				return fmt.Errorf("Responses output contains an unsupported non-object item")
			}
			continue
		}
		typ := stringValue(item["type"])
		var err error
		switch typ {
		case "message":
			err = validateClaudeResponseMessage(item)
		case "reasoning":
			err = validateResponsesReasoning(item)
		case "function_call":
			err = validateResponsesFunctionCall(item)
		case "custom_tool_call":
			if input, exists := item["input"]; exists {
				if _, ok := input.(string); !ok && hasMeaningfulValue(input) {
					err = fmt.Errorf("Responses custom tool input is not a string")
				}
			}
		case "function_call_output", "custom_tool_call_output":
			err = fmt.Errorf("Responses tool output item cannot appear in an assistant response")
		default:
			if typ != "" || hasMeaningfulValue(item) {
				err = fmt.Errorf("unsupported nonempty Responses output item type %q", typ)
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func validateResponsesStreamFrame(frame []byte, target sdktranslator.Format) error {
	event, data, done, err := parseSSEFrame(frame)
	if err != nil || done || len(data) == 0 {
		return err
	}
	payload, err := decodeObject(data)
	if err != nil {
		return fmt.Errorf("decode Responses stream event")
	}
	if event == "" {
		event = stringValue(payload["type"])
	}
	validateItem := func(item map[string]any) error {
		root := map[string]any{"output": []any{item}}
		if target == sdktranslator.FormatClaude {
			return validateResponsesOutputForClaude(root)
		}
		return validateResponsesOutputForChat(root)
	}
	validatePart := func(part map[string]any) error {
		return validateClaudeResponseMessage(map[string]any{"content": []any{part}})
	}
	switch event {
	case "response.output_item.added", "response.output_item.done":
		item, ok := payload["item"].(map[string]any)
		if !ok {
			if hasMeaningfulValue(payload["item"]) {
				return fmt.Errorf("Responses stream contains an unsupported output item")
			}
			return nil
		}
		return validateItem(item)
	case "response.content_part.added", "response.content_part.done":
		part, ok := payload["part"].(map[string]any)
		if !ok {
			if hasMeaningfulValue(payload["part"]) {
				return fmt.Errorf("Responses stream contains an unsupported content part")
			}
			return nil
		}
		return validatePart(part)
	case "response.output_text.delta", "response.refusal.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if _, ok := payload["delta"].(string); !ok && hasMeaningfulValue(payload["delta"]) {
			return fmt.Errorf("Responses stream contains an unsupported text delta")
		}
	case "response.completed":
		response := objectValue(payload["response"])
		if target == sdktranslator.FormatClaude {
			return validateResponsesOutputForClaude(response)
		}
		return validateResponsesOutputForChat(response)
	case "response.created", "response.in_progress", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done", "response.function_call_arguments.delta", "response.custom_tool_call_input.delta", "response.failed", "response.incomplete", "error", "":
	default:
		if strings.HasPrefix(event, "response.") && hasMeaningfulValue(payload["delta"]) {
			return fmt.Errorf("unsupported nonempty Responses stream event %q", event)
		}
	}
	return nil
}

func validateClaudeStreamFrame(frame []byte) error {
	_, data, done, err := parseSSEFrame(frame)
	if err != nil || done || len(data) == 0 {
		return err
	}
	payload, err := decodeObject(data)
	if err != nil {
		return fmt.Errorf("decode Claude Messages stream event")
	}
	switch stringValue(payload["type"]) {
	case "content_block_start":
		block, ok := payload["content_block"].(map[string]any)
		if !ok {
			if hasMeaningfulValue(payload["content_block"]) {
				return fmt.Errorf("Claude stream contains an unsupported content block")
			}
			return nil
		}
		typ := stringValue(block["type"])
		switch typ {
		case "text":
			if _, ok := block["text"].(string); !ok && hasMeaningfulValue(block["text"]) {
				return fmt.Errorf("Claude stream text block has an unsupported value")
			}
		case "thinking":
			if _, ok := block["thinking"].(string); !ok && hasMeaningfulValue(block["thinking"]) {
				return fmt.Errorf("Claude stream thinking block has an unsupported value")
			}
		case "redacted_thinking":
			if _, ok := block["data"].(string); !ok && hasMeaningfulValue(block["data"]) {
				return fmt.Errorf("Claude stream redacted reasoning has an unsupported value")
			}
		case "tool_use":
			if !validClaudeResponseToolUse(block) {
				return fmt.Errorf("Claude stream tool block is incomplete")
			}
		case "server_tool_use":
			if stringValue(block["name"]) != "web_search" || rawStringValue(block["id"]) == "" || !objectMap(block["input"]) {
				return fmt.Errorf("Claude stream server tool block is unsupported")
			}
		case "web_search_tool_result":
			if rawStringValue(block["tool_use_id"]) == "" {
				return fmt.Errorf("Claude stream web search result has no call ID")
			}
		default:
			return fmt.Errorf("unsupported Claude stream content block type %q", typ)
		}
	case "content_block_delta":
		delta, ok := payload["delta"].(map[string]any)
		if !ok {
			if hasMeaningfulValue(payload["delta"]) {
				return fmt.Errorf("Claude stream contains an unsupported content delta")
			}
			return nil
		}
		typ := stringValue(delta["type"])
		var field string
		switch typ {
		case "text_delta":
			field = "text"
		case "thinking_delta":
			field = "thinking"
		case "signature_delta":
			field = "signature"
		case "input_json_delta":
			field = "partial_json"
		case "citations_delta":
			if hasMeaningfulValue(delta["citation"]) {
				return nil
			}
			return fmt.Errorf("Claude citation delta has no citation")
		default:
			return fmt.Errorf("unsupported Claude stream content delta type %q", typ)
		}
		if _, ok := delta[field].(string); !ok && hasMeaningfulValue(delta[field]) {
			return fmt.Errorf("Claude stream content delta has an unsupported value")
		}
	}
	return nil
}

func validateClaudeResponseMessage(item map[string]any) error {
	content := item["content"]
	if content == nil || !hasMeaningfulValue(content) {
		return nil
	}
	parts, ok := content.([]any)
	if !ok {
		return fmt.Errorf("Responses message output content has an unsupported shape")
	}
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			if hasMeaningfulValue(rawPart) {
				return fmt.Errorf("Responses message output contains an unsupported non-object part")
			}
			continue
		}
		typ := stringValue(part["type"])
		var text any
		switch typ {
		case "output_text", "text":
			text = part["text"]
		case "refusal":
			text = part["refusal"]
		default:
			if typ != "" || hasMeaningfulValue(part) {
				return fmt.Errorf("unsupported nonempty Responses message output part type %q", typ)
			}
		}
		if text != nil {
			if _, ok := text.(string); !ok && hasMeaningfulValue(text) {
				return fmt.Errorf("Responses message output text has an unsupported value")
			}
		}
		if hasMeaningfulValue(part["annotations"]) {
			return fmt.Errorf("Responses output annotations cannot be represented by this endpoint")
		}
	}
	return nil
}
