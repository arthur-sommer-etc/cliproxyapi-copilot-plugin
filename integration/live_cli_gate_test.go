//go:build !windows

package integration

import (
	"errors"
	"strings"
	"testing"
)

const liveCapturedCLIFixAttempt = "fix-native-reserved-schema"

func liveCapturedCLIAttemptPhase(attempt string) (string, error) {
	switch attempt {
	case "", "latest-dependencies":
		return "L-latest-dependencies", nil
	case "native-stream-revalidation":
		return "L-native-stream-revalidation", nil
	case liveCapturedCLIFixAttempt:
		return "L-fix-native-reserved-schema", nil
	default:
		return "", errors.New("unsupported captured CLI attempt")
	}
}

func liveOriginalCLIClaimCells(phase string) []string {
	if phase != "L-latest-dependencies" && phase != "L-fix-native-reserved-schema" && phase != "L-native-stream-revalidation" {
		return nil
	}
	cells := []string{
		"claude-baseline",
		"claude-web-search",
		"claude-subagent",
		"codex-baseline",
		"codex-web-search",
		"codex-subagent",
	}
	keys := make([]string, 0, len(cells))
	for _, cell := range cells {
		keys = append(keys, phase+"/"+cell)
	}
	return keys
}

func liveOriginalCLIClaimCell(key string) bool {
	phase, _, ok := strings.Cut(key, "/")
	if !ok {
		return false
	}
	for _, allowed := range liveOriginalCLIClaimCells(phase) {
		if key == allowed {
			return true
		}
	}
	return false
}

func TestLiveOriginalCLIClaimCell(t *testing.T) {
	for _, phase := range []string{"L-latest-dependencies", "L-fix-native-reserved-schema", "L-native-stream-revalidation"} {
		cells := liveOriginalCLIClaimCells(phase)
		if len(cells) != 6 {
			t.Fatalf("phase %q returned %d claim cells, want exactly six", phase, len(cells))
		}
		for _, key := range cells {
			if !liveOriginalCLIClaimCell(key) {
				t.Errorf("valid original CLI claim cell %q was rejected", key)
			}
		}
	}
	invalid := []string{
		"",
		"L-latest-dependencies/claude",
		"L-latest-dependencies/codex-catalog",
		"L-latest-dependencies/claude-web_search",
		"L-latest-dependencies/codex-subagent-extra",
		"M-latest-dependencies/codex-baseline",
		"L-unknown-attempt/codex-baseline",
		"L-fix-native-reserved-schema/codex-catalog",
		"L-fix-native-reserved-schema/codex-baseline/extra",
	}
	for _, key := range invalid {
		if liveOriginalCLIClaimCell(key) {
			t.Errorf("invalid original CLI claim cell %q was accepted", key)
		}
	}
}

func TestLiveCapturedCLIAttemptPhase(t *testing.T) {
	tests := []struct {
		attempt string
		phase   string
		wantErr bool
	}{
		{attempt: "", phase: "L-latest-dependencies"},
		{attempt: "latest-dependencies", phase: "L-latest-dependencies"},
		{attempt: liveCapturedCLIFixAttempt, phase: "L-fix-native-reserved-schema"},
		{attempt: "native-stream-revalidation", phase: "L-native-stream-revalidation"},
		{attempt: "fix-native-reserved-schema-extra", wantErr: true},
		{attempt: "Latest-dependencies", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.attempt, func(t *testing.T) {
			phase, err := liveCapturedCLIAttemptPhase(test.attempt)
			if (err != nil) != test.wantErr {
				t.Fatalf("liveCapturedCLIAttemptPhase(%q) error = %v", test.attempt, err)
			}
			if phase != test.phase {
				t.Fatalf("liveCapturedCLIAttemptPhase(%q) = %q, want %q", test.attempt, phase, test.phase)
			}
		})
	}
}
