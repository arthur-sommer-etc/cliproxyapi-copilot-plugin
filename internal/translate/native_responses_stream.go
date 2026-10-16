package translate

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const nativeWebSearchBufferLimit = 8 << 20

type nativeResponsesWebSearchState struct {
	Frames         [][]byte
	Bytes          int
	DoneIDs        map[int]string
	FinalStatuses  map[int]string
	WholeLifecycle bool
}

// Copilot's hosted search responses change opaque response and item IDs between
// phases. Declared search requests hold the whole lifecycle until the terminal
// snapshot supplies authoritative identities, without changing its replay data.
func nativeResponsesStream(frame []byte, state *any, wholeLifecycle bool) ([][]byte, error) {
	event, data, done, err := parseSSEFrame(frame)
	if err != nil {
		return nil, err
	}
	if event == "" {
		event = gjson.GetBytes(data, "type").String()
	}
	var pending *nativeResponsesWebSearchState
	if state != nil {
		pending, _ = (*state).(*nativeResponsesWebSearchState)
	}
	isSearchItem := gjson.GetBytes(data, "item.type").String() == "web_search_call"
	if pending == nil && !wholeLifecycle && !isSearchItem && !strings.HasPrefix(event, "response.web_search_call.") {
		return [][]byte{append([]byte(nil), frame...)}, nil
	}
	if state == nil {
		return nil, fmt.Errorf("native Responses web search stream requires state")
	}
	if pending == nil {
		pending = &nativeResponsesWebSearchState{DoneIDs: make(map[int]string), FinalStatuses: make(map[int]string), WholeLifecycle: wholeLifecycle}
		*state = pending
	}
	if done || event == "response.failed" || event == "error" {
		return nil, fmt.Errorf("native Responses web search stream ended without successful completion")
	}
	if isSearchItem || strings.HasPrefix(event, "response.web_search_call.") {
		indexValue := gjson.GetBytes(data, "output_index")
		index, err := strconv.Atoi(indexValue.Raw)
		if indexValue.Type != gjson.Number || err != nil || index < 0 {
			return nil, fmt.Errorf("native Responses web search event has an invalid output index")
		}
		if _, exists := pending.DoneIDs[index]; !exists {
			pending.DoneIDs[index] = ""
		}
		if event == "response.web_search_call.completed" || event == "response.web_search_call.failed" {
			status := strings.TrimPrefix(event, "response.web_search_call.")
			if finalStatus := pending.FinalStatuses[index]; finalStatus != "" && finalStatus != status {
				return nil, fmt.Errorf("native Responses web search event conflicts with its terminal lifecycle")
			}
			pending.FinalStatuses[index] = status
		}
		if event == "response.output_item.done" {
			id := gjson.GetBytes(data, "item.id")
			status := gjson.GetBytes(data, "item.status").String()
			if id.Type != gjson.String || id.String() == "" {
				return nil, fmt.Errorf("native Responses web search done item is incomplete")
			}
			switch status {
			case "completed", "in_progress", "searching", "failed":
			default:
				return nil, fmt.Errorf("native Responses web search done item has an invalid status")
			}
			if finalStatus := pending.FinalStatuses[index]; finalStatus != "" && finalStatus != status {
				return nil, fmt.Errorf("native Responses web search done item conflicts with its terminal lifecycle")
			}
			pending.DoneIDs[index] = id.String()
			pending.FinalStatuses[index] = status
		}
	}
	if len(frame) > nativeWebSearchBufferLimit-pending.Bytes {
		return nil, fmt.Errorf("native Responses web search stream exceeds the pending event limit")
	}
	pending.Frames = append(pending.Frames, append([]byte(nil), frame...))
	pending.Bytes += len(frame)
	if event != "response.completed" && event != "response.incomplete" {
		return nil, nil
	}
	incomplete := event == "response.incomplete"
	responseStatus := gjson.GetBytes(data, "response.status").String()
	if (!incomplete && responseStatus != "completed") || (incomplete && responseStatus != "incomplete") {
		return nil, fmt.Errorf("native Responses web search terminal status does not match its event")
	}
	if incomplete {
		if reason := gjson.GetBytes(data, "response.incomplete_details.reason"); reason.Type != gjson.String || strings.TrimSpace(reason.String()) == "" {
			return nil, fmt.Errorf("native Responses web search incomplete snapshot has no termination reason")
		}
		for _, path := range []string{"error", "response.error"} {
			if value := gjson.GetBytes(data, path); value.Exists() && value.Type != gjson.Null {
				return nil, fmt.Errorf("native Responses web search incomplete snapshot contains an error")
			}
		}
	}
	finalIDs := make(map[int]string, len(pending.DoneIDs))
	for index, doneID := range pending.DoneIDs {
		item := gjson.GetBytes(data, fmt.Sprintf("response.output.%d", index))
		id := item.Get("id")
		status := item.Get("status").String()
		if item.Get("type").String() != "web_search_call" || id.Type != gjson.String || id.String() == "" || (!incomplete && (doneID == "" || status != "completed")) {
			return nil, fmt.Errorf("native Responses completed without the completed web search item at output index %d", index)
		}
		if finalStatus := pending.FinalStatuses[index]; finalStatus != "" && finalStatus != status {
			return nil, fmt.Errorf("native Responses web search item conflicts with its terminal lifecycle at output index %d", index)
		}
		if incomplete {
			switch status {
			case "completed", "in_progress", "searching", "failed":
			default:
				return nil, fmt.Errorf("native Responses incomplete web search item has an invalid status at output index %d", index)
			}
		}
		finalIDs[index] = id.String()
	}
	responseID := gjson.GetBytes(data, "response.id")
	if pending.WholeLifecycle {
		if responseID.Type != gjson.String || responseID.String() == "" {
			return nil, fmt.Errorf("native Responses hosted-tool terminal has no response identity")
		}
		for index, item := range gjson.GetBytes(data, "response.output").Array() {
			id := item.Get("id")
			if id.Type != gjson.String || id.String() == "" {
				return nil, fmt.Errorf("native Responses hosted-tool terminal has no output identity at index %d", index)
			}
			finalIDs[index] = id.String()
		}
	}
	out := make([][]byte, 0, len(pending.Frames))
	for _, buffered := range pending.Frames {
		name, payload, _, err := parseSSEFrame(buffered)
		if err != nil {
			return nil, err
		}
		if len(payload) == 0 {
			out = append(out, buffered)
			continue
		}
		kind := gjson.GetBytes(payload, "type").String()
		if kind == "response.completed" || kind == "response.incomplete" {
			out = append(out, buffered)
			continue
		}
		root, err := decodeObject(payload)
		if err != nil {
			return nil, err
		}
		changed := false
		if pending.WholeLifecycle && gjson.GetBytes(payload, "response.id").Exists() {
			payload, err = sjson.SetBytes(payload, "response.id", responseID.String())
			if err != nil {
				return nil, fmt.Errorf("retain terminal native response identity: %w", err)
			}
			changed = true
		}
		index, err := strconv.Atoi(numberKey(root["output_index"]))
		if pending.WholeLifecycle && gjson.GetBytes(payload, "output_index").Exists() {
			if gjson.GetBytes(payload, "output_index").Type != gjson.Number || err != nil || index < 0 || finalIDs[index] == "" {
				return nil, fmt.Errorf("native Responses event has no terminal output index")
			}
			if itemType := gjson.GetBytes(payload, "item.type").String(); itemType != "" && itemType != gjson.GetBytes(data, fmt.Sprintf("response.output.%d.type", index)).String() {
				return nil, fmt.Errorf("native Responses event conflicts with its terminal output type")
			}
		}
		if err == nil {
			if id, exists := finalIDs[index]; exists {
				path := ""
				if gjson.GetBytes(payload, "item.type").String() == "web_search_call" || pending.WholeLifecycle && gjson.GetBytes(payload, "item.id").Exists() {
					path = "item.id"
				} else if strings.HasPrefix(kind, "response.web_search_call.") || pending.WholeLifecycle && gjson.GetBytes(payload, "item_id").Exists() {
					path = "item_id"
				}
				if path != "" {
					payload, err = sjson.SetBytes(payload, path, id)
					if err != nil {
						return nil, fmt.Errorf("retain terminal native web search identity: %w", err)
					}
					changed = true
				}
			}
		}
		if changed {
			buffered = responseSSEBytes(name, payload)
		}
		out = append(out, buffered)
	}
	*state = nil
	return out, nil
}
