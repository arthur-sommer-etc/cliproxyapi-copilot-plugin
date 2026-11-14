package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/sse"
	"github.com/tidwall/gjson"
)

func TestCapturedCopilotReasoningOnlyIncompleteExchange(t *testing.T) {
	t.Parallel()
	const fixture = "live-incomplete-gpt-responses"
	original := readLiveBodyFixture(t, fixture, "client-request.json")
	request := readLiveBodyFixture(t, fixture, "upstream-request.json")
	for _, body := range [][]byte{original, request} {
		if gjson.GetBytes(body, "model").String() != "gpt-6-luna" || gjson.GetBytes(body, "max_output_tokens").Int() != 16 || !gjson.GetBytes(body, "stream").Bool() {
			t.Fatal("captured request lost its exact model or token limit")
		}
	}
	decoder := &sse.Decoder{}
	frames := decoder.Feed(readLiveBodyFixture(t, fixture, "upstream-response.sse"))
	client := readLiveBodyFixture(t, fixture, "client-response.sse")
	if len(frames) != 5 {
		t.Fatal("captured incomplete exchange lost stream events")
	}
	var nativeState any
	var native bytes.Buffer
	for _, frame := range frames {
		out, err := StreamFromEndpoint(context.Background(), EndpointResponses, "openai-response", "gpt-6-luna", original, request, frame, &nativeState)
		if err != nil {
			t.Fatal(err)
		}
		for _, output := range out {
			native.Write(output)
		}
	}
	if !bytes.Equal(native.Bytes(), client) {
		t.Fatal("native incomplete stream differs from the captured client response")
	}
	_, terminal, _, err := parseSSEFrame(frames[len(frames)-1])
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(terminal, "type").String() != "response.incomplete" || gjson.GetBytes(terminal, "response.output.0.type").String() != "reasoning" || gjson.GetBytes(terminal, "response.usage.output_tokens_details.reasoning_tokens").Int() != 16 {
		t.Fatal("captured reasoning-only incomplete terminal changed")
	}
	for _, destination := range []string{"openai", "claude"} {
		t.Run(destination, func(t *testing.T) {
			clientRequest, err := json.Marshal(map[string]any{
				"model": "gpt-6-luna", "stream": true, "max_tokens": 16,
				"messages": []any{map[string]any{"role": "user", "content": gjson.GetBytes(original, "input").String()}},
			})
			if err != nil {
				t.Fatal(err)
			}
			var state any
			stops := 0
			for _, frame := range frames {
				out, err := StreamFromEndpoint(context.Background(), EndpointResponses, destination, "gpt-6-luna", clientRequest, request, frame, &state)
				if err != nil {
					t.Fatal(err)
				}
				for _, output := range out {
					data := output
					if destination == "claude" {
						_, data, _, err = parseSSEFrame(output)
						if err != nil {
							t.Fatal(err)
						}
					}
					payload := gjson.ParseBytes(data)
					if destination == "claude" && payload.Get("type").String() == "message_delta" {
						if payload.Get("delta.stop_reason").String() != "max_tokens" || payload.Get("usage.input_tokens").Int() != 24 || payload.Get("usage.output_tokens").Int() != 16 {
							t.Fatal("captured partial Messages termination or usage changed")
						}
						stops++
					}
					if destination == "openai" && payload.Get("choices.0.finish_reason").String() != "" {
						if payload.Get("choices.0.finish_reason").String() != "length" || payload.Get("usage.prompt_tokens").Int() != 24 || payload.Get("usage.completion_tokens").Int() != 16 || payload.Get("usage.completion_tokens_details.reasoning_tokens").Int() != 16 {
							t.Fatal("captured partial Chat termination or usage changed")
						}
						stops++
					}
					if payload.Get("delta.text").String() != "" || payload.Get("choices.0.delta.content").String() != "" {
						t.Fatal("reasoning-only incomplete response fabricated visible text")
					}
				}
			}
			if stops != 1 {
				t.Fatalf("captured incomplete stream emitted %d terminals", stops)
			}
		})
	}
}
