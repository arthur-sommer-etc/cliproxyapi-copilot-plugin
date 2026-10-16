package translate

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func validateResponsesRequestForTarget(body []byte, target sdktranslator.Format) error {
	if err := validateRequestAttachments(body, sdktranslator.FormatOpenAIResponse, target); err != nil {
		return err
	}
	root, err := decodeObject(body)
	if err != nil {
		return fmt.Errorf("decode Responses request for translation")
	}
	if err := validateResponsesToolsForTarget(root, target); err != nil {
		return err
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
	webSearchIDs := make(map[string]struct{})
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
				id := rawStringValue(item["id"])
				if _, exists := webSearchIDs[id]; exists {
					err = fmt.Errorf("Responses web search history has an ambiguous item ID")
				}
				webSearchIDs[id] = struct{}{}
			} else {
				err = fmt.Errorf("Responses web search history cannot be represented by Chat Completions")
			}
		case "function_call_output", "custom_tool_call_output":
			err = validateResponsesToolOutput(item["output"], target)
		case "additional_tools":
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

func validateResponsesToolsForTarget(root map[string]any, target sdktranslator.Format) error {
	type toolSource struct {
		value any
		label string
	}
	sources := []toolSource{{value: root["tools"], label: "tools"}}
	if input, ok := root["input"].([]any); ok {
		for index, rawItem := range input {
			item, ok := rawItem.(map[string]any)
			if ok && stringValue(item["type"]) == "additional_tools" {
				sources = append(sources, toolSource{value: item["tools"], label: fmt.Sprintf("input.%d.tools", index)})
			}
		}
	}

	webSearchNames := make(map[string]struct{})
	localShell := false
	for _, source := range sources {
		if source.value == nil || !hasMeaningfulValue(source.value) {
			continue
		}
		tools, ok := source.value.([]any)
		if !ok {
			return fmt.Errorf("Responses %s has an unsupported shape", source.label)
		}
		for _, rawTool := range tools {
			tool, ok := rawTool.(map[string]any)
			if !ok {
				if hasMeaningfulValue(rawTool) {
					return fmt.Errorf("Responses %s contains an unsupported tool definition", source.label)
				}
				continue
			}
			typ := strings.TrimSpace(stringValue(tool["type"]))
			representable, valid := NativeToolRepresentable(sdktranslator.FormatOpenAIResponse.String(), endpointForFormat(target), tool)
			if !valid {
				return fmt.Errorf("Responses %s contains an unsupported tool definition", source.label)
			}
			if target == sdktranslator.FormatClaude && (typ == "web_search" || typ == "web_search_preview") {
				if err := validateResponsesWebSearchToolOptions(tool, typ == "web_search_preview"); err != nil {
					return err
				}
			}
			if !representable {
				return fmt.Errorf("Responses native %s tool cannot be represented by %s", typ, responsesTargetName(target))
			}
			switch typ {
			case "web_search", "web_search_preview":
				name := stringValue(tool["name"])
				if name == "" {
					name = "web_search"
				}
				webSearchNames[name] = struct{}{}
			case "shell":
				localShell = true
			}
		}
	}

	return validateResponsesNativeToolChoice(root["tool_choice"], target, webSearchNames, localShell)
}

func responsesTargetName(target sdktranslator.Format) string {
	if target == sdktranslator.FormatClaude {
		return "Claude Messages"
	}
	return "Chat Completions"
}

func endpointForFormat(target sdktranslator.Format) string {
	switch target {
	case sdktranslator.FormatOpenAIResponse:
		return EndpointResponses
	case sdktranslator.FormatOpenAI:
		return EndpointChatCompletions
	case sdktranslator.FormatClaude:
		return EndpointMessages
	default:
		return ""
	}
}

func validateResponsesWebSearchToolOptions(tool map[string]any, preview bool) error {
	for key, value := range tool {
		switch key {
		case "type":
		case "name":
			if _, ok := value.(string); !ok {
				return fmt.Errorf("Responses web search name has an unsupported value")
			}
		case "user_location":
			if _, ok := value.(map[string]any); !ok {
				return fmt.Errorf("Responses web search user_location cannot be represented by Claude Messages")
			}
		case "external_web_access":
			allowed, ok := value.(bool)
			if !ok || !allowed {
				return fmt.Errorf("Responses web search external_web_access=false cannot be represented by Claude Messages")
			}
		case "max_uses":
			if preview {
				return fmt.Errorf("Responses web_search_preview max_uses cannot be represented by Claude Messages")
			}
			count, ok := value.(json.Number)
			if !ok {
				return fmt.Errorf("Responses web search max_uses has an unsupported value")
			}
			parsed, err := strconv.ParseInt(count.String(), 10, 64)
			if err != nil || parsed <= 0 {
				return fmt.Errorf("Responses web search max_uses has an unsupported value")
			}
		case "filters":
			if preview {
				return fmt.Errorf("Responses web_search_preview filters cannot be represented by Claude Messages")
			}
			filters, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("Responses web search filters has an unsupported shape")
			}
			for filter := range filters {
				if filter != "allowed_domains" {
					return fmt.Errorf("Responses web search filter %q cannot be represented by Claude Messages", filter)
				}
				domains, ok := filters[filter].([]any)
				if !ok {
					return fmt.Errorf("Responses web search allowed_domains has an unsupported shape")
				}
				for _, domain := range domains {
					if _, ok := domain.(string); !ok {
						return fmt.Errorf("Responses web search allowed_domains contains an unsupported value")
					}
				}
			}
		default:
			return fmt.Errorf("Responses web search option %q cannot be represented by Claude Messages", key)
		}
	}
	return nil
}

func validateResponsesNativeToolChoice(choice any, target sdktranslator.Format, webSearchNames map[string]struct{}, localShell bool) error {
	if choice == nil || !hasMeaningfulValue(choice) {
		return nil
	}
	choiceString, isString := choice.(string)
	if isString {
		switch choiceString {
		case "auto", "required":
			return nil
		case "none":
			if target == sdktranslator.FormatClaude && len(webSearchNames) > 0 {
				return fmt.Errorf("Responses tool_choice none cannot be preserved for a native web search tool by Claude Messages")
			}
			return nil
		default:
			if len(webSearchNames) > 0 {
				return fmt.Errorf("Responses tool_choice %q cannot be represented by %s", choiceString, responsesTargetName(target))
			}
			return nil
		}
	}
	choiceObject, ok := choice.(map[string]any)
	if !ok {
		return fmt.Errorf("Responses tool_choice has an unsupported shape")
	}
	choiceType := stringValue(choiceObject["type"])
	switch choiceType {
	case "shell":
		if !localShell {
			return fmt.Errorf("Responses native tool_choice %q cannot be represented by %s", choiceType, responsesTargetName(target))
		}
	case "function", "custom":
		name := firstString(choiceObject, "name")
		if choiceType == "function" {
			name = firstString(objectValue(choiceObject["function"]), "name")
			if name == "" {
				name = firstString(choiceObject, "name")
			}
		}
		if _, exists := webSearchNames[name]; exists && name != "" {
			return fmt.Errorf("Responses tool_choice for native web search cannot be represented by %s", responsesTargetName(target))
		}
	case "allowed_tools":
		return fmt.Errorf("Responses allowed_tools tool_choice cannot be represented by %s", responsesTargetName(target))
	default:
		return fmt.Errorf("Responses native tool_choice %q cannot be represented by %s", choiceType, responsesTargetName(target))
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
		if err == nil && hasMeaningfulValue(part["annotations"]) {
			if role != "assistant" || typ != "output_text" && typ != "text" || rawStringValue(part["text"]) == "" {
				err = fmt.Errorf("Responses citations require nonempty assistant text")
			}
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
		if err := validateClaudeCitation(rawAnnotation); err != nil {
			return fmt.Errorf("Responses annotation cannot be replayed by Claude Messages: %w", err)
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
	id, ok := item["id"].(string)
	const prefix = "ws_srvtoolu_"
	if !ok || !strings.HasPrefix(id, prefix) || len(id) == len(prefix) {
		return fmt.Errorf("Responses web search history requires a Claude server tool item ID")
	}
	for _, character := range id[len(prefix):] {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_') {
			return fmt.Errorf("Responses web search history item ID cannot be preserved by Claude Messages")
		}
	}
	for field := range item {
		switch field {
		case "id", "type", "status", "action", "results":
		default:
			return fmt.Errorf("Responses web search history field %q cannot be represented by Claude Messages", field)
		}
	}
	if item["status"] != "completed" {
		return fmt.Errorf("Responses web search history requires a completed search")
	}
	action, ok := item["action"].(map[string]any)
	if !ok || action["type"] != "search" {
		return fmt.Errorf("Responses web search history has an unsupported action")
	}
	for field := range action {
		if field != "type" && field != "query" {
			return fmt.Errorf("Responses web search action field %q cannot be represented by Claude Messages", field)
		}
	}
	if query, ok := action["query"].(string); !ok || strings.TrimSpace(query) == "" {
		return fmt.Errorf("Responses web search history has no query")
	}
	if result, ok := item["results"].(map[string]any); ok {
		if err := rejectFields(result, "type", "error_code"); err != nil || result["type"] != "web_search_tool_result_error" || stringValue(result["error_code"]) == "" {
			return fmt.Errorf("Responses web search results have an unsupported error shape")
		}
		return nil
	}
	entries, ok := item["results"].([]any)
	if !ok {
		return fmt.Errorf("Responses web search results have an unsupported shape")
	}
	for _, rawEntry := range entries {
		entry, ok := rawEntry.(map[string]any)
		if !ok {
			return fmt.Errorf("Responses web search results contain an unsupported entry")
		}
		if entry["type"] == "web_search_tool_result_error" && stringValue(entry["error_code"]) != "" {
			if err := rejectFields(entry, "type", "error_code"); err != nil {
				return fmt.Errorf("Responses web search results have an unsupported error shape")
			}
			continue
		}
		if entry["type"] != "web_search_result" || stringValue(entry["url"]) == "" {
			return fmt.Errorf("Responses web search results contain an unsupported entry")
		}
		if stringValue(entry["encrypted_content"]) == "" {
			return fmt.Errorf("Responses web search result lacks its replay token")
		}
		for _, field := range []string{"title", "page_age", "snippet"} {
			if value, exists := entry[field]; exists {
				if _, ok := value.(string); !ok {
					return fmt.Errorf("Responses web search result field %q has an unsupported value", field)
				}
			}
		}
	}
	return nil
}

func validateResponsesWebSearchProgress(item map[string]any, completed bool) error {
	if completed {
		return validateResponsesWebSearchCall(item)
	}
	id := rawStringValue(item["id"])
	const prefix = "ws_srvtoolu_"
	if !strings.HasPrefix(id, prefix) || len(id) == len(prefix) {
		return fmt.Errorf("Responses web search stream requires a Claude server tool item ID")
	}
	for _, character := range id[len(prefix):] {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_') {
			return fmt.Errorf("Responses web search stream item ID cannot be preserved by Claude Messages")
		}
	}
	if err := rejectFields(item, "id", "type", "status", "action"); err != nil {
		return fmt.Errorf("Responses web search stream item has an unsupported field")
	}
	if stringValue(item["type"]) != "web_search_call" || stringValue(item["status"]) != "in_progress" {
		return fmt.Errorf("Responses web search stream item has an unsupported status")
	}
	action, ok := item["action"].(map[string]any)
	if !ok || action["type"] != "search" {
		return fmt.Errorf("Responses web search stream item has an unsupported action")
	}
	if err := rejectFields(action, "type", "query"); err != nil {
		return fmt.Errorf("Responses web search stream action has an unsupported field")
	}
	if query, exists := action["query"]; exists {
		if _, ok := query.(string); !ok {
			return fmt.Errorf("Responses web search stream query has an unsupported value")
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
	webSearchIDs := make(map[string]struct{})
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
			err = validateResponsesResponseMessage(item, sdktranslator.FormatClaude)
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
			err = validateResponsesWebSearchCall(item)
			id := rawStringValue(item["id"])
			if _, exists := webSearchIDs[id]; exists {
				err = fmt.Errorf("Responses web search output has an ambiguous item ID")
			}
			webSearchIDs[id] = struct{}{}
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
			err = validateResponsesResponseMessage(item, sdktranslator.FormatOpenAI)
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
	if target == sdktranslator.FormatOpenAI {
		if err := validateResponsesOutputForChat(objectValue(payload["response"])); err != nil {
			return err
		}
		for _, rawItem := range arrayValue(objectValue(payload["response"])["output"]) {
			if err := rejectResponsesChatStreamCitations(objectValue(rawItem)); err != nil {
				return err
			}
		}
		if strings.HasPrefix(event, "response.web_search_call.") {
			return fmt.Errorf("Responses web_search_call stream events cannot be represented by Chat")
		}
	}
	validateItem := func(item map[string]any) error {
		root := map[string]any{"output": []any{item}}
		if target == sdktranslator.FormatClaude {
			return validateResponsesOutputForClaude(root)
		}
		if err := rejectResponsesChatStreamCitations(item); err != nil {
			return err
		}
		return validateResponsesOutputForChat(root)
	}
	validatePart := func(part map[string]any) error {
		if target == sdktranslator.FormatOpenAI && hasMeaningfulValue(part["annotations"]) {
			return fmt.Errorf("Responses annotations cannot be represented by Chat streaming")
		}
		return validateResponsesResponseMessage(map[string]any{"content": []any{part}}, target)
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
		if target == sdktranslator.FormatClaude && stringValue(item["type"]) == "web_search_call" {
			return validateResponsesWebSearchProgress(item, event == "response.output_item.done")
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
	case "response.completed", "response.incomplete":
		response := objectValue(payload["response"])
		if target == sdktranslator.FormatClaude {
			return validateResponsesOutputForClaude(response)
		}
		return validateResponsesOutputForChat(response)
	case "response.output_text.annotation.added":
		if !hasMeaningfulValue(payload["annotation"]) {
			return nil
		}
		if target == sdktranslator.FormatClaude {
			return validateClaudeCitation(payload["annotation"])
		}
		return fmt.Errorf("Responses annotations cannot be represented by Chat streaming")
	case "response.created", "response.in_progress", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done", "response.function_call_arguments.delta", "response.custom_tool_call_input.delta", "response.failed", "error", "":
	default:
		if strings.HasPrefix(event, "response.") && hasMeaningfulValue(payload["delta"]) {
			return fmt.Errorf("unsupported nonempty Responses stream event %q", event)
		}
	}
	return nil
}

func validateClaudeStreamFrame(frame []byte, target sdktranslator.Format) error {
	_, data, done, err := parseSSEFrame(frame)
	if err != nil || done || len(data) == 0 {
		return err
	}
	payload, err := decodeObject(data)
	if err != nil {
		return fmt.Errorf("decode Claude Messages stream event")
	}
	if target == sdktranslator.FormatOpenAI {
		if err := validateClaudeHostedStateForChat(payload); err != nil {
			return err
		}
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
			if citations, exists := block["citations"]; exists && hasMeaningfulValue(citations) {
				if rawStringValue(block["text"]) == "" {
					return fmt.Errorf("Claude stream citations require nonempty text")
				}
				if err := validateClaudeCitations(citations); err != nil {
					return err
				}
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

func validateClaudeHostedStateForChat(payload map[string]any) error {
	switch stringValue(payload["type"]) {
	case "content_block_start":
		block := objectValue(payload["content_block"])
		switch stringValue(block["type"]) {
		case "server_tool_use", "web_search_tool_result":
			return fmt.Errorf("Claude %s cannot be represented by Chat", stringValue(block["type"]))
		case "text":
			if hasMeaningfulValue(block["citations"]) {
				return fmt.Errorf("Claude citations cannot be represented by Chat")
			}
		}
	case "content_block_delta":
		delta := objectValue(payload["delta"])
		if stringValue(delta["type"]) == "citations_delta" && hasMeaningfulValue(delta["citation"]) {
			return fmt.Errorf("Claude citations_delta cannot be represented by Chat")
		}
	}
	return nil
}

func validateResponsesResponseMessage(item map[string]any, target sdktranslator.Format) error {
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
		if err := validateResponsesResponseAnnotations(part, target); err != nil {
			return err
		}
		if hasMeaningfulValue(part["annotations"]) {
			if typ != "output_text" && typ != "text" || rawStringValue(text) == "" {
				return fmt.Errorf("Responses citations require nonempty text output")
			}
		}
	}
	return nil
}

func validateResponsesResponseAnnotations(part map[string]any, target sdktranslator.Format) error {
	if target != sdktranslator.FormatOpenAI {
		return validateResponsesAnnotations(part, target)
	}
	value := part["annotations"]
	if !hasMeaningfulValue(value) {
		return nil
	}
	annotations, ok := value.([]any)
	if !ok {
		return fmt.Errorf("Responses annotations have an unsupported shape")
	}
	for _, annotation := range annotations {
		if _, err := responsesURLCitation(annotation); err != nil {
			return err
		}
	}
	return nil
}

func rejectResponsesChatStreamCitations(item map[string]any) error {
	for _, rawPart := range arrayValue(item["content"]) {
		if hasMeaningfulValue(objectValue(rawPart)["annotations"]) {
			return fmt.Errorf("Responses annotations cannot be represented by Chat streaming")
		}
	}
	return nil
}
