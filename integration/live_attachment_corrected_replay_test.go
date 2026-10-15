//go:build !windows

package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestAttachmentPacketCorrectedPDFReplay(t *testing.T) {
	if os.Getenv("CPA_ATTACHMENT_CORRECTED_REPLAY") != "1" {
		t.Skip("set CPA_ATTACHMENT_CORRECTED_REPLAY=1 only after independent review of the retained PDF evidence")
	}
	caseDirectory := os.Getenv("CPA_ATTACHMENT_CORRECTED_REPLAY_CASE_DIR")
	gateDirectory := os.Getenv("CPA_ATTACHMENT_CORRECTED_REPLAY_GATE_DIR")
	outputDirectory := os.Getenv("CPA_ATTACHMENT_CORRECTED_REPLAY_OUTPUT_DIR")
	expectedCell := os.Getenv("CPA_ATTACHMENT_CORRECTED_REPLAY_CELL")
	expectedAttempt := os.Getenv("CPA_ATTACHMENT_CORRECTED_REPLAY_ATTEMPT")
	expectedVersion := os.Getenv("CPA_ATTACHMENT_CORRECTED_REPLAY_CLAUDE_VERSION")
	if expectedCell == "" || expectedAttempt == "" || expectedVersion == "" {
		t.Fatal("corrected replay requires an explicit retained cell, attempt, and client version")
	}
	for _, path := range []string{caseDirectory, gateDirectory, outputDirectory} {
		info, err := os.Stat(path)
		if err != nil || !filepath.IsAbs(path) || !info.IsDir() || info.Mode().Perm() != 0700 {
			t.Fatal("corrected replay requires existing private absolute evidence directories")
		}
	}
	claimBody, err := os.ReadFile(filepath.Join(caseDirectory, "claim.json"))
	if err != nil {
		t.Fatal("retained original case claim is missing")
	}
	var claim map[string]any
	if json.Unmarshal(claimBody, &claim) != nil || claim["cell"] != expectedCell || claim["model"] != "gpt-6-luna" || claim["attempt"] != expectedAttempt || claim["original_fixture"] != attachmentPDF {
		t.Fatal("corrected replay source case does not match the completed changed-condition PDF run")
	}
	startupPath := filepath.Join(filepath.Dir(caseDirectory), "startup-claim.json")
	startupBody, err := os.ReadFile(startupPath)
	if err != nil {
		t.Fatal("retained startup claim is missing")
	}
	var startup struct {
		Attempt          string            `json:"attempt"`
		Cases            []string          `json:"cases"`
		ExpectedVersions map[string]string `json:"expected_client_versions"`
		ObservedVersions map[string]string `json:"observed_client_versions"`
		VersionMatches   map[string]bool   `json:"client_version_match"`
		FontconfigSHA256 string            `json:"fontconfig_sha256"`
		RendererHashes   map[string]string `json:"offline_rendered_jpeg_sha256_by_case"`
	}
	if json.Unmarshal(startupBody, &startup) != nil || startup.Attempt != expectedAttempt || len(startup.Cases) != 1 || startup.Cases[0] != claim["cell"] || startup.ExpectedVersions["claude"] != expectedVersion || startup.ObservedVersions["claude"] != expectedVersion || !startup.VersionMatches["claude"] || len(startup.FontconfigSHA256) != 64 || startup.RendererHashes["claude-gpt-pdf"] != attachmentExpectedReadablePDFPageSHA256 {
		t.Fatal("retained startup version or original renderer preparation does not match this case")
	}
	priorBody, err := os.ReadFile(filepath.Join(caseDirectory, "result.json"))
	if err != nil {
		t.Fatal("retained historical result is missing")
	}
	var prior struct {
		Accepted                    bool                   `json:"accepted"`
		ExitCode                    int                    `json:"exit_code"`
		CaptureError                bool                   `json:"capture_error"`
		GateDenials                 int                    `json:"gate_denials"`
		OriginalClientDenials       int                    `json:"original_client_denials"`
		OriginalClientRefusalStatus int                    `json:"original_client_refusal_status"`
		Evidence                    liveAttachmentEvidence `json:"evidence"`
	}
	if json.Unmarshal(priorBody, &prior) != nil || prior.Accepted || prior.ExitCode != 0 || prior.CaptureError || prior.GateDenials != 0 || prior.OriginalClientDenials != 0 || prior.OriginalClientRefusalStatus != 0 || prior.Evidence.ClientMedia || prior.Evidence.NativePDFPage || prior.Evidence.PhysicalInference < 1 {
		t.Fatal("historical PDF failure differs from the original validator-only failure")
	}
	clientBody, err := os.ReadFile(filepath.Join(caseDirectory, "client-stdout.jsonl"))
	if err != nil {
		t.Fatal("retained original client events are missing")
	}
	candidate := liveAttachmentCases()[1]
	if err := liveAttachmentFixtureValid(candidate); err != nil {
		t.Fatal("pinned original single-page PDF source is unavailable or changed")
	}
	gatePaths, err := filepath.Glob(filepath.Join(gateDirectory, "gate-*.json"))
	if err != nil {
		t.Fatal("retained public capture inventory is unavailable")
	}
	sort.Strings(gatePaths)
	physicalInference := 0
	for _, path := range gatePaths {
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal("retained public capture is unavailable")
		}
		var capture liveServerToolCapture
		if json.Unmarshal(body, &capture) != nil {
			t.Fatal("retained public capture is invalid")
		}
		if capture.Category == "inference" && capture.PhaseCell == claim["cell"] && capture.Dispatched {
			physicalInference++
		}
	}
	if physicalInference != prior.Evidence.PhysicalInference || physicalInference < 1 {
		t.Fatal("retained physical inference count differs from the historical case result")
	}
	client := liveAttachmentCLI{stdout: clientBody, exitCode: prior.ExitCode}
	gate := &liveServerToolGate{directory: gateDirectory}
	evidence := inspectLiveAttachmentEvidence(t, gate, candidate, claim["cell"].(string), client)
	ingress := &liveAttachmentIngress{directory: filepath.Join(caseDirectory, "original-client-hop")}
	captures, err := ingress.captures()
	if err != nil || len(captures) == 0 {
		t.Fatal("retained original client request captures are missing")
	}
	for _, capture := range captures {
		if !capture.Dispatched || !capture.BodyComplete || capture.ErrorClass != "" || capture.Response.Status < 200 || capture.Response.Status >= 300 {
			t.Fatal("retained original client request was incomplete or refused")
		}
	}
	inspectLiveOriginalClientAttachment(candidate, captures, &evidence)
	evidence.PhysicalInference = physicalInference
	complete := evidence.ClientFinal && evidence.ClientToolCall && evidence.ClientToolResult && evidence.ClientMedia && evidence.ClientAnswer && evidence.IngressPrepared && liveAttachmentMediaPrepared(candidate, evidence) && evidence.PublicModel && evidence.PublicEffort && evidence.PublicEndpoint && evidence.PublicMedia && evidence.IngressMediaSHA256 == evidence.PublicMediaSHA256 && evidence.PublicToolCall && evidence.PublicToolResult && evidence.PublicTerminal && evidence.PublicStreamProven && evidence.PhysicalInference > 0 && evidence.ClientMediaSHA256 == attachmentExpectedReadablePDFPageSHA256
	if !complete {
		t.Fatal("retained original PDF bodies do not prove a completed corrected evaluation")
	}
	hashes := make(map[string]string)
	sourcePaths := []string{startupPath, filepath.Join(caseDirectory, "claim.json"), filepath.Join(caseDirectory, "result.json"), filepath.Join(caseDirectory, "client-stdout.jsonl"), filepath.Join(caseDirectory, "client-invocation.json")}
	ingressPaths, err := filepath.Glob(filepath.Join(caseDirectory, "original-client-hop", "client-hop-*.json"))
	if err != nil || len(ingressPaths) == 0 {
		t.Fatal("retained original-client request body captures are unavailable")
	}
	sort.Strings(ingressPaths)
	sourcePaths = append(sourcePaths, ingressPaths...)
	sourcePaths = append(sourcePaths, gatePaths...)
	for _, path := range sourcePaths {
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal("retained replay source changed or became unavailable")
		}
		sum := sha256.Sum256(body)
		key, relErr := filepath.Rel(filepath.Dir(caseDirectory), path)
		if relErr != nil {
			t.Fatal("retained replay source path cannot be bound")
		}
		if filepath.Dir(path) == gateDirectory {
			key = filepath.Join("public-gate", filepath.Base(path))
		}
		hashes[key] = hex.EncodeToString(sum[:])
	}
	livePacketWriteJSON(t, outputDirectory, "corrected-pdf-evaluation.json", map[string]any{"historical_result_preserved": true, "historical_accepted": false, "corrected_accepted": true, "replay_only": true, "provider_dispatches": 0, "new_condition_evaluated": "native_single_page_range_start_one_end_at_most_twenty_with_exact_read_arguments_and_one_jpeg", "source_case": caseDirectory, "source_gate": gateDirectory, "source_sha256_by_relative_path": hashes, "source_fixture_sha256": livePacketFileSHA256(t, attachmentPDF), "startup_client_versions": startup.ObservedVersions, "evidence": evidence})
	t.Log("retained exact original-client and public PDF bodies passed corrected read-only evaluation")
}
