package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
	"github.com/tiktoken-go/tokenizer"
)

var (
	inputTokenizerOnce  sync.Once
	inputTokenizerCodec tokenizer.Codec
	inputTokenizerErr   error
)

func (s *Service) CountTokens(req ExecuteRequest) (pluginapi.ExecutorResponse, error) {
	payload := req.OriginalRequest
	if len(bytes.TrimSpace(payload)) == 0 {
		payload = req.Payload
	}
	if normalizeRequestFormat(firstNonEmpty(req.SourceFormat, req.Format)) != "claude" {
		return pluginapi.ExecutorResponse{}, statusError(
			"unsupported_format",
			fmt.Sprintf("token counting is unsupported for request format %q", firstNonEmpty(req.SourceFormat, req.Format)),
			http.StatusUnprocessableEntity,
		)
	}
	count, errCount := countClaudeInputTokens(payload)
	if errCount != nil {
		return pluginapi.ExecutorResponse{}, statusError("invalid_request", errCount.Error(), http.StatusBadRequest)
	}
	body, errMarshal := json.Marshal(map[string]int64{"input_tokens": count})
	if errMarshal != nil {
		return pluginapi.ExecutorResponse{}, statusError("encoding_error", errMarshal.Error(), http.StatusInternalServerError)
	}
	return pluginapi.ExecutorResponse{
		Payload: body,
		Headers: http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

func countClaudeInputTokens(payload []byte) (int64, error) {
	inputTokenizerOnce.Do(func() {
		inputTokenizerCodec, inputTokenizerErr = tokenizer.Get(tokenizer.O200kBase)
	})
	if inputTokenizerErr != nil {
		return 0, fmt.Errorf("initialize O200kBase tokenizer: %w", inputTokenizerErr)
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		return 0, nil
	}
	if !gjson.ValidBytes(payload) {
		return 0, fmt.Errorf("invalid Claude request JSON")
	}

	root := gjson.ParseBytes(payload)
	segments := make([]string, 0, 32)
	collectSystemTokenSegments(root.Get("system"), &segments)
	collectMessageTokenSegments(root.Get("messages"), &segments)
	collectToolTokenSegments(root.Get("tools"), &segments)
	collectToolChoiceTokenSegments(root.Get("tool_choice"), &segments)
	if len(segments) == 0 {
		return 0, nil
	}
	count, errCount := inputTokenizerCodec.Count(strings.Join(segments, "\n"))
	if errCount != nil {
		return 0, fmt.Errorf("count Claude input tokens: %w", errCount)
	}
	return int64(count), nil
}

func collectSystemTokenSegments(system gjson.Result, segments *[]string) {
	if system.Type == gjson.String {
		appendTokenString(segments, system.String())
		return
	}
	if !system.IsArray() {
		return
	}
	system.ForEach(func(_, part gjson.Result) bool {
		if part.Type == gjson.String {
			appendTokenString(segments, part.String())
		} else if part.Get("type").String() == "text" {
			appendTokenString(segments, part.Get("text").String())
		}
		return true
	})
}

func collectMessageTokenSegments(messages gjson.Result, segments *[]string) {
	if !messages.IsArray() {
		return
	}
	messages.ForEach(func(_, message gjson.Result) bool {
		appendTokenString(segments, message.Get("role").String())
		collectContentTokenSegments(message.Get("content"), segments)
		return true
	})
}

func collectContentTokenSegments(content gjson.Result, segments *[]string) {
	if !content.Exists() {
		return
	}
	if content.Type == gjson.String {
		appendTokenString(segments, content.String())
		return
	}
	if content.IsArray() {
		content.ForEach(func(_, part gjson.Result) bool {
			collectContentTokenSegments(part, segments)
			return true
		})
		return
	}
	if !content.IsObject() {
		return
	}

	switch content.Get("type").String() {
	case "text":
		appendTokenString(segments, content.Get("text").String())
	case "thinking":
		appendTokenString(segments, content.Get("thinking").String())
	case "document":
		source := content.Get("source")
		if source.Get("type").String() == "text" {
			appendTokenString(segments, content.Get("title").String())
			appendTokenString(segments, content.Get("context").String())
			appendTokenString(segments, source.Get("data").String())
			appendTokenString(segments, source.Get("content").String())
		}
	case "tool_use", "server_tool_use", "mcp_tool_use":
		appendTokenString(segments, content.Get("id").String())
		appendTokenString(segments, content.Get("name").String())
		appendTokenJSON(segments, content.Get("input"))
	case "tool_result", "mcp_tool_result", "web_search_tool_result", "web_fetch_tool_result",
		"code_execution_tool_result", "bash_code_execution_tool_result", "text_editor_code_execution_tool_result":
		appendTokenString(segments, content.Get("tool_use_id").String())
		appendTokenString(segments, content.Get("tool_call_id").String())
		collectContentTokenSegments(content.Get("content"), segments)
	case "web_search_result", "search_result":
		appendTokenString(segments, content.Get("source").String())
		appendTokenString(segments, content.Get("title").String())
		appendTokenString(segments, content.Get("url").String())
		appendTokenString(segments, content.Get("page_age").String())
		collectContentTokenSegments(content.Get("content"), segments)
	case "web_fetch_result":
		appendTokenString(segments, content.Get("url").String())
		appendTokenString(segments, content.Get("retrieved_at").String())
		collectContentTokenSegments(content.Get("content"), segments)
	case "code_execution_result", "bash_code_execution_result", "text_editor_code_execution_result":
		appendTokenString(segments, content.Get("stdout").String())
		appendTokenString(segments, content.Get("stderr").String())
		appendTokenString(segments, content.Get("return_code").String())
		collectContentTokenSegments(content.Get("content"), segments)
		collectContentTokenSegments(content.Get("output"), segments)
	case "tool_reference":
		appendTokenString(segments, content.Get("tool_name").String())
	case "image", "input_audio", "audio", "video", "redacted_thinking":
		return
	case "":
		appendTokenJSON(segments, content)
	default:
		appendTokenString(segments, content.Get("text").String())
	}
}

func collectToolTokenSegments(tools gjson.Result, segments *[]string) {
	if !tools.IsArray() {
		return
	}
	tools.ForEach(func(_, tool gjson.Result) bool {
		appendTokenString(segments, tool.Get("type").String())
		appendTokenString(segments, tool.Get("name").String())
		appendTokenString(segments, tool.Get("description").String())
		appendTokenJSON(segments, tool.Get("input_schema"))
		return true
	})
}

func collectToolChoiceTokenSegments(toolChoice gjson.Result, segments *[]string) {
	if !toolChoice.Exists() {
		return
	}
	if toolChoice.Type == gjson.String {
		appendTokenString(segments, toolChoice.String())
		return
	}
	appendTokenString(segments, toolChoice.Get("type").String())
	appendTokenString(segments, toolChoice.Get("name").String())
}

func appendTokenString(segments *[]string, value string) {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		*segments = append(*segments, trimmed)
	}
}

func appendTokenJSON(segments *[]string, value gjson.Result) {
	if !value.Exists() {
		return
	}
	if value.Type == gjson.String {
		appendTokenString(segments, value.String())
		return
	}
	raw := strings.TrimSpace(value.Raw)
	if raw == "" {
		return
	}
	var compact bytes.Buffer
	if errCompact := json.Compact(&compact, []byte(raw)); errCompact == nil {
		appendTokenString(segments, compact.String())
		return
	}
	appendTokenString(segments, raw)
}
