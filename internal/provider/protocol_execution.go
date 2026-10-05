package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
)

func (s *Service) compactionEnabled(model string) bool {
	for _, configured := range s.Config().CompactionModels {
		if strings.EqualFold(strings.TrimSpace(configured), strings.TrimSpace(model)) {
			return true
		}
	}
	return false
}

func compactionKeyMaterial(authID, credential, model, endpoint, apiBaseURL string) (string, []byte) {
	return compactionKeyMaterialFromFingerprint(authID, tokenFingerprint(credential), model, endpoint, apiBaseURL)
}

func compactionKeyMaterialFromFingerprint(authID, credentialHash, model, endpoint, apiBaseURL string) (string, []byte) {
	secret, err := hex.DecodeString(credentialHash)
	if err != nil {
		return "", nil
	}
	parts := []string{"cpa-copilot-bridge-compaction-v1", strings.TrimSpace(authID), credentialHash, strings.ToLower(strings.TrimSpace(model)), strings.TrimSpace(endpoint), strings.TrimRight(strings.TrimSpace(apiBaseURL), "/")}
	scopeHash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(scopeHash[:]), secret
}

func buildResponsesCompactionFrames(payload []byte) ([][]byte, error) {
	var response map[string]json.RawMessage
	if err := json.Unmarshal(payload, &response); err != nil || response == nil {
		return nil, fmt.Errorf("compaction result is not a JSON object")
	}
	var status string
	if err := json.Unmarshal(response["status"], &status); err != nil || status != "completed" {
		return nil, fmt.Errorf("compaction result is not completed")
	}
	if rawError := bytes.TrimSpace(response["error"]); len(rawError) > 0 && !bytes.Equal(rawError, []byte("null")) {
		return nil, fmt.Errorf("compaction result contains an error")
	}
	var output []json.RawMessage
	if err := json.Unmarshal(response["output"], &output); err != nil || output == nil {
		return nil, fmt.Errorf("compaction result has no output array")
	}
	compactionCount := 0
	for _, item := range output {
		var decoded struct {
			Type             string `json:"type"`
			EncryptedContent string `json:"encrypted_content"`
		}
		if err := json.Unmarshal(item, &decoded); err != nil {
			return nil, fmt.Errorf("compaction result contains an invalid output item")
		}
		if decoded.Type == "compaction" || decoded.Type == "compaction_summary" {
			if strings.TrimSpace(decoded.EncryptedContent) == "" {
				return nil, fmt.Errorf("compaction result contains an empty compaction item")
			}
			compactionCount++
		}
	}
	if compactionCount != 1 {
		return nil, fmt.Errorf("compaction result must contain exactly one compaction item")
	}
	progress := make(map[string]json.RawMessage, len(response))
	for key, value := range response {
		progress[key] = append(json.RawMessage(nil), value...)
	}
	progress["status"] = json.RawMessage(`"in_progress"`)
	progress["output"] = json.RawMessage(`[]`)
	delete(progress, "usage")
	delete(progress, "completed_at")
	progressResponse, err := json.Marshal(progress)
	if err != nil {
		return nil, fmt.Errorf("encode compaction stream response")
	}
	frames := make([][]byte, 0, 3+len(output)*2)
	sequence := 0
	created, err := marshalCompactionEvent("response.created", sequence, map[string]json.RawMessage{"response": progressResponse})
	if err != nil {
		return nil, err
	}
	frames = append(frames, created)
	sequence++
	inProgress, err := marshalCompactionEvent("response.in_progress", sequence, map[string]json.RawMessage{"response": progressResponse})
	if err != nil {
		return nil, err
	}
	frames = append(frames, inProgress)
	sequence++
	for index, item := range output {
		fields := map[string]json.RawMessage{
			"output_index": json.RawMessage(strconv.Itoa(index)),
			"item":         item,
		}
		added, errAdded := marshalCompactionEvent("response.output_item.added", sequence, fields)
		if errAdded != nil {
			return nil, errAdded
		}
		frames = append(frames, added)
		sequence++
		done, errDone := marshalCompactionEvent("response.output_item.done", sequence, fields)
		if errDone != nil {
			return nil, errDone
		}
		frames = append(frames, done)
		sequence++
	}
	completed, err := marshalCompactionEvent("response.completed", sequence, map[string]json.RawMessage{"response": append(json.RawMessage(nil), payload...)})
	if err != nil {
		return nil, err
	}
	frames = append(frames, completed)
	return frames, nil
}

func marshalCompactionEvent(event string, sequence int, fields map[string]json.RawMessage) ([]byte, error) {
	typeJSON, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("encode compaction stream event")
	}
	data := map[string]json.RawMessage{"type": typeJSON, "sequence_number": json.RawMessage(strconv.Itoa(sequence))}
	for key, value := range fields {
		data[key] = value
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("encode compaction stream event")
	}
	frame := make([]byte, 0, len(event)+len(payload)+16)
	frame = append(frame, "event: "...)
	frame = append(frame, event...)
	frame = append(frame, "\ndata: "...)
	frame = append(frame, payload...)
	frame = append(frame, '\n', '\n')
	return frame, nil
}

func hasCompactionTrigger(payload []byte) bool {
	var root map[string]json.RawMessage
	if json.Unmarshal(payload, &root) != nil {
		return false
	}
	var input []json.RawMessage
	if json.Unmarshal(root["input"], &input) != nil {
		return false
	}
	for _, rawItem := range input {
		var item struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(rawItem, &item) == nil && item.Type == "compaction_trigger" {
			return true
		}
	}
	return false
}

func addCompactionTrigger(payload []byte) ([]byte, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(payload, &root); err != nil || root == nil {
		return nil, fmt.Errorf("Responses compact request must be a JSON object")
	}
	var input []json.RawMessage
	rawInput := root["input"]
	if len(rawInput) == 0 {
		input = []json.RawMessage{}
	} else if bytesFirstNonSpace(rawInput) == '[' {
		if err := json.Unmarshal(rawInput, &input); err != nil {
			return nil, fmt.Errorf("Responses compact input must be an array")
		}
	} else if bytesFirstNonSpace(rawInput) == '"' {
		var text string
		if err := json.Unmarshal(rawInput, &text); err != nil {
			return nil, fmt.Errorf("Responses compact input text is invalid")
		}
		message, err := json.Marshal(map[string]any{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": text}}})
		if err != nil {
			return nil, fmt.Errorf("encode Responses compact input: %w", err)
		}
		input = []json.RawMessage{message}
	} else {
		return nil, fmt.Errorf("Responses compact input must be a string or array")
	}
	input = append(input, json.RawMessage(`{"type":"compaction_trigger"}`))
	updatedInput, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("encode Responses compact trigger: %w", err)
	}
	root["input"] = updatedInput
	updated, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("encode Responses compact request: %w", err)
	}
	return updated, nil
}

func bytesFirstNonSpace(raw []byte) byte {
	for _, value := range raw {
		if value != ' ' && value != '\n' && value != '\r' && value != '\t' {
			return value
		}
	}
	return 0
}

type streamTerminal struct {
	completed bool
	response  []byte
}

func (s *streamTerminal) observe(endpoint string, frame []byte, copilotToken, githubToken string) (bool, error) {
	event, data := parseSSEFrame(frame)
	if endpoint == translate.EndpointResponses {
		if data == "" {
			return false, nil
		}
		if data == "[DONE]" {
			return false, fmt.Errorf("Responses stream ended with a non-terminal DONE marker")
		}
		var payload struct {
			Type     string          `json:"type"`
			Error    json.RawMessage `json:"error"`
			Response json.RawMessage `json:"response"`
		}
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			return false, fmt.Errorf("decode Responses stream event: %s", redactStreamError(data, copilotToken, githubToken))
		}
		kind := firstNonEmpty(event, payload.Type)
		switch kind {
		case "error", "response.failed", "response.incomplete":
			return false, fmt.Errorf("Copilot Responses stream failed: %s", redactStreamError(data, copilotToken, githubToken))
		case "response.completed":
			if len(payload.Response) == 0 || bytesFirstNonSpace(payload.Response) != '{' {
				return false, fmt.Errorf("Copilot Responses stream completed without a response object")
			}
			var response struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal(payload.Response, &response); err != nil || (response.Status != "" && response.Status != "completed") {
				return false, fmt.Errorf("Copilot Responses stream completed with an unsuccessful response status")
			}
			s.completed = true
			s.response = append([]byte(nil), payload.Response...)
			return true, nil
		default:
			if len(payload.Error) > 0 && string(payload.Error) != "null" {
				return false, fmt.Errorf("Copilot Responses stream error: %s", redactStreamError(data, copilotToken, githubToken))
			}
		}
		return false, nil
	}
	if endpoint == translate.EndpointChatCompletions {
		if data == "" {
			return false, nil
		}
		if data == "[DONE]" {
			s.completed = true
			return true, nil
		}
		var payload struct {
			Error json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			return false, fmt.Errorf("decode Chat Completions stream event: %s", redactStreamError(data, copilotToken, githubToken))
		}
		if strings.EqualFold(event, "error") || (len(payload.Error) > 0 && string(payload.Error) != "null") {
			return false, fmt.Errorf("Copilot Chat Completions stream failed: %s", redactStreamError(data, copilotToken, githubToken))
		}
		return false, nil
	}
	if endpoint == translate.EndpointMessages {
		if strings.EqualFold(event, "error") {
			return false, fmt.Errorf("Copilot Messages stream failed: %s", redactStreamError(data, copilotToken, githubToken))
		}
		if strings.EqualFold(event, "message_stop") {
			s.completed = true
			return true, nil
		}
		return false, nil
	}
	return false, fmt.Errorf("unsupported Copilot stream endpoint %q", endpoint)
}

func parseSSEFrame(frame []byte) (string, string) {
	var event string
	var data []string
	for _, rawLine := range strings.Split(string(frame), "\n") {
		line := strings.TrimSuffix(rawLine, "\r")
		field, value, found := strings.Cut(line, ":")
		if !found {
			field = line
		}
		if strings.HasPrefix(value, " ") {
			value = value[1:]
		}
		switch field {
		case "event":
			event = value
		case "data":
			data = append(data, value)
		}
	}
	return event, strings.Join(data, "\n")
}

func redactStreamError(_, _, _ string) string {
	return "upstream stream error details withheld"
}
