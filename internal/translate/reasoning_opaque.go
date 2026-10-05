package translate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const copilotOpaquePrefix = "cpa-copilot-reasoning:v1:"

type copilotOpaqueAnchor struct {
	ToolCallIDs     []string                  `json:"tool_call_ids,omitempty"`
	ToolCalls       []copilotOpaqueToolAnchor `json:"tool_calls,omitempty"`
	ContentSHA256   string                    `json:"content_sha256,omitempty"`
	PriorUserSHA256 string                    `json:"prior_user_sha256,omitempty"`
}

type copilotOpaqueToolAnchor struct {
	Name          string `json:"name,omitempty"`
	ArgumentsHash string `json:"arguments_sha256,omitempty"`
}

type copilotOpaqueEnvelope struct {
	Version    int                 `json:"v"`
	Provider   string              `json:"provider"`
	Endpoint   string              `json:"endpoint"`
	Model      string              `json:"model"`
	RawJSONB64 string              `json:"raw_json_b64"`
	Anchor     copilotOpaqueAnchor `json:"anchor"`
}

type copilotOpaqueValue struct {
	Raw    []byte
	Anchor copilotOpaqueAnchor
}

type chatOpaqueStreamTool struct {
	ID        string
	Name      string
	Arguments strings.Builder
}

type chatOpaqueStreamState struct {
	Model        string
	Chat         any
	Claude       any
	LastSequence int64
	Raw          []byte
	Content      strings.Builder
	Tools        map[int]*chatOpaqueStreamTool
	Completed    bool
}

func encodeCopilotOpaque(raw []byte, model string, anchor copilotOpaqueAnchor) (string, error) {
	if !json.Valid(raw) {
		return "", fmt.Errorf("Copilot reasoning_opaque is not valid JSON")
	}
	if len(anchor.ToolCallIDs) == 0 && anchor.ContentSHA256 == "" && anchor.PriorUserSHA256 == "" {
		return "", fmt.Errorf("Copilot reasoning_opaque has no replay anchor")
	}
	envelope := copilotOpaqueEnvelope{
		Version:    1,
		Provider:   "copilot",
		Endpoint:   "chat/completions",
		Model:      model,
		RawJSONB64: base64.RawURLEncoding.EncodeToString(raw),
		Anchor:     anchor,
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("encode Copilot reasoning carrier: %w", err)
	}
	return copilotOpaquePrefix + base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeCopilotOpaque(value, model string) (copilotOpaqueValue, error) {
	if !strings.HasPrefix(value, copilotOpaquePrefix) {
		return copilotOpaqueValue{}, fmt.Errorf("cannot replay foreign encrypted reasoning to Copilot Chat")
	}
	encodedEnvelope, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, copilotOpaquePrefix))
	if err != nil {
		return copilotOpaqueValue{}, fmt.Errorf("invalid Copilot reasoning carrier")
	}
	var envelope copilotOpaqueEnvelope
	if err := json.Unmarshal(encodedEnvelope, &envelope); err != nil {
		return copilotOpaqueValue{}, fmt.Errorf("invalid Copilot reasoning carrier")
	}
	if envelope.Version != 1 || envelope.Provider != "copilot" || envelope.Endpoint != "chat/completions" || envelope.Model != model {
		return copilotOpaqueValue{}, fmt.Errorf("Copilot reasoning carrier scope does not match this request")
	}
	raw, err := base64.RawURLEncoding.DecodeString(envelope.RawJSONB64)
	if err != nil || !json.Valid(raw) {
		return copilotOpaqueValue{}, fmt.Errorf("invalid Copilot reasoning carrier payload")
	}
	if len(envelope.Anchor.ToolCallIDs) == 0 && envelope.Anchor.ContentSHA256 == "" && envelope.Anchor.PriorUserSHA256 == "" {
		return copilotOpaqueValue{}, fmt.Errorf("Copilot reasoning carrier has no replay anchor")
	}
	return copilotOpaqueValue{Raw: raw, Anchor: envelope.Anchor}, nil
}

func rejectMismatchedCopilotCarrier(from, to sdktranslator.Format, model string, body []byte) error {
	if from == sdktranslator.FormatClaude && containsClaudeCarrier(body) && to != sdktranslator.FormatOpenAI {
		return fmt.Errorf("Copilot reasoning carrier requires the matching Chat Completions endpoint")
	}
	if from == sdktranslator.FormatOpenAIResponse && containsResponsesCarrier(body) && to != sdktranslator.FormatOpenAI {
		return fmt.Errorf("Copilot reasoning carrier requires the matching Chat Completions endpoint")
	}
	return nil
}

func containsClaudeCarrier(body []byte) bool {
	root, err := decodeObject(body)
	return err == nil && findClaudeCarrier(root)
}

func findClaudeCarrier(value any) bool {
	switch current := value.(type) {
	case map[string]any:
		if rawStringValue(current["type"]) == "thinking" && strings.HasPrefix(rawStringValue(current["signature"]), copilotOpaquePrefix) {
			return true
		}
		for _, nested := range current {
			if findClaudeCarrier(nested) {
				return true
			}
		}
	case []any:
		for _, nested := range current {
			if findClaudeCarrier(nested) {
				return true
			}
		}
	}
	return false
}

func containsResponsesCarrier(body []byte) bool {
	root, err := decodeObject(body)
	return err == nil && findResponsesCarrier(root)
}

func findResponsesCarrier(value any) bool {
	switch current := value.(type) {
	case map[string]any:
		if rawStringValue(current["type"]) == "reasoning" && strings.HasPrefix(rawStringValue(current["encrypted_content"]), copilotOpaquePrefix) {
			return true
		}
		for _, nested := range current {
			if findResponsesCarrier(nested) {
				return true
			}
		}
	case []any:
		for _, nested := range current {
			if findResponsesCarrier(nested) {
				return true
			}
		}
	}
	return false
}

func copilotOpaqueFromChatResponse(response, request []byte, model string) (string, bool, error) {
	root := gjson.ParseBytes(response)
	choices := root.Get("choices")
	if !choices.IsArray() {
		return "", false, nil
	}
	var raw []byte
	var anchor copilotOpaqueAnchor
	for _, choice := range choices.Array() {
		message := choice.Get("message")
		field := message.Get("reasoning_opaque")
		if !field.Exists() {
			continue
		}
		if role := message.Get("role"); role.Exists() && role.String() != "assistant" {
			return "", false, fmt.Errorf("Chat opaque reasoning is not attached to an assistant message")
		}
		candidate := []byte(field.Raw)
		if !meaningfulOpaqueJSON(candidate) {
			continue
		}
		if raw != nil {
			return "", false, fmt.Errorf("multiple Chat choices contain opaque reasoning")
		}
		raw = candidate
		anchor = copilotAnchorForMessage(message, request)
	}
	if raw == nil {
		return "", false, nil
	}
	carrier, err := encodeCopilotOpaque(raw, model, anchor)
	if err != nil {
		return "", false, err
	}
	return carrier, true, nil
}

func copilotAnchorForMessage(message gjson.Result, request []byte) copilotOpaqueAnchor {
	anchor := copilotOpaqueAnchor{}
	message.Get("tool_calls").ForEach(func(_, call gjson.Result) bool {
		id := call.Get("id").String()
		if _, callID, encoded, err := parseClaudeToolID(id); err == nil && encoded {
			id = callID
		}
		if id != "" {
			anchor.ToolCallIDs = append(anchor.ToolCallIDs, id)
			function := call.Get("function")
			anchor.ToolCalls = append(anchor.ToolCalls, copilotOpaqueToolAnchor{
				Name:          function.Get("name").String(),
				ArgumentsHash: canonicalJSONDigest(function.Get("arguments").String()),
			})
		}
		return true
	})
	if content := message.Get("content"); content.Exists() && content.Raw != "null" {
		if text, ok := chatContentFingerprint(content); ok && text != "" {
			anchor.ContentSHA256 = digest([]byte(text))
		}
	}
	anchor.PriorUserSHA256 = priorChatUserFingerprint(request)
	return anchor
}

func meaningfulOpaqueJSON(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("[]")) || bytes.Equal(trimmed, []byte("{}")) {
		return false
	}
	var value string
	if err := json.Unmarshal(trimmed, &value); err == nil && value == "" {
		return false
	}
	return true
}

func canonicalJSONDigest(raw string) string {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return digest([]byte(raw))
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return digest([]byte(raw))
	}
	return digest(canonical)
}

func chatContentFingerprint(content gjson.Result) (string, bool) {
	if content.Type == gjson.String {
		return content.String(), true
	}
	if !content.IsArray() {
		return "", false
	}
	var text strings.Builder
	for _, part := range content.Array() {
		typeName := part.Get("type").String()
		if typeName != "text" && typeName != "input_text" && typeName != "output_text" {
			return "", false
		}
		value := part.Get("text")
		if !value.Exists() || value.Type != gjson.String {
			return "", false
		}
		text.WriteString(value.String())
	}
	return text.String(), true
}

func priorChatUserFingerprint(request []byte) string {
	messages := gjson.GetBytes(request, "messages")
	if !messages.IsArray() {
		return ""
	}
	var last string
	for _, message := range messages.Array() {
		if message.Get("role").String() != "user" {
			continue
		}
		content, ok := chatContentFingerprint(message.Get("content"))
		if ok && content != "" {
			last = digest([]byte(content))
		}
	}
	return last
}

func digest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func copyResponsesPromptCacheKey(chatRequest, responsesRequest []byte) ([]byte, error) {
	key := gjson.GetBytes(responsesRequest, "prompt_cache_key")
	if key.Exists() {
		updated, err := sjson.SetRawBytes(chatRequest, "prompt_cache_key", []byte(key.Raw))
		if err != nil {
			return nil, fmt.Errorf("preserve Responses prompt_cache_key")
		}
		chatRequest = updated
	}
	return copyResponsesCacheControls(chatRequest, responsesRequest)
}

func copyResponsesCacheControls(chatRequest, responsesRequest []byte) ([]byte, error) {
	inputs := gjson.GetBytes(responsesRequest, "input")
	messages := gjson.GetBytes(chatRequest, "messages")
	if inputs.IsArray() && messages.IsArray() {
		messageIndex, contentIndex := 0, 0
		for _, input := range inputs.Array() {
			switch input.Get("type").String() {
			case "message":
				contents := input.Get("content")
				if !contents.IsArray() {
					continue
				}
				for _, content := range contents.Array() {
					cacheControl := content.Get("cache_control")
					if !cacheControl.Exists() {
						continue
					}
					text := content.Get("text")
					if !text.Exists() || text.Type != gjson.String {
						return nil, fmt.Errorf("cannot preserve cache_control on non-text Responses content")
					}
					var targetPath string
					for messageIndex < len(messages.Array()) {
						message := messages.Array()[messageIndex]
						if message.Get("role").String() != input.Get("role").String() {
							messageIndex++
							contentIndex = 0
							continue
						}
						chatContent := message.Get("content")
						if chatContent.Type == gjson.String && chatContent.String() == text.String() {
							targetPath = fmt.Sprintf("messages.%d.cache_control", messageIndex)
							messageIndex++
							contentIndex = 0
							break
						}
						if chatContent.IsArray() {
							parts := chatContent.Array()
							for contentIndex < len(parts) {
								part := parts[contentIndex]
								currentIndex := contentIndex
								contentIndex++
								if part.Get("text").Type == gjson.String && part.Get("text").String() == text.String() {
									targetPath = fmt.Sprintf("messages.%d.content.%d.cache_control", messageIndex, currentIndex)
									break
								}
							}
							if targetPath != "" {
								break
							}
							messageIndex++
							contentIndex = 0
							continue
						}
						messageIndex++
						contentIndex = 0
					}
					if targetPath == "" {
						return nil, fmt.Errorf("cannot match Responses cache_control content in Chat request")
					}
					updated, err := sjson.SetRawBytes(chatRequest, targetPath, []byte(cacheControl.Raw))
					if err != nil {
						return nil, fmt.Errorf("preserve Responses cache_control")
					}
					chatRequest = updated
					messages = gjson.GetBytes(chatRequest, "messages")
				}
			}
		}
	}
	tools := gjson.GetBytes(responsesRequest, "tools")
	chatTools := gjson.GetBytes(chatRequest, "tools")
	if tools.IsArray() {
		for _, tool := range tools.Array() {
			cacheControl := tool.Get("cache_control")
			if !cacheControl.Exists() {
				continue
			}
			name := tool.Get("name").String()
			var targetIndex = -1
			for index, chatTool := range chatTools.Array() {
				if chatTool.Get("function.name").String() == name {
					targetIndex = index
					break
				}
			}
			if targetIndex < 0 {
				return nil, fmt.Errorf("cannot match Responses tool cache_control in Chat request")
			}
			updated, err := sjson.SetRawBytes(chatRequest, fmt.Sprintf("tools.%d.cache_control", targetIndex), []byte(cacheControl.Raw))
			if err != nil {
				return nil, fmt.Errorf("preserve Responses tool cache_control")
			}
			chatRequest = updated
			chatTools = gjson.GetBytes(chatRequest, "tools")
		}
	}
	return chatRequest, nil
}

func restoreCopilotOpaqueToChat(chatRequest, responsesRequest []byte, model string) ([]byte, error) {
	items := gjson.GetBytes(responsesRequest, "input")
	if !items.IsArray() {
		return chatRequest, nil
	}
	values := make([]copilotOpaqueValue, 0)
	for _, item := range items.Array() {
		if item.Get("type").String() != "reasoning" {
			continue
		}
		encrypted := item.Get("encrypted_content")
		if !encrypted.Exists() || encrypted.Type == gjson.Null || encrypted.String() == "" {
			continue
		}
		if encrypted.Type != gjson.String {
			return nil, fmt.Errorf("cannot replay non-string encrypted reasoning to Copilot Chat")
		}
		value, err := decodeCopilotOpaque(encrypted.String(), model)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if len(values) == 0 {
		return chatRequest, nil
	}
	groups := make(map[string][]copilotOpaqueValue)
	order := make([]string, 0)
	for _, value := range values {
		anchorJSON, _ := json.Marshal(value.Anchor)
		key := string(anchorJSON)
		if _, exists := groups[key]; !exists {
			order = append(order, key)
		}
		groups[key] = append(groups[key], value)
	}
	messages := make([][]byte, 0, len(gjson.GetBytes(chatRequest, "messages").Array())+len(groups))
	messageResults := gjson.GetBytes(chatRequest, "messages").Array()
	for _, message := range messageResults {
		messages = append(messages, []byte(message.Raw))
	}
	for _, key := range order {
		group := groups[key]
		if len(group) != 1 {
			return nil, fmt.Errorf("multiple Copilot opaque reasoning items share one assistant anchor")
		}
		messageIndex, insertIndex, err := findOpaqueAnchor(messages, group[0].Anchor)
		if err != nil {
			return nil, err
		}
		if messageIndex >= 0 {
			updated, err := sjson.SetRawBytes(messages[messageIndex], "reasoning_opaque", group[0].Raw)
			if err != nil {
				return nil, fmt.Errorf("restore Copilot opaque reasoning")
			}
			if gjson.GetBytes(updated, "reasoning_content").String() == "[reasoning unavailable]" {
				updated, _ = sjson.DeleteBytes(updated, "reasoning_content")
			}
			messages[messageIndex] = updated
			continue
		}
		assistant := []byte(`{"role":"assistant","content":""}`)
		assistant, _ = sjson.SetRawBytes(assistant, "reasoning_opaque", group[0].Raw)
		messages = append(messages[:insertIndex], append([][]byte{assistant}, messages[insertIndex:]...)...)
	}
	var encodedMessages bytes.Buffer
	encodedMessages.WriteByte('[')
	for index, message := range messages {
		if index > 0 {
			encodedMessages.WriteByte(',')
		}
		encodedMessages.Write(message)
	}
	encodedMessages.WriteByte(']')
	updated, err := sjson.SetRawBytes(chatRequest, "messages", encodedMessages.Bytes())
	if err != nil {
		return nil, fmt.Errorf("restore Copilot reasoning history")
	}
	return updated, nil
}

func findOpaqueAnchor(messages [][]byte, anchor copilotOpaqueAnchor) (int, int, error) {
	var matches []int
	for index, message := range messages {
		if gjson.GetBytes(message, "role").String() != "assistant" {
			continue
		}
		if len(anchor.ToolCallIDs) == 0 && anchor.ContentSHA256 == "" {
			continue
		}
		if len(anchor.ToolCallIDs) > 0 && !sameToolCallIDs(message, anchor.ToolCallIDs) {
			continue
		}
		if len(anchor.ToolCalls) > 0 && !sameToolCallAnchors(message, anchor.ToolCalls) {
			continue
		}
		if anchor.ContentSHA256 != "" {
			content, ok := chatContentFingerprint(gjson.GetBytes(message, "content"))
			if !ok || digest([]byte(content)) != anchor.ContentSHA256 {
				continue
			}
		}
		if anchor.PriorUserSHA256 != "" && previousUserFingerprint(messages, index) != anchor.PriorUserSHA256 {
			continue
		}
		matches = append(matches, index)
	}
	if len(matches) == 1 {
		return matches[0], -1, nil
	}
	if len(matches) > 1 {
		return -1, -1, fmt.Errorf("Copilot opaque reasoning assistant anchor is ambiguous")
	}
	if anchor.ContentSHA256 != "" || len(anchor.ToolCallIDs) > 0 {
		return -1, -1, fmt.Errorf("Copilot opaque reasoning assistant anchor was not found")
	}
	if anchor.PriorUserSHA256 == "" {
		return -1, -1, fmt.Errorf("Copilot opaque reasoning assistant anchor is incomplete")
	}
	users := make([]int, 0, 1)
	for index, message := range messages {
		if gjson.GetBytes(message, "role").String() != "user" {
			continue
		}
		content, ok := chatContentFingerprint(gjson.GetBytes(message, "content"))
		if ok && content != "" && digest([]byte(content)) == anchor.PriorUserSHA256 {
			users = append(users, index)
		}
	}
	if len(users) != 1 {
		return -1, -1, fmt.Errorf("Copilot opaque reasoning prior-user anchor is missing or ambiguous")
	}
	userIndex := users[0]
	insertIndex := userIndex + 1
	for insertIndex < len(messages) && gjson.GetBytes(messages[insertIndex], "role").String() == "assistant" {
		if sameToolCallIDs(messages[insertIndex], nil) && gjson.GetBytes(messages[insertIndex], "reasoning_opaque").Exists() {
			return insertIndex, -1, nil
		}
		insertIndex++
	}
	return -1, insertIndex, nil
}

func sameToolCallIDs(message []byte, expected []string) bool {
	calls := gjson.GetBytes(message, "tool_calls")
	if !calls.IsArray() || calls.Array() == nil || len(calls.Array()) != len(expected) {
		return len(expected) == 0 && (!calls.Exists() || len(calls.Array()) == 0)
	}
	for index, call := range calls.Array() {
		id := call.Get("id").String()
		if _, callID, encoded, err := parseClaudeToolID(id); err == nil && encoded {
			id = callID
		}
		if id != expected[index] {
			return false
		}
	}
	return true
}

func sameToolCallAnchors(message []byte, expected []copilotOpaqueToolAnchor) bool {
	calls := gjson.GetBytes(message, "tool_calls")
	if !calls.IsArray() || len(calls.Array()) != len(expected) {
		return false
	}
	for index, call := range calls.Array() {
		function := call.Get("function")
		if expected[index].Name != "" && function.Get("name").String() != expected[index].Name {
			return false
		}
		if expected[index].ArgumentsHash != "" && canonicalJSONDigest(function.Get("arguments").String()) != expected[index].ArgumentsHash {
			return false
		}
	}
	return true
}

func previousUserFingerprint(messages [][]byte, before int) string {
	for index := before - 1; index >= 0; index-- {
		message := gjson.ParseBytes(messages[index])
		if message.Get("role").String() != "user" {
			continue
		}
		content, ok := chatContentFingerprint(message.Get("content"))
		if ok && content != "" {
			return digest([]byte(content))
		}
		return ""
	}
	return ""
}

func responsesOpaqueCarriers(body []byte, model string) ([]copilotOpaqueValue, error) {
	items := gjson.GetBytes(body, "input")
	if !items.IsArray() {
		return nil, nil
	}
	var values []copilotOpaqueValue
	for _, item := range items.Array() {
		if item.Get("type").String() != "reasoning" {
			continue
		}
		encrypted := item.Get("encrypted_content")
		if !encrypted.Exists() || encrypted.Type == gjson.Null || encrypted.String() == "" {
			continue
		}
		if encrypted.Type != gjson.String {
			return nil, fmt.Errorf("cannot translate non-string encrypted reasoning to Copilot Chat")
		}
		value, err := decodeCopilotOpaque(encrypted.String(), model)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func chatResponseOpaqueFromSSE(frame, request []byte, model string) (string, bool, error) {
	_, data, done, err := parseSSEFrame(frame)
	if err != nil || done || len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return "", false, err
	}
	root := gjson.ParseBytes(data)
	choices := root.Get("choices")
	if !choices.IsArray() {
		return "", false, nil
	}
	var raw []byte
	var anchor copilotOpaqueAnchor
	for _, choice := range choices.Array() {
		field := choice.Get("delta.reasoning_opaque")
		if !field.Exists() {
			field = choice.Get("message.reasoning_opaque")
		}
		if !field.Exists() {
			continue
		}
		if choice.Get("index").Int() != 0 {
			return "", false, fmt.Errorf("multiple Chat choices contain opaque reasoning")
		}
		candidate := []byte(field.Raw)
		if !meaningfulOpaqueJSON(candidate) {
			continue
		}
		if raw != nil {
			return "", false, fmt.Errorf("multiple Chat choices contain opaque reasoning")
		}
		raw = candidate
		message := choice.Get("message")
		if !message.Exists() {
			message = choice.Get("delta")
		}
		anchor = copilotAnchorForMessage(message, request)
	}
	if raw == nil {
		return "", false, nil
	}
	carrier, err := encodeCopilotOpaque(raw, model, anchor)
	if err != nil {
		return "", false, err
	}
	return carrier, true, nil
}

func (s *chatOpaqueStreamState) observe(frame, request []byte, model string) error {
	_, data, done, err := parseSSEFrame(frame)
	if err != nil || done || len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return err
	}
	choices := gjson.GetBytes(data, "choices")
	if !choices.IsArray() {
		return nil
	}
	for _, choice := range choices.Array() {
		message := choice.Get("message")
		delta := choice.Get("delta")
		if choice.Get("index").Int() != 0 {
			if meaningfulOpaqueJSON([]byte(choiceOpaqueRaw(message, delta))) {
				return fmt.Errorf("multiple Chat choices contain opaque reasoning")
			}
			continue
		}
		if message.Exists() {
			s.Content.Reset()
			if content, ok := chatContentFingerprint(message.Get("content")); ok {
				s.Content.WriteString(content)
			}
			s.Tools = make(map[int]*chatOpaqueStreamTool)
			if err := s.observeToolCalls(message.Get("tool_calls"), false); err != nil {
				return err
			}
		} else {
			if content := delta.Get("content"); content.Type == gjson.String {
				s.Content.WriteString(content.String())
			}
			if err := s.observeToolCalls(delta.Get("tool_calls"), true); err != nil {
				return err
			}
		}
		raw := choiceOpaqueRaw(message, delta)
		if raw == "" || !meaningfulOpaqueJSON([]byte(raw)) {
			continue
		}
		if s.Raw != nil && !bytes.Equal(s.Raw, []byte(raw)) {
			return fmt.Errorf("Chat reasoning_opaque changed during one stream")
		}
		s.Raw = []byte(raw)
	}
	return nil
}

func choiceOpaqueRaw(message, delta gjson.Result) string {
	if value := message.Get("reasoning_opaque"); value.Exists() {
		return value.Raw
	}
	return delta.Get("reasoning_opaque").Raw
}

func (s *chatOpaqueStreamState) observeToolCalls(calls gjson.Result, fragments bool) error {
	if !calls.IsArray() {
		return nil
	}
	if s.Tools == nil {
		s.Tools = make(map[int]*chatOpaqueStreamTool)
	}
	seen := make(map[int]struct{}, len(calls.Array()))
	for ordinal, call := range calls.Array() {
		index := ordinal
		if indexValue := call.Get("index"); indexValue.Exists() {
			index = int(indexValue.Int())
		} else if fragments {
			if len(calls.Array()) > 1 {
				return fmt.Errorf("Chat tool-call fragments have no stable indexes")
			}
			id := call.Get("id").String()
			if id != "" {
				matched := false
				for existingIndex, existing := range s.Tools {
					if existing.ID == id {
						index = existingIndex
						matched = true
						break
					}
				}
				if !matched && len(s.Tools) == 1 {
					for existingIndex, existing := range s.Tools {
						if existing.ID == "" {
							index = existingIndex
							matched = true
						}
					}
				}
				if !matched && len(s.Tools) > 0 {
					return fmt.Errorf("Chat tool-call fragment cannot be matched to an existing index")
				}
			} else if len(s.Tools) == 1 {
				for existingIndex := range s.Tools {
					index = existingIndex
				}
			} else if len(s.Tools) > 1 {
				return fmt.Errorf("Chat tool-call fragment cannot be matched to an existing index")
			}
		}
		if _, exists := seen[index]; exists {
			return fmt.Errorf("Chat tool-call indexes are duplicated")
		}
		seen[index] = struct{}{}
		tool := s.Tools[index]
		if tool == nil {
			tool = &chatOpaqueStreamTool{}
			s.Tools[index] = tool
		}
		if id := call.Get("id").String(); id != "" {
			tool.ID = id
		}
		function := call.Get("function")
		if name := function.Get("name").String(); name != "" {
			tool.Name = name
		}
		if arguments := function.Get("arguments"); arguments.Type == gjson.String {
			if fragments {
				tool.Arguments.WriteString(arguments.String())
			} else {
				tool.Arguments.Reset()
				tool.Arguments.WriteString(arguments.String())
			}
		}
	}
	return nil
}

func (s *chatOpaqueStreamState) carrier(request []byte, model string) (string, bool, error) {
	if len(s.Raw) == 0 {
		return "", false, nil
	}
	message := map[string]any{"role": "assistant", "content": s.Content.String()}
	indices := make([]int, 0, len(s.Tools))
	for index := range s.Tools {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	calls := make([]any, 0, len(indices))
	for _, index := range indices {
		tool := s.Tools[index]
		if tool.ID == "" {
			continue
		}
		calls = append(calls, map[string]any{
			"id": tool.ID,
			"function": map[string]any{
				"name":      tool.Name,
				"arguments": tool.Arguments.String(),
			},
		})
	}
	if len(calls) > 0 {
		message["tool_calls"] = calls
	}
	messageJSON, err := json.Marshal(message)
	if err != nil {
		return "", false, fmt.Errorf("encode Chat assistant anchor")
	}
	anchor := copilotAnchorForMessage(gjson.ParseBytes(messageJSON), request)
	carrier, err := encodeCopilotOpaque(s.Raw, model, anchor)
	if err != nil {
		return "", false, err
	}
	return carrier, true, nil
}

func chatToResponsesStream(ctx context.Context, model string, original, translated, frame []byte, state *chatOpaqueStreamState) ([][]byte, error) {
	out := registry.TranslateStream(ctx, sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse, model, original, translated, frame, &state.Chat)
	var translatedFrames [][]byte
	for _, responseFrame := range out {
		processed, err := state.processResponseFrame(responseFrame, translated)
		if err != nil {
			return nil, err
		}
		translatedFrames = append(translatedFrames, processed...)
	}
	return translatedFrames, nil
}

func (s *chatOpaqueStreamState) processResponseFrame(frame, request []byte) ([][]byte, error) {
	event, data, done, err := parseSSEFrame(frame)
	if err != nil {
		return nil, err
	}
	if done || len(data) == 0 {
		return [][]byte{append([]byte(nil), frame...)}, nil
	}
	payload, err := decodeObject(data)
	if err != nil {
		return nil, fmt.Errorf("decode translated Responses stream event")
	}
	if event == "" {
		event = stringValue(payload["type"])
	}
	if event != "response.completed" {
		if sequence, ok := payload["sequence_number"].(json.Number); ok {
			if value, err := sequence.Int64(); err == nil && value > s.LastSequence {
				s.LastSequence = value
			}
		}
		return [][]byte{append([]byte(nil), frame...)}, nil
	}
	if s.Completed {
		return nil, nil
	}
	s.Completed = true
	carrier, exists, err := s.carrier(request, s.Model)
	if err != nil {
		return nil, err
	}
	if !exists {
		if sequence, ok := payload["sequence_number"].(json.Number); ok {
			if value, err := sequence.Int64(); err == nil && value > s.LastSequence {
				s.LastSequence = value
			}
		}
		return [][]byte{append([]byte(nil), frame...)}, nil
	}
	responseObject := objectValue(payload["response"])
	output, ok := responseObject["output"].([]any)
	if !ok {
		return nil, fmt.Errorf("translated Responses terminal event has no output array")
	}
	index := len(output)
	responseID := stringValue(responseObject["id"])
	itemID := opaqueResponseItemID(responseID, carrier)
	item := map[string]any{
		"id":                itemID,
		"type":              "reasoning",
		"status":            "completed",
		"summary":           []any{},
		"encrypted_content": carrier,
	}
	output = append(output, item)
	responseObject["output"] = output
	payload["response"] = responseObject

	baseSequence := s.LastSequence + 1
	if terminalSequence, ok := payload["sequence_number"].(json.Number); ok {
		if value, err := terminalSequence.Int64(); err == nil && value > baseSequence {
			baseSequence = value
		}
	}
	added := map[string]any{
		"type":            "response.output_item.added",
		"output_index":    index,
		"sequence_number": baseSequence,
		"item": map[string]any{
			"id":                itemID,
			"type":              "reasoning",
			"status":            "in_progress",
			"summary":           []any{},
			"encrypted_content": carrier,
		},
	}
	doneItem := map[string]any{
		"type":            "response.output_item.done",
		"output_index":    index,
		"sequence_number": baseSequence + 1,
		"item":            item,
	}
	payload["sequence_number"] = baseSequence + 2
	completedJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode translated Responses terminal event")
	}
	addedFrame, err := responseSSE("response.output_item.added", added)
	if err != nil {
		return nil, err
	}
	doneFrame, err := responseSSE("response.output_item.done", doneItem)
	if err != nil {
		return nil, err
	}
	s.LastSequence = baseSequence + 2
	return [][]byte{addedFrame, doneFrame, responseSSEBytes("response.completed", completedJSON)}, nil
}

func responseSSE(event string, payload any) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode Responses SSE event")
	}
	return responseSSEBytes(event, encoded), nil
}

func responseSSEBytes(event string, payload []byte) []byte {
	return []byte("event: " + event + "\ndata: " + string(payload) + "\n\n")
}

func insertOpaqueReasoning(response []byte, carrier string) ([]byte, error) {
	root := gjson.ParseBytes(response)
	output := root.Get("output")
	if !output.IsArray() {
		return nil, fmt.Errorf("translated Responses output has no output array")
	}
	items := make([][]byte, 0, len(output.Array())+1)
	attached := false
	for _, item := range output.Array() {
		itemRaw := []byte(item.Raw)
		if !attached && item.Get("type").String() == "reasoning" {
			var err error
			itemRaw, err = sjson.SetBytes(itemRaw, "encrypted_content", carrier)
			if err != nil {
				return nil, fmt.Errorf("attach Copilot reasoning carrier")
			}
			attached = true
		}
		items = append(items, itemRaw)
	}
	if !attached {
		id := opaqueResponseItemID(root.Get("id").String(), carrier)
		item := []byte(`{"id":"","type":"reasoning","summary":[],"encrypted_content":""}`)
		item, _ = sjson.SetBytes(item, "id", id)
		item, _ = sjson.SetBytes(item, "encrypted_content", carrier)
		items = append(items, item)
	}
	return setRawArray(response, "output", items)
}

func opaqueResponseItemID(responseID, carrier string) string {
	return "rs_cpa_" + digest([]byte(responseID + "\x00" + carrier))[:20]
}

func setRawArray(body []byte, path string, values [][]byte) ([]byte, error) {
	var array bytes.Buffer
	array.WriteByte('[')
	for index, value := range values {
		if index > 0 {
			array.WriteByte(',')
		}
		array.Write(value)
	}
	array.WriteByte(']')
	return sjson.SetRawBytes(body, path, array.Bytes())
}
