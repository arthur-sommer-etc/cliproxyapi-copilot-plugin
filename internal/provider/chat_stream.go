package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
)

func chatClientChunk(payload []byte) ([]byte, bool, error) {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return nil, false, nil
	}
	if !bytes.HasPrefix(trimmed, []byte("data:")) && !bytes.Contains(trimmed, []byte("\ndata:")) {
		if !json.Valid(trimmed) {
			return nil, false, fmt.Errorf("translated Chat stream chunk is not valid JSON")
		}
		return append([]byte(nil), trimmed...), true, nil
	}
	var data []byte
	for _, line := range bytes.Split(trimmed, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if bytes.HasPrefix(line, []byte("data:")) {
			value := line[len("data:"):]
			if len(value) > 0 && value[0] == ' ' {
				value = value[1:]
			}
			if data != nil {
				data = append(data, '\n')
			}
			data = append(data, value...)
		}
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return nil, false, nil
	}
	if !json.Valid(data) {
		return nil, false, fmt.Errorf("translated Chat stream event is not valid JSON")
	}
	return data, true, nil
}
