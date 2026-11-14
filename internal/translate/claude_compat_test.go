package translate

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestResponsesClaudeToolIDReplayPreservesItemAndCallIDs(t *testing.T) {
	const itemID = "fc_opaque+value/with/slash"
	const callID = "call_opaque+value/with/slash"
	responseBody := []byte(`{"id":"resp_1","status":"completed","model":"gpt-test","output":[{"id":"` + itemID + `","type":"function_call","call_id":"` + callID + `","name":"lookup","arguments":"{\"q\":\"x\"}"}],"usage":{"input_tokens":2,"output_tokens":1}}`)
	claudeResponse, err := ResponseFromEndpoint(context.Background(), EndpointResponses, "claude", "gpt-test", nil, nil, responseBody)
	if err != nil {
		t.Fatalf("translate Responses response: %v", err)
	}
	toolID := gjson.GetBytes(claudeResponse, "content.0.id").String()
	if !strings.HasPrefix(toolID, claudeToolIDPrefix) || !safeClaudeToolID(toolID) {
		t.Fatalf("Claude tool ID is not a safe reversible carrier: %q", toolID)
	}
	if gotItemID, gotCallID, ok := DecodeClaudeToolIDs(toolID); !ok || gotItemID != itemID || gotCallID != callID {
		t.Fatalf("decoded tool IDs = (%q, %q, %t), want (%q, %q, true)", gotItemID, gotCallID, ok, itemID, callID)
	}
	if gotItemID, gotCallID, ok := DecodeClaudeToolIDs("toolu_plain"); ok || gotItemID != "" || gotCallID != "toolu_plain" {
		t.Fatalf("plain Claude ID decode = (%q, %q, %t)", gotItemID, gotCallID, ok)
	}
	requestBody, err := json.Marshal(map[string]any{
		"messages": []any{
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": toolID, "name": "lookup", "input": map[string]any{"q": "x"}}}},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": toolID, "content": "found"}}},
		},
	})
	if err != nil {
		t.Fatalf("encode Claude replay request: %v", err)
	}
	replayed, err := RequestForEndpointFrom("claude", "gpt-test", requestBody, false, EndpointResponses)
	if err != nil {
		t.Fatalf("translate Claude replay request: %v", err)
	}
	if got := gjson.GetBytes(replayed, "input.0.id").String(); got != itemID {
		t.Fatalf("function_call.id = %q, want %q; request=%s", got, itemID, replayed)
	}
	if got := gjson.GetBytes(replayed, "input.0.call_id").String(); got != callID {
		t.Fatalf("function_call.call_id = %q, want %q; request=%s", got, callID, replayed)
	}
	if got := gjson.GetBytes(replayed, "input.1.call_id").String(); got != callID {
		t.Fatalf("function_call_output.call_id = %q, want %q; request=%s", got, callID, replayed)
	}
}

func TestClaudeResponsesRequestIncludesStatelessEncryptedReasoning(t *testing.T) {
	requestBody, err := json.Marshal(map[string]any{
		"max_tokens":       8192,
		"prompt_cache_key": "cache-prefix-1",
		"system": []any{map[string]any{
			"type": "text", "text": "Use this cached prefix.", "cache_control": map[string]any{"type": "ephemeral"},
		}},
		"thinking": map[string]any{"type": "enabled", "budget_tokens": 4096},
		"tools": []any{map[string]any{
			"name":        "lookup",
			"description": "Look up a value",
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{
				"query":         map[string]any{"type": "string"},
				"cache_control": map[string]any{"type": "string"},
			}},
			"cache_control": map[string]any{"type": "ephemeral"},
		}},
		"tool_choice": map[string]any{"type": "tool", "name": "lookup"},
		"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{
			"type": "text", "text": "Find a value", "cache_control": map[string]any{"type": "ephemeral"},
		}}}},
	})
	if err != nil {
		t.Fatalf("encode Claude request: %v", err)
	}
	responsesRequest, err := RequestForEndpointFrom("claude", "gpt-test", requestBody, true, EndpointResponses)
	if err != nil {
		t.Fatalf("translate Claude request: %v", err)
	}
	if !gjson.GetBytes(responsesRequest, "store").Exists() || gjson.GetBytes(responsesRequest, "store").Bool() {
		t.Fatalf("Responses store = %s, want false; request=%s", gjson.GetBytes(responsesRequest, "store"), responsesRequest)
	}
	if got := gjson.GetBytes(responsesRequest, "prompt_cache_key").String(); got != "cache-prefix-1" {
		t.Fatalf("prompt_cache_key = %q; request=%s", got, responsesRequest)
	}
	for _, path := range []string{"tools.0.cache_control", "input.0.cache_control", "input.0.content.0.cache_control", "input.1.content.0.cache_control"} {
		if gjson.GetBytes(responsesRequest, path).Exists() {
			t.Fatalf("Claude cache_control leaked at %s: %s", path, responsesRequest)
		}
	}
	if got := gjson.GetBytes(responsesRequest, "input.0.content.0.text").String(); got != "Use this cached prefix." {
		t.Fatalf("system prefix text = %q; request=%s", got, responsesRequest)
	}
	if got := gjson.GetBytes(responsesRequest, "input.1.content.0.text").String(); got != "Find a value" {
		t.Fatalf("user text = %q; request=%s", got, responsesRequest)
	}
	include := gjson.GetBytes(responsesRequest, "include").Array()
	if len(include) != 1 || include[0].String() != "reasoning.encrypted_content" {
		t.Fatalf("Responses include = %s, want [reasoning.encrypted_content]; request=%s", gjson.GetBytes(responsesRequest, "include"), responsesRequest)
	}
	if !gjson.GetBytes(responsesRequest, "stream").Bool() || gjson.GetBytes(responsesRequest, "max_output_tokens").Int() != 8192 {
		t.Fatalf("Claude stream or token limit was not preserved: %s", responsesRequest)
	}
	if gjson.GetBytes(responsesRequest, "reasoning.effort").String() != "medium" || gjson.GetBytes(responsesRequest, "reasoning.summary").String() != "auto" {
		t.Fatalf("Claude reasoning settings changed: %s", responsesRequest)
	}
	if gjson.GetBytes(responsesRequest, "tools.0.type").String() != "function" || gjson.GetBytes(responsesRequest, "tools.0.name").String() != "lookup" || gjson.GetBytes(responsesRequest, "tools.0.parameters.type").String() != "object" {
		t.Fatalf("Claude tool definition changed: %s", responsesRequest)
	}
	if got := gjson.GetBytes(responsesRequest, "tools.0.parameters.properties.cache_control.type").String(); got != "string" {
		t.Fatalf("tool schema cache_control property = %q; request=%s", got, responsesRequest)
	}
	if gjson.GetBytes(responsesRequest, "tool_choice.type").String() != "function" || gjson.GetBytes(responsesRequest, "tool_choice.name").String() != "lookup" {
		t.Fatalf("Claude tool choice changed: %s", responsesRequest)
	}
}

func TestClaudeResponsesToolSchemaAliasesPreserveCallerTools(t *testing.T) {
	schema := map[string]any{
		"$schema":              "urn:synthetic-schema",
		"type":                 "object",
		"properties":           map[string]any{"key": map[string]any{"type": "string"}},
		"required":             []any{"key"},
		"additionalProperties": false,
		"anyOf": []any{
			map[string]any{"type": "object"},
			map[string]any{"type": "array"},
		},
	}
	for _, test := range []struct {
		name      string
		toolExtra map[string]any
		want      map[string]any
	}{
		{
			name:      "input_schema",
			toolExtra: map[string]any{"input_schema": schema},
			want:      schema,
		},
		{
			name:      "flat_parameters",
			toolExtra: map[string]any{"type": "function", "parameters": schema},
			want:      schema,
		},
		{
			name:      "matching_both",
			toolExtra: map[string]any{"input_schema": schema, "parameters": schema},
			want:      schema,
		},
		{
			name:      "missing_schema_keeps_empty_object",
			toolExtra: map[string]any{},
			want:      map[string]any{},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tool := map[string]any{
				"name":          "lookup",
				"description":   "Look up a value.",
				"cache_control": map[string]any{"type": "ephemeral"},
			}
			for key, value := range test.toolExtra {
				tool[key] = value
			}
			callerTool := map[string]any{"name": "inspect", "input_schema": map[string]any{"type": "object"}}
			requestBody, err := json.Marshal(map[string]any{"tools": []any{tool, callerTool}})
			if err != nil {
				t.Fatalf("encode Claude tools: %v", err)
			}
			responsesRequest, err := claudeRequestToResponses("gpt-6-luna", requestBody, false)
			if err != nil {
				t.Fatalf("translate Claude tools: %v", err)
			}
			tools := gjson.GetBytes(responsesRequest, "tools").Array()
			if len(tools) != 2 || tools[0].Get("name").String() != "lookup" || tools[1].Get("name").String() != "inspect" {
				t.Fatalf("caller tool set changed: %s", responsesRequest)
			}
			if tools[0].Get("type").String() != "function" || tools[0].Get("description").String() != "Look up a value." {
				t.Fatalf("tool type or description changed: %s", responsesRequest)
			}
			if tools[0].Get("cache_control.type").String() != "ephemeral" {
				t.Fatalf("tool cache_control changed: %s", responsesRequest)
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(tools[0].Get("parameters").Raw), &got); err != nil {
				t.Fatalf("decode converted parameters: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("converted schema = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestClaudeResponsesToolSchemaAliasesRejectMalformedOrConflictingValues(t *testing.T) {
	validSchema := map[string]any{"type": "object", "properties": map[string]any{"key": map[string]any{"type": "string"}}}
	for _, test := range []struct {
		name      string
		toolExtra map[string]any
		wantError string
	}{
		{
			name:      "conflict",
			toolExtra: map[string]any{"input_schema": validSchema, "parameters": map[string]any{"type": "array"}},
			wantError: "input_schema and parameters conflict",
		},
		{
			name:      "invalid_input_schema",
			toolExtra: map[string]any{"input_schema": "not-an-object"},
			wantError: "input_schema must be an object",
		},
		{
			name:      "invalid_parameters",
			toolExtra: map[string]any{"parameters": []any{validSchema}},
			wantError: "parameters must be an object",
		},
		{
			name:      "valid_input_schema_with_invalid_parameters",
			toolExtra: map[string]any{"input_schema": validSchema, "parameters": nil},
			wantError: "parameters must be an object",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tool := map[string]any{"name": "lookup"}
			for key, value := range test.toolExtra {
				tool[key] = value
			}
			requestBody, err := json.Marshal(map[string]any{"tools": []any{tool}})
			if err != nil {
				t.Fatalf("encode Claude tool: %v", err)
			}
			if _, err := claudeRequestToResponses("gpt-6-luna", requestBody, false); err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("schema error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestClaudeRequestRejectsNativeToolsThatWouldBecomeFunctions(t *testing.T) {
	for _, typ := range []string{"web_search_20250305", "web_search_20260209", "web_search_preview", "web_search", "image_generation"} {
		t.Run(typ, func(t *testing.T) {
			requestBody, err := json.Marshal(map[string]any{"tools": []any{map[string]any{"type": typ, "name": typ}}})
			if err != nil {
				t.Fatalf("encode Claude native tool: %v", err)
			}
			if _, err := RequestForEndpointFrom("claude", "gpt-test", requestBody, false, EndpointResponses); err == nil || !strings.Contains(err.Error(), typ) {
				t.Fatalf("native tool translation error = %v, want explicit %q rejection", err, typ)
			}
		})
	}
}

func TestClaudeRequestKeepsOrdinaryFunctionsNamedLikeNativeTools(t *testing.T) {
	requestBody := []byte(`{"tools":[{"name":"web_search","input_schema":{"type":"object"}},{"type":"custom","name":"web_search_preview","input_schema":{"type":"object"}},{"type":"function","name":"image_generation","input_schema":{"type":"object"}}]}`)
	translated, err := RequestForEndpointFrom("claude", "gpt-test", requestBody, false, EndpointResponses)
	if err != nil {
		t.Fatalf("translate ordinary functions: %v", err)
	}
	tools := gjson.GetBytes(translated, "tools").Array()
	if len(tools) != 3 {
		t.Fatalf("translated tools = %s", gjson.GetBytes(translated, "tools"))
	}
	wantNames := []string{"web_search", "web_search_preview", "image_generation"}
	for index, tool := range tools {
		if tool.Get("type").String() != "function" || tool.Get("name").String() != wantNames[index] {
			t.Fatalf("ordinary tool %d was not preserved as a function: %s", index, translated)
		}
	}
}

func TestClaudeReasoningPayloadsPreserveOpaqueWhitespace(t *testing.T) {
	const thinking = " inspect carefully \n"
	const signature = " sig "
	const redacted = "\nopaque redacted\t"
	requestBody, err := json.Marshal(map[string]any{"messages": []any{map[string]any{"role": "assistant", "content": []any{
		map[string]any{"type": "thinking", "thinking": thinking, "signature": signature},
		map[string]any{"type": "redacted_thinking", "data": redacted},
	}}}})
	if err != nil {
		t.Fatalf("encode Claude reasoning request: %v", err)
	}
	responsesRequest, err := RequestForEndpointFrom("claude", "gpt-test", requestBody, false, EndpointResponses)
	if err != nil {
		t.Fatalf("translate Claude reasoning request: %v", err)
	}
	if gjson.GetBytes(responsesRequest, "input.0.summary.0.text").String() != thinking || gjson.GetBytes(responsesRequest, "input.0.encrypted_content").String() != signature {
		t.Fatalf("Claude thinking payload changed: %s", responsesRequest)
	}
	if got := gjson.GetBytes(responsesRequest, "input.1.encrypted_content").String(); got != redactedThinkingPrefix+redacted {
		t.Fatalf("Claude redacted payload changed: %q", got)
	}
	responseBody, err := json.Marshal(map[string]any{
		"id": "resp_reasoning", "status": "completed", "model": "gpt-test",
		"output": []any{
			map[string]any{"type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": thinking}}, "encrypted_content": signature},
			map[string]any{"type": "reasoning", "summary": []any{}, "encrypted_content": redactedThinkingPrefix + redacted},
		},
		"usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
	})
	if err != nil {
		t.Fatalf("encode Responses reasoning response: %v", err)
	}
	claudeResponse, err := ResponseFromEndpoint(context.Background(), EndpointResponses, "claude", "gpt-test", nil, nil, responseBody)
	if err != nil {
		t.Fatalf("translate Responses reasoning response: %v", err)
	}
	if got := gjson.GetBytes(claudeResponse, "content.0.thinking").String(); got != thinking {
		t.Fatalf("Claude thinking text = %q, want %q; response=%s", got, thinking, claudeResponse)
	}
	if got := gjson.GetBytes(claudeResponse, "content.0.signature").String(); got != signature {
		t.Fatalf("Claude thinking signature = %q, want %q; response=%s", got, signature, claudeResponse)
	}
	if got := gjson.GetBytes(claudeResponse, "content.1.data").String(); got != redacted {
		t.Fatalf("Claude redacted data = %q, want %q; response=%s", got, redacted, claudeResponse)
	}
}

func TestClaudeOutputConfigEffortMarkerIsNormalizedWithoutDroppingMessages(t *testing.T) {
	const model = "gpt-test"
	requestBody := []byte(`{"model":"claude","prompt_cache_key":"marker-cache-key","messages":[{"role":"system","content":[],"output_config":{"effort":"low"}},{"role":"user","content":"keep this prompt"},{"role":"system","content":[],"output_config":{"effort":"high"}}]}`)
	responsesRequest, err := RequestForEndpointFrom("claude", model, requestBody, false, EndpointResponses)
	if err != nil {
		t.Fatalf("translate Claude effort-marker request to Responses: %v", err)
	}
	if got := gjson.GetBytes(responsesRequest, "reasoning.effort").String(); got != "high" {
		t.Fatalf("Responses reasoning effort = %q; request=%s", got, responsesRequest)
	}
	if got := gjson.GetBytes(responsesRequest, "input.0.content.0.text").String(); got != "keep this prompt" {
		t.Fatalf("Responses user prompt = %q; request=%s", got, responsesRequest)
	}
	if got := gjson.GetBytes(responsesRequest, "prompt_cache_key").String(); got != "marker-cache-key" {
		t.Fatalf("Responses prompt_cache_key = %q; request=%s", got, responsesRequest)
	}
	if strings.Contains(string(responsesRequest), `"role":"system"`) {
		t.Fatalf("synthetic effort markers reached Responses: %s", responsesRequest)
	}
	chatRequest, err := RequestForEndpointFrom("claude", model, requestBody, false, EndpointChatCompletions)
	if err != nil {
		t.Fatalf("translate Claude effort-marker request to Chat: %v", err)
	}
	if got := gjson.GetBytes(chatRequest, "reasoning_effort").String(); got != "high" {
		t.Fatalf("Chat reasoning effort = %q; request=%s", got, chatRequest)
	}
	if got := gjson.GetBytes(chatRequest, "messages.0.content.0.text").String(); got != "keep this prompt" {
		t.Fatalf("Chat prompt was lost: %s", chatRequest)
	}
	rootEffortWins := []byte(`{"output_config":{"effort":"low"},"messages":[{"role":"system","content":[],"output_config":{"effort":"high"}},{"role":"user","content":"keep"}]}`)
	responsesRequest, err = RequestForEndpointFrom("claude", model, rootEffortWins, false, EndpointResponses)
	if err != nil {
		t.Fatalf("translate root effort request: %v", err)
	}
	if got := gjson.GetBytes(responsesRequest, "reasoning.effort").String(); got != "low" {
		t.Fatalf("root reasoning effort = %q; request=%s", got, responsesRequest)
	}
	for _, effort := range []string{"xhigh", "max"} {
		request := []byte(`{"output_config":{"effort":"` + effort + `"},"messages":[{"role":"user","content":"keep"}]}`)
		translated, err := RequestForEndpointFrom("claude", model, request, false, EndpointResponses)
		if err != nil {
			t.Fatalf("translate %s effort request: %v", effort, err)
		}
		if got := gjson.GetBytes(translated, "reasoning.effort").String(); got != effort {
			t.Fatalf("Responses reasoning effort = %q, want %q; request=%s", got, effort, translated)
		}
	}
	unknownMarker := []byte(`{"messages":[{"role":"system","content":[],"output_config":{"effort":"high","extra":true}},{"role":"user","content":"keep"}]}`)
	if _, err := RequestForEndpointFrom("claude", model, unknownMarker, false, EndpointResponses); err == nil {
		t.Fatal("unknown synthetic system marker field was silently dropped")
	}
	meaningfulMarker := []byte(`{"messages":[{"role":"system","content":[{"type":"text","text":"must not drop"}],"output_config":{"effort":"high"}},{"role":"user","content":"keep"}]}`)
	translated, err := RequestForEndpointFrom("claude", model, meaningfulMarker, false, EndpointResponses)
	if err != nil {
		t.Fatalf("translate effort-bearing system content: %v", err)
	}
	if got := gjson.GetBytes(translated, "input.0.role").String(); got != "system" {
		t.Fatalf("system content authority changed to role %q; request=%s", got, translated)
	}
	if got := gjson.GetBytes(translated, "input.0.content.0.text").String(); got != "must not drop" {
		t.Fatalf("system content was lost: %s", translated)
	}
	if got := gjson.GetBytes(translated, "reasoning.effort").String(); got != "high" {
		t.Fatalf("system effort = %q, want high; request=%s", got, translated)
	}
}

func TestClaudeEmptySystemEffortMarkerAcceptsEmptyContentForms(t *testing.T) {
	for _, content := range []string{`""`, `null`} {
		t.Run(content, func(t *testing.T) {
			requestBody := []byte(`{"messages":[{"role":"system","content":` + content + `,"output_config":{"effort":"high"}},{"role":"user","content":"keep"}]}`)
			translated, err := RequestForEndpointFrom("claude", "gpt-test", requestBody, false, EndpointResponses)
			if err != nil {
				t.Fatalf("translate empty effort marker: %v", err)
			}
			if got := gjson.GetBytes(translated, "reasoning.effort").String(); got != "high" {
				t.Fatalf("reasoning effort = %q, want high; request=%s", got, translated)
			}
			if got := gjson.GetBytes(translated, "input.0.role").String(); got != "user" {
				t.Fatalf("empty effort marker reached Responses input: %s", translated)
			}
		})
	}
}

func TestClaudeSystemContentWithEffortPreservesOrderAndCacheControl(t *testing.T) {
	requestBody := []byte(`{"messages":[{"role":"user","content":"before"},{"role":"system","content":"keep this policy","cache_control":{"type":"ephemeral"},"output_config":{"effort":"low"}},{"role":"system","content":[{"type":"text","text":"second policy"},{"type":"text","text":"cached policy","cache_control":{"type":"ephemeral"}}],"output_config":{"effort":"high"}},{"role":"user","content":"after"}]}`)
	translated, err := claudeRequestToResponses("gpt-test", requestBody, false)
	if err != nil {
		t.Fatalf("translate effort-bearing system messages: %v", err)
	}
	input := gjson.GetBytes(translated, "input").Array()
	if len(input) != 4 || input[0].Get("role").String() != "user" || input[1].Get("role").String() != "system" || input[2].Get("role").String() != "system" || input[3].Get("role").String() != "user" {
		t.Fatalf("message order or system authority changed: %s", translated)
	}
	if got := input[1].Get("content.0.text").String(); got != "keep this policy" {
		t.Fatalf("system string content = %q; request=%s", got, translated)
	}
	if got := input[1].Get("content.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("message cache_control moved or was lost: %s", translated)
	}
	if got := input[2].Get("content.0.text").String() + input[2].Get("content.1.text").String(); got != "second policycached policy" {
		t.Fatalf("system block content changed: %s", translated)
	}
	if got := input[2].Get("content.1.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("block cache_control was lost: %s", translated)
	}
	if got := gjson.GetBytes(translated, "reasoning.effort").String(); got != "high" {
		t.Fatalf("last system effort = %q, want high; request=%s", got, translated)
	}
	rootEffortWins := []byte(`{"output_config":{"effort":"low"},"messages":[{"role":"system","content":"keep this policy","output_config":{"effort":"high"}}]}`)
	translated, err = claudeRequestToResponses("gpt-test", rootEffortWins, false)
	if err != nil {
		t.Fatalf("translate root effort with system content: %v", err)
	}
	if got := gjson.GetBytes(translated, "reasoning.effort").String(); got != "low" {
		t.Fatalf("root effort = %q, want low; request=%s", got, translated)
	}
}

func TestClaudeSystemContentWithEffortStillRejectsUnsupportedSemantics(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "non_text_block",
			body: `{"messages":[{"role":"system","content":[{"type":"image","source":{}}],"output_config":{"effort":"high"}}]}`,
			want: "unsupported non-text Claude system block",
		},
		{
			name: "clear_at",
			body: `{"messages":[{"role":"system","content":"policy","clear_at":"next_user_message","output_config":{"effort":"high"}}]}`,
			want: "clear_at",
		},
		{
			name: "unknown_message_field",
			body: `{"messages":[{"role":"system","content":"policy","unknown":"value","output_config":{"effort":"high"}}]}`,
			want: "unsupported Claude system message field",
		},
		{
			name: "unknown_output_config_option",
			body: `{"messages":[{"role":"system","content":"policy","output_config":{"effort":"high","temperature":0.2}}]}`,
			want: "unsupported Claude system message",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := RequestForEndpointFrom("claude", "gpt-test", []byte(test.body), false, EndpointResponses); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("unsupported system content error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestClaudeMidConversationSystemMessagePreservesOrderAndCacheControl(t *testing.T) {
	const model = "gemini-3.8-flash"
	requestBody := []byte(`{"system":[{"type":"text","text":"root cached prefix","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"Find a value."},{"role":"system","content":[{"type":"text","text":"Check the result carefully."},{"type":"text","text":"Keep this instruction cached.","cache_control":{"type":"ephemeral"}}]},{"role":"assistant","content":[{"type":"text","text":"I will look it up."},{"type":"tool_use","id":"toolu_lookup_1","name":"lookup","input":{"query":"value"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_lookup_1","content":"found"},{"type":"text","text":"Continue."}]}]}`)
	intermediate, err := claudeRequestToResponses(model, requestBody, false)
	if err != nil {
		t.Fatalf("translate Claude request to Responses input: %v", err)
	}
	input := gjson.GetBytes(intermediate, "input").Array()
	if len(input) != 7 {
		t.Fatalf("Responses input has %d items, want 7; request=%s", len(input), intermediate)
	}
	if input[0].Get("role").String() != "system" || input[0].Get("content.0.text").String() != "root cached prefix" {
		t.Fatalf("root cached system prefix moved: %s", intermediate)
	}
	if input[1].Get("role").String() != "user" || input[1].Get("content.0.text").String() != "Find a value." {
		t.Fatalf("initial user message moved: %s", intermediate)
	}
	if input[2].Get("role").String() != "system" || input[2].Get("content.0.text").String() != "Check the result carefully." || input[2].Get("content.1.text").String() != "Keep this instruction cached." {
		t.Fatalf("mid-conversation system content or order changed: %s", intermediate)
	}
	if input[2].Get("content.1.cache_control.type").String() != "ephemeral" {
		t.Fatalf("mid-conversation cache_control was lost: %s", intermediate)
	}
	if input[3].Get("role").String() != "assistant" || input[4].Get("type").String() != "function_call" || input[5].Get("type").String() != "function_call_output" || input[6].Get("role").String() != "user" {
		t.Fatalf("assistant and tool turn order changed: %s", intermediate)
	}
	chatRequest, err := RequestForEndpointFrom("claude", model, requestBody, false, EndpointChatCompletions)
	if err != nil {
		t.Fatalf("translate Claude request to Chat: %v", err)
	}
	messages := gjson.GetBytes(chatRequest, "messages").Array()
	if len(messages) != 6 || messages[0].Get("role").String() != "system" || messages[1].Get("role").String() != "user" || messages[2].Get("role").String() != "system" || messages[3].Get("role").String() != "assistant" || messages[4].Get("role").String() != "tool" || messages[5].Get("role").String() != "user" {
		t.Fatalf("Chat message order changed: %s", chatRequest)
	}
	if messages[2].Get("content.0.text").String() != "Check the result carefully." || messages[2].Get("content.1.text").String() != "Keep this instruction cached." || messages[2].Get("content.1.cache_control.type").String() != "ephemeral" {
		t.Fatalf("Chat mid-conversation system content or cache boundary changed: %s", chatRequest)
	}
}

func TestClaudeMidConversationSystemMessageRejectsUnsupportedSemantics(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "clear_at",
			body: `{"messages":[{"role":"user","content":"start"},{"role":"system","content":"temporary instruction","clear_at":"next_user_message"}]}`,
			want: "clear_at",
		},
		{
			name: "tool_addition",
			body: `{"messages":[{"role":"user","content":"start"},{"role":"system","content":[{"type":"tool_addition","tools":[] }]}]}`,
			want: "tool_addition",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := claudeRequestToResponses("gpt-test", []byte(test.body), false); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("unsupported system semantics error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestNativeProtocolPassthroughPreservesToolIDs(t *testing.T) {
	responsesRequest := []byte(`{"model":"old","stream":false,"input":[{"type":"function_call","id":"fc+opaque/1","call_id":"call+opaque/1","name":"lookup","arguments":"{}"}]}`)
	translatedRequest, err := RequestForEndpointFrom("openai-response", "new", responsesRequest, true, EndpointResponses)
	if err != nil {
		t.Fatalf("translate native Responses request: %v", err)
	}
	if got := gjson.GetBytes(translatedRequest, "input.0.id").String(); got != "fc+opaque/1" {
		t.Fatalf("native request item id = %q; request=%s", got, translatedRequest)
	}
	if got := gjson.GetBytes(translatedRequest, "input.0.call_id").String(); got != "call+opaque/1" {
		t.Fatalf("native request call_id = %q; request=%s", got, translatedRequest)
	}
	if got := gjson.GetBytes(translatedRequest, "model").String(); got != "new" {
		t.Fatalf("native request model = %q", got)
	}
	if !gjson.GetBytes(translatedRequest, "stream").Bool() {
		t.Fatal("native request stream flag was not updated")
	}
	responsesBody := []byte(`{"id":"resp_1","status":"completed","output":[{"id":"fc+opaque/1","type":"function_call","call_id":"call+opaque/1","name":"lookup","arguments":"{}"}]}`)
	responsesBodyOut, err := ResponseFromEndpoint(context.Background(), EndpointResponses, "openai-response", "new", nil, nil, responsesBody)
	if err != nil {
		t.Fatalf("pass through native Responses response: %v", err)
	}
	if string(responsesBodyOut) != string(responsesBody) {
		t.Fatalf("native Responses response changed: %s", responsesBodyOut)
	}
	claudeRequest := []byte(`{"model":"old","stream":false,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_a/b+","name":"lookup","input":{}}]}]}`)
	claudeRequestOut, err := RequestForEndpointFrom("claude", "new", claudeRequest, true, EndpointMessages)
	if err != nil {
		t.Fatalf("pass through native Claude request: %v", err)
	}
	if got := gjson.GetBytes(claudeRequestOut, "messages.0.content.0.id").String(); got != "toolu_a/b+" {
		t.Fatalf("native Claude tool ID = %q; request=%s", got, claudeRequestOut)
	}
	claudeResponse := []byte(`{"type":"message","content":[{"type":"tool_use","id":"toolu_a/b+"}]}`)
	claudeResponseOut, err := ResponseFromEndpoint(context.Background(), EndpointMessages, "claude", "new", nil, nil, claudeResponse)
	if err != nil {
		t.Fatalf("pass through native Claude response: %v", err)
	}
	if string(claudeResponseOut) != string(claudeResponse) {
		t.Fatalf("native Claude response changed: %s", claudeResponseOut)
	}
	frame := []byte("event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fc+opaque/1\",\"type\":\"function_call\",\"call_id\":\"call+opaque/1\"}}\n\n")
	var state any
	frames, err := StreamFromEndpoint(context.Background(), EndpointResponses, "openai-response", "new", nil, nil, frame, &state)
	if err != nil {
		t.Fatalf("pass through native Responses stream: %v", err)
	}
	if len(frames) != 1 || string(frames[0]) != string(frame) {
		t.Fatalf("native Responses stream changed: %q", frames)
	}
}

func TestResponsesStreamPreservesTerminalReasoningAndLateUsage(t *testing.T) {
	chunks := [][]byte{
		[]byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\",\"model\":\"gpt-test\",\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\n"),
		[]byte("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"summary\":[]}}\n\n"),
		[]byte("event: response.reasoning_summary_text.delta\ndata: {\"type\":\"response.reasoning_summary_text.delta\",\"output_index\":0,\"item_id\":\"rs_1\",\"delta\":\"think\"}\n\n"),
		[]byte("event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"think\"}]}}\n\n"),
		[]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-test\",\"output\":[{\"id\":\"rs_1\",\"type\":\"reasoning\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"think\"}],\"encrypted_content\":\"terminal-signature\"}],\"usage\":{\"input_tokens\":11,\"output_tokens\":7,\"input_tokens_details\":{\"cached_tokens\":4}}}}\n\n"),
	}
	var state any
	var payloads []gjson.Result
	for _, chunk := range chunks {
		frames, err := ResponsesSSEToClaude(context.Background(), "gpt-test", nil, nil, chunk, &state)
		if err != nil {
			t.Fatalf("translate Responses event: %v", err)
		}
		payloads = append(payloads, claudePayloads(t, frames)...)
	}
	signatureCount := 0
	var finalUsage gjson.Result
	for _, payload := range payloads {
		if payload.Get("delta.type").String() == "signature_delta" && payload.Get("delta.signature").String() == "terminal-signature" {
			signatureCount++
		}
		if payload.Get("type").String() == "message_delta" {
			finalUsage = payload.Get("usage")
		}
	}
	if signatureCount != 1 {
		t.Fatalf("terminal signature count = %d; payloads=%v", signatureCount, payloads)
	}
	if finalUsage.Get("input_tokens").Int() != 11 || finalUsage.Get("output_tokens").Int() != 7 || finalUsage.Get("cache_read_input_tokens").Int() != 4 {
		t.Fatalf("late final usage was not preserved: %s", finalUsage.Raw)
	}
}

func TestResponsesStreamErrorsDoNotExposeProviderMessages(t *testing.T) {
	const secret = "private-prompt-fragment-73e4"
	for _, frame := range [][]byte{
		[]byte("event: error\ndata: {\"type\":\"error\",\"message\":\"" + secret + "\"}\n\n"),
		[]byte("event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"" + secret + "\"}}}\n\n"),
	} {
		var state any
		if _, err := StreamFromEndpoint(context.Background(), EndpointResponses, "claude", "gpt-test", nil, nil, frame, &state); err == nil {
			t.Fatal("provider stream error was not returned")
		} else if strings.Contains(err.Error(), secret) {
			t.Fatalf("provider stream error exposed secret content: %v", err)
		}
	}
	const body = `{"id":"resp_1","status":"failed","error":{"message":"private-prompt-fragment-73e4"}}`
	if _, err := ResponseFromEndpoint(context.Background(), EndpointResponses, "claude", "gpt-test", nil, nil, []byte(body)); err == nil {
		t.Fatal("provider Responses failure was not returned")
	} else if strings.Contains(err.Error(), secret) {
		t.Fatalf("provider Responses failure exposed secret content: %v", err)
	}
}

func TestResponsesTerminalSignatureOnlyReasoning(t *testing.T) {
	chunk := []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_2\",\"status\":\"completed\",\"model\":\"gpt-test\",\"output\":[{\"id\":\"rs_2\",\"type\":\"reasoning\",\"summary\":[],\"encrypted_content\":\"signature-only\"}],\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n")
	var state any
	frames, err := ResponsesSSEToClaude(context.Background(), "gpt-test", nil, nil, chunk, &state)
	if err != nil {
		t.Fatalf("translate terminal reasoning response: %v", err)
	}
	payloads := claudePayloads(t, frames)
	var signatureCount int
	for _, payload := range payloads {
		if payload.Get("delta.type").String() == "signature_delta" && payload.Get("delta.signature").String() == "signature-only" {
			signatureCount++
		}
	}
	if signatureCount != 1 {
		t.Fatalf("signature-only reasoning emitted %d signatures; frames=%q", signatureCount, frames)
	}
}

func TestResponsesStreamTerminalItemsReplayOnNextClaudeRequest(t *testing.T) {
	const itemID = "fc+terminal/item"
	const callID = "call+terminal/item"
	chunk := []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-test\",\"output\":[{\"id\":\"" + itemID + "\",\"type\":\"function_call\",\"call_id\":\"" + callID + "\",\"name\":\"lookup\",\"arguments\":\"{\\\"q\\\":\\\"x\\\"}\"},{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}],\"usage\":{\"input_tokens\":5,\"output_tokens\":3}}}\n\n")
	var state any
	frames, err := ResponsesSSEToClaude(context.Background(), "gpt-test", nil, nil, chunk, &state)
	if err != nil {
		t.Fatalf("translate terminal response: %v", err)
	}
	payloads := claudePayloads(t, frames)
	var toolID string
	var sawTerminalText bool
	for _, payload := range payloads {
		if payload.Get("content_block.type").String() == "tool_use" {
			toolID = payload.Get("content_block.id").String()
		}
		if payload.Get("delta.text").String() == "done" {
			sawTerminalText = true
		}
	}
	if toolID == "" || !safeClaudeToolID(toolID) {
		t.Fatalf("terminal tool ID missing or unsafe: %q; frames=%q", toolID, frames)
	}
	if !sawTerminalText {
		t.Fatalf("response.completed text was not emitted: %q", frames)
	}
	requestBody, err := json.Marshal(map[string]any{"messages": []any{
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": toolID, "name": "lookup", "input": map[string]any{"q": "x"}}}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": toolID, "content": "found"}}},
	}})
	if err != nil {
		t.Fatalf("encode follow-up Claude request: %v", err)
	}
	replayed, err := RequestForEndpointFrom("claude", "gpt-test", requestBody, false, EndpointResponses)
	if err != nil {
		t.Fatalf("translate follow-up Claude request: %v", err)
	}
	if gjson.GetBytes(replayed, "input.0.id").String() != itemID || gjson.GetBytes(replayed, "input.0.call_id").String() != callID || gjson.GetBytes(replayed, "input.1.call_id").String() != callID {
		t.Fatalf("terminal tool identity did not replay: %s", replayed)
	}
}

func claudePayloads(t *testing.T, frames [][]byte) []gjson.Result {
	t.Helper()
	var payloads []gjson.Result
	for _, frame := range frames {
		_, data, _, err := parseSSEFrame(frame)
		if err != nil {
			t.Fatalf("parse Claude SSE event: %v", err)
		}
		if len(data) > 0 {
			payloads = append(payloads, gjson.ParseBytes(data))
		}
	}
	return payloads
}
