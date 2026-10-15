package translate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const capturedClaudeGPTPNGReadFixtureRoot = "testdata/claude-gpt-png-read"

func capturedClaudeGPTPNGReadRoot() string {
	if root := os.Getenv("CPA_NATIVE_ATTACHMENT_FIXTURE_ROOT"); root != "" {
		return root
	}
	return capturedClaudeGPTPNGReadFixtureRoot
}

func readCapturedClaudeGPTPNGReadFile(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(capturedClaudeGPTPNGReadRoot(), name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func capturedClaudeGPTPNGReadJSON(t *testing.T, name string) []byte {
	t.Helper()
	return readCapturedClaudeGPTPNGReadFile(t, "bodies/"+name+".json")
}

func capturedClaudeGPTPNGReadSSE(t *testing.T, name string) []byte {
	t.Helper()
	return readCapturedClaudeGPTPNGReadFile(t, "bodies/"+name+".sse")
}

func capturedClaudeGPTPNGReadShape(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		fields := make(map[string]any, len(typed))
		for key, item := range typed {
			fields[key] = capturedClaudeGPTPNGReadShape(item)
		}
		return map[string]any{"type": "object", "fields": fields}
	case []any:
		items := make([]any, len(typed))
		for index, item := range typed {
			items[index] = capturedClaudeGPTPNGReadShape(item)
		}
		return map[string]any{"type": "array", "length": len(typed), "items": items}
	case nil:
		return map[string]any{"type": "null"}
	case bool:
		return map[string]any{"type": "boolean"}
	case json.Number, float64, float32, int, int64, uint64:
		return map[string]any{"type": "number"}
	case string:
		return map[string]any{"type": "string"}
	default:
		return map[string]any{"type": "unsupported"}
	}
}

func capturedClaudeGPTPNGReadSSEFrames(t *testing.T, body []byte) [][]byte {
	t.Helper()
	normalized := strings.ReplaceAll(string(body), "\r\n", "\n")
	parts := strings.Split(normalized, "\n\n")
	frames := make([][]byte, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			frames = append(frames, []byte(part))
		}
	}
	return frames
}

func capturedClaudeGPTPNGReadSSEShape(t *testing.T, body []byte) any {
	t.Helper()
	frames := capturedClaudeGPTPNGReadSSEFrames(t, body)
	shapes := make([]any, 0, len(frames))
	for index, frame := range frames {
		event, data, done, err := parseSSEFrame(frame)
		if err != nil {
			t.Fatalf("parse captured SSE frame %d: %v", index, err)
		}
		var dataValue any
		dataType := ""
		if done {
			dataValue = "[DONE]"
			dataType = "[DONE]"
		} else {
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.UseNumber()
			if err := decoder.Decode(&dataValue); err != nil {
				t.Fatalf("decode captured SSE frame %d: %v", index, err)
			}
			dataType = gjson.GetBytes(data, "type").String()
		}
		shapes = append(shapes, map[string]any{
			"index":      index,
			"event":      event,
			"data_type":  dataType,
			"data_shape": capturedClaudeGPTPNGReadShape(dataValue),
		})
	}
	return map[string]any{"type": "sse", "frame_count": len(shapes), "frames": shapes}
}

func assertCapturedClaudeGPTReadFixtureIntegrity(t *testing.T, readFile func(*testing.T, string) []byte, name, kind string, body []byte) {
	t.Helper()
	var manifest struct {
		PNGDecodedSHA256 string `json:"png_decoded_sha256"`
		EvaluationSHA256 string `json:"evaluation_sha256"`
		Bodies           []struct {
			Name                  string `json:"name"`
			Kind                  string `json:"kind"`
			Fixture               string `json:"fixture"`
			Shape                 string `json:"shape"`
			SourceBodySHA256      string `json:"source_body_sha256"`
			SanitizedSourceSHA256 string `json:"sanitized_source_sha256"`
			FixtureSHA256         string `json:"fixture_sha256"`
			SourceShapeSHA256     string `json:"source_shape_sha256"`
		} `json:"bodies"`
	}
	manifestBody := readFile(t, "manifest.json")
	if err := json.Unmarshal(manifestBody, &manifest); err != nil {
		t.Fatal(err)
	}
	var record *struct {
		Name                  string `json:"name"`
		Kind                  string `json:"kind"`
		Fixture               string `json:"fixture"`
		Shape                 string `json:"shape"`
		SourceBodySHA256      string `json:"source_body_sha256"`
		SanitizedSourceSHA256 string `json:"sanitized_source_sha256"`
		FixtureSHA256         string `json:"fixture_sha256"`
		SourceShapeSHA256     string `json:"source_shape_sha256"`
	}
	for index := range manifest.Bodies {
		if manifest.Bodies[index].Name == name && manifest.Bodies[index].Kind == kind {
			record = &manifest.Bodies[index]
			break
		}
	}
	if record == nil {
		t.Fatalf("fixture manifest omitted %s (%s)", name, kind)
	}
	actualHashBytes := sha256.Sum256(body)
	actualHash := hex.EncodeToString(actualHashBytes[:])
	if actualHash != record.FixtureSHA256 || actualHash != record.SanitizedSourceSHA256 {
		t.Fatalf("fixture bytes do not match the sanitized source hash for %s", name)
	}
	if decoded, err := hex.DecodeString(record.SourceBodySHA256); err != nil || len(decoded) != sha256.Size {
		t.Fatalf("original source hash is missing or malformed for %s", name)
	}
	shapeBody := readFile(t, record.Shape)
	var wantShape any
	if err := json.Unmarshal(shapeBody, &wantShape); err != nil {
		t.Fatal(err)
	}
	var actualShape any
	if kind == "json" {
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		if err := decoder.Decode(&actualShape); err != nil {
			t.Fatalf("decode fixture %s: %v", name, err)
		}
		actualShape = capturedClaudeGPTPNGReadShape(actualShape)
	} else {
		actualShape = capturedClaudeGPTPNGReadSSEShape(t, body)
	}
	actualShapeJSON, err := json.Marshal(actualShape)
	if err != nil {
		t.Fatal(err)
	}
	wantShapeJSON, err := json.Marshal(wantShape)
	if err != nil {
		t.Fatal(err)
	}
	requireLiveBodyJSONEqual(t, actualShapeJSON, wantShapeJSON)
	shapeHash := sha256.Sum256(wantShapeJSON)
	if hex.EncodeToString(shapeHash[:]) != record.SourceShapeSHA256 {
		t.Fatalf("source-derived shape hash changed for %s", name)
	}
}

func capturedClaudeGPTPNGReadWalk(t *testing.T, value any, visit func(key string, value any)) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			visit(key, item)
			capturedClaudeGPTPNGReadWalk(t, item, visit)
		}
	case []any:
		for _, item := range typed {
			capturedClaudeGPTPNGReadWalk(t, item, visit)
		}
	}
}

func assertCapturedClaudeGPTPNGReadPrivacySentinels(t *testing.T, body []byte, kind string) {
	t.Helper()
	pathPattern := regexp.MustCompile(`(?:/Users/|/private/var/|/var/folders/|/tmp/|/Volumes/|/System/Volumes/|/home/)`)
	visitValue := func(key string, item any) {
		text, ok := item.(string)
		if !ok || text == "" {
			return
		}
		lower := strings.ToLower(key)
		switch {
		case lower == "encrypted_content" || lower == "encrypted_index" || lower == "signature":
			if !strings.HasPrefix(text, "fixture_redacted_") {
				t.Errorf("opaque %s value was not replaced by a fixture sentinel", key)
			}
		case lower == "prompt_cache_key" || lower == "safety_identifier":
			if !strings.HasPrefix(text, "fixture_"+lower+"_") {
				t.Errorf("%s value was not replaced by a fixture sentinel", key)
			}
		case lower == "user_id" || lower == "device_id" || lower == "session_id" || lower == "account_uuid" || lower == "client_id" || lower == "account_id":
			if !strings.Contains(text, "fixture_") {
				t.Errorf("private client identity field %s was not replaced", key)
			}
		case lower == "file_path" || lower == "path" || lower == "filename":
			if strings.HasPrefix(text, "/fixture/private-path-") {
				return
			}
		}
		if pathPattern.MatchString(text) {
			t.Errorf("private filesystem path remains in %s fixture", kind)
		}
		if lower == "id" || strings.HasSuffix(lower, "_id") {
			if strings.HasPrefix(text, "cpa_tool_v1_") {
				itemID, callID, ok := DecodeClaudeToolIDs(text)
				if !ok || (itemID != "" && !strings.Contains(itemID, "fixture_")) || !strings.Contains(callID, "fixture_") {
					t.Errorf("encoded Claude tool ID carrier %s did not retain sanitized embedded IDs", key)
				}
			} else if !strings.Contains(text, "fixture_") {
				t.Errorf("opaque identifier field %s was not replaced by a fixture sentinel", key)
			}
		}
	}
	if kind == "json" {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		capturedClaudeGPTPNGReadWalk(t, value, visitValue)
		return
	}
	for index, frame := range capturedClaudeGPTPNGReadSSEFrames(t, body) {
		_, data, done, err := parseSSEFrame(frame)
		if err != nil {
			t.Fatalf("parse privacy-sensitive SSE frame %d: %v", index, err)
		}
		if done {
			continue
		}
		var event any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&event); err != nil {
			t.Fatalf("decode privacy-sensitive SSE frame %d: %v", index, err)
		}
		capturedClaudeGPTPNGReadWalk(t, event, visitValue)
	}
}

func capturedClaudeGPTPNGReadDecodeObject(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func capturedClaudeGPTPNGReadNestedMap(value any, keys ...string) map[string]any {
	current := value
	for _, key := range keys {
		obj, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = obj[key]
	}
	result, _ := current.(map[string]any)
	return result
}

func capturedClaudeGPTPNGReadArray(value any) []any {
	items, _ := value.([]any)
	return items
}

func capturedClaudeGPTPNGReadString(value any) string {
	text, _ := value.(string)
	return text
}

func capturedClaudeGPTPNGReadResponseEvents(t *testing.T, body []byte) ([]string, []map[string]any, []map[string]any) {
	t.Helper()
	var types []string
	var items []map[string]any
	var events []map[string]any
	for index, frame := range capturedClaudeGPTPNGReadSSEFrames(t, body) {
		event, data, done, err := parseSSEFrame(frame)
		if err != nil {
			t.Fatalf("parse response frame %d: %v", index, err)
		}
		if done {
			continue
		}
		obj := capturedClaudeGPTPNGReadDecodeObject(t, data)
		types = append(types, capturedClaudeGPTPNGReadString(obj["type"]))
		obj["_event"] = event
		events = append(events, obj)
		if capturedClaudeGPTPNGReadString(obj["type"]) == "response.output_item.done" {
			if item, ok := obj["item"].(map[string]any); ok && capturedClaudeGPTPNGReadString(item["type"]) == "function_call" {
				items = append(items, item)
			}
		}
		if capturedClaudeGPTPNGReadString(obj["type"]) == "response.completed" {
			response := capturedClaudeGPTPNGReadNestedMap(obj, "response")
			for _, rawItem := range capturedClaudeGPTPNGReadArray(response["output"]) {
				if item, ok := rawItem.(map[string]any); ok && capturedClaudeGPTPNGReadString(item["type"]) == "function_call" {
					items = append(items, item)
				}
			}
		}
	}
	return types, items, events
}

func capturedClaudeGPTPNGReadFunctionCalls(body []byte) []map[string]any {
	obj := map[string]any{}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&obj) != nil {
		return nil
	}
	var calls []map[string]any
	for _, item := range capturedClaudeGPTPNGReadArray(obj["input"]) {
		if typed, ok := item.(map[string]any); ok && capturedClaudeGPTPNGReadString(typed["type"]) == "function_call" {
			calls = append(calls, typed)
		}
	}
	return calls
}

func capturedClaudeGPTPNGReadFunctionOutputs(body []byte) []map[string]any {
	obj := map[string]any{}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&obj) != nil {
		return nil
	}
	var outputs []map[string]any
	for _, item := range capturedClaudeGPTPNGReadArray(obj["input"]) {
		if typed, ok := item.(map[string]any); ok && capturedClaudeGPTPNGReadString(typed["type"]) == "function_call_output" {
			outputs = append(outputs, typed)
		}
	}
	return outputs
}

func capturedClaudeGPTPNGReadClaudeToolUse(t *testing.T, body []byte) (string, string, map[string]any, string) {
	t.Helper()
	var toolID, name, stopReason string
	var partial strings.Builder
	var input map[string]any
	for index, frame := range capturedClaudeGPTPNGReadSSEFrames(t, body) {
		_, data, done, err := parseSSEFrame(frame)
		if err != nil {
			t.Fatalf("parse Claude SSE frame %d: %v", index, err)
		}
		if done {
			continue
		}
		obj := capturedClaudeGPTPNGReadDecodeObject(t, data)
		switch capturedClaudeGPTPNGReadString(obj["type"]) {
		case "content_block_start":
			block := capturedClaudeGPTPNGReadNestedMap(obj, "content_block")
			if capturedClaudeGPTPNGReadString(block["type"]) == "tool_use" {
				toolID = capturedClaudeGPTPNGReadString(block["id"])
				name = capturedClaudeGPTPNGReadString(block["name"])
				input, _ = block["input"].(map[string]any)
			}
		case "content_block_delta":
			delta := capturedClaudeGPTPNGReadNestedMap(obj, "delta")
			if capturedClaudeGPTPNGReadString(delta["type"]) == "input_json_delta" {
				partial.WriteString(capturedClaudeGPTPNGReadString(delta["partial_json"]))
			}
		case "message_delta":
			stopReason = capturedClaudeGPTPNGReadString(capturedClaudeGPTPNGReadNestedMap(obj, "delta")["stop_reason"])
		}
	}
	if partial.Len() > 0 {
		var parsed map[string]any
		decoder := json.NewDecoder(strings.NewReader(partial.String()))
		decoder.UseNumber()
		if err := decoder.Decode(&parsed); err != nil {
			t.Fatalf("decode captured Claude tool arguments: %v", err)
		}
		input = parsed
	}
	return toolID, name, input, stopReason
}

func capturedClaudeGPTPNGReadText(t *testing.T, body []byte) string {
	t.Helper()
	var text strings.Builder
	for index, frame := range capturedClaudeGPTPNGReadSSEFrames(t, body) {
		_, data, done, err := parseSSEFrame(frame)
		if err != nil {
			t.Fatalf("parse text stream frame %d: %v", index, err)
		}
		if done {
			continue
		}
		obj := capturedClaudeGPTPNGReadDecodeObject(t, data)
		switch capturedClaudeGPTPNGReadString(obj["type"]) {
		case "response.output_text.delta":
			text.WriteString(capturedClaudeGPTPNGReadString(obj["delta"]))
		case "content_block_delta":
			delta := capturedClaudeGPTPNGReadNestedMap(obj, "delta")
			if capturedClaudeGPTPNGReadString(delta["type"]) == "text_delta" {
				text.WriteString(capturedClaudeGPTPNGReadString(delta["text"]))
			}
		}
	}
	return text.String()
}

func capturedClaudeGPTPNGReadTranslateResponsesStream(t *testing.T, sourceBody, original, translated []byte) []byte {
	t.Helper()
	var state any
	var out bytes.Buffer
	for index, frame := range capturedClaudeGPTPNGReadSSEFrames(t, sourceBody) {
		converted, err := StreamFromEndpoint(context.Background(), EndpointResponses, "claude", "gpt-6-luna", original, translated, frame, &state)
		if err != nil {
			t.Fatalf("translate captured Responses SSE frame %d: %v", index, err)
		}
		for _, chunk := range converted {
			out.Write(chunk)
		}
	}
	return out.Bytes()
}

func capturedClaudeGPTPNGReadToolCallInRequest(t *testing.T, body []byte) map[string]any {
	t.Helper()
	calls := capturedClaudeGPTPNGReadFunctionCalls(body)
	if len(calls) != 1 {
		t.Fatalf("Responses request has %d function_call items, want one", len(calls))
	}
	return calls[0]
}

func capturedClaudeGPTPNGReadToolResultInRequest(t *testing.T, body []byte) map[string]any {
	t.Helper()
	outputs := capturedClaudeGPTPNGReadFunctionOutputs(body)
	if len(outputs) != 1 {
		t.Fatalf("Responses request has %d function_call_output items, want one", len(outputs))
	}
	return outputs[0]
}

func capturedClaudeGPTAttachmentReadFindImageHash(t *testing.T, value any, mediaType string) string {
	t.Helper()
	var hashes []string
	dataURLPrefix := "data:" + mediaType + ";base64,"
	var visit func(any)
	visit = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			if capturedClaudeGPTPNGReadString(typed["media_type"]) == mediaType {
				if data := capturedClaudeGPTPNGReadString(typed["data"]); data != "" {
					decoded, err := base64.StdEncoding.DecodeString(data)
					if err != nil {
						t.Fatalf("decode captured %s payload: %v", mediaType, err)
					}
					digest := sha256.Sum256(decoded)
					hashes = append(hashes, hex.EncodeToString(digest[:]))
				}
			}
			for _, item := range typed {
				visit(item)
			}
		case []any:
			for _, item := range typed {
				visit(item)
			}
		case string:
			if strings.HasPrefix(typed, dataURLPrefix) {
				decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(typed, dataURLPrefix))
				if err != nil {
					t.Fatalf("decode captured %s data URL: %v", mediaType, err)
				}
				digest := sha256.Sum256(decoded)
				hashes = append(hashes, hex.EncodeToString(digest[:]))
			}
		}
	}
	visit(value)
	if len(hashes) == 0 {
		return ""
	}
	for _, hash := range hashes[1:] {
		if hash != hashes[0] {
			t.Fatalf("one captured body contains multiple different %s payloads", mediaType)
		}
	}
	return hashes[0]
}

func capturedClaudeGPTAttachmentReadSourceImage(t *testing.T, claudeRequest []byte, mediaType string) (string, map[string]any) {
	t.Helper()
	messages := capturedClaudeGPTPNGReadArray(capturedClaudeGPTPNGReadDecodeObject(t, claudeRequest)["messages"])
	for _, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok || capturedClaudeGPTPNGReadString(message["role"]) != "user" {
			continue
		}
		for _, rawBlock := range capturedClaudeGPTPNGReadArray(message["content"]) {
			block, ok := rawBlock.(map[string]any)
			if !ok || capturedClaudeGPTPNGReadString(block["type"]) != "tool_result" {
				continue
			}
			for _, rawContent := range capturedClaudeGPTPNGReadArray(block["content"]) {
				content, ok := rawContent.(map[string]any)
				if !ok || capturedClaudeGPTPNGReadString(content["type"]) != "image" {
					continue
				}
				source := capturedClaudeGPTPNGReadNestedMap(content, "source")
				if capturedClaudeGPTPNGReadString(source["media_type"]) != mediaType || capturedClaudeGPTPNGReadString(source["type"]) != "base64" {
					t.Fatalf("native tool result did not contain captured base64 %s", mediaType)
				}
				return capturedClaudeGPTAttachmentReadFindImageHash(t, source, mediaType), block
			}
		}
	}
	t.Fatalf("captured Claude tool_result did not contain a %s image", mediaType)
	return "", nil
}

func capturedClaudeGPTPNGReadClaudeUserTexts(value any) []string {
	root, _ := value.(map[string]any)
	var out []string
	for _, rawMessage := range capturedClaudeGPTPNGReadArray(root["messages"]) {
		message, _ := rawMessage.(map[string]any)
		if capturedClaudeGPTPNGReadString(message["role"]) != "user" {
			continue
		}
		if text, ok := message["content"].(string); ok && text != "" {
			out = append(out, text)
			continue
		}
		for _, rawBlock := range capturedClaudeGPTPNGReadArray(message["content"]) {
			block, _ := rawBlock.(map[string]any)
			if capturedClaudeGPTPNGReadString(block["type"]) == "text" {
				out = append(out, capturedClaudeGPTPNGReadString(block["text"]))
			}
		}
	}
	return out
}

func capturedClaudeGPTPNGReadResponsesUserTexts(value any) []string {
	root, _ := value.(map[string]any)
	var out []string
	for _, rawItem := range capturedClaudeGPTPNGReadArray(root["input"]) {
		item, _ := rawItem.(map[string]any)
		if capturedClaudeGPTPNGReadString(item["role"]) != "user" {
			continue
		}
		for _, rawPart := range capturedClaudeGPTPNGReadArray(item["content"]) {
			part, _ := rawPart.(map[string]any)
			if capturedClaudeGPTPNGReadString(part["type"]) == "input_text" {
				out = append(out, capturedClaudeGPTPNGReadString(part["text"]))
			}
		}
	}
	return out
}

func TestCapturedClaudeGPTPNGReadBodyShapeAndProvenance(t *testing.T) {
	t.Parallel()
	for _, nameKind := range [][2]string{
		{"hop-001-claude-request", "json"},
		{"hop-001-claude-response", "sse"},
		{"hop-001-responses-request", "json"},
		{"hop-001-responses-response", "sse"},
		{"hop-002-claude-request", "json"},
		{"hop-002-claude-response", "sse"},
		{"hop-002-responses-request", "json"},
		{"hop-002-responses-response", "sse"},
	} {
		name, kind := nameKind[0], nameKind[1]
		t.Run(name, func(t *testing.T) {
			var body []byte
			if kind == "json" {
				body = capturedClaudeGPTPNGReadJSON(t, name)
			} else {
				body = capturedClaudeGPTPNGReadSSE(t, name)
			}
			assertCapturedClaudeGPTReadFixtureIntegrity(t, readCapturedClaudeGPTPNGReadFile, name, kind, body)
			assertCapturedClaudeGPTPNGReadPrivacySentinels(t, body, kind)
		})
	}
}

func TestCapturedClaudeGPTPNGReadRunsExistingMessagesToResponsesAndStreamConverters(t *testing.T) {
	t.Parallel()
	claudeRequest1 := capturedClaudeGPTPNGReadJSON(t, "hop-001-claude-request")
	claudeRequest2 := capturedClaudeGPTPNGReadJSON(t, "hop-002-claude-request")
	responsesRequest1 := capturedClaudeGPTPNGReadJSON(t, "hop-001-responses-request")
	responsesRequest2 := capturedClaudeGPTPNGReadJSON(t, "hop-002-responses-request")
	providerResponse1 := capturedClaudeGPTPNGReadSSE(t, "hop-001-responses-response")
	providerResponse2 := capturedClaudeGPTPNGReadSSE(t, "hop-002-responses-response")
	clientResponse1 := capturedClaudeGPTPNGReadSSE(t, "hop-001-claude-response")
	clientResponse2 := capturedClaudeGPTPNGReadSSE(t, "hop-002-claude-response")

	convertedRequest1, err := RequestForEndpointFrom("claude", "gpt-6-luna", claudeRequest1, true, EndpointResponses)
	if err != nil {
		t.Fatalf("translate captured first Messages request: %v", err)
	}
	convertedRequest2, err := RequestForEndpointFrom("claude", "gpt-6-luna", claudeRequest2, true, EndpointResponses)
	if err != nil {
		t.Fatalf("translate captured follow-up Messages request: %v", err)
	}
	for _, pair := range [][2][]byte{{convertedRequest1, responsesRequest1}, {convertedRequest2, responsesRequest2}} {
		if gjson.GetBytes(pair[0], "model").String() != "gpt-6-luna" || gjson.GetBytes(pair[0], "stream").Bool() != true {
			t.Fatal("existing converter did not preserve the requested destination model/stream mode")
		}
	}

	capturedMessages1 := capturedClaudeGPTPNGReadDecodeObject(t, claudeRequest1)
	capturedMessages2 := capturedClaudeGPTPNGReadDecodeObject(t, claudeRequest2)
	capturedResponses1 := capturedClaudeGPTPNGReadDecodeObject(t, responsesRequest1)
	capturedResponses2 := capturedClaudeGPTPNGReadDecodeObject(t, responsesRequest2)
	generated1 := capturedClaudeGPTPNGReadDecodeObject(t, convertedRequest1)
	generated2 := capturedClaudeGPTPNGReadDecodeObject(t, convertedRequest2)
	if capturedClaudeGPTPNGReadString(capturedResponses1["model"]) != "gpt-6-luna" || capturedClaudeGPTPNGReadString(capturedResponses2["model"]) != "gpt-6-luna" {
		t.Fatal("captured Responses requests used a different destination model")
	}
	if len(capturedClaudeGPTPNGReadArray(capturedResponses1["tools"])) != 1 || capturedClaudeGPTPNGReadString(capturedClaudeGPTPNGReadArray(capturedResponses1["tools"])[0].(map[string]any)["name"]) != "Read" {
		t.Fatal("actual first Responses request does not declare the native Read function")
	}
	if len(capturedClaudeGPTPNGReadArray(generated1["tools"])) != 1 || capturedClaudeGPTPNGReadString(capturedClaudeGPTPNGReadArray(generated1["tools"])[0].(map[string]any)["name"]) != "Read" {
		t.Fatal("existing Messages-to-Responses converter lost the native Read function declaration")
	}
	generatedTool1, capturedTool1 := capturedClaudeGPTPNGReadArray(generated1["tools"])[0].(map[string]any), capturedClaudeGPTPNGReadArray(capturedResponses1["tools"])[0].(map[string]any)
	if capturedClaudeGPTPNGReadString(generatedTool1["type"]) != "function" || capturedClaudeGPTPNGReadString(capturedTool1["type"]) != "function" {
		t.Fatal("existing converter or captured Responses request did not use the native function declaration")
	}
	generatedParameters, _ := json.Marshal(generatedTool1["parameters"])
	capturedParameters, _ := json.Marshal(capturedTool1["parameters"])
	requireLiveBodyJSONEqual(t, generatedParameters, capturedParameters)
	if !strings.Contains(string(convertedRequest1), `"name":"Read"`) {
		t.Fatal("existing converter did not produce the captured Responses Read function declaration")
	}
	messages1, messages2 := capturedClaudeGPTPNGReadArray(capturedMessages1["messages"]), capturedClaudeGPTPNGReadArray(capturedMessages2["messages"])
	if len(messages1) == 0 || len(messages2) == 0 {
		t.Fatal("captured original Messages requests have no messages")
	}
	for _, pair := range []struct {
		claude, responses, generated map[string]any
	}{
		{capturedMessages1, capturedResponses1, generated1},
		{capturedMessages2, capturedResponses2, generated2},
	} {
		claudeText := capturedClaudeGPTPNGReadClaudeUserTexts(pair.claude)
		responsesText := capturedClaudeGPTPNGReadResponsesUserTexts(pair.responses)
		generatedText := capturedClaudeGPTPNGReadResponsesUserTexts(pair.generated)
		if len(claudeText) == 0 || len(responsesText) == 0 || strings.Join(claudeText, "\x00") != strings.Join(responsesText, "\x00") {
			t.Fatal("captured Claude and provider Requests do not preserve the original user message text")
		}
		if strings.Join(claudeText, "\x00") != strings.Join(generatedText, "\x00") {
			t.Fatal("existing converter changed the original user message text")
		}
		if len(generatedText) == 0 {
			t.Fatal("existing converter dropped the original user message text")
		}
	}

	_, callsFromResponses1, providerEvents1 := capturedClaudeGPTPNGReadResponseEvents(t, providerResponse1)
	if len(callsFromResponses1) == 0 {
		t.Fatal("captured provider SSE did not contain a completed Read function call")
	}
	terminalCompleted1 := false
	for _, event := range providerEvents1 {
		if capturedClaudeGPTPNGReadString(event["type"]) == "response.completed" {
			terminalCompleted1 = true
			response := capturedClaudeGPTPNGReadNestedMap(event, "response")
			if capturedClaudeGPTPNGReadString(response["status"]) != "completed" {
				t.Fatal("captured first provider response did not complete normally")
			}
		}
	}
	if !terminalCompleted1 {
		t.Fatal("captured first provider SSE omitted response.completed")
	}
	providerCall1 := callsFromResponses1[len(callsFromResponses1)-1]
	if capturedClaudeGPTPNGReadString(providerCall1["name"]) != "Read" {
		t.Fatal("captured completed Responses function call was not Read")
	}
	var providerArgs1 map[string]any
	if err := json.Unmarshal([]byte(capturedClaudeGPTPNGReadString(providerCall1["arguments"])), &providerArgs1); err != nil {
		t.Fatalf("decode captured Read arguments: %v", err)
	}
	if capturedClaudeGPTPNGReadString(providerArgs1["file_path"]) == "" || !strings.HasPrefix(capturedClaudeGPTPNGReadString(providerArgs1["file_path"]), "/fixture/private-path-") {
		t.Fatal("captured Read arguments did not retain a sanitized private PNG path")
	}

	translatedClientResponse1 := capturedClaudeGPTPNGReadTranslateResponsesStream(t, providerResponse1, claudeRequest1, convertedRequest1)
	clientToolID1, clientToolName1, clientToolArgs1, clientStop1 := capturedClaudeGPTPNGReadClaudeToolUse(t, translatedClientResponse1)
	capturedToolID1, capturedToolName1, capturedToolArgs1, capturedStop1 := capturedClaudeGPTPNGReadClaudeToolUse(t, clientResponse1)
	if clientToolName1 != "Read" || capturedToolName1 != "Read" || clientStop1 != "tool_use" || capturedStop1 != "tool_use" {
		t.Fatal("captured provider stream did not translate to the native Read tool_use turn")
	}
	if clientToolID1 == "" || clientToolID1 != capturedToolID1 {
		t.Fatalf("existing Responses-to-Messages converter tool ID differs from actual local response (converted=%q captured=%q)", clientToolID1, capturedToolID1)
	}
	carrierItemID, carrierCallID, carrierOK := DecodeClaudeToolIDs(clientToolID1)
	if !carrierOK || carrierItemID != capturedClaudeGPTPNGReadString(providerCall1["id"]) || carrierCallID != capturedClaudeGPTPNGReadString(providerCall1["call_id"]) {
		t.Fatal("native Claude tool ID carrier did not preserve the captured Responses item/call ID pair")
	}
	translatedArgsJSON, _ := json.Marshal(clientToolArgs1)
	capturedArgsJSON, _ := json.Marshal(capturedToolArgs1)
	providerArgsJSON, _ := json.Marshal(providerArgs1)
	requireLiveBodyJSONEqual(t, translatedArgsJSON, providerArgsJSON)
	requireLiveBodyJSONEqual(t, capturedArgsJSON, providerArgsJSON)

	claudeToolID2, claudeToolName2, claudeToolArgs2 := capturedClaudeGPTPNGReadClaudeToolUseFromRequest(t, claudeRequest2)
	toolResultID2 := capturedClaudeGPTPNGReadToolResultID(t, claudeRequest2)
	if claudeToolName2 != "Read" || claudeToolID2 != clientToolID1 || toolResultID2 != claudeToolID2 {
		t.Fatal("follow-up Messages request did not preserve the actual Read call/result ID correlation")
	}
	claudeArgsJSON, _ := json.Marshal(claudeToolArgs2)
	requireLiveBodyJSONEqual(t, claudeArgsJSON, providerArgsJSON)

	generatedCall2 := capturedClaudeGPTPNGReadToolCallInRequest(t, convertedRequest2)
	capturedCall2 := capturedClaudeGPTPNGReadToolCallInRequest(t, responsesRequest2)
	generatedOutput2 := capturedClaudeGPTPNGReadToolResultInRequest(t, convertedRequest2)
	capturedOutput2 := capturedClaudeGPTPNGReadToolResultInRequest(t, responsesRequest2)
	if capturedClaudeGPTPNGReadString(generatedCall2["name"]) != "Read" || capturedClaudeGPTPNGReadString(capturedCall2["name"]) != "Read" {
		t.Fatal("follow-up Responses request did not retain the original Read function call")
	}
	callID := capturedClaudeGPTPNGReadString(providerCall1["call_id"])
	if callID == "" || capturedClaudeGPTPNGReadString(generatedCall2["id"]) != capturedClaudeGPTPNGReadString(providerCall1["id"]) || capturedClaudeGPTPNGReadString(capturedCall2["id"]) != capturedClaudeGPTPNGReadString(providerCall1["id"]) || capturedClaudeGPTPNGReadString(generatedCall2["call_id"]) != callID || capturedClaudeGPTPNGReadString(capturedCall2["call_id"]) != callID || capturedClaudeGPTPNGReadString(generatedOutput2["call_id"]) != callID || capturedClaudeGPTPNGReadString(capturedOutput2["call_id"]) != callID {
		t.Fatal("Responses function_call/function_call_output IDs do not correlate with the completed provider Read call")
	}
	var generatedArgs2, capturedArgs2 map[string]any
	if err := json.Unmarshal([]byte(capturedClaudeGPTPNGReadString(generatedCall2["arguments"])), &generatedArgs2); err != nil {
		t.Fatalf("decode translated follow-up Read arguments: %v", err)
	}
	if err := json.Unmarshal([]byte(capturedClaudeGPTPNGReadString(capturedCall2["arguments"])), &capturedArgs2); err != nil {
		t.Fatalf("decode captured public follow-up Read arguments: %v", err)
	}
	generatedArgs2JSON, _ := json.Marshal(generatedArgs2)
	capturedArgs2JSON, _ := json.Marshal(capturedArgs2)
	requireLiveBodyJSONEqual(t, generatedArgs2JSON, claudeArgsJSON)
	requireLiveBodyJSONEqual(t, capturedArgs2JSON, providerArgsJSON)

	pngHash, _ := capturedClaudeGPTAttachmentReadSourceImage(t, claudeRequest2, "image/png")
	generatedPNGHash := capturedClaudeGPTAttachmentReadFindImageHash(t, generatedOutput2["output"], "image/png")
	capturedPNGHash := capturedClaudeGPTAttachmentReadFindImageHash(t, capturedOutput2["output"], "image/png")
	manifestBody := readCapturedClaudeGPTPNGReadFile(t, "manifest.json")
	var mediaManifest struct {
		PNGDecodedSHA256 string `json:"png_decoded_sha256"`
	}
	if err := json.Unmarshal(manifestBody, &mediaManifest); err != nil {
		t.Fatal(err)
	}
	if pngHash == "" || pngHash != mediaManifest.PNGDecodedSHA256 || generatedPNGHash != pngHash || capturedPNGHash != pngHash {
		t.Fatal("decoded PNG bytes changed between native tool_result, converted request, and captured Responses function output")
	}

	translatedClientResponse2 := capturedClaudeGPTPNGReadTranslateResponsesStream(t, providerResponse2, claudeRequest2, convertedRequest2)
	providerText := capturedClaudeGPTPNGReadText(t, providerResponse2)
	capturedClientText := capturedClaudeGPTPNGReadText(t, clientResponse2)
	translatedClientText := capturedClaudeGPTPNGReadText(t, translatedClientResponse2)
	if providerText == "" || providerText != capturedClientText || providerText != translatedClientText {
		t.Fatal("final provider answer, actual client SSE answer, and existing translated answer diverged")
	}
	normalized := " " + strings.ToLower(strings.Join(strings.Fields(providerText), " ")) + " "
	mentionsBoth := regexp.MustCompile(`\bboth\b`).MatchString(normalized)
	mentionsRed := regexp.MustCompile(`\bred\b`).MatchString(normalized)
	mentionsBlue := regexp.MustCompile(`\bblue\b`).MatchString(normalized)
	wrongBothRed := mentionsBoth && mentionsRed && !mentionsBlue
	if !wrongBothRed {
		t.Fatalf("captured final answer does not match the expected failure semantics (both=%t red=%t blue=%t)", mentionsBoth, mentionsRed, mentionsBlue)
	}
	evaluationBody := readCapturedClaudeGPTPNGReadFile(t, "evaluation.json")
	var provenance struct {
		EvaluationSHA256 string `json:"evaluation_sha256"`
	}
	if err := json.Unmarshal(readCapturedClaudeGPTPNGReadFile(t, "manifest.json"), &provenance); err != nil {
		t.Fatal(err)
	}
	evaluationHash := sha256.Sum256(evaluationBody)
	if provenance.EvaluationSHA256 == "" || provenance.EvaluationSHA256 != hex.EncodeToString(evaluationHash[:]) {
		t.Fatal("evaluation provenance hash does not match the full captured result metadata")
	}
	var evaluation struct {
		ExpectedVerdict string `json:"expected_verdict"`
		Accepted        bool   `json:"accepted"`
		ErrorClass      string `json:"error_class"`
		FinalTextSHA256 string `json:"final_text_sha256"`
	}
	if err := json.Unmarshal(evaluationBody, &evaluation); err != nil {
		t.Fatal(err)
	}
	finalHash := sha256.Sum256([]byte(providerText))
	if evaluation.ExpectedVerdict != "FAIL" || evaluation.Accepted || evaluation.ErrorClass != "unclassified" || evaluation.FinalTextSHA256 != hex.EncodeToString(finalHash[:]) {
		t.Fatal("captured wrong-both-red answer was changed from its actual FAIL result or coarse unclassified error class")
	}

	_, _, events2 := capturedClaudeGPTPNGReadResponseEvents(t, providerResponse2)
	terminalCompleted2 := false
	for _, event := range events2 {
		if capturedClaudeGPTPNGReadString(event["type"]) == "response.completed" {
			terminalCompleted2 = capturedClaudeGPTPNGReadString(capturedClaudeGPTPNGReadNestedMap(event, "response")["status"]) == "completed"
		}
	}
	if !terminalCompleted2 {
		t.Fatal("actual follow-up provider SSE did not retain its completed final response")
	}
}

func capturedClaudeGPTPNGReadClaudeToolUseFromRequest(t *testing.T, body []byte) (string, string, map[string]any) {
	t.Helper()
	root := capturedClaudeGPTPNGReadDecodeObject(t, body)
	var id, name string
	var args map[string]any
	for _, rawMessage := range capturedClaudeGPTPNGReadArray(root["messages"]) {
		message, _ := rawMessage.(map[string]any)
		if capturedClaudeGPTPNGReadString(message["role"]) != "assistant" {
			continue
		}
		for _, rawBlock := range capturedClaudeGPTPNGReadArray(message["content"]) {
			block, _ := rawBlock.(map[string]any)
			if capturedClaudeGPTPNGReadString(block["type"]) == "tool_use" {
				id = capturedClaudeGPTPNGReadString(block["id"])
				name = capturedClaudeGPTPNGReadString(block["name"])
				args, _ = block["input"].(map[string]any)
			}
		}
	}
	if id == "" || name == "" || args == nil {
		t.Fatal("follow-up Messages request omitted the captured assistant Read tool_use")
	}
	return id, name, args
}

func capturedClaudeGPTPNGReadToolResultID(t *testing.T, body []byte) string {
	t.Helper()
	root := capturedClaudeGPTPNGReadDecodeObject(t, body)
	for _, rawMessage := range capturedClaudeGPTPNGReadArray(root["messages"]) {
		message, _ := rawMessage.(map[string]any)
		if capturedClaudeGPTPNGReadString(message["role"]) != "user" {
			continue
		}
		for _, rawBlock := range capturedClaudeGPTPNGReadArray(message["content"]) {
			block, _ := rawBlock.(map[string]any)
			if capturedClaudeGPTPNGReadString(block["type"]) == "tool_result" {
				return capturedClaudeGPTPNGReadString(block["tool_use_id"])
			}
		}
	}
	t.Fatal("follow-up Messages request omitted the paired Read tool_result")
	return ""
}

func TestCapturedClaudeGPTPNGReadCycleDoesNotInferReadFailureCause(t *testing.T) {
	t.Parallel()
	body := capturedClaudeGPTPNGReadSSE(t, "hop-001-responses-response")
	types, calls, events := capturedClaudeGPTPNGReadResponseEvents(t, body)
	completed := false
	for _, event := range events {
		if capturedClaudeGPTPNGReadString(event["type"]) == "response.completed" {
			completed = capturedClaudeGPTPNGReadString(capturedClaudeGPTPNGReadNestedMap(event, "response")["status"]) == "completed"
		}
	}
	if !completed || len(calls) == 0 || !strings.Contains(strings.Join(types, ","), "response.function_call_arguments.done") {
		t.Fatal("captured Read response was not a fully framed completed provider function call")
	}
	var evaluation struct {
		ErrorClass string `json:"error_class"`
	}
	if err := json.Unmarshal(readCapturedClaudeGPTPNGReadFile(t, "evaluation.json"), &evaluation); err != nil {
		t.Fatal(err)
	}
	if evaluation.ErrorClass != "unclassified" {
		t.Fatal("captured client verdict class was changed or replaced with an inferred cause")
	}
}

func TestCapturedClaudeGPTPNGReadFixtureToolCallValuesAreNotEmpty(t *testing.T) {
	t.Parallel()
	body := capturedClaudeGPTPNGReadJSON(t, "hop-001-responses-request")
	tool := capturedClaudeGPTPNGReadArray(capturedClaudeGPTPNGReadDecodeObject(t, body)["tools"])
	if len(tool) != 1 {
		t.Fatal("captured Responses request tool declaration missing")
	}
	toolObject, _ := tool[0].(map[string]any)
	if capturedClaudeGPTPNGReadString(toolObject["name"]) != "Read" {
		t.Fatal("captured native tool declaration name changed")
	}
	if capturedClaudeGPTPNGReadString(toolObject["type"]) != "function" {
		t.Fatal("captured Responses tool declaration type changed")
	}
	if capturedClaudeGPTPNGReadString(toolObject["description"]) == "" {
		t.Fatal("captured Read description unexpectedly empty")
	}
}
