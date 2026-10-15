package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type responsesClaudeReasoningPayload struct {
	marker       string
	encrypted    string
	thinking     string
	redacted     bool
	redactedData string
}

type responsesClaudeToolPayload struct {
	marker     string
	claudeID   string
	callID     string
	outputSeen bool
}

func responsesRequestToClaude(model string, body []byte, stream bool) ([]byte, error) {
	input := gjson.GetBytes(body, "input")
	temporary := body
	reasoningRestore := make(map[string]responsesClaudeReasoningPayload)
	toolRestore := make(map[string]responsesClaudeToolPayload)
	toolCallsByCallID := make(map[string]responsesClaudeToolPayload)
	toolCallsByClaudeID := make(map[string]struct{})
	webSearchRestore := make(map[string]gjson.Result)
	citationRestore := make(map[string]json.RawMessage)
	if input.IsArray() {
		for index, item := range input.Array() {
			switch item.Get("type").String() {
			case "web_search_call":
				webSearchRestore[strings.TrimPrefix(item.Get("id").String(), "ws_")] = item
			case "reasoning":
				encrypted := item.Get("encrypted_content")
				if encrypted.Type != gjson.String || encrypted.String() == "" {
					continue
				}
				payload := responsesClaudeReasoningPayload{encrypted: encrypted.String()}
				if strings.HasPrefix(payload.encrypted, redactedThinkingPrefix) {
					payload.redacted = true
					payload.redactedData = strings.TrimPrefix(payload.encrypted, redactedThinkingPrefix)
				} else {
					itemObject, err := decodeObject([]byte(item.Raw))
					if err != nil {
						return nil, fmt.Errorf("decode Responses reasoning item for Claude translation")
					}
					payload.thinking = responsesReasoningText(itemObject)
				}
				payload.marker = temporaryResponsesMarker(body, "reasoning", index)
				reasoningRestore[payload.marker] = payload
				updated, err := sjson.SetBytes(temporary, fmt.Sprintf("input.%d.encrypted_content", index), redactedThinkingPrefix+payload.marker)
				if err != nil {
					return nil, fmt.Errorf("prepare Responses reasoning for Claude translation")
				}
				temporary = updated
			case "function_call", "custom_tool_call":
				payload, ok, err := responsesClaudeToolPayloadFromItem(item)
				if err != nil {
					return nil, err
				}
				if !ok {
					continue
				}
				if _, exists := toolCallsByCallID[payload.callID]; exists {
					return nil, fmt.Errorf("Responses tool call identity is ambiguous for Claude translation")
				}
				if _, exists := toolCallsByClaudeID[payload.claudeID]; exists {
					return nil, fmt.Errorf("Responses tool call identity is ambiguous for Claude translation")
				}
				payload.marker = temporaryResponsesMarker(body, "tool", index)
				toolCallsByCallID[payload.callID] = payload
				toolCallsByClaudeID[payload.claudeID] = struct{}{}
				toolRestore[payload.marker] = payload
				updated, err := sjson.SetBytes(temporary, fmt.Sprintf("input.%d.call_id", index), payload.marker)
				if err != nil {
					return nil, fmt.Errorf("prepare Responses tool identity for Claude translation")
				}
				temporary = updated
			}
		}
		for index, item := range input.Array() {
			typ := item.Get("type").String()
			if typ != "function_call_output" && typ != "custom_tool_call_output" {
				continue
			}
			callID := responsesRequestItemCallID(item)
			payload, exists := toolCallsByCallID[callID]
			if !exists {
				continue
			}
			if payload.outputSeen {
				return nil, fmt.Errorf("Responses tool output identity is ambiguous for Claude translation")
			}
			payload.outputSeen = true
			toolCallsByCallID[callID] = payload
			toolRestore[payload.marker] = payload
			updated, err := sjson.SetBytes(temporary, fmt.Sprintf("input.%d.call_id", index), payload.marker)
			if err != nil {
				return nil, fmt.Errorf("prepare Responses tool output identity for Claude translation")
			}
			temporary = updated
		}
		markerIndex := 0
		for itemIndex, item := range input.Array() {
			if item.Get("type").String() != "message" || item.Get("role").String() != "assistant" {
				continue
			}
			for partIndex, part := range item.Get("content").Array() {
				annotations := part.Get("annotations")
				if !annotations.IsArray() || len(annotations.Array()) == 0 {
					continue
				}
				marker := temporaryResponsesMarker(body, "citation", markerIndex)
				markerIndex++
				citationRestore[marker] = json.RawMessage(annotations.Raw)
				text, errText := temporaryCitationText(temporary, itemIndex, partIndex)
				if errText != nil {
					return nil, errText
				}
				updated, err := sjson.SetBytes(temporary, fmt.Sprintf("input.%d.content.%d.text", itemIndex, partIndex), text+marker)
				if err != nil {
					return nil, fmt.Errorf("prepare Responses citation for Claude translation")
				}
				temporary = updated
			}
		}
	}
	temporary, err := normalizeResponsesWebSearchPreviewForClaude(temporary)
	if err != nil {
		return nil, err
	}
	out, err := translateSDKRequest(sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, model, temporary, stream)
	if err != nil {
		return nil, err
	}
	if temperature := gjson.GetBytes(body, "temperature"); temperature.Exists() {
		if temperature.Type != gjson.Number || temperature.Float() < 0 || temperature.Float() > 1 {
			return nil, fmt.Errorf("Responses temperature cannot be represented by Claude Messages")
		}
		out, err = sjson.SetRawBytes(out, "temperature", []byte(temperature.Raw))
		if err != nil {
			return nil, fmt.Errorf("preserve Responses temperature for Claude Messages: %w", err)
		}
	}
	toolChoice := gjson.GetBytes(body, "tool_choice")
	if toolChoice.Type == gjson.String && toolChoice.String() == "required" {
		translatedTools := gjson.GetBytes(out, "tools")
		if !translatedTools.IsArray() || len(translatedTools.Array()) == 0 || gjson.GetBytes(out, "tool_choice.type").String() != "any" {
			return nil, fmt.Errorf("Responses tool_choice required cannot be preserved by Claude Messages without translated tools")
		}
	}
	if len(reasoningRestore) == 0 && len(toolRestore) == 0 && len(webSearchRestore) == 0 && len(citationRestore) == 0 {
		return out, nil
	}
	reasoningCounts := make(map[string]int, len(reasoningRestore))
	toolUseCounts := make(map[string]int, len(toolRestore))
	toolResultCounts := make(map[string]int, len(toolRestore))
	webSearchUseCounts := make(map[string]int, len(webSearchRestore))
	webSearchResultCounts := make(map[string]int, len(webSearchRestore))
	for messageIndex, message := range gjson.GetBytes(out, "messages").Array() {
		for blockIndex, block := range message.Get("content").Array() {
			var restored []byte
			var err error
			switch block.Get("type").String() {
			case "server_tool_use":
				id := block.Get("id").String()
				item, ok := webSearchRestore[id]
				if !ok || block.Get("name").String() != "web_search" {
					return nil, fmt.Errorf("Responses web search call could not be restored exactly in Claude request")
				}
				restored, err = sjson.SetBytes([]byte(block.Raw), "input.query", item.Get("action.query").String())
				webSearchUseCounts[id]++
			case "web_search_tool_result":
				id := block.Get("tool_use_id").String()
				item, ok := webSearchRestore[id]
				if !ok {
					return nil, fmt.Errorf("Responses web search result could not be restored exactly in Claude request")
				}
				restored, err = sjson.SetRawBytes([]byte(block.Raw), "content", []byte(item.Get("results").Raw))
				webSearchResultCounts[id]++
			case "redacted_thinking":
				payload, ok := reasoningRestore[block.Get("data").String()]
				if !ok {
					continue
				}
				restored = []byte(block.Raw)
				if payload.redacted {
					restored, err = sjson.SetBytes(restored, "data", payload.redactedData)
				} else {
					restored, err = sjson.SetBytes(restored, "type", "thinking")
					if err == nil {
						restored, err = sjson.SetBytes(restored, "thinking", payload.thinking)
					}
					if err == nil {
						restored, err = sjson.SetBytes(restored, "signature", payload.encrypted)
					}
					if err == nil {
						restored, err = sjson.DeleteBytes(restored, "data")
					}
				}
				if err != nil {
					return nil, fmt.Errorf("restore Responses reasoning in Claude request")
				}
				reasoningCounts[payload.marker]++
			case "tool_use":
				payload, ok := toolRestore[block.Get("id").String()]
				if !ok {
					continue
				}
				restored, err = sjson.SetBytes([]byte(block.Raw), "id", payload.claudeID)
				if err == nil {
					toolUseCounts[payload.marker]++
				}
			case "tool_result":
				payload, ok := toolRestore[block.Get("tool_use_id").String()]
				if !ok {
					continue
				}
				restored, err = sjson.SetBytes([]byte(block.Raw), "tool_use_id", payload.claudeID)
				if err == nil {
					toolResultCounts[payload.marker]++
				}
			default:
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("restore Responses identity in Claude request")
			}
			updated, err := sjson.SetRawBytes(out, fmt.Sprintf("messages.%d.content.%d", messageIndex, blockIndex), restored)
			if err != nil {
				return nil, fmt.Errorf("restore Responses identity in Claude request")
			}
			out = updated
		}
	}
	for marker := range reasoningRestore {
		if reasoningCounts[marker] != 1 {
			return nil, fmt.Errorf("Responses reasoning could not be restored exactly in Claude request")
		}
	}
	for marker, payload := range toolRestore {
		if toolUseCounts[marker] != 1 || (payload.outputSeen && toolResultCounts[marker] != 1) || toolResultCounts[marker] > 1 {
			return nil, fmt.Errorf("Responses tool identity could not be restored exactly in Claude request")
		}
	}
	if len(citationRestore) > 0 {
		out, err = restoreResponsesRequestCitations(out, citationRestore)
		if err != nil {
			return nil, err
		}
	}
	for id := range webSearchRestore {
		if webSearchUseCounts[id] != 1 || webSearchResultCounts[id] != 1 {
			return nil, fmt.Errorf("Responses web search pair could not be restored exactly in Claude request")
		}
	}
	return out, nil
}

func normalizeResponsesWebSearchPreviewForClaude(body []byte) ([]byte, error) {
	root := gjson.ParseBytes(body)
	updated, err := normalizeResponsesWebSearchPreviewToolArray(body, root.Get("tools"), "tools")
	if err != nil {
		return nil, err
	}
	for index, item := range root.Get("input").Array() {
		if item.Get("type").String() != "additional_tools" {
			continue
		}
		updated, err = normalizeResponsesWebSearchPreviewToolArray(updated, item.Get("tools"), fmt.Sprintf("input.%d.tools", index))
		if err != nil {
			return nil, err
		}
	}
	return updated, nil
}

func normalizeResponsesWebSearchPreviewToolArray(body []byte, tools gjson.Result, path string) ([]byte, error) {
	for index, tool := range tools.Array() {
		if tool.Get("type").String() != "web_search_preview" {
			continue
		}
		updated, err := sjson.SetBytes(body, fmt.Sprintf("%s.%d.type", path, index), "web_search")
		if err != nil {
			return nil, fmt.Errorf("normalize Responses web_search_preview tool for Claude Messages")
		}
		body = updated
	}
	return body, nil
}

func temporaryCitationText(body []byte, itemIndex, partIndex int) (string, error) {
	value := gjson.GetBytes(body, fmt.Sprintf("input.%d.content.%d.text", itemIndex, partIndex))
	if value.Type != gjson.String || value.String() == "" {
		return "", fmt.Errorf("Responses citation requires nonempty text")
	}
	return value.String(), nil
}

func restoreResponsesRequestCitations(body []byte, citations map[string]json.RawMessage) ([]byte, error) {
	root, err := decodeObject(body)
	if err != nil {
		return nil, fmt.Errorf("decode translated Messages citations")
	}
	messages, ok := root["messages"].([]any)
	if !ok {
		return nil, fmt.Errorf("translated Messages citations have no message list")
	}
	seen := make(map[string]int, len(citations))
	for _, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok || stringValue(message["role"]) != "assistant" {
			continue
		}
		content, ok := message["content"].([]any)
		if !ok {
			continue
		}
		restored := make([]any, 0, len(content))
		for _, rawBlock := range content {
			block, ok := rawBlock.(map[string]any)
			if !ok || stringValue(block["type"]) != "text" {
				restored = append(restored, rawBlock)
				continue
			}
			text, ok := block["text"].(string)
			if !ok {
				restored = append(restored, rawBlock)
				continue
			}
			cursor := 0
			matched := false
			for cursor < len(text) {
				marker, offset := nextCitationMarker(text[cursor:], citations)
				if marker == "" {
					break
				}
				markerStart := cursor + offset
				if markerStart == cursor {
					return nil, fmt.Errorf("translated Messages citation lost its text boundary")
				}
				segment := cloneClaudeTextBlock(block, text[cursor:markerStart])
				restored = append(restored, segment)
				var values []any
				if err := json.Unmarshal(citations[marker], &values); err != nil || len(values) == 0 {
					return nil, fmt.Errorf("decode restored Responses citations")
				}
				segment["citations"] = values
				restored[len(restored)-1] = segment
				seen[marker]++
				cursor = markerStart + len(marker)
				matched = true
			}
			if !matched {
				restored = append(restored, rawBlock)
				continue
			}
			if cursor < len(text) {
				restored = append(restored, cloneClaudeTextBlock(block, text[cursor:]))
			}
		}
		message["content"] = restored
	}
	for marker := range citations {
		if seen[marker] != 1 {
			return nil, fmt.Errorf("Responses citation could not be restored exactly in Claude request")
		}
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("encode translated Messages citations")
	}
	return encoded, nil
}

func nextCitationMarker(text string, citations map[string]json.RawMessage) (string, int) {
	selected, selectedIndex := "", -1
	for marker := range citations {
		if index := strings.Index(text, marker); index >= 0 && (selectedIndex < 0 || index < selectedIndex) {
			selected, selectedIndex = marker, index
		}
	}
	return selected, selectedIndex
}

func cloneClaudeTextBlock(original map[string]any, text string) map[string]any {
	block := make(map[string]any, len(original))
	for key, value := range original {
		if key != "citations" {
			block[key] = value
		}
	}
	block["text"] = text
	return block
}

func temporaryResponsesMarker(body []byte, kind string, index int) string {
	marker := fmt.Sprintf("cpa_bridge_%s_%d", kind, index)
	for bytes.Contains(body, []byte(marker)) {
		marker += "_"
	}
	return marker
}

func responsesClaudeToolPayloadFromItem(item gjson.Result) (responsesClaudeToolPayload, bool, error) {
	callID := responsesRequestItemCallID(item)
	itemID := item.Get("id").String()
	if callID == "" && itemID == "" {
		return responsesClaudeToolPayload{}, false, nil
	}
	if callID == "" {
		callID = itemID
	}
	carrierCallID := ""
	for _, field := range []string{"call_id", "tool_call_id", "callId"} {
		value := item.Get(field)
		if value.Type == gjson.String && strings.TrimSpace(value.String()) != "" {
			carrierCallID = value.String()
			break
		}
	}
	claudeID := claudeToolIDFromResponses(map[string]any{"id": itemID, "call_id": carrierCallID})
	if carrierCallID != "" {
		decodedCallID, encoded, err := unwrapClaudeToolIDCarrier(itemID, carrierCallID)
		if err != nil {
			return responsesClaudeToolPayload{}, false, fmt.Errorf("Responses tool call has an invalid Claude identity")
		}
		if encoded {
			claudeID = decodedCallID
		} else {
			itemPrefix := ""
			switch item.Get("type").String() {
			case "function_call":
				itemPrefix = "fc_"
			case "custom_tool_call":
				itemPrefix = "ctc_"
			}
			if itemPrefix != "" && itemID == itemPrefix+carrierCallID {
				claudeID = carrierCallID
			}
		}
	}
	return responsesClaudeToolPayload{claudeID: claudeID, callID: callID}, true, nil
}

func unwrapClaudeToolIDCarrier(itemID, value string) (string, bool, error) {
	if !strings.HasPrefix(value, bridgeToolIDPrefix) {
		return value, false, nil
	}
	carrierItemID, callID, wrapped, err := parseClaudeToolID(value)
	if err != nil || !wrapped || callID == "" || strings.HasPrefix(callID, bridgeToolIDPrefix) || (carrierItemID != "" && carrierItemID != itemID) {
		return "", false, errInvalidClaudeToolIDCarrier
	}
	return callID, true, nil
}

func responsesRequestItemCallID(item gjson.Result) string {
	for _, field := range []string{"call_id", "tool_call_id", "callId"} {
		value := item.Get(field)
		if value.Type == gjson.String && strings.TrimSpace(value.String()) != "" {
			return strings.TrimSpace(value.String())
		}
	}
	id := strings.TrimSpace(item.Get("id").String())
	if strings.HasPrefix(id, "fco_") {
		return ""
	}
	return id
}
