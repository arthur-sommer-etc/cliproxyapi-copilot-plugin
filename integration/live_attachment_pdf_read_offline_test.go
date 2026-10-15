//go:build !windows

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestAttachmentOriginalClaudePDFReadOffline(t *testing.T) {
	if os.Getenv("CPA_OFFLINE_CLAUDE_PDF_READ") != "1" {
		t.Skip("set CPA_OFFLINE_CLAUDE_PDF_READ=1 for an installed Claude Code PDF Read preparation probe")
	}
	candidate := liveAttachmentCases()[1]
	if err := liveAttachmentFixtureValid(candidate); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var requestCount int
	var readResult bool
	var readError bool
	var mediaKinds []string
	var preparedBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/messages" {
			http.Error(writer, "unexpected local path", http.StatusBadRequest)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body["model"] != candidate.model {
			http.Error(writer, "unexpected local request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		requestCount++
		turn := requestCount
		encoded, _ := json.Marshal(body)
		preparedBodies = append(preparedBodies, string(encoded))
		if turn == 2 {
			readResult = strings.Contains(string(encoded), `"tool_use_id":"toolu_pdf_read"`)
			readError = strings.Contains(string(encoded), `"is_error":true`)
			walkAttachmentJSON(body, func(item map[string]any) {
				kind := attachmentString(item, "type")
				if kind == "image" || kind == "input_image" || kind == "document" {
					source, _ := item["source"].(map[string]any)
					mediaKinds = append(mediaKinds, kind+":"+attachmentString(source, "media_type"))
				}
			})
		}
		mu.Unlock()
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set("Cache-Control", "no-cache")
		write := func(event string, data any) {
			encoded, _ := json.Marshal(data)
			fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", event, encoded)
			if flusher, ok := writer.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		write("message_start", map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id": fmt.Sprintf("msg_offline_%d", turn), "type": "message", "role": "assistant", "model": candidate.model,
				"content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 1, "output_tokens": 0},
			},
		})
		if turn == 1 {
			write("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "toolu_pdf_read", "name": "Read", "input": map[string]any{}}})
			input, _ := json.Marshal(map[string]any{"file_path": candidate.fixture, "pages": "1"})
			write("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(input)}})
			write("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
			write("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]int{"output_tokens": 1}})
		} else {
			write("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
			write("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "Offline Read preparation observed."}})
			write("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
			write("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]int{"output_tokens": 5}})
		}
		write("message_stop", map[string]any{"type": "message_stop"})
	}))
	defer server.Close()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "claude"), 0700); err != nil {
		t.Fatal(err)
	}
	result := liveAttachmentRunCLI(t, candidate, server.URL, root)
	clientEvidence := inspectLiveAttachmentClient(candidate, result.stdout)
	mu.Lock()
	count, paired, failed, kinds, prepared := requestCount, readResult, readError, append([]string(nil), mediaKinds...), append([]string(nil), preparedBodies...)
	mu.Unlock()
	t.Logf("offline original Read: exit=%d requests=%d paired=%t tool_error=%t media_kinds=%v client_media=%t", result.exitCode, count, paired, failed, kinds, clientEvidence.ClientMedia)
	if count < 2 || !paired || failed || len(kinds) != 1 || kinds[0] != "image:image/jpeg" || clientEvidence.ClientMediaSHA256 == "" {
		t.Fatal("installed original Claude PDF Read did not return a successful native tool result")
	}
	var captures []liveAttachmentIngressCapture
	for _, body := range prepared {
		captures = append(captures, liveAttachmentIngressCapture{Dispatched: true, BodyComplete: true, Request: liveHTTPLog{URL: "/v1/messages", Body: body}})
	}
	var ingressEvidence liveAttachmentEvidence
	inspectLiveOriginalClientAttachment(candidate, captures, &ingressEvidence)
	proof := ingressEvidence
	proof.ClientMediaSHA256 = clientEvidence.ClientMediaSHA256
	proof.ClientMediaCount = clientEvidence.ClientMediaCount
	proof.ClientReadArgsSHA256 = clientEvidence.ClientReadArgsSHA256
	proof.ClientReadCount = clientEvidence.ClientReadCount
	proof.PublicMediaCount = 1
	proof.PublicReadCount = 1
	proof.PublicReadArgsSHA256 = ingressEvidence.IngressReadArgsSHA256
	if !ingressEvidence.NativePDFPage || !ingressEvidence.IngressPrepared || ingressEvidence.SourcePreparedEqual || ingressEvidence.IngressMediaSHA256 != clientEvidence.ClientMediaSHA256 || !liveAttachmentMediaPrepared(candidate, proof) {
		t.Fatal("original PDF source and native JPEG preparation did not retain distinct, matching provenance")
	}
	if ingressEvidence.IngressMediaSHA256 != attachmentExpectedReadablePDFPageSHA256 {
		t.Fatal("installed native Read output did not match the visually verified printed-code JPEG")
	}
	withoutPages := append([]liveAttachmentIngressCapture(nil), captures...)
	withoutPages[1].Request.Body = strings.Replace(withoutPages[1].Request.Body, `"pages":"1"`, `"pages":"2"`, 1)
	var missingPages liveAttachmentEvidence
	inspectLiveOriginalClientAttachment(candidate, withoutPages, &missingPages)
	missingProof := proof
	missingProof.NativePDFPage = missingPages.NativePDFPage
	missingProof.IngressReadArgsSHA256 = missingPages.IngressReadArgsSHA256
	if missingPages.NativePDFPage || liveAttachmentMediaPrepared(candidate, missingProof) {
		t.Fatal("a different native PDF page selection passed the original Read provenance check")
	}
	wrongHash := proof
	wrongHash.ClientMediaSHA256 = strings.Repeat("0", 64)
	if liveAttachmentMediaPrepared(candidate, wrongHash) {
		t.Fatal("a tampered native JPEG passed the original-to-prepared byte check")
	}
	wrongPublicArgs := proof
	wrongPublicArgs.PublicReadArgsSHA256 = strings.Repeat("0", 64)
	if liveAttachmentMediaPrepared(candidate, wrongPublicArgs) {
		t.Fatal("different public Read arguments passed the original-client correlation check")
	}
	extraPage := proof
	extraPage.IngressMediaCount = 2
	if liveAttachmentMediaPrepared(candidate, extraPage) {
		t.Fatal("multiple rendered PDF pages passed the one-page fixture check")
	}
	wrongFile := append([]liveAttachmentIngressCapture(nil), captures...)
	wrongFile[1].Request.Body = strings.ReplaceAll(wrongFile[1].Request.Body, attachmentPDF, attachmentPDF+".other")
	var invalidFile liveAttachmentEvidence
	inspectLiveOriginalClientAttachment(candidate, wrongFile, &invalidFile)
	if invalidFile.NativePDFPage || invalidFile.IngressReadCount != 0 {
		t.Fatal("a different PDF file passed the native Read source correlation check")
	}
	wrongType := append([]liveAttachmentIngressCapture(nil), captures...)
	wrongType[1].Request.Body = strings.Replace(wrongType[1].Request.Body, `"media_type":"image/jpeg"`, `"media_type":"image/png"`, 1)
	var invalidJPEG liveAttachmentEvidence
	inspectLiveOriginalClientAttachment(candidate, wrongType, &invalidJPEG)
	if invalidJPEG.IngressPrepared || invalidJPEG.NativePDFPage {
		t.Fatal("a mislabeled native image passed the JPEG preparation check")
	}
	for _, pages := range []string{"1", "1-1", "1-2", "1-19", "1-20"} {
		if !liveAttachmentNativePDFPageOne(pages) {
			t.Fatal("original one-page PDF page selection was rejected")
		}
	}
	for _, pages := range []string{"2", "1-0", "1-01", "1-21", "2-20", "1,2", "all", ""} {
		if liveAttachmentNativePDFPageOne(pages) {
			t.Fatal("non-original or invalid PDF page selection was accepted")
		}
	}
}
