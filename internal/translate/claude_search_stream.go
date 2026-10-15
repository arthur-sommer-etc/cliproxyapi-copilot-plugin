package translate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/sjson"
)

type claudeHostedSearchStreamState struct {
	SDKState        any
	Searches        map[string]*claudeHostedSearchStreamCall
	SearchesByBlock map[int]*claudeHostedSearchStreamCall
	Blocks          map[int]map[string]any
	Content         []any
	TextGroupsDone  int
	TextPart        map[string]any
	TextPartIndex   int
	TextBlockIndex  int
	TextBlockOpen   bool
	Sequence        int
	PendingResult   string
	Stopped         bool
}

type claudeHostedSearchStreamCall struct {
	ID              string
	Query           string
	InitialQuery    string
	HasInitialQuery bool
	Input           strings.Builder
	SawDelta        bool
	Results         json.RawMessage
	Part            map[string]any
	CallStopped     bool
	ResultSeen      bool
	ResultStopped   bool
}

func claudeStreamToResponses(ctx context.Context, model string, original, translated, frame []byte, state *any) ([][]byte, error) {
	if err := validateClaudeStreamFrame(frame, sdktranslator.FormatOpenAIResponse); err != nil {
		return nil, err
	}
	event, data, done, err := parseSSEFrame(frame)
	if err != nil {
		return nil, err
	}
	if state == nil {
		if claudeFrameNeedsHostedSearchState(event, data) {
			return nil, fmt.Errorf("Claude hosted-search stream translation requires state")
		}
		if len(data) == 0 {
			if done {
				return registry.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse, model, original, translated, []byte("data: [DONE]"), nil), nil
			}
			return nil, nil
		}
		normalized := append([]byte("data: "), data...)
		return registry.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse, model, original, translated, normalized, nil), nil
	}
	streamState, ok := (*state).(*claudeHostedSearchStreamState)
	if !ok {
		if *state != nil {
			return nil, fmt.Errorf("Claude hosted-search stream state has an unsupported type")
		}
		streamState = &claudeHostedSearchStreamState{
			Searches:        make(map[string]*claudeHostedSearchStreamCall),
			SearchesByBlock: make(map[int]*claudeHostedSearchStreamCall),
			Blocks:          make(map[int]map[string]any),
		}
		*state = streamState
	}
	if err := streamState.observe(event, data, done); err != nil {
		return nil, err
	}
	if len(data) == 0 {
		if done {
			return registry.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse, model, original, translated, []byte("data: [DONE]"), &streamState.SDKState), nil
		}
		return nil, nil
	}
	normalized := append([]byte("data: "), data...)
	frames := registry.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse, model, original, translated, normalized, &streamState.SDKState)
	for index, output := range frames {
		frames[index], err = streamState.restore(output)
		if err != nil {
			return nil, err
		}
	}
	return streamState.restoreTextParts(event, data, frames)
}

func (s *claudeHostedSearchStreamState) restoreTextParts(sourceEvent string, sourceData []byte, frames [][]byte) ([][]byte, error) {
	source, err := decodeObject(sourceData)
	if err != nil {
		return nil, err
	}
	if sourceEvent == "" {
		sourceEvent = stringValue(source["type"])
	}
	block := s.Blocks[intValue(source["index"])]
	part := map[string]any{"type": "output_text", "text": rawStringValue(block["text"]), "annotations": append([]any{}, arrayValue(block["citations"])...)}
	var payloads []map[string]any
	partAdded := false
	for _, frame := range frames {
		event, data, done, err := parseSSEFrame(frame)
		if err != nil {
			return nil, err
		}
		if done || len(data) == 0 {
			continue
		}
		payload, err := decodeObject(data)
		if err != nil {
			return nil, err
		}
		if event == "" {
			event = stringValue(payload["type"])
		}
		switch event {
		case "response.output_text.done", "response.content_part.done":
			continue
		case "response.content_part.added":
			s.TextPart = payload
			s.TextPartIndex = 0
			partAdded = true
			payload["part"].(map[string]any)["text"] = part["text"]
			payload["part"].(map[string]any)["annotations"] = part["annotations"]
		case "response.output_text.delta":
			payload["content_index"] = s.TextPartIndex
		}
		payloads = append(payloads, payload)
	}
	if stringValue(block["type"]) == "text" {
		if sourceEvent == "content_block_start" && !partAdded {
			if s.TextPart == nil {
				return nil, fmt.Errorf("Claude text part has no translated message identity")
			}
			s.TextPartIndex++
			payloads = append(payloads, map[string]any{
				"type": "response.content_part.added", "item_id": s.TextPart["item_id"], "output_index": s.TextPart["output_index"], "content_index": s.TextPartIndex,
				"part": part,
			})
		}
		if sourceEvent == "content_block_stop" {
			if s.TextPart == nil {
				return nil, fmt.Errorf("Claude text completion has no translated message identity")
			}
			payloads = append(payloads,
				map[string]any{"type": "response.output_text.done", "item_id": s.TextPart["item_id"], "output_index": s.TextPart["output_index"], "content_index": s.TextPartIndex, "text": block["text"]},
				map[string]any{"type": "response.content_part.done", "item_id": s.TextPart["item_id"], "output_index": s.TextPart["output_index"], "content_index": s.TextPartIndex,
					"part": part},
			)
		}
	}
	var out [][]byte
	for _, payload := range payloads {
		s.Sequence++
		payload["sequence_number"] = s.Sequence
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("encode restored Responses text lifecycle: %w", err)
		}
		out = append(out, responseSSEBytes(stringValue(payload["type"]), encoded))
	}
	return out, nil
}

func claudeFrameNeedsHostedSearchState(event string, data []byte) bool {
	if len(data) == 0 {
		return false
	}
	payload, err := decodeObject(data)
	if err != nil {
		return false
	}
	if event == "" {
		event = stringValue(payload["type"])
	}
	if event == "content_block_start" {
		block := objectValue(payload["content_block"])
		if stringValue(block["type"]) == "server_tool_use" || hasMeaningfulValue(block["citations"]) {
			return true
		}
	}
	if event == "content_block_delta" && stringValue(objectValue(payload["delta"])["type"]) == "citations_delta" {
		return true
	}
	return false
}

func (s *claudeHostedSearchStreamState) observe(event string, data []byte, done bool) error {
	if done {
		if len(s.Searches) > 0 && !s.Stopped {
			return fmt.Errorf("Claude hosted-search stream ended before message_stop")
		}
		return nil
	}
	if len(data) == 0 {
		return nil
	}
	payload, err := decodeObject(data)
	if err != nil {
		return fmt.Errorf("decode Claude hosted-search stream event")
	}
	if event == "" {
		event = stringValue(payload["type"])
	}
	if s.Stopped {
		return fmt.Errorf("Claude hosted-search stream continued after message_stop")
	}
	switch event {
	case "content_block_start":
		block, ok := payload["content_block"].(map[string]any)
		if !ok {
			return nil
		}
		index := intValue(payload["index"])
		if _, exists := s.Blocks[index]; exists {
			return fmt.Errorf("Claude stream reused a content block index")
		}
		typ := stringValue(block["type"])
		if s.PendingResult != "" && (typ != "web_search_tool_result" || rawStringValue(block["tool_use_id"]) != s.PendingResult) {
			return fmt.Errorf("Claude web search result is not adjacent to its call")
		}
		copyBlock := make(map[string]any, len(block))
		for key, value := range block {
			copyBlock[key] = value
		}
		s.Blocks[index] = copyBlock
		if s.TextBlockOpen {
			return fmt.Errorf("Claude stream started content before completing its text block")
		}
		switch typ {
		case "server_tool_use":
			if s.PendingResult != "" {
				return fmt.Errorf("Claude web search call has an unmatched previous result")
			}
			if err := rejectFields(block, "type", "id", "name", "input"); err != nil || stringValue(block["name"]) != "web_search" {
				return fmt.Errorf("Claude stream server tool block is unsupported")
			}
			id := rawStringValue(block["id"])
			if !validClaudeServerToolID(id) {
				return fmt.Errorf("Claude stream server tool ID is unsupported")
			}
			if _, exists := s.Searches[id]; exists {
				return fmt.Errorf("Claude stream web search call ID is ambiguous")
			}
			input, ok := block["input"].(map[string]any)
			if !ok || rejectFields(input, "query") != nil {
				return fmt.Errorf("Claude stream web search input has an unsupported shape")
			}
			call := &claudeHostedSearchStreamCall{ID: id, Part: map[string]any{"type": "server_tool_use", "id": id, "name": "web_search", "input": map[string]any{}}}
			if query, exists := input["query"]; exists {
				call.HasInitialQuery = true
				var okQuery bool
				call.InitialQuery, okQuery = query.(string)
				if !okQuery {
					return fmt.Errorf("Claude stream web search query has an unsupported value")
				}
			}
			if call.HasInitialQuery {
				call.Part["input"] = map[string]any{"query": call.InitialQuery}
			}
			s.Searches[id] = call
			s.SearchesByBlock[index] = call
			s.Content = append(s.Content, call.Part)
		case "web_search_tool_result":
			id := rawStringValue(block["tool_use_id"])
			call := s.Searches[id]
			if call == nil || !call.CallStopped || call.ResultSeen {
				return fmt.Errorf("Claude stream web search result has no unique matching call")
			}
			results, err := validateClaudeHostedSearchResult(block, id)
			if err != nil {
				return err
			}
			call.Results = results
			call.ResultSeen = true
			var content any
			if err := json.Unmarshal(results, &content); err != nil {
				return fmt.Errorf("decode Claude stream web search result")
			}
			s.Content = append(s.Content, map[string]any{"type": "web_search_tool_result", "tool_use_id": id, "content": content})
		default:
			if typ == "text" {
				if _, ok := block["text"].(string); !ok {
					return fmt.Errorf("Claude stream text block has an unsupported value")
				}
				copyBlock["text"] = rawStringValue(block["text"])
				s.TextBlockIndex = index
				s.TextBlockOpen = true
				s.Content = append(s.Content, copyBlock)
			} else {
				s.Content = append(s.Content, copyBlock)
			}
		}
	case "content_block_delta":
		index := intValue(payload["index"])
		delta := objectValue(payload["delta"])
		call := s.SearchesByBlock[index]
		if call != nil {
			if call.CallStopped {
				return fmt.Errorf("Claude stream changed a completed web search call")
			}
			if stringValue(delta["type"]) != "input_json_delta" {
				return fmt.Errorf("Claude stream web search input has an unsupported delta")
			}
			fragment := rawStringValue(delta["partial_json"])
			if fragment == "" {
				return nil
			}
			if call.HasInitialQuery {
				return fmt.Errorf("Claude stream web search query has ambiguous initial and delta values")
			}
			call.SawDelta = true
			call.Input.WriteString(fragment)
			return nil
		}
		block := s.Blocks[index]
		if block == nil {
			if stringValue(delta["type"]) == "citations_delta" {
				return fmt.Errorf("Claude stream citation has no text block")
			}
			return nil
		}
		if stringValue(block["type"]) == "text" && (!s.TextBlockOpen || s.TextBlockIndex != index) {
			return fmt.Errorf("Claude stream changed a completed text block")
		}
		switch stringValue(delta["type"]) {
		case "text_delta":
			if text, ok := block["text"].(string); ok {
				block["text"] = text + rawStringValue(delta["text"])
			}
		case "citations_delta":
			if stringValue(block["type"]) != "text" {
				return fmt.Errorf("Claude stream citation is not attached to text")
			}
			if err := validateClaudeCitation(delta["citation"]); err != nil {
				return err
			}
			citations := arrayValue(block["citations"])
			citations = append(citations, delta["citation"])
			block["citations"] = citations
		default:
			if stringValue(block["type"]) == "web_search_tool_result" {
				return fmt.Errorf("Claude stream web search result contains an unsupported delta")
			}
		}
	case "content_block_stop":
		index := intValue(payload["index"])
		if block := s.Blocks[index]; stringValue(block["type"]) == "web_search_tool_result" {
			call := s.Searches[rawStringValue(block["tool_use_id"])]
			if call == nil || !call.ResultSeen || call.ResultStopped {
				return fmt.Errorf("Claude stream completed a web search result without an open block")
			}
			call.ResultStopped = true
			s.PendingResult = ""
		}
		if stringValue(s.Blocks[index]["type"]) == "text" {
			if !s.TextBlockOpen || s.TextBlockIndex != index {
				return fmt.Errorf("Claude stream completed text without an open block")
			}
			s.TextBlockOpen = false
		}
		if call := s.SearchesByBlock[index]; call != nil {
			if call.CallStopped {
				return fmt.Errorf("Claude stream repeated a web search call stop")
			}
			if call.SawDelta {
				decoder := json.NewDecoder(strings.NewReader(call.Input.String()))
				start, err := decoder.Token()
				if err != nil || start != json.Delim('{') {
					return fmt.Errorf("Claude stream web search query JSON is incomplete or ambiguous")
				}
				key, err := decoder.Token()
				if err != nil || key != "query" {
					return fmt.Errorf("Claude stream web search query JSON is incomplete or ambiguous")
				}
				var query string
				if err := decoder.Decode(&query); err != nil {
					return fmt.Errorf("Claude stream web search query has an unsupported value")
				}
				end, err := decoder.Token()
				if err != nil || end != json.Delim('}') {
					return fmt.Errorf("Claude stream web search query JSON is incomplete or ambiguous")
				}
				if _, err := decoder.Token(); err != io.EOF {
					return fmt.Errorf("Claude stream web search query JSON has trailing data")
				}
				if strings.TrimSpace(query) == "" {
					return fmt.Errorf("Claude stream web search query is incomplete")
				}
				call.Query = query
			} else {
				call.Query = call.InitialQuery
			}
			if strings.TrimSpace(call.Query) == "" {
				return fmt.Errorf("Claude stream web search query is incomplete")
			}
			call.Part["input"] = map[string]any{"query": call.Query}
			call.CallStopped = true
			s.PendingResult = call.ID
		}
	case "message_stop":
		if s.TextBlockOpen {
			return fmt.Errorf("Claude stream stopped before completing its text block")
		}
		if s.PendingResult != "" {
			return fmt.Errorf("Claude web search call has no result")
		}
		for _, call := range s.Searches {
			if !call.CallStopped || !call.ResultStopped || call.Query == "" {
				return fmt.Errorf("Claude web search call/result pair is incomplete")
			}
		}
		s.Stopped = true
	}
	return nil
}

func (s *claudeHostedSearchStreamState) restore(frame []byte) ([]byte, error) {
	event, data, done, err := parseSSEFrame(frame)
	if err != nil || done || len(data) == 0 {
		return frame, err
	}
	payload, err := decodeObject(data)
	if err != nil {
		return nil, fmt.Errorf("decode translated Responses stream event")
	}
	if event == "" {
		event = stringValue(payload["type"])
	}
	if event == "response.output_item.added" {
		item := objectValue(payload["item"])
		if stringValue(item["type"]) == "web_search_call" {
			call := s.Searches[strings.TrimPrefix(rawStringValue(item["id"]), "ws_")]
			if call == nil {
				return nil, fmt.Errorf("translated Responses search addition has no Claude call")
			}
			if call.HasInitialQuery {
				data, err = sjson.SetBytes(data, "item.action.query", call.InitialQuery)
				if err != nil {
					return nil, fmt.Errorf("restore initial Claude hosted search query")
				}
			}
		}
	}
	if event == "response.output_item.done" {
		item := objectValue(payload["item"])
		if stringValue(item["type"]) == "web_search_call" {
			updated, err := restoreClaudeStreamSearchItem(data, "item", item, s.Searches)
			if err != nil {
				return nil, err
			}
			data = updated
		} else if stringValue(item["type"]) == "message" {
			_, groups := claudeHostedSearchContent(map[string]any{"content": s.Content})
			var messageGroups []claudeCitationGroup
			if s.TextGroupsDone < len(groups) {
				run := groups[s.TextGroupsDone].Run
				for s.TextGroupsDone < len(groups) && groups[s.TextGroupsDone].Run == run {
					messageGroups = append(messageGroups, groups[s.TextGroupsDone])
					s.TextGroupsDone++
				}
			}
			if err := restoreClaudeResponseCitations([]any{item}, messageGroups); err != nil {
				return nil, err
			}
			data, err = sjson.SetBytes(data, "item", item)
			if err != nil {
				return nil, fmt.Errorf("restore Claude text boundaries in Responses stream item")
			}
		}
	}
	if event == "response.completed" || event == "response.incomplete" {
		responseBytes, err := json.Marshal(objectValue(payload["response"]))
		if err != nil {
			return nil, fmt.Errorf("encode translated Responses stream completion")
		}
		restored, err := restoreClaudeHostedSearchResponse(map[string]any{"content": s.Content}, responseBytes)
		if err != nil {
			return nil, err
		}
		data, err = sjson.SetRawBytes(data, "response", restored)
		if err != nil {
			return nil, fmt.Errorf("restore Claude hosted search in Responses stream")
		}
	}
	return responseSSEBytes(event, data), nil
}

func restoreClaudeStreamSearchItem(data []byte, path string, item map[string]any, searches map[string]*claudeHostedSearchStreamCall) ([]byte, error) {
	responsesID := rawStringValue(item["id"])
	callID := strings.TrimPrefix(responsesID, "ws_")
	call := searches[callID]
	if call == nil || !call.ResultStopped || call.Query == "" {
		return nil, fmt.Errorf("translated Responses search item has no complete Claude pair")
	}
	updated, err := sjson.SetBytes(data, path+".action.query", call.Query)
	if err != nil {
		return nil, fmt.Errorf("restore Claude hosted search query")
	}
	updated, err = sjson.SetRawBytes(updated, path+".results", call.Results)
	if err != nil {
		return nil, fmt.Errorf("restore Claude hosted search results")
	}
	return updated, nil
}

func intValue(value any) int {
	switch number := value.(type) {
	case json.Number:
		parsed, _ := number.Int64()
		return int(parsed)
	case float64:
		return int(number)
	case int:
		return number
	case int64:
		return int(number)
	default:
		return 0
	}
}
