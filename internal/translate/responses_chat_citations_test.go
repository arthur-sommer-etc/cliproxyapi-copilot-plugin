package translate

import (
	"context"
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestResponsesURLCitationsToChat(t *testing.T) {
	t.Parallel()
	response := []byte(`{"id":"resp_1","object":"response","status":"completed","model":"gpt-test","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"\u00e9cho ","annotations":[]},{"type":"output_text","text":"https://example.test/path?q=a%2Fb&lang=en","annotations":[{"type":"url_citation","start_index":0,"end_index":40,"title":"Example title","url":"https://example.test/path?q=a%2Fb&lang=en"}]}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`)
	out, err := ResponseFromEndpoint(context.Background(), EndpointResponses, "openai", "gpt-test", []byte(`{}`), []byte(`{}`), response)
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(out, "choices.0.message.content").String(); got != "\u00e9cho https://example.test/path?q=a%2Fb&lang=en" {
		t.Fatalf("Chat text = %q", got)
	}
	annotation := gjson.GetBytes(out, "choices.0.message.annotations.0")
	if !annotation.Exists() || annotation.Get("type").String() != "url_citation" ||
		annotation.Get("url_citation.url").String() != "https://example.test/path?q=a%2Fb&lang=en" ||
		annotation.Get("url_citation.title").String() != "Example title" ||
		annotation.Get("url_citation.start_index").Int() != 5 || annotation.Get("url_citation.end_index").Int() != 45 {
		t.Fatalf("Chat URL citation was not preserved: %s", out)
	}
}

func TestResponsesURLCitationValidationAndStreamingGuard(t *testing.T) {
	t.Parallel()
	base := `{"id":"resp_1","object":"response","status":"completed","model":"gpt-test","output":[{"type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hello","annotations":[%s]}]}]}`
	for name, citation := range map[string]string{
		"out of bounds":           `{"type":"url_citation","start_index":0,"end_index":6,"title":"title","url":"https://example.test"}`,
		"reversed range":          `{"type":"url_citation","start_index":2,"end_index":1,"title":"title","url":"https://example.test"}`,
		"non-integer range":       `{"type":"url_citation","start_index":0.5,"end_index":1,"title":"title","url":"https://example.test"}`,
		"unknown citation field":  `{"type":"url_citation","start_index":0,"end_index":1,"title":"title","url":"https://example.test","extra":"must not drop"}`,
		"unknown annotation type": `{"type":"other","start_index":0,"end_index":1,"title":"title","url":"https://example.test"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ResponseFromEndpoint(context.Background(), EndpointResponses, "openai", "gpt-test", []byte(`{}`), []byte(`{}`), []byte(fmt.Sprintf(base, citation))); err == nil {
				t.Fatal("invalid citation was accepted")
			}
		})
	}
	frame := []byte("event: response.output_text.annotation.added\ndata: {\"type\":\"response.output_text.annotation.added\",\"annotation\":{\"type\":\"url_citation\",\"start_index\":0,\"end_index\":1,\"title\":\"title\",\"url\":\"https://example.test\"}}\n\n")
	var state any
	if _, err := StreamFromEndpoint(context.Background(), EndpointResponses, "openai", "gpt-test", []byte(`{}`), []byte(`{}`), frame, &state); err == nil {
		t.Fatal("streamed URL citation was silently dropped")
	}
	failedFrame := []byte("event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\"}}\n\n")
	if _, err := StreamFromEndpoint(context.Background(), EndpointResponses, "openai", "gpt-test", []byte(`{}`), []byte(`{}`), failedFrame, &state); err == nil {
		t.Fatal("failed Responses event was accepted")
	}
}

func TestNativeResponsesCitationPassthroughUnchanged(t *testing.T) {
	t.Parallel()
	body := []byte(`{"id":"resp_1","output":[{"type":"message","content":[{"type":"output_text","text":"linked","annotations":[{"type":"url_citation","start_index":0,"end_index":5,"title":"title","url":"https://example.test/?a=1&b=2"}]}]}]}`)
	out, err := ResponseFromEndpoint(context.Background(), EndpointResponses, "openai-response", "gpt-test", nil, nil, body)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(body) {
		t.Fatalf("native Responses passthrough changed citation data: %s", out)
	}
}

func TestResponsesCitationTextBoundaries(t *testing.T) {
	t.Parallel()
	for name, parts := range map[string]string{
		"outer whitespace": `[{"type":"output_text","text":" \u00e9 ","annotations":[{"type":"url_citation","start_index":1,"end_index":3,"title":" title ","url":"https://example.test/?q=a%2Fb"}]}]`,
		"part boundary":    `[{"type":"output_text","text":" \u00e9 ","annotations":[]},{"type":"output_text","text":"tail ","annotations":[{"type":"url_citation","start_index":0,"end_index":5,"title":" title ","url":"https://example.test/?q=a%2Fb"}]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			body := []byte(`{"id":"r","status":"completed","output":[{"type":"message","role":"assistant","content":` + parts + `}]}`)
			out, err := ResponseFromEndpoint(context.Background(), EndpointResponses, "openai", "gpt-test", []byte(`{}`), []byte(`{}`), body)
			if err != nil {
				t.Fatal(err)
			}
			wantText, wantStart, wantEnd := " \u00e9 ", int64(1), int64(3)
			if name == "part boundary" {
				wantText, wantStart, wantEnd = " \u00e9 tail ", 3, 8
			}
			message := gjson.GetBytes(out, "choices.0.message")
			citation := message.Get("annotations.0.url_citation")
			if message.Get("content").String() != wantText || citation.Get("start_index").Int() != wantStart || citation.Get("end_index").Int() != wantEnd || citation.Get("title").String() != " title " {
				t.Fatalf("text or citation boundary changed: %s", out)
			}
		})
	}
}

func TestResponsesUncitedTextWhitespace(t *testing.T) {
	t.Parallel()
	body := []byte(`{"id":"r","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":" leading "},{"type":"refusal","refusal":" refusal "},{"type":"output_text","text":" trailing "}]}]}`)
	for _, format := range []string{"openai", "claude"} {
		t.Run(format, func(t *testing.T) {
			out, err := ResponseFromEndpoint(context.Background(), EndpointResponses, format, "gpt-test", []byte(`{}`), []byte(`{}`), body)
			if err != nil {
				t.Fatal(err)
			}
			text := gjson.GetBytes(out, "choices.0.message.content").String()
			if format == "claude" {
				text = ""
				for _, part := range gjson.GetBytes(out, "content").Array() {
					text += part.Get("text").String()
				}
			}
			if text != " leading  refusal  trailing " {
				t.Fatalf("text whitespace changed: %q", text)
			}
		})
	}
}
