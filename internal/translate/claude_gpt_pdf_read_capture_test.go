package translate

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const capturedClaudeGPTPDFReadFixtureRoot = "testdata/claude-gpt-pdf-read"

func capturedClaudeGPTPDFReadRoot() string {
	if root := os.Getenv("CPA_NATIVE_PDF_FIXTURE_ROOT"); root != "" {
		return root
	}
	return capturedClaudeGPTPDFReadFixtureRoot
}

func readCapturedClaudeGPTPDFReadFile(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(capturedClaudeGPTPDFReadRoot(), name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func capturedClaudeGPTPDFReadJSON(t *testing.T, caseName string, hop int, side string) []byte {
	t.Helper()
	name := caseName + "-hop-00" + string(rune('0'+hop)) + "-" + side + ".json"
	return readCapturedClaudeGPTPDFReadFile(t, filepath.Join("bodies", name))
}

func capturedClaudeGPTPDFReadSSE(t *testing.T, caseName string, hop int, side string) []byte {
	t.Helper()
	name := caseName + "-hop-00" + string(rune('0'+hop)) + "-" + side + ".sse"
	return readCapturedClaudeGPTPDFReadFile(t, filepath.Join("bodies", name))
}

type capturedClaudeGPTPDFReadCase struct {
	name         string
	pages        string
	answerSignal string
}

var capturedClaudeGPTPDFReadCases = []capturedClaudeGPTPDFReadCase{
	{name: "text-missing-fail", pages: "1", answerSignal: "printed_code_absent"},
	{name: "content-visible-range-1-20", pages: "1-20", answerSignal: "printed_code_q7b9"},
}

func TestCapturedClaudeGPTPDFReadBodyShapeAndProvenance(t *testing.T) {
	t.Parallel()
	for _, testCase := range capturedClaudeGPTPDFReadCases {
		for _, spec := range [][2]string{
			{"hop-001-claude-request", "json"},
			{"hop-001-claude-response", "sse"},
			{"hop-001-responses-request", "json"},
			{"hop-001-responses-response", "sse"},
			{"hop-002-claude-request", "json"},
			{"hop-002-claude-response", "sse"},
			{"hop-002-responses-request", "json"},
			{"hop-002-responses-response", "sse"},
		} {
			name, kind := testCase.name+"-"+spec[0], spec[1]
			t.Run(name, func(t *testing.T) {
				var body []byte
				if kind == "json" {
					body = readCapturedClaudeGPTPDFReadFile(t, filepath.Join("bodies", name+".json"))
				} else {
					body = readCapturedClaudeGPTPDFReadFile(t, filepath.Join("bodies", name+".sse"))
				}
				assertCapturedClaudeGPTReadFixtureIntegrity(t, readCapturedClaudeGPTPDFReadFile, name, kind, body)
				assertCapturedClaudeGPTPNGReadPrivacySentinels(t, body, kind)
			})
		}
	}
}

func TestCapturedClaudeGPTPDFReadRunsExistingMessagesToResponsesAndStreamConverters(t *testing.T) {
	t.Parallel()
	for _, testCase := range capturedClaudeGPTPDFReadCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			assertCapturedClaudeGPTPDFReadCycle(t, testCase)
		})
	}
}

func assertCapturedClaudeGPTPDFReadCycle(t *testing.T, testCase capturedClaudeGPTPDFReadCase) {
	t.Helper()
	claudeRequest1 := capturedClaudeGPTPDFReadJSON(t, testCase.name, 1, "claude-request")
	claudeRequest2 := capturedClaudeGPTPDFReadJSON(t, testCase.name, 2, "claude-request")
	responsesRequest1 := capturedClaudeGPTPDFReadJSON(t, testCase.name, 1, "responses-request")
	responsesRequest2 := capturedClaudeGPTPDFReadJSON(t, testCase.name, 2, "responses-request")
	providerResponse1 := capturedClaudeGPTPDFReadSSE(t, testCase.name, 1, "responses-response")
	providerResponse2 := capturedClaudeGPTPDFReadSSE(t, testCase.name, 2, "responses-response")
	clientResponse1 := capturedClaudeGPTPDFReadSSE(t, testCase.name, 1, "claude-response")
	clientResponse2 := capturedClaudeGPTPDFReadSSE(t, testCase.name, 2, "claude-response")

	convertedRequest1, err := RequestForEndpointFrom("claude", "gpt-6-luna", claudeRequest1, true, EndpointResponses)
	if err != nil {
		t.Fatalf("translate captured first Messages request: %v", err)
	}
	convertedRequest2, err := RequestForEndpointFrom("claude", "gpt-6-luna", claudeRequest2, true, EndpointResponses)
	if err != nil {
		t.Fatalf("translate captured follow-up Messages request: %v", err)
	}
	for _, request := range [][]byte{convertedRequest1, convertedRequest2} {
		if gjson.GetBytes(request, "model").String() != "gpt-6-luna" || !gjson.GetBytes(request, "stream").Bool() {
			t.Fatal("existing converter changed the requested destination model or stream mode")
		}
	}

	capturedMessages1 := capturedClaudeGPTPNGReadDecodeObject(t, claudeRequest1)
	capturedMessages2 := capturedClaudeGPTPNGReadDecodeObject(t, claudeRequest2)
	capturedResponses1 := capturedClaudeGPTPNGReadDecodeObject(t, responsesRequest1)
	capturedResponses2 := capturedClaudeGPTPNGReadDecodeObject(t, responsesRequest2)
	generated1 := capturedClaudeGPTPNGReadDecodeObject(t, convertedRequest1)
	generated2 := capturedClaudeGPTPNGReadDecodeObject(t, convertedRequest2)
	if capturedClaudeGPTPNGReadString(capturedResponses1["model"]) != "gpt-6-luna" || capturedClaudeGPTPNGReadString(capturedResponses2["model"]) != "gpt-6-luna" {
		t.Fatal("captured public request used a different Responses model")
	}
	assertCapturedClaudeGPTPDFReadDeclaration(t, capturedResponses1, generated1)
	for _, pair := range []struct {
		claude, responses, generated map[string]any
	}{
		{capturedMessages1, capturedResponses1, generated1},
		{capturedMessages2, capturedResponses2, generated2},
	} {
		claudeText := capturedClaudeGPTPNGReadClaudeUserTexts(pair.claude)
		responsesText := capturedClaudeGPTPNGReadResponsesUserTexts(pair.responses)
		generatedText := capturedClaudeGPTPNGReadResponsesUserTexts(pair.generated)
		if len(claudeText) == 0 || strings.Join(claudeText, "\x00") != strings.Join(responsesText, "\x00") || strings.Join(claudeText, "\x00") != strings.Join(generatedText, "\x00") {
			t.Fatal("Messages-to-Responses conversion changed captured user text")
		}
	}

	_, providerCalls1, providerEvents1 := capturedClaudeGPTPNGReadResponseEvents(t, providerResponse1)
	if len(providerCalls1) == 0 || !capturedClaudeGPTPDFReadCompleted(t, providerEvents1) {
		t.Fatal("captured first provider stream did not finish with its actual Read function call")
	}
	providerCall1 := providerCalls1[len(providerCalls1)-1]
	if capturedClaudeGPTPNGReadString(providerCall1["name"]) != "Read" {
		t.Fatal("captured provider function call was not Read")
	}
	var providerArgs map[string]any
	if err := json.Unmarshal([]byte(capturedClaudeGPTPNGReadString(providerCall1["arguments"])), &providerArgs); err != nil {
		t.Fatalf("decode captured Read arguments: %v", err)
	}
	if capturedClaudeGPTPNGReadString(providerArgs["pages"]) != testCase.pages {
		t.Fatal("captured provider Read page range changed")
	}

	translatedClientResponse1 := capturedClaudeGPTPNGReadTranslateResponsesStream(t, providerResponse1, claudeRequest1, convertedRequest1)
	translatedID, translatedName, translatedArgs, translatedStop := capturedClaudeGPTPNGReadClaudeToolUse(t, translatedClientResponse1)
	capturedID, capturedName, capturedArgs, capturedStop := capturedClaudeGPTPNGReadClaudeToolUse(t, clientResponse1)
	if translatedName != "Read" || capturedName != "Read" || translatedStop != "tool_use" || capturedStop != "tool_use" {
		t.Fatal("provider stream did not retain the native Claude Read tool_use turn")
	}
	if translatedID == "" || translatedID != capturedID {
		t.Fatal("existing stream converter changed the captured Claude tool ID")
	}
	carrierItemID, carrierCallID, carrierOK := DecodeClaudeToolIDs(translatedID)
	if !carrierOK || carrierItemID != capturedClaudeGPTPNGReadString(providerCall1["id"]) || carrierCallID != capturedClaudeGPTPNGReadString(providerCall1["call_id"]) {
		t.Fatal("native Claude tool ID carrier did not preserve the captured Responses item/call pair")
	}
	providerArgsJSON, _ := json.Marshal(providerArgs)
	translatedArgsJSON, _ := json.Marshal(translatedArgs)
	capturedArgsJSON, _ := json.Marshal(capturedArgs)
	requireLiveBodyJSONEqual(t, providerArgsJSON, translatedArgsJSON)
	requireLiveBodyJSONEqual(t, providerArgsJSON, capturedArgsJSON)

	claudeToolID2, claudeToolName2, claudeToolArgs2 := capturedClaudeGPTPNGReadClaudeToolUseFromRequest(t, claudeRequest2)
	toolResultID2 := capturedClaudeGPTPNGReadToolResultID(t, claudeRequest2)
	if claudeToolName2 != "Read" || claudeToolID2 != translatedID || toolResultID2 != claudeToolID2 {
		t.Fatal("follow-up Messages request lost the actual Read call/result correlation")
	}
	claudeArgsJSON, _ := json.Marshal(claudeToolArgs2)
	requireLiveBodyJSONEqual(t, providerArgsJSON, claudeArgsJSON)
	if capturedClaudeGPTPNGReadString(claudeToolArgs2["pages"]) != testCase.pages {
		t.Fatal("original follow-up Messages request changed the captured page range")
	}

	generatedCall := capturedClaudeGPTPNGReadToolCallInRequest(t, convertedRequest2)
	capturedCall := capturedClaudeGPTPNGReadToolCallInRequest(t, responsesRequest2)
	generatedOutput := capturedClaudeGPTPNGReadToolResultInRequest(t, convertedRequest2)
	capturedOutput := capturedClaudeGPTPNGReadToolResultInRequest(t, responsesRequest2)
	if capturedClaudeGPTPNGReadString(generatedCall["name"]) != "Read" || capturedClaudeGPTPNGReadString(capturedCall["name"]) != "Read" {
		t.Fatal("follow-up Responses request lost the captured Read call")
	}
	callID := capturedClaudeGPTPNGReadString(providerCall1["call_id"])
	itemID := capturedClaudeGPTPNGReadString(providerCall1["id"])
	if callID == "" || capturedClaudeGPTPNGReadString(generatedCall["id"]) != itemID || capturedClaudeGPTPNGReadString(capturedCall["id"]) != itemID || capturedClaudeGPTPNGReadString(generatedCall["call_id"]) != callID || capturedClaudeGPTPNGReadString(capturedCall["call_id"]) != callID || capturedClaudeGPTPNGReadString(generatedOutput["call_id"]) != callID || capturedClaudeGPTPNGReadString(capturedOutput["call_id"]) != callID {
		t.Fatal("Responses function_call and function_call_output IDs do not match the captured provider call")
	}
	var generatedArgs, capturedCallArgs map[string]any
	if err := json.Unmarshal([]byte(capturedClaudeGPTPNGReadString(generatedCall["arguments"])), &generatedArgs); err != nil {
		t.Fatalf("decode converted follow-up Read arguments: %v", err)
	}
	if err := json.Unmarshal([]byte(capturedClaudeGPTPNGReadString(capturedCall["arguments"])), &capturedCallArgs); err != nil {
		t.Fatalf("decode captured public follow-up Read arguments: %v", err)
	}
	generatedArgsJSON, _ := json.Marshal(generatedArgs)
	capturedPublicArgsJSON, _ := json.Marshal(capturedCallArgs)
	requireLiveBodyJSONEqual(t, claudeArgsJSON, generatedArgsJSON)
	requireLiveBodyJSONEqual(t, claudeArgsJSON, capturedPublicArgsJSON)
	if capturedClaudeGPTPNGReadString(generatedArgs["pages"]) != testCase.pages || capturedClaudeGPTPNGReadString(capturedCallArgs["pages"]) != testCase.pages {
		t.Fatal("converter or captured Responses request changed the actual Read page range")
	}

	jpegHash, _ := capturedClaudeGPTAttachmentReadSourceImage(t, claudeRequest2, "image/jpeg")
	generatedJPEGHash := capturedClaudeGPTAttachmentReadFindImageHash(t, generatedOutput["output"], "image/jpeg")
	capturedJPEGHash := capturedClaudeGPTAttachmentReadFindImageHash(t, capturedOutput["output"], "image/jpeg")
	if jpegHash == "" || generatedJPEGHash != jpegHash || capturedJPEGHash != jpegHash {
		t.Fatal("decoded JPEG bytes changed across Claude tool_result and Responses function_call_output")
	}
	evaluationBody := readCapturedClaudeGPTPDFReadFile(t, "evaluation.json")
	var evaluation struct {
		CapturedCases []struct {
			Name                           string `json:"name"`
			ClaudeToolResultJPEGSHA256     string `json:"claude_tool_result_jpeg_sha256"`
			ResponsesFunctionOutputJPEGSHA string `json:"responses_function_output_jpeg_sha256"`
		} `json:"captured_cases"`
	}
	if err := json.Unmarshal(evaluationBody, &evaluation); err != nil {
		t.Fatal(err)
	}
	var recordedHash string
	for _, capture := range evaluation.CapturedCases {
		if capture.Name == testCase.name {
			recordedHash = capture.ClaudeToolResultJPEGSHA256
			if capture.ResponsesFunctionOutputJPEGSHA != capture.ClaudeToolResultJPEGSHA256 {
				t.Fatal("capture metadata recorded different JPEG data across original and converted bodies")
			}
		}
	}
	if recordedHash == "" || recordedHash != jpegHash {
		t.Fatal("decoded JPEG hash differs from body provenance metadata")
	}

	translatedClientResponse2 := capturedClaudeGPTPNGReadTranslateResponsesStream(t, providerResponse2, claudeRequest2, convertedRequest2)
	providerText := capturedClaudeGPTPNGReadText(t, providerResponse2)
	capturedClientText := capturedClaudeGPTPNGReadText(t, clientResponse2)
	translatedClientText := capturedClaudeGPTPNGReadText(t, translatedClientResponse2)
	if providerText == "" || providerText != capturedClientText || providerText != translatedClientText {
		t.Fatal("provider final answer, captured Claude answer, and converted answer diverged")
	}
	normalized := " " + strings.ToLower(strings.Join(strings.Fields(providerText), " ")) + " "
	mentionsCode := strings.Contains(normalized, "q7b9")
	mentionsBlue := strings.Contains(normalized, "blue")
	if !mentionsBlue {
		t.Fatal("captured final answer lost its blue content")
	}
	if testCase.answerSignal == "printed_code_absent" {
		if mentionsCode || !strings.Contains(normalized, "no printed code is visible") {
			t.Fatal("historical text-missing answer was changed")
		}
	} else if !mentionsCode {
		t.Fatal("later original-range answer lost the captured Q7B9 content")
	}
	assertCapturedClaudeGPTPDFReadOriginalResult(t, testCase.name, testCase.pages, testCase.answerSignal)

	if !capturedClaudeGPTPDFReadCompleted(t, providerEvents1) {
		t.Fatal("captured initial Read provider stream is not completed")
	}
	_, _, providerEvents2 := capturedClaudeGPTPNGReadResponseEvents(t, providerResponse2)
	if !capturedClaudeGPTPDFReadCompleted(t, providerEvents2) {
		t.Fatal("captured final provider stream is not completed")
	}
	if !capturedClaudeGPTPDFReadClaudeStopped(t, clientResponse2) || !capturedClaudeGPTPDFReadClaudeStopped(t, translatedClientResponse2) {
		t.Fatal("captured or converted Claude final SSE is missing message_stop")
	}
}

func assertCapturedClaudeGPTPDFReadDeclaration(t *testing.T, captured, generated map[string]any) {
	t.Helper()
	for _, request := range []map[string]any{captured, generated} {
		tools := capturedClaudeGPTPNGReadArray(request["tools"])
		if len(tools) != 1 {
			t.Fatal("captured or converted first Responses request omitted its Read declaration")
		}
		tool, _ := tools[0].(map[string]any)
		if capturedClaudeGPTPNGReadString(tool["name"]) != "Read" || capturedClaudeGPTPNGReadString(tool["type"]) != "function" {
			t.Fatal("captured or converted Responses declaration is not the native Read function")
		}
		if capturedClaudeGPTPNGReadString(tool["description"]) == "" {
			t.Fatal("captured or converted Read declaration lost its description")
		}
	}
	capturedTool := capturedClaudeGPTPNGReadArray(captured["tools"])[0].(map[string]any)
	generatedTool := capturedClaudeGPTPNGReadArray(generated["tools"])[0].(map[string]any)
	capturedParameters, _ := json.Marshal(capturedTool["parameters"])
	generatedParameters, _ := json.Marshal(generatedTool["parameters"])
	requireLiveBodyJSONEqual(t, capturedParameters, generatedParameters)
}

func capturedClaudeGPTPDFReadCompleted(t *testing.T, events []map[string]any) bool {
	t.Helper()
	for _, event := range events {
		if capturedClaudeGPTPNGReadString(event["type"]) == "response.completed" {
			return capturedClaudeGPTPNGReadString(capturedClaudeGPTPNGReadNestedMap(event, "response")["status"]) == "completed"
		}
	}
	return false
}

func capturedClaudeGPTPDFReadClaudeStopped(t *testing.T, body []byte) bool {
	t.Helper()
	lastType := ""
	for index, frame := range capturedClaudeGPTPNGReadSSEFrames(t, body) {
		_, data, done, err := parseSSEFrame(frame)
		if err != nil {
			t.Fatalf("parse captured Claude terminal frame %d: %v", index, err)
		}
		if done {
			continue
		}
		var event map[string]any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&event); err != nil {
			t.Fatalf("decode captured Claude terminal frame %d: %v", index, err)
		}
		lastType = capturedClaudeGPTPNGReadString(event["type"])
	}
	return lastType == "message_stop"
}

func assertCapturedClaudeGPTPDFReadOriginalResult(t *testing.T, caseName, pages, answerSignal string) {
	t.Helper()
	var evaluation struct {
		CapturedCases []struct {
			Name               string `json:"name"`
			RecordedAccepted   bool   `json:"recorded_accepted"`
			RecordedErrorClass string `json:"recorded_error_class"`
			ActualReadPages    string `json:"actual_read_pages"`
			AnswerSignal       string `json:"captured_answer_signal"`
		} `json:"captured_cases"`
	}
	if err := json.Unmarshal(readCapturedClaudeGPTPDFReadFile(t, "evaluation.json"), &evaluation); err != nil {
		t.Fatal(err)
	}
	for _, recorded := range evaluation.CapturedCases {
		if recorded.Name == caseName {
			if recorded.RecordedAccepted || recorded.RecordedErrorClass != "unclassified" || recorded.ActualReadPages != pages || recorded.AnswerSignal != answerSignal {
				t.Fatal("historical PDF capture result or original page range was changed")
			}
			return
		}
	}
	t.Fatal("PDF evaluation metadata omitted the historical capture")
}

func TestCapturedClaudeGPTPDFReadKeepsCorrectedBodyReplaySeparateFromHistoricalFailure(t *testing.T) {
	t.Parallel()
	var evaluation struct {
		CorrectedReplay struct {
			CorrectedAccepted         bool `json:"corrected_accepted"`
			HistoricalAccepted        bool `json:"historical_accepted"`
			HistoricalResultPreserved bool `json:"historical_result_preserved"`
			ReplayOnly                bool `json:"replay_only"`
			ProviderDispatches        int  `json:"provider_dispatches"`
			Evidence                  struct {
				ClientReadArgsSHA256         string `json:"client_read_args_sha256"`
				PublicReadArgsSHA256         string `json:"public_read_args_sha256"`
				OriginalClientReadArgsSHA256 string `json:"original_client_read_args_sha256"`
				ClientMediaSHA256            string `json:"client_media_sha256"`
				PublicMediaSHA256            string `json:"public_media_sha256"`
				OriginalClientMediaSHA256    string `json:"original_client_media_sha256"`
				ClientReadCount              int    `json:"client_read_count"`
				PublicReadCount              int    `json:"public_read_count"`
				OriginalClientReadCount      int    `json:"original_client_read_count"`
				ClientMediaCount             int    `json:"client_media_count"`
				PublicMediaCount             int    `json:"public_media_count"`
				OriginalClientMediaCount     int    `json:"original_client_media_count"`
				PublicStreamClean            bool   `json:"public_stream_clean"`
				PublicStreamProven           bool   `json:"public_stream_proven"`
				PublicTerminal               bool   `json:"public_terminal"`
				ClientAnswer                 bool   `json:"client_answer"`
				ClientFinal                  bool   `json:"client_final"`
				ClientToolCall               bool   `json:"client_tool_call"`
				ClientToolResult             bool   `json:"client_tool_result"`
				PublicModel                  bool   `json:"public_model"`
				PublicMediumEffort           bool   `json:"public_medium_effort"`
				PublicEndpoint               bool   `json:"public_endpoint"`
				PublicMedia                  bool   `json:"public_media"`
				PublicToolCall               bool   `json:"public_tool_call"`
				PublicToolResult             bool   `json:"public_tool_result"`
			} `json:"evidence"`
		} `json:"corrected_replay"`
	}
	if err := json.Unmarshal(readCapturedClaudeGPTPDFReadFile(t, "evaluation.json"), &evaluation); err != nil {
		t.Fatal(err)
	}
	replay := evaluation.CorrectedReplay
	evidence := replay.Evidence
	if !replay.CorrectedAccepted || replay.HistoricalAccepted || !replay.HistoricalResultPreserved || !replay.ReplayOnly || replay.ProviderDispatches != 0 {
		t.Fatal("separate corrected replay overwrote history or was not a zero-dispatch replay")
	}
	if evidence.ClientReadArgsSHA256 == "" || evidence.ClientReadArgsSHA256 != evidence.PublicReadArgsSHA256 || evidence.ClientReadArgsSHA256 != evidence.OriginalClientReadArgsSHA256 {
		t.Fatal("corrected replay did not preserve exact Read arguments across all three evidence views")
	}
	if evidence.ClientMediaSHA256 != "0ab5e7e997a5eecf128d90494e111a00c6e9e79ccee690e280cd7e7fa8187b54" || evidence.ClientMediaSHA256 != evidence.PublicMediaSHA256 || evidence.ClientMediaSHA256 != evidence.OriginalClientMediaSHA256 {
		t.Fatal("corrected replay JPEG evidence does not match the captured content body")
	}
	if evidence.ClientReadCount != 1 || evidence.PublicReadCount != 1 || evidence.OriginalClientReadCount != 1 || evidence.ClientMediaCount != 1 || evidence.PublicMediaCount != 1 || evidence.OriginalClientMediaCount != 1 || !evidence.PublicStreamClean || !evidence.PublicStreamProven || !evidence.PublicTerminal || !evidence.ClientAnswer || !evidence.ClientFinal || !evidence.ClientToolCall || !evidence.ClientToolResult || !evidence.PublicModel || !evidence.PublicMediumEffort || !evidence.PublicEndpoint || !evidence.PublicMedia || !evidence.PublicToolCall || !evidence.PublicToolResult {
		t.Fatal("corrected replay omitted a required one-read, one-JPEG, clean-terminal evidence condition")
	}
}
