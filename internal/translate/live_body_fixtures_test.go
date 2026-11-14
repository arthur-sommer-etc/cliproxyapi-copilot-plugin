package translate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/sse"
	"github.com/tidwall/gjson"
)

func TestCapturedNativeChatFourBodyRoundTrip(t *testing.T) {
	t.Parallel()
	for _, stream := range []bool{false, true} {
		name := "live-matrix-gemini-chat"
		responseName := "response.json"
		if stream {
			name += "-stream"
			responseName = "response.sse"
		}
		t.Run(name, func(t *testing.T) {
			original := readLiveBodyFixture(t, name, "client-request.json")
			upstreamRequest := readLiveBodyFixture(t, name, "upstream-request.json")
			upstreamResponse := readLiveBodyFixture(t, name, "upstream-"+responseName)
			clientResponse := readLiveBodyFixture(t, name, "client-"+responseName)
			model := gjson.GetBytes(original, "model").String()
			translated, err := RequestForEndpointFrom("openai", model, original, stream, EndpointChatCompletions)
			if err != nil {
				t.Fatal(err)
			}
			requireLiveBodyJSONEqual(t, translated, upstreamRequest)
			if !stream {
				response, err := ResponseFromEndpoint(context.Background(), EndpointChatCompletions, "openai", model, original, translated, upstreamResponse)
				if err != nil {
					t.Fatal(err)
				}
				requireLiveBodyJSONEqual(t, response, clientResponse)
				return
			}
			decoder := &sse.Decoder{}
			var state any
			var response []byte
			for _, frame := range decoder.Feed(upstreamResponse) {
				frames, err := StreamFromEndpoint(context.Background(), EndpointChatCompletions, "openai", model, original, translated, frame, &state)
				if err != nil {
					t.Fatal(err)
				}
				for _, translatedFrame := range frames {
					response = append(response, translatedFrame...)
				}
			}
			actualEvents, actualDone := liveBodySSEEvents(t, response)
			expectedEvents, expectedDone := liveBodySSEEvents(t, clientResponse)
			if !reflect.DeepEqual(actualEvents, expectedEvents) || actualDone != expectedDone {
				t.Fatal("captured native Chat stream client body changed")
			}
		})
	}
}

func readLiveBodyFixture(t *testing.T, folder, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", folder, name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func requireLiveBodyJSONEqual(t *testing.T, actual, expected []byte) {
	t.Helper()
	var got, want any
	if json.Unmarshal(actual, &got) != nil || json.Unmarshal(expected, &want) != nil {
		t.Fatal("captured body or translation is invalid JSON")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("captured body differs: got=%s want=%s", actual, expected)
	}
}

func liveBodySSEEvents(t *testing.T, body []byte) ([]any, bool) {
	t.Helper()
	decoder := &sse.Decoder{}
	var events []any
	terminated := false
	for _, frame := range decoder.Feed(body) {
		_, data, done, err := parseSSEFrame(frame)
		if err != nil {
			t.Fatal(err)
		}
		if done {
			terminated = true
			continue
		}
		var event any
		if json.Unmarshal(data, &event) != nil {
			t.Fatal("captured SSE body has an invalid event")
		}
		events = append(events, event)
	}
	return events, terminated
}
