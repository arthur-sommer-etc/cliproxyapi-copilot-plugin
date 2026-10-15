//go:build !windows

package integration

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/redact"
)

var attachmentPNG = liveAttachmentFixturePath("visual.png", "CPA_ATTACHMENT_PNG_PATH")
var attachmentPDF = liveAttachmentFixturePath("document.pdf", "CPA_ATTACHMENT_PDF_PATH")
var attachmentMaintainedPoppler = strings.TrimSpace(os.Getenv("CPA_ATTACHMENT_PDF_RENDERER"))
var attachmentMaintainedFonts = strings.TrimSpace(os.Getenv("CPA_ATTACHMENT_FONTCONFIG_DIR"))

func liveAttachmentFixturePath(name, override string) string {
	if path := strings.TrimSpace(os.Getenv(override)); path != "" {
		return path
	}
	path, err := filepath.Abs(filepath.Join("testdata", "original-client-attachments", name))
	if err != nil {
		return ""
	}
	return path
}

const attachmentExpectedReadablePDFPageSHA256 = "0ab5e7e997a5eecf128d90494e111a00c6e9e79ccee690e280cd7e7fa8187b54"

type liveAttachmentCase struct {
	cell        string
	client      string
	model       string
	media       string
	fixture     string
	modelPath   string
	imageDetail string
}

type liveAttachmentCLI struct {
	exitCode  int
	truncated bool
	stdout    []byte
	stderr    []byte
	args      []string
}

type liveAttachmentEvidence struct {
	ClientFinal           bool   `json:"client_final"`
	ClientToolCall        bool   `json:"client_tool_call"`
	ClientToolResult      bool   `json:"client_tool_result"`
	ClientMedia           bool   `json:"client_media"`
	ClientMediaSHA256     string `json:"client_media_sha256,omitempty"`
	ClientMediaCount      int    `json:"client_media_count,omitempty"`
	ClientReadArgsSHA256  string `json:"client_read_args_sha256,omitempty"`
	ClientReadCount       int    `json:"client_read_count,omitempty"`
	ClientAnswer          bool   `json:"client_answer"`
	PublicModel           bool   `json:"public_model"`
	PublicEffort          bool   `json:"public_medium_effort"`
	PublicEndpoint        bool   `json:"public_endpoint"`
	PublicMedia           bool   `json:"public_media"`
	PublicMediaSHA256     string `json:"public_media_sha256,omitempty"`
	PublicMediaDetail     string `json:"public_media_detail,omitempty"`
	PublicMediaCount      int    `json:"public_media_count,omitempty"`
	PublicReadArgsSHA256  string `json:"public_read_args_sha256,omitempty"`
	PublicReadCount       int    `json:"public_read_count,omitempty"`
	PublicToolCall        bool   `json:"public_tool_call"`
	PublicToolResult      bool   `json:"public_tool_result"`
	PublicTerminal        bool   `json:"public_terminal"`
	PublicStreamClean     bool   `json:"public_stream_clean"`
	PublicStreamProven    bool   `json:"public_stream_proven"`
	RendererOutput        bool   `json:"renderer_output"`
	NativePDFPage         bool   `json:"native_pdf_page"`
	RendererSHA256        string `json:"renderer_sha256,omitempty"`
	IngressPrepared       bool   `json:"original_client_prepared"`
	IngressMediaSHA256    string `json:"original_client_media_sha256,omitempty"`
	IngressMediaDetail    string `json:"original_client_media_detail,omitempty"`
	IngressMediaCount     int    `json:"original_client_media_count,omitempty"`
	IngressReadArgsSHA256 string `json:"original_client_read_args_sha256,omitempty"`
	IngressReadCount      int    `json:"original_client_read_count,omitempty"`
	SourceSHA256          string `json:"fixture_sha256,omitempty"`
	SourcePreparedEqual   bool   `json:"source_prepared_equal"`
	PhysicalInference     int    `json:"physical_inference"`
}

func liveAttachmentCases() []liveAttachmentCase {
	return []liveAttachmentCase{
		{cell: "claude-gpt-png", client: "claude", model: "gpt-6-luna", media: "png", fixture: attachmentPNG, modelPath: "/responses"},
		{cell: "claude-gpt-pdf", client: "claude", model: "gpt-6-luna", media: "pdf", fixture: attachmentPDF, modelPath: "/responses"},
		{cell: "codex-claude-png", client: "codex", model: "claude-haiku-5.5", media: "png", fixture: attachmentPNG, modelPath: "/chat/completions", imageDetail: "high"},
		{cell: "codex-claude-pdf", client: "codex", model: "claude-haiku-5.5", media: "pdf", fixture: attachmentPDF, modelPath: "/chat/completions", imageDetail: "high"},
	}
}

func liveAttachmentPrompt(candidate liveAttachmentCase) string {
	switch candidate.cell {
	case "claude-gpt-png":
		return "Use the built-in Read tool to inspect the PNG file at " + candidate.fixture + ". Describe its top and bottom colors."
	case "claude-gpt-pdf":
		return "Use the built-in Read tool to inspect the PDF file at " + candidate.fixture + ". Report its printed code and the color of its element."
	case "codex-claude-png":
		return "Inspect the attached image and describe its top and bottom colors."
	case "codex-claude-pdf":
		return "Inspect the PDF at " + candidate.fixture + " using installed pdftoppm -png to render it into the current working directory, then use the built-in view_image tool on the rendered page. Report the printed code and element color."
	default:
		return ""
	}
}

func liveAttachmentArgs(candidate liveAttachmentCase, root string) ([]string, error) {
	prompt := liveAttachmentPrompt(candidate)
	if prompt == "" || candidate.model == "" {
		return nil, errors.New("unknown attachment case")
	}
	if candidate.client == "claude" {
		return []string{"--safe-mode", "--print", "--output-format", "stream-json", "--verbose", "--no-session-persistence", "--permission-mode", "dontAsk", "--tools", "Read", "--allowedTools", "Read", "--model", candidate.model, "--effort", "medium", prompt}, nil
	}
	if candidate.client != "codex" {
		return nil, errors.New("unknown attachment client")
	}
	sandbox := "read-only"
	if candidate.media == "pdf" {
		sandbox = "workspace-write"
	}
	args := []string{"exec", "--strict-config", "--json", "--ignore-rules", "--skip-git-repo-check", "--sandbox", sandbox, "--model", candidate.model, "--cd", root, "--config", "model_reasoning_effort=\"medium\""}
	if candidate.media == "png" {
		return append(args, prompt, "--image", candidate.fixture), nil
	}
	return append(args, prompt), nil
}

func liveAttachmentCodexConfiguration(base string) string {
	return fmt.Sprintf("model = \"claude-haiku-5.5\"\nmodel_provider = \"task_proxy\"\nweb_search = \"cached\"\n[model_providers.task_proxy]\nname = \"Task proxy\"\nbase_url = %q\nenv_key = \"TASK_PROXY_API_KEY\"\nwire_api = \"responses\"\nsupports_websockets = false\nrequest_max_retries = 0\nstream_max_retries = 0\n", base+"/v1")
}

func liveAttachmentOperationCell(candidate liveAttachmentCase, attempt string) (string, error) {
	if attempt == "" {
		return "X/" + candidate.cell, nil
	}
	if !regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`).MatchString(attempt) {
		return "", errors.New("attachment attempt identity is invalid")
	}
	return "X-" + attempt + "/" + candidate.cell, nil
}

func liveAttachmentPreparedPublicRequest(cell, path string, body []byte) bool {
	return liveAttachmentPreparedPublicRequestForFixture(cell, path, body, "")
}

func liveAttachmentPreparedPublicRequestForFixture(cell, path string, body []byte, fixturePath string) bool {
	if !liveAttachmentGateCell(cell) || path == "/copilot_internal/v2/token" || path == "/models" {
		return true
	}
	_, name, _ := strings.Cut(cell, "/")
	for _, candidate := range liveAttachmentCases() {
		if candidate.cell != name {
			continue
		}
		if candidate.client == "codex" && candidate.media == "pdf" {
			if fixturePath != "" {
				candidate.fixture = fixturePath
			}
			switch path {
			case "/v1/messages":
				return liveAttachmentMessagesRequest(body, candidate)
			case "/chat/completions":
				_, ok := liveAttachmentChatRequest(body, candidate)
				return ok
			default:
				return false
			}
		}
		if path != candidate.modelPath {
			return false
		}
		if candidate.modelPath == "/chat/completions" {
			_, ok := liveAttachmentChatRequest(body, candidate)
			return ok
		}
		var request map[string]any
		if json.Unmarshal(body, &request) != nil || request["model"] != candidate.model {
			return false
		}
		if candidate.client == "claude" {
			reasoning, _ := request["reasoning"].(map[string]any)
			return reasoning["effort"] == "medium"
		}
		outputConfig, _ := request["output_config"].(map[string]any)
		return outputConfig["effort"] == "medium"
	}
	return false
}

func liveAttachmentExpectedModel(cell string) string {
	if !liveAttachmentGateCell(cell) {
		return ""
	}
	_, name, _ := strings.Cut(cell, "/")
	for _, candidate := range liveAttachmentCases() {
		if candidate.cell == name {
			return candidate.model
		}
	}
	return ""
}

func liveAttachmentExpectedPublicEndpoints(candidate liveAttachmentCase) []string {
	if candidate.client == "codex" && candidate.media == "pdf" {
		return []string{"/v1/messages", "/chat/completions"}
	}
	return []string{candidate.modelPath}
}

func liveAttachmentSelectedCases(selection string) ([]liveAttachmentCase, error) {
	cases := liveAttachmentCases()
	if selection == "" {
		return cases, nil
	}
	known := make(map[string]liveAttachmentCase, len(cases))
	for _, candidate := range cases {
		known[candidate.cell] = candidate
	}
	selected := make([]liveAttachmentCase, 0, len(cases))
	seen := make(map[string]bool)
	for _, cell := range strings.Split(selection, ",") {
		candidate, ok := known[cell]
		if !ok || seen[cell] {
			return nil, errors.New("unknown or duplicate attachment case selection")
		}
		seen[cell] = true
		selected = append(selected, candidate)
	}
	return selected, nil
}

func liveAttachmentChangedCondition(candidate liveAttachmentCase) string {
	switch candidate.cell {
	case "claude-gpt-png":
		return "refreshed dependencies and matched CPA host; original Claude Code Read image path and fixture bytes"
	case "codex-claude-png":
		return "refreshed dependencies and catalog-advertised same-model Chat routing preserve original Codex high image detail"
	case "codex-claude-pdf":
		return "latest supported dependencies and matched CPA host; original workspace-write sandbox applies directly without the nested external Seatbelt wrapper"
	case "claude-gpt-pdf":
		return "refreshed dependencies and matched CPA host with existing Fontconfig resources; original Claude Code PDF Read JPEG page"
	default:
		return "none"
	}
}

func liveAttachmentClientExecutable(candidate liveAttachmentCase, path string, args ...string) (string, []string) {
	if candidate.client == "codex" && candidate.media == "pdf" {
		return path, args
	}
	return livePacketExecutable(path, args...)
}

func liveAttachmentClientSandbox(candidate liveAttachmentCase) string {
	if candidate.client == "codex" && candidate.media == "pdf" {
		return "native_codex_workspace_write"
	}
	return "outer_loopback_seatbelt"
}

func liveAttachmentRunCLI(t *testing.T, candidate liveAttachmentCase, base, root string) liveAttachmentCLI {
	t.Helper()
	return liveAttachmentRunCLIIsolated(t, candidate, base, root, root, root)
}

func liveAttachmentRunCLIIsolated(t *testing.T, candidate liveAttachmentCase, base, home, workspace, temp string) liveAttachmentCLI {
	t.Helper()
	path, err := exec.LookPath(candidate.client)
	if err != nil {
		t.Fatal("required original client is unavailable")
	}
	args, err := liveAttachmentArgs(candidate, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.client == "codex" {
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(liveAttachmentCodexConfiguration(base)), 0600); err != nil {
			t.Fatal("could not write isolated Codex config")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	commandPath, commandArgs := liveAttachmentClientExecutable(candidate, path, args...)
	command := exec.CommandContext(ctx, commandPath, commandArgs...)
	command.Dir = workspace
	command.Env = liveAttachmentIsolatedEnvironment(candidate, base, home, workspace, temp)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
	command.WaitDelay = 5 * time.Second
	stdout, stderr := &liveCLIBuffer{}, &liveCLIBuffer{}
	command.Stdout, command.Stderr = stdout, stderr
	err = command.Run()
	if command.Process != nil {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	result := liveAttachmentCLI{exitCode: -1, truncated: stdout.truncated || stderr.truncated, stdout: append([]byte(nil), stdout.bytes...), stderr: append([]byte(nil), stderr.bytes...), args: args}
	if command.ProcessState != nil {
		result.exitCode = command.ProcessState.ExitCode()
	}
	if ctx.Err() != nil {
		result.exitCode = -1
	}
	if err != nil && result.exitCode == 0 {
		result.exitCode = -1
	}
	return result
}

func liveAttachmentEnvironment(candidate liveAttachmentCase, base, root string) []string {
	env := liveCLIEnvironment(candidate.client, base, root)
	if candidate.media != "pdf" {
		return env
	}
	for index, value := range env {
		if strings.HasPrefix(value, "PATH=") {
			env[index] = "PATH=" + filepath.Dir(attachmentMaintainedPoppler) + string(os.PathListSeparator) + strings.TrimPrefix(value, "PATH=")
			break
		}
	}
	env = append(env, "FONTCONFIG_PATH="+attachmentMaintainedFonts, "FONTCONFIG_FILE="+filepath.Join(attachmentMaintainedFonts, "fonts.conf"))
	return env
}

func liveAttachmentIsolatedEnvironment(candidate liveAttachmentCase, base, home, workspace, temp string) []string {
	env := liveCLIEnvironment(candidate.client, base, home)
	for index, value := range env {
		if strings.HasPrefix(value, "TMPDIR=") {
			env[index] = "TMPDIR=" + temp
		}
		if candidate.client == "codex" && candidate.media == "pdf" && strings.HasPrefix(value, "XDG_CACHE_HOME=") {
			env[index] = "XDG_CACHE_HOME=" + filepath.Join(workspace, ".cache")
		}
	}
	if candidate.media != "pdf" {
		return env
	}
	for index, value := range env {
		if strings.HasPrefix(value, "PATH=") {
			env[index] = "PATH=" + filepath.Dir(attachmentMaintainedPoppler) + string(os.PathListSeparator) + strings.TrimPrefix(value, "PATH=")
			break
		}
	}
	return append(env, "FONTCONFIG_PATH="+attachmentMaintainedFonts, "FONTCONFIG_FILE="+filepath.Join(attachmentMaintainedFonts, "fonts.conf"))
}

func liveAttachmentStageFixture(t *testing.T, source, workspace string) string {
	t.Helper()
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal("original client attachment fixture could not be read")
	}
	staged := filepath.Join(workspace, filepath.Base(source))
	file, err := os.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal("could not stage the original attachment in the isolated client workspace")
	}
	written, writeErr := file.Write(original)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || written != len(original) || syncErr != nil || closeErr != nil {
		t.Fatal("staged original attachment could not be saved exactly")
	}
	stagedBytes, err := os.ReadFile(staged)
	if err != nil || !bytes.Equal(original, stagedBytes) {
		t.Fatal("staged client attachment does not match the original bytes")
	}
	originalHash := sha256.Sum256(original)
	stagedHash := sha256.Sum256(stagedBytes)
	if originalHash != stagedHash {
		t.Fatal("staged client attachment SHA256 differs from the original")
	}
	return staged
}

func liveAttachmentPathValue(env []string) string {
	for _, value := range env {
		if strings.HasPrefix(value, "PATH=") {
			return strings.TrimPrefix(value, "PATH=")
		}
	}
	return ""
}

func liveAttachmentPopplerPreflight(t *testing.T, candidate liveAttachmentCase, root string) string {
	t.Helper()
	if candidate.media != "pdf" {
		return ""
	}
	info, err := os.Stat(attachmentMaintainedPoppler)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		t.Fatal("maintained installed PDF renderer is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if candidate.client == "codex" {
		path, args := livePacketExecutable("/usr/bin/env", "pdftoppm", "-v")
		command := exec.CommandContext(ctx, path, args...)
		command.Dir = root
		command.Env = liveAttachmentEnvironment(candidate, "http://127.0.0.1:1", root)
		output, err := command.CombinedOutput()
		if err != nil || !bytes.Contains(output, []byte("pdftoppm version")) {
			t.Fatal("maintained renderer did not pass the isolated Codex child preflight")
		}
		return livePacketFileSHA256(t, attachmentMaintainedPoppler)
	}
	outputRoot := filepath.Join(root, "offline-render-probe")
	if err := os.MkdirAll(outputRoot, 0700); err != nil {
		t.Fatal("could not create task-owned renderer output root")
	}
	path, args := livePacketExecutable("/usr/bin/env", "pdftoppm", "-jpeg", "-r", "100", "-f", "1", "-l", "1", candidate.fixture, filepath.Join(outputRoot, "page"))
	command := exec.CommandContext(ctx, path, args...)
	command.Dir = root
	command.Env = liveAttachmentEnvironment(candidate, "http://127.0.0.1:1", root)
	output, err := command.CombinedOutput()
	page := filepath.Join(outputRoot, "page-1.jpg")
	pageBytes, readErr := os.ReadFile(page)
	if err != nil || readErr != nil || !bytes.HasPrefix(pageBytes, []byte("\xff\xd8\xff")) || len(output) != 0 {
		t.Fatal("maintained renderer did not produce one clean JPEG page under the isolated child environment and sandbox")
	}
	if err := os.Chmod(page, 0600); err != nil {
		t.Fatal("could not protect task-owned renderer preflight page")
	}
	pageHash := sha256.Sum256(pageBytes)
	if hex.EncodeToString(pageHash[:]) != attachmentExpectedReadablePDFPageSHA256 {
		t.Fatal("task-owned PDF renderer no longer produces the visually verified printed code and blue element")
	}
	return livePacketFileSHA256(t, attachmentMaintainedPoppler)
}

func liveAttachmentCaptureCLI(t *testing.T, directory string, candidate liveAttachmentCase, result liveAttachmentCLI) {
	t.Helper()
	for name, body := range map[string][]byte{"client-stdout.jsonl": result.stdout, "client-stderr.txt": result.stderr} {
		body = []byte(redact.Text(string(body), liveCopilotClientKey))
		if err := os.WriteFile(filepath.Join(directory, name), body, 0600); err != nil {
			t.Fatal("could not retain private client capture")
		}
	}
	livePacketWriteJSON(t, directory, "client-invocation.json", map[string]any{"client": candidate.client, "exact_model": candidate.model, "media": candidate.media, "fixture": candidate.fixture, "args": result.args, "exit_code": result.exitCode, "truncated": result.truncated})
}

func TestLiveCrossClientAttachmentsPacket(t *testing.T) {
	if os.Getenv("CPA_LIVE_CLIENT_ATTACHMENTS") != "1" {
		t.Skip("set CPA_LIVE_CLIENT_ATTACHMENTS=1 for the original-client attachment packet")
	}
	if os.Getenv("CPA_LIVE_SERVER_TOOLS_PACKET") == "1" {
		t.Fatal("attachment packet requires the fresh no-ceiling gate mode only")
	}
	diagnostics := livePacketPreflight(t)
	packetDirectory, err := os.MkdirTemp(diagnostics, "cross-client-attachments-")
	if err != nil || os.Chmod(packetDirectory, 0700) != nil {
		t.Fatal("could not create private attachment packet root")
	}
	selected, err := liveAttachmentSelectedCases(os.Getenv("CPA_LIVE_CLIENT_ATTACHMENTS_CASES"))
	if err != nil {
		t.Fatal(err)
	}
	attempt := os.Getenv("CPA_LIVE_CLIENT_ATTACHMENTS_ATTEMPT")
	cells := make([]string, 0, len(selected))
	rendererHashes := make(map[string]string)
	rendererPageHashes := make(map[string]string)
	for _, candidate := range selected {
		cell, err := liveAttachmentOperationCell(candidate, attempt)
		if err != nil {
			t.Fatal(err)
		}
		if attempt != "" && liveAttachmentChangedCondition(candidate) == "none" {
			t.Fatal("unchanged attachment case cannot be armed in a changed-evidence attempt")
		}
		cells = append(cells, cell)
		if err := liveAttachmentFixtureValid(candidate); err != nil {
			t.Fatal(err)
		}
		rendererHashes[candidate.cell] = liveAttachmentPopplerPreflight(t, candidate, packetDirectory)
		if candidate.cell == "claude-gpt-pdf" {
			rendererPageHashes[candidate.cell] = livePacketFileSHA256(t, filepath.Join(packetDirectory, "offline-render-probe", "page-1.jpg"))
		}
	}
	fontConfigHash := ""
	for _, candidate := range selected {
		if candidate.media == "pdf" {
			fontConfigHash = livePacketFileSHA256(t, filepath.Join(attachmentMaintainedFonts, "fonts.conf"))
			break
		}
	}
	expectedClaudeVersion := strings.TrimSpace(os.Getenv("CPA_LIVE_CLAUDE_EXPECTED_VERSION"))
	if expectedClaudeVersion != "" && !regexp.MustCompile(`^\d+\.\d+\.\d+ \(Claude Code\)$`).MatchString(expectedClaudeVersion) {
		t.Fatal("an explicit installed Claude Code version is required")
	}
	expectedVersions := map[string]string{}
	for _, candidate := range selected {
		envName := "CPA_LIVE_CODEX_EXPECTED_VERSION"
		if candidate.client == "claude" {
			envName = "CPA_LIVE_CLAUDE_EXPECTED_VERSION"
		}
		expected := strings.TrimSpace(os.Getenv(envName))
		if expected == "" {
			t.Fatal("selected original client requires explicit expected installed version")
		}
		expectedVersions[candidate.client] = expected
	}
	observedVersions := make(map[string]string)
	versionMatches := make(map[string]bool)
	for client, expected := range expectedVersions {
		path, err := exec.LookPath(client)
		if err != nil {
			observedVersions[client] = "unavailable"
			continue
		}
		commandPath, commandArgs := livePacketExecutable(path, "--version")
		versionContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		version, err := exec.CommandContext(versionContext, commandPath, commandArgs...).Output()
		cancel()
		observed := strings.TrimSpace(string(version))
		if err != nil || !regexp.MustCompile(`^(?:\d+\.\d+\.\d+ \(Claude Code\)|codex-cli \d+\.\d+\.\d+)$`).MatchString(observed) {
			observed = "unrecognized"
		}
		observedVersions[client] = observed
		versionMatches[client] = observed == expected
	}
	livePacketWriteJSON(t, packetDirectory, "startup-claim.json", map[string]any{"profile": "accepted_plugin_original_client_cross_model", "binary_sha256": livePacketFileSHA256(t, livePacketBinary()), "plugin_sha256": livePacketFileSHA256(t, livePacketPlugin()), "sandbox_sha256": livePacketFileSHA256(t, livePacketSandboxProfile()), "auth_mode": "token_exchange", "catalog_mode": "fresh_normal_sdk", "numeric_ceiling": "none", "attempt": attempt, "cases": cells, "renderer_sha256_by_case": rendererHashes, "fontconfig_sha256": fontConfigHash, "offline_rendered_jpeg_sha256_by_case": rendererPageHashes, "expected_client_versions": expectedVersions, "observed_client_versions": observedVersions, "client_version_match": versionMatches})
	for client := range expectedVersions {
		if !versionMatches[client] {
			t.Fatal("original client version changed")
		}
	}
	var readiness []byte
	gate, base, stop := startLiveAttachmentHarness(t, func(body []byte) { readiness = append([]byte(nil), body...) })
	defer stop()
	if !liveModelCatalogHasTargets(readiness) || livePacketDeniedCount(gate) != 0 {
		t.Fatal("fresh exact-model startup did not complete")
	}
	publicCatalog, origin, _ := liveFreshPublicCatalogFromGate(t, gate)
	if _, err := validateFreshPublicCatalog(publicCatalog); err != nil {
		t.Fatal("fresh public catalog was invalid")
	}
	for _, candidate := range selected {
		for _, endpoint := range liveAttachmentExpectedPublicEndpoints(candidate) {
			if !liveFreshCatalogAdvertisesEndpoint(publicCatalog, candidate.model, endpoint) {
				t.Fatalf("fresh public catalog does not advertise %s for the exact model %s", endpoint, candidate.model)
			}
		}
	}
	catalogHash := sha256.Sum256(publicCatalog)
	livePacketWriteJSON(t, packetDirectory, "startup.json", map[string]any{"public_origin": origin, "catalog_sha256": hex.EncodeToString(catalogHash[:]), "gate_directory": gate.directory})
	for _, candidate := range selected {
		candidate := candidate
		t.Run(candidate.cell, func(t *testing.T) {
			cell, _ := liveAttachmentOperationCell(candidate, attempt)
			liveRunAttachmentCase(t, gate, base, packetDirectory, candidate, cell, attempt, rendererHashes[candidate.cell])
		})
	}
	t.Logf("retained private cross-client packet: %s", packetDirectory)
}

func liveAttachmentFixtureValid(candidate liveAttachmentCase) error {
	info, err := os.Stat(candidate.fixture)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return errors.New("task-owned attachment fixture missing")
	}
	if candidate.media == "png" && info.Size() != 177 || candidate.media == "pdf" && info.Size() != 623 {
		return errors.New("task-owned attachment fixture size changed")
	}
	body, err := os.ReadFile(candidate.fixture)
	if err != nil {
		return errors.New("task-owned attachment fixture could not be read")
	}
	hash := sha256.Sum256(body)
	want := "648a0925b839950a3546fb16cae8db3819b4c5ab6288f4051b02daaa5fd9c02e"
	if candidate.media == "pdf" {
		want = "e8627ac30088e8d989b1e265018ac32bc5b7b94bb4fdb51954deaff0c3ff2d68"
	}
	if hex.EncodeToString(hash[:]) != want {
		return errors.New("task-owned attachment fixture hash changed")
	}
	return nil
}

func liveRunAttachmentCase(t *testing.T, gate *liveServerToolGate, base, packetDirectory string, candidate liveAttachmentCase, cell, attempt, rendererSHA string) {
	t.Helper()
	caseDirectory, err := os.MkdirTemp(packetDirectory, candidate.cell+"-")
	if err != nil || os.Chmod(caseDirectory, 0700) != nil {
		t.Fatal("could not create private case evidence directory")
	}
	home := filepath.Join(caseDirectory, "client-home")
	workspace := filepath.Join(caseDirectory, "client-workspace")
	temp := filepath.Join(caseDirectory, "client-tmp")
	for _, path := range []string{home, workspace, temp} {
		if err := os.MkdirAll(path, 0700); err != nil || os.Chmod(path, 0700) != nil {
			t.Fatal("could not create isolated client home, workspace, or temporary directory")
		}
	}
	originalFixture := candidate.fixture
	candidate.fixture = liveAttachmentStageFixture(t, originalFixture, workspace)
	if candidate.client == "codex" && candidate.media == "pdf" {
		fontconfigCache := filepath.Join(workspace, ".cache", "fontconfig")
		if err := os.MkdirAll(fontconfigCache, 0700); err != nil || os.Chmod(fontconfigCache, 0700) != nil {
			t.Fatal("could not create writable workspace Fontconfig cache for native Codex PDF rendering")
		}
		probe := filepath.Join(fontconfigCache, ".write-probe")
		if err := os.WriteFile(probe, []byte("workspace-write"), 0600); err != nil {
			t.Fatal("native Codex Fontconfig cache is not writable inside its workspace")
		}
		if err := os.Remove(probe); err != nil {
			t.Fatal("native Codex Fontconfig cache write probe could not be cleaned up")
		}
	}
	gate.setAttachmentFixture(cell, candidate.fixture)
	args, err := liveAttachmentArgs(candidate, workspace)
	if err != nil {
		t.Fatal(err)
	}
	before, _, _, _, captureErr := gate.countsFor(cell)
	if captureErr != nil || livePacketDeniedCount(gate) != 0 || !livePacketCatalogHasModel(readinessFromGate(t, gate), candidate.model) {
		t.Fatal("candidate lost exact model or capture readiness")
	}
	livePacketWriteJSON(t, caseDirectory, "claim.json", map[string]any{"cell": cell, "prior_cell": "X/" + candidate.cell, "attempt": attempt, "changed_condition": liveAttachmentChangedCondition(candidate), "client": candidate.client, "model": candidate.model, "media": candidate.media, "original_fixture": originalFixture, "original_fixture_sha256": livePacketFileSHA256(t, originalFixture), "staged_fixture": candidate.fixture, "staged_fixture_sha256": livePacketFileSHA256(t, candidate.fixture), "client_home": home, "client_workspace": workspace, "client_tmp": temp, "client_sandbox": liveAttachmentClientSandbox(candidate), "original_cli_args": args, "requested_effort": "medium", "expected_public_path": candidate.modelPath, "expected_public_paths": liveAttachmentExpectedPublicEndpoints(candidate), "inference_before": before, "renderer_executable": map[bool]string{true: attachmentMaintainedPoppler, false: ""}[candidate.media == "pdf"], "renderer_sha256": rendererSHA, "fontconfig_file": map[bool]string{true: filepath.Join(attachmentMaintainedFonts, "fonts.conf"), false: ""}[candidate.media == "pdf"], "isolated_child_path": liveAttachmentPathValue(liveAttachmentIsolatedEnvironment(candidate, base, home, workspace, temp))})
	phase, name, _ := strings.Cut(cell, "/")
	if err := gate.setPhaseCell(phase, name); err != nil {
		t.Fatal(err)
	}
	ingress := newLiveAttachmentIngress(t, gate, base, filepath.Join(caseDirectory, "original-client-hop"), candidate.client)
	gate.setFreshClaim("inference", cell)
	result := liveAttachmentRunCLIIsolated(t, candidate, ingress.URL(), home, workspace, temp)
	gate.clearFreshInferenceClaim(cell)
	ingress.Close()
	liveAttachmentCaptureCLI(t, caseDirectory, candidate, result)
	after, total, auth, catalog, captureErr := gate.countsFor(cell)
	ingressCaptures, ingressReadErr := ingress.captures()
	ingressDenials, ingressFailure := ingress.state()
	ingressCaptureFailure, ingressRefusalStatus := ingress.failureState()
	evidence := inspectLiveAttachmentEvidence(t, gate, candidate, cell, result, ingressCaptures)
	inspectLiveOriginalClientAttachment(candidate, ingressCaptures, &evidence)
	evidence.PhysicalInference = after - before
	needsTool := candidate.client == "claude" || candidate.media == "pdf"
	mediaPrepared := liveAttachmentMediaPrepared(candidate, evidence)
	accepted := result.exitCode == 0 && !result.truncated && captureErr == nil && ingressReadErr == nil && ingressFailure == nil && ingressDenials == 0 && livePacketDeniedCount(gate) == 0 && evidence.PhysicalInference > 0 && evidence.ClientFinal && (!needsTool || evidence.ClientToolCall && evidence.ClientToolResult) && evidence.ClientMedia && evidence.ClientAnswer && evidence.IngressPrepared && mediaPrepared && evidence.PublicModel && evidence.PublicEffort && evidence.PublicEndpoint && evidence.PublicMedia && evidence.IngressMediaSHA256 == evidence.PublicMediaSHA256 && (!needsTool || evidence.PublicToolCall && evidence.PublicToolResult) && evidence.PublicTerminal && evidence.PublicStreamProven && (candidate.client != "codex" || candidate.media != "pdf" || evidence.RendererOutput)
	livePacketWriteJSON(t, caseDirectory, "result.json", map[string]any{"evidence": evidence, "accepted": accepted, "exit_code": result.exitCode, "error_class": safeCLIErrorCode(append(append([]byte(nil), result.stdout...), result.stderr...)), "capture_error": captureErr != nil || ingressCaptureFailure || ingressReadErr != nil, "original_client_refusal_status": ingressRefusalStatus, "pre_egress_refusal": ingressRefusalStatus != 0 && evidence.PhysicalInference == 0, "gate_denials": livePacketDeniedCount(gate), "original_client_denials": ingressDenials, "total_inference": total, "auth": auth, "catalog": catalog, "raw_client_and_gate_captures_retained": true})
	if !accepted {
		gate.freezeCell(cell)
		t.Errorf("original-client attachment did not complete: client=%s model=%s media=%s", candidate.client, candidate.model, candidate.media)
	}
}

func liveAttachmentMediaPrepared(candidate liveAttachmentCase, evidence liveAttachmentEvidence) bool {
	if candidate.modelPath == "/chat/completions" && candidate.client == "codex" {
		return evidence.IngressMediaDetail != "" && evidence.IngressMediaDetail == evidence.PublicMediaDetail && (candidate.imageDetail == "" || evidence.PublicMediaDetail == candidate.imageDetail) && (evidence.SourcePreparedEqual || candidate.media == "pdf")
	}
	if evidence.SourcePreparedEqual {
		return true
	}
	if candidate.client == "codex" && candidate.media == "pdf" {
		return true
	}
	return candidate.cell == "claude-gpt-pdf" && evidence.NativePDFPage && evidence.ClientMediaSHA256 != "" && evidence.ClientMediaSHA256 == evidence.IngressMediaSHA256 && evidence.SourceSHA256 != evidence.IngressMediaSHA256 && evidence.ClientMediaCount == 1 && evidence.IngressMediaCount == 1 && evidence.PublicMediaCount == 1 && evidence.ClientReadCount == 1 && evidence.IngressReadCount == 1 && evidence.PublicReadCount == 1 && evidence.ClientReadArgsSHA256 != "" && evidence.ClientReadArgsSHA256 == evidence.IngressReadArgsSHA256 && evidence.IngressReadArgsSHA256 == evidence.PublicReadArgsSHA256
}

func liveAttachmentNativePDFPageOne(pages string) bool {
	if pages == "1" {
		return true
	}
	start, endText, ranged := strings.Cut(pages, "-")
	if !ranged || start != "1" {
		return false
	}
	end, err := strconv.Atoi(endText)
	return err == nil && end >= 1 && end <= 20 && strconv.Itoa(end) == endText
}

func liveAttachmentArgumentSHA256(input map[string]any) string {
	if input == nil {
		return ""
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func readinessFromGate(t *testing.T, gate *liveServerToolGate) []byte {
	t.Helper()
	entries, err := os.ReadDir(gate.directory)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "gate-") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(gate.directory, entry.Name()))
		if err != nil {
			continue
		}
		var capture liveServerToolCapture
		if json.Unmarshal(body, &capture) == nil && capture.Category == "catalog" && capture.Response.Status == 200 {
			return []byte(capture.Response.Body)
		}
	}
	return nil
}

func inspectLiveAttachmentEvidence(t *testing.T, gate *liveServerToolGate, candidate liveAttachmentCase, cell string, client liveAttachmentCLI, ingress ...[]liveAttachmentIngressCapture) liveAttachmentEvidence {
	t.Helper()
	evidence := inspectLiveAttachmentClient(candidate, client.stdout)
	entries, err := os.ReadDir(gate.directory)
	if err != nil {
		return evidence
	}
	responseCalls := make(map[string]string)
	requestResults := make(map[string]bool)
	requestCommandSuccess := make(map[string]bool)
	toolInputs := make(map[string]map[string]any)
	originalCalls := make(map[string]liveAttachmentCapturedToolCall)
	if len(ingress) > 0 {
		originalCalls = liveAttachmentOriginalResponsesToolCalls(ingress[0])
	}
	publicReadArguments := make(map[string]string)
	publicMediaHashes := make(map[string]bool)
	publicMediaDetails := make(map[string]string)
	publicMediaDetailConflict := false
	allModel, allEffort, allEndpoint, allStreamsClean, allStreamsProven, sawDispatch := true, true, true, true, true, false
	rendererRoot := ""
	for index, arg := range client.args {
		if arg == "--cd" && index+1 < len(client.args) {
			rendererRoot = client.args[index+1]
		}
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "gate-") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(gate.directory, entry.Name()))
		if err != nil {
			return evidence
		}
		var capture liveServerToolCapture
		if json.Unmarshal(body, &capture) != nil || capture.Category != "inference" || capture.PhaseCell != cell || !capture.Dispatched {
			continue
		}
		sawDispatch = true
		evidence.PhysicalInference++
		publicPath := liveAttachmentPublicPath(capture.PublicRequest.URL)
		allEndpoint = allEndpoint && liveAttachmentPreparedPublicRequestForFixture("X/"+candidate.cell, publicPath, []byte(capture.PublicRequest.Body), candidate.fixture)
		var request map[string]any
		parsed := json.Unmarshal([]byte(capture.PublicRequest.Body), &request) == nil
		allModel = allModel && parsed && request["model"] == candidate.model
		var chatRequest liveAttachmentChatRequestEvidence
		chatRequestValid := false
		if publicPath == "/chat/completions" {
			chatRequest, chatRequestValid = liveAttachmentChatRequest([]byte(capture.PublicRequest.Body), candidate)
			allModel = allModel && chatRequestValid && chatRequest.Model == candidate.model
			allEffort = allEffort && chatRequestValid && chatRequest.Effort == "medium"
		} else if candidate.client == "claude" {
			reasoning, _ := request["reasoning"].(map[string]any)
			allEffort = allEffort && reasoning["effort"] == "medium"
		} else {
			outputConfig, _ := request["output_config"].(map[string]any)
			allEffort = allEffort && outputConfig["effort"] == "medium"
		}
		if capture.Response.Status >= 200 && capture.Response.Status < 300 {
			clean := !capture.CaptureTruncated && capture.ErrorClass == ""
			allStreamsClean = allStreamsClean && clean
			allStreamsProven = allStreamsProven && liveCaptureStreamProven(capture, candidate.model)
		}
		if capture.Response.Status < 200 || capture.Response.Status >= 300 {
			continue
		}
		requestHadPriorToolResult := false
		requestHadRequiredToolResult := false
		chatCarrierNames := map[string]string(nil)
		if chatRequestValid && candidate.client == "codex" && candidate.media == "pdf" && len(ingress) > 0 {
			messagesCalls := make(map[string]liveAttachmentCapturedToolCall, len(responseCalls))
			for id, name := range responseCalls {
				messagesCalls[id] = liveAttachmentCapturedToolCall{ItemID: "fc_" + id, CallID: id, Name: name, Arguments: toolInputs[id]}
			}
			var correlated bool
			chatCarrierNames, correlated = liveAttachmentChatHistoryCorrelations(chatRequest, originalCalls, messagesCalls)
			allEndpoint = allEndpoint && correlated
			for _, call := range chatRequest.ToolCalls {
				name := chatCarrierNames[call.ID]
				if name != "" {
					responseCalls[call.ID] = name
					toolInputs[call.ID] = call.Arguments
				}
			}
		}
		if chatRequestValid {
			for _, image := range chatRequest.Images {
				publicMediaHashes[image.SHA256] = true
				if previous, exists := publicMediaDetails[image.SHA256]; exists && previous != image.Detail {
					publicMediaDetailConflict = true
				}
				publicMediaDetails[image.SHA256] = image.Detail
			}
			for _, result := range chatRequest.ToolResults {
				id := result.ID
				if id == "" {
					continue
				}
				callName := responseCalls[id]
				success := liveChatAttachmentToolResultSucceeded(result, callName, candidate)
				requestResults[id] = success
				requestCommandSuccess[id] = success
				requestHadPriorToolResult = requestHadPriorToolResult || callName != "" && success
				requestHadRequiredToolResult = requestHadRequiredToolResult || candidate.client == "codex" && candidate.media == "pdf" && callName == "view_image" && success
			}
		}
		walkAttachmentJSON(request, func(item map[string]any) {
			typeName, _ := item["type"].(string)
			if typeName == "input_image" || typeName == "input_file" || typeName == "image" || typeName == "document" {
				PublicMedia := candidate.media
				if candidate.client == "codex" && candidate.media == "pdf" {
					PublicMedia = "png"
				}
				if candidate.client == "claude" && candidate.media == "pdf" {
					PublicMedia = "jpeg"
				}
				mediaBytes, ok := liveAttachmentMediaBytes(item, PublicMedia)
				if !ok && candidate.client == "claude" && candidate.media == "pdf" {
					mediaBytes, ok = liveAttachmentMediaBytes(item, "pdf")
				}
				if ok {
					hash := sha256.Sum256(mediaBytes)
					publicMediaHashes[hex.EncodeToString(hash[:])] = true
					if candidate.cell == "claude-gpt-pdf" {
						evidence.PublicMediaCount++
					}
				}
			}
			if typeName == "function_call_output" || typeName == "tool_result" {
				id := attachmentString(item, "call_id", "tool_use_id")
				if id != "" {
					requestResults[id] = true
					requestCommandSuccess[id] = liveAttachmentCommandOutputSuccess(item)
					requestHadPriorToolResult = requestHadPriorToolResult || responseCalls[id] != ""
					if candidate.client == "claude" && responseCalls[id] == "Read" || candidate.client == "codex" && candidate.media == "pdf" && responseCalls[id] == "view_image" {
						requestHadRequiredToolResult = true
					}
				}
			}
		})
		if publicPath == "/chat/completions" {
			stream, valid := liveChatCompletionStream([]byte(capture.Response.Body), candidate.model)
			if valid {
				for _, call := range stream.ToolCalls {
					responseCalls[call.ID] = call.Name
					toolInputs[call.ID] = call.Arguments
					if call.Name == "Read" || call.Name == "view_image" || call.Name == "exec_command" || call.Name == "shell_command" {
						evidence.PublicToolCall = true
					}
					if candidate.cell == "claude-gpt-pdf" && call.Name == "Read" {
						publicReadArguments[call.ID] = liveAttachmentArgumentSHA256(call.Arguments)
					}
				}
			}
		} else {
			for _, event := range attachmentStreamObjects(capture.Response.Body) {
				walkAttachmentJSON(event, func(item map[string]any) {
					typeName, _ := item["type"].(string)
					if typeName != "function_call" && typeName != "tool_use" {
						return
					}
					if event["type"] == "content_block_start" && typeName == "tool_use" {
						return
					}
					id := attachmentString(item, "call_id", "id")
					name := attachmentString(item, "name")
					if id != "" && name != "" {
						responseCalls[id] = name
						if input, ok := item["input"].(map[string]any); ok {
							toolInputs[id] = input
						} else if rawArguments, ok := item["arguments"].(string); ok {
							var input map[string]any
							if json.Unmarshal([]byte(rawArguments), &input) == nil {
								toolInputs[id] = input
							}
						}
						if name == "Read" || name == "view_image" || name == "exec_command" || name == "shell_command" {
							evidence.PublicToolCall = true
						}
						if candidate.cell == "claude-gpt-pdf" && name == "Read" {
							publicReadArguments[id] = liveAttachmentArgumentSHA256(toolInputs[id])
						}
					}
				})
			}
		}
		needsTool := candidate.client == "claude" || candidate.media == "pdf"
		evidence.PublicTerminal = evidence.PublicTerminal || liveCaptureStreamProven(capture, candidate.model) && liveAttachmentCapturedResponseCompleted(capture.Response.Body, candidate, publicPath) && (!needsTool || requestHadPriorToolResult && requestHadRequiredToolResult)
	}
	if len(publicMediaHashes) == 1 {
		evidence.PublicMedia = true
		for hash := range publicMediaHashes {
			evidence.PublicMediaSHA256 = hash
			if !publicMediaDetailConflict {
				evidence.PublicMediaDetail = publicMediaDetails[hash]
			}
		}
	}
	for id, name := range responseCalls {
		if requestResults[id] && (name == "Read" || name == "view_image" || name == "exec_command" || name == "shell_command") {
			evidence.PublicToolResult = true
		}
	}
	if candidate.cell == "claude-gpt-pdf" {
		evidence.PublicReadCount = len(publicReadArguments)
		for id, hash := range publicReadArguments {
			if requestResults[id] {
				evidence.PublicReadArgsSHA256 = hash
			}
		}
	}
	if candidate.client == "codex" && candidate.media == "pdf" {
		rendered, viewed := false, false
		prefixes := make(map[string]bool)
		for id, name := range responseCalls {
			if !requestResults[id] || !requestCommandSuccess[id] {
				continue
			}
			if name == "exec_command" || name == "shell_command" {
				if prefix, ok := liveAttachmentRendererPrefix(attachmentString(toolInputs[id], "cmd", "command"), candidate.fixture, rendererRoot); ok {
					prefixes[prefix] = true
					rendered = true
				}
			}
		}
		images, err := filepath.Glob(filepath.Join(rendererRoot, "*.png"))
		if err == nil {
			for _, imagePath := range images {
				body, readErr := os.ReadFile(imagePath)
				if readErr == nil && bytes.HasPrefix(body, []byte("\x89PNG\r\n\x1a\n")) && liveAttachmentRendererOutputMatchesPrefix(imagePath, prefixes) {
					hash := sha256.Sum256(body)
					if hex.EncodeToString(hash[:]) == evidence.PublicMediaSHA256 {
						for id, name := range responseCalls {
							viewedPath := attachmentString(toolInputs[id], "path")
							if viewedPath != "" && !filepath.IsAbs(viewedPath) {
								viewedPath = filepath.Join(rendererRoot, viewedPath)
							}
							if name == "view_image" && requestResults[id] && filepath.Clean(viewedPath) == filepath.Clean(imagePath) {
								viewed = true
								evidence.RendererOutput = true
								evidence.RendererSHA256 = hex.EncodeToString(hash[:])
							}
						}
					}
				}
			}
		}
		evidence.PublicToolCall = rendered && viewed
		evidence.PublicToolResult = rendered && viewed
	}
	evidence.PublicModel = sawDispatch && allModel
	evidence.PublicEffort = sawDispatch && allEffort
	evidence.PublicEndpoint = sawDispatch && allEndpoint
	evidence.PublicStreamClean = sawDispatch && allStreamsClean
	evidence.PublicStreamProven = sawDispatch && allStreamsProven
	return evidence
}

func liveCaptureStreamProven(capture liveServerToolCapture, expectedModel string) bool {
	if capture.CaptureTruncated || capture.Response.Status < 200 || capture.Response.Status >= 300 {
		return false
	}
	terminal := false
	switch {
	case strings.HasSuffix(capture.PublicRequest.URL, "/chat/completions"):
		_, terminal = liveChatCompletionStream([]byte(capture.Response.Body), expectedModel)
	case strings.HasSuffix(capture.PublicRequest.URL, "/responses"):
		terminal = liveCompleteResponseTerminal([]byte(capture.Response.Body), expectedModel)
	case strings.HasSuffix(capture.PublicRequest.URL, "/v1/messages"):
		terminal = liveCompleteMessagesTerminal([]byte(capture.Response.Body), expectedModel)
	}
	if !terminal {
		return false
	}
	if capture.ErrorClass == "" {
		return true
	}
	return strings.HasSuffix(capture.PublicRequest.URL, "/responses") && capture.ErrorClass == "upstream_response_read_error" && capture.StreamOutcome == "semantic_complete_downstream_cancelled" && capture.StreamReadErrorKind == "context_canceled" && capture.DownstreamContextState == "context_canceled" && capture.OutboundContextState == "context_canceled" && capture.StreamContextAtTerminal == "active" && capture.StreamTerminalComplete && capture.StreamTerminalForwarded && capture.StreamTerminalBytes > 0 && capture.StreamForwardedBytes >= capture.StreamTerminalBytes && capture.StreamFlushedBytes >= capture.StreamTerminalBytes
}

func liveCompleteMessagesTerminal(body []byte, expectedModel string) bool {
	var message map[string]any
	if json.Unmarshal(bytes.TrimSpace(body), &message) == nil {
		usage, _ := message["usage"].(map[string]any)
		return message["type"] == "message" && message["role"] == "assistant" && liveMatrixResponseModelMatches(expectedModel, attachmentString(message, "model")) && message["error"] == nil && liveMessagesStopReason(message["stop_reason"]) && attachmentTokenCount(usage["input_tokens"], false) && attachmentTokenCount(usage["output_tokens"], true) && liveMessagesContentMeaningful(message["content"])
	}
	normalized := bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
	if !bytes.HasSuffix(normalized, []byte("\n\n")) {
		return false
	}
	type blockState struct {
		kind         string
		id           string
		name         string
		text         bool
		initialInput map[string]any
		inputDelta   strings.Builder
		closed       bool
	}
	blocks := make(map[int]*blockState)
	started, delta, stopped, meaningful, done := false, false, false, false, false
	for _, frame := range bytes.Split(normalized, []byte("\n\n")) {
		if len(bytes.TrimSpace(frame)) == 0 {
			continue
		}
		if done {
			return false
		}
		var eventName, data string
		for _, line := range bytes.Split(frame, []byte("\n")) {
			if bytes.HasPrefix(line, []byte("event:")) {
				eventName = strings.TrimSpace(string(bytes.TrimPrefix(line, []byte("event:"))))
			} else if bytes.HasPrefix(line, []byte("data:")) {
				if data != "" {
					return false
				}
				data = strings.TrimSpace(string(bytes.TrimPrefix(line, []byte("data:"))))
			}
		}
		if data == "[DONE]" && eventName == "" && stopped {
			done = true
			continue
		}
		if stopped {
			return false
		}
		var event map[string]any
		if eventName == "" || data == "" || json.Unmarshal([]byte(data), &event) != nil || event["type"] != eventName || event["error"] != nil {
			return false
		}
		switch eventName {
		case "message_start":
			if started || delta || len(blocks) != 0 {
				return false
			}
			msg, _ := event["message"].(map[string]any)
			usage, _ := msg["usage"].(map[string]any)
			started = msg["type"] == "message" && msg["role"] == "assistant" && liveMatrixResponseModelMatches(expectedModel, attachmentString(msg, "model")) && msg["error"] == nil && attachmentTokenCount(usage["input_tokens"], false)
			if !started {
				return false
			}
		case "content_block_start":
			index, ok := liveMessagesBlockIndex(event)
			if !started || delta || !ok || blocks[index] != nil {
				return false
			}
			block, _ := event["content_block"].(map[string]any)
			state := &blockState{kind: attachmentString(block, "type"), text: strings.TrimSpace(attachmentString(block, "text")) != "", id: attachmentString(block, "id"), name: attachmentString(block, "name")}
			if state.kind != "text" && state.kind != "tool_use" {
				return false
			}
			if state.kind == "tool_use" {
				state.initialInput, ok = block["input"].(map[string]any)
				if !ok || state.id == "" || state.name == "" {
					return false
				}
			}
			blocks[index] = state
		case "content_block_delta":
			index, ok := liveMessagesBlockIndex(event)
			if !started || delta || !ok || blocks[index] == nil || blocks[index].closed {
				return false
			}
			detail, _ := event["delta"].(map[string]any)
			state := blocks[index]
			if state.kind == "text" && detail["type"] == "text_delta" {
				if strings.TrimSpace(attachmentString(detail, "text")) != "" {
					state.text = true
				}
			} else if state.kind == "tool_use" && detail["type"] == "input_json_delta" {
				partial, ok := detail["partial_json"].(string)
				if !ok || state.inputDelta.Len()+len(partial) > 1<<20 {
					return false
				}
				state.inputDelta.WriteString(partial)
			} else {
				return false
			}
		case "content_block_stop":
			index, ok := liveMessagesBlockIndex(event)
			if !started || delta || !ok || blocks[index] == nil || blocks[index].closed {
				return false
			}
			state := blocks[index]
			if state.kind == "text" {
				if !state.text {
					return false
				}
			} else {
				input := state.initialInput
				if state.inputDelta.Len() > 0 {
					if json.Unmarshal([]byte(state.inputDelta.String()), &input) != nil {
						return false
					}
				}
				if len(input) == 0 {
					return false
				}
			}
			state.closed = true
			meaningful = true
		case "message_delta":
			if !started || delta || !meaningful {
				return false
			}
			for _, block := range blocks {
				if !block.closed {
					return false
				}
			}
			detail, _ := event["delta"].(map[string]any)
			usage, _ := event["usage"].(map[string]any)
			delta = liveMessagesStopReason(detail["stop_reason"]) && attachmentTokenCount(usage["output_tokens"], true)
			if !delta {
				return false
			}
		case "message_stop":
			if !started || !delta || !meaningful {
				return false
			}
			stopped = true
		case "ping":
		default:
			return false
		}
	}
	return started && delta && stopped && meaningful
}

func liveMessagesBlockIndex(event map[string]any) (int, bool) {
	value, ok := event["index"].(float64)
	if !ok || value < 0 || value > 1024 || math.Trunc(value) != value {
		return 0, false
	}
	return int(value), true
}

func liveMessagesStopReason(value any) bool {
	return value == "end_turn" || value == "stop_sequence" || value == "tool_use"
}

func liveMessagesContentMeaningful(value any) bool {
	blocks, _ := value.([]any)
	for _, raw := range blocks {
		block, _ := raw.(map[string]any)
		if block["type"] == "text" && strings.TrimSpace(attachmentString(block, "text")) != "" {
			return true
		}
		if block["type"] == "tool_use" && attachmentString(block, "id") != "" && attachmentString(block, "name") != "" {
			if input, ok := block["input"].(map[string]any); ok && len(input) > 0 {
				return true
			}
		}
	}
	return false
}

func inspectLiveAttachmentClient(candidate liveAttachmentCase, output []byte) liveAttachmentEvidence {
	evidence := liveAttachmentEvidence{}
	claudeCalls := make(map[string]bool)
	claudePDFPages := make(map[string]bool)
	claudeReadArgs := make(map[string]string)
	claudeResults := make(map[string]bool)
	codeExecution := false
	var answer string
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		var event map[string]any
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if candidate.client == "claude" {
			if event["type"] == "result" && event["is_error"] != true {
				answer, _ = event["result"].(string)
				evidence.ClientFinal = strings.TrimSpace(answer) != ""
			}
			message, _ := event["message"].(map[string]any)
			content, _ := message["content"].([]any)
			for _, raw := range content {
				block, _ := raw.(map[string]any)
				if block["type"] == "tool_use" && block["name"] == "Read" {
					id, _ := block["id"].(string)
					if id != "" && strings.Contains(fmt.Sprint(block["input"]), candidate.fixture) {
						claudeCalls[id] = true
						input, _ := block["input"].(map[string]any)
						claudePDFPages[id] = candidate.media == "pdf" && attachmentString(input, "file_path") == candidate.fixture && liveAttachmentNativePDFPageOne(attachmentString(input, "pages"))
						if candidate.media == "pdf" {
							claudeReadArgs[id] = liveAttachmentArgumentSHA256(input)
						}
					}
				}
				if block["type"] == "tool_result" && block["is_error"] != true {
					id, _ := block["tool_use_id"].(string)
					if id != "" && claudeCalls[id] {
						claudeResults[id] = true
						if candidate.media == "png" {
							contentJSON, _ := json.Marshal(block["content"])
							evidence.ClientMedia = len(contentJSON) > len("null")
						} else {
							walkAttachmentJSON(block["content"], func(item map[string]any) {
								media := "pdf"
								if claudePDFPages[id] {
									media = "jpeg"
								}
								if body, ok := liveAttachmentMediaBytes(item, media); ok {
									hash := sha256.Sum256(body)
									evidence.ClientMedia = true
									evidence.ClientMediaSHA256 = hex.EncodeToString(hash[:])
									evidence.ClientMediaCount++
								}
							})
						}
					}
				}
			}
		} else {
			if event["type"] == "turn.completed" {
				evidence.ClientFinal = true
			}
			item, _ := event["item"].(map[string]any)
			if event["type"] == "item.completed" && item["type"] == "agent_message" {
				answer, _ = item["text"].(string)
			}
			if event["type"] == "item.completed" && item["type"] == "command_execution" && item["status"] == "completed" && item["exit_code"] == float64(0) {
				command := attachmentString(item, "command")
				codeExecution = codeExecution || strings.Contains(command, "pdftoppm") && strings.Contains(command, "-png") && strings.Contains(command, candidate.fixture)
			}
		}
	}
	if candidate.client == "claude" {
		for id := range claudeCalls {
			evidence.ClientToolCall = true
			evidence.ClientToolResult = evidence.ClientToolResult || claudeResults[id]
			if candidate.media == "pdf" {
				evidence.ClientReadCount++
				if claudePDFPages[id] && claudeResults[id] {
					evidence.ClientReadArgsSHA256 = claudeReadArgs[id]
				}
			}
		}
	} else if candidate.media == "png" {
		evidence.ClientMedia = true
	} else {
		evidence.ClientToolCall = codeExecution
		evidence.ClientToolResult = codeExecution
		evidence.ClientMedia = codeExecution
	}
	answer = strings.ToLower(answer)
	if candidate.media == "png" {
		topRed := regexp.MustCompile(`\btop\b[^.!?]{0,80}\bred\b|\bred\b[^.!?]{0,80}\btop\b`).MatchString(answer)
		bottomBlue := regexp.MustCompile(`\bbottom\b[^.!?]{0,80}\bblue\b|\bblue\b[^.!?]{0,80}\bbottom\b`).MatchString(answer)
		evidence.ClientAnswer = topRed && bottomBlue
	} else {
		evidence.ClientAnswer = strings.Contains(answer, "q7b9") && regexp.MustCompile(`\bblue\b`).MatchString(answer)
	}
	return evidence
}

func attachmentString(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := object[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func liveAttachmentCommandOutputSuccess(item map[string]any) bool {
	if item["is_error"] == true || item["status"] == "failed" {
		return false
	}
	if code, ok := item["exit_code"].(float64); ok {
		return code == 0
	}
	if output, ok := item["output"].(map[string]any); ok {
		if code, ok := output["exit_code"].(float64); ok {
			return code == 0 && output["status"] != "failed"
		}
	}
	output := strings.TrimSpace(attachmentString(item, "output", "content"))
	return strings.HasPrefix(output, "Process exited with code 0\n") || output == "Process exited with code 0" || liveAttachmentPDFRenderOutput(output)
}

func liveAttachmentRendererPrefix(command, source, root string) (string, bool) {
	if command == "" || source == "" || root == "" {
		return "", false
	}
	parts := strings.Fields(command)
	if len(parts) != 4 && len(parts) != 7 && len(parts) != 8 || filepath.Base(parts[0]) != "pdftoppm" || parts[1] != "-png" || parts[2] != source {
		return "", false
	}
	if len(parts) == 7 && (parts[4] != "&&" || parts[5] != "ls" || parts[6] != "-la") || len(parts) == 8 && (parts[4] != "&&" || parts[5] != "ls" || parts[6] != "-la" || parts[7] != "page*") {
		return "", false
	}
	prefix := parts[3]
	if !filepath.IsAbs(prefix) {
		prefix = filepath.Join(root, prefix)
	}
	prefix = filepath.Clean(prefix)
	if !strings.HasPrefix(prefix, filepath.Clean(root)+string(os.PathSeparator)) || filepath.Base(prefix) == "." {
		return "", false
	}
	return prefix, true
}

func liveAttachmentRendererOutputMatchesPrefix(image string, prefixes map[string]bool) bool {
	for prefix := range prefixes {
		if image == prefix+".png" || image == prefix+"-1.png" {
			return true
		}
	}
	return false
}

func walkAttachmentJSON(value any, visit func(map[string]any)) {
	switch item := value.(type) {
	case map[string]any:
		visit(item)
		for _, child := range item {
			walkAttachmentJSON(child, visit)
		}
	case []any:
		for _, child := range item {
			walkAttachmentJSON(child, visit)
		}
	case string:
		if len(item) < 16<<20 && (strings.HasPrefix(item, "{") || strings.HasPrefix(item, "[")) {
			var decoded any
			if json.Unmarshal([]byte(item), &decoded) == nil {
				walkAttachmentJSON(decoded, visit)
			}
		}
	}
}

func liveAttachmentMediaHasBytes(item map[string]any, media string) bool {
	_, ok := liveAttachmentMediaBytes(item, media)
	return ok
}

func liveAttachmentMediaBytes(item map[string]any, media string) ([]byte, bool) {
	if media == "jpeg" {
		typeName := attachmentString(item, "type")
		if typeName != "image" && typeName != "input_image" {
			return nil, false
		}
		data := ""
		if source, ok := item["source"].(map[string]any); ok && attachmentString(source, "media_type") == "image/jpeg" {
			data = attachmentString(source, "data")
		}
		if imageURL := attachmentString(item, "image_url"); strings.HasPrefix(imageURL, "data:image/jpeg;base64,") {
			data = strings.TrimPrefix(imageURL, "data:image/jpeg;base64,")
		}
		decoded, err := base64.StdEncoding.DecodeString(data)
		return decoded, err == nil && len(decoded) > 4 && bytes.HasPrefix(decoded, []byte("\xff\xd8\xff")) && bytes.HasSuffix(decoded, []byte("\xff\xd9"))
	}
	if media == "png" {
		url := attachmentString(item, "image_url", "url")
		if imageURL, ok := item["image_url"].(map[string]any); ok {
			url = attachmentString(imageURL, "url")
		}
		if source, ok := item["source"].(map[string]any); ok {
			url = attachmentString(source, "data")
		}
		url = strings.TrimPrefix(url, "data:image/png;base64,")
		decoded, err := base64.StdEncoding.DecodeString(url)
		return decoded, err == nil && bytes.HasPrefix(decoded, []byte("\x89PNG\r\n\x1a\n"))
	}
	data := attachmentString(item, "file_data")
	if source, ok := item["source"].(map[string]any); ok {
		data = attachmentString(source, "data")
	}
	data = strings.TrimPrefix(data, "data:application/pdf;base64,")
	decoded, err := base64.StdEncoding.DecodeString(data)
	return decoded, err == nil && bytes.HasPrefix(decoded, []byte("%PDF-"))
}

func inspectLiveOriginalClientAttachment(candidate liveAttachmentCase, captures []liveAttachmentIngressCapture, evidence *liveAttachmentEvidence) {
	source, err := os.ReadFile(candidate.fixture)
	if err != nil {
		return
	}
	sourceHash := sha256.Sum256(source)
	evidence.SourceSHA256 = hex.EncodeToString(sourceHash[:])
	allIdentity := len(captures) > 0
	mediaHashes := make(map[string]bool)
	mediaDetails := make(map[string]string)
	mediaDetailConflict := false
	readCalls := make(map[string]bool)
	readPDFPageCalls := make(map[string]bool)
	readArguments := make(map[string]string)
	readResults := make(map[string]bool)
	jpegHashes := make(map[string]bool)
	initialPDFPath := false
	for _, capture := range captures {
		path := "/v1/messages"
		if candidate.client == "codex" {
			path = "/v1/responses"
		}
		requestURL, urlErr := url.Parse(capture.Request.URL)
		var request map[string]any
		parsed := json.Unmarshal([]byte(capture.Request.Body), &request) == nil
		confirmedFunctionCallCancellation := capture.StreamOutcome == "semantic_complete_function_call_downstream_cancelled" && liveCompleteResponseFunctionCallTerminal([]byte(capture.Response.Body), candidate.model) && liveAttachmentIngressSemanticCancellation(capture, context.Canceled, context.Canceled)
		confirmedTextTerminalCancellation := capture.StreamOutcome == "semantic_complete_text_terminal_downstream_cancelled" && liveCompleteResponseTextTerminal([]byte(capture.Response.Body), candidate.model) && liveAttachmentIngressTextTerminalCancellation(capture, context.Canceled, context.Canceled)
		confirmedCancellation := confirmedFunctionCallCancellation || confirmedTextTerminalCancellation
		allIdentity = allIdentity && capture.Dispatched && capture.BodyComplete && (capture.ErrorClass == "" || confirmedCancellation) && urlErr == nil && requestURL.Path == path && parsed && request["model"] == candidate.model
		if candidate.client == "claude" {
			config, _ := request["output_config"].(map[string]any)
			allIdentity = allIdentity && config["effort"] == "medium"
		} else {
			reasoning, _ := request["reasoning"].(map[string]any)
			allIdentity = allIdentity && reasoning["effort"] == "medium"
		}
		if candidate.client == "codex" && candidate.media == "pdf" && strings.Contains(capture.Request.Body, candidate.fixture) {
			initialPDFPath = true
		}
		walkAttachmentJSON(request, func(item map[string]any) {
			typeName := attachmentString(item, "type")
			if typeName == "tool_use" && attachmentString(item, "name") == "Read" {
				input, _ := item["input"].(map[string]any)
				if id := attachmentString(item, "id"); id != "" && attachmentString(input, "file_path") == candidate.fixture {
					readCalls[id] = true
					readPDFPageCalls[id] = candidate.media == "pdf" && liveAttachmentNativePDFPageOne(attachmentString(input, "pages"))
					if candidate.media == "pdf" {
						readArguments[id] = liveAttachmentArgumentSHA256(input)
					}
				}
			}
			if typeName == "tool_result" {
				if id := attachmentString(item, "tool_use_id"); id != "" {
					readResults[id] = true
				}
			}
			if typeName != "input_image" && typeName != "input_file" && typeName != "image" && typeName != "document" {
				return
			}
			media := candidate.media
			if candidate.client == "codex" && candidate.media == "pdf" {
				media = "png"
			}
			if candidate.client == "claude" && candidate.media == "pdf" {
				media = "jpeg"
			}
			if body, ok := liveAttachmentMediaBytes(item, media); ok {
				hash := sha256.Sum256(body)
				digest := hex.EncodeToString(hash[:])
				mediaHashes[digest] = true
				if candidate.client == "codex" && media == "png" {
					detail := liveAttachmentImageDetail(item)
					if previous, exists := mediaDetails[digest]; exists && previous != detail {
						mediaDetailConflict = true
					}
					mediaDetails[digest] = detail
				}
				if candidate.cell == "claude-gpt-pdf" {
					evidence.IngressMediaCount++
				}
				if media == "jpeg" {
					jpegHashes[digest] = true
				}
			} else if candidate.client == "claude" && candidate.media == "pdf" {
				if body, ok := liveAttachmentMediaBytes(item, "pdf"); ok {
					hash := sha256.Sum256(body)
					mediaHashes[hex.EncodeToString(hash[:])] = true
				}
			}
		})
	}
	if len(mediaHashes) == 1 {
		for hash := range mediaHashes {
			evidence.IngressMediaSHA256 = hash
			if !mediaDetailConflict {
				evidence.IngressMediaDetail = mediaDetails[hash]
			}
		}
	}
	evidence.SourcePreparedEqual = evidence.SourceSHA256 == evidence.IngressMediaSHA256
	if candidate.client == "claude" {
		paired := false
		pairedPage := false
		for id := range readCalls {
			paired = paired || readResults[id]
			pairedPage = pairedPage || readPDFPageCalls[id] && readResults[id]
			if candidate.media == "pdf" {
				evidence.IngressReadCount++
				if readPDFPageCalls[id] && readResults[id] {
					evidence.IngressReadArgsSHA256 = readArguments[id]
				}
			}
		}
		allIdentity = allIdentity && paired
		evidence.NativePDFPage = pairedPage && evidence.IngressMediaSHA256 != "" && jpegHashes[evidence.IngressMediaSHA256]
	}
	if candidate.client == "codex" && candidate.media == "pdf" {
		allIdentity = allIdentity && initialPDFPath
	}
	evidence.IngressPrepared = allIdentity && evidence.IngressMediaSHA256 != ""
}

func attachmentStreamObjects(body string) []map[string]any {
	objects := make([]map[string]any, 0)
	appendObject := func(line string) {
		var object map[string]any
		if json.Unmarshal([]byte(line), &object) == nil {
			objects = append(objects, object)
		}
	}
	if strings.HasPrefix(strings.TrimSpace(body), "{") {
		appendObject(body)
		return objects
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			appendObject(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	type partialTool struct {
		id    string
		name  string
		input string
	}
	partials := make(map[int]*partialTool)
	for _, event := range append([]map[string]any(nil), objects...) {
		index, ok := event["index"].(float64)
		if !ok {
			continue
		}
		blockIndex := int(index)
		switch event["type"] {
		case "content_block_start":
			block, _ := event["content_block"].(map[string]any)
			if block["type"] != "tool_use" {
				continue
			}
			initial := ""
			if input, ok := block["input"].(map[string]any); ok && len(input) > 0 {
				body, _ := json.Marshal(input)
				initial = string(body)
			}
			partials[blockIndex] = &partialTool{id: attachmentString(block, "id"), name: attachmentString(block, "name"), input: initial}
		case "content_block_delta":
			tool := partials[blockIndex]
			delta, _ := event["delta"].(map[string]any)
			if tool != nil && delta["type"] == "input_json_delta" {
				tool.input += attachmentString(delta, "partial_json")
			}
		case "content_block_stop":
			tool := partials[blockIndex]
			if tool == nil || tool.id == "" || tool.name == "" {
				continue
			}
			var input map[string]any
			if json.Unmarshal([]byte(tool.input), &input) == nil {
				objects = append(objects, map[string]any{"type": "assembled_tool_use", "item": map[string]any{"type": "tool_use", "id": tool.id, "name": tool.name, "input": input}})
			}
			delete(partials, blockIndex)
		}
	}
	return objects
}

func attachmentTokenCount(value any, positive bool) bool {
	number, ok := value.(float64)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number || number < 0 {
		return false
	}
	return !positive || number > 0
}

func attachmentResponseCompleted(body string, candidate liveAttachmentCase) bool {
	if candidate.modelPath == "/chat/completions" {
		stream, valid := liveChatCompletionStream([]byte(body), candidate.model)
		return valid && stream.FinishReason == "stop" && strings.TrimSpace(stream.Text) != ""
	}
	events := attachmentStreamObjects(body)
	if candidate.client == "claude" {
		if !liveCompleteResponseTerminal([]byte(body), candidate.model) || len(events) == 0 {
			return false
		}
		for _, event := range events {
			if event["type"] == "response.failed" || event["type"] == "response.incomplete" || event["type"] == "error" {
				return false
			}
		}
		response, _ := events[len(events)-1]["response"].(map[string]any)
		usage, _ := response["usage"].(map[string]any)
		if response["object"] != "response" || response["status"] != "completed" || response["model"] != candidate.model || response["error"] != nil || response["incomplete_details"] != nil || !attachmentTokenCount(usage["input_tokens"], false) || !attachmentTokenCount(usage["output_tokens"], true) {
			return false
		}
		foundText := false
		walkAttachmentJSON(response["output"], func(item map[string]any) {
			if item["type"] == "output_text" && strings.TrimSpace(attachmentString(item, "text")) != "" {
				foundText = true
			}
		})
		if foundText {
			return true
		}

		return false
	}
	if !liveCompleteMessagesTerminal([]byte(body), candidate.model) {
		return false
	}
	if len(events) == 1 && events[0]["type"] == "message" {
		message := events[0]
		usage, _ := message["usage"].(map[string]any)
		model, _ := message["model"].(string)
		if message["role"] != "assistant" || !liveMatrixResponseModelMatches(candidate.model, model) || message["stop_reason"] != "end_turn" && message["stop_reason"] != "stop_sequence" || !attachmentTokenCount(usage["input_tokens"], false) || !attachmentTokenCount(usage["output_tokens"], true) {
			return false
		}
		foundText := false
		walkAttachmentJSON(message["content"], func(item map[string]any) {
			if item["type"] == "text" && strings.TrimSpace(attachmentString(item, "text")) != "" {
				foundText = true
			}
		})
		return foundText
	}
	started, stopped, finalReason, textContent := false, false, false, false
	inputUsed, outputUsed := false, false
	for _, event := range events {
		switch event["type"] {
		case "message_start":
			message, _ := event["message"].(map[string]any)
			usage, _ := message["usage"].(map[string]any)
			model, _ := message["model"].(string)
			started = message["type"] == "message" && message["role"] == "assistant" && liveMatrixResponseModelMatches(candidate.model, model)
			inputUsed = attachmentTokenCount(usage["input_tokens"], false)
		case "content_block_start":
			block, _ := event["content_block"].(map[string]any)
			textContent = textContent || block["type"] == "text" && strings.TrimSpace(attachmentString(block, "text")) != ""
		case "content_block_delta":
			delta, _ := event["delta"].(map[string]any)
			textContent = textContent || delta["type"] == "text_delta" && strings.TrimSpace(attachmentString(delta, "text")) != ""
		case "message_delta":
			delta, _ := event["delta"].(map[string]any)
			usage, _ := event["usage"].(map[string]any)
			finalReason = delta["stop_reason"] == "end_turn" || delta["stop_reason"] == "stop_sequence"
			outputUsed = attachmentTokenCount(usage["output_tokens"], true)
		case "message_stop":
			stopped = true
		}
	}
	return started && stopped && finalReason && textContent && inputUsed && outputUsed
}

func TestAttachmentPacketOriginalClientCommands(t *testing.T) {
	root := t.TempDir()
	for _, candidate := range liveAttachmentCases() {
		args, err := liveAttachmentArgs(candidate, root)
		if err != nil || len(args) == 0 {
			t.Fatalf("case %s did not prepare original client arguments", candidate.cell)
		}
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--model "+candidate.model) || !strings.Contains(joined, "medium") || strings.Contains(joined, "Q7B9") {
			t.Fatalf("case %s lost exact model or effort, or leaked expected answer into prompt", candidate.cell)
		}
		if candidate.client == "claude" {
			if !strings.Contains(joined, "--tools Read --allowedTools Read") || !strings.Contains(joined, candidate.fixture) || strings.Contains(joined, "--image") {
				t.Fatalf("case %s replaced the built-in Read path", candidate.cell)
			}
		} else if candidate.media == "png" {
			if !strings.Contains(joined, "--image "+candidate.fixture) || !strings.Contains(joined, "--sandbox read-only") {
				t.Fatal("Codex PNG did not use the original image flag")
			}
		} else if !strings.Contains(joined, "pdftoppm") || !strings.Contains(joined, "view_image") || !strings.Contains(joined, "--sandbox workspace-write") || strings.Contains(joined, "--image "+candidate.fixture) {
			t.Fatal("Codex PDF did not use its original renderer and image tool route")
		}
		if candidate.client == "codex" {
			foundEffort := false
			for index, arg := range args {
				foundEffort = foundEffort || arg == "--config" && index+1 < len(args) && args[index+1] == "model_reasoning_effort=\"medium\""
			}
			if !foundEffort {
				t.Fatal("Codex medium effort argv is not exact valid TOML")
			}
		}
	}
	configuration := liveAttachmentCodexConfiguration("http://127.0.0.1:1000")
	if strings.Contains(configuration, "gpt-6-luna") || strings.Contains(configuration, "model_catalog_json") || !strings.Contains(configuration, `model = "claude-haiku-5.5"`) || !strings.Contains(configuration, "request_max_retries = 0") {
		t.Fatal("Codex cross-model profile reused GPT metadata or enabled transport retries")
	}
}

func TestAttachmentPacketCodexUnknownSlugReachesLocalProvider(t *testing.T) {
	if os.Getenv("CPA_OFFLINE_CLIENT_PROBE") != "1" {
		t.Skip("set CPA_OFFLINE_CLIENT_PROBE=1 to launch isolated Codex against an owned loopback fixture")
	}
	t.Setenv("CPA_LIVE_CLIENT_ATTACHMENTS", "1")
	var calls atomic.Int32
	var valid atomic.Bool
	fixture := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost && request.URL.Path == "/chat/completions" {
			calls.Add(1)
			var body map[string]any
			if json.NewDecoder(request.Body).Decode(&body) == nil {
				valid.Store(body["model"] == "claude-haiku-5.5" && body["reasoning_effort"] == "medium" && body["stream"] == true)
			}
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":{"type":"invalid_request_error","message":"offline fixture refusal"}}`))
	}))
	defer fixture.Close()
	candidate := liveAttachmentCases()[2]
	result := liveAttachmentRunCLI(t, candidate, fixture.URL, t.TempDir())
	if calls.Load() != 1 || !valid.Load() || result.exitCode == 0 {
		words := regexp.MustCompile(`[A-Za-z]+`).FindAllString(string(result.stderr), 12)
		t.Fatalf("isolated Codex did not prepare one exact-model medium-effort Chat request: calls=%d valid=%t exit=%d stderr_bytes=%d stdout_bytes=%d stderr_words=%q", calls.Load(), valid.Load(), result.exitCode, len(result.stderr), len(result.stdout), words)
	}
}

func TestAttachmentPacketChangedCaseSelectionAndRendererPreflight(t *testing.T) {
	selected, err := liveAttachmentSelectedCases("claude-gpt-pdf,codex-claude-png,codex-claude-pdf")
	if err != nil || len(selected) != 3 {
		t.Fatal("changed-case selection was not exact")
	}
	for _, candidate := range selected {
		cell, err := liveAttachmentOperationCell(candidate, "fix-client-preparation")
		if err != nil || !liveAttachmentGateCell(cell) || cell == "X/"+candidate.cell {
			t.Fatal("changed-case identity could overwrite a frozen historical cell")
		}
	}
	for _, selection := range []string{"claude-gpt-png,claude-gpt-png", "unknown", ","} {
		if _, err := liveAttachmentSelectedCases(selection); err == nil {
			t.Fatal("invalid changed-case selection was allowed")
		}
	}
	if _, err := liveAttachmentOperationCell(selected[0], "../../escape"); err == nil {
		t.Fatal("unsafe attempt identity was allowed")
	}
	t.Setenv("CPA_LIVE_CLIENT_ATTACHMENTS", "1")
	gate := newLiveServerToolGate(t, t.TempDir())
	gate.publicAPI, _ = url.Parse("https://api.githubcopilot.com")
	gate.freezeCell("X/claude-gpt-pdf")
	changedCell, _ := liveAttachmentOperationCell(selected[0], "fix-client-preparation")
	phase, name, _ := strings.Cut(changedCell, "/")
	if err := gate.setPhaseCell(phase, name); err != nil {
		t.Fatal(err)
	}
	gate.setFreshClaim("inference", changedCell)
	if _, cell, _, allowed := gate.reserve("/responses"); !allowed || cell != changedCell {
		t.Fatal("frozen historical failure blocked a separately claimed changed case")
	}
	if os.Getenv("CPA_OFFLINE_CLIENT_PROBE") != "1" {
		t.Skip("set CPA_OFFLINE_CLIENT_PROBE=1 for installed PDF renderer child-path preflight")
	}
	for _, candidate := range selected {
		if candidate.media != "pdf" {
			continue
		}
		if liveAttachmentPopplerPreflight(t, candidate, t.TempDir()) == "" {
			t.Fatal("installed PDF renderer identity was missing")
		}
	}
}

func TestAttachmentPacketWrongModelIsDeniedBeforePublicDispatch(t *testing.T) {
	t.Setenv("CPA_LIVE_CLIENT_ATTACHMENTS", "1")
	gate := newLiveServerToolGate(t, t.TempDir())
	gate.publicAPI, _ = url.Parse("https://api.githubcopilot.com")
	if err := gate.setPhaseCell("X-revalidation", "claude-gpt-pdf"); err != nil {
		t.Fatal(err)
	}
	gate.setFreshClaim("inference", "X-revalidation/claude-gpt-pdf")
	for _, body := range []string{
		`{"model":"other","reasoning":{"effort":"medium"}}`,
		`{"model":"gpt-6-luna","reasoning":{"effort":"high"}}`,
	} {
		request, err := http.NewRequest(http.MethodPost, gate.server.URL+"/responses", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatal("unprepared model or effort was not denied locally")
		}
	}
	count, total, _, _, captureErr := gate.countsFor("X-revalidation/claude-gpt-pdf")
	if count != 0 || total != 0 || captureErr == nil {
		t.Fatal("unprepared request consumed a physical dispatch or failed to stop later egress")
	}
}

func TestAttachmentPacketHistoricalPNGBodyReplay(t *testing.T) {
	gateDirectory := os.Getenv("CPA_ATTACHMENT_REPLAY_GATE_DIR")
	caseDirectory := os.Getenv("CPA_ATTACHMENT_REPLAY_CASE_DIR")
	if gateDirectory == "" || caseDirectory == "" {
		t.Skip("set both private body-replay directories to inspect the retained historical PNG attempt")
	}
	candidate := liveAttachmentCases()[0]
	output, err := os.ReadFile(filepath.Join(caseDirectory, "client-stdout.jsonl"))
	if err != nil {
		t.Fatal("retained historical client event stream is unavailable")
	}
	gate := &liveServerToolGate{directory: gateDirectory}
	evidence := inspectLiveAttachmentEvidence(t, gate, candidate, "X/"+candidate.cell, liveAttachmentCLI{stdout: output})
	ingress := &liveAttachmentIngress{directory: filepath.Join(caseDirectory, "original-client-hop")}
	captures, err := ingress.captures()
	if err != nil {
		t.Fatal("retained historical original-client captures are unavailable")
	}
	inspectLiveOriginalClientAttachment(candidate, captures, &evidence)
	if !evidence.PublicToolCall || !evidence.PublicToolResult || !evidence.PublicTerminal || !evidence.IngressPrepared || !evidence.SourcePreparedEqual || evidence.PublicStreamProven || evidence.ClientAnswer {
		t.Fatal("historical PNG observation or its retained failure was misclassified")
	}
	t.Log("Historical PNG body replay observed a paired public Read call/result and later terminal; the unclassified stream read error and wrong visual answer remain failures.")
}

func TestAttachmentPacketGateClaimsWithoutNumericalCeilings(t *testing.T) {
	t.Setenv("CPA_LIVE_CLIENT_ATTACHMENTS", "1")
	directory := t.TempDir()
	gate := newLiveServerToolGateWithLedger(t, directory, directory)
	gate.publicAPI, _ = url.Parse("https://api.githubcopilot.com")
	if err := gate.setPhaseCell("X", "codex-claude-png"); err != nil {
		t.Fatal(err)
	}
	if err := gate.setPhaseCell("X", "unknown-model"); err == nil {
		t.Fatal("unknown attachment cell was allowed")
	}
	gate.setFreshClaim("inference", "X/codex-claude-png")
	for dispatch := 1; dispatch <= 3; dispatch++ {
		category, cell, origin, allowed := gate.reserve("/v1/messages")
		if !allowed || category != "inference" || cell != "X/codex-claude-png" || origin != "https://api.githubcopilot.com" {
			t.Fatal("prepared attachment cell did not account for a physical dispatch")
		}
	}
	gate.clearFreshInferenceClaim("X/codex-claude-png")
	_, _, _, allowed := gate.reserve("/v1/messages")
	if allowed {
		t.Fatal("unclaimed attachment inference was dispatched")
	}
	count, total, _, _, err := gate.countsFor("X/codex-claude-png")
	if err != nil || count != 3 || total != 3 {
		t.Fatal("durable physical attachment accounting was lost")
	}
}

func TestAttachmentPacketClientResultRequiresActualToolPair(t *testing.T) {
	candidate := liveAttachmentCases()[1]
	final := `{"type":"result","is_error":false,"result":"The code is Q7B9 and the element is blue."}` + "\n"
	for name, output := range map[string]string{
		"final-only":  final,
		"wrong-id":    `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"call-a","name":"Read","input":{"file_path":"` + attachmentPDF + `"}}]}}` + "\n" + `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"call-b","content":"Q7B9"}]}}` + "\n" + final,
		"failed-tool": `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"call-a","name":"Read","input":{"file_path":"` + attachmentPDF + `"}}]}}` + "\n" + `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"call-a","is_error":true,"content":"Q7B9"}]}}` + "\n" + final,
	} {
		evidence := inspectLiveAttachmentClient(candidate, []byte(output))
		if evidence.ClientToolCall && evidence.ClientToolResult && evidence.ClientMedia {
			t.Fatalf("%s falsely proved completed native Read", name)
		}
	}
	pdfBytes, err := os.ReadFile(attachmentPDF)
	if err != nil {
		t.Fatal(err)
	}
	encodedPDF := base64.StdEncoding.EncodeToString(pdfBytes)
	paired := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"call-a","name":"Read","input":{"file_path":"` + attachmentPDF + `"}}]}}` + "\n" + `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"call-a","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"` + encodedPDF + `"}}]}]}}` + "\n" + final
	evidence := inspectLiveAttachmentClient(candidate, []byte(paired))
	if !evidence.ClientFinal || !evidence.ClientToolCall || !evidence.ClientToolResult || !evidence.ClientMedia || !evidence.ClientAnswer {
		t.Fatal("genuine ID-paired native Read and completed answer were not recognized")
	}
}

func TestAttachmentPacketCodexPDFNeedsCompletedRenderer(t *testing.T) {
	candidate := liveAttachmentCases()[3]
	final := `{"type":"item.completed","item":{"type":"agent_message","text":"The code is Q7B9 and its element is blue."}}` + "\n" + `{"type":"turn.completed"}` + "\n"
	if evidence := inspectLiveAttachmentClient(candidate, []byte(final)); evidence.ClientToolCall || evidence.ClientToolResult {
		t.Fatal("Codex PDF answer without native renderer passed")
	}
	withRenderer := `{"type":"item.completed","item":{"type":"command_execution","status":"completed","command":"pdftoppm -png ` + attachmentPDF + ` page","exit_code":0}}` + "\n" + final
	evidence := inspectLiveAttachmentClient(candidate, []byte(withRenderer))
	if !evidence.ClientFinal || !evidence.ClientToolCall || !evidence.ClientToolResult || !evidence.ClientAnswer {
		t.Fatal("completed original Codex renderer was missed")
	}
	failedRenderer := strings.Replace(withRenderer, `"status":"completed"`, `"status":"failed"`, 1)
	if evidence := inspectLiveAttachmentClient(candidate, []byte(failedRenderer)); evidence.ClientToolCall || evidence.ClientToolResult {
		t.Fatal("failed Codex renderer passed")
	}
	nonzeroRenderer := strings.Replace(withRenderer, `"exit_code":0`, `"exit_code":1`, 1)
	if evidence := inspectLiveAttachmentClient(candidate, []byte(nonzeroRenderer)); evidence.ClientToolCall || evidence.ClientToolResult || evidence.ClientMedia {
		t.Fatal("completed Codex renderer with nonzero exit passed")
	}
}

func TestAttachmentPacketStrictPublicTerminalAndRendererProvenance(t *testing.T) {
	response := "event: response.completed\ndata: " + `{"type":"response.completed","response":{"object":"response","status":"completed","model":"gpt-6-luna","usage":{"input_tokens":2,"output_tokens":3},"output":[{"type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Q7B9 blue"}]}]}}` + "\n\n"
	good := liveServerToolCapture{PublicRequest: liveHTTPLog{URL: "https://api.githubcopilot.com/responses"}, Response: liveHTTPLog{Status: 200, Body: response}}
	if !liveCaptureStreamProven(good, "gpt-6-luna") {
		t.Fatal("complete authentic Responses terminal was rejected")
	}
	for _, changed := range []string{
		response + "event: response.failed\ndata: {\"type\":\"response.failed\"}\n\n",
		response + "event: response.incomplete\ndata: {\"type\":\"response.incomplete\"}\n\n",
		strings.Replace(response, `"object":"response"`, `"object":"other"`, 1),
		strings.Replace(response, `"status":"completed"`, `"status":"incomplete"`, 1),
		strings.Replace(response, `"role":"assistant"`, `"role":"user"`, 1),
		strings.Replace(response, `"usage":`, `"error":{"message":"refused"},"usage":`, 1),
		strings.TrimSuffix(response, "\n"),
	} {
		bad := good
		bad.Response.Body = changed
		if liveCaptureStreamProven(bad, "gpt-6-luna") {
			t.Fatal("invalid or trailing Responses terminal passed")
		}
	}
	message := "event: message_start\ndata: " + `{"type":"message_start","message":{"type":"message","role":"assistant","model":"claude-haiku-5-5","usage":{"input_tokens":2}}}` + "\n\n" +
		"event: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Q7B9 blue"}}` + "\n\n" +
		"event: content_block_stop\ndata: " + `{"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}` + "\n\n" +
		"event: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n"
	good.PublicRequest.URL = "https://api.githubcopilot.com/v1/messages"
	good.Response.Body = message
	if !liveCaptureStreamProven(good, "claude-haiku-5.5") {
		t.Fatal("complete native Messages terminal was rejected")
	}
	for _, changed := range []string{
		message + "event: error\ndata: {\"type\":\"error\"}\n\n",
		strings.Replace(message, `"role":"assistant"`, `"role":"user"`, 1),
		strings.Replace(message, `"stop_reason":"end_turn"`, `"stop_reason":"max_tokens"`, 1),
		strings.TrimSuffix(message, "\n"),
	} {
		bad := good
		bad.Response.Body = changed
		if liveCaptureStreamProven(bad, "claude-haiku-5.5") {
			t.Fatal("invalid or trailing Messages terminal passed")
		}
	}
	root := t.TempDir()
	prefix, ok := liveAttachmentRendererPrefix("pdftoppm -png "+attachmentPDF+" page", attachmentPDF, root)
	if !ok || !liveAttachmentRendererOutputMatchesPrefix(prefix+"-1.png", map[string]bool{prefix: true}) || liveAttachmentRendererOutputMatchesPrefix(filepath.Join(root, "unrelated.png"), map[string]bool{prefix: true}) {
		t.Fatal("renderer output was not bound to the owned command prefix")
	}
	for _, command := range []string{"pdftoppm -png " + attachmentPDF + " page && ls -la", "pdftoppm -png " + attachmentPDF + " page && ls -la page*"} {
		if _, ok := liveAttachmentRendererPrefix(command, attachmentPDF, root); !ok {
			t.Fatal("captured PDF renderer command with its bounded listing suffix was rejected")
		}
	}
	if _, ok := liveAttachmentRendererPrefix("pdftoppm -png other.pdf page", attachmentPDF, root); ok {
		t.Fatal("renderer for a different PDF passed")
	}
	for _, command := range []string{"pdftoppm -png " + attachmentPDF + " page && cat page-1.png", "pdftoppm -png " + attachmentPDF + " page && ls -la unrelated", "pdftoppm -png " + attachmentPDF + " page && ls -la page* extra"} {
		if _, ok := liveAttachmentRendererPrefix(command, attachmentPDF, root); ok {
			t.Fatal("unobserved or extended renderer command passed")
		}
	}
	if liveAttachmentCommandOutputSuccess(map[string]any{"type": "function_call_output", "output": "Process exited with code 1\n"}) || liveAttachmentCommandOutputSuccess(map[string]any{"type": "tool_result", "is_error": true, "exit_code": float64(0)}) || !liveAttachmentCommandOutputSuccess(map[string]any{"type": "function_call_output", "output": "Process exited with code 0\nFinal output:\n"}) {
		t.Fatal("public renderer success was not tied to a genuine zero exit")
	}
}

func TestAttachmentPacketMessagesBlockLifecycle(t *testing.T) {
	start := "event: message_start\ndata: " + `{"type":"message_start","message":{"type":"message","role":"assistant","model":"claude-haiku-5-5","usage":{"input_tokens":2}}}` + "\n\n"
	blockStart := "event: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n"
	textDelta := "event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Q7B9 blue"}}` + "\n\n"
	blockStop := "event: content_block_stop\ndata: " + `{"type":"content_block_stop","index":0}` + "\n\n"
	messageDelta := "event: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}` + "\n\n"
	messageStop := "event: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n"
	valid := start + blockStart + textDelta + blockStop + messageDelta + messageStop
	if !liveCompleteMessagesTerminal([]byte(valid), "claude-haiku-5.5") {
		t.Fatal("complete matched Messages content block was rejected")
	}
	for _, invalid := range []string{
		start + textDelta + blockStop + messageDelta + messageStop,
		start + blockStart + strings.Replace(textDelta, `"index":0`, `"index":1`, 1) + blockStop + messageDelta + messageStop,
		start + blockStart + textDelta + messageDelta + messageStop,
		textDelta + start + blockStart + blockStop + messageDelta + messageStop,
		start + blockStart + blockStop + messageDelta + textDelta + messageStop,
		start + blockStart + textDelta + blockStop + messageStop + messageDelta,
	} {
		if liveCompleteMessagesTerminal([]byte(invalid), "claude-haiku-5.5") {
			t.Fatal("out-of-order or unmatched Messages content block passed")
		}
	}
	toolStart := "event: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool-a","name":"exec_command","input":{}}}` + "\n\n"
	inputDelta := "event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"pdftoppm -png input.pdf page\"}"}}` + "\n\n"
	toolMessageDelta := strings.Replace(messageDelta, `"end_turn"`, `"tool_use"`, 1)
	tool := start + toolStart + inputDelta + blockStop + toolMessageDelta + messageStop
	if !liveCompleteMessagesTerminal([]byte(tool), "claude-haiku-5.5") {
		t.Fatal("complete native tool input JSON was rejected")
	}
	for _, invalid := range []string{
		start + toolStart + blockStop + toolMessageDelta + messageStop,
		strings.Replace(tool, `"partial_json":"{\"cmd\":`, `"partial_json":"broken{\"cmd\":`, 1),
		strings.Replace(tool, `"index":0,"delta":{"type":"input_json_delta"`, `"index":1,"delta":{"type":"input_json_delta"`, 1),
	} {
		if liveCompleteMessagesTerminal([]byte(invalid), "claude-haiku-5.5") {
			t.Fatal("incomplete or mismatched native tool input passed")
		}
	}
}

func TestAttachmentPacketPublicMediaAndTerminalRejectEmptyEvidence(t *testing.T) {
	if liveAttachmentMediaHasBytes(map[string]any{"type": "input_image", "image_url": "data:image/png;base64,"}, "png") {
		t.Fatal("empty image was accepted")
	}
	if liveAttachmentMediaHasBytes(map[string]any{"type": "input_file", "file_data": "data:application/pdf;base64,"}, "pdf") {
		t.Fatal("empty PDF was accepted")
	}
	fixture, err := os.ReadFile(attachmentPNG)
	if err != nil {
		t.Fatal("task-owned PNG fixture unavailable for offline regression")
	}
	encoded := base64.StdEncoding.EncodeToString(fixture)
	if !liveAttachmentMediaHasBytes(map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + encoded}, "png") {
		t.Fatal("valid native image bytes were rejected")
	}
	if len(attachmentStreamObjects("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")) != 1 || len(attachmentStreamObjects("event: response.completed\ndata: [DONE]\n\n")) != 0 {
		t.Fatal("stream parser accepted a non-JSON terminal or lost a real one")
	}
}

func TestAttachmentPacketPublicFollowupRequiresMatchedNativeToolResult(t *testing.T) {
	candidate := liveAttachmentCases()[1]
	fixture, err := os.ReadFile(candidate.fixture)
	if err != nil {
		t.Fatal("task-owned PDF fixture unavailable for offline regression")
	}
	firstRequest := `{"model":"gpt-6-luna","reasoning":{"effort":"medium"},"input":[{"role":"user","content":[{"type":"input_text","text":"Read the PDF"}]}]}`
	firstResponse := "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"call_id\":\"call-a\",\"name\":\"Read\"}}\n\n" + "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-6-luna\",\"usage\":{\"input_tokens\":2,\"output_tokens\":3},\"output\":[{\"type\":\"function_call\",\"status\":\"completed\",\"call_id\":\"call-a\",\"name\":\"Read\",\"arguments\":\"{\\\"file_path\\\":\\\"fixture.pdf\\\"}\"}]}}\n\n"
	secondRequest := fmt.Sprintf(`{"model":"gpt-6-luna","reasoning":{"effort":"medium"},"input":[{"type":"function_call_output","call_id":"call-a","output":[{"type":"input_file","file_data":"data:application/pdf;base64,%s"}]}]}`, base64.StdEncoding.EncodeToString(fixture))
	finalResponse := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-6-luna\",\"usage\":{\"input_tokens\":2,\"output_tokens\":3},\"output\":[{\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Q7B9 blue\"}]}]}}\n\n"
	clientOutput := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"call-a","name":"Read","input":{"file_path":"` + attachmentPDF + `"}}]}}` + "\n" + `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"call-a","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"` + base64.StdEncoding.EncodeToString(fixture) + `"}}]}]}}` + "\n" + `{"type":"result","is_error":false,"result":"The code is Q7B9 beside a blue element."}` + "\n"
	for _, test := range []struct {
		name          string
		callID        string
		lastResponse  string
		firstError    string
		wantCompleted bool
	}{
		{name: "paired", callID: "call-a", lastResponse: finalResponse, wantCompleted: true},
		{name: "observed-call-with-read-error", callID: "call-a", lastResponse: finalResponse, firstError: "upstream_response_read_error"},
		{name: "wrong-call-id", callID: "call-b", lastResponse: finalResponse},
		{name: "no-terminal", callID: "call-a", lastResponse: "event: response.incomplete\ndata: {\"type\":\"response.incomplete\"}\n\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			second := strings.Replace(secondRequest, `"call_id":"call-a"`, `"call_id":"`+test.callID+`"`, 1)
			captures := []liveServerToolCapture{
				{Category: "inference", PhaseCell: "X/" + candidate.cell, Dispatched: true, PublicRequest: liveHTTPLog{URL: "https://api.githubcopilot.com/responses", Body: firstRequest}, Response: liveHTTPLog{Status: 200, Body: firstResponse}, ErrorClass: test.firstError},
				{Category: "inference", PhaseCell: "X/" + candidate.cell, Dispatched: true, PublicRequest: liveHTTPLog{URL: "https://api.githubcopilot.com/responses", Body: second}, Response: liveHTTPLog{Status: 200, Body: test.lastResponse}},
			}
			for index, capture := range captures {
				encoded, err := json.Marshal(capture)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf("gate-%03d.json", index+1)), encoded, 0600); err != nil {
					t.Fatal(err)
				}
			}
			gate := &liveServerToolGate{directory: directory}
			evidence := inspectLiveAttachmentEvidence(t, gate, candidate, "X/"+candidate.cell, liveAttachmentCLI{stdout: []byte(clientOutput)})
			if test.firstError != "" && (!evidence.PublicToolCall || !evidence.PublicToolResult || !evidence.PublicTerminal || evidence.PublicStreamClean || evidence.PublicStreamProven) {
				t.Fatal("observed native tool and final terminal were erased or unclean stream was accepted")
			}
			completed := evidence.ClientFinal && evidence.ClientToolCall && evidence.ClientToolResult && evidence.ClientMedia && evidence.ClientAnswer && evidence.PublicModel && evidence.PublicEffort && evidence.PublicEndpoint && evidence.PublicMedia && evidence.PublicToolCall && evidence.PublicToolResult && evidence.PublicTerminal && evidence.PublicStreamProven
			if completed != test.wantCompleted {
				t.Fatalf("public follow-up completion = %t, want %t: %+v", completed, test.wantCompleted, evidence)
			}
		})
	}
}

func TestAttachmentPacketReconstructsStreamedNativeToolArguments(t *testing.T) {
	stream := "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tool-a\",\"name\":\"exec_command\",\"input\":{}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"cmd\\\":\\\"pdftoppm -png " + attachmentPDF + " page\\\"}\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n"
	assembled := false
	for _, event := range attachmentStreamObjects(stream) {
		if event["type"] != "assembled_tool_use" {
			continue
		}
		item, _ := event["item"].(map[string]any)
		input, _ := item["input"].(map[string]any)
		assembled = item["id"] == "tool-a" && item["name"] == "exec_command" && strings.Contains(attachmentString(input, "cmd"), "pdftoppm -png "+attachmentPDF)
	}
	if !assembled {
		t.Fatal("streamed Messages tool arguments were not reconstructed")
	}
	broken := strings.Replace(stream, `"partial_json":"{`, `"partial_json":"broken{`, 1)
	for _, event := range attachmentStreamObjects(broken) {
		if event["type"] == "assembled_tool_use" {
			t.Fatal("malformed streamed tool arguments were accepted")
		}
	}
}

func TestAttachmentPacketTerminalRequiresCompletedStatusUsageAndText(t *testing.T) {
	candidate := liveAttachmentCases()[0]
	complete := "event: response.completed\ndata: " + `{"type":"response.completed","response":{"object":"response","status":"completed","model":"gpt-6-luna","usage":{"input_tokens":2,"output_tokens":3},"output":[{"type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"top red bottom blue"}]}]}}` + "\n\n"
	if !attachmentResponseCompleted(complete, candidate) {
		t.Fatal("valid Responses terminal was rejected")
	}
	for _, changed := range []string{
		strings.Replace(complete, `"completed"`, `"incomplete"`, 1),
		strings.Replace(complete, `"output_tokens":3`, `"output_tokens":0`, 1),
		strings.Replace(complete, `"output_tokens":3`, `"output_tokens":1.5`, 1),
		strings.Replace(complete, `"input_tokens":2`, `"input_tokens":-1`, 1),
		strings.Replace(complete, `"output_text"`, `"refusal"`, 1),
	} {
		if attachmentResponseCompleted(changed, candidate) {
			t.Fatal("unproven Responses terminal was accepted")
		}
	}
	claude := liveAttachmentCase{client: "codex", model: "claude-haiku-5.5", modelPath: "/v1/messages"}
	message := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-haiku-5-5\",\"usage\":{\"input_tokens\":2}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"top red bottom blue\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	if !attachmentResponseCompleted(message, claude) || attachmentResponseCompleted(strings.Replace(message, "end_turn", "max_tokens", 1), claude) {
		t.Fatal("Messages terminal status was not enforced")
	}
	if !attachmentResponseCompleted(strings.Replace(message, `"input_tokens":2`, `"input_tokens":0,"cache_read_input_tokens":2`, 1), claude) {
		t.Fatal("valid cached Messages usage was rejected")
	}
	if attachmentResponseCompleted(strings.Replace(message, `"output_tokens":3`, `"output_tokens":1.5`, 1), claude) {
		t.Fatal("fractional Messages usage was accepted")
	}
}

func TestAttachmentPacketOriginalClientBytesAndEveryTurnIdentity(t *testing.T) {
	candidate := liveAttachmentCases()[1]
	fixture, err := os.ReadFile(candidate.fixture)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(fixture)
	first := liveAttachmentIngressCapture{Dispatched: true, BodyComplete: true, Request: liveHTTPLog{URL: "/v1/messages?beta=true", Body: `{"model":"gpt-6-luna","output_config":{"effort":"medium"},"messages":[{"role":"user","content":"Read the PDF"}]}`}}
	secondBody := `{"model":"gpt-6-luna","output_config":{"effort":"medium"},"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"tool-a","name":"Read","input":{"file_path":"` + candidate.fixture + `"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-a","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"` + encoded + `"}}]}]}]}`
	second := liveAttachmentIngressCapture{Dispatched: true, BodyComplete: true, Request: liveHTTPLog{URL: "/v1/messages?beta=true", Body: secondBody}}
	evidence := &liveAttachmentEvidence{}
	inspectLiveOriginalClientAttachment(candidate, []liveAttachmentIngressCapture{first, second}, evidence)
	if !evidence.IngressPrepared || !evidence.SourcePreparedEqual || evidence.IngressMediaSHA256 != evidence.SourceSHA256 {
		t.Fatal("genuine original Read result lost source PDF bytes or paired tool identity")
	}
	wrongModel := second
	wrongModel.Request.Body = strings.Replace(secondBody, `"model":"gpt-6-luna"`, `"model":"other"`, 1)
	evidence = &liveAttachmentEvidence{}
	inspectLiveOriginalClientAttachment(candidate, []liveAttachmentIngressCapture{first, wrongModel}, evidence)
	if evidence.IngressPrepared {
		t.Fatal("wrong-model follow-up was masked by correct initial request")
	}
	wrongBytes := second
	wrongBytes.Request.Body = strings.Replace(secondBody, encoded, base64.StdEncoding.EncodeToString([]byte("%PDF-other")), 1)
	evidence = &liveAttachmentEvidence{}
	inspectLiveOriginalClientAttachment(candidate, []liveAttachmentIngressCapture{first, wrongBytes}, evidence)
	if evidence.SourcePreparedEqual {
		t.Fatal("altered original PDF bytes passed source parity")
	}
}
