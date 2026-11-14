package translate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/sse"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestCapturedHostedStreamBodyShapeAndProvenance(t *testing.T) {
	root := "testdata/native-hosted-tool-stream-captures"
	manifest := readHostedStreamFixture(t, root, "manifest.json")
	for _, name := range gjson.GetBytes(manifest, "captures").Array() {
		t.Run(name.String(), func(t *testing.T) {
			directory := filepath.Join(root, name.String())
			metadata := readHostedStreamFixture(t, directory, "metadata.json")
			for _, kind := range []string{"request", "response"} {
				filename := kind + ".json"
				if kind == "response" && gjson.GetBytes(metadata, "response_format").String() == "sse" {
					filename = "response.sse"
				}
				body := readHostedStreamFixture(t, directory, filename)
				var decoded any
				if strings.HasSuffix(filename, ".sse") {
					var decoder sse.Decoder
					var events []any
					for _, frame := range decoder.Feed(body) {
						_, data, done, err := parseSSEFrame(frame)
						if err != nil {
							t.Fatal(err)
						}
						if done {
							events = append(events, "[DONE]")
						} else if len(data) > 0 {
							events = append(events, decodeHostedStreamFixture(t, data))
						}
					}
					if len(decoder.Flush()) != 0 {
						t.Fatal("captured upstream stream is truncated")
					}
					decoded = events
				} else {
					decoded = decodeHostedStreamFixture(t, body)
				}
				shape, err := json.Marshal(nativeServerToolShape(decoded))
				if err != nil {
					t.Fatal(err)
				}
				expectedShape := readHostedStreamFixture(t, directory, kind+".shape.json")
				requireLiveBodyJSONEqual(t, shape, expectedShape)
				for _, check := range []struct {
					body  []byte
					field string
				}{{storedHostedStreamFixture(t, directory, filename), "fixture_sha256." + kind}, {body, "sanitized_source_sha256." + kind}, {expectedShape, "original_shape_sha256." + kind}} {
					digest := sha256.Sum256(check.body)
					if hex.EncodeToString(digest[:]) != gjson.GetBytes(metadata, check.field).String() {
						t.Fatalf("complete captured body/shape digest differs: %s", check.field)
					}
				}
				assertHostedStreamFixturePrivacy(t, decoded)
			}
		})
	}
}

func TestCapturedHostedSearchReconcilesEveryNativeIdentity(t *testing.T) {
	directory := "testdata/native-hosted-tool-stream-captures/gpt-search-stream-before-repair"
	request := readHostedStreamFixture(t, directory, "request.json")
	body := readHostedStreamFixture(t, directory, "response.sse")
	var decoder sse.Decoder
	frames := decoder.Feed(body)
	_, terminal, _, err := parseSSEFrame(frames[len(frames)-1])
	if err != nil {
		t.Fatal(err)
	}
	var state any
	var output [][]byte
	for index, frame := range frames {
		out, err := StreamFromEndpoint(context.Background(), EndpointResponses, "openai-response", "gpt-6-luna", request, request, frame, &state)
		if err != nil {
			t.Fatal(err)
		}
		if index != len(frames)-1 && len(out) != 0 {
			t.Fatal("a mutable response or item identity escaped before authoritative completion")
		}
		output = append(output, out...)
	}
	if len(output) != len(frames) || !bytes.Equal(output[len(output)-1], frames[len(frames)-1]) {
		t.Fatal("actual native event sequence or terminal bytes changed")
	}
	changed := 0
	for index, frame := range frames[:len(frames)-1] {
		name, original, _, err := parseSSEFrame(frame)
		if err != nil {
			t.Fatal(err)
		}
		expected := original
		if gjson.GetBytes(original, "response.id").Exists() {
			expected, err = sjson.SetBytes(expected, "response.id", gjson.GetBytes(terminal, "response.id").String())
			if err != nil {
				t.Fatal(err)
			}
		}
		if outputIndex := gjson.GetBytes(original, "output_index"); outputIndex.Exists() {
			id := gjson.GetBytes(terminal, fmt.Sprintf("response.output.%d.id", outputIndex.Int())).String()
			for _, path := range []string{"item.id", "item_id"} {
				if gjson.GetBytes(original, path).Exists() {
					expected, err = sjson.SetBytes(expected, path, id)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
		}
		actualName, actual, _, err := parseSSEFrame(output[index])
		if err != nil || actualName != name {
			t.Fatal("native SSE envelope changed")
		}
		requireLiveBodyJSONEqual(t, actual, expected)
		if !bytes.Equal(original, expected) {
			changed++
		}
	}
	if changed != len(frames)-1 {
		t.Fatal("fixture did not exercise every observed mutable preterminal identity")
	}
}

func readHostedStreamFixture(t *testing.T, directory, name string) []byte {
	t.Helper()
	body := storedHostedStreamFixture(t, directory, name)
	if name == "response.sse" {
		var raw string
		if err := json.Unmarshal(body, &raw); err != nil {
			t.Fatal(err)
		}
		return []byte(raw)
	}
	return body
}

func storedHostedStreamFixture(t *testing.T, directory, name string) []byte {
	t.Helper()
	if name == "response.sse" {
		name = "response.json"
	}
	body, err := os.ReadFile(filepath.Join(directory, name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestCapturedHostedSearchRejectsUnboundTerminalIdentities(t *testing.T) {
	directory := "testdata/native-hosted-tool-stream-captures/gpt-search-stream-before-repair"
	request := readHostedStreamFixture(t, directory, "request.json")
	body := readHostedStreamFixture(t, directory, "response.sse")
	for _, test := range []struct {
		name, event, path string
		value             any
	}{
		{"missing response identity", "response.completed", "response.id", nil},
		{"missing message identity", "response.completed", "response.output.2.id", nil},
		{"unbound ordinary index", "response.content_part.added", "output_index", 999},
		{"wrong ordinary type", "response.output_item.added", "item.type", "invalid_item_type"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var decoder sse.Decoder
			var state any
			failed := false
			for _, frame := range decoder.Feed(body) {
				name, data, _, err := parseSSEFrame(frame)
				if err != nil {
					t.Fatal(err)
				}
				if gjson.GetBytes(data, "type").String() == test.event {
					data, err = sjson.SetBytes(data, test.path, test.value)
					if err != nil {
						t.Fatal(err)
					}
					frame = responseSSEBytes(name, data)
				}
				if _, err := StreamFromEndpoint(context.Background(), EndpointResponses, "openai-response", "gpt-6-luna", request, request, frame, &state); err != nil {
					failed = true
					break
				}
			}
			if !failed {
				t.Fatal("unbound native hosted-tool identity was accepted")
			}
		})
	}
}

func decodeHostedStreamFixture(t *testing.T, body []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func assertHostedStreamFixturePrivacy(t *testing.T, value any) {
	t.Helper()
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			assertHostedStreamFixturePrivacy(t, item)
		}
	case map[string]any:
		for key, item := range typed {
			text, isString := item.(string)
			private := key == "id" || strings.HasSuffix(key, "_id") || key == "prompt_cache_key" || key == "safety_identifier" || key == "encrypted_content" || key == "encrypted_index" || key == "signature" || key == "obfuscation"
			if private && isString && text != "" && !strings.HasPrefix(text, "fixture_opaque_") {
				t.Fatalf("opaque source value was published for field %s", key)
			}
			assertHostedStreamFixturePrivacy(t, item)
		}
	}
}

func TestCapturedHostedSearchContinuationKeepsActualTerminalHistory(t *testing.T) {
	root := "testdata/native-hosted-tool-stream-captures"
	original := readHostedStreamFixture(t, filepath.Join(root, "gpt-search-stream-before-repair"), "response.sse")
	var decoder sse.Decoder
	frames := decoder.Feed(original)
	_, terminal, _, err := parseSSEFrame(frames[len(frames)-1])
	if err != nil {
		t.Fatal(err)
	}
	continuation := readHostedStreamFixture(t, filepath.Join(root, "gpt-search-captured-history-continuation"), "request.json")
	history := gjson.GetBytes(continuation, "input").Array()
	owned := gjson.GetBytes(terminal, "response.output").Array()
	if len(history) != len(owned)+2 || gjson.GetBytes(continuation, "tool_choice").String() != "none" {
		t.Fatal("captured continuation lost its prior user, actual output, or tool exclusion")
	}
	for index, item := range owned {
		requireLiveBodyJSONEqual(t, []byte(history[index+1].Raw), []byte(item.Raw))
	}
	response := readHostedStreamFixture(t, filepath.Join(root, "gpt-search-captured-history-continuation"), "response.json")
	if gjson.GetBytes(response, "status").String() != "completed" || gjson.GetBytes(response, "model").String() != "gpt-6-luna" {
		t.Fatal("captured native continuation did not complete on the assigned model")
	}
	for _, item := range gjson.GetBytes(response, "output").Array() {
		if item.Get("type").String() != "message" && item.Get("type").String() != "reasoning" {
			t.Fatal("captured continuation executed a new tool")
		}
	}
}
