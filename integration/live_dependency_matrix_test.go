//go:build !windows

package integration

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func liveDependencyMatrixCell(key string) bool {
	for model := range liveCopilotRoutes {
		for _, api := range []string{"Chat", "Responses", "Claude_Messages"} {
			for _, stream := range []bool{false, true} {
				if key == fmt.Sprintf("M-latest-dependencies/%s-%s-stream-%t", model, api, stream) {
					return true
				}
			}
		}
	}
	return false
}

func TestLiveDependencyProtocolMatrix(t *testing.T) {
	if os.Getenv("CPA_LIVE_DEPENDENCY_MATRIX") != "1" {
		t.Skip("set CPA_LIVE_DEPENDENCY_MATRIX=1 to capture the latest dependency matrix")
	}
	gate, base, stop := startLiveCapturedHarness(t, nil, liveEndpointOverrides(nil))
	defer stop()
	for model, endpoint := range liveCopilotRoutes {
		if !liveCapturedCatalogAdvertisesEndpoint(gate, model, endpoint) {
			t.Fatalf("fresh catalog does not advertise %s for %s", endpoint, model)
		}
	}
	attempted, valid := 0, 0
	for _, model := range []string{"gemini-3.8-flash", "gpt-6-luna", "claude-haiku-5.5"} {
		for _, api := range []string{"Chat", "Responses", "Claude Messages"} {
			for _, stream := range []bool{false, true} {
				cell := fmt.Sprintf("%s-%s-stream-%t", model, strings.ReplaceAll(api, " ", "_"), stream)
				t.Run(cell, func(t *testing.T) {
					if err := gate.setPhaseCell("M-latest-dependencies", cell); err != nil {
						t.Fatal(err)
					}
					key := "M-latest-dependencies/" + cell
					gate.setFreshClaim("inference", key)
					defer gate.clearFreshInferenceClaim(key)
					defer gate.freezeCell(key)
					attempted++
					if runLiveMatrixCell(t, base, model, api, stream) {
						valid++
					}
				})
			}
		}
	}
	if _, _, _, _, err := gate.countsFor(""); err != nil || livePacketDeniedCount(gate) != 0 {
		t.Fatal("matrix capture or dispatch failed")
	}
	t.Logf("captured live matrix requests attempted=%d valid=%d", attempted, valid)
	if attempted != 18 || valid != 18 {
		t.Errorf("latest dependency matrix passed %d of %d requests, want 18 of 18", valid, attempted)
	}
}
