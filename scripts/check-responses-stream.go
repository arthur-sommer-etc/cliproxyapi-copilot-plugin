package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/sse"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
)

func data(frame []byte) map[string]any {
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(string(frame), "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "data:") {
			lines = append(lines, strings.TrimSpace(line[5:]))
		}
	}
	var event map[string]any
	if json.Unmarshal([]byte(strings.Join(lines, "\n")), &event) == nil {
		return event
	}
	return nil
}

func removeItemIDs(event map[string]any) {
	delete(event, "item_id")
	if item, ok := event["item"].(map[string]any); ok {
		delete(item, "id")
	}
	if response, ok := event["response"].(map[string]any); ok {
		if output, ok := response["output"].([]any); ok {
			for _, value := range output {
				if item, ok := value.(map[string]any); ok {
					delete(item, "id")
				}
			}
		}
	}
}

func run(raw []byte) error {
	var decoder sse.Decoder
	var state any
	ids := make(map[float64]string)
	frames := 0
	completed := false
	for start := 0; start < len(raw); start += 17 {
		end := min(start+17, len(raw))
		for _, frame := range decoder.Feed(raw[start:end]) {
			out, err := translate.StreamFromEndpoint(context.Background(), translate.EndpointResponses, "openai-response", "check", nil, nil, frame, &state)
			if err != nil || len(out) != 1 {
				return fmt.Errorf("translate stream: %v", err)
			}
			before, after := data(frame), data(out[0])
			if before == nil {
				if after != nil || string(frame) != string(out[0]) {
					return fmt.Errorf("changed non-JSON SSE frame")
				}
				continue
			}
			if after == nil {
				return fmt.Errorf("lost SSE data framing")
			}
			frames++
			if index, ok := after["output_index"].(float64); ok {
				id, _ := after["item_id"].(string)
				if item, ok := after["item"].(map[string]any); ok && id == "" {
					id, _ = item["id"].(string)
				}
				if first, ok := ids[index]; id != "" && ok && id != first {
					return fmt.Errorf("output item %g has different IDs", index)
				} else if id != "" && !ok {
					ids[index] = id
				}
			}
			if response, ok := after["response"].(map[string]any); ok {
				if output, ok := response["output"].([]any); ok {
					for i, value := range output {
						if item, ok := value.(map[string]any); ok {
							if id, ok := item["id"].(string); ok && ids[float64(i)] != "" && id != ids[float64(i)] {
								return fmt.Errorf("completed output item %d has a different ID", i)
							}
						}
					}
				}
			}
			completed = completed || after["type"] == "response.completed"
			removeItemIDs(before)
			removeItemIDs(after)
			if !reflect.DeepEqual(before, after) {
				return fmt.Errorf("changed non-ID event data")
			}
		}
	}
	if frames == 0 || len(ids) == 0 || !completed {
		return fmt.Errorf("stream has no completed output items")
	}
	fmt.Printf("%d events, %d output items: IDs stable and other data preserved\n", frames, len(ids))
	return nil
}

func main() {
	raw, err := io.ReadAll(os.Stdin)
	if err == nil {
		err = run(raw)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
