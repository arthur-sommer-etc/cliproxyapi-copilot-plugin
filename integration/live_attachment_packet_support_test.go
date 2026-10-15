//go:build !windows

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pollFreshLocalRegistry(ctx context.Context, client *http.Client, base string, maxPolls int, delay time.Duration, alive func() bool, observed func([]byte) error) ([]byte, int, error) {
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Path != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, 0, fmt.Errorf("local registry origin was not the owned loopback host")
	}
	localClient := *client
	localClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for poll := 1; poll <= maxPolls; poll++ {
		if !alive() {
			return nil, poll - 1, fmt.Errorf("native host exited before local registry became ready")
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models", nil)
		if err != nil {
			return nil, poll - 1, err
		}
		request.Header.Set("Authorization", "Bearer "+liveCopilotClientKey)
		response, err := localClient.Do(request)
		if err != nil {
			return nil, poll, fmt.Errorf("local registry read failed without retry: %w", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK {
			return body, poll, fmt.Errorf("local registry returned an unusable response")
		}
		if observed != nil {
			if err := observed(body); err != nil {
				return body, poll, err
			}
		}
		if liveModelCatalogHasTargets(body) {
			return body, poll, nil
		}
		if poll < maxPolls {
			select {
			case <-ctx.Done():
				return body, poll, ctx.Err()
			case <-time.After(delay):
			}
		}
	}
	return nil, maxPolls, fmt.Errorf("local registry remained unready after bounded polls")
}

func livePacketBinary() string { return strings.TrimSpace(os.Getenv("CPA_BINARY")) }
func livePacketPlugin() string { return strings.TrimSpace(os.Getenv("CPA_LIVE_PLUGIN_PATH")) }
func livePacketSandboxProfile() string {
	return strings.TrimSpace(os.Getenv("CPA_ATTACHMENT_CHILD_SANDBOX"))
}

func livePacketExecutable(path string, args ...string) (string, []string) {
	if os.Getenv("CPA_LIVE_CLIENT_ATTACHMENTS") != "1" && os.Getenv("CPA_OFFLINE_CLAUDE_PDF_READ") != "1" {
		return path, args
	}
	return "/usr/bin/sandbox-exec", append([]string{"-f", livePacketSandboxProfile(), path}, args...)
}

func livePacketPreflight(t *testing.T) string {
	t.Helper()
	if os.Getenv("CPA_LIVE_COPILOT_AUTH_MODE") != "token_exchange" {
		t.Fatal("attachment packet requires explicit token_exchange profile")
	}
	for _, path := range []string{livePacketBinary(), livePacketPlugin(), livePacketSandboxProfile()} {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatal("attachment packet requires explicit existing regular binary, plugin, and sandbox")
		}
	}
	profile, err := os.ReadFile(livePacketSandboxProfile())
	if err != nil || !strings.Contains(string(profile), "(deny network*)") || !strings.Contains(string(profile), `(allow network-outbound (remote ip "localhost:*"))`) {
		t.Fatal("fresh packet child sandbox is not loopback only")
	}
	diagnostics := os.Getenv("CPA_LIVE_COPILOT_DEBUG_DIR")
	if !filepath.IsAbs(diagnostics) {
		t.Fatal("fresh packet requires an absolute private diagnostics root")
	}
	root, err := os.Stat(diagnostics)
	if err != nil || !root.IsDir() || root.Mode().Perm() != 0700 {
		t.Fatal("fresh packet diagnostics root is unavailable or not private")
	}
	if _, err := os.ReadFile(filepath.Join(diagnostics, "dispatch-ledger.json")); err != nil {
		t.Fatal("shared physical dispatch ledger is unavailable")
	}
	return diagnostics
}

func livePacketWriteJSON(t *testing.T, directory, name string, value any) {
	t.Helper()
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal("packet evidence could not be encoded")
	}
	path := filepath.Join(directory, name)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal("packet evidence file could not be created")
	}
	if _, err = file.Write(content); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatal("packet evidence could not be durably saved")
	}
	dir, err := os.Open(directory)
	if err != nil {
		t.Fatal("packet evidence directory could not be opened for sync")
	}
	err = dir.Sync()
	closeErr = dir.Close()
	if err != nil || closeErr != nil {
		t.Fatal("packet evidence filename could not be durably saved")
	}
}

func livePacketCatalogHasModel(body []byte, model string) bool {
	var catalog struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &catalog) != nil {
		return false
	}
	for _, entry := range catalog.Data {
		if entry.ID == model {
			return true
		}
	}
	return false
}

func validateFreshPublicCatalog(body []byte) (int, error) {
	var catalog struct {
		Object string `json:"object"`
		Data   []struct {
			ID                 string `json:"id"`
			ModelPickerEnabled bool   `json:"model_picker_enabled"`
			Policy             struct {
				State string `json:"state"`
			} `json:"policy"`
			SupportedEndpoints []string `json:"supported_endpoints"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &catalog) != nil || catalog.Object != "list" || len(catalog.Data) == 0 {
		return 0, fmt.Errorf("public catalog schema was invalid")
	}
	expected := map[string]string{"gemini-3.8-flash": "/chat/completions", "gpt-6-luna": "/responses", "claude-haiku-5.5": "/v1/messages"}
	seen := make(map[string]bool)
	for _, row := range catalog.Data {
		endpoint, targeted := expected[row.ID]
		if !targeted {
			continue
		}
		if seen[row.ID] || !row.ModelPickerEnabled || row.Policy.State != "enabled" {
			return 0, fmt.Errorf("target model policy was not enabled and unique")
		}
		for _, supported := range row.SupportedEndpoints {
			if supported == endpoint {
				seen[row.ID] = true
			}
		}
		if !seen[row.ID] {
			return 0, fmt.Errorf("target model native endpoint was not advertised")
		}
	}
	if len(seen) != len(expected) {
		return 0, fmt.Errorf("exact target models were not all enabled")
	}
	return len(catalog.Data), nil
}

func liveFreshPublicCatalogFromGate(t *testing.T, gate *liveServerToolGate) ([]byte, string, int) {
	t.Helper()
	entries, err := os.ReadDir(gate.directory)
	if err != nil {
		t.Fatal("fresh catalog capture directory could not be read")
	}
	var body []byte
	var origin string
	captures := 0
	lastStatus := 0
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "gate-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		captureBytes, err := os.ReadFile(filepath.Join(gate.directory, entry.Name()))
		if err != nil {
			t.Fatal("fresh catalog capture could not be read")
		}
		var capture struct {
			Category             string `json:"category"`
			Dispatched           bool   `json:"dispatched"`
			OriginalPublicOrigin string `json:"original_public_origin"`
			PublicRequest        struct {
				Method string `json:"method"`
				URL    string `json:"url"`
			} `json:"public_request"`
			PublicResponse struct {
				Status int    `json:"status"`
				Body   string `json:"body"`
			} `json:"public_response"`
		}
		if json.Unmarshal(captureBytes, &capture) != nil {
			t.Fatal("fresh capture schema invalid")
		}
		if capture.Category != "catalog" {
			continue
		}
		if !capture.Dispatched || capture.PublicRequest.Method != http.MethodGet || capture.PublicRequest.URL != capture.OriginalPublicOrigin+"/models" || origin != "" && origin != capture.OriginalPublicOrigin {
			t.Fatal("fresh public catalog capture provenance invalid")
		}
		origin = capture.OriginalPublicOrigin
		captures++
		lastStatus = capture.PublicResponse.Status
		if capture.PublicResponse.Status == http.StatusUnauthorized {
			continue
		}
		if capture.PublicResponse.Status != http.StatusOK {
			t.Fatal("fresh public catalog had a terminal refusal")
		}
		body = []byte(capture.PublicResponse.Body)
	}
	if body == nil || lastStatus != http.StatusOK {
		t.Fatal("fresh public catalog capture missing")
	}
	return body, origin, captures
}

func livePacketDeniedCount(gate *liveServerToolGate) int {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return gate.deniedCount
}

func livePacketFileSHA256(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("packet could not hash a pinned artifact")
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func startLiveAttachmentHarness(t *testing.T, readiness func([]byte)) (*liveServerToolGate, string, func()) {
	endpointOverrides := liveEndpointOverrides(nil)
	delete(endpointOverrides, "claude-haiku-5.5")
	return startLiveCapturedHarness(t, readiness, endpointOverrides)
}

func startLiveCapturedHarness(t *testing.T, readiness func([]byte), endpointOverrides map[string]string) (*liveServerToolGate, string, func()) {
	t.Helper()
	binary := livePacketBinary()
	authPath := strings.TrimSpace(os.Getenv("CPA_LIVE_COPILOT_AUTH_FILE"))
	if binary == "" || authPath == "" || os.Getenv("CPA_LIVE_COPILOT_AUTH_MODE") != "token_exchange" {
		t.Fatal("attachment host requires explicit binary and copied token_exchange auth")
	}
	storageJSON, err := readLiveCopilotAuthFile(authPath)
	if err != nil {
		t.Fatal("copied auth file unavailable")
	}
	var storage struct {
		GitHubAccessToken string `json:"github_access_token"`
	}
	if json.Unmarshal(storageJSON, &storage) != nil || storage.GitHubAccessToken == "" {
		t.Fatal("copied auth file invalid")
	}
	diagnostics := livePacketPreflight(t)
	directory, err := os.MkdirTemp(diagnostics, "gate-")
	if err != nil {
		t.Fatal("private gate root unavailable")
	}
	gate := newLiveServerToolGateWithLedger(t, directory, diagnostics, storage.GitHubAccessToken)
	gate.armFreshOperation()
	check := func(body []byte) {
		_, _, _, _, captureErr := gate.countsFor("")
		if captureErr != nil || livePacketDeniedCount(gate) != 0 {
			t.Fatal("attachment startup capture or dispatch failed")
		}
		if readiness != nil {
			readiness(body)
		}
	}
	base, stop := startLiveNativeHostWithGate(t, binary, storageJSON, "token_exchange", endpointOverrides, gate.server.URL, check)
	return gate, base, stop
}
