//go:build !windows

package integration

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image/png"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const liveOriginalHTTPSImageKey = "H-original-https/codex-image"

func liveOriginalHTTPSImageCell(key string) bool {
	return key == liveOriginalHTTPSImageKey || key == liveOriginalClaudeURLImageKey
}

// This case supplies an image URL to the original app-server; it never constructs
// a provider image request or downloads the image on the client's behalf.
func TestLiveOriginalCodexHTTPSImage(t *testing.T) {
	if os.Getenv("CPA_LIVE_ORIGINAL_HTTPS_IMAGE") != "1" {
		t.Skip("set CPA_LIVE_ORIGINAL_HTTPS_IMAGE=1 for an original app-server HTTPS image turn")
	}
	_ = livePacketPreflight(t)
	imageURL := os.Getenv("CPA_LIVE_IMAGE_URL")
	parsed, err := url.Parse(imageURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		t.Fatal("an existing public HTTPS image source is required")
	}
	driver := os.Getenv("CPA_LIVE_CODEX_IMAGE_DRIVER")
	info, err := os.Lstat(driver)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		t.Fatal("an existing private original app-server image driver is required")
	}
	var versionOutput bytes.Buffer
	_, versionErr := runLiveClient(t, liveClientStartupTimeout, os.Getenv("CPA_LIVE_CODEX_BINARY"), []string{"--version"}, func(command *exec.Cmd) {
		command.Stdout = &versionOutput
	})
	if versionErr != nil || strings.TrimSpace(versionOutput.String()) != "codex-cli "+os.Getenv("CPA_LIVE_CODEX_EXPECTED_VERSION") {
		t.Fatal("selected original app-server version did not match its declared version")
	}
	root, err := os.MkdirTemp(os.Getenv("CPA_LIVE_COPILOT_DEBUG_DIR"), "original-https-image-")
	if err != nil {
		t.Fatal(err)
	}
	summary := map[string]any{"claim_cell": liveOriginalHTTPSImageKey, "cli_version": strings.TrimSpace(versionOutput.String()), "image_url": imageURL, "stage": "host-start", "passed": false}
	defer func() {
		if err := writeOriginalInterruptJSON(filepath.Join(root, "validation-summary.json"), summary); err != nil {
			t.Error("could not retain original image validation summary")
		}
	}()
	gate, base, stop := startLiveCapturedHarness(t, nil, liveEndpointOverrides(nil))
	defer stop()
	if !liveCapturedCatalogAdvertisesEndpoint(gate, "gpt-6-luna", liveCopilotRoutes["gpt-6-luna"]) {
		t.Fatal("fresh catalog did not advertise the exact model Responses route")
	}
	if err := gate.setPhaseCell("H-original-https", "codex-image"); err != nil {
		t.Fatal(err)
	}
	gate.setFreshClaim("inference", liveOriginalHTTPSImageKey)
	defer gate.freezeCell(liveOriginalHTTPSImageKey)
	defer gate.clearFreshInferenceClaim(liveOriginalHTTPSImageKey)
	relayBase, err := originalInterruptRelayBaseURL(base)
	if err != nil {
		t.Fatal(err)
	}
	relay, relayURL, stopRelay := startOriginalInterruptRelay(t, relayBase, root)
	defer func() {
		if err := stopRelay(); err != nil {
			t.Error("could not flush original image ingress log")
		}
	}()
	stdout, err := os.OpenFile(filepath.Join(root, "driver-stdout.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	stderr, err := os.OpenFile(filepath.Join(root, "driver-stderr.txt"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	summary["stage"] = "original-app-server"
	_, runErr := runLiveClient(t, liveClientTimeout, python, []string{driver, "--base-url", relayURL, "--output-root", root, "--image-url", imageURL, "--supervised"}, func(command *exec.Cmd) {
		command.Env = append(os.Environ(), "TASK_PROXY_API_KEY="+liveCopilotClientKey)
		command.Stdout, command.Stderr = stdout, stderr
	})
	_ = stdout.Close()
	_ = stderr.Close()
	if err := stopRelay(); err != nil || relay.log.failure() != nil {
		t.Fatal("original image request/response capture did not flush")
	}
	body, err := os.ReadFile(filepath.Join(root, "driver-stdout.jsonl"))
	var outcome struct {
		Completed bool   `json:"completed"`
		Text      string `json:"text"`
		Error     string `json:"error"`
	}
	if err != nil || json.Unmarshal(body, &outcome) != nil {
		t.Fatal("original image driver did not retain a structured outcome")
	}
	summary["driver_outcome"] = outcome
	summary["stage"] = "wire-validation"
	events, err := readOriginalInterruptWireLog(filepath.Join(root, "original-client-ingress.jsonl"))
	if err != nil {
		t.Fatal("original image ingress capture is unavailable")
	}
	responseRoutes := make(map[uint64]bool)
	responseStatuses := make(map[uint64]int)
	for _, event := range events {
		if event.Kind == "http_request" {
			responseRoutes[event.RequestID] = event.Method == "POST" && event.URLPath == "/v1/responses"
		}
		if event.Kind == "http_response" {
			responseStatuses[event.RequestID] = event.Status
		}
	}
	originalCarriers := 0
	clientTerminals := make(map[string]map[string]any)
	for _, event := range events {
		if event.Kind == "http_response_body" && event.Direction == "cpa_to_original_client" && responseRoutes[event.RequestID] && responseStatuses[event.RequestID] == 200 {
			body, err := base64.StdEncoding.DecodeString(event.PayloadBase64)
			if err == nil && liveCompleteResponseTextTerminal(body, "gpt-6-luna") {
				events, err := liveHostedToolEvents(body)
				if err == nil {
					response, err := liveHostedToolSnapshot(t, "gpt-responses-search", events)
					if err == nil {
						clientTerminals[stringValue(response["id"])] = response
					}
				}
			}
		}
		if event.Kind != "http_request_body" || event.Direction != "original_client_to_cpa" || !event.Complete || !responseRoutes[event.RequestID] {
			continue
		}
		body, err := base64.StdEncoding.DecodeString(event.PayloadBase64)
		var request map[string]any
		if err == nil && json.Unmarshal(body, &request) == nil && originalHTTPSImageInRequest(request, imageURL) && request["model"] == "gpt-6-luna" && liveOriginalFrameEffort(request) == "medium" {
			originalCarriers++
		}
	}
	summary["original_url_requests"] = originalCarriers
	captures, err := readLiveGateCaptures(gate.directory)
	if err != nil {
		t.Fatal("complete public image request/response captures are unavailable")
	}
	matched := 0
	for _, capture := range captures {
		if capture.Category != "inference" || capture.PhaseCell != liveOriginalHTTPSImageKey {
			continue
		}
		parsed, err := url.Parse(capture.PublicRequest.URL)
		if err != nil || parsed.Path != "/responses" || capture.Response.Status != 200 || capture.CaptureTruncated {
			t.Fatal("original image dispatch did not complete on the exact Responses route")
		}
		var request map[string]any
		if json.Unmarshal([]byte(capture.PublicRequest.Body), &request) != nil || request["model"] != "gpt-6-luna" || liveOriginalFrameEffort(request) != "medium" {
			t.Fatal("original image request changed model or effort")
		}
		if !originalHTTPSImageInRequest(request, imageURL) {
			summary["url_carrier"] = "not_preserved"
			t.Fatal("original client did not preserve the exact HTTPS image URL and detail")
		}
		body := []byte(capture.Response.Body)
		if !liveCompleteResponseTextTerminal(body, "gpt-6-luna") || !liveCaptureStreamProven(capture, "gpt-6-luna") {
			t.Fatal("public image response lacks a proven complete text terminal")
		}
		events, err := liveHostedToolEvents(body)
		if err != nil {
			t.Fatal("public image stream was malformed or incomplete")
		}
		response, err := liveHostedToolSnapshot(t, "gpt-responses-search", events)
		if err != nil || !reflect.DeepEqual(response, clientTerminals[stringValue(response["id"])]) || liveHostedToolText(response) != outcome.Text {
			t.Fatal("public image lifecycle, original client terminal and actual RPC answer did not match")
		}
		matched++
	}
	summary["upstream_captures"] = matched
	if matched == 0 || originalCarriers != matched || livePacketDeniedCount(gate) != 0 {
		t.Fatal("no allowed original image provider dispatch was captured")
	}
	summary["url_carrier"] = "preserved"
	if runErr != nil || !outcome.Completed || outcome.Error != "" || !originalHTTPSImageAnswerMatchesFixture(outcome.Text) {
		t.Fatal("original image turn did not complete with the actual fixture colors")
	}
	summary["stage"], summary["passed"] = "complete", true
}

func originalHTTPSImageAnswerMatchesFixture(text string) bool {
	return strings.ToLower(strings.TrimSpace(text)) == "top=red;bottom=blue"
}

func TestOriginalHTTPSImageOracleMatchesActualFixture(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "original-client-attachments", "visual.png"))
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := png.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	bounds := fixture.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			red, green, blue, _ := fixture.At(x, y).RGBA()
			if y < bounds.Min.Y+bounds.Dy()/2 {
				if red <= green || red <= blue {
					t.Fatal("actual fixture top half is not red")
				}
			} else if blue <= red || blue <= green {
				t.Fatal("actual fixture bottom half is not blue")
			}
		}
	}
	if !originalHTTPSImageAnswerMatchesFixture("TOP=red;BOTTOM=blue") {
		t.Fatal("correct actual fixture colors were rejected")
	}
	for _, wrong := range []string{"LEFT=red;RIGHT=blue", "TOP=red;BOTTOM=red", "TOP=blue;BOTTOM=blue", "TOP=blue;BOTTOM=red"} {
		if originalHTTPSImageAnswerMatchesFixture(wrong) {
			t.Fatal("wrong fixture colors or orientation were accepted")
		}
	}
}

func originalHTTPSImageInRequest(request map[string]any, imageURL string) bool {
	input, _ := request["input"].([]any)
	for _, raw := range input {
		item, _ := raw.(map[string]any)
		content, _ := item["content"].([]any)
		for _, raw := range content {
			part, _ := raw.(map[string]any)
			if part["type"] == "input_image" && part["image_url"] == imageURL && part["detail"] == "high" {
				return true
			}
		}
	}
	return false
}

func TestOriginalHTTPSImageCarrierPreservesURLAndDetail(t *testing.T) {
	const source = "https://example.invalid/image.png?revision=1"
	request := func(urlValue, detail string) map[string]any {
		return map[string]any{"input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": urlValue, "detail": detail}}}}}
	}
	if !originalHTTPSImageInRequest(request(source, "high"), source) {
		t.Fatal("unchanged original URL carrier was rejected")
	}
	for _, changed := range []map[string]any{request("https://example.invalid/image.png", "high"), request(source, "auto"), request("data:image/png;base64,synthetic", "high")} {
		if originalHTTPSImageInRequest(changed, source) {
			t.Fatal("changed URL or image detail was accepted as original URL preservation")
		}
	}
}
