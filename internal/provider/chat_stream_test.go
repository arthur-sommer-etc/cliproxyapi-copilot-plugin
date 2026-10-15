package provider

import (
	"bytes"
	"testing"
)

func TestChatClientChunkUnframesOpenAISSE(t *testing.T) {
	t.Parallel()
	payload := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"answer\"}}]}\n\n")
	got, emit, err := chatClientChunk(payload)
	if err != nil {
		t.Fatalf("unwrap Chat SSE: %v", err)
	}
	if !emit || !bytes.Equal(got, []byte(`{"choices":[{"delta":{"content":"answer"}}]}`)) {
		t.Fatalf("chunk = %s, emit=%v", got, emit)
	}
}

func TestChatClientChunkKeepsJSONAndSuppressesDone(t *testing.T) {
	t.Parallel()
	jsonChunk := []byte(`{"choices":[{"delta":{"content":"answer"}}]}`)
	got, emit, err := chatClientChunk(jsonChunk)
	if err != nil || !emit || !bytes.Equal(got, jsonChunk) {
		t.Fatalf("JSON chunk = %s, emit=%v, err=%v", got, emit, err)
	}
	got, emit, err = chatClientChunk([]byte("data: [DONE]\n\n"))
	if err != nil || emit || len(got) != 0 {
		t.Fatalf("terminal chunk = %s, emit=%v, err=%v", got, emit, err)
	}
}

func TestChatClientChunkRejectsInvalidEventData(t *testing.T) {
	t.Parallel()
	if _, _, err := chatClientChunk([]byte("data: data: {\"choices\":[]}\n\n")); err == nil {
		t.Fatal("accepted a double-framed Chat event")
	}
}
