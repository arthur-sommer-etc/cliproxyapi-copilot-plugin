package translate

import (
	"fmt"
	"strings"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func rejectOpaqueReasoningRequest(from, to sdktranslator.Format, body []byte) error {
	if from == to {
		return nil
	}
	switch from {
	case sdktranslator.FormatClaude:
		if to != sdktranslator.FormatOpenAIResponse && containsClaudeOpaqueReasoning(body) {
			return fmt.Errorf("cannot translate signed or redacted Claude reasoning from %s to %s because this route cannot preserve verification data", from, to)
		}
	case sdktranslator.FormatOpenAIResponse:
		if to == sdktranslator.FormatOpenAI && containsForeignResponsesOpaqueReasoning(body) {
			return fmt.Errorf("cannot translate foreign encrypted Responses reasoning to Copilot Chat")
		}
		if to != sdktranslator.FormatOpenAIResponse && to != sdktranslator.FormatOpenAI && to != sdktranslator.FormatClaude && containsResponsesOpaqueReasoning(body) {
			return fmt.Errorf("cannot translate encrypted Responses reasoning from %s to %s because this route cannot preserve verification data", from, to)
		}
	}
	return nil
}

func rejectOpaqueReasoningResponse(from, to sdktranslator.Format, body []byte) error {
	if from == to {
		return nil
	}
	if from == sdktranslator.FormatOpenAIResponse && to != sdktranslator.FormatClaude && containsResponsesOpaqueReasoning(body) {
		return fmt.Errorf("cannot translate encrypted Responses reasoning from %s to %s because this route cannot preserve verification data", from, to)
	}
	if from == sdktranslator.FormatClaude && to != sdktranslator.FormatOpenAIResponse && containsClaudeOpaqueReasoning(body) {
		return fmt.Errorf("cannot translate signed or redacted Claude reasoning from %s to %s because this route cannot preserve verification data", from, to)
	}
	return nil
}

func rejectOpaqueReasoningStream(from, to sdktranslator.Format, frame []byte) error {
	if from == to {
		return nil
	}
	_, data, _, err := parseSSEFrame(frame)
	if err != nil || len(data) == 0 {
		return nil
	}
	payload, err := decodeObject(data)
	if err != nil {
		return nil
	}
	if from == sdktranslator.FormatOpenAIResponse && to != sdktranslator.FormatClaude && containsResponsesOpaqueReasoning(data) {
		return fmt.Errorf("cannot translate encrypted Responses reasoning stream from %s to %s because this route cannot preserve verification data", from, to)
	}
	if from == sdktranslator.FormatClaude && to != sdktranslator.FormatOpenAIResponse && (containsClaudeOpaqueReasoning(data) || stringValue(objectValue(payload["delta"])["type"]) == "signature_delta") {
		return fmt.Errorf("cannot translate signed or redacted Claude reasoning stream from %s to %s because this route cannot preserve verification data", from, to)
	}
	return nil
}

func containsClaudeOpaqueReasoning(body []byte) bool {
	root, err := decodeObject(body)
	return err == nil && containsClaudeOpaqueValue(root)
}

func containsClaudeOpaqueValue(value any) bool {
	switch current := value.(type) {
	case map[string]any:
		switch rawStringValue(current["type"]) {
		case "thinking":
			if rawStringValue(current["signature"]) != "" {
				return true
			}
		case "redacted_thinking":
			if rawStringValue(current["data"]) != "" {
				return true
			}
		}
		for _, nested := range current {
			if containsClaudeOpaqueValue(nested) {
				return true
			}
		}
	case []any:
		for _, nested := range current {
			if containsClaudeOpaqueValue(nested) {
				return true
			}
		}
	}
	return false
}

func containsResponsesOpaqueReasoning(body []byte) bool {
	root, err := decodeObject(body)
	return err == nil && containsResponsesOpaqueValue(root)
}

func containsForeignResponsesOpaqueReasoning(body []byte) bool {
	root, err := decodeObject(body)
	return err == nil && containsForeignResponsesOpaqueValue(root)
}

func containsForeignResponsesOpaqueValue(value any) bool {
	switch current := value.(type) {
	case map[string]any:
		if rawStringValue(current["type"]) == "reasoning" {
			if encrypted := rawStringValue(current["encrypted_content"]); encrypted != "" && !strings.HasPrefix(encrypted, copilotOpaquePrefix) {
				return true
			}
		}
		for _, nested := range current {
			if containsForeignResponsesOpaqueValue(nested) {
				return true
			}
		}
	case []any:
		for _, nested := range current {
			if containsForeignResponsesOpaqueValue(nested) {
				return true
			}
		}
	}
	return false
}

func containsResponsesOpaqueValue(value any) bool {
	switch current := value.(type) {
	case map[string]any:
		if rawStringValue(current["type"]) == "reasoning" && rawStringValue(current["encrypted_content"]) != "" {
			return true
		}
		for _, nested := range current {
			if containsResponsesOpaqueValue(nested) {
				return true
			}
		}
	case []any:
		for _, nested := range current {
			if containsResponsesOpaqueValue(nested) {
				return true
			}
		}
	}
	return false
}
