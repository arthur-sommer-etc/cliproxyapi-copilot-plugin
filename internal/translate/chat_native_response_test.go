package translate

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/sse"
	"github.com/tidwall/gjson"
)

func TestNativeChatResponseCompletesMissingObject(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile("testdata/live-matrix-gemini-chat/upstream-response.json")
	if err != nil {
		t.Fatal(err)
	}
	request, err := os.ReadFile("testdata/live-matrix-gemini-chat/client-request.json")
	if err != nil {
		t.Fatal(err)
	}
	translated, err := os.ReadFile("testdata/live-matrix-gemini-chat/upstream-request.json")
	if err != nil {
		t.Fatal(err)
	}
	out, err := ResponseFromEndpoint(context.Background(), EndpointChatCompletions, "openai", "gemini-3.8-flash", request, translated, body)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(out, "object").String() != "chat.completion" {
		t.Fatalf("missing Chat response object: %s", out)
	}
	for _, field := range []string{"id", "created", "model", "choices", "usage", "copilot_usage"} {
		if gjson.GetBytes(out, field).Raw != gjson.GetBytes(body, field).Raw {
			t.Fatalf("normalization changed %s", field)
		}
	}
}

func TestNativeChatResponsePreservesExistingEnvelopeAndModel(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`{"object":"chat.completion","model":"provider-canonical-model","choices":[]}`,
		`{"object":"unexpected-object","model":"provider-canonical-model","choices":[]}`,
		`{"object":null,"model":"provider-canonical-model","choices":[]}`,
		`{"error":{"message":"fixture error"}}`,
		`{"model":"provider-canonical-model","choices":null}`,
	} {
		out, err := ResponseFromEndpoint(context.Background(), EndpointChatCompletions, "openai", "requested-model", nil, nil, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != body {
			t.Fatalf("response envelope or identity changed: %s", out)
		}
	}
}

func TestNativeChatStreamCompletesMissingObject(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile("testdata/live-matrix-gemini-chat-stream/upstream-response.sse")
	if err != nil {
		t.Fatal(err)
	}
	var state any
	chunks, terminated := 0, false
	decoder := &sse.Decoder{}
	for _, frame := range decoder.Feed(body) {
		_, original, done, err := parseSSEFrame(frame)
		if err != nil {
			t.Fatal(err)
		}
		out, err := StreamFromEndpoint(context.Background(), EndpointChatCompletions, "openai", "gemini-3.8-flash", nil, nil, frame, &state)
		if err != nil || len(out) != 1 {
			t.Fatalf("translate native Chat frame: count=%d err=%v", len(out), err)
		}
		if done {
			terminated = true
			if !bytes.Equal(out[0], frame) {
				t.Fatal("terminal frame changed")
			}
			continue
		}
		chunks++
		_, normalized, _, err := parseSSEFrame(out[0])
		if err != nil {
			t.Fatal(err)
		}
		if gjson.GetBytes(normalized, "object").String() != "chat.completion.chunk" {
			t.Fatal("stream chunk object missing")
		}
		for _, key := range []string{"id", "model", "created", "choices", "usage", "copilot_usage"} {
			if gjson.GetBytes(normalized, key).Raw != gjson.GetBytes(original, key).Raw {
				t.Fatalf("normalization changed %s", key)
			}
		}
	}
	if chunks < 2 || !terminated {
		t.Fatalf("native stream fixture chunks=%d terminated=%t", chunks, terminated)
	}
}
