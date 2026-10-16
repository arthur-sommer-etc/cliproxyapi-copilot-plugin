package translate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const nativeServerToolFixtureRoot = "testdata/native-server-tool-captures"

func readNativeServerToolFixture(t *testing.T, capture, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(nativeServerToolFixtureRoot, capture, name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func nativeServerToolMetadata(t *testing.T, capture string) map[string]any {
	t.Helper()
	body := readNativeServerToolFixture(t, capture, "metadata.json")
	var metadata map[string]any
	if err := json.Unmarshal(body, &metadata); err != nil {
		t.Fatal(err)
	}
	return metadata
}

func nativeServerToolStatus(t *testing.T, capture string) int {
	t.Helper()
	metadata := nativeServerToolMetadata(t, capture)
	return int(gjson.GetBytes(mustMarshalNativeToolMetadata(t, metadata), "http_status").Int())
}

func mustMarshalNativeToolMetadata(t *testing.T, metadata map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func nativeServerToolShape(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		fields := make(map[string]any, len(typed))
		for key, item := range typed {
			fields[key] = nativeServerToolShape(item)
		}
		return map[string]any{"type": "object", "fields": fields}
	case []any:
		items := make([]any, len(typed))
		for index, item := range typed {
			items[index] = nativeServerToolShape(item)
		}
		return map[string]any{"type": "array", "length": len(typed), "items": items}
	case nil:
		return map[string]any{"type": "null"}
	case bool:
		return map[string]any{"type": "boolean"}
	case json.Number:
		return map[string]any{"type": "number"}
	case string:
		return map[string]any{"type": "string"}
	default:
		return map[string]any{"type": "unsupported"}
	}
}

func assertNativeServerToolBodyParity(t *testing.T, capture, kind string, body []byte) {
	t.Helper()
	var decoded any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	shape, err := json.Marshal(nativeServerToolShape(decoded))
	if err != nil {
		t.Fatal(err)
	}
	wantShape := readNativeServerToolFixture(t, capture, kind+".shape.json")
	requireLiveBodyJSONEqual(t, shape, wantShape)

	digest := sha256.Sum256(body)
	actualHash := hex.EncodeToString(digest[:])
	metadataBody := mustMarshalNativeToolMetadata(t, nativeServerToolMetadata(t, capture))
	fixtureHash := gjson.GetBytes(metadataBody, "fixture_sha256."+kind).String()
	sourceHash := gjson.GetBytes(metadataBody, "sanitized_source_sha256."+kind).String()
	originalSourceHash := gjson.GetBytes(metadataBody, "source_body_sha256."+kind).String()

	if actualHash != fixtureHash || actualHash != sourceHash {
		t.Fatalf("%s fixture bytes do not match the sanitized captured source hash", kind)
	}
	if decoded, err := hex.DecodeString(originalSourceHash); err != nil || len(decoded) != sha256.Size {
		t.Fatalf("%s original captured source hash is missing or malformed", kind)
	}
	var shapeValue any
	if err := json.Unmarshal(wantShape, &shapeValue); err != nil {
		t.Fatal(err)
	}
	canonicalShape, err := json.Marshal(shapeValue)
	if err != nil {
		t.Fatal(err)
	}
	shapeDigest := sha256.Sum256(canonicalShape)
	originalShapeHash := gjson.GetBytes(metadataBody, "original_shape_sha256."+kind).String()
	if hex.EncodeToString(shapeDigest[:]) != originalShapeHash {
		t.Fatalf("%s recorded source shape does not match its original shape hash", kind)
	}
}

func assertNativeServerToolSentinels(t *testing.T, body []byte) {
	t.Helper()
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	var visit func(any, string)
	visit = func(current any, key string) {
		switch typed := current.(type) {
		case map[string]any:
			for childKey, child := range typed {
				visit(child, childKey)
			}
		case []any:
			for _, child := range typed {
				visit(child, key)
			}
		case string:
			if typed == "" {
				return
			}
			switch key {
			case "encrypted_content", "encrypted_index":
				if !strings.HasPrefix(typed, "fixture_redacted_"+key+"_") {
					t.Errorf("%s was not replaced by its documented fixture sentinel", key)
				}
			case "prompt_cache_key", "safety_identifier":
				if !strings.HasPrefix(typed, "fixture_"+key+"_") {
					t.Errorf("%s was not replaced by its documented fixture sentinel", key)
				}
			case "id", "tool_use_id", "call_id", "response_id", "previous_response_id":
				if !strings.Contains(typed, "fixture") {
					t.Errorf("opaque %s was not replaced by a deterministic fixture sentinel", key)
				}
			}
		}
	}
	visit(value, "")
}

func TestCapturedNativeServerToolFullBodyParity(t *testing.T) {
	captures := []string{
		"gemini-chat-search",
		"gpt-responses-search",
		"claude-messages-web-fetch",
		"claude-messages-code-execution",
		"gpt-responses-code-interpreter",
	}
	for _, capture := range captures {
		t.Run(capture, func(t *testing.T) {
			request := readNativeServerToolFixture(t, capture, "request.json")
			response := readNativeServerToolFixture(t, capture, "response.json")
			assertNativeServerToolBodyParity(t, capture, "request", request)
			assertNativeServerToolBodyParity(t, capture, "response", response)
			assertNativeServerToolSentinels(t, request)
			assertNativeServerToolSentinels(t, response)
		})
	}
}

func TestCapturedNativeServerToolProviderOutcomes(t *testing.T) {
	geminiRequest := readNativeServerToolFixture(t, "gemini-chat-search", "request.json")
	geminiResponse := readNativeServerToolFixture(t, "gemini-chat-search", "response.json")
	if nativeServerToolStatus(t, "gemini-chat-search") != 200 || gjson.GetBytes(geminiRequest, "model").String() != "gemini-3.8-flash" || !gjson.GetBytes(geminiRequest, "web_search_options").Exists() {
		t.Fatal("captured Gemini Chat search request or HTTP status changed")
	}
	if gjson.GetBytes(geminiResponse, "choices.0.finish_reason").String() != "error" || gjson.GetBytes(geminiResponse, "choices.0.message.content").String() != "" {
		t.Fatal("captured Gemini search failure was changed into a positive result")
	}

	gptRequest := readNativeServerToolFixture(t, "gpt-responses-search", "request.json")
	gptResponse := readNativeServerToolFixture(t, "gpt-responses-search", "response.json")
	if nativeServerToolStatus(t, "gpt-responses-search") != 200 || gjson.GetBytes(gptRequest, "model").String() != "gpt-6-luna" || gjson.GetBytes(gptRequest, "tools.0.type").String() != "web_search" || gjson.GetBytes(gptRequest, "tool_choice").String() != "required" {
		t.Fatal("captured GPT Responses search declaration or status changed")
	}
	if gjson.GetBytes(gptRequest, "prompt_cache_key").String() != "fixture_prompt_cache_key_gpt-responses-search" || gjson.GetBytes(gptResponse, "safety_identifier").String() != "fixture_safety_identifier_gpt-responses-search" {
		t.Fatal("opaque identity fields were not deterministically scrubbed in place")
	}
	if gjson.GetBytes(gptResponse, "status").String() != "completed" || !gjson.GetBytes(gptResponse, "usage").Exists() || !gjson.GetBytes(gptResponse, "instructions").Exists() {
		t.Fatal("captured GPT response status, usage, or instructions were omitted")
	}
	var search gjson.Result
	for _, item := range gjson.GetBytes(gptResponse, "output").Array() {
		if item.Get("type").String() == "web_search_call" {
			search = item
		}
		if item.Get("type").String() == "reasoning" && item.Get("encrypted_content").Exists() && !strings.HasPrefix(item.Get("encrypted_content").String(), "fixture_redacted_encrypted_content_") {
			t.Fatal("opaque encrypted_content value was not replaced in place")
		}
	}
	if !search.Exists() || len(search.Get("results").Array()) != 11 || len(search.Get("action.queries").Array()) == 0 || len(search.Get("action.sources").Array()) == 0 {
		t.Fatal("captured GPT hosted search results or action sources are incomplete")
	}
	resultURLs := map[string]bool{}
	for _, result := range search.Get("results").Array() {
		resultURLs[result.Get("url").String()] = true
	}
	sourceURLs := map[string]bool{}
	for _, source := range search.Get("action.sources").Array() {
		sourceURLs[source.Get("url").String()] = true
	}
	matchedCitation := false
	for _, item := range gjson.GetBytes(gptResponse, "output").Array() {
		if item.Get("type").String() != "message" {
			continue
		}
		for _, part := range item.Get("content").Array() {
			for _, annotation := range part.Get("annotations").Array() {
				url := annotation.Get("url").String()
				if annotation.Get("type").String() == "url_citation" && url != "" && resultURLs[url] && sourceURLs[url] {
					matchedCitation = true
				}
			}
		}
	}
	if !matchedCitation {
		t.Fatal("captured GPT citation no longer points to the same search result/source URL")
	}

	fetchRequest := readNativeServerToolFixture(t, "claude-messages-web-fetch", "request.json")
	fetchResponse := readNativeServerToolFixture(t, "claude-messages-web-fetch", "response.json")
	if nativeServerToolStatus(t, "claude-messages-web-fetch") != 400 || len(gjson.GetBytes(fetchRequest, "tools").Array()) == 0 || !gjson.GetBytes(fetchResponse, "error").Exists() {
		t.Fatal("captured Claude WebFetch rejection changed")
	}

	claudeRequest := readNativeServerToolFixture(t, "claude-messages-code-execution", "request.json")
	claudeResponse := readNativeServerToolFixture(t, "claude-messages-code-execution", "response.json")
	if nativeServerToolStatus(t, "claude-messages-code-execution") != 200 || gjson.GetBytes(claudeRequest, "tools.0.type").String() != "code_execution_20250825" || gjson.GetBytes(claudeResponse, "stop_reason").String() != "end_turn" || !gjson.GetBytes(claudeResponse, "usage").Exists() || !gjson.GetBytes(claudeResponse, "diagnostics").Exists() {
		t.Fatal("captured Claude execution request, stop, usage, or diagnostics fields changed")
	}
	var serverUse, toolResult gjson.Result
	for _, block := range gjson.GetBytes(claudeResponse, "content").Array() {
		switch block.Get("type").String() {
		case "server_tool_use":
			serverUse = block
		case "bash_code_execution_tool_result":
			toolResult = block
		}
	}
	if !serverUse.Exists() || serverUse.Get("name").String() != "bash_code_execution" || serverUse.Get("id").String() != "srvtoolu_fixture_claude-messages-code-execution" || !toolResult.Exists() {
		t.Fatal("captured Claude server execution call/result pair is incomplete")
	}
	if serverUse.Get("id").String() != toolResult.Get("tool_use_id").String() || toolResult.Get("content.return_code").Int() != 0 || toolResult.Get("content.stdout").String() == "" {
		t.Fatal("captured Claude execution result or deterministic ID correlation changed")
	}

	interpreterRequest := readNativeServerToolFixture(t, "gpt-responses-code-interpreter", "request.json")
	interpreterResponse := readNativeServerToolFixture(t, "gpt-responses-code-interpreter", "response.json")
	if nativeServerToolStatus(t, "gpt-responses-code-interpreter") != 400 || gjson.GetBytes(interpreterRequest, "tools.0.type").String() != "code_interpreter" || gjson.GetBytes(interpreterResponse, "error.code").String() != "unsupported_value" {
		t.Fatal("captured GPT code_interpreter provider rejection changed")
	}
}

func TestCapturedNativeServerToolSameFormatRequestsRemainNative(t *testing.T) {
	cases := []struct {
		capture  string
		api      string
		endpoint string
	}{
		{capture: "gemini-chat-search", api: "openai", endpoint: EndpointChatCompletions},
		{capture: "gpt-responses-search", api: "openai-response", endpoint: EndpointResponses},
		{capture: "claude-messages-web-fetch", api: "claude", endpoint: EndpointMessages},
		{capture: "claude-messages-code-execution", api: "claude", endpoint: EndpointMessages},
		{capture: "gpt-responses-code-interpreter", api: "openai-response", endpoint: EndpointResponses},
	}
	for _, test := range cases {
		t.Run(test.capture, func(t *testing.T) {
			body := readNativeServerToolFixture(t, test.capture, "request.json")
			model := gjson.GetBytes(body, "model").String()
			translated, err := RequestForEndpointFrom(test.api, model, body, false, test.endpoint)
			if err != nil {
				t.Fatal(err)
			}
			requireLiveBodyJSONEqual(t, translated, body)
		})
	}
}

func TestCapturedNativeServerToolCrossFormatRefusalsRemainExplicit(t *testing.T) {
	claudeExecution := readNativeServerToolFixture(t, "claude-messages-code-execution", "request.json")
	if _, err := RequestForEndpointFrom("claude", "gpt-6-luna", claudeExecution, false, EndpointResponses); err == nil || !strings.Contains(err.Error(), "Claude native tool type") {
		t.Fatalf("Claude code execution declaration was not rejected explicitly: %v", err)
	}
	claudeFetch := readNativeServerToolFixture(t, "claude-messages-web-fetch", "request.json")
	if _, err := RequestForEndpointFrom("claude", "gpt-6-luna", claudeFetch, false, EndpointResponses); err == nil || !strings.Contains(err.Error(), "Claude native tool type") {
		t.Fatalf("Claude WebFetch declaration was not rejected explicitly: %v", err)
	}
	interpreter := readNativeServerToolFixture(t, "gpt-responses-code-interpreter", "request.json")
	if _, err := RequestForEndpointFrom("openai-response", "claude-haiku-5.5", interpreter, false, EndpointMessages); err == nil || !strings.Contains(err.Error(), "native code_interpreter") {
		t.Fatalf("Responses code_interpreter declaration was not rejected explicitly: %v", err)
	}
	searchRequest := readNativeServerToolFixture(t, "gpt-responses-search", "request.json")
	if _, err := RequestForEndpointFrom("openai-response", "claude-haiku-5.5", searchRequest, false, EndpointMessages); err == nil || !strings.Contains(err.Error(), "search_context_size") {
		t.Fatalf("unrepresentable captured web_search options were not rejected explicitly: %v", err)
	}

	gptSearchResponse := readNativeServerToolFixture(t, "gpt-responses-search", "response.json")
	if _, err := ResponsesToClaude(context.Background(), "claude-haiku-5.5", nil, nil, gptSearchResponse); err == nil || !strings.Contains(err.Error(), "Claude server tool item ID") {
		t.Fatalf("Responses hosted search output was not refused explicitly: %v", err)
	}
	claudeExecutionResponse := readNativeServerToolFixture(t, "claude-messages-code-execution", "response.json")
	if _, err := ResponseFromEndpoint(context.Background(), EndpointMessages, "openai-response", "gpt-6-luna", claudeExecution, claudeExecution, claudeExecutionResponse); err == nil || !strings.Contains(err.Error(), "server tool block is unsupported") {
		t.Fatalf("Claude server execution result was not refused explicitly: %v", err)
	}
}
