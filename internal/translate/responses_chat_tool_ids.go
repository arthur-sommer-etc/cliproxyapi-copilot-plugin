package translate

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func responsesRequestToChatToolIDs(body []byte) ([]byte, error) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, nil
	}
	out := body
	callIDs := make(map[string]string)
	outputSeen := make(map[string]struct{})
	for index, item := range input.Array() {
		switch item.Get("type").String() {
		case "function_call", "custom_tool_call":
			callID := item.Get("call_id").String()
			if callID == "" {
				return nil, fmt.Errorf("Responses tool call has no call ID for Chat translation")
			}
			if _, exists := callIDs[callID]; exists {
				return nil, fmt.Errorf("Responses tool call IDs are ambiguous for Chat translation")
			}
			carrier := encodeClaudeToolID(item.Get("id").String(), callID)
			if carrier == "" {
				return nil, fmt.Errorf("Responses tool call ID cannot be represented in Chat")
			}
			callIDs[callID] = carrier
			updated, err := sjson.SetBytes(out, fmt.Sprintf("input.%d.call_id", index), carrier)
			if err != nil {
				return nil, fmt.Errorf("encode Responses tool call ID for Chat translation")
			}
			out = updated
		}
	}
	for index, item := range input.Array() {
		switch item.Get("type").String() {
		case "function_call_output", "custom_tool_call_output":
			callID := item.Get("call_id").String()
			if callID == "" {
				return nil, fmt.Errorf("Responses tool output has no call ID for Chat translation")
			}
			if _, exists := outputSeen[callID]; exists {
				return nil, fmt.Errorf("Responses tool outputs have ambiguous call IDs for Chat translation")
			}
			outputSeen[callID] = struct{}{}
			carrier, exists := callIDs[callID]
			if !exists {
				carrier = encodeClaudeToolID("", callID)
			}
			updated, err := sjson.SetBytes(out, fmt.Sprintf("input.%d.call_id", index), carrier)
			if err != nil {
				return nil, fmt.Errorf("encode Responses tool output ID for Chat translation")
			}
			out = updated
		}
	}
	return out, nil
}
