package translate

import (
	"context"
	"encoding/json"
	"fmt"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func responsesResponseToChat(ctx context.Context, model string, original, translated, body []byte) ([]byte, error) {
	root, err := decodeObject(body)
	if err != nil {
		return nil, fmt.Errorf("decode Responses response for Chat translation")
	}
	if err := validateResponsesOutputForChat(root); err != nil {
		return nil, err
	}
	preparedBody, expectedText, citations, err := prepareResponsesChatCitations(body)
	if err != nil {
		return nil, err
	}
	claudeRequest, err := translateSDKRequest(sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, model, original, false)
	if err != nil {
		return nil, err
	}
	claudeResponse, err := responsesResponseToClaude(model, preparedBody)
	if err != nil {
		return nil, err
	}
	chatResponse := registry.TranslateNonStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, model, claudeRequest, translated, claudeResponse, nil)
	if len(chatResponse) == 0 || !json.Valid(chatResponse) {
		return nil, fmt.Errorf("official Responses-to-Chat response translation failed")
	}
	chatResponse, err = restoreClaudeToolIDsToChat(chatResponse)
	if err != nil {
		return nil, err
	}
	items := responsesOpaqueItems(body)
	if len(items) > 0 {
		choices := gjson.GetBytes(chatResponse, "choices")
		if !choices.IsArray() || len(choices.Array()) != 1 {
			return nil, fmt.Errorf("Responses reasoning cannot be anchored to one Chat choice")
		}
		message := choices.Array()[0].Get("message")
		carrier, err := encodeCopilotOpaque(rawJSONArray(items), model, copilotAnchorForMessage(message, original))
		if err != nil {
			return nil, err
		}
		chatResponse, err = sjson.SetBytes(chatResponse, "choices.0.message.reasoning_opaque", carrier)
		if err != nil {
			return nil, fmt.Errorf("preserve Responses reasoning in Chat response")
		}
	}
	return restoreResponsesChatCitations(chatResponse, expectedText, citations)
}

func restoreClaudeToolIDsToChat(body []byte) ([]byte, error) {
	choices := gjson.GetBytes(body, "choices")
	if !choices.IsArray() {
		return body, nil
	}
	out := body
	for choiceIndex, choice := range choices.Array() {
		for callIndex, call := range choice.Get("message.tool_calls").Array() {
			id := call.Get("id").String()
			itemID, callID, encoded, err := parseClaudeToolID(id)
			if err != nil {
				return nil, fmt.Errorf("decode Responses tool identity from Chat response")
			}
			if encoded {
				visibleID := encodeClaudeToolID(itemID, callID)
				updated, err := sjson.SetBytes(out, fmt.Sprintf("choices.%d.message.tool_calls.%d.id", choiceIndex, callIndex), visibleID)
				if err != nil {
					return nil, fmt.Errorf("preserve Responses tool IDs in Chat response")
				}
				out = updated
			}
		}
	}
	return out, nil
}

func responsesOpaqueItems(body []byte) [][]byte {
	var items [][]byte
	for _, item := range gjson.GetBytes(body, "output").Array() {
		if item.Get("type").String() == "reasoning" && item.Get("encrypted_content").Type == gjson.String && item.Get("encrypted_content").String() != "" {
			items = append(items, []byte(item.Raw))
		}
	}
	return items
}

func rawJSONArray(items [][]byte) []byte {
	out := []byte{'['}
	for index, item := range items {
		if index > 0 {
			out = append(out, ',')
		}
		out = append(out, item...)
	}
	return append(out, ']')
}
