package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/sse"
)

type nativePDFToolCapture struct {
	nativeAttachmentCapture
	Model            string `json:"model"`
	ReasoningEffort  string `json:"reasoning_effort"`
	ContinuationKind string `json:"continuation_kind"`
	NewToolCalls     int    `json:"new_tool_calls"`
}

func nativePDFToolCaptures(t *testing.T) map[string]nativePDFToolCapture {
	t.Helper()
	var manifest struct {
		Cases            []nativePDFToolCapture `json:"cases"`
		GPTExtraUserTurn string                 `json:"gpt_extra_user_turn"`
		PDFResultSHA256  string                 `json:"pdf_result_sha256"`
		PDFResultLength  int                    `json:"pdf_result_length"`
	}
	if err := json.Unmarshal(readLiveBodyFixture(t, "native-pdf-tool-captures", "manifest.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Cases) != 6 || manifest.GPTExtraUserTurn != "captured_native_persisted_session_load" || manifest.PDFResultLength != 623 || manifest.PDFResultSHA256 != "e8627ac30088e8d989b1e265018ac32bc5b7b94bb4fdb51954deaff0c3ff2d68" {
		t.Fatal("the captured native PDF path evidence boundary changed")
	}
	captures := make(map[string]nativePDFToolCapture)
	for _, capture := range manifest.Cases {
		captures[capture.Name] = capture
	}
	return captures
}

func nativePDFToolRequest(t *testing.T, name string) map[string]any {
	t.Helper()
	root, err := decodeObject(readLiveBodyFixture(t, "native-pdf-tool-captures/"+name, "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestCapturedNativePDFToolPassthrough(t *testing.T) {
	for _, capture := range nativePDFToolCaptures(t) {
		t.Run(capture.Name, func(t *testing.T) {
			folder := "native-pdf-tool-captures/" + capture.Name
			request := readLiveBodyFixture(t, folder, "request.json")
			body := readLiveBodyFixture(t, folder, "response.sse")
			if nativeAttachmentSHA256(request) != capture.RequestSHA256 || nativeAttachmentSHA256(body) != capture.ResponseSSESHA256 || capture.HTTPStatus != 200 || !bytes.HasSuffix(body, []byte("\n\n")) {
				t.Fatal("the complete sanitized native capture or SSE delimiter changed")
			}
			for _, stream := range []bool{false, true} {
				original := bytes.Clone(request)
				translated, err := RequestForEndpointFrom(capture.Source, capture.Model, request, stream, capture.Endpoint)
				if err != nil {
					t.Fatal(err)
				}
				expected := nativePDFToolRequest(t, capture.Name)
				expected["model"], expected["stream"] = capture.Model, stream
				encoded, err := json.Marshal(expected)
				if err != nil {
					t.Fatal(err)
				}
				requireLiveBodyJSONEqual(t, translated, encoded)
				if !bytes.Equal(request, original) {
					t.Fatal("native replay mutated the source request")
				}
			}
			text, model, terminal, count, done := nativeAttachmentSSESummary(t, capture.Source, body)
			if text != capture.Text || model != capture.ResponseModel || terminal != capture.Terminal || count != capture.SSEJSONEvents || done != capture.SSEDoneMarker {
				t.Fatal("the captured model, text, terminal, or event set changed")
			}
			if !strings.HasSuffix(capture.Name, "initial") && !strings.Contains(text, "Q7B9") {
				t.Fatal("the native PDF result follow-up lost the observed marker")
			}
			var state any
			var replay bytes.Buffer
			decoder := &sse.Decoder{}
			for _, frame := range decoder.Feed(body) {
				frames, err := StreamFromEndpoint(context.Background(), capture.Endpoint, capture.Source, capture.Model, request, request, frame, &state)
				if err != nil {
					t.Fatal(err)
				}
				for _, output := range frames {
					replay.Write(output)
				}
			}
			expectedEvents, expectedDone := liveBodySSEEvents(t, body)
			actualEvents, actualDone := liveBodySSEEvents(t, replay.Bytes())
			if capture.Source == "openai" {
				for _, raw := range expectedEvents {
					event := objectValue(raw)
					if _, exists := event["object"]; !exists {
						event["object"] = "chat.completion.chunk"
					}
				}
			}
			if !reflect.DeepEqual(actualEvents, expectedEvents) || actualDone != expectedDone {
				t.Fatal("native SSE replay changed fields or order beyond Chat object normalization")
			}
		})
	}
}

func nativePDFToolViewCall(t *testing.T, name string) (id, arguments string) {
	t.Helper()
	events, _ := liveBodySSEEvents(t, readLiveBodyFixture(t, "native-pdf-tool-captures/"+name, "response.sse"))
	toolName := ""
	for _, raw := range events {
		event := objectValue(raw)
		if event["type"] == "response.completed" {
			for _, item := range arrayValue(objectValue(event["response"])["output"]) {
				call := objectValue(item)
				if call["type"] == "function_call" {
					id, arguments, toolName = rawStringValue(call["call_id"]), rawStringValue(call["arguments"]), rawStringValue(call["name"])
				}
			}
		}
		for _, choice := range arrayValue(event["choices"]) {
			for _, rawCall := range arrayValue(objectValue(objectValue(choice)["delta"])["tool_calls"]) {
				call := objectValue(rawCall)
				if value := rawStringValue(call["id"]); value != "" {
					id = value
				}
				function := objectValue(call["function"])
				toolName += rawStringValue(function["name"])
				arguments += rawStringValue(function["arguments"])
			}
		}
	}
	if toolName != "view" || !strings.HasPrefix(id, "SANITIZED_ID_") || !json.Valid([]byte(arguments)) {
		t.Fatal("the initial native response must retain its real view call")
	}
	return id, arguments
}

func TestCapturedNativePDFToolCallAndResultIdentity(t *testing.T) {
	for _, prefix := range []string{"gpt", "gemini"} {
		t.Run(prefix, func(t *testing.T) {
			id, arguments := nativePDFToolViewCall(t, prefix+"-initial")
			callArguments, err := decodeObject([]byte(arguments))
			if err != nil {
				t.Fatal(err)
			}
			path := rawStringValue(callArguments["path"])
			initial := readLiveBodyFixture(t, "native-pdf-tool-captures/"+prefix+"-initial", "request.json")
			if len(callArguments) != 1 || !strings.HasPrefix(path, "/sanitized/native-attachments/SANITIZED_FILE_") || !bytes.Contains(initial, []byte(path)) || !bytes.Contains(initial, []byte("tagged_files")) || bytes.Contains(initial, []byte(`"input_file"`)) {
				t.Fatal("the original tagged_files route and view path no longer agree")
			}
			root := nativePDFToolRequest(t, prefix+"-result")
			initialRoot := nativePDFToolRequest(t, prefix+"-initial")
			historyKey := "messages"
			if prefix == "gpt" {
				historyKey = "input"
			}
			if len(arrayValue(root[historyKey])) < 2 || !reflect.DeepEqual(arrayValue(initialRoot[historyKey]), arrayValue(root[historyKey])[:2]) {
				t.Fatal("the native result request lost its original system and tagged_files user history")
			}
			var call, result map[string]any
			var pdf string
			if prefix == "gpt" {
				items := arrayValue(root["input"])
				if len(items) != 4 {
					t.Fatal("the GPT native request history changed")
				}
				call, result = objectValue(items[2]), objectValue(items[3])
				if call["type"] != "function_call" || call["name"] != "view" || call["call_id"] != id || result["type"] != "function_call_output" || result["call_id"] != id {
					t.Fatal("the GPT native view call and function output are no longer paired")
				}
				pdf = rawStringValue(result["output"])
				events, _ := liveBodySSEEvents(t, readLiveBodyFixture(t, "native-pdf-tool-captures/gpt-initial", "response.sse"))
				for _, raw := range events {
					event := objectValue(raw)
					if event["type"] == "response.output_item.done" && objectValue(event["item"])["type"] == "function_call" {
						emitted := objectValue(event["item"])
						if len(call) != len(emitted) || call["id"] == emitted["id"] {
							t.Fatal("the captured CLI's distinct item ID and preserved call shape changed")
						}
						for key, value := range emitted {
							if key != "id" && !reflect.DeepEqual(call[key], value) {
								t.Fatal("the GPT native continuation changed an emitted function call field beyond its captured item ID replacement")
							}
						}
					}
				}
			} else {
				items := arrayValue(root["messages"])
				if len(items) != 4 {
					t.Fatal("the Gemini native request history changed")
				}
				assistant := objectValue(items[2])
				calls := arrayValue(assistant["tool_calls"])
				if len(calls) != 1 {
					t.Fatal("the native Gemini view call count changed")
				}
				tool := objectValue(calls[0])
				call, result = objectValue(tool["function"]), objectValue(items[3])
				if assistant["role"] != "assistant" || tool["id"] != id || call["name"] != "view" || result["role"] != "tool" || result["tool_call_id"] != id {
					t.Fatal("the Gemini native assistant call and tool result are no longer paired")
				}
				pdf = rawStringValue(result["content"])
			}
			requireLiveBodyJSONEqual(t, []byte(rawStringValue(call["arguments"])), []byte(arguments))
			if len([]byte(pdf)) != 623 || nativeAttachmentSHA256([]byte(pdf)) != "e8627ac30088e8d989b1e265018ac32bc5b7b94bb4fdb51954deaff0c3ff2d68" || !strings.HasPrefix(pdf, "%PDF-") || !strings.Contains(pdf, "Document canary: Q7B9") {
				t.Fatal("the actual native view PDF result bytes were replaced or extracted synthetically")
			}
		})
	}
}

func TestCapturedNativePDFGeminiExtraUserTurnHistory(t *testing.T) {
	captures := nativePDFToolCaptures(t)
	result := arrayValue(nativePDFToolRequest(t, "gemini-result")["messages"])
	followup := arrayValue(nativePDFToolRequest(t, "gemini-followup")["messages"])
	if len(followup) != 6 || !reflect.DeepEqual(result, followup[:4]) {
		t.Fatal("the genuine extra user turn lost its original view call and PDF result history")
	}
	answer, user := objectValue(followup[4]), objectValue(followup[5])
	if answer["role"] != "assistant" || answer["content"] != captures["gemini-result"].Text || user["role"] != "user" || !strings.Contains(rawStringValue(user["content"]), "Without reading a new file") || !strings.Contains(captures["gemini-followup"].Text, "Q7B9") {
		t.Fatal("the captured same-session text-only follow-up and marker answer changed")
	}
}

func TestCapturedNativePDFGPTPersistedSessionFollowup(t *testing.T) {
	captures := nativePDFToolCaptures(t)
	capture := captures["gpt-resumed-followup"]
	root := nativePDFToolRequest(t, capture.Name)
	items := arrayValue(root["input"])
	prior := arrayValue(nativePDFToolRequest(t, "gpt-result")["input"])
	if capture.Model != "gpt-6-luna" || capture.ResponseModel != "gpt-6-luna" || capture.ReasoningEffort != "medium" || capture.ContinuationKind != "native_persisted_session_load" || objectValue(root["reasoning"])["effort"] != "medium" || len(items) != 6 || !reflect.DeepEqual(prior[1:4], items[1:4]) {
		t.Fatal("the real resumed native session lost its exact model, effort, tagged_files user, view call, or paired PDF result")
	}
	id, arguments := nativePDFToolViewCall(t, "gpt-initial")
	call, result := objectValue(items[2]), objectValue(items[3])
	if call["call_id"] != id || result["call_id"] != id || call["type"] != "function_call" || call["name"] != "view" || result["type"] != "function_call_output" {
		t.Fatal("persisted session loading changed the original native call/result identity")
	}
	requireLiveBodyJSONEqual(t, []byte(rawStringValue(call["arguments"])), []byte(arguments))
	pdf := []byte(rawStringValue(result["output"]))
	if len(pdf) != 623 || nativeAttachmentSHA256(pdf) != "e8627ac30088e8d989b1e265018ac32bc5b7b94bb4fdb51954deaff0c3ff2d68" {
		t.Fatal("persisted session loading changed the original native PDF result bytes")
	}
	answer, user := objectValue(items[4]), objectValue(items[5])
	parts := arrayValue(answer["content"])
	userJSON, err := json.Marshal(user["content"])
	if err != nil {
		t.Fatal(err)
	}
	if answer["role"] != "assistant" || len(parts) != 1 || objectValue(parts[0])["type"] != "output_text" || objectValue(parts[0])["text"] != captures["gpt-result"].Text || user["role"] != "user" || bytes.Contains(userJSON, []byte("Q7B9")) || bytes.Contains(userJSON, []byte("tagged_files")) || !bytes.Contains(userJSON, []byte("unique marker")) {
		t.Fatal("the captured loaded answer or text-only user prompt changed or leaked the expected marker into the new prompt")
	}
	events, _ := liveBodySSEEvents(t, readLiveBodyFixture(t, "native-pdf-tool-captures/"+capture.Name, "response.sse"))
	newCalls, completed := 0, false
	for _, raw := range events {
		event := objectValue(raw)
		if event["type"] == "response.output_item.done" && objectValue(event["item"])["type"] == "function_call" {
			newCalls++
		}
		if event["type"] == "response.completed" {
			response := objectValue(event["response"])
			encoded, err := json.Marshal(response["output"])
			if err != nil {
				t.Fatal(err)
			}
			completed = response["status"] == "completed" && response["model"] == "gpt-6-luna" && bytes.Contains(encoded, []byte("Q7B9"))
			for _, item := range arrayValue(response["output"]) {
				if objectValue(item)["type"] == "function_call" {
					newCalls++
				}
			}
		}
	}
	if !completed || capture.Text != "Q7B9" || capture.NewToolCalls != 0 || newCalls != 0 {
		t.Fatal("the real resumed text-only turn must complete with Q7B9 and zero new tool calls")
	}
}
