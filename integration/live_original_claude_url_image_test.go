//go:build !windows

package integration

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const liveOriginalClaudeURLImageKey = "H-original-https/claude-url-image-native-origin"

// Claude's URL source has no MIME or detail fields. This case proves only the
// genuine URL carrier; it never fetches an image or constructs an API request.
func TestLiveOriginalClaudeHTTPSImage(t *testing.T) {
	if os.Getenv("CPA_LIVE_ORIGINAL_CLAUDE_HTTPS_IMAGE") != "1" {
		t.Skip("set CPA_LIVE_ORIGINAL_CLAUDE_HTTPS_IMAGE=1 for original Claude URL input")
	}
	_ = livePacketPreflight(t)
	imageURL := os.Getenv("CPA_LIVE_IMAGE_URL")
	parsed, err := url.Parse(imageURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		t.Fatal("an existing public HTTPS image source is required")
	}
	binary := os.Getenv("CPA_LIVE_CLAUDE_BINARY")
	var versionOutput bytes.Buffer
	_, versionErr := runLiveClient(t, liveClientStartupTimeout, binary, []string{"--version"}, func(command *exec.Cmd) {
		command.Stdout = &versionOutput
	})
	if versionErr != nil || strings.TrimSpace(versionOutput.String()) != os.Getenv("CPA_LIVE_CLAUDE_EXPECTED_VERSION")+" (Claude Code)" {
		t.Fatal("selected original Claude version did not match its declared version")
	}
	root, err := os.MkdirTemp(os.Getenv("CPA_LIVE_COPILOT_DEBUG_DIR"), "original-claude-url-image-")
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(home); err != nil {
			t.Error("could not remove disposable original Claude home")
		}
	})
	summary := map[string]any{"claim_cell": liveOriginalClaudeURLImageKey, "cli_version": strings.TrimSpace(versionOutput.String()), "image_url": imageURL, "carrier_scope": "url_only_no_independent_mime_or_detail", "stage": "host-start", "passed": false}
	defer func() {
		if err := writeOriginalInterruptJSON(filepath.Join(root, "validation-summary.json"), summary); err != nil {
			t.Error("could not retain original Claude URL validation summary")
		}
	}()
	gate, base, stop := startLiveCapturedHarness(t, nil, liveEndpointOverrides(nil))
	defer stop()
	if !liveCapturedCatalogAdvertisesEndpoint(gate, "claude-haiku-5.5", "/v1/messages") {
		t.Fatal("fresh catalog did not advertise the exact native Messages route")
	}
	if err := gate.setPhaseCell("H-original-https", "claude-url-image-native-origin"); err != nil {
		t.Fatal(err)
	}
	gate.setFreshClaim("inference", liveOriginalClaudeURLImageKey)
	defer gate.freezeCell(liveOriginalClaudeURLImageKey)
	defer gate.clearFreshInferenceClaim(liveOriginalClaudeURLImageKey)
	relayBase, err := originalInterruptRelayBaseURL(base)
	if err != nil {
		t.Fatal(err)
	}
	_, relayURL, stopRelay := startOriginalInterruptRelay(t, relayBase, root)
	defer func() {
		if err := stopRelay(); err != nil {
			t.Error("could not flush original Claude URL ingress log")
		}
	}()
	input, err := json.Marshal(map[string]any{"type": "user", "parent_tool_use_id": nil, "origin": map[string]any{"kind": "human"}, "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "Inspect this image. Reply with exactly top=COLOR;bottom=COLOR using lowercase color names."}, map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": imageURL}}}}})
	if err != nil {
		t.Fatal(err)
	}
	input = append(input, '\n')
	if err := os.WriteFile(filepath.Join(root, "stdin.jsonl"), input, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--safe-mode", "--print", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--no-session-persistence", "--permission-mode", "dontAsk", "--tools", "", "--model", "claude-haiku-5.5", "--effort", "medium"}
	stdout, stderr := &liveCLIBuffer{}, &liveCLIBuffer{}
	stdoutFile, err := os.OpenFile(filepath.Join(root, "stdout.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := stdoutFile.Close(); err != nil {
			t.Error("could not close original Claude stdout capture")
		}
	}()
	stderrFile, err := os.OpenFile(filepath.Join(root, "stderr.txt"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := stderrFile.Close(); err != nil {
			t.Error("could not close original Claude stderr capture")
		}
	}()
	summary["stage"] = "original-client"
	if err := writeOriginalInterruptJSON(filepath.Join(root, "validation-summary.json"), summary); err != nil {
		t.Fatal("could not retain pre-launch original Claude URL summary")
	}
	_, runErr := runLiveClient(t, liveClientTimeout, "/usr/bin/sandbox-exec", append([]string{"-f", livePacketSandboxProfile(), binary}, args...), func(command *exec.Cmd) {
		command.Dir, command.Env = home, append(liveCLIEnvironment("claude", strings.TrimSuffix(relayURL, "/v1"), home), "CLAUDE_CODE_MAX_RETRIES=0")
		command.Stdin = bytes.NewReader(input)
		command.Stdout, command.Stderr = io.MultiWriter(stdoutFile, stdout), io.MultiWriter(stderrFile, stderr)
	})
	if err := stdoutFile.Sync(); err != nil {
		t.Fatal("could not sync original Claude stdout capture")
	}
	if err := stderrFile.Sync(); err != nil {
		t.Fatal("could not sync original Claude stderr capture")
	}
	if err := stopRelay(); err != nil {
		t.Fatal("original Claude request/response capture did not flush")
	}
	summary["stage"] = "wire-validation"
	events, err := readOriginalInterruptWireLog(filepath.Join(root, "original-client-ingress.jsonl"))
	if err != nil {
		t.Fatal("original Claude ingress capture is unavailable")
	}
	originalCarriers := 0
	responseRoutes := make(map[uint64]bool)
	responseStatuses := make(map[uint64]int)
	for _, event := range events {
		if event.Kind == "http_request" {
			responseRoutes[event.RequestID] = event.Method == "POST" && event.URLPath == "/v1/messages"
		}
		if event.Kind == "http_response" {
			responseStatuses[event.RequestID] = event.Status
		}
	}
	summary["client_statuses"] = responseStatuses
	var clientText strings.Builder
	for _, event := range events {
		body, err := base64.StdEncoding.DecodeString(event.PayloadBase64)
		if err != nil || !event.Complete || !responseRoutes[event.RequestID] {
			continue
		}
		if event.Kind == "http_request_body" && event.Direction == "original_client_to_cpa" && originalClaudeURLImageRequest(body, imageURL) {
			originalCarriers++
		}
		if event.Kind == "http_response_body" && event.Direction == "cpa_to_original_client" && responseStatuses[event.RequestID] == 200 {
			if !liveCompleteMessagesTerminal(body, "claude-haiku-5.5") {
				t.Fatal("original client URL response lacks a complete Messages lifecycle, model or usage")
			}
			clientText.WriteString(originalClaudeImageStreamText(body))
		}
	}
	summary["original_url_requests"] = originalCarriers
	captures, err := readLiveGateCaptures(gate.directory)
	if err != nil {
		t.Fatal("complete public Claude URL captures are unavailable")
	}
	matched := 0
	var publicText strings.Builder
	for _, capture := range captures {
		if capture.Category != "inference" || capture.PhaseCell != liveOriginalClaudeURLImageKey {
			continue
		}
		parsed, err := url.Parse(capture.PublicRequest.URL)
		if err != nil || parsed.Path != "/v1/messages" || !originalClaudeURLImageRequest([]byte(capture.PublicRequest.Body), imageURL) {
			t.Fatal("original URL or exact native model/route was not preserved")
		}
		summary["public_status"] = capture.Response.Status
		if !liveCaptureStreamProven(capture, "claude-haiku-5.5") {
			t.Fatal("public Claude URL response lacks a proven complete terminal")
		}
		publicText.WriteString(originalClaudeImageStreamText([]byte(capture.Response.Body)))
		matched++
	}
	summary["upstream_captures"], summary["public_text"], summary["client_text"] = matched, publicText.String(), clientText.String()
	var resultText string
	for _, line := range bytes.Split(stdout.bytes, []byte("\n")) {
		var event map[string]any
		if json.Unmarshal(line, &event) == nil && event["type"] == "result" && event["is_error"] == false {
			resultText, _ = event["result"].(string)
		}
	}
	summary["result_text"] = resultText
	if matched == 0 || originalCarriers != matched || livePacketDeniedCount(gate) != 0 || runErr != nil || stdout.truncated || stderr.truncated {
		t.Fatal("original Claude URL carrier did not complete an allowed dispatch")
	}
	if publicText.String() != clientText.String() || publicText.String() != resultText {
		t.Fatal("provider, original client wire and actual CLI answer differ")
	}
	if !originalHTTPSImageAnswerMatchesFixture(resultText) {
		t.Fatal("original Claude URL image answer does not match actual fixture colors")
	}
	summary["stage"], summary["passed"] = "complete", true
}

func originalClaudeURLImageRequest(body []byte, imageURL string) bool {
	var request map[string]any
	if imageURL == "" || json.Unmarshal(body, &request) != nil || request["model"] != "claude-haiku-5.5" {
		return false
	}
	images := 0
	messages, _ := request["messages"].([]any)
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		content, _ := message["content"].([]any)
		for _, raw := range content {
			block, _ := raw.(map[string]any)
			if block["type"] != "image" {
				continue
			}
			source, _ := block["source"].(map[string]any)
			if message["role"] != "user" || len(block) != 2 || len(source) != 2 || source["type"] != "url" || source["url"] != imageURL {
				return false
			}
			images++
		}
	}
	return images == 1
}

func originalClaudeImageStreamText(body []byte) string {
	var text strings.Builder
	for _, event := range attachmentStreamObjects(string(body)) {
		if event["type"] == "content_block_start" {
			block, _ := event["content_block"].(map[string]any)
			if block["type"] == "text" {
				value, _ := block["text"].(string)
				text.WriteString(value)
			}
		}
		if event["type"] == "content_block_delta" {
			delta, _ := event["delta"].(map[string]any)
			if delta["type"] == "text_delta" {
				value, _ := delta["text"].(string)
				text.WriteString(value)
			}
		}
	}
	return text.String()
}

func TestOriginalClaudeURLImageCarrier(t *testing.T) {
	const imageURL = "https://example.com/image.png?literal=1"
	valid := `{"model":"claude-haiku-5.5","messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"` + imageURL + `"}}]}]}`
	for _, test := range []struct {
		name, body string
		want       bool
	}{
		{"original URL", valid, true},
		{"changed query", strings.ReplaceAll(valid, "literal=1", "literal=2"), false},
		{"wrong model", strings.ReplaceAll(valid, "claude-haiku-5.5", "gpt-6-luna"), false},
		{"inline replacement", strings.ReplaceAll(valid, `"type":"url"`, `"type":"base64"`), false},
		{"invented detail", strings.ReplaceAll(valid, `"type":"image",`, `"type":"image","detail":"high",`), false},
		{"invented MIME", strings.ReplaceAll(valid, `"type":"url",`, `"type":"url","media_type":"image/png",`), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := originalClaudeURLImageRequest([]byte(test.body), imageURL); got != test.want {
				t.Fatalf("original URL carrier = %t, want %t", got, test.want)
			}
		})
	}
}
