//go:build !windows

package integration

import (
	"strings"
	"testing"
)

func TestAttachmentActualPDFPageMustMatchVerifiedSourceRender(t *testing.T) {
	for _, candidate := range liveAttachmentCases() {
		if candidate.media != "pdf" {
			continue
		}
		t.Run(candidate.cell, func(t *testing.T) {
			want := attachmentExpectedReadablePDFPageSHA256
			if candidate.client == "codex" {
				want = strings.Repeat("1", 64)
				candidate.expectedPageSHA256 = want
			}
			proof := liveAttachmentEvidence{
				ClientMediaSHA256: want, IngressMediaSHA256: want, PublicMediaSHA256: want,
				NativePDFPage: true, SourceSHA256: "source-pdf", ClientMediaCount: 1, IngressMediaCount: 1, PublicMediaCount: 1,
				ClientReadCount: 1, IngressReadCount: 1, PublicReadCount: 1,
				ClientReadArgsSHA256: "args", IngressReadArgsSHA256: "args", PublicReadArgsSHA256: "args",
				IngressMediaDetail: "high", PublicMediaDetail: "high",
			}
			if !liveAttachmentMediaPrepared(candidate, proof) {
				t.Fatal("verified source-bound page was rejected")
			}
			proof.ClientAnswer = true
			proof.ClientMediaSHA256, proof.IngressMediaSHA256, proof.PublicMediaSHA256 = strings.Repeat("0", 64), strings.Repeat("0", 64), strings.Repeat("0", 64)
			if liveAttachmentMediaPrepared(candidate, proof) {
				t.Fatal("consistently substituted page passed despite correct textual answer")
			}
		})
	}
}

func TestAttachmentNativeResponsesRequiresExactImageDetail(t *testing.T) {
	for _, candidate := range liveAttachmentCases() {
		if candidate.client != "codex" || candidate.modelPath != "/responses" {
			continue
		}
		t.Run(candidate.cell, func(t *testing.T) {
			candidate.expectedPageSHA256 = strings.Repeat("1", 64)
			proof := liveAttachmentEvidence{SourcePreparedEqual: candidate.media == "png", IngressMediaSHA256: candidate.expectedPageSHA256, PublicMediaSHA256: candidate.expectedPageSHA256, IngressMediaDetail: "high", PublicMediaDetail: "high"}
			if !liveAttachmentMediaPrepared(candidate, proof) {
				t.Fatal("exact native Responses image preparation rejected")
			}
			for _, detail := range []string{"low", ""} {
				proof.PublicMediaDetail = detail
				if liveAttachmentMediaPrepared(candidate, proof) {
					t.Fatal("downgraded or missing native Responses image detail admitted")
				}
			}
		})
	}
}
