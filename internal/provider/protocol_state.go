package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
)

const (
	reasoningReplayTTL        = 15 * time.Minute
	maxReasoningReplayEntries = 64
	maxReasoningReplayRecord  = 256 * 1024
	maxReasoningReplayCache   = 4 * 1024 * 1024
	maxProtocolIdentityLength = 256
)

var claudeSessionSuffix = regexp.MustCompile(`_session_([a-fA-F0-9-]+)$`)

type reasoningReplayEntry struct {
	ScopeKey      string
	CallIDs       []string
	FunctionCalls []reasoningReplayFunctionCall
	Reasoning     []json.RawMessage
	CreatedAt     time.Time
	EncodedLength int
}

type reasoningReplayFunctionCall struct {
	ItemID    string
	CallID    string
	Name      string
	Arguments string
}

func protocolSessionIdentity(payload []byte, headers http.Header, metadata map[string]any) (string, string) {
	sessionID := executionSessionIdentityFromMetadata(metadata)
	if sessionID == "" {
		sessionID = firstProtocolHeader(headers, "X-Claude-Code-Session-Id", "Session-Id", "Session_id", "session_id", "X-Codex-Session-Id")
	}
	if sessionID == "" {
		sessionID = sessionIdentityFromPayload(payload)
	}
	if sessionID == "" {
		sessionID = sessionIdentityFromMetadata(metadata)
	}
	sessionID = boundedProtocolIdentity(sessionID)
	if sessionID == "" {
		return "", ""
	}
	agentID := firstProtocolHeader(headers, "X-Claude-Code-Agent-Id")
	if agentID == "" {
		agentID = firstProtocolMetadata(metadata, "agent_id", "agentId", "claude_code_agent_id")
	}
	if agentID == "" {
		agentID = "main"
	}
	return sessionID, boundedProtocolIdentity(agentID)
}

func executionSessionIdentityFromMetadata(metadata map[string]any) string {
	if metadata == nil {
		return ""
	}
	executionID, _ := metadata[cliproxyexecutor.ExecutionSessionMetadataKey].(string)
	normalized := cliproxysession.NormalizeExplicitID(executionID)
	if normalized == "" {
		return ""
	}
	return cliproxysession.BoundSessionIdentity("execution:" + normalized)
}

func firstProtocolHeader(headers http.Header, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(headers.Get(name)); value != "" {
			return value
		}
		for key, values := range headers {
			if !strings.EqualFold(key, name) {
				continue
			}
			for _, value := range values {
				if value = strings.TrimSpace(value); value != "" {
					return value
				}
			}
		}
	}
	return ""
}

func sessionIdentityFromPayload(payload []byte) string {
	var root map[string]json.RawMessage
	if json.Unmarshal(payload, &root) != nil {
		return ""
	}
	var metadata map[string]json.RawMessage
	if json.Unmarshal(root["metadata"], &metadata) == nil {
		if userID := rawString(metadata["user_id"]); userID != "" {
			if sessionID := sessionIDFromUserID(userID); sessionID != "" {
				return sessionID
			}
		}
		for _, name := range []string{"session_id", "conversation_id", "conversationId"} {
			if sessionID := rawString(metadata[name]); sessionID != "" {
				return sessionID
			}
		}
	}
	for _, name := range []string{"session_id", "conversation_id", "conversationId"} {
		if sessionID := rawString(root[name]); sessionID != "" {
			return sessionID
		}
	}
	return ""
}

func sessionIdentityFromMetadata(metadata map[string]any) string {
	for _, name := range []string{"canonical_session_id", "session_id", "sessionId", "conversation_id", "conversationId"} {
		if value, ok := metadata[name].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	if value, ok := metadata["user_id"].(string); ok {
		return sessionIDFromUserID(value)
	}
	return ""
}

func firstProtocolMetadata(metadata map[string]any, names ...string) string {
	for _, name := range names {
		if value, ok := metadata[name].(string); ok {
			if value = boundedProtocolIdentity(value); value != "" {
				return value
			}
		}
	}
	return ""
}

func sessionIDFromUserID(userID string) string {
	if matches := claudeSessionSuffix.FindStringSubmatch(userID); len(matches) == 2 {
		return matches[1]
	}
	if strings.HasPrefix(userID, "session_") {
		return strings.TrimPrefix(userID, "session_")
	}
	var identity struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal([]byte(userID), &identity) == nil {
		return identity.SessionID
	}
	return ""
}

func rawString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

func boundedProtocolIdentity(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxProtocolIdentityLength || strings.ContainsAny(value, "\r\n\x00") {
		return ""
	}
	return value
}

func protocolScopeKey(authID string, storage authStorage, model, apiBaseURL, endpoint, sessionID, agentID string, generation uint64) string {
	if sessionID == "" {
		return ""
	}
	root, err := validateContinuityKeyring(storage, authID)
	if err != nil {
		return ""
	}
	rootFingerprint := sha256.Sum256(root)
	values := []string{
		"cpa-copilot-bridge/continuity/replay/v1",
		strings.TrimSpace(authID),
		fmt.Sprint(storage.GitHubUserID),
		storage.ContinuityKeyring.KeyID,
		hex.EncodeToString(rootFingerprint[:]),
		strings.ToLower(strings.TrimSpace(model)),
		normalizeContinuityAPIBase(apiBaseURL),
		endpoint,
		sessionID,
		agentID,
		strconv.FormatUint(generation, 10),
	}
	sum := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return strconv.FormatUint(generation, 10) + ":" + hex.EncodeToString(sum[:])
}

func (s *Service) protocolScopeIsCurrent(scopeKey string) bool {
	generation, ok := protocolScopeGeneration(scopeKey)
	_, current := s.configSnapshot()
	return ok && generation == current
}

func protocolScopeGeneration(scopeKey string) (uint64, bool) {
	generationText, _, ok := strings.Cut(scopeKey, ":")
	if !ok {
		return 0, false
	}
	generation, err := strconv.ParseUint(generationText, 10, 64)
	return generation, err == nil
}

func explicitPromptCacheKey(payload []byte, metadata map[string]any) string {
	var root map[string]json.RawMessage
	if json.Unmarshal(payload, &root) == nil {
		if key := rawString(root["prompt_cache_key"]); strings.TrimSpace(key) != "" {
			return key
		}
		var nested map[string]json.RawMessage
		if json.Unmarshal(root["metadata"], &nested) == nil {
			if key := rawString(nested["prompt_cache_key"]); strings.TrimSpace(key) != "" {
				return key
			}
		}
	}
	if key, ok := metadata["prompt_cache_key"].(string); ok && strings.TrimSpace(key) != "" {
		return key
	}
	return ""
}

func derivedPromptCacheKey(scopeKey string) string {
	if scopeKey == "" {
		return ""
	}
	return "cpa-" + scopeKey
}

func setPromptCacheKey(payload []byte, key string) ([]byte, error) {
	if key == "" {
		return payload, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(payload, &root); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(key)
	if err != nil {
		return nil, err
	}
	root["prompt_cache_key"] = encoded
	return json.Marshal(root)
}

func (s *Service) restoreReasoningReplay(scopeKey string, original, translated []byte) []byte {
	if scopeKey == "" || !s.protocolScopeIsCurrent(scopeKey) || len(original) == 0 || len(translated) == 0 || !s.Config().ReasoningReplay {
		return translated
	}
	entries := s.reasoningReplayForScope(scopeKey)
	for _, entry := range entries {
		if !assistantMessageMatchesCalls(original, entry.CallIDs) {
			continue
		}
		if restored, ok := insertReasoningBeforeCalls(translated, entry.CallIDs, entry.Reasoning); ok {
			return restored
		}
	}
	return translated
}

func (s *Service) restoreNativeResponsesReplay(scopeKey string, translated []byte) []byte {
	if scopeKey == "" || !s.protocolScopeIsCurrent(scopeKey) || len(translated) == 0 || !s.Config().ReasoningReplay {
		return translated
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(translated, &root) != nil {
		return translated
	}
	var input []json.RawMessage
	if json.Unmarshal(root["input"], &input) != nil {
		return translated
	}
	entries := s.reasoningReplayForScope(scopeKey)
	if len(entries) == 0 {
		return translated
	}
	changed := false
	for index, rawItem := range input {
		var item map[string]json.RawMessage
		if json.Unmarshal(rawItem, &item) != nil {
			return translated
		}
		var kind string
		if json.Unmarshal(item["type"], &kind) != nil {
			continue
		}
		var restoredID string
		switch kind {
		case "function_call":
			if _, exists := item["id"]; exists {
				continue
			}
			var call reasoningReplayFunctionCall
			call.CallID = rawString(item["call_id"])
			call.Name = rawString(item["name"])
			if json.Unmarshal(item["arguments"], &call.Arguments) != nil {
				continue
			}
			restoredID = uniqueNativeFunctionID(entries, call)
		case "reasoning":
			if _, exists := item["id"]; exists {
				continue
			}
			encrypted := rawString(item["encrypted_content"])
			if encrypted != "" {
				restoredID = uniqueNativeReasoningID(entries, encrypted)
			}
		}
		if restoredID == "" {
			continue
		}
		encodedID, err := json.Marshal(restoredID)
		if err != nil {
			continue
		}
		item["id"] = encodedID
		input[index], err = json.Marshal(item)
		if err == nil {
			changed = true
		}
	}
	if !changed {
		return translated
	}
	encodedInput, err := json.Marshal(input)
	if err != nil {
		return translated
	}
	root["input"] = encodedInput
	encoded, err := json.Marshal(root)
	if err != nil {
		return translated
	}
	return encoded
}

func uniqueNativeFunctionID(entries []reasoningReplayEntry, request reasoningReplayFunctionCall) string {
	if request.CallID == "" || request.Name == "" {
		return ""
	}
	var itemID string
	for _, entry := range entries {
		for _, call := range entry.FunctionCalls {
			if call.CallID != request.CallID || call.Name != request.Name || call.Arguments != request.Arguments {
				continue
			}
			if itemID != "" && itemID != call.ItemID {
				return ""
			}
			itemID = call.ItemID
		}
	}
	return itemID
}

func uniqueNativeReasoningID(entries []reasoningReplayEntry, encryptedContent string) string {
	var itemID string
	for _, entry := range entries {
		for _, rawItem := range entry.Reasoning {
			var item struct {
				ID               string `json:"id"`
				EncryptedContent string `json:"encrypted_content"`
			}
			if json.Unmarshal(rawItem, &item) != nil || item.EncryptedContent != encryptedContent || item.ID == "" {
				continue
			}
			if itemID != "" && itemID != item.ID {
				return ""
			}
			itemID = item.ID
		}
	}
	return itemID
}

func assistantMessageMatchesCalls(original []byte, expected []string) bool {
	var request struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(original, &request) != nil {
		return false
	}
	for _, message := range request.Messages {
		if !strings.EqualFold(message.Role, "assistant") {
			continue
		}
		var blocks []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if json.Unmarshal(message.Content, &blocks) != nil {
			continue
		}
		callIDs := make([]string, 0, len(expected))
		missingThinking := true
		for _, block := range blocks {
			switch block.Type {
			case "thinking", "redacted_thinking":
				missingThinking = false
			case "tool_use", "server_tool_use":
				if block.ID != "" {
					_, callID, decoded := translate.DecodeClaudeToolIDs(block.ID)
					if decoded {
						callIDs = append(callIDs, callID)
					} else {
						callIDs = append(callIDs, block.ID)
					}
				}
			}
		}
		if missingThinking && equalStrings(callIDs, expected) {
			return true
		}
	}
	return false
}

func insertReasoningBeforeCalls(translated []byte, callIDs []string, reasoning []json.RawMessage) ([]byte, bool) {
	var root map[string]json.RawMessage
	if json.Unmarshal(translated, &root) != nil || len(callIDs) == 0 || len(reasoning) == 0 {
		return nil, false
	}
	var input []json.RawMessage
	if json.Unmarshal(root["input"], &input) != nil {
		return nil, false
	}
	indices := make([]int, len(callIDs))
	lastIndex := -1
	for callIndex, callID := range callIDs {
		found := -1
		for inputIndex, rawItem := range input {
			var item struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
			}
			if json.Unmarshal(rawItem, &item) == nil && item.Type == "function_call" && item.CallID == callID {
				if found >= 0 {
					return nil, false
				}
				found = inputIndex
			}
		}
		if found <= lastIndex || found < 0 {
			return nil, false
		}
		indices[callIndex] = found
		lastIndex = found
	}
	insertAt := indices[0]
	out := make([]json.RawMessage, 0, len(input)+len(reasoning))
	out = append(out, input[:insertAt]...)
	for _, item := range reasoning {
		out = append(out, append(json.RawMessage(nil), item...))
	}
	out = append(out, input[insertAt:]...)
	encodedInput, err := json.Marshal(out)
	if err != nil {
		return nil, false
	}
	root["input"] = encodedInput
	encoded, err := json.Marshal(root)
	return encoded, err == nil
}

func (s *Service) recordReasoningReplay(scopeKey string, response []byte) {
	generation, validScope := protocolScopeGeneration(scopeKey)
	if scopeKey == "" || !validScope || len(response) > maxReasoningReplayRecord {
		return
	}
	var root struct {
		Status string            `json:"status"`
		Output []json.RawMessage `json:"output"`
	}
	if json.Unmarshal(response, &root) != nil || root.Status != "completed" {
		return
	}
	entry := reasoningReplayEntry{ScopeKey: scopeKey, CallIDs: []string{}, FunctionCalls: []reasoningReplayFunctionCall{}, Reasoning: []json.RawMessage{}}
	seenCalls := make(map[string]struct{})
	for _, rawItem := range root.Output {
		var item struct {
			Type      string `json:"type"`
			ID        string `json:"id"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}
		if json.Unmarshal(rawItem, &item) != nil {
			return
		}
		switch item.Type {
		case "reasoning":
			entry.Reasoning = append(entry.Reasoning, append(json.RawMessage(nil), rawItem...))
		case "function_call":
			if item.CallID == "" {
				return
			}
			if _, exists := seenCalls[item.CallID]; exists {
				return
			}
			seenCalls[item.CallID] = struct{}{}
			entry.CallIDs = append(entry.CallIDs, item.CallID)
			if item.ID != "" && item.Name != "" {
				entry.FunctionCalls = append(entry.FunctionCalls, reasoningReplayFunctionCall{ItemID: item.ID, CallID: item.CallID, Name: item.Name, Arguments: item.Arguments})
			}
		}
	}
	if len(entry.Reasoning) == 0 && len(entry.FunctionCalls) == 0 {
		return
	}
	entry.EncodedLength = len(scopeKey)
	for _, callID := range entry.CallIDs {
		entry.EncodedLength += len(callID)
	}
	for _, call := range entry.FunctionCalls {
		entry.EncodedLength += len(call.ItemID) + len(call.CallID) + len(call.Name) + len(call.Arguments)
	}
	for _, item := range entry.Reasoning {
		entry.EncodedLength += len(item)
	}
	if entry.EncodedLength > maxReasoningReplayRecord {
		return
	}
	if s.now != nil {
		entry.CreatedAt = s.now()
	} else {
		entry.CreatedAt = time.Now()
	}
	key := reasoningReplayKey(scopeKey, entry)
	s.configMu.RLock()
	if s.configGeneration != generation || !s.config.ReasoningReplay {
		s.configMu.RUnlock()
		return
	}
	s.replayMu.Lock()
	if s.replayEntries == nil {
		s.replayEntries = make(map[string]reasoningReplayEntry)
	}
	s.purgeExpiredReasoningReplayLocked(entry.CreatedAt)
	if previous, exists := s.replayEntries[key]; exists {
		s.replayBytes -= previous.EncodedLength
	}
	s.replayEntries[key] = entry
	s.replayBytes += entry.EncodedLength
	for len(s.replayEntries) > maxReasoningReplayEntries || s.replayBytes > maxReasoningReplayCache {
		oldestKey := ""
		var oldest time.Time
		for candidateKey, candidate := range s.replayEntries {
			if oldestKey == "" || candidate.CreatedAt.Before(oldest) {
				oldestKey = candidateKey
				oldest = candidate.CreatedAt
			}
		}
		if oldestKey == "" {
			break
		}
		s.removeReasoningReplayLocked(oldestKey)
	}
	s.replayMu.Unlock()
	s.configMu.RUnlock()
}

func reasoningReplayKey(scopeKey string, entry reasoningReplayEntry) string {
	identity, _ := json.Marshal(struct {
		CallIDs       []string                      `json:"call_ids"`
		FunctionCalls []reasoningReplayFunctionCall `json:"function_calls"`
		Reasoning     []json.RawMessage             `json:"reasoning"`
	}{CallIDs: entry.CallIDs, FunctionCalls: entry.FunctionCalls, Reasoning: entry.Reasoning})
	sum := sha256.Sum256(identity)
	return scopeKey + ":" + hex.EncodeToString(sum[:])
}

func (s *Service) reasoningReplayForScope(scopeKey string) []reasoningReplayEntry {
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	s.replayMu.Lock()
	s.purgeExpiredReasoningReplayLocked(now)
	var entries []reasoningReplayEntry
	for _, entry := range s.replayEntries {
		if entry.ScopeKey == scopeKey {
			entry.CallIDs = append([]string(nil), entry.CallIDs...)
			entry.FunctionCalls = append([]reasoningReplayFunctionCall(nil), entry.FunctionCalls...)
			entry.Reasoning = cloneRawMessages(entry.Reasoning)
			entries = append(entries, entry)
		}
	}
	s.replayMu.Unlock()
	sort.Slice(entries, func(i, j int) bool { return entries[i].CreatedAt.After(entries[j].CreatedAt) })
	return entries
}

func cloneRawMessages(values []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, len(values))
	for index, value := range values {
		out[index] = append(json.RawMessage(nil), value...)
	}
	return out
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (s *Service) purgeExpiredReasoningReplayLocked(now time.Time) {
	for key, entry := range s.replayEntries {
		if !entry.CreatedAt.Add(reasoningReplayTTL).After(now) {
			s.removeReasoningReplayLocked(key)
		}
	}
}

func (s *Service) removeReasoningReplayLocked(key string) {
	if entry, exists := s.replayEntries[key]; exists {
		s.replayBytes -= entry.EncodedLength
		delete(s.replayEntries, key)
	}
}

func (s *Service) clearReasoningReplay() {
	s.replayMu.Lock()
	clear(s.replayEntries)
	s.replayBytes = 0
	s.replayMu.Unlock()
}
