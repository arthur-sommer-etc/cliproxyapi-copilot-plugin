package translate

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type orphanResponsesToolOutput struct {
	marker  string
	message map[string]any
}

func prepareOrphanResponsesToolOutputs(body []byte) ([]byte, []orphanResponsesToolOutput, error) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, nil, nil
	}
	seenCalls := make(map[string]struct{})
	seenOutputs := make(map[string]struct{})
	var orphaned []orphanResponsesToolOutput
	out := body
	for index, item := range input.Array() {
		switch item.Get("type").String() {
		case "function_call", "custom_tool_call":
			callID := item.Get("call_id").String()
			if strings.TrimSpace(callID) == "" {
				return nil, nil, fmt.Errorf("Responses tool call has no call ID for Chat translation")
			}
			seenCalls[callID] = struct{}{}
		case "function_call_output", "custom_tool_call_output":
			callID := item.Get("call_id").String()
			if strings.TrimSpace(callID) == "" {
				return nil, nil, fmt.Errorf("Responses tool output has no call ID for Chat translation")
			}
			if _, exists := seenOutputs[callID]; exists {
				return nil, nil, fmt.Errorf("Responses tool outputs have ambiguous call IDs for Chat translation")
			}
			seenOutputs[callID] = struct{}{}
			if _, exists := seenCalls[callID]; !exists {
				content, err := responsesToolOutputToChatContent(item.Get("output"))
				if err != nil {
					return nil, nil, err
				}
				marker := temporaryResponsesMarker(body, "orphan_tool", index)
				for jsonContainsStringValue(body, marker) {
					marker += "_"
				}
				message := map[string]any{"role": "tool", "tool_call_id": callID, "content": content}
				placeholder, err := json.Marshal(map[string]any{
					"type":    "message",
					"role":    "user",
					"content": []any{map[string]any{"type": "input_text", "text": marker}},
				})
				if err != nil {
					return nil, nil, fmt.Errorf("encode standalone Responses tool output placeholder")
				}
				updated, err := sjson.SetRawBytes(out, fmt.Sprintf("input.%d", index), placeholder)
				if err != nil {
					return nil, nil, fmt.Errorf("prepare standalone Responses tool output for Chat translation")
				}
				out = updated
				orphaned = append(orphaned, orphanResponsesToolOutput{marker: marker, message: message})
			}
		}
	}
	return out, orphaned, nil
}

func restoreOrphanResponsesToolOutputs(chatRequest []byte, orphaned []orphanResponsesToolOutput) ([]byte, error) {
	if len(orphaned) == 0 {
		return chatRequest, nil
	}
	root, err := decodeObject(chatRequest)
	if err != nil {
		return nil, err
	}
	messages := arrayValue(root["messages"])
	for _, result := range orphaned {
		messageIndex := -1
		for index, rawMessage := range messages {
			message := objectValue(rawMessage)
			if stringValue(message["role"]) != "user" || !chatMessageHasOnlyMarker(message, result.marker) {
				continue
			}
			if messageIndex >= 0 {
				return nil, fmt.Errorf("standalone Responses tool output placeholder was duplicated during Chat translation")
			}
			messageIndex = index
		}
		if messageIndex < 0 || jsonStringValueCount(root, result.marker) != 1 {
			return nil, fmt.Errorf("standalone Responses tool output placeholder was lost or changed during Chat translation")
		}
		messages[messageIndex] = result.message
	}
	root["messages"] = messages
	return json.Marshal(root)
}

func chatMessageHasOnlyMarker(message map[string]any, marker string) bool {
	content, ok := message["content"].([]any)
	if !ok || len(content) != 1 {
		return false
	}
	part := objectValue(content[0])
	return stringValue(part["type"]) == "text" && stringValue(part["text"]) == marker && len(part) == 2
}

func jsonContainsStringValue(body []byte, expected string) bool {
	var value any
	if json.Unmarshal(body, &value) != nil {
		return false
	}
	return jsonStringValueCount(value, expected) > 0
}

func jsonStringValueCount(value any, expected string) int {
	switch typed := value.(type) {
	case string:
		return strings.Count(typed, expected)
	case []any:
		count := 0
		for _, child := range typed {
			count += jsonStringValueCount(child, expected)
		}
		return count
	case map[string]any:
		count := 0
		for _, child := range typed {
			count += jsonStringValueCount(child, expected)
		}
		return count
	}
	return 0
}

func responsesToolOutputToChatContent(output gjson.Result) (any, error) {
	if output.Type == gjson.String {
		return output.String(), nil
	}
	if !output.IsArray() {
		return nil, fmt.Errorf("standalone Responses tool output cannot be represented by Chat Completions")
	}
	content := make([]any, 0, len(output.Array()))
	for _, part := range output.Array() {
		switch part.Get("type").String() {
		case "input_text", "output_text":
			content = append(content, map[string]any{"type": "text", "text": part.Get("text").String()})
		case "input_image":
			imageURL := part.Get("image_url").String()
			if imageURL == "" {
				imageURL = part.Get("url").String()
			}
			if imageURL == "" {
				return nil, fmt.Errorf("standalone Responses tool image has no URL")
			}
			image := map[string]any{"url": imageURL}
			if detail := part.Get("detail"); detail.Exists() {
				image["detail"] = detail.Value()
			}
			content = append(content, map[string]any{"type": "image_url", "image_url": image})
		default:
			return nil, fmt.Errorf("standalone Responses tool output cannot be represented by Chat Completions")
		}
	}
	return content, nil
}

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
