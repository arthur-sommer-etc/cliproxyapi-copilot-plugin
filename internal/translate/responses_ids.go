package translate

import (
	"bytes"
	"encoding/json"
)

// Copilot can assign a different opaque ID to each event for the same item.
type responsesStreamState struct {
	itemIDs     map[int64]string
	translation any
}

func (s *responsesStreamState) normalize(frame []byte) []byte {
	_, data, done, err := parseSSEFrame(frame)
	if err != nil || done || len(data) == 0 {
		return frame
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(data, &payload) != nil {
		return frame
	}
	changed := false
	var index int64
	if raw, ok := payload["output_index"]; ok && json.Unmarshal(raw, &index) == nil && index >= 0 {
		var item map[string]json.RawMessage
		if json.Unmarshal(payload["item"], &item) == nil && item != nil {
			if s.pin(item, "id", index) {
				payload["item"], _ = json.Marshal(item)
				changed = true
			}
		}
		changed = s.pin(payload, "item_id", index) || changed
	}
	var response map[string]json.RawMessage
	if json.Unmarshal(payload["response"], &response) == nil && response != nil {
		var output []map[string]json.RawMessage
		if json.Unmarshal(response["output"], &output) == nil {
			outputChanged := false
			for i, item := range output {
				if _, ok := s.itemIDs[int64(i)]; ok && s.pin(item, "id", int64(i)) {
					outputChanged = true
				}
			}
			if outputChanged {
				response["output"], _ = json.Marshal(output)
				payload["response"], _ = json.Marshal(response)
				changed = true
			}
		}
	}
	if !changed {
		return frame
	}
	data, _ = json.Marshal(payload)
	lines := bytes.Split(frame, []byte("\n"))
	written := false
	out := make([][]byte, 0, len(lines))
	for _, line := range lines {
		if bytes.HasPrefix(line, []byte("data:")) {
			if written {
				continue
			}
			written = true
			ending := []byte(nil)
			if bytes.HasSuffix(line, []byte("\r")) {
				ending = []byte("\r")
			}
			line = append(append([]byte("data: "), data...), ending...)
		}
		out = append(out, line)
	}
	return bytes.Join(out, []byte("\n"))
}

func (s *responsesStreamState) pin(object map[string]json.RawMessage, field string, index int64) bool {
	var id string
	if json.Unmarshal(object[field], &id) != nil || id == "" {
		return false
	}
	first, ok := s.itemIDs[index]
	if !ok {
		s.itemIDs[index] = id
		return false
	}
	if id == first {
		return false
	}
	object[field], _ = json.Marshal(first)
	return true
}
