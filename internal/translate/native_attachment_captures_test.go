package translate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/png"
	"reflect"
	"strings"
	"testing"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/sse"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type nativeAttachmentCapture struct {
	Name                  string `json:"name"`
	Source                string `json:"source"`
	Endpoint              string `json:"endpoint"`
	RequestedModel        string `json:"requested_model"`
	ResponseModel         string `json:"response_model"`
	HTTPStatus            int    `json:"http_status"`
	ContentType           string `json:"content_type"`
	RequestSHA256         string `json:"request_sha256"`
	ResponseSSESHA256     string `json:"response_sse_sha256"`
	SSEJSONEvents         int    `json:"sse_json_events"`
	SSEDoneMarker         bool   `json:"sse_done_marker"`
	Terminal              string `json:"terminal"`
	Text                  string `json:"text"`
	TaggedFiles           bool   `json:"tagged_files"`
	ObservedOutcome       string `json:"observed_outcome"`
	DerivedJSONProvenance string `json:"derived_json_provenance"`
	Attachments           []struct {
		MediaType string `json:"media_type"`
		Length    int    `json:"length"`
		SHA256    string `json:"sha256"`
	} `json:"attachments"`
}

func nativeAttachmentCaptures(t *testing.T) []nativeAttachmentCapture {
	t.Helper()
	var manifest struct {
		Cases []nativeAttachmentCapture `json:"cases"`
	}
	if err := json.Unmarshal(readLiveBodyFixture(t, "native-attachment-captures", "manifest.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Cases) != 6 {
		t.Fatal("the native attachment capture set must contain all six inference cases")
	}
	return manifest.Cases
}

func nativeAttachmentSHA256(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func TestCapturedNativeAttachmentSafetyIdentifierSanitization(t *testing.T) {
	const sentinel = "SANITIZED_SAFETY_IDENTIFIER_001"
	for _, capture := range nativeAttachmentCaptures(t) {
		t.Run(capture.Name, func(t *testing.T) {
			count := 0
			var inspect func(any)
			inspect = func(value any) {
				switch value := value.(type) {
				case map[string]any:
					for key, child := range value {
						if key == "safety_identifier" {
							if child != sentinel {
								t.Fatal("a captured persistent safety identifier was not replaced with the shared portable sentinel")
							}
							count++
						}
						inspect(child)
					}
				case []any:
					for _, child := range value {
						inspect(child)
					}
				}
			}
			folder := "native-attachment-captures/" + capture.Name
			for _, filename := range []string{"request.json", "derived-response.json"} {
				root, err := decodeObject(readLiveBodyFixture(t, folder, filename))
				if err != nil {
					t.Fatal(err)
				}
				inspect(root)
			}
			events, _ := liveBodySSEEvents(t, readLiveBodyFixture(t, folder, "response.sse"))
			inspect(events)
			if strings.HasPrefix(capture.Name, "gpt-") && count != 4 {
				t.Fatal("the captured GPT safety identifier equality relationships changed")
			}
		})
	}
}

func TestCapturedNativeAttachmentRequestsAndEvidence(t *testing.T) {
	for _, capture := range nativeAttachmentCaptures(t) {
		t.Run(capture.Name, func(t *testing.T) {
			folder := "native-attachment-captures/" + capture.Name
			request := readLiveBodyFixture(t, folder, "request.json")
			response := readLiveBodyFixture(t, folder, "response.sse")
			if nativeAttachmentSHA256(request) != capture.RequestSHA256 || nativeAttachmentSHA256(response) != capture.ResponseSSESHA256 {
				t.Fatal("the sanitized complete capture fixture changed")
			}
			if capture.HTTPStatus != 200 || capture.ContentType != "text/event-stream" {
				t.Fatal("the observed native HTTP result changed")
			}
			root, err := decodeObject(request)
			if err != nil {
				t.Fatal(err)
			}
			attachments, err := requestAttachments(root, sdktranslator.FromString(capture.Source))
			if err != nil || len(attachments) != len(capture.Attachments) {
				t.Fatalf("captured attachment structure changed: %v", err)
			}
			for index, attachment := range attachments {
				expected := capture.Attachments[index]
				binary, err := inlineAttachmentData(attachment.data, expected.MediaType)
				if err != nil || len(binary) != expected.Length || nativeAttachmentSHA256(binary) != expected.SHA256 {
					t.Fatalf("captured attachment bytes or MIME changed: %v", err)
				}
				if expected.MediaType == "image/png" {
					if expected.SHA256 != "648a0925b839950a3546fb16cae8db3819b4c5ab6288f4051b02daaa5fd9c02e" {
						t.Fatal("the recorded PNG bytes were replaced with a projected fixture")
					}
					image, err := png.Decode(bytes.NewReader(binary))
					if err != nil {
						t.Fatal(err)
					}
					bounds := image.Bounds()
					r, g, b, _ := image.At(bounds.Min.X, bounds.Min.Y).RGBA()
					if r <= g || r <= b {
						t.Fatal("the recorded PNG top half is no longer red")
					}
					r, g, b, _ = image.At(bounds.Min.X, bounds.Max.Y-1).RGBA()
					if b <= r || b <= g {
						t.Fatal("the recorded PNG bottom half is no longer blue")
					}
				} else if expected.MediaType != "application/pdf" || expected.SHA256 != "e8627ac30088e8d989b1e265018ac32bc5b7b94bb4fdb51954deaff0c3ff2d68" || !bytes.HasPrefix(binary, []byte("%PDF-")) || !bytes.Contains(binary, []byte("Q7B9")) {
					t.Fatal("the recorded inline PDF bytes or canary changed")
				}
			}
			if bytes.Contains(request, []byte("tagged_files")) != capture.TaggedFiles {
				t.Fatal("the original native local-reference route changed")
			}
			switch capture.Name {
			case "gpt-png":
				if capture.ObservedOutcome != "visual_canary_failed" || capture.Text != "The top half and bottom half are both blue." || len(attachments) != 1 {
					t.Fatal("HTTP success must not turn the GPT visual canary failure into acceptance")
				}
			case "claude-png", "gemini-png":
				if capture.ObservedOutcome != "visual_canary_passed" || !strings.Contains(capture.Text, "red") || !strings.Contains(capture.Text, "blue") || len(attachments) != 1 {
					t.Fatal("the recorded successful PNG canary evidence changed")
				}
			case "claude-pdf":
				if capture.ObservedOutcome != "inline_pdf_canary_passed" || !strings.Contains(capture.Text, "Q7B9") || !strings.Contains(capture.Text, "blue") || len(attachments) != 1 {
					t.Fatal("the recorded Claude inline PDF canary evidence changed")
				}
			case "gpt-pdf", "gemini-pdf":
				if capture.ObservedOutcome != "initial_local_reference_only" || len(attachments) != 0 || !capture.TaggedFiles || bytes.Contains(request, []byte(`"input_file"`)) || bytes.Contains(request, []byte(`"type": "file"`)) {
					t.Fatal("a native local file reference must not become an inline PDF acceptance claim")
				}
			default:
				t.Fatal("unexpected native attachment capture")
			}
			original := bytes.Clone(request)
			for _, stream := range []bool{false, true} {
				translated, err := RequestForEndpointFrom(capture.Source, capture.RequestedModel, request, stream, capture.Endpoint)
				if err != nil {
					t.Fatal(err)
				}
				expected, err := decodeObject(request)
				if err != nil {
					t.Fatal(err)
				}
				expected["model"], expected["stream"] = capture.RequestedModel, stream
				body, err := json.Marshal(expected)
				if err != nil {
					t.Fatal(err)
				}
				requireLiveBodyJSONEqual(t, translated, body)
				if !bytes.Equal(request, original) {
					t.Fatal("the captured source request was mutated")
				}
			}
		})
	}
}

func nativeAttachmentSSESummary(t *testing.T, source string, body []byte) (text, model, terminal string, count int, done bool) {
	t.Helper()
	decoder := &sse.Decoder{}
	for _, frame := range decoder.Feed(body) {
		_, data, end, err := parseSSEFrame(frame)
		if err != nil {
			t.Fatal(err)
		}
		if end {
			done = true
			continue
		}
		if len(data) == 0 {
			continue
		}
		count++
		root, err := decodeObject(data)
		if err != nil {
			t.Fatal(err)
		}
		switch source {
		case "openai-response":
			if root["type"] == "response.output_text.delta" {
				text += rawStringValue(root["delta"])
			}
			if root["type"] == "response.completed" {
				response := objectValue(root["response"])
				model, terminal = rawStringValue(response["model"]), rawStringValue(response["status"])
			}
		case "claude":
			if root["type"] == "message_start" {
				model = rawStringValue(objectValue(root["message"])["model"])
			}
			delta := objectValue(root["delta"])
			if delta["type"] == "text_delta" {
				text += rawStringValue(delta["text"])
			}
			if root["type"] == "message_delta" {
				terminal = rawStringValue(delta["stop_reason"])
			}
		case "openai":
			if value := rawStringValue(root["model"]); value != "" {
				model = value
			}
			for _, rawChoice := range arrayValue(root["choices"]) {
				choice := objectValue(rawChoice)
				text += rawStringValue(objectValue(choice["delta"])["content"])
				if value := rawStringValue(choice["finish_reason"]); value != "" {
					terminal = value
				}
			}
		}
	}
	return text, model, terminal, count, done
}

func nativeAttachmentJSONText(t *testing.T, source string, body []byte) (text, model, terminal string) {
	t.Helper()
	root, err := decodeObject(body)
	if err != nil {
		t.Fatal(err)
	}
	model = rawStringValue(root["model"])
	switch source {
	case "openai-response":
		terminal = rawStringValue(root["status"])
		for _, rawItem := range arrayValue(root["output"]) {
			item := objectValue(rawItem)
			if item["type"] == "message" {
				for _, rawPart := range arrayValue(item["content"]) {
					text += rawStringValue(objectValue(rawPart)["text"])
				}
			}
		}
	case "claude":
		terminal = rawStringValue(root["stop_reason"])
		for _, rawPart := range arrayValue(root["content"]) {
			part := objectValue(rawPart)
			if part["type"] == "text" {
				text += rawStringValue(part["text"])
			}
		}
	case "openai":
		for _, rawChoice := range arrayValue(root["choices"]) {
			choice := objectValue(rawChoice)
			text += rawStringValue(objectValue(choice["message"])["content"])
			terminal = rawStringValue(choice["finish_reason"])
		}
	}
	return text, model, terminal
}

func TestCapturedNativeAttachmentResponseReplay(t *testing.T) {
	for _, capture := range nativeAttachmentCaptures(t) {
		t.Run(capture.Name, func(t *testing.T) {
			folder := "native-attachment-captures/" + capture.Name
			request := readLiveBodyFixture(t, folder, "request.json")
			body := readLiveBodyFixture(t, folder, "response.sse")
			text, model, terminal, count, done := nativeAttachmentSSESummary(t, capture.Source, body)
			if text != capture.Text || model != capture.ResponseModel || terminal != capture.Terminal || count != capture.SSEJSONEvents || done != capture.SSEDoneMarker {
				t.Fatal("the original recorded response model, text, or terminal changed")
			}
			var state any
			var replay bytes.Buffer
			decoder := &sse.Decoder{}
			for _, frame := range decoder.Feed(body) {
				frames, err := StreamFromEndpoint(context.Background(), capture.Endpoint, capture.Source, capture.RequestedModel, request, request, frame, &state)
				if err != nil {
					t.Fatal(err)
				}
				for _, output := range frames {
					replay.Write(output)
				}
			}
			text, model, terminal, _, done = nativeAttachmentSSESummary(t, capture.Source, replay.Bytes())
			if text != capture.Text || model != capture.ResponseModel || terminal != capture.Terminal || done != capture.SSEDoneMarker {
				t.Fatal("native SSE replay changed the recorded response semantics")
			}
			expectedEvents, expectedDone := liveBodySSEEvents(t, body)
			actualEvents, actualDone := liveBodySSEEvents(t, replay.Bytes())
			if capture.Source == "openai" {
				for _, rawEvent := range expectedEvents {
					event := objectValue(rawEvent)
					if _, exists := event["object"]; !exists {
						event["object"] = "chat.completion.chunk"
					}
				}
			}
			if !reflect.DeepEqual(actualEvents, expectedEvents) || actualDone != expectedDone {
				t.Fatal("native SSE replay changed captured event fields or order beyond Chat object normalization")
			}
			jsonBody := readLiveBodyFixture(t, folder, "derived-response.json")
			if capture.DerivedJSONProvenance == "" {
				t.Fatal("derived JSON must not be mislabeled as an independently captured HTTP JSON response")
			}
			response, err := ResponseFromEndpoint(context.Background(), capture.Endpoint, capture.Source, capture.RequestedModel, request, request, jsonBody)
			if err != nil {
				t.Fatal(err)
			}
			expectedResponse, err := decodeObject(jsonBody)
			if err != nil {
				t.Fatal(err)
			}
			if capture.Source == "openai" {
				expectedResponse["object"] = "chat.completion"
			}
			expectedJSON, err := json.Marshal(expectedResponse)
			if err != nil {
				t.Fatal(err)
			}
			requireLiveBodyJSONEqual(t, response, expectedJSON)
			text, model, terminal = nativeAttachmentJSONText(t, capture.Source, response)
			if text != capture.Text || model != capture.ResponseModel || terminal != capture.Terminal {
				t.Fatal("native JSON snapshot or SSE assembly replay changed the recorded response semantics")
			}
		})
	}
}

func TestCapturedNativeAttachmentOfflineResponseConversions(t *testing.T) {
	for _, capture := range nativeAttachmentCaptures(t) {
		for _, destination := range []string{"openai", "openai-response", "claude"} {
			if destination == capture.Source {
				continue
			}
			t.Run(fmt.Sprintf("%s-to-%s", capture.Name, destination), func(t *testing.T) {
				folder := "native-attachment-captures/" + capture.Name
				request := readLiveBodyFixture(t, folder, "request.json")
				jsonBody := readLiveBodyFixture(t, folder, "derived-response.json")
				response, jsonErr := ResponseFromEndpoint(context.Background(), capture.Endpoint, destination, capture.RequestedModel, request, request, jsonBody)
				decoder := &sse.Decoder{}
				var state any
				var converted bytes.Buffer
				var streamErr error
				for _, frame := range decoder.Feed(readLiveBodyFixture(t, folder, "response.sse")) {
					frames, err := StreamFromEndpoint(context.Background(), capture.Endpoint, destination, capture.RequestedModel, request, request, frame, &state)
					if err != nil {
						streamErr = err
						break
					}
					for _, output := range frames {
						if json.Valid(output) {
							converted.Write(responseSSEBytes("", output))
						} else {
							converted.Write(output)
						}
					}
				}
				if jsonErr != nil || streamErr != nil {
					t.Fatalf("captured conversion failed: JSON=%v SSE=%v", jsonErr, streamErr)
				}
				text, _, terminal := nativeAttachmentJSONText(t, destination, response)
				if text != capture.Text || terminal == "" {
					t.Fatal("offline JSON conversion lost the recorded answer or terminal")
				}
				text, _, terminal, _, _ = nativeAttachmentSSESummary(t, destination, converted.Bytes())
				if text != capture.Text || terminal == "" {
					t.Fatalf("offline SSE conversion changed the recorded answer or terminal: text=%q terminal=%q", text, terminal)
				}
			})
		}
	}
}
