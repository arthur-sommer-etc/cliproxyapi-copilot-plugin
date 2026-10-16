package translate

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type responsesChatStreamState struct {
	Responses     any
	Chat          any
	Output        chatOpaqueStreamState
	Request       []byte
	Model         string
	ReportedModel string
	Items         [][]byte
	Attached      bool
}

type claudeChatStreamState struct {
	Chat          any
	Output        chatOpaqueStreamState
	Blocks        map[int]*claudeOpaqueStreamBlock
	Order         []int
	Model         string
	ReportedModel string
	Attached      bool
}

type claudeOpaqueStreamBlock struct {
	Type      string
	Thinking  strings.Builder
	Signature string
	Data      string
}

func responsesStreamToChat(ctx context.Context, model string, original, translated, frame []byte, state *any) ([][]byte, error) {
	if err := validateResponsesStreamFrame(frame, sdktranslator.FormatOpenAI); err != nil {
		return nil, err
	}
	if state == nil {
		return nil, fmt.Errorf("Responses-to-Chat stream translation requires state")
	}
	streamState, ok := (*state).(*responsesChatStreamState)
	if !ok {
		claudeRequest, err := translateSDKRequest(sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, model, original, true)
		if err != nil {
			return nil, err
		}
		streamState = &responsesChatStreamState{Request: claudeRequest, Model: model, Output: chatOpaqueStreamState{Model: model, LastSequence: -1}}
		*state = streamState
	}
	if streamState.Model != model {
		return nil, fmt.Errorf("Responses-to-Chat stream model changed")
	}
	event, data, _, err := parseSSEFrame(frame)
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if len(data) > 0 {
		root, err = decodeObject(data)
		if err != nil {
			return nil, fmt.Errorf("decode Responses stream event for Chat translation")
		}
		if responseModel := stringValue(objectValue(root["response"])["model"]); responseModel != "" {
			streamState.ReportedModel = responseModel
		}
		if event == "" {
			event = stringValue(root["type"])
		}
	}
	if event == "response.output_item.done" {
		itemRaw := []byte(gjson.GetBytes(data, "item").Raw)
		if isOpaqueResponsesReasoningItem(itemRaw) {
			streamState.Items = mergeResponsesOpaqueItems(streamState.Items, [][]byte{itemRaw})
		}
	}
	if event == "response.completed" || event == "response.incomplete" {
		terminalItems := responsesOpaqueItems([]byte(gjson.GetBytes(data, "response").Raw))
		streamState.Items = mergeResponsesOpaqueItems(streamState.Items, terminalItems)
		if len(streamState.Items) > 0 {
			streamState.Output.Raw = rawJSONArray(streamState.Items)
		}
		if err := responsesFailure(objectValue(root["response"])); err != nil {
			return nil, err
		}
	}
	claudeFrames, err := responsesStreamToClaude(model, frame, &streamState.Responses)
	if err != nil {
		return nil, err
	}
	var out [][]byte
	for _, claudeFrame := range claudeFrames {
		translatedFrames, err := streamClaudeFrameToChat(ctx, model, original, streamState.Request, claudeFrame, &streamState.Chat, &streamState.Output, streamState.Items, &streamState.Attached)
		if err != nil {
			return nil, err
		}
		out = append(out, translatedFrames...)
	}
	if event == "response.incomplete" {
		usage := gjson.GetBytes(data, "response.usage")
		for index, chunk := range out {
			if !gjson.GetBytes(chunk, "usage").IsObject() {
				continue
			}
			for _, fields := range [][2]string{
				{"input_tokens", "prompt_tokens"}, {"output_tokens", "completion_tokens"}, {"total_tokens", "total_tokens"},
				{"input_tokens_details", "prompt_tokens_details"}, {"output_tokens_details", "completion_tokens_details"},
			} {
				if value := usage.Get(fields[0]); value.Exists() {
					chunk, err = sjson.SetRawBytes(chunk, "usage."+fields[1], []byte(value.Raw))
					if err != nil {
						return nil, fmt.Errorf("preserve incomplete Responses usage in Chat stream: %w", err)
					}
				}
			}
			out[index] = chunk
		}
	}
	return chatChunksWithReportedModel(out, streamState.ReportedModel, model, translated)
}

func isOpaqueResponsesReasoningItem(raw []byte) bool {
	return gjson.GetBytes(raw, "type").String() == "reasoning" && gjson.GetBytes(raw, "encrypted_content").Type == gjson.String && gjson.GetBytes(raw, "encrypted_content").String() != ""
}

func mergeResponsesOpaqueItems(current, candidates [][]byte) [][]byte {
	items := append([][]byte(nil), current...)
	positions := make(map[string]int, len(items)+len(candidates))
	for index, item := range items {
		positions[responsesOpaqueItemKey(item)] = index
	}
	for _, item := range candidates {
		key := responsesOpaqueItemKey(item)
		if index, exists := positions[key]; exists {
			items[index] = item
		} else {
			positions[key] = len(items)
			items = append(items, item)
		}
	}
	return items
}

func responsesOpaqueItemKey(item []byte) string {
	if id := gjson.GetBytes(item, "id").String(); id != "" {
		return "id:" + id
	}
	if encrypted := gjson.GetBytes(item, "encrypted_content").String(); encrypted != "" {
		return "encrypted:" + encrypted
	}
	return "json:" + canonicalJSONDigest(string(item))
}

func claudeStreamToChat(ctx context.Context, model string, original, translated, frame []byte, state *any) ([][]byte, error) {
	if err := validateClaudeStreamFrame(frame, sdktranslator.FormatOpenAI); err != nil {
		return nil, err
	}
	if state == nil {
		return nil, fmt.Errorf("Claude-to-Chat stream translation requires state")
	}
	streamState, ok := (*state).(*claudeChatStreamState)
	if !ok {
		streamState = &claudeChatStreamState{Model: model, Output: chatOpaqueStreamState{Model: model, LastSequence: -1}, Blocks: make(map[int]*claudeOpaqueStreamBlock)}
		*state = streamState
	}
	if streamState.Model != model {
		return nil, fmt.Errorf("Claude-to-Chat stream model changed")
	}
	event, data, done, err := parseSSEFrame(frame)
	if err != nil {
		return nil, err
	}
	resolvedEvent := firstNonEmptyString(event, gjson.GetBytes(data, "type").String())
	if resolvedEvent == "message_start" {
		if reportedModel := gjson.GetBytes(data, "message.model").String(); reportedModel != "" {
			streamState.ReportedModel = reportedModel
		}
	}
	if len(data) > 0 && !done {
		if err := streamState.observeOpaque(data); err != nil {
			return nil, err
		}
	}
	normalized, err := nativeClaudeFrame(data, done)
	if err != nil {
		return nil, err
	}
	chatFrames := registry.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, model, original, translated, normalized, &streamState.Chat)
	for _, chatFrame := range chatFrames {
		if err := observeChatChunk(&streamState.Output, chatFrame, original, model); err != nil {
			return nil, err
		}
	}
	if resolvedEvent == "message_delta" && len(data) > 0 && !streamState.Attached {
		items := streamState.responsesReasoningItems()
		if len(items) > 0 {
			streamState.Output.Raw = rawJSONArray(items)
			attachedFrames, err := attachOpaqueToChatFrames(chatFrames, &streamState.Output, original, model, &streamState.Attached)
			if err != nil {
				return nil, err
			}
			return chatChunksWithReportedModel(attachedFrames, streamState.ReportedModel, model, translated)
		}
	}
	return chatChunksWithReportedModel(chatFrames, streamState.ReportedModel, model, translated)
}

func chatChunksWithReportedModel(frames [][]byte, reportedModel, routeModel string, translated []byte) ([][]byte, error) {
	model := firstNonEmptyString(reportedModel, routeModel, gjson.GetBytes(translated, "model").String())
	if model == "" {
		return frames, nil
	}
	updated := append([][]byte(nil), frames...)
	for index, frame := range updated {
		if !json.Valid(frame) {
			return nil, fmt.Errorf("translated Chat stream output is not valid JSON")
		}
		value, err := sjson.SetBytes(frame, "model", model)
		if err != nil {
			return nil, fmt.Errorf("preserve upstream model in Chat stream")
		}
		updated[index] = value
	}
	return updated, nil
}

func streamClaudeFrameToChat(ctx context.Context, model string, original, claudeRequest, frame []byte, nativeState *any, outputState *chatOpaqueStreamState, items [][]byte, attached *bool) ([][]byte, error) {
	event, data, done, err := parseSSEFrame(frame)
	if err != nil {
		return nil, err
	}
	if !done && len(data) > 0 {
		payload, err := decodeObject(data)
		if err != nil {
			return nil, fmt.Errorf("decode Claude stream event for Chat translation")
		}
		if err := validateClaudeHostedStateForChat(payload); err != nil {
			return nil, err
		}
	}
	normalized, err := nativeClaudeFrame(data, done)
	if err != nil {
		return nil, err
	}
	chatFrames := registry.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, model, original, claudeRequest, normalized, nativeState)
	for _, chatFrame := range chatFrames {
		if err := observeChatChunk(outputState, chatFrame, original, model); err != nil {
			return nil, err
		}
	}
	if event == "message_delta" && len(items) > 0 && !*attached {
		outputState.Raw = rawJSONArray(items)
		return attachOpaqueToChatFrames(chatFrames, outputState, original, model, attached)
	}
	return chatFrames, nil
}

func nativeClaudeFrame(data []byte, done bool) ([]byte, error) {
	if done {
		return []byte("data: [DONE]\n\n"), nil
	}
	if len(data) == 0 {
		return nil, nil
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("Claude stream event data is not valid JSON")
	}
	return append(append([]byte("data: "), data...), '\n', '\n'), nil
}

func observeChatChunk(state *chatOpaqueStreamState, chunk, request []byte, model string) error {
	if !json.Valid(chunk) {
		return fmt.Errorf("official Claude-to-Chat stream output is not valid JSON")
	}
	frame := make([]byte, 0, len(chunk)+10)
	frame = append(frame, "data: "...)
	frame = append(frame, chunk...)
	frame = append(frame, '\n', '\n')
	return state.observe(frame, request, model)
}

func attachOpaqueToChatFrames(frames [][]byte, outputState *chatOpaqueStreamState, request []byte, model string, attached *bool) ([][]byte, error) {
	carrier, exists, err := outputState.carrier(request, model)
	if err != nil {
		return nil, err
	}
	if !exists {
		return frames, nil
	}
	out := append([][]byte(nil), frames...)
	for index := len(out) - 1; index >= 0; index-- {
		if gjson.GetBytes(out[index], "choices.0").IsObject() {
			updated, err := sjson.SetBytes(out[index], "choices.0.delta.reasoning_opaque", carrier)
			if err != nil {
				return nil, fmt.Errorf("attach Chat stream reasoning carrier")
			}
			out[index] = updated
			*attached = true
			return out, nil
		}
	}
	if outputState.ID == "" {
		return nil, fmt.Errorf("Chat stream reasoning carrier has no response ID")
	}
	chunk := []byte(`{"id":"","object":"chat.completion.chunk","model":"","choices":[{"index":0,"delta":{"reasoning_opaque":""},"finish_reason":null}]}`)
	chunk, err = sjson.SetBytes(chunk, "id", outputState.ID)
	if err == nil {
		chunk, err = sjson.SetBytes(chunk, "model", model)
	}
	if err == nil {
		chunk, err = sjson.SetBytes(chunk, "choices.0.delta.reasoning_opaque", carrier)
	}
	if err == nil && outputState.Created != "" {
		chunk, err = sjson.SetRawBytes(chunk, "created", []byte(outputState.Created))
	}
	if err != nil {
		return nil, fmt.Errorf("encode Chat stream reasoning carrier")
	}
	*attached = true
	return append(out, chunk), nil
}

func (s *claudeChatStreamState) observeOpaque(data []byte) error {
	root, err := decodeObject(data)
	if err != nil {
		return fmt.Errorf("decode Claude thinking stream event")
	}
	switch stringValue(root["type"]) {
	case "content_block_start":
		index, err := strconv.Atoi(numberKey(root["index"]))
		if err != nil {
			return fmt.Errorf("Claude thinking stream block has an invalid index")
		}
		block := objectValue(root["content_block"])
		kind := stringValue(block["type"])
		if kind == "thinking" || kind == "redacted_thinking" {
			capture := &claudeOpaqueStreamBlock{Type: kind}
			if kind == "thinking" {
				capture.Thinking.WriteString(rawStringValue(block["thinking"]))
			} else {
				capture.Data = rawStringValue(block["data"])
			}
			s.Blocks[index] = capture
			s.Order = append(s.Order, index)
		}
	case "content_block_delta":
		index, err := strconv.Atoi(numberKey(root["index"]))
		if err != nil {
			return fmt.Errorf("Claude thinking stream delta has an invalid index")
		}
		block := s.Blocks[index]
		if block != nil {
			delta := objectValue(root["delta"])
			switch stringValue(delta["type"]) {
			case "thinking_delta":
				block.Thinking.WriteString(rawStringValue(delta["thinking"]))
			case "signature_delta":
				block.Signature = rawStringValue(delta["signature"])
			}
		}
	}
	return nil
}

func (s *claudeChatStreamState) responsesReasoningItems() [][]byte {
	indices := append([]int(nil), s.Order...)
	sort.Ints(indices)
	items := make([][]byte, 0, len(indices))
	for _, index := range indices {
		block := s.Blocks[index]
		var item map[string]any
		if block.Type == "redacted_thinking" {
			item = map[string]any{"type": "reasoning", "summary": []any{}, "encrypted_content": redactedThinkingPrefix + block.Data}
		} else {
			summary := []any{}
			if block.Thinking.Len() > 0 {
				summary = append(summary, map[string]any{"type": "summary_text", "text": block.Thinking.String()})
			}
			item = map[string]any{"type": "reasoning", "summary": summary, "encrypted_content": block.Signature}
		}
		encoded, err := json.Marshal(item)
		if err == nil {
			items = append(items, encoded)
		}
	}
	return items
}
