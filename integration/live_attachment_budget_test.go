//go:build !windows

package integration

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestAttachmentBudgetReservationPersistsCellAndPacketCaps(t *testing.T) {
	directory := t.TempDir()
	gate := newLiveServerToolGate(t, directory)
	gate.publicAPI, _ = url.Parse("https://copilot.example")
	cells := []string{"X-final/claude-gemini-png", "X-final/codex-gemini-png"}
	if err := gate.withLedger(func(ledger *liveDispatchLedger) error {
		ledger.Frozen["X/claude-gemini-png"] = true
		ledger.Budgets = map[string]liveDispatchBudget{"final": {Cells: map[string]int{cells[0]: 4, cells[1]: 4}, Limit: 5, Provenance: "synthetic changed source and artifact"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := liveAttachmentValidateBudget(directory, "final", cells); err != nil {
		t.Fatal(err)
	}
	if err := gate.setPhaseCell("X-final", "claude-gemini-png"); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 4; index++ {
		if _, _, _, allowed := gate.reserve("/chat/completions"); !allowed {
			t.Fatalf("dispatch %d denied before cap", index)
		}
	}
	if _, _, _, allowed := gate.reserve("/chat/completions"); allowed {
		t.Fatal("per-cell cap exceeded")
	}
	if err := gate.setPhaseCell("X-final", "codex-gemini-png"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, allowed := gate.reserve("/chat/completions"); !allowed {
		t.Fatal("remaining packet allowance denied")
	}
	if _, _, _, allowed := gate.reserve("/chat/completions"); allowed {
		t.Fatal("packet cap exceeded")
	}
	if err := liveAttachmentValidateBudget(directory, "final", cells); err == nil {
		t.Fatal("attempted cells were allowed to restart")
	}
	body, err := os.ReadFile(filepath.Join(directory, "dispatch-ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ledger liveDispatchLedger
	if err := json.Unmarshal(body, &ledger); err != nil {
		t.Fatal(err)
	}
	if ledger.Inference != 5 || ledger.Counts[cells[0]] != 4 || ledger.Counts[cells[1]] != 1 || !ledger.Frozen["X/claude-gemini-png"] {
		t.Fatal("ledger lost exact accounting or frozen history")
	}
	restarted := newLiveServerToolGate(t, directory)
	restarted.publicAPI, _ = url.Parse("https://copilot.example")
	if err := restarted.setPhaseCell("X-final", "codex-gemini-png"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, allowed := restarted.reserve("/chat/completions"); allowed {
		t.Fatal("restart reset persisted packet cap")
	}
}

func TestAttachmentReadinessFailureStopsFurtherDispatch(t *testing.T) {
	gate := newLiveServerToolGate(t, t.TempDir())
	gate.publicAPI, _ = url.Parse("https://copilot.example")
	if err := liveAttachmentReadinessError(gate); err != nil {
		t.Fatal(err)
	}
	gate.captureErr = errors.New("synthetic capture failure")
	if liveAttachmentReadinessError(gate) == nil {
		t.Fatal("systemic readiness failure ignored")
	}
	if _, _, _, allowed := gate.reserve("/chat/completions"); allowed {
		t.Fatal("dispatch continued after capture failure")
	}
}
