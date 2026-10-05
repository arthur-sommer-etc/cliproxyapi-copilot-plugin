package provider

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func normalizeClaudeSourceRequest(sourceFormat string, payload []byte) ([]byte, error) {
	if sourceFormat != "claude" {
		return payload, nil
	}
	translated := append([]byte(nil), payload...)
	var err error
	translated, err = normalizeClaudeContextManagement(translated)
	if err != nil {
		return nil, err
	}
	return normalizeClaudeSafeguards(translated)
}

func normalizeClaudeMessagesRequest(model upstreamModel, sourceFormat string, original, translated []byte) ([]byte, error) {
	if !isClaudeFamilyModel(model) || sourceFormat != "claude" && sourceFormat != "openai-response" {
		return translated, nil
	}
	var err error
	translated, err = normalizeClaudeContextManagement(translated)
	if err != nil {
		return nil, err
	}
	translated, err = normalizeClaudeSafeguards(translated)
	if err != nil {
		return nil, err
	}
	root := gjson.ParseBytes(translated)
	messages := root.Get("messages")
	if messages.IsArray() {
		var messageEffort string
		hasMessageEffort := false
		messageIndexes := make([]int, 0)
		effortMarkerIndexes := make([]int, 0)
		for index, message := range messages.Array() {
			config := message.Get("output_config")
			if !config.Exists() {
				continue
			}
			if !config.IsObject() {
				return nil, fmt.Errorf("Claude Messages message %d has an invalid output_config", index)
			}
			configHasEffort := false
			for name, value := range config.Map() {
				if name != "effort" {
					return nil, fmt.Errorf("Claude Messages message %d has an unsupported output_config option %q", index, name)
				}
				if value.Type != gjson.String {
					return nil, fmt.Errorf("Claude Messages message %d has a non-string output_config effort", index)
				}
				messageEffort = value.String()
				hasMessageEffort = true
				configHasEffort = true
			}
			if message.Get("role").String() == "system" {
				content := message.Get("content")
				if configHasEffort && content.IsArray() && len(content.Array()) == 0 {
					if !isClaudeEffortMarker(message) {
						return nil, fmt.Errorf("Claude Messages system effort marker has unsupported fields")
					}
					effortMarkerIndexes = append(effortMarkerIndexes, index)
					continue
				}
				return nil, fmt.Errorf("Claude Messages system content with per-message output_config is unsupported")
			}
			messageIndexes = append(messageIndexes, index)
		}
		if len(messageIndexes) > 0 || len(effortMarkerIndexes) > 0 {
			rootOutputConfig := root.Get("output_config")
			if hasMessageEffort && rootOutputConfig.Exists() && !rootOutputConfig.IsObject() && !rootOutputConfig.Get("effort").Exists() {
				return nil, fmt.Errorf("Claude Messages root output_config cannot accept message-level effort")
			}
			if hasMessageEffort && !root.Get("output_config.effort").Exists() {
				var err error
				translated, err = sjson.SetBytes(translated, "output_config.effort", messageEffort)
				if err != nil {
					return nil, fmt.Errorf("move Claude Messages effort to root: %w", err)
				}
			}
			for _, index := range messageIndexes {
				path := "messages." + strconv.Itoa(index) + ".output_config"
				var err error
				translated, err = sjson.DeleteBytes(translated, path)
				if err != nil {
					return nil, fmt.Errorf("remove Claude Messages message output_config: %w", err)
				}
			}
			for index := len(effortMarkerIndexes) - 1; index >= 0; index-- {
				path := "messages." + strconv.Itoa(effortMarkerIndexes[index])
				translated, err = sjson.DeleteBytes(translated, path)
				if err != nil {
					return nil, fmt.Errorf("remove Claude Messages effort marker: %w", err)
				}
			}
		}
	}
	if sourceFormat != "openai-response" || !model.Capabilities.Supports.AdaptiveThinking || root.Get("thinking.type").String() != "enabled" {
		return normalizeClaudeThinkingDisplay(translated)
	}
	requestedEffort := gjson.GetBytes(original, "reasoning.effort").String()
	rootEffortExists := gjson.GetBytes(translated, "output_config.effort").Exists()
	effort, effortOK := adaptiveClaudeEffort(requestedEffort, model.Capabilities.Supports.ReasoningEffort)
	if !rootEffortExists && !effortOK {
		return translated, nil
	}
	translated, err = sjson.SetBytes(translated, "thinking.type", "adaptive")
	if err != nil {
		return nil, fmt.Errorf("enable adaptive Claude thinking: %w", err)
	}
	translated, err = sjson.DeleteBytes(translated, "thinking.budget_tokens")
	if err != nil {
		return nil, fmt.Errorf("remove Claude thinking budget: %w", err)
	}
	if effortOK && effort != "" && !gjson.GetBytes(translated, "output_config.effort").Exists() {
		translated, err = sjson.SetBytes(translated, "output_config.effort", effort)
		if err != nil {
			return nil, fmt.Errorf("set adaptive Claude effort: %w", err)
		}
	}
	return normalizeClaudeThinkingDisplay(translated)
}

func normalizeClaudeThinkingDisplay(body []byte) ([]byte, error) {
	thinking := gjson.GetBytes(body, "thinking")
	if thinking.Get("type").String() != "adaptive" {
		return body, nil
	}
	display := thinking.Get("display")
	if !display.Exists() {
		return body, nil
	}
	if display.Type != gjson.String {
		return nil, fmt.Errorf("Claude adaptive thinking display must be a string")
	}
	switch display.String() {
	case "summarized", "omitted":
		return body, nil
	case "updates":
		updated, err := sjson.SetBytes(body, "thinking.display", "summarized")
		if err != nil {
			return nil, fmt.Errorf("normalize Claude adaptive thinking display: %w", err)
		}
		return updated, nil
	default:
		return nil, fmt.Errorf("Claude adaptive thinking display is unsupported by the selected Copilot endpoint")
	}
}

func normalizeClaudeContextManagement(body []byte) ([]byte, error) {
	contextManagement := gjson.GetBytes(body, "context_management")
	if !contextManagement.Exists() {
		return body, nil
	}
	if contextManagement.Type == gjson.Null {
		return deleteClaudeMessagesField(body, "context_management")
	}
	if !contextManagement.IsObject() {
		return nil, fmt.Errorf("Claude context_management is unsupported by the selected Copilot endpoint")
	}
	for name := range contextManagement.Map() {
		if name != "edits" {
			return nil, fmt.Errorf("Claude context_management option is unsupported by the selected Copilot endpoint")
		}
	}
	edits := contextManagement.Get("edits")
	if !edits.Exists() {
		return nil, fmt.Errorf("Claude context_management is unsupported by the selected Copilot endpoint")
	}
	if !edits.IsArray() {
		return nil, fmt.Errorf("Claude context_management edits are unsupported by the selected Copilot endpoint")
	}
	if len(edits.Array()) == 0 {
		return deleteClaudeMessagesField(body, "context_management")
	}
	if len(edits.Array()) != 1 {
		return nil, fmt.Errorf("Claude context_management edits are unsupported by the selected Copilot endpoint")
	}
	edit := edits.Array()[0]
	if !edit.IsObject() || len(edit.Map()) != 2 || edit.Get("type").Type != gjson.String || edit.Get("type").String() != "clear_thinking_20251015" || edit.Get("keep").Type != gjson.String || edit.Get("keep").String() != "all" {
		return nil, fmt.Errorf("Claude context_management edit is unsupported by the selected Copilot endpoint")
	}
	return deleteClaudeMessagesField(body, "context_management")
}

func normalizeClaudeSafeguards(body []byte) ([]byte, error) {
	safeguards := gjson.GetBytes(body, "safeguards")
	if !safeguards.Exists() {
		return body, nil
	}
	if safeguards.Type == gjson.Null || safeguards.IsObject() && len(safeguards.Map()) == 0 || safeguards.IsArray() && len(safeguards.Array()) == 0 {
		return deleteClaudeMessagesField(body, "safeguards")
	}
	return nil, fmt.Errorf("Claude safeguards are unsupported by the selected Copilot endpoint")
}

func deleteClaudeMessagesField(body []byte, path string) ([]byte, error) {
	updated, err := sjson.DeleteBytes(body, path)
	if err != nil {
		return nil, fmt.Errorf("remove unsupported Claude Messages field: %w", err)
	}
	return updated, nil
}

func isClaudeEffortMarker(message gjson.Result) bool {
	if !message.IsObject() || len(message.Map()) != 3 {
		return false
	}
	for name := range message.Map() {
		if name != "role" && name != "content" && name != "output_config" {
			return false
		}
	}
	return true
}

func adaptiveClaudeEffort(requested string, supported []string) (string, bool) {
	requested = strings.ToLower(strings.TrimSpace(requested))
	if requested == "auto" {
		return "", true
	}
	if modelSupportsEffort(supported, requested) {
		return requested, true
	}
	switch requested {
	case "minimal":
		requested = "low"
	case "xhigh":
		if modelSupportsEffort(supported, "max") {
			requested = "max"
		} else {
			requested = "high"
		}
	case "max":
		requested = "high"
	case "low", "medium", "high":
	default:
		return "", false
	}
	return requested, modelSupportsEffort(supported, requested)
}

func modelSupportsEffort(supported []string, effort string) bool {
	for _, candidate := range supported {
		if strings.EqualFold(strings.TrimSpace(candidate), effort) {
			return true
		}
	}
	return false
}
