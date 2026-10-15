//go:build !windows

package integration

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
)

type liveChatAttachmentImage struct {
	SHA256 string
	Detail string
}

type liveChatAttachmentToolCall struct {
	ID        string
	Name      string
	Arguments map[string]any
}

type liveAttachmentCapturedToolCall struct {
	ItemID    string
	CallID    string
	Name      string
	Arguments map[string]any
}

type liveChatAttachmentToolResult struct {
	ID       string
	Content  string
	Images   []liveChatAttachmentImage
	HasImage bool
	Failed   bool
}

type liveAttachmentChatRequestEvidence struct {
	Model       string
	Effort      string
	Images      []liveChatAttachmentImage
	ToolCalls   []liveChatAttachmentToolCall
	ToolResults []liveChatAttachmentToolResult
}

type liveChatCompletionEvidence struct {
	Model        string
	ResponseID   string
	Text         string
	FinishReason string
	PromptTokens int64
	OutputTokens int64
	TotalTokens  int64
	ToolCalls    []liveChatAttachmentToolCall
}

type liveChatToolCallBuilder struct {
	id        string
	typeName  string
	name      strings.Builder
	arguments strings.Builder
}

func liveAttachmentOriginalResponsesToolCalls(captures []liveAttachmentIngressCapture) map[string]liveAttachmentCapturedToolCall {
	calls := make(map[string]liveAttachmentCapturedToolCall)
	for _, capture := range captures {
		requestURL, err := url.Parse(capture.Request.URL)
		if err != nil || requestURL.Path != "/v1/responses" || !capture.Dispatched || !capture.BodyComplete || capture.CaptureTruncated || capture.Response.Status < 200 || capture.Response.Status >= 300 {
			continue
		}
		for _, event := range attachmentStreamObjects(capture.Response.Body) {
			if event["type"] != "response.output_item.done" {
				continue
			}
			item, ok := event["item"].(map[string]any)
			if !ok || item["type"] != "function_call" {
				continue
			}
			itemID := attachmentString(item, "id")
			callID := attachmentString(item, "call_id")
			name := attachmentString(item, "name")
			argumentsText := attachmentString(item, "arguments")
			var arguments map[string]any
			if itemID == "" || callID == "" || name == "" || argumentsText == "" || json.Unmarshal([]byte(argumentsText), &arguments) != nil || arguments == nil {
				return nil
			}
			call := liveAttachmentCapturedToolCall{ItemID: itemID, CallID: callID, Name: name, Arguments: arguments}
			if previous, exists := calls[callID]; exists && (previous.ItemID != itemID || previous.Name != name || liveAttachmentArgumentSHA256(previous.Arguments) != liveAttachmentArgumentSHA256(arguments)) {
				return nil
			}
			calls[callID] = call
		}
	}
	return calls
}

func liveAttachmentChatHistoryCorrelations(request liveAttachmentChatRequestEvidence, originalCalls, messagesCalls map[string]liveAttachmentCapturedToolCall) (map[string]string, bool) {
	if len(request.ToolCalls) != 2 || len(request.ToolResults) != 2 || len(originalCalls) != 2 || len(messagesCalls) != 2 {
		return nil, false
	}
	acceptedNames := map[string]bool{"exec_command": false, "view_image": false}
	carrierNames := make(map[string]string, len(request.ToolCalls))
	seenCallIDs := make(map[string]bool, len(request.ToolCalls))
	for _, call := range request.ToolCalls {
		itemID, callID, encoded := translate.DecodeClaudeToolIDs(call.ID)
		original, originalOK := originalCalls[callID]
		_, nameAllowed := acceptedNames[call.Name]
		messageCall, messageOK := messagesCalls[callID]
		if !encoded || !originalOK || !messageOK || itemID != original.ItemID || callID != original.CallID || itemID != "fc_"+callID || messageCall.ItemID != original.ItemID || messageCall.CallID != callID || call.Name != original.Name || call.Name != messageCall.Name || !nameAllowed || liveAttachmentArgumentSHA256(call.Arguments) == "" || liveAttachmentArgumentSHA256(call.Arguments) != liveAttachmentArgumentSHA256(original.Arguments) || liveAttachmentArgumentSHA256(call.Arguments) != liveAttachmentArgumentSHA256(messageCall.Arguments) || seenCallIDs[callID] {
			return nil, false
		}
		seenCallIDs[callID] = true
		acceptedNames[call.Name] = true
		carrierNames[call.ID] = call.Name
	}
	if !acceptedNames["exec_command"] || !acceptedNames["view_image"] {
		return nil, false
	}
	seenResults := make(map[string]bool, len(request.ToolResults))
	for _, result := range request.ToolResults {
		if carrierNames[result.ID] == "" || seenResults[result.ID] {
			return nil, false
		}
		seenResults[result.ID] = true
	}
	if len(seenResults) != len(carrierNames) {
		return nil, false
	}
	return carrierNames, true
}

func liveFreshCatalogAdvertisesEndpoint(body []byte, model, endpoint string) bool {
	var catalog struct {
		Object string `json:"object"`
		Data   []struct {
			ID                 string `json:"id"`
			ModelPickerEnabled bool   `json:"model_picker_enabled"`
			Policy             struct {
				State string `json:"state"`
			} `json:"policy"`
			SupportedEndpoints []string `json:"supported_endpoints"`
		} `json:"data"`
	}
	if model == "" || endpoint == "" || json.Unmarshal(body, &catalog) != nil || catalog.Object != "list" {
		return false
	}
	matches := 0
	for _, row := range catalog.Data {
		if row.ID != model {
			continue
		}
		matches++
		if !row.ModelPickerEnabled || row.Policy.State != "enabled" || !slicesContain(row.SupportedEndpoints, endpoint) {
			return false
		}
	}
	return matches == 1
}

func liveCapturedCatalogAdvertisesEndpoint(gate *liveServerToolGate, model, endpoint string) bool {
	if gate == nil || model == "" || endpoint == "" {
		return false
	}
	entries, err := os.ReadDir(gate.directory)
	if err != nil {
		return false
	}
	var body []byte
	var origin string
	lastStatus := 0
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "gate-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		encoded, err := os.ReadFile(filepath.Join(gate.directory, entry.Name()))
		if err != nil {
			return false
		}
		var capture liveServerToolCapture
		if json.Unmarshal(encoded, &capture) != nil {
			return false
		}
		if capture.Category != "catalog" {
			continue
		}
		if !capture.Dispatched || capture.PublicRequest.Method != "GET" || capture.OriginalPublicOrigin == "" || capture.PublicRequest.URL != capture.OriginalPublicOrigin+"/models" || origin != "" && origin != capture.OriginalPublicOrigin {
			return false
		}
		origin = capture.OriginalPublicOrigin
		lastStatus = capture.Response.Status
		if lastStatus == 200 {
			body = []byte(capture.Response.Body)
		} else if lastStatus != 401 {
			return false
		}
	}
	return lastStatus == 200 && liveFreshCatalogAdvertisesEndpoint(body, model, endpoint)
}

func slicesContain(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func liveAttachmentChatRequest(body []byte, candidate liveAttachmentCase) (liveAttachmentChatRequestEvidence, bool) {
	var request map[string]any
	if json.Unmarshal(body, &request) != nil || request["model"] != candidate.model || request["reasoning_effort"] != "medium" || request["stream"] != true {
		return liveAttachmentChatRequestEvidence{}, false
	}
	messages, ok := request["messages"].([]any)
	if !ok || len(messages) == 0 {
		return liveAttachmentChatRequestEvidence{}, false
	}
	evidence := liveAttachmentChatRequestEvidence{Model: candidate.model, Effort: "medium"}
	knownCalls := make(map[string]bool)
	toolCallNames := make(map[string]string)
	seenResults := make(map[string]bool)
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			return liveAttachmentChatRequestEvidence{}, false
		}
		role := attachmentString(message, "role")
		if role != "system" && role != "developer" && role != "user" && role != "assistant" && role != "tool" {
			return liveAttachmentChatRequestEvidence{}, false
		}
		if role == "assistant" {
			calls, ok := message["tool_calls"].([]any)
			if !ok {
				continue
			}
			for _, rawCall := range calls {
				call, ok := rawCall.(map[string]any)
				function, functionOK := call["function"].(map[string]any)
				id, name := attachmentString(call, "id"), attachmentString(function, "name")
				argumentsText := attachmentString(function, "arguments")
				var arguments map[string]any
				if !ok || !functionOK || id == "" || name == "" || call["type"] != "function" || argumentsText == "" || json.Unmarshal([]byte(argumentsText), &arguments) != nil || arguments == nil || knownCalls[id] {
					return liveAttachmentChatRequestEvidence{}, false
				}
				knownCalls[id] = true
				toolCallNames[id] = name
				evidence.ToolCalls = append(evidence.ToolCalls, liveChatAttachmentToolCall{ID: id, Name: name, Arguments: arguments})
			}
		}
		if role == "tool" {
			id := attachmentString(message, "tool_call_id")
			content, contentOK := liveChatToolContent(message["content"])
			if id == "" || !knownCalls[id] || seenResults[id] || !contentOK {
				return liveAttachmentChatRequestEvidence{}, false
			}
			seenResults[id] = true
			result := liveChatAttachmentToolResult{ID: id, Content: content, Failed: message["is_error"] == true}
			images, validImages := liveChatAttachmentImages(message["content"])
			if !validImages {
				return liveAttachmentChatRequestEvidence{}, false
			}
			result.Images = images
			result.HasImage = len(result.Images) > 0
			evidence.ToolResults = append(evidence.ToolResults, result)
		}
		images, valid := liveChatAttachmentImages(message["content"])
		if !valid {
			return liveAttachmentChatRequestEvidence{}, false
		}
		evidence.Images = append(evidence.Images, images...)
	}
	if candidate.client == "codex" && candidate.media == "pdf" {
		viewImageResult := false
		for _, result := range evidence.ToolResults {
			if toolCallNames[result.ID] != "view_image" || !result.HasImage {
				continue
			}
			viewImageResult = true
			for _, image := range result.Images {
				if image.Detail != candidate.imageDetail {
					return liveAttachmentChatRequestEvidence{}, false
				}
			}
		}
		if !viewImageResult {
			return liveAttachmentChatRequestEvidence{}, false
		}
	}
	if candidate.client == "codex" && candidate.media == "png" {
		fixture, err := os.ReadFile(candidate.fixture)
		if err != nil || len(evidence.Images) == 0 {
			return liveAttachmentChatRequestEvidence{}, false
		}
		digest := sha256.Sum256(fixture)
		want := hex.EncodeToString(digest[:])
		for _, image := range evidence.Images {
			if image.SHA256 != want || image.Detail != candidate.imageDetail {
				return liveAttachmentChatRequestEvidence{}, false
			}
		}
	}
	nativeFile := false
	walkAttachmentJSON(request, func(item map[string]any) {
		nativeFile = nativeFile || item["type"] == "input_file"
	})
	if nativeFile || strings.Contains(string(body), "data:application/pdf;base64,") {
		return liveAttachmentChatRequestEvidence{}, false
	}
	return evidence, true
}

func liveAttachmentMessagesRequest(body []byte, candidate liveAttachmentCase) bool {
	var request map[string]any
	if json.Unmarshal(body, &request) != nil || request["model"] != candidate.model || request["stream"] != true || !filepath.IsAbs(candidate.fixture) {
		return false
	}
	output, _ := request["output_config"].(map[string]any)
	if output["effort"] != "medium" {
		return false
	}
	messages, ok := request["messages"].([]any)
	if !ok || len(messages) == 0 {
		return false
	}
	userText, userMessage := liveAttachmentMessagesText(messages[0])
	if !userMessage || !liveAttachmentMessagesSourcePrompt(userText, candidate) {
		return false
	}
	containsMedia := false
	walkAttachmentJSON(messages[0], func(item map[string]any) {
		typeName := attachmentString(item, "type")
		containsMedia = containsMedia || typeName == "image" || typeName == "image_url" || typeName == "input_image" || typeName == "input_file" || typeName == "document"
	})
	if containsMedia {
		return false
	}
	tools, ok := request["tools"].([]any)
	if !ok {
		return false
	}
	hasExec, hasViewImage := false, false
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		switch attachmentString(tool, "name") {
		case "exec_command":
			hasExec = true
		case "view_image":
			hasViewImage = true
		}
	}
	if !hasExec || !hasViewImage || strings.Contains(string(body), "data:application/pdf;base64,") {
		return false
	}
	if len(messages) == 1 {
		return attachmentString(messages[0].(map[string]any), "role") == "user"
	}
	return liveAttachmentPDFRendererContinuation(messages, candidate)
}

func liveAttachmentMessagesText(value any) (string, bool) {
	message, ok := value.(map[string]any)
	if !ok || attachmentString(message, "role") != "user" {
		return "", false
	}
	var text strings.Builder
	switch content := message["content"].(type) {
	case string:
		text.WriteString(content)
	case []any:
		for _, rawPart := range content {
			part, ok := rawPart.(map[string]any)
			if !ok || part["type"] != "text" {
				return "", false
			}
			text.WriteString(attachmentString(part, "text"))
		}
	default:
		return "", false
	}
	return text.String(), true
}

func liveAttachmentMessagesSourcePrompt(text string, candidate liveAttachmentCase) bool {
	return candidate.fixture != "" && strings.Contains(text, candidate.fixture) && strings.Contains(text, "pdftoppm") && strings.Contains(text, "view_image")
}

func liveAttachmentPDFRendererContinuation(messages []any, candidate liveAttachmentCase) bool {
	if len(messages) != 3 {
		return false
	}
	userText, ok := liveAttachmentMessagesText(messages[0])
	if !ok || !liveAttachmentMessagesSourcePrompt(userText, candidate) {
		return false
	}
	assistant, ok := messages[1].(map[string]any)
	blocks, blocksOK := assistant["content"].([]any)
	if !ok || attachmentString(assistant, "role") != "assistant" || !blocksOK {
		return false
	}
	var callID, command, workdir string
	assistantTextCount := 0
	for _, raw := range blocks {
		block, blockOK := raw.(map[string]any)
		if !blockOK {
			return false
		}
		switch attachmentString(block, "type") {
		case "thinking":
			if attachmentString(block, "thinking") == "" {
				return false
			}
		case "text":
			assistantTextCount++
			text := strings.ToLower(attachmentString(block, "text"))
			if assistantTextCount > 1 || !strings.Contains(text, "render") || !strings.Contains(text, "pdftoppm") {
				return false
			}
		case "tool_use":
			if attachmentString(block, "name") != "exec_command" || callID != "" {
				return false
			}
			input, inputOK := block["input"].(map[string]any)
			if !inputOK {
				return false
			}
			callID = attachmentString(block, "id")
			command = attachmentString(input, "cmd")
			workdir = attachmentString(input, "workdir")
		default:
			return false
		}
	}
	if callID == "" || workdir != filepath.Dir(candidate.fixture) || !liveAttachmentPDFRenderCommand(command, candidate.fixture) {
		return false
	}
	resultMessage, ok := messages[2].(map[string]any)
	resultBlocks, blocksOK := resultMessage["content"].([]any)
	if !ok || attachmentString(resultMessage, "role") != "user" || !blocksOK || len(resultBlocks) != 1 {
		return false
	}
	result, ok := resultBlocks[0].(map[string]any)
	if !ok || result["type"] != "tool_result" || result["is_error"] == true || attachmentString(result, "tool_use_id") != callID {
		return false
	}
	output, ok := result["content"].(string)
	return ok && liveAttachmentPDFRenderOutput(output)
}

func liveAttachmentPDFRenderCommand(command, fixture string) bool {
	parts := strings.Fields(command)
	if len(parts) != 7 && len(parts) != 8 {
		return false
	}
	return parts[0] == "pdftoppm" && parts[1] == "-png" && parts[2] == fixture && parts[3] == "page" && parts[4] == "&&" && parts[5] == "ls" && parts[6] == "-la" && (len(parts) == 7 || parts[7] == "page*")
}

func liveAttachmentPDFRenderOutput(output string) bool {
	if !strings.Contains(output, "\nOutput:\n") {
		return false
	}
	hasExplicitSuccess := strings.Contains(output, "\nProcess exited with code 0\n")
	if !hasExplicitSuccess && (!strings.HasPrefix(output, "Chunk ID: ") || !strings.Contains(output, "\nWall time: ")) {
		return false
	}
	hasRenderedPage := false
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "Process exited with code ") && line != "Process exited with code 0" {
			return false
		}
		if strings.HasSuffix(strings.TrimSpace(line), "page-1.png") && strings.HasPrefix(strings.TrimSpace(line), "-rw") {
			hasRenderedPage = true
		}
	}
	return hasRenderedPage
}

func liveChatToolContent(value any) (string, bool) {
	switch content := value.(type) {
	case string:
		return content, strings.TrimSpace(content) != ""
	case []any:
		var text strings.Builder
		for _, raw := range content {
			part, ok := raw.(map[string]any)
			if !ok {
				return "", false
			}
			if part["type"] == "text" {
				text.WriteString(attachmentString(part, "text"))
			} else if part["type"] != "image_url" {
				return "", false
			}
		}
		return text.String(), len(content) > 0
	default:
		return "", false
	}
}

func liveChatAttachmentImages(value any) ([]liveChatAttachmentImage, bool) {
	var images []liveChatAttachmentImage
	valid := true
	walkAttachmentJSON(value, func(item map[string]any) {
		if item["type"] != "image_url" {
			return
		}
		imageURL, ok := item["image_url"].(map[string]any)
		if !ok {
			valid = false
			return
		}
		dataURL := attachmentString(imageURL, "url")
		if !strings.HasPrefix(dataURL, "data:image/png;base64,") {
			valid = false
			return
		}
		data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(dataURL, "data:image/png;base64,"))
		if err != nil || !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
			valid = false
			return
		}
		detail := attachmentString(imageURL, "detail")
		if detail == "" {
			detail = attachmentString(item, "detail")
		}
		if detail == "" {
			detail = "<omitted>"
		}
		if detail != "high" && detail != "low" && detail != "auto" && detail != "<omitted>" {
			valid = false
			return
		}
		digest := sha256.Sum256(data)
		images = append(images, liveChatAttachmentImage{SHA256: hex.EncodeToString(digest[:]), Detail: detail})
	})
	return images, valid
}

func liveAttachmentImageDetail(item map[string]any) string {
	detail := attachmentString(item, "detail")
	if imageURL, ok := item["image_url"].(map[string]any); ok {
		if nested := attachmentString(imageURL, "detail"); nested != "" {
			detail = nested
		}
	}
	if detail == "" {
		return "<omitted>"
	}
	return detail
}

func liveChatAttachmentToolResultSucceeded(result liveChatAttachmentToolResult, name string, candidate liveAttachmentCase) bool {
	if result.ID == "" || result.Failed || strings.TrimSpace(result.Content) == "" && !result.HasImage {
		return false
	}
	switch name {
	case "exec_command", "shell_command":
		return liveAttachmentCommandOutputSuccess(map[string]any{"content": result.Content})
	case "view_image":
		return candidate.media == "pdf" && result.HasImage
	default:
		return true
	}
}

func liveAttachmentMessagesToolCallTerminal(body []byte, model string) bool {
	if !liveCompleteMessagesTerminal(body, model) {
		return false
	}
	finishReason := ""
	validToolCall := false
	for _, event := range attachmentStreamObjects(string(body)) {
		if event["type"] == "message_delta" {
			delta, _ := event["delta"].(map[string]any)
			finishReason = attachmentString(delta, "stop_reason")
		}
		if event["type"] != "assembled_tool_use" {
			continue
		}
		item, _ := event["item"].(map[string]any)
		input, _ := item["input"].(map[string]any)
		name := attachmentString(item, "name")
		validToolCall = validToolCall || attachmentString(item, "id") != "" && (name == "exec_command" || name == "view_image") && len(input) > 0
	}
	return finishReason == "tool_use" && validToolCall
}

func liveAttachmentPublicPath(requestURL string) string {
	parsed, err := url.Parse(requestURL)
	if err != nil {
		return ""
	}
	return parsed.Path
}

func liveAttachmentCapturedResponseCompleted(body string, candidate liveAttachmentCase, publicPath string) bool {
	if publicPath == "/chat/completions" {
		stream, valid := liveChatCompletionStream([]byte(body), candidate.model)
		return valid && stream.FinishReason == "stop" && strings.TrimSpace(stream.Text) != ""
	}
	if candidate.client == "codex" && candidate.media == "pdf" && publicPath == "/v1/messages" {
		return false
	}
	return attachmentResponseCompleted(body, candidate)
}

func liveChatCompletionStream(body []byte, expectedModel string) (liveChatCompletionEvidence, bool) {
	var evidence liveChatCompletionEvidence
	if expectedModel == "" || len(body) == 0 {
		return evidence, false
	}
	normalized := bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
	if bytes.Contains(normalized, []byte("\r")) || !bytes.HasSuffix(normalized, []byte("\n\n")) {
		return evidence, false
	}
	textBody := strings.TrimSuffix(string(normalized), "\n\n")
	frames := strings.Split(textBody, "\n\n")
	builders := make(map[int]*liveChatToolCallBuilder)
	seenIDs := make(map[string]bool)
	done, finishSeen, usageSeen := false, false, false
	var usagePrompt, usageCompletion, usageTotal int64
	for frameIndex, frame := range frames {
		if strings.TrimSpace(frame) == "" || done {
			return evidence, false
		}
		var dataLines []string
		for _, line := range strings.Split(frame, "\n") {
			switch {
			case strings.HasPrefix(line, "data:"):
				dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			case strings.HasPrefix(line, "event:") || strings.HasPrefix(line, "id:") || strings.HasPrefix(line, "retry:") || strings.HasPrefix(line, ":"):
			default:
				return evidence, false
			}
		}
		if len(dataLines) == 0 {
			continue
		}
		data := strings.Join(dataLines, "\n")
		if data == "[DONE]" {
			if frameIndex != len(frames)-1 || !finishSeen || !usageSeen {
				return evidence, false
			}
			done = true
			continue
		}
		var chunk map[string]any
		if json.Unmarshal([]byte(data), &chunk) != nil || chunk["error"] != nil {
			return evidence, false
		}
		if object, exists := chunk["object"]; exists && object != "chat.completion.chunk" {
			return evidence, false
		}
		responseID := attachmentString(chunk, "id")
		if responseID == "" || evidence.ResponseID != "" && evidence.ResponseID != responseID {
			return evidence, false
		}
		evidence.ResponseID = responseID
		model := attachmentString(chunk, "model")
		if !liveMatrixResponseModelMatches(expectedModel, model) || evidence.Model != "" && evidence.Model != model {
			return evidence, false
		}
		evidence.Model = model
		choices, ok := chunk["choices"].([]any)
		if !ok {
			return evidence, false
		}
		if rawUsage, exists := chunk["usage"]; exists && rawUsage != nil {
			usage, ok := rawUsage.(map[string]any)
			if !ok {
				return evidence, false
			}
			prompt, promptOK := liveChatTokenValue(usage["prompt_tokens"])
			completion, completionOK := liveChatTokenValue(usage["completion_tokens"])
			total, totalOK := liveChatTokenValue(usage["total_tokens"])
			if !promptOK || !completionOK || !totalOK || completion == 0 || total != prompt+completion || finishSeen && usageSeen && (prompt != usagePrompt || completion != usageCompletion || total != usageTotal) {
				return evidence, false
			}
			usageSeen, usagePrompt, usageCompletion, usageTotal = true, prompt, completion, total
		}
		if len(choices) == 0 {
			if !finishSeen || !usageSeen {
				return evidence, false
			}
			continue
		}
		if finishSeen || len(choices) != 1 {
			return evidence, false
		}
		choice, ok := choices[0].(map[string]any)
		if !ok || choice["index"] != float64(0) {
			return evidence, false
		}
		delta, ok := choice["delta"].(map[string]any)
		if !ok {
			return evidence, false
		}
		if role := attachmentString(delta, "role"); role != "" && role != "assistant" {
			return evidence, false
		}
		if content, exists := delta["content"]; exists && content != nil {
			text, ok := content.(string)
			if !ok {
				return evidence, false
			}
			evidence.Text += text
		}
		if rawCalls, exists := delta["tool_calls"]; exists && rawCalls != nil {
			calls, ok := rawCalls.([]any)
			if !ok || len(calls) == 0 {
				return evidence, false
			}
			for _, rawCall := range calls {
				call, ok := rawCall.(map[string]any)
				index, indexOK := liveChatToolIndex(call["index"])
				if !ok || !indexOK {
					return evidence, false
				}
				builder := builders[index]
				if builder == nil {
					builder = &liveChatToolCallBuilder{}
					builders[index] = builder
				}
				if value := attachmentString(call, "id"); value != "" {
					if builder.id != "" && builder.id != value || seenIDs[value] && builder.id != value {
						return evidence, false
					}
					builder.id = value
					seenIDs[value] = true
				}
				if value := attachmentString(call, "type"); value != "" {
					if value != "function" || builder.typeName != "" && builder.typeName != value {
						return evidence, false
					}
					builder.typeName = value
				}
				if rawFunction, exists := call["function"]; exists {
					function, ok := rawFunction.(map[string]any)
					if !ok {
						return evidence, false
					}
					if value, ok := function["name"].(string); ok {
						builder.name.WriteString(value)
					} else if _, exists := function["name"]; exists {
						return evidence, false
					}
					if value, ok := function["arguments"].(string); ok {
						builder.arguments.WriteString(value)
					} else if _, exists := function["arguments"]; exists {
						return evidence, false
					}
				}
			}
		}
		if rawFinish, exists := choice["finish_reason"]; exists && rawFinish != nil {
			finish, ok := rawFinish.(string)
			if !ok || finish != "stop" && finish != "tool_calls" {
				return evidence, false
			}
			finishSeen = true
			evidence.FinishReason = finish
		}
	}
	if !done || evidence.Model == "" || !finishSeen || !usageSeen {
		return evidence, false
	}
	evidence.PromptTokens, evidence.OutputTokens, evidence.TotalTokens = usagePrompt, usageCompletion, usageTotal
	indices := make([]int, 0, len(builders))
	for index := range builders {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for expectedIndex, index := range indices {
		builder := builders[index]
		if index != expectedIndex || builder.id == "" || builder.typeName != "function" || builder.name.Len() == 0 || builder.arguments.Len() == 0 {
			return evidence, false
		}
		var arguments map[string]any
		if json.Unmarshal([]byte(builder.arguments.String()), &arguments) != nil || arguments == nil {
			return evidence, false
		}
		evidence.ToolCalls = append(evidence.ToolCalls, liveChatAttachmentToolCall{ID: builder.id, Name: builder.name.String(), Arguments: arguments})
	}
	if evidence.FinishReason == "tool_calls" && len(evidence.ToolCalls) == 0 || evidence.FinishReason == "stop" && len(evidence.ToolCalls) != 0 {
		return evidence, false
	}
	return evidence, true
}

func liveChatTokenValue(value any) (int64, bool) {
	number, ok := value.(float64)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 || math.Trunc(number) != number || number > math.MaxInt64 {
		return 0, false
	}
	return int64(number), true
}

func liveChatToolIndex(value any) (int, bool) {
	number, ok := value.(float64)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 || math.Trunc(number) != number || number > 64 {
		return 0, false
	}
	return int(number), true
}

func TestLiveChatAttachmentCatalogSelectionRequiresExactAdvertisement(t *testing.T) {
	catalog := []byte(`{"object":"list","data":[{"id":"claude-haiku-5.5","model_picker_enabled":true,"policy":{"state":"enabled"},"supported_endpoints":["/v1/messages","/chat/completions"]}]}`)
	if !liveFreshCatalogAdvertisesEndpoint(catalog, "claude-haiku-5.5", "/chat/completions") || liveFreshCatalogAdvertisesEndpoint(catalog, "claude-haiku-5.5", "/responses") {
		t.Fatal("fresh catalog did not constrain the exact model to its advertised Chat endpoint")
	}
	for _, invalid := range []string{
		strings.Replace(string(catalog), `"model_picker_enabled":true`, `"model_picker_enabled":false`, 1),
		strings.Replace(string(catalog), `"state":"enabled"`, `"state":"restricted"`, 1),
		strings.Replace(string(catalog), `,"/chat/completions"`, "", 1),
		strings.Replace(string(catalog), `"id":"claude-haiku-5.5"`, `"id":"other-model"`, 1),
		`{"object":"list","data":[{"id":"claude-haiku-5.5","model_picker_enabled":true,"policy":{"state":"enabled"},"supported_endpoints":["/chat/completions"]},{"id":"claude-haiku-5.5","model_picker_enabled":true,"policy":{"state":"enabled"},"supported_endpoints":["/chat/completions"]}]}`,
	} {
		if liveFreshCatalogAdvertisesEndpoint([]byte(invalid), "claude-haiku-5.5", "/chat/completions") {
			t.Fatal("unadvertised, disabled, or duplicate model endpoint passed catalog validation")
		}
	}
}

func TestLiveChatAttachmentRequestPreservesBytesDetailAndToolIDs(t *testing.T) {
	candidate := liveAttachmentCases()[2]
	image, err := os.ReadFile(candidate.fixture)
	if err != nil {
		t.Fatal("pinned PNG fixture unavailable")
	}
	request := map[string]any{
		"model": "claude-haiku-5.5", "stream": true, "reasoning_effort": "medium",
		"messages": []any{
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "Inspect the attached image."}, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(image), "detail": "high"}}}},
			map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "call-exact", "type": "function", "function": map[string]any{"name": "inspect", "arguments": `{"path":"image.png"}`}}}},
			map[string]any{"role": "tool", "tool_call_id": "call-exact", "content": "inspection complete"},
		},
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	evidence, ok := liveAttachmentChatRequest(encoded, candidate)
	if !ok || evidence.Model != candidate.model || evidence.Effort != "medium" || len(evidence.Images) != 1 || evidence.Images[0].Detail != "high" || len(evidence.ToolCalls) != 1 || evidence.ToolCalls[0].ID != "call-exact" || len(evidence.ToolResults) != 1 || evidence.ToolResults[0].ID != evidence.ToolCalls[0].ID {
		t.Fatal("valid original image bytes, high detail, or paired Chat tool result were rejected")
	}
	if !liveChatAttachmentToolResultSucceeded(evidence.ToolResults[0], evidence.ToolCalls[0].Name, candidate) {
		t.Fatal("non-empty successful Chat role=tool result was rejected")
	}
	for name, alter := range map[string]func(map[string]any){
		"wrong-model":  func(value map[string]any) { value["model"] = "claude-haiku-5.5-low" },
		"wrong-effort": func(value map[string]any) { value["reasoning_effort"] = "low" },
		"low-detail": func(value map[string]any) {
			value["messages"].([]any)[0].(map[string]any)["content"].([]any)[1].(map[string]any)["image_url"].(map[string]any)["detail"] = "low"
		},
		"wrong-tool-id": func(value map[string]any) {
			value["messages"].([]any)[2].(map[string]any)["tool_call_id"] = "call-other"
		},
		"wrong-image-bytes": func(value map[string]any) {
			value["messages"].([]any)[0].(map[string]any)["content"].([]any)[1].(map[string]any)["image_url"].(map[string]any)["url"] = "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("different"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			var value map[string]any
			if json.Unmarshal(changed, &value) != nil {
				t.Fatal("test request did not decode")
			}
			alter(value)
			changed, err = json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := liveAttachmentChatRequest(changed, candidate); ok {
				t.Fatal("wrong model, effort, image bytes/detail, or role=tool ID was accepted")
			}
		})
	}
}

func TestLiveChatAttachmentPDFContinuationRequiresHighViewImageResult(t *testing.T) {
	candidate := liveAttachmentCases()[3]
	image, err := os.ReadFile(attachmentPNG)
	if err != nil {
		t.Fatal("pinned PNG structural fixture unavailable")
	}
	request := map[string]any{
		"model": candidate.model, "stream": true, "reasoning_effort": "medium",
		"messages": []any{
			map[string]any{"role": "user", "content": "Inspect the rendered first page of the staged PDF."},
			map[string]any{"role": "assistant", "tool_calls": []any{
				map[string]any{"id": "call-view-image", "type": "function", "function": map[string]any{"name": "view_image", "arguments": `{"path":"page-1.png"}`}},
			}},
			map[string]any{"role": "tool", "tool_call_id": "call-view-image", "content": []any{
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(image), "detail": "high"}},
			}},
		},
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	evidence, ok := liveAttachmentChatRequest(encoded, candidate)
	if !ok || len(evidence.Images) != 1 || evidence.Images[0].Detail != "high" || len(evidence.ToolResults) != 1 || !liveChatAttachmentToolResultSucceeded(evidence.ToolResults[0], "view_image", candidate) {
		t.Fatal("high-detail Chat view_image tool result was not recognized")
	}
	if !liveAttachmentPreparedPublicRequestForFixture("X/"+candidate.cell, "/chat/completions", encoded, candidate.fixture) {
		t.Fatal("same-model Chat continuation after the PDF view_image result was rejected")
	}
	for name, alter := range map[string]func(map[string]any){
		"low-detail": func(value map[string]any) {
			value["messages"].([]any)[2].(map[string]any)["content"].([]any)[0].(map[string]any)["image_url"].(map[string]any)["detail"] = "low"
		},
		"wrong-tool-id": func(value map[string]any) {
			value["messages"].([]any)[2].(map[string]any)["tool_call_id"] = "call-other"
		},
		"missing-image": func(value map[string]any) {
			value["messages"].([]any)[2].(map[string]any)["content"] = "no image was returned"
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			var value map[string]any
			if json.Unmarshal(changed, &value) != nil {
				t.Fatal("test request did not decode")
			}
			alter(value)
			changed, err = json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := liveAttachmentChatRequest(changed, candidate); ok {
				t.Fatal("low-detail, unmatched, or missing PDF view_image result was accepted")
			}
		})
	}
}

func TestLiveChatAttachmentStreamReconstructsCallsAndRejectsIncompleteResults(t *testing.T) {
	firstCall := map[string]any{"index": 0, "id": "call-pdf", "type": "function", "function": map[string]any{"name": "exec_command", "arguments": `{"cmd":"pdftoppm -png doc.pdf page`}}
	firstChoice := map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "tool_calls": []any{firstCall}}, "finish_reason": nil}
	first := liveChatTestFrame(t, map[string]any{"object": "chat.completion.chunk", "model": "claude-haiku-5.5", "choices": []any{firstChoice}})
	secondCall := map[string]any{"index": 0, "function": map[string]any{"arguments": `"}`}}
	secondChoice := map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{secondCall}}, "finish_reason": nil}
	second := liveChatTestFrame(t, map[string]any{"object": "chat.completion.chunk", "model": "claude-haiku-5.5", "choices": []any{secondChoice}})
	finished := liveChatTestFrame(t, map[string]any{
		"object": "chat.completion.chunk", "model": "claude-haiku-5.5",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}},
		"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
	})
	usage := liveChatTestFrame(t, map[string]any{
		"object": "chat.completion.chunk", "model": "claude-haiku-5.5", "choices": []any{},
		"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
	})
	callStream := first + second + finished + usage +
		"data: [DONE]\n\n"
	stream, ok := liveChatCompletionStream([]byte(callStream), "claude-haiku-5.5")
	if !ok || stream.FinishReason != "tool_calls" || len(stream.ToolCalls) != 1 || stream.ToolCalls[0].ID != "call-pdf" || stream.ToolCalls[0].Name != "exec_command" || attachmentString(stream.ToolCalls[0].Arguments, "cmd") != "pdftoppm -png doc.pdf page" || stream.TotalTokens != 15 {
		t.Fatal("complete Chat function call stream was not reconstructed with its exact ID and arguments")
	}
	if liveAttachmentCapturedResponseCompleted(callStream, liveAttachmentCases()[3], "/chat/completions") {
		t.Fatal("a completed tool_calls response was mistaken for a final user-facing answer")
	}
	for name, invalid := range map[string]string{
		"truncated":      strings.TrimSuffix(callStream, "data: [DONE]\n\n"),
		"wrong-model":    strings.Replace(callStream, `"model":"claude-haiku-5.5"`, `"model":"claude-haiku-5.5-low"`, 1),
		"missing-usage":  strings.ReplaceAll(callStream, `,"usage":{"completion_tokens":5,"prompt_tokens":10,"total_tokens":15}`, ""),
		"bad-finish":     strings.Replace(callStream, `"finish_reason":"tool_calls"`, `"finish_reason":"length"`, 1),
		"bad-arguments":  strings.Replace(callStream, `"arguments":"\"}"`, `"arguments":"not-json"`, 1),
		"trailing-frame": callStream + "data: [DONE]\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			if invalid == callStream {
				t.Fatal("negative Chat stream fixture did not change the valid response")
			}
			if _, ok := liveChatCompletionStream([]byte(invalid), "claude-haiku-5.5"); ok {
				t.Fatal("truncated, mismatched, or malformed Chat completion was accepted")
			}
		})
	}
	final := liveChatTestFrame(t, map[string]any{"object": "chat.completion.chunk", "model": "claude-haiku-5.5", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "Top red, bottom blue."}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 8, "completion_tokens": 4, "total_tokens": 12}}) +
		"data: [DONE]\n\n"
	if !attachmentResponseCompleted(final, liveAttachmentCases()[2]) {
		t.Fatal("native Chat stop, usage, model, and done terminal was rejected")
	}
	if attachmentResponseCompleted(callStream, liveAttachmentCases()[2]) {
		t.Fatal("tool_calls terminal was mistaken for the final user-facing answer")
	}
}

func liveChatTestFrame(t *testing.T, chunk any) string {
	t.Helper()
	value, ok := chunk.(map[string]any)
	if !ok {
		t.Fatal("test Chat chunk is not an object")
	}
	if value["id"] == nil {
		value["id"] = "chatcmpl-test"
	}
	if value["created"] == nil {
		value["created"] = 1
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return "data: " + string(encoded) + "\n\n"
}

func TestLiveChatAttachmentResponseCaptureRequiresCompleteNativeTerminal(t *testing.T) {
	body := "data: {\"object\":\"chat.completion.chunk\",\"id\":\"chatcmpl-test\",\"model\":\"claude-haiku-5.5\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n" + "data: [DONE]\n\n"
	capture := liveServerToolCapture{PublicRequest: liveHTTPLog{URL: "https://api.githubcopilot.com/chat/completions"}, Response: liveHTTPLog{Status: 200, Body: body}}
	if !liveCaptureStreamProven(capture, "claude-haiku-5.5") {
		t.Fatal("complete native Chat stream was rejected")
	}
	for name, alter := range map[string]func(*liveServerToolCapture){
		"truncated-capture": func(value *liveServerToolCapture) { value.CaptureTruncated = true },
		"wrong-status":      func(value *liveServerToolCapture) { value.Response.Status = 422 },
		"upstream-error":    func(value *liveServerToolCapture) { value.ErrorClass = "upstream_response_read_error" },
		"wrong-path": func(value *liveServerToolCapture) {
			value.PublicRequest.URL = "https://api.githubcopilot.com/responses"
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := capture
			alter(&changed)
			if liveCaptureStreamProven(changed, "claude-haiku-5.5") {
				t.Fatal("truncated or mismatched native Chat capture was accepted")
			}
		})
	}
}

func TestLiveChatAttachmentCapturedCodexPDFMessagesBootstrapIsContextual(t *testing.T) {
	candidate := liveAttachmentCases()[3]
	candidate.fixture = filepath.Join(t.TempDir(), "client-workspace", "document.pdf")
	request := map[string]any{
		"model": candidate.model, "stream": true, "output_config": map[string]any{"effort": "medium"},
		"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "Inspect the PDF at " + candidate.fixture + " using installed pdftoppm -png to render it into the current working directory, then use the built-in view_image tool on the rendered page. Report the printed code and element color."}}}},
		"tools": []any{
			map[string]any{"name": "exec_command", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"cmd": map[string]any{"type": "string"}}}},
			map[string]any{"name": "view_image", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}},
		},
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	gate := &liveServerToolGate{}
	cell := "X-fixture/" + candidate.cell
	gate.setAttachmentFixture(cell, candidate.fixture)
	gate.mu.Lock()
	preparedFixture := gate.attachmentFixtures[cell]
	gate.mu.Unlock()
	if !liveAttachmentMessagesRequest(encoded, candidate) || preparedFixture != candidate.fixture || liveAttachmentPreparedPublicRequest(cell, "/v1/messages", encoded) || !liveAttachmentPreparedPublicRequestForFixture(cell, "/v1/messages", encoded, preparedFixture) {
		t.Fatal("the exact same-model, medium-effort PDF bootstrap Messages request was rejected")
	}
	if liveAttachmentPreparedPublicRequestForFixture(cell, "/responses", encoded, preparedFixture) || liveAttachmentPreparedPublicRequestForFixture(cell, "/chat/completions", encoded, preparedFixture) {
		t.Fatal("the pre-media Messages request was admitted on a different endpoint")
	}
	for name, alter := range map[string]func(map[string]any){
		"wrong-model":  func(value map[string]any) { value["model"] = "claude-haiku-5.5-low" },
		"wrong-effort": func(value map[string]any) { value["output_config"].(map[string]any)["effort"] = "low" },
		"wrong-path": func(value map[string]any) {
			value["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"] = "Inspect another.pdf using pdftoppm -png and use view_image."
		},
		"media-in-bootstrap": func(value map[string]any) {
			value["messages"].([]any)[0].(map[string]any)["content"] = []any{map[string]any{"type": "input_file", "file_id": "file-other"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			var value map[string]any
			if json.Unmarshal(changed, &value) != nil {
				t.Fatal("test request did not decode")
			}
			alter(value)
			changed, err = json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if liveAttachmentPreparedPublicRequestForFixture(cell, "/v1/messages", changed, preparedFixture) {
				t.Fatal("lossy model/effort, different source path, or media bootstrap was accepted")
			}
		})
	}
}

func TestLiveChatAttachmentCapturedCodexPDFMessagesRenderContinuationIsContextual(t *testing.T) {
	candidate := liveAttachmentCases()[3]
	workspace := filepath.Join(t.TempDir(), "client-workspace")
	candidate.fixture = filepath.Join(workspace, "document.pdf")
	request := map[string]any{
		"model": candidate.model, "stream": true, "output_config": map[string]any{"effort": "medium"},
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "<environment_context><cwd>" + workspace + "</cwd></environment_context>"},
				map[string]any{"type": "text", "text": "Inspect the PDF at " + candidate.fixture + " using installed pdftoppm -png to render it into the current working directory, then use the built-in view_image tool on the rendered page."},
			}},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "thinking", "thinking": "Render the staged PDF page."},
				map[string]any{"type": "text", "text": "I'll render the PDF to PNG in the working directory with `pdftoppm`."},
				map[string]any{"type": "tool_use", "id": "call-render", "name": "exec_command", "input": map[string]any{"cmd": "pdftoppm -png " + candidate.fixture + " page && ls -la page*", "workdir": workspace}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "call-render", "content": "Chunk ID: fixture\nWall time: 2.2 seconds\nProcess exited with code 0\nOriginal token count: 425\nOutput:\n\ntotal 32\n-rw-r--r-- 1 fixture staff 10369 Oct 10 12:44 page-1.png\n"},
			}},
		},
		"tools": []any{
			map[string]any{"name": "exec_command", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"cmd": map[string]any{"type": "string"}}}},
			map[string]any{"name": "view_image", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}},
		},
	}
	encoded, err := json.Marshal(request)
	if err != nil || !liveAttachmentMessagesRequest(encoded, candidate) || !liveAttachmentPreparedPublicRequestForFixture("X-fixture/"+candidate.cell, "/v1/messages", encoded, candidate.fixture) {
		t.Fatal("same-model medium-effort PDF renderer continuation with a correlated successful result was rejected")
	}

	for name, alter := range map[string]func(map[string]any){
		"wrong-model": func(value map[string]any) { value["model"] = "claude-haiku-5.5-low" },
		"wrong-effort": func(value map[string]any) {
			value["output_config"].(map[string]any)["effort"] = "low"
		},
		"wrong-source": func(value map[string]any) {
			value["messages"].([]any)[1].(map[string]any)["content"].([]any)[2].(map[string]any)["input"].(map[string]any)["cmd"] = "pdftoppm -png other.pdf page && ls -la"
		},
		"unbounded-list-command": func(value map[string]any) {
			value["messages"].([]any)[1].(map[string]any)["content"].([]any)[2].(map[string]any)["input"].(map[string]any)["cmd"] = "pdftoppm -png " + candidate.fixture + " page && ls -la page* extra"
		},
		"unrelated-assistant-text": func(value map[string]any) {
			value["messages"].([]any)[1].(map[string]any)["content"].([]any)[1].(map[string]any)["text"] = "I will inspect the PDF later."
		},
		"wrong-workdir": func(value map[string]any) {
			value["messages"].([]any)[1].(map[string]any)["content"].([]any)[2].(map[string]any)["input"].(map[string]any)["workdir"] = filepath.Dir(candidate.fixture) + "-other"
		},
		"wrong-tool-result-id": func(value map[string]any) {
			value["messages"].([]any)[2].(map[string]any)["content"].([]any)[0].(map[string]any)["tool_use_id"] = "call-other"
		},
		"unrelated-tool": func(value map[string]any) {
			value["messages"].([]any)[1].(map[string]any)["content"].([]any)[2].(map[string]any)["name"] = "view_image"
		},
		"failed-command": func(value map[string]any) {
			value["messages"].([]any)[2].(map[string]any)["content"].([]any)[0].(map[string]any)["content"] = "Chunk ID: fixture\nProcess exited with code 1\nOutput:\npage-1.png\n"
		},
		"failed-command-after-page": func(value map[string]any) {
			value["messages"].([]any)[2].(map[string]any)["content"].([]any)[0].(map[string]any)["content"] = "Chunk ID: fixture\nWall time: 2.2s\nOutput:\n-rw-r--r-- 1 fixture staff 10369 Oct 10 12:44 page-1.png\nProcess exited with code 1\n"
		},
		"missing-rendered-page": func(value map[string]any) {
			value["messages"].([]any)[2].(map[string]any)["content"].([]any)[0].(map[string]any)["content"] = "Chunk ID: fixture\nProcess exited with code 0\nOutput:\nno rendered file\n"
		},
		"error-result": func(value map[string]any) {
			value["messages"].([]any)[2].(map[string]any)["content"].([]any)[0].(map[string]any)["is_error"] = true
		},
		"extra-turn": func(value map[string]any) {
			value["messages"] = append(value["messages"].([]any), map[string]any{"role": "assistant", "content": "unrelated"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			original, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			var changed map[string]any
			if json.Unmarshal(original, &changed) != nil {
				t.Fatal("captured-shape request did not decode")
			}
			alter(changed)
			body, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			if liveAttachmentPreparedPublicRequestForFixture("X-fixture/"+candidate.cell, "/v1/messages", body, candidate.fixture) {
				t.Fatal("unrelated, failed, uncorrelated, or lossy PDF Messages continuation was admitted")
			}
		})
	}
}

func TestLiveCodexPDFFontconfigCacheUsesWritableWorkspace(t *testing.T) {
	candidate := liveAttachmentCases()[3]
	home := filepath.Join(t.TempDir(), "client-home")
	workspace := filepath.Join(t.TempDir(), "client-workspace")
	temp := filepath.Join(t.TempDir(), "client-tmp")
	environment := liveAttachmentIsolatedEnvironment(candidate, "http://127.0.0.1:1000", home, workspace, temp)
	values := make(map[string]string)
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	wantCache := filepath.Join(workspace, ".cache")
	if values["HOME"] != home || values["CODEX_HOME"] != home || values["TMPDIR"] != temp || values["XDG_CACHE_HOME"] != wantCache || !strings.HasPrefix(filepath.Join(wantCache, "fontconfig"), workspace+string(os.PathSeparator)) {
		t.Fatal("Codex PDF Fontconfig cache escaped the isolated workspace or changed its client home")
	}
	if values["XDG_CACHE_HOME"] == filepath.Join(home, "cache") {
		t.Fatal("Codex PDF Fontconfig cache still points at the read-only client home")
	}
}

func TestAttachmentPacketHistoricalPDFMessagesContinuationReplay(t *testing.T) {
	gateDirectory := os.Getenv("CPA_ATTACHMENT_REPLAY_GATE_DIR")
	caseDirectory := os.Getenv("CPA_ATTACHMENT_REPLAY_CASE_DIR")
	if gateDirectory == "" || caseDirectory == "" {
		t.Skip("set both private body-replay directories to inspect the retained historical PDF request")
	}
	var claim struct {
		StagedFixture         string `json:"staged_fixture"`
		StagedFixtureSHA256   string `json:"staged_fixture_sha256"`
		OriginalFixtureSHA256 string `json:"original_fixture_sha256"`
	}
	claimBody, err := os.ReadFile(filepath.Join(caseDirectory, "claim.json"))
	if err != nil || json.Unmarshal(claimBody, &claim) != nil || claim.StagedFixture == "" || claim.StagedFixtureSHA256 == "" || claim.StagedFixtureSHA256 != claim.OriginalFixtureSHA256 || livePacketFileSHA256(t, claim.StagedFixture) != claim.StagedFixtureSHA256 {
		t.Fatal("retained staged PDF did not preserve the exact original fixture bytes")
	}
	entries, err := os.ReadDir(gateDirectory)
	if err != nil {
		t.Fatal("retained PDF gate captures are unavailable")
	}
	var rejected liveServerToolCapture
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "gate-") {
			continue
		}
		body, readErr := os.ReadFile(filepath.Join(gateDirectory, entry.Name()))
		if readErr != nil {
			t.Fatal("retained PDF gate capture could not be read")
		}
		var capture liveServerToolCapture
		if json.Unmarshal(body, &capture) == nil && capture.ErrorClass == "attachment_request_unprepared" && capture.Request.URL == "/v1/messages" {
			rejected = capture
		}
	}
	if rejected.Request.Body == "" || rejected.Dispatched {
		t.Fatal("retained PDF pre-image Messages rejection was not found")
	}
	candidate := liveAttachmentCases()[3]
	candidate.fixture = claim.StagedFixture
	requestBody := []byte(rejected.Request.Body)
	messagesOK := liveAttachmentMessagesRequest(requestBody, candidate)
	preparedOK := liveAttachmentPreparedPublicRequestForFixture("X/"+candidate.cell, rejected.Request.URL, requestBody, candidate.fixture)
	if !messagesOK || !preparedOK {
		t.Fatalf("captured same-model PDF renderer continuation was rejected (messages=%t prepared=%t)", messagesOK, preparedOK)
	}
	for name, mutate := range map[string]func([]byte) []byte{
		"wrong-model": func(body []byte) []byte {
			return bytes.Replace(body, []byte(`"model":"claude-haiku-5.5"`), []byte(`"model":"claude-haiku-5.5-low"`), 1)
		},
		"wrong-effort": func(body []byte) []byte {
			return bytes.Replace(body, []byte(`"effort":"medium"`), []byte(`"effort":"low"`), 1)
		},
		"wrong-tool-id": func(body []byte) []byte {
			return bytes.Replace(body, []byte(`"tool_use_id":"`+liveCapturedPDFRendererCallID(t, body)+`"`), []byte(`"tool_use_id":"other-call"`), 1)
		},
		"failed-render": func(body []byte) []byte {
			return bytes.Replace(body, []byte(`\nOutput:\n`), []byte(`\nProcess exited with code 1\nOutput:\n`), 1)
		},
		"failed-render-after-page": func(body []byte) []byte {
			return bytes.Replace(body, []byte("page-1.png"), []byte(`page-1.png\nProcess exited with code 1`), 1)
		},
		"wrong-source": func(body []byte) []byte {
			return bytes.Replace(body, []byte(candidate.fixture), []byte(filepath.Join(filepath.Dir(candidate.fixture), "other.pdf")), 1)
		},
		"truncated-result": func(body []byte) []byte {
			return bytes.Replace(body, []byte("page-1.png"), []byte("other.png"), 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := mutate(requestBody)
			if bytes.Equal(changed, requestBody) {
				t.Fatal("captured negative fixture did not change the intended field")
			}
			if liveAttachmentPreparedPublicRequestForFixture("X/"+candidate.cell, rejected.Request.URL, changed, candidate.fixture) {
				t.Fatal("lossy or unproven captured PDF Messages continuation was admitted")
			}
		})
	}
}

func liveCapturedPDFRendererCallID(t *testing.T, body []byte) string {
	t.Helper()
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatalf("captured PDF request could not be decoded: %v", err)
	}
	messages, ok := request["messages"].([]any)
	if !ok {
		t.Fatal("captured PDF request has no message history")
	}
	var callID string
	for _, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok {
			continue
		}
		blocks, ok := message["content"].([]any)
		if !ok {
			continue
		}
		for _, rawBlock := range blocks {
			block, ok := rawBlock.(map[string]any)
			if !ok {
				continue
			}
			if block["type"] == "tool_use" && block["name"] == "exec_command" {
				if id, ok := block["id"].(string); ok {
					callID = id
				}
			}
		}
	}
	if callID == "" {
		t.Fatal("captured PDF request has no renderer call id")
	}
	return callID
}

func TestLiveChatAttachmentMessagesToolUseIsStreamProofButNotPublicTerminal(t *testing.T) {
	candidate := liveAttachmentCases()[3]
	body := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-haiku-5-5\",\"usage\":{\"input_tokens\":12}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_render\",\"name\":\"exec_command\",\"input\":{\"cmd\":\"pdftoppm -png document.pdf page\"}}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":5}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	capture := liveServerToolCapture{PublicRequest: liveHTTPLog{URL: "https://api.githubcopilot.com/v1/messages"}, Response: liveHTTPLog{Status: 200, Body: body}}
	if !liveCaptureStreamProven(capture, candidate.model) || !liveAttachmentMessagesToolCallTerminal([]byte(body), candidate.model) {
		t.Fatal("a complete native Messages tool_use response was not proven")
	}
	if liveAttachmentCapturedResponseCompleted(body, candidate, "/v1/messages") {
		t.Fatal("the pre-media Messages tool_use response was mistaken for a final user-facing answer")
	}
}

func TestLiveChatAttachmentRecordedNativeStreamWithoutObject(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "live_chat_completion_no_object.json"))
	if err != nil {
		t.Fatal(err)
	}
	var captured string
	if err := json.Unmarshal(body, &captured); err != nil {
		t.Fatal(err)
	}
	body = []byte(captured)
	stream, ok := liveChatCompletionStream(body, "claude-haiku-5.5")
	if !ok || stream.ResponseID != "chatcmpl-replay" || stream.Model != "claude-haiku-5.5" || stream.FinishReason != "stop" || stream.Text != "The top half is a bright red, and the bottom half is a royal blue. The image is split into two roughly equal horizontal bands." || stream.PromptTokens != 12856 || stream.OutputTokens != 118 || stream.TotalTokens != 12974 {
		t.Fatal("the captured native Chat stream without object fields was not validated completely")
	}
	if !liveAttachmentCapturedResponseCompleted(string(body), liveAttachmentCases()[2], "/chat/completions") {
		t.Fatal("the recorded captured Chat stop was not recognized as a final answer")
	}
	mutations := map[string]string{
		"missing-id":      strings.ReplaceAll(string(body), `"id":"chatcmpl-replay",`, ""),
		"inconsistent-id": strings.Replace(string(body), `"id":"chatcmpl-replay"`, `"id":"chatcmpl-other"`, 1),
		"wrong-object":    strings.Replace(string(body), `{"choices":`, `{"object":"unexpected","choices":`, 1),
		"wrong-model":     strings.Replace(string(body), `"model":"claude-haiku-5.5"`, `"model":"gpt-6-luna"`, 1),
		"bad-usage":       strings.Replace(string(body), `"total_tokens":12974`, `"total_tokens":12973`, 1),
		"missing-usage":   strings.Replace(string(body), `,"usage":{"completion_tokens":118,"prompt_tokens":12856,"prompt_tokens_details":{"cached_tokens":0},"total_tokens":12974}`, "", 1),
		"bad-finish":      strings.Replace(string(body), `"finish_reason":"stop"`, `"finish_reason":"length"`, 1),
		"truncated":       strings.TrimSuffix(string(body), "data: [DONE]\n\n"),
	}
	for name, invalid := range mutations {
		t.Run(name, func(t *testing.T) {
			if invalid == string(body) {
				t.Fatal("negative recorded-wire fixture did not mutate the response")
			}
			if _, ok := liveChatCompletionStream([]byte(invalid), "claude-haiku-5.5"); ok {
				t.Fatal("invalid recorded native Chat response was accepted")
			}
		})
	}
}

func TestLiveChatAttachmentCatalogAndRequestFailures(t *testing.T) {
	candidate := liveAttachmentCases()[2]
	if liveAttachmentPreparedPublicRequest("X/"+candidate.cell, "/v1/messages", []byte(`{"model":"claude-haiku-5.5","output_config":{"effort":"medium"}}`)) {
		t.Fatal("Chat-only catalog model was sent to the unsupported Messages endpoint")
	}
	if liveAttachmentPreparedPublicRequest("X/"+candidate.cell, "/chat/completions", []byte(`{"model":"claude-haiku-5.5-low","stream":true,"reasoning_effort":"medium","messages":[{"role":"user","content":"inspect"}]}`)) {
		t.Fatal("lossy model downgrade was accepted for the Chat endpoint")
	}
	if liveChatAttachmentToolResultSucceeded(liveChatAttachmentToolResult{ID: "call-a", Content: "Process exited with code 71\n"}, "exec_command", liveAttachmentCases()[3]) {
		t.Fatal("failed local PDF renderer tool result was accepted")
	}
	if liveChatAttachmentToolResultSucceeded(liveChatAttachmentToolResult{ID: "call-a", Content: "completed"}, "view_image", liveAttachmentCases()[3]) {
		t.Fatal("view_image tool result without actual image bytes was accepted")
	}
	if !liveChatAttachmentToolResultSucceeded(liveChatAttachmentToolResult{ID: "call-a", HasImage: true}, "view_image", liveAttachmentCases()[3]) {
		t.Fatal("paired view_image tool result with verified image bytes was rejected")
	}
	if _, ok := liveChatTokenValue(float64(math.Inf(1))); ok {
		t.Fatal("invalid usage or tool-call indices were accepted")
	}
	if _, ok := liveChatToolIndex(float64(65)); ok {
		t.Fatal("invalid usage or tool-call indices were accepted")
	}
}

func TestLiveAttachmentChatHistoryCorrelationsPreserveBothOriginalIDsAndArguments(t *testing.T) {
	calls := map[string]liveAttachmentCapturedToolCall{
		"render": {ItemID: "fc_render", CallID: "render", Name: "exec_command", Arguments: map[string]any{"cmd": "pdftoppm -png document.pdf page && ls -la page*"}},
		"view":   {ItemID: "fc_view", CallID: "view", Name: "view_image", Arguments: map[string]any{"path": "page-1.png"}},
	}
	request := liveAttachmentChatRequestEvidence{}
	for _, id := range []string{"render", "view"} {
		call := calls[id]
		carrier := map[string]string{"render": "cpa_tool_v1_eyJpIjoiZmNfcmVuZGVyIiwiYyI6InJlbmRlciJ9", "view": "cpa_tool_v1_eyJpIjoiZmNfdmlldyIsImMiOiJ2aWV3In0"}[id]
		request.ToolCalls = append(request.ToolCalls, liveChatAttachmentToolCall{ID: carrier, Name: call.Name, Arguments: call.Arguments})
		request.ToolResults = append(request.ToolResults, liveChatAttachmentToolResult{ID: carrier})
	}
	if _, ok := liveAttachmentChatHistoryCorrelations(request, calls, calls); !ok {
		t.Fatal("original Messages to Chat history did not retain both Responses IDs")
	}
	for _, mutation := range []string{"item-id", "call-id", "arguments", "result-id", "missing-result"} {
		t.Run(mutation, func(t *testing.T) {
			invalid := request
			invalid.ToolCalls = append([]liveChatAttachmentToolCall(nil), request.ToolCalls...)
			invalid.ToolResults = append([]liveChatAttachmentToolResult(nil), request.ToolResults...)
			switch mutation {
			case "item-id":
				invalid.ToolCalls[0].ID = "cpa_tool_v1_eyJpIjoiZmNfb3RoZXIiLCJjIjoicmVuZGVyIn0"
			case "call-id":
				invalid.ToolCalls[0].ID = "cpa_tool_v1_eyJpIjoiZmNfcmVuZGVyIiwiYyI6Im90aGVyIn0"
			case "arguments":
				invalid.ToolCalls[0].Arguments = map[string]any{"cmd": "other"}
			case "result-id":
				invalid.ToolResults[0].ID = "other"
			case "missing-result":
				invalid.ToolResults = invalid.ToolResults[:1]
			}
			if _, ok := liveAttachmentChatHistoryCorrelations(invalid, calls, calls); ok {
				t.Fatal("unmatched native history was accepted")
			}
		})
	}
}

func TestAttachmentPacketHistoricalCodexPDFChatTransitionReplay(t *testing.T) {
	gateDirectory := os.Getenv("CPA_ATTACHMENT_CHAT_TRANSITION_REPLAY_GATE_DIR")
	caseDirectory := os.Getenv("CPA_ATTACHMENT_CHAT_TRANSITION_REPLAY_CASE_DIR")
	if gateDirectory == "" || caseDirectory == "" {
		t.Skip("set both private directories to audit the retained original Codex PDF packet")
	}
	read := func(name string) []byte {
		body, err := os.ReadFile(filepath.Join(caseDirectory, name))
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	original := read("result.json")
	originalSHA := sha256.Sum256(original)
	var oldResult struct {
		Accepted        bool `json:"accepted"`
		ExitCode        int  `json:"exit_code"`
		CaptureError    bool `json:"capture_error"`
		GateDenials     int  `json:"gate_denials"`
		OriginalDenials int  `json:"original_client_denials"`
	}
	if err := json.Unmarshal(original, &oldResult); err != nil {
		t.Fatal(err)
	}
	var claim struct {
		Cell     string   `json:"cell"`
		Fixture  string   `json:"staged_fixture"`
		Original string   `json:"original_fixture"`
		Args     []string `json:"original_cli_args"`
	}
	if err := json.Unmarshal(read("claim.json"), &claim); err != nil {
		t.Fatal(err)
	}
	candidate := liveAttachmentCases()[3]
	candidate.fixture = claim.Fixture
	captures, err := (&liveAttachmentIngress{directory: filepath.Join(caseDirectory, "original-client-hop")}).captures()
	if err != nil {
		t.Fatal(err)
	}
	client := liveAttachmentCLI{stdout: read("client-stdout.jsonl"), args: claim.Args, exitCode: oldResult.ExitCode}
	evidence := inspectLiveAttachmentEvidence(t, &liveServerToolGate{directory: gateDirectory}, candidate, claim.Cell, client, captures)
	inspectLiveOriginalClientAttachment(candidate, captures, &evidence)
	source, err := os.ReadFile(claim.Original)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := os.ReadFile(claim.Fixture)
	if err != nil {
		t.Fatal(err)
	}
	sourceEqual := bytes.Equal(source, staged) && bytes.HasPrefix(source, []byte("%PDF-"))
	accepted := oldResult.ExitCode == 0 && !oldResult.CaptureError && oldResult.GateDenials == 0 && oldResult.OriginalDenials == 0 && len(captures) == 3 && evidence.PhysicalInference == 3 && sourceEqual && evidence.ClientFinal && evidence.ClientToolCall && evidence.ClientToolResult && evidence.ClientMedia && evidence.ClientAnswer && evidence.IngressPrepared && liveAttachmentMediaPrepared(candidate, evidence) && evidence.PublicModel && evidence.PublicEffort && evidence.PublicEndpoint && evidence.PublicMedia && evidence.IngressMediaSHA256 == evidence.PublicMediaSHA256 && evidence.PublicToolCall && evidence.PublicToolResult && evidence.PublicTerminal && evidence.PublicStreamProven && evidence.RendererOutput
	t.Logf("audited evidence: %+v", evidence)
	if !accepted {
		t.Fatal("retained original PDF packet did not satisfy full attachment acceptance")
	}
	if !bytes.Equal(original, read("result.json")) {
		t.Fatal("original failure verdict was modified")
	}
	if os.Getenv("CPA_ATTACHMENT_CHAT_TRANSITION_WRITE_AUDIT") != "1" {
		return
	}
	audit, err := json.MarshalIndent(map[string]any{"accepted": accepted, "original_accepted": oldResult.Accepted, "original_result_sha256": hex.EncodeToString(originalSHA[:]), "source_prepared_bytes_equal": sourceEqual, "evidence": evidence, "provider_calls": 0}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(caseDirectory, "private-audited-result.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.Write(append(audit, '\n'))
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("write private audit: %v; close: %v", writeErr, closeErr)
	}
}

func TestLiveAttachmentMessagesObservedEmptyDeltaAndDoneKeepTerminalStrict(t *testing.T) {
	stream := "event: message_start\ndata: " + `{"type":"message_start","message":{"type":"message","role":"assistant","model":"claude-haiku-5-5","usage":{"input_tokens":2}}}` + "\n\n" +
		"event: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool-a","name":"view_image","input":{}}}` + "\n\n" +
		"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}` + "\n\n" +
		"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"page-1.png\"}"}}` + "\n\n" +
		"event: content_block_stop\ndata: " + `{"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}` + "\n\n" +
		"event: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\ndata: [DONE]\n\n"
	if !liveCompleteMessagesTerminal([]byte(stream), "claude-haiku-5.5") {
		t.Fatal("observed empty delta and post-stop DONE were refused")
	}
	for _, invalid := range []string{
		stream + "data: [DONE]\n\n",
		stream + "event: error\ndata: {\"type\":\"error\"}\n\n",
		strings.Replace(stream, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", "", 1),
		strings.Replace(stream, `"partial_json":"{\"path\":\"page-1.png\"}"`, `"partial_json":"{"`, 1),
		strings.Replace(stream, `"partial_json":""`, `"partial_json":null`, 1),
	} {
		if invalid == stream || liveCompleteMessagesTerminal([]byte(invalid), "claude-haiku-5.5") {
			t.Fatal("unproven or trailing Messages terminal was accepted")
		}
	}
}
