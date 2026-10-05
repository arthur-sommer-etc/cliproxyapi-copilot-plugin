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
	if input.IsArray() {
		for index, item := range input.Array() {
			switch item.Get("type").String() {
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
				payload, ok := responsesClaudeToolPayloadFromItem(item)
				if !ok {
					continue
				}
				if _, exists := toolCallsByCallID[payload.callID]; exists {
					return nil, fmt.Errorf("Responses tool call identity is ambiguous for Claude translation")
				}
				payload.marker = temporaryResponsesMarker(body, "tool", index)
				toolCallsByCallID[payload.callID] = payload
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
	}
	out := registry.TranslateRequest(sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, model, temporary, stream)
	if len(out) == 0 || !json.Valid(out) {
		return nil, fmt.Errorf("official Responses-to-Claude request translation failed")
	}
	if len(reasoningRestore) == 0 && len(toolRestore) == 0 {
		return out, nil
	}
	reasoningCounts := make(map[string]int, len(reasoningRestore))
	toolUseCounts := make(map[string]int, len(toolRestore))
	toolResultCounts := make(map[string]int, len(toolRestore))
	for messageIndex, message := range gjson.GetBytes(out, "messages").Array() {
		for blockIndex, block := range message.Get("content").Array() {
			var restored []byte
			var err error
			switch block.Get("type").String() {
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
	return out, nil
}

func temporaryResponsesMarker(body []byte, kind string, index int) string {
	marker := fmt.Sprintf("cpa_bridge_%s_%d", kind, index)
	for bytes.Contains(body, []byte(marker)) {
		marker += "_"
	}
	return marker
}

func responsesClaudeToolPayloadFromItem(item gjson.Result) (responsesClaudeToolPayload, bool) {
	callID := responsesRequestItemCallID(item)
	itemID := item.Get("id").String()
	if callID == "" && itemID == "" {
		return responsesClaudeToolPayload{}, false
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
	return responsesClaudeToolPayload{claudeID: claudeID, callID: callID}, true
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
