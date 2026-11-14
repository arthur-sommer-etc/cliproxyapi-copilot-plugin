package translate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

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
	Thinking   *string             `json:"thinking,omitempty"`
	Anchor     copilotOpaqueAnchor `json:"anchor"`
}

type copilotOpaqueValue struct {
	Version  int
	Raw      []byte
	Thinking *string
	Anchor   copilotOpaqueAnchor
}

type chatOpaqueStreamTool struct {
	ID        string
	Name      string
	Arguments strings.Builder
}

type chatOpaqueStreamState struct {
	Model             string
	Chat              any
	Claude            any
	ID                string
	Created           string
	LastSequence      int64
	Raw               []byte
	Thinking          strings.Builder
	HasThinking       bool
	HasReasoningAlias bool
	PendingCompleted  []byte
	Content           strings.Builder
	Tools             map[int]*chatOpaqueStreamTool
	Completed         bool
	Failure           error
}

func encodeCopilotOpaque(raw []byte, model string, anchor copilotOpaqueAnchor) (string, error) {
	return encodeCopilotThinking(raw, nil, model, anchor)
}

func encodeCopilotThinking(raw []byte, thinking *string, model string, anchor copilotOpaqueAnchor) (string, error) {
	if raw == nil {
		raw = []byte("null")
	}
	if !json.Valid(raw) {
		return "", fmt.Errorf("Copilot reasoning_opaque is not valid JSON")
	}
	if len(anchor.ToolCallIDs) == 0 && anchor.ContentSHA256 == "" && anchor.PriorUserSHA256 == "" {
		return "", fmt.Errorf("Copilot reasoning_opaque has no replay anchor")
	}
	envelope := copilotOpaqueEnvelope{
		Version:    2,
		Provider:   "copilot",
		Endpoint:   "chat/completions",
		Model:      model,
		RawJSONB64: base64.RawURLEncoding.EncodeToString(raw),
		Thinking:   thinking,
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
	if (envelope.Version != 1 && envelope.Version != 2) || envelope.Provider != "copilot" || envelope.Endpoint != "chat/completions" || envelope.Model != model {
		return copilotOpaqueValue{}, fmt.Errorf("Copilot reasoning carrier scope does not match this request")
	}
	if envelope.Version == 1 {
		return copilotOpaqueValue{}, fmt.Errorf("legacy Copilot reasoning carrier cannot safely bind conversation history; start a fresh turn")
	}
	raw, err := base64.RawURLEncoding.DecodeString(envelope.RawJSONB64)
	if err != nil || !json.Valid(raw) {
		return copilotOpaqueValue{}, fmt.Errorf("invalid Copilot reasoning carrier payload")
	}
	if len(envelope.Anchor.ToolCallIDs) == 0 && envelope.Anchor.ContentSHA256 == "" && envelope.Anchor.PriorUserSHA256 == "" {
		return copilotOpaqueValue{}, fmt.Errorf("Copilot reasoning carrier has no replay anchor")
	}
	return copilotOpaqueValue{Version: envelope.Version, Raw: raw, Thinking: envelope.Thinking, Anchor: envelope.Anchor}, nil
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
		for nested := range maps.Values(current) {
			if findClaudeCarrier(nested) {
				return true
			}
		}
	case []any:
		for nested := range slices.Values(current) {
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
		for nested := range maps.Values(current) {
			if findResponsesCarrier(nested) {
				return true
			}
		}
	case []any:
		for nested := range slices.Values(current) {
			if findResponsesCarrier(nested) {
				return true
			}
		}
	}
	return false
}

func copilotOpaqueFromChatResponse(response, request []byte, model string) (string, bool, error) {
	choices := gjson.GetBytes(response, "choices")
	if !choices.IsArray() {
		return "", false, nil
	}
	var raw []byte
	var thinking *string
	var anchor copilotOpaqueAnchor
	found := false
	for choice := range slices.Values(choices.Array()) {
		message := choice.Get("message")
		field := message.Get("reasoning_opaque")
		text := message.Get("reasoning_text")
		if text.Exists() && text.Type != gjson.String && text.Type != gjson.Null {
			return "", false, fmt.Errorf("Chat reasoning_text is not a string")
		}
		if text.Type == gjson.String && !jsonHasSafeStrings(text.Raw) {
			return "", false, fmt.Errorf("Chat reasoning_text has invalid Unicode")
		}
		if text.Type == gjson.String && meaningfulOpaqueJSON([]byte(message.Get("reasoning_content").Raw)) {
			return "", false, fmt.Errorf("Chat response contains multiple reasoning text fields")
		}
		if !meaningfulOpaqueJSON([]byte(field.Raw)) && text.Type != gjson.String {
			continue
		}
		if !json.Valid(response) || !jsonHasSafeStrings(string(response)) || !jsonHasUniqueObjectKeys(string(response)) {
			return "", false, fmt.Errorf("Copilot reasoning response has ambiguous JSON or invalid Unicode")
		}
		if found {
			return "", false, fmt.Errorf("multiple Chat choices contain reasoning")
		}
		if role := message.Get("role"); role.Exists() && role.String() != "assistant" {
			return "", false, fmt.Errorf("Chat reasoning is not attached to an assistant message")
		}
		found = true
		if meaningfulOpaqueJSON([]byte(field.Raw)) {
			raw = []byte(field.Raw)
		}
		if text.Type == gjson.String {
			value := text.String()
			thinking = &value
		}
		anchor = copilotAnchorForMessage(message, request)
	}
	if !found {
		return "", false, nil
	}
	carrier, err := encodeCopilotThinking(raw, thinking, model, anchor)
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
		if fingerprint, ok := assistantContentFingerprint(content); ok {
			anchor.ContentSHA256 = fingerprint
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
	rawDigest := func() string { return digest([]byte("raw\x00" + raw)) }
	if !json.Valid([]byte(raw)) || !jsonHasSafeStrings(raw) || !jsonHasUniqueObjectKeys(raw) {
		return rawDigest()
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return rawDigest()
	}
	value, ok := canonicalJSONNumbers(value)
	if !ok {
		return rawDigest()
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return rawDigest()
	}
	return digest(append([]byte("canonical\x00"), canonical...))
}

// encoding/json replaces malformed UTF-8 and unpaired UTF-16 surrogates.
// Preserve exact bytes when decoding would silently change a string or key.
func jsonHasSafeStrings(raw string) bool {
	if !utf8.ValidString(raw) {
		return false
	}
	inside := false
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case '"':
			inside = !inside
		case '\\':
			if !inside || i+1 >= len(raw) {
				return false
			}
			i++
			if raw[i] != 'u' {
				continue
			}
			if i+4 >= len(raw) {
				return false
			}
			unit, err := strconv.ParseUint(raw[i+1:i+5], 16, 16)
			if err != nil {
				return false
			}
			i += 4
			if unit >= 0xdc00 && unit <= 0xdfff {
				return false
			}
			if unit >= 0xd800 && unit <= 0xdbff {
				if i+6 >= len(raw) || raw[i+1:i+3] != `\u` {
					return false
				}
				low, err := strconv.ParseUint(raw[i+3:i+7], 16, 16)
				if err != nil || low < 0xdc00 || low > 0xdfff {
					return false
				}
				i += 6
			}
		}
	}
	return !inside
}

func validateCopilotSourceJSON(source sdktranslator.Format, body []byte) error {
	switch source {
	case sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude:
	default:
		return nil
	}
	if !json.Valid(body) || !jsonHasSafeStrings(string(body)) || !jsonHasUniqueObjectKeys(string(body)) {
		return fmt.Errorf("Copilot source has ambiguous JSON or invalid Unicode")
	}
	return nil
}

// A duplicate key has no portable JSON meaning. Keep its exact bytes rather
// than accepting the last-value interpretation selected by encoding/json.
func jsonHasUniqueObjectKeys(raw string) bool {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var scan func() bool
	scan = func() bool {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return true
		}
		switch delim {
		case '{':
			keys := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				key, ok := keyToken.(string)
				if err != nil || !ok {
					return false
				}
				if _, exists := keys[key]; exists {
					return false
				}
				keys[key] = struct{}{}
				if !scan() {
					return false
				}
			}
		case '[':
			for decoder.More() {
				if !scan() {
					return false
				}
			}
		default:
			return false
		}
		_, err = decoder.Token()
		return err == nil
	}
	return scan()
}

func canonicalJSONNumbers(value any) (any, bool) {
	switch current := value.(type) {
	case json.Number:
		number, ok := canonicalJSONNumber(string(current))
		return json.Number(number), ok
	case []any:
		for index, item := range current {
			normalized, ok := canonicalJSONNumbers(item)
			if !ok {
				return nil, false
			}
			current[index] = normalized
		}
	case map[string]any:
		for key, item := range current {
			normalized, ok := canonicalJSONNumbers(item)
			if !ok {
				return nil, false
			}
			current[key] = normalized
		}
	}
	return value, true
}

func canonicalJSONNumber(raw string) (string, bool) {
	const maxDigits = 4096
	const maxExponent = 1000000
	sign := ""
	if strings.HasPrefix(raw, "-") {
		sign = "-"
		raw = raw[1:]
	}
	mantissa := raw
	exponent := 0
	if index := strings.IndexAny(raw, "eE"); index >= 0 {
		mantissa = raw[:index]
		exponentText := raw[index+1:]
		negative := strings.HasPrefix(exponentText, "-")
		exponentText = strings.TrimPrefix(strings.TrimPrefix(exponentText, "-"), "+")
		if len(exponentText) > 7 {
			return "", false
		}
		for _, digit := range exponentText {
			exponent = exponent*10 + int(digit-'0')
			if exponent > maxExponent {
				return "", false
			}
		}
		if negative {
			exponent = -exponent
		}
	}
	fractionDigits := 0
	if point := strings.IndexByte(mantissa, '.'); point >= 0 {
		fractionDigits = len(mantissa) - point - 1
	}
	if len(mantissa) > maxDigits+1 {
		return "", false
	}
	digits := strings.ReplaceAll(mantissa, ".", "")
	if len(digits) > maxDigits {
		return "", false
	}
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		return "0", true
	}
	trimmed := strings.TrimRight(digits, "0")
	exponent += len(digits) - len(trimmed) - fractionDigits
	if exponent < -maxExponent || exponent > maxExponent {
		return "", false
	}
	if exponent == 0 {
		return sign + trimmed, true
	}
	return fmt.Sprintf("%s%se%d", sign, trimmed, exponent), true
}

func chatContentFingerprint(content gjson.Result) (string, bool) {
	if content.Type == gjson.String {
		return content.String(), true
	}
	if !content.IsArray() {
		return "", false
	}
	var text strings.Builder
	for part := range slices.Values(content.Array()) {
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

func assistantContentFingerprint(content gjson.Result) (string, bool) {
	if !jsonHasSafeStrings(content.Raw) || !jsonHasUniqueObjectKeys(content.Raw) {
		return digest([]byte("assistant-raw\x00" + content.Raw)), true
	}
	text, ok := chatContentFingerprint(content)
	if !ok || text == "" {
		return "", false
	}
	return digest([]byte(text)), true
}

func priorChatUserFingerprint(request []byte) string {
	messages := gjson.GetBytes(request, "messages")
	if !messages.IsArray() {
		return ""
	}
	var last string
	for message := range slices.Values(messages.Array()) {
		if message.Get("role").String() != "user" {
			continue
		}
		last, _ = chatUserContentFingerprint(message.Get("content"))
	}
	return last
}

func chatUserContentFingerprint(content gjson.Result) (string, bool) {
	if !content.Exists() || content.Type == gjson.Null {
		return "", false
	}
	if !json.Valid([]byte(content.Raw)) || !jsonHasSafeStrings(content.Raw) || !jsonHasUniqueObjectKeys(content.Raw) {
		return digest([]byte("user-raw\x00" + content.Raw)), true
	}
	if text, ok := chatContentFingerprint(content); ok {
		if text == "" {
			return "", false
		}
		return digest([]byte("user-text\x00" + text)), true
	}
	return digest([]byte("user-json\x00" + canonicalJSONDigest(content.Raw))), true
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
		for input := range slices.Values(inputs.Array()) {
			switch input.Get("type").String() {
			case "message":
				contents := input.Get("content")
				if !contents.IsArray() {
					continue
				}
				for content := range slices.Values(contents.Array()) {
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
		for tool := range slices.Values(tools.Array()) {
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
	priorUser := ""
	var priorUserItem gjson.Result
	for item := range slices.Values(items.Array()) {
		if item.Get("role").String() == "user" {
			priorUserItem = item
			priorUser, _ = chatUserContentFingerprint(item.Get("content"))
		}
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
		if value.Thinking != nil {
			if err := validateCopilotThinkingSummary(item, *value.Thinking); err != nil {
				return nil, err
			}
		}
		if value.Version >= 2 && value.Thinking != nil && len(value.Anchor.ToolCallIDs) == 0 && value.Anchor.ContentSHA256 == "" {
			if _, textOnly := chatContentFingerprint(priorUserItem.Get("content")); priorUserItem.Exists() && !textOnly {
				// Responses and Chat use different shapes for image and other media
				// parts. Compare the same translated representation used at capture.
				single := []byte(`{"input":[]}`)
				single, err := sjson.SetRawBytes(single, "input.0", []byte(priorUserItem.Raw))
				if err != nil {
					return nil, fmt.Errorf("prepare Copilot reasoning user anchor")
				}
				translated, err := request(sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAI, model, single, false)
				if err != nil {
					return nil, fmt.Errorf("translate Copilot reasoning user anchor: %w", err)
				}
				priorUser = priorChatUserFingerprint(translated)
			}
			if value.Anchor.PriorUserSHA256 != priorUser {
				return nil, fmt.Errorf("Copilot reasoning item does not follow its original user turn")
			}
		}
		values = append(values, value)
	}
	if len(values) == 0 {
		return chatRequest, nil
	}
	groups := make(map[string][]copilotOpaqueValue)
	order := make([]string, 0)
	for value := range slices.Values(values) {
		anchorJSON, _ := json.Marshal(value.Anchor)
		key := string(anchorJSON)
		if _, exists := groups[key]; !exists {
			order = append(order, key)
		}
		groups[key] = append(groups[key], value)
	}
	messages := make([][]byte, 0, len(gjson.GetBytes(chatRequest, "messages").Array())+len(groups))
	messageResults := gjson.GetBytes(chatRequest, "messages").Array()
	for message := range slices.Values(messageResults) {
		messages = append(messages, []byte(message.Raw))
	}
	for key := range slices.Values(order) {
		group := groups[key]
		if len(group) != 1 {
			return nil, fmt.Errorf("multiple Copilot opaque reasoning items share one assistant anchor")
		}
		if group[0].Version == 1 && len(group[0].Anchor.ToolCallIDs) > 0 {
			return nil, fmt.Errorf("legacy Copilot reasoning carrier cannot safely bind tool arguments")
		}
		messageIndex, insertIndex, err := findOpaqueAnchor(messages, group[0])
		if err != nil {
			return nil, err
		}
		if messageIndex >= 0 {
			if group[0].Thinking != nil && len(group[0].Anchor.ToolCallIDs) > 0 {
				// A normalized unsigned reasoning item can be relocated after the
				// tool results by the generic translator. Its original position is
				// then ambiguous, so do not silently retain it beside this carrier.
				for i := messageIndex + 1; i < len(messages); i++ {
					role := gjson.GetBytes(messages[i], "role").String()
					if role == "user" {
						break
					}
					if role != "assistant" {
						continue
					}
					if sameToolCallIDs(messages[i], nil) && gjson.GetBytes(messages[i], "content").String() == "" && (gjson.GetBytes(messages[i], "reasoning_content").Exists() || gjson.GetBytes(messages[i], "reasoning_text").Exists()) {
						alias := gjson.GetBytes(messages[i], "reasoning_content")
						if alias.Type != gjson.String || alias.String() != *group[0].Thinking || gjson.GetBytes(messages[i], "reasoning_text").Exists() || gjson.GetBytes(messages[i], "reasoning_opaque").Exists() {
							return nil, fmt.Errorf("Copilot reasoning following tool results is ambiguous")
						}
						messages = append(messages[:i], messages[i+1:]...)
					}
					break
				}
			}
			updated, err := restoreCopilotThinkingFields(messages[messageIndex], group[0])
			if err != nil {
				return nil, err
			}
			if gjson.GetBytes(updated, "reasoning_content").String() == "[reasoning unavailable]" {
				updated, _ = sjson.DeleteBytes(updated, "reasoning_content")
			}
			messages[messageIndex] = updated
			continue
		}
		assistant := []byte(`{"role":"assistant","content":""}`)
		assistant, err = restoreCopilotThinkingFields(assistant, group[0])
		if err != nil {
			return nil, err
		}
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

func validateCopilotThinkingSummary(item gjson.Result, expected string) error {
	summary := item.Get("summary")
	if !summary.IsArray() || len(summary.Array()) == 0 || meaningfulOpaqueJSON([]byte(item.Get("content").Raw)) {
		return fmt.Errorf("Copilot reasoning text does not match its carrier")
	}
	var text strings.Builder
	for part := range slices.Values(summary.Array()) {
		value := part.Get("text")
		if !part.IsObject() || part.Get("type").String() != "summary_text" || value.Type != gjson.String || !jsonHasSafeStrings(value.Raw) {
			return fmt.Errorf("Copilot reasoning text does not match its carrier")
		}
		text.WriteString(value.String())
	}
	if text.String() != expected {
		return fmt.Errorf("Copilot reasoning text does not match its carrier")
	}
	return nil
}

func restoreCopilotThinkingFields(message []byte, value copilotOpaqueValue) ([]byte, error) {
	var err error
	if value.Thinking != nil {
		native := gjson.GetBytes(message, "reasoning_text")
		alias := gjson.GetBytes(message, "reasoning_content")
		opaque := gjson.GetBytes(message, "reasoning_opaque")
		if (native.Exists() && (native.Type != gjson.String || native.String() != *value.Thinking)) ||
			(alias.Exists() && (alias.Type != gjson.String || (alias.String() != "" && alias.String() != "[reasoning unavailable]" && alias.String() != *value.Thinking))) ||
			(opaque.Exists() && (meaningfulOpaqueJSON(value.Raw) != meaningfulOpaqueJSON([]byte(opaque.Raw)) || (meaningfulOpaqueJSON(value.Raw) && opaque.Raw != string(value.Raw)))) {
			return nil, fmt.Errorf("Copilot reasoning fields conflict with their carrier")
		}
	}
	if meaningfulOpaqueJSON(value.Raw) {
		message, err = sjson.SetRawBytes(message, "reasoning_opaque", value.Raw)
		if err != nil {
			return nil, fmt.Errorf("restore Copilot opaque reasoning")
		}
	}
	if value.Thinking != nil {
		message, err = sjson.DeleteBytes(message, "reasoning_content")
		if err != nil {
			return nil, fmt.Errorf("remove translated reasoning alias")
		}
		message, err = sjson.SetBytes(message, "reasoning_text", *value.Thinking)
		if err != nil {
			return nil, fmt.Errorf("restore Copilot reasoning text")
		}
	}
	return message, nil
}

func findOpaqueAnchor(messages [][]byte, value copilotOpaqueValue) (int, int, error) {
	anchor := value.Anchor
	var matches []int
	for index, message := range messages {
		if gjson.GetBytes(message, "role").String() != "assistant" {
			continue
		}
		if len(anchor.ToolCallIDs) == 0 && anchor.ContentSHA256 == "" {
			reasoning := gjson.GetBytes(message, "reasoning_content").String()
			matchesThinking := value.Thinking != nil && reasoning == *value.Thinking && sameToolCallIDs(message, nil) && gjson.GetBytes(message, "content").String() == ""
			if value.Thinking != nil && reasoning != "" && reasoning != "[reasoning unavailable]" && !matchesThinking && sameToolCallIDs(message, nil) && gjson.GetBytes(message, "content").String() == "" && previousUserFingerprint(messages, index) == anchor.PriorUserSHA256 {
				return -1, -1, fmt.Errorf("Copilot reasoning fields conflict with their carrier")
			}
			nativeText := gjson.GetBytes(message, "reasoning_text")
			matchesNative := nativeText.Exists() && value.Thinking != nil && nativeText.Type == gjson.String && nativeText.String() == *value.Thinking && sameToolCallIDs(message, nil) && gjson.GetBytes(message, "content").String() == ""
			if nativeText.Exists() && !matchesNative && value.Thinking != nil && sameToolCallIDs(message, nil) && gjson.GetBytes(message, "content").String() == "" && previousUserFingerprint(messages, index) == anchor.PriorUserSHA256 {
				return -1, -1, fmt.Errorf("Copilot reasoning fields conflict with their carrier")
			}
			if matchesNative {
				existingOpaque := gjson.GetBytes(message, "reasoning_opaque")
				if meaningfulOpaqueJSON(value.Raw) != meaningfulOpaqueJSON([]byte(existingOpaque.Raw)) || (meaningfulOpaqueJSON(value.Raw) && existingOpaque.Raw != string(value.Raw)) {
					return -1, -1, fmt.Errorf("Copilot reasoning fields conflict with their carrier")
				}
			}
			if anchor.PriorUserSHA256 != "" && (reasoning == "[reasoning unavailable]" || matchesThinking || matchesNative) && previousUserFingerprint(messages, index) == anchor.PriorUserSHA256 {
				matches = append(matches, index)
			}
			continue
		}
		if len(anchor.ToolCallIDs) > 0 && !sameToolCallIDs(message, anchor.ToolCallIDs) {
			continue
		}
		if len(anchor.ToolCalls) > 0 && !sameToolCallAnchors(message, anchor.ToolCalls) {
			continue
		}
		if anchor.ContentSHA256 != "" {
			content, ok := assistantContentFingerprint(gjson.GetBytes(message, "content"))
			if !ok || content != anchor.ContentSHA256 {
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
	userIndex, matchedUserIndex, userMatchCount := -1, -1, 0
	for index, message := range messages {
		if gjson.GetBytes(message, "role").String() != "user" {
			continue
		}
		userIndex = index
		fingerprint, ok := chatUserContentFingerprint(gjson.GetBytes(message, "content"))
		if ok && fingerprint == anchor.PriorUserSHA256 {
			matchedUserIndex = index
			userMatchCount++
		}
	}
	if userMatchCount != 1 || ((value.Version == 1 || value.Thinking == nil) && matchedUserIndex != userIndex) {
		return -1, -1, fmt.Errorf("Copilot opaque reasoning prior-user anchor is missing or ambiguous")
	}
	userIndex = matchedUserIndex
	insertIndex := userIndex + 1
	for insertIndex < len(messages) && gjson.GetBytes(messages[insertIndex], "role").String() == "assistant" {
		if sameToolCallIDs(messages[insertIndex], nil) && gjson.GetBytes(messages[insertIndex], "reasoning_opaque").Exists() {
			if value.Thinking != nil {
				return -1, -1, fmt.Errorf("Copilot reasoning fields conflict with their carrier")
			}
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
		if message.Get("role").String() == "user" {
			fingerprint, _ := chatUserContentFingerprint(message.Get("content"))
			return fingerprint
		}
	}
	return ""
}

func responsesOpaqueCarriers(body []byte, model string) ([]copilotOpaqueValue, error) {
	items := gjson.GetBytes(body, "input")
	if !items.IsArray() {
		return nil, nil
	}
	var values []copilotOpaqueValue
	for item := range slices.Values(items.Array()) {
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
		if value.Thinking != nil {
			if err := validateCopilotThinkingSummary(item, *value.Thinking); err != nil {
				return nil, err
			}
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
	for choice := range slices.Values(choices.Array()) {
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
		if meaningfulOpaqueJSON(candidate) {
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
	// Reasoning may arrive after any preceding delta. Validate each event before
	// it can be translated into a lossy assistant or tool-call anchor.
	if !json.Valid(data) || !jsonHasSafeStrings(string(data)) || !jsonHasUniqueObjectKeys(string(data)) {
		return fmt.Errorf("Copilot Chat stream has ambiguous JSON or invalid Unicode")
	}
	if id := gjson.GetBytes(data, "id").String(); id != "" {
		s.ID = id
	}
	if created := gjson.GetBytes(data, "created"); created.Exists() {
		s.Created = created.Raw
	}
	choices := gjson.GetBytes(data, "choices")
	if !choices.IsArray() {
		return nil
	}
	for choice := range slices.Values(choices.Array()) {
		message := choice.Get("message")
		delta := choice.Get("delta")
		if choice.Get("index").Int() != 0 {
			if meaningfulOpaqueJSON([]byte(choiceOpaqueRaw(message, delta))) || message.Get("reasoning_text").Type == gjson.String || delta.Get("reasoning_text").Type == gjson.String {
				return fmt.Errorf("multiple Chat choices contain opaque reasoning")
			}
			continue
		}
		text := delta.Get("reasoning_text")
		if message.Exists() {
			text = message.Get("reasoning_text")
		}
		if text.Exists() && text.Type != gjson.String && text.Type != gjson.Null {
			return fmt.Errorf("Chat reasoning_text is not a string")
		}
		if text.Type == gjson.String && !jsonHasSafeStrings(text.Raw) {
			return fmt.Errorf("Chat reasoning_text has invalid Unicode")
		}
		alias := delta.Get("reasoning_content")
		if message.Exists() {
			alias = message.Get("reasoning_content")
		}
		if meaningfulOpaqueJSON([]byte(alias.Raw)) {
			s.HasReasoningAlias = true
		}
		if text.Type == gjson.String {
			if message.Exists() {
				s.Thinking.Reset()
			}
			s.HasThinking = true
			s.Thinking.WriteString(text.String())
		}
		if s.HasThinking && s.HasReasoningAlias {
			return fmt.Errorf("Chat stream contains multiple reasoning text fields")
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
	if len(s.Raw) == 0 && !s.HasThinking {
		return "", false, nil
	}
	message := map[string]any{"role": "assistant", "content": s.Content.String()}
	indices := make([]int, 0, len(s.Tools))
	for index := range s.Tools {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	calls := make([]any, 0, len(indices))
	for index := range slices.Values(indices) {
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
	var thinking *string
	if s.HasThinking {
		text := s.Thinking.String()
		thinking = &text
	}
	carrier, err := encodeCopilotThinking(s.Raw, thinking, model, anchor)
	if err != nil {
		return "", false, err
	}
	return carrier, true, nil
}

func chatToResponsesStream(ctx context.Context, model string, original, translated, frame []byte, state *chatOpaqueStreamState) ([][]byte, error) {
	out := registry.TranslateStream(ctx, sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse, model, original, translated, frame, &state.Chat)
	var translatedFrames [][]byte
	for responseFrame := range slices.Values(out) {
		event, data, _, err := parseSSEFrame(responseFrame)
		if err != nil {
			return nil, err
		}
		if event == "response.completed" || (event == "" && gjson.GetBytes(data, "type").String() == "response.completed") {
			if state.PendingCompleted != nil {
				return nil, fmt.Errorf("Chat stream emitted multiple completed responses")
			}
			state.PendingCompleted = append([]byte(nil), responseFrame...)
			continue
		}
		processed, err := state.processResponseFrame(responseFrame, translated)
		if err != nil {
			return nil, err
		}
		translatedFrames = append(translatedFrames, processed...)
	}
	_, _, done, err := parseSSEFrame(frame)
	if err != nil {
		return nil, err
	}
	if done && state.PendingCompleted == nil && !state.Completed {
		return nil, fmt.Errorf("Chat stream ended without a translated completion")
	}
	if done && state.PendingCompleted != nil {
		processed, err := state.processResponseFrame(state.PendingCompleted, translated)
		if err != nil {
			return nil, err
		}
		state.PendingCompleted = nil
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
	summary := []any{}
	if s.HasThinking {
		summary = append(summary, map[string]any{"type": "summary_text", "text": s.Thinking.String()})
	}
	item := map[string]any{
		"id":                itemID,
		"type":              "reasoning",
		"status":            "completed",
		"summary":           summary,
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

func insertOpaqueReasoning(response []byte, carrier, model string) ([]byte, error) {
	value, err := decodeCopilotOpaque(carrier, model)
	if err != nil {
		return nil, err
	}
	summary := []map[string]string{}
	if value.Thinking != nil {
		summary = append(summary, map[string]string{"type": "summary_text", "text": *value.Thinking})
	}
	root := gjson.ParseBytes(response)
	output := root.Get("output")
	if output.Exists() && !output.IsArray() {
		return nil, fmt.Errorf("translated Responses output is not an array")
	}
	items := make([][]byte, 0, len(output.Array())+1)
	attached := false
	for item := range slices.Values(output.Array()) {
		itemRaw := []byte(item.Raw)
		if !attached && item.Get("type").String() == "reasoning" {
			var err error
			itemRaw, err = sjson.SetBytes(itemRaw, "encrypted_content", carrier)
			if err != nil {
				return nil, fmt.Errorf("attach Copilot reasoning carrier")
			}
			if value.Thinking != nil {
				itemRaw, err = sjson.SetBytes(itemRaw, "summary", summary)
				if err != nil {
					return nil, fmt.Errorf("attach Copilot reasoning text")
				}
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
		if value.Thinking != nil {
			item, _ = sjson.SetBytes(item, "summary", summary)
		}
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

// RestoreCopilotReasoningFromOriginal restores authenticated Chat reasoning from
// an original client history onto the already translated host payload.
func RestoreCopilotReasoningFromOriginal(source, model string, original, chatRequest []byte, stream bool) ([]byte, error) {
	var responses []byte
	var err error
	if err = validateCopilotSourceJSON(sdktranslator.FromString(source), original); err != nil {
		return nil, err
	}
	switch sdktranslator.FromString(source) {
	case sdktranslator.FormatClaude:
		responses, err = claudeRequestToResponses(model, original, stream)
	case sdktranslator.FormatOpenAIResponse:
		responses = original
	default:
		return chatRequest, nil
	}
	if err != nil {
		return nil, err
	}
	return restoreCopilotOpaqueToChat(chatRequest, responses, model)
}
