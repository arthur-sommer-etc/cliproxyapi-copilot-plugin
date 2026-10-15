package translate

import (
	"encoding/json"
	"fmt"
	"strings"
)

func validateClaudeHostedSearchUse(block map[string]any) (string, string, error) {
	if err := rejectFields(block, "type", "id", "name", "input"); err != nil {
		return "", "", fmt.Errorf("Claude web search call has an unsupported field")
	}
	if stringValue(block["type"]) != "server_tool_use" || stringValue(block["name"]) != "web_search" {
		return "", "", fmt.Errorf("Claude server tool block is unsupported: server_tool_use is not a web search")
	}
	id := rawStringValue(block["id"])
	if !validClaudeServerToolID(id) {
		return "", "", fmt.Errorf("Claude web search call has an unsupported ID")
	}
	input, ok := block["input"].(map[string]any)
	if !ok || len(input) != 1 {
		return "", "", fmt.Errorf("Claude web search call input has an unsupported shape")
	}
	query, ok := input["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		return "", "", fmt.Errorf("Claude web search call has no query")
	}
	return id, query, nil
}

func validateClaudeHostedSearchResult(block map[string]any, callID string) (json.RawMessage, error) {
	if err := rejectFields(block, "type", "tool_use_id", "content"); err != nil {
		return nil, fmt.Errorf("Claude web search result has an unsupported field")
	}
	if stringValue(block["type"]) != "web_search_tool_result" || rawStringValue(block["tool_use_id"]) != callID {
		return nil, fmt.Errorf("Claude web search result has no matching call")
	}
	content, ok := block["content"]
	if !ok {
		return nil, fmt.Errorf("Claude web search result has no content")
	}
	if err := validateClaudeWebSearchResults(content); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		return nil, fmt.Errorf("encode Claude web search results: %w", err)
	}
	return encoded, nil
}

func validateClaudeWebSearchResults(value any) error {
	validateEntry := func(entry map[string]any) error {
		switch stringValue(entry["type"]) {
		case "web_search_result":
			if rawStringValue(entry["url"]) == "" || rawStringValue(entry["encrypted_content"]) == "" {
				return fmt.Errorf("Claude web search result lacks its URL or replay token")
			}
			for _, field := range []string{"title", "page_age", "snippet"} {
				if raw, exists := entry[field]; exists {
					if _, ok := raw.(string); !ok {
						return fmt.Errorf("Claude web search result field %q has an unsupported value", field)
					}
				}
			}
		case "web_search_tool_result_error":
			if err := rejectFields(entry, "type", "error_code"); err != nil || rawStringValue(entry["error_code"]) == "" {
				return fmt.Errorf("Claude web search result error has an unsupported shape")
			}
		default:
			return fmt.Errorf("Claude web search results contain an unsupported entry")
		}
		return nil
	}
	switch content := value.(type) {
	case []any:
		for _, rawEntry := range content {
			entry, ok := rawEntry.(map[string]any)
			if !ok {
				return fmt.Errorf("Claude web search results contain an unsupported entry")
			}
			if err := validateEntry(entry); err != nil {
				return err
			}
		}
	case map[string]any:
		if err := validateEntry(content); err != nil || stringValue(content["type"]) != "web_search_tool_result_error" {
			return fmt.Errorf("Claude web search result content has an unsupported shape")
		}
	default:
		return fmt.Errorf("Claude web search result content has an unsupported shape")
	}
	return nil
}

func validateClaudeCitation(value any) error {
	citation, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("Claude citation has an unsupported shape")
	}
	if err := rejectFields(citation, "type", "url", "title", "encrypted_index", "cited_text"); err != nil {
		return fmt.Errorf("Claude citation has an unsupported field")
	}
	if stringValue(citation["type"]) != "web_search_result_location" || rawStringValue(citation["url"]) == "" || rawStringValue(citation["encrypted_index"]) == "" {
		return fmt.Errorf("Claude citation cannot be replayed exactly")
	}
	for _, field := range []string{"title", "cited_text"} {
		if raw, exists := citation[field]; exists {
			if _, ok := raw.(string); !ok {
				return fmt.Errorf("Claude citation field %q has an unsupported value", field)
			}
		}
	}
	return nil
}

func validateClaudeCitations(value any) error {
	citations, ok := value.([]any)
	if !ok || len(citations) == 0 {
		return fmt.Errorf("Claude citations have an unsupported shape")
	}
	for _, citation := range citations {
		if err := validateClaudeCitation(citation); err != nil {
			return err
		}
	}
	return nil
}

func validateClaudeHostedSearchMessage(root map[string]any) error {
	content := root["content"]
	if content == nil || !hasMeaningfulValue(content) {
		return nil
	}
	blocks, ok := content.([]any)
	if !ok {
		return nil
	}
	ids := make(map[string]struct{})
	for index := 0; index < len(blocks); index++ {
		block, ok := blocks[index].(map[string]any)
		if !ok {
			continue
		}
		switch stringValue(block["type"]) {
		case "server_tool_use":
			if index+1 >= len(blocks) {
				return fmt.Errorf("Claude web search call has no adjacent result")
			}
			id, _, err := validateClaudeHostedSearchUse(block)
			if err != nil {
				return err
			}
			if _, exists := ids[id]; exists {
				return fmt.Errorf("Claude web search response has a duplicate call ID")
			}
			if _, err := validateClaudeHostedSearchResult(objectValue(blocks[index+1]), id); err != nil {
				return err
			}
			ids[id] = struct{}{}
			index++
		case "web_search_tool_result":
			return fmt.Errorf("Claude web search result has no adjacent call")
		case "text":
			if citations, exists := block["citations"]; exists && hasMeaningfulValue(citations) {
				if rawStringValue(block["text"]) == "" {
					return fmt.Errorf("Claude citations require nonempty assistant text")
				}
				if err := validateClaudeCitations(citations); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

type claudeHostedSearchPair struct {
	ID      string
	Query   string
	Results json.RawMessage
}

type claudeCitationGroup struct {
	Text      string
	Run       int
	Citations []json.RawMessage
}

func claudeHostedSearchContent(root map[string]any) ([]claudeHostedSearchPair, []claudeCitationGroup) {
	blocks, _ := root["content"].([]any)
	var searches []claudeHostedSearchPair
	var groups []claudeCitationGroup
	run := 0
	for index := 0; index < len(blocks); index++ {
		block, _ := blocks[index].(map[string]any)
		if block == nil {
			continue
		}
		switch stringValue(block["type"]) {
		case "server_tool_use":
			run++
			id, query, err := validateClaudeHostedSearchUse(block)
			if err != nil || index+1 >= len(blocks) {
				continue
			}
			result, err := validateClaudeHostedSearchResult(objectValue(blocks[index+1]), id)
			if err == nil {
				searches = append(searches, claudeHostedSearchPair{ID: id, Query: query, Results: result})
			}
			index++
		case "text":
			group := claudeCitationGroup{Text: rawStringValue(block["text"]), Run: run}
			for _, citation := range arrayValue(block["citations"]) {
				encoded, err := json.Marshal(citation)
				if err == nil {
					group.Citations = append(group.Citations, encoded)
				}
			}
			groups = append(groups, group)
		default:
			run++
		}
	}
	return searches, groups
}

func restoreClaudeHostedSearchResponse(source map[string]any, body []byte) ([]byte, error) {
	searches, groups := claudeHostedSearchContent(source)
	root, err := decodeObject(body)
	if err != nil {
		return nil, fmt.Errorf("decode translated Claude hosted search response")
	}
	output, ok := root["output"].([]any)
	if !ok && len(searches) > 0 {
		return nil, fmt.Errorf("translated Responses output omitted the hosted search list")
	}
	for _, search := range searches {
		responsesID := "ws_" + search.ID
		matches := 0
		for _, rawItem := range output {
			item, ok := rawItem.(map[string]any)
			if !ok || stringValue(item["type"]) != "web_search_call" || rawStringValue(item["id"]) != responsesID {
				continue
			}
			item["action"] = map[string]any{"type": "search", "query": search.Query}
			var results any
			if err := json.Unmarshal(search.Results, &results); err != nil {
				return nil, fmt.Errorf("decode Claude hosted search results")
			}
			item["results"] = results
			matches++
		}
		if matches != 1 {
			return nil, fmt.Errorf("Claude hosted search could not be translated exactly")
		}
	}
	if err := restoreClaudeResponseCitations(output, groups); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("encode translated Claude hosted search response")
	}
	return encoded, nil
}

func restoreClaudeResponseCitations(output []any, groups []claudeCitationGroup) error {
	groupIndex := 0
	for _, rawItem := range output {
		item, ok := rawItem.(map[string]any)
		if !ok || stringValue(item["type"]) != "message" {
			continue
		}
		var text strings.Builder
		for _, rawPart := range arrayValue(item["content"]) {
			part := objectValue(rawPart)
			if stringValue(part["type"]) != "output_text" && stringValue(part["type"]) != "text" {
				return fmt.Errorf("Claude text boundaries cannot be restored onto non-text content")
			}
			text.WriteString(rawStringValue(part["text"]))
		}
		if groupIndex == len(groups) {
			if text.Len() != 0 {
				return fmt.Errorf("translated Responses message has no matching Claude text")
			}
			continue
		}
		run := groups[groupIndex].Run
		var sourceText strings.Builder
		var content []any
		for groupIndex < len(groups) && groups[groupIndex].Run == run {
			group := groups[groupIndex]
			sourceText.WriteString(group.Text)
			part := map[string]any{"type": "output_text", "text": group.Text, "annotations": []any{}}
			annotations := make([]any, 0, len(group.Citations))
			for _, rawCitation := range group.Citations {
				var citation any
				if err := json.Unmarshal(rawCitation, &citation); err != nil {
					return fmt.Errorf("decode Claude hosted search citation")
				}
				annotations = append(annotations, citation)
			}
			part["annotations"] = annotations
			content = append(content, part)
			groupIndex++
		}
		if text.String() != sourceText.String() {
			return fmt.Errorf("Claude hosted search text could not be restored exactly")
		}
		item["content"] = content
	}
	if groupIndex != len(groups) {
		return fmt.Errorf("Claude hosted search citation could not be restored exactly")
	}
	return nil
}

func validClaudeServerToolID(id string) bool {
	const prefix = "srvtoolu_"
	if !strings.HasPrefix(id, prefix) || len(id) == len(prefix) {
		return false
	}
	for _, character := range id[len(prefix):] {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_') {
			return false
		}
	}
	return true
}

func rejectFields(object map[string]any, fields ...string) error {
	allowed := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		allowed[field] = struct{}{}
	}
	for field := range object {
		if _, ok := allowed[field]; !ok {
			return fmt.Errorf("unsupported field %q", field)
		}
	}
	return nil
}
