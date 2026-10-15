package translate

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func syntheticAttachmentData(t *testing.T) (string, string) {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, 2, 2))
	canvas.Set(0, 0, color.RGBA{R: 255, A: 255})
	canvas.Set(1, 1, color.RGBA{B: 255, A: 255})
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, canvas); err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(bytes.NewReader(imageData.Bytes())); err != nil {
		t.Fatal(err)
	}
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n")
	stream := "BT /F1 12 Tf 10 30 Td (Synthetic attachment fixture) Tj ET\n"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 60] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	offsets := make([]int, len(objects))
	for index, object := range objects {
		offsets[index] = pdf.Len()
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xref := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&pdf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(imageData.Bytes()), "data:application/pdf;base64," + base64.StdEncoding.EncodeToString(pdf.Bytes())
}

func syntheticAttachmentRequest(t *testing.T, format sdktranslator.Format, imageData, pdfData string) []byte {
	t.Helper()
	var root map[string]any
	switch format {
	case sdktranslator.FormatOpenAI:
		root = map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "Inspect these synthetic fixtures."},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": imageData, "detail": "auto"}},
			map[string]any{"type": "file", "file": map[string]any{"filename": "synthetic.pdf", "file_data": pdfData}},
		}}}}
	case sdktranslator.FormatOpenAIResponse:
		root = map[string]any{"input": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "input_text", "text": "Inspect these synthetic fixtures."},
			map[string]any{"type": "input_image", "image_url": imageData, "detail": "auto"},
			map[string]any{"type": "input_file", "filename": "synthetic.pdf", "file_data": pdfData},
		}}}}
	case sdktranslator.FormatClaude:
		root = map[string]any{"max_tokens": 256, "messages": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "Inspect these synthetic fixtures."},
			map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": strings.SplitN(imageData, ",", 2)[1]}},
			map[string]any{"type": "document", "title": "synthetic.pdf", "source": map[string]any{"type": "base64", "media_type": "application/pdf", "data": strings.SplitN(pdfData, ",", 2)[1]}},
		}}}}
	}
	body, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestSyntheticInlineAttachmentsPreserveAllRequestRoutes(t *testing.T) {
	imageData, pdfData := syntheticAttachmentData(t)
	formats := []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude}
	for _, source := range formats {
		for _, target := range formats {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s-to-%s-stream-%t", source, target, stream), func(t *testing.T) {
					request := syntheticAttachmentRequest(t, source, imageData, pdfData)
					endpoint := map[sdktranslator.Format]string{sdktranslator.FormatOpenAI: EndpointChatCompletions, sdktranslator.FormatOpenAIResponse: EndpointResponses, sdktranslator.FormatClaude: EndpointMessages}[target]
					converted, err := RequestForEndpointFrom(source.String(), "exact-upstream-id", request, stream, endpoint)
					if err != nil {
						t.Fatal(err)
					}
					root, err := decodeObject(converted)
					if err != nil {
						t.Fatal(err)
					}
					attachments, err := requestAttachments(root, target)
					if err != nil {
						t.Fatal(err)
					}
					if len(attachments) != 2 || attachments[0].data != imageData || attachments[1].data != pdfData || attachments[1].filename != "synthetic.pdf" {
						t.Fatalf("synthetic attachment bytes, MIME, or filename changed: %s", converted)
					}
					if root["model"] != "exact-upstream-id" || root["stream"] != stream {
						t.Fatalf("model or stream changed: %s", converted)
					}
					if bytes.Contains(converted, []byte("cpa-inline-pdf:")) {
						t.Fatalf("internal attachment marker escaped: %s", converted)
					}
				})
			}
		}
	}
}

func TestSyntheticAttachmentImageAliasAndDetail(t *testing.T) {
	imageData, _ := syntheticAttachmentData(t)
	for _, source := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse} {
		for _, target := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse} {
			request := map[string]any{"input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_image", "url": imageData, "detail": "low"}}}}}
			if source == sdktranslator.FormatOpenAI {
				request = map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": imageData, "detail": "low"}}}}}}
			}
			if source == target {
				continue
			}
			body, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			endpoint := EndpointChatCompletions
			if target == sdktranslator.FormatOpenAIResponse {
				endpoint = EndpointResponses
			}
			converted, err := RequestForEndpointFrom(source.String(), "exact-upstream-id", body, false, endpoint)
			if err != nil {
				t.Fatal(err)
			}
			root, err := decodeObject(converted)
			if err != nil {
				t.Fatal(err)
			}
			attachments, err := requestAttachments(root, target)
			if err != nil || len(attachments) != 1 || attachments[0].data != imageData || attachments[0].detail != "low" {
				t.Fatalf("image URL alias or detail changed: %s; error=%v", converted, err)
			}
		}
	}
}

func TestSyntheticAttachmentOriginalDetailContract(t *testing.T) {
	imageData, _ := syntheticAttachmentData(t)
	for _, from := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse} {
		for _, to := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude} {
			for _, detail := range []string{"auto", "low", "high", "original"} {
				for _, stream := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s-to-%s-detail-%s-stream-%t", from, to, detail, stream), func(t *testing.T) {
						root := map[string]any{"input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": imageData, "detail": detail}}}}}
						if from == sdktranslator.FormatOpenAI {
							root = map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": imageData, "detail": detail}}}}}}
						}
						body, err := json.Marshal(root)
						if err != nil {
							t.Fatal(err)
						}
						originalBody := append([]byte(nil), body...)
						endpoint := map[sdktranslator.Format]string{sdktranslator.FormatOpenAI: EndpointChatCompletions, sdktranslator.FormatOpenAIResponse: EndpointResponses, sdktranslator.FormatClaude: EndpointMessages}[to]
						converted, err := RequestForEndpointFrom(from.String(), "exact-upstream-id", body, stream, endpoint)
						reject := from != to && (detail == "original" && (from == sdktranslator.FormatOpenAI || to == sdktranslator.FormatOpenAI) || to == sdktranslator.FormatClaude && detail != "auto")
						if reject {
							if err == nil {
								t.Fatalf("unsupported image detail was accepted or normalized: %s", converted)
							}
							if from != to && (to == sdktranslator.FormatClaude && detail != "auto" || detail == "original") {
								var detailErr *UnsupportedImageDetailError
								wantEndpoint := endpoint
								if to == sdktranslator.FormatClaude {
									wantEndpoint = EndpointMessages
								} else if to == sdktranslator.FormatOpenAI {
									wantEndpoint = EndpointChatCompletions
								}
								if !errors.As(err, &detailErr) || detailErr.Endpoint != wantEndpoint || detailErr.Detail != detail || !bytes.Equal(body, originalBody) {
									t.Fatalf("unsupported Messages image detail lost its typed context or source request: error=%v", err)
								}
							}
							if from == sdktranslator.FormatOpenAIResponse {
								if err := validateResponsesRequestForTarget(body, to); err == nil {
									t.Fatal("direct Responses preflight accepted unsupported image detail")
								}
							}
							return
						}
						if err != nil {
							t.Fatal(err)
						}
						translated, err := decodeObject(converted)
						if err != nil {
							t.Fatal(err)
						}
						attachments, err := requestAttachments(translated, to)
						if err != nil || len(attachments) != 1 || attachments[0].data != imageData {
							t.Fatalf("image content changed: %s; error=%v", converted, err)
						}
						if to != sdktranslator.FormatClaude && attachments[0].detail != detail {
							t.Fatalf("image detail changed from %s to %s", detail, attachments[0].detail)
						}
						if translated["model"] != "exact-upstream-id" || translated["stream"] != stream {
							t.Fatalf("upstream model or stream changed: %s", converted)
						}
					})
				}
			}
		}
	}
}

func TestValidateRequestAttachmentsForEndpointPreservesOriginalSemantics(t *testing.T) {
	imageData := "data:image/png;base64,YQ=="
	for _, test := range []struct {
		name     string
		body     string
		endpoint string
		wantCode bool
		wantPath string
	}{
		{
			name:     "Responses high detail to Messages is incompatible",
			body:     `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"` + imageData + `","detail":"high"}]}]}`,
			endpoint: EndpointMessages,
			wantCode: true,
			wantPath: EndpointMessages,
		},
		{
			name:     "Responses high detail to Chat is preserved",
			body:     `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"` + imageData + `","detail":"high"}]}]}`,
			endpoint: EndpointChatCompletions,
		},
		{
			name:     "Responses original detail to Chat is incompatible",
			body:     `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"` + imageData + `","detail":"original"}]}]}`,
			endpoint: EndpointChatCompletions,
			wantCode: true,
			wantPath: EndpointChatCompletions,
		},
		{
			name:     "attachment in custom tool output is checked",
			body:     `{"input":[{"type":"custom_tool_call_output","call_id":"call_fixture","output":[{"type":"input_image","image_url":"` + imageData + `","detail":"high"}]}]}`,
			endpoint: EndpointMessages,
			wantCode: true,
			wantPath: EndpointMessages,
		},
		{
			name:     "attachment in function output is checked",
			body:     `{"input":[{"type":"function_call_output","call_id":"call_fixture","output":[{"type":"input_image","image_url":"` + imageData + `","detail":"high"}]}]}`,
			endpoint: EndpointMessages,
			wantCode: true,
			wantPath: EndpointMessages,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateRequestAttachmentsForEndpoint(sdktranslator.FormatOpenAIResponse.String(), test.endpoint, []byte(test.body))
			var detailErr *UnsupportedImageDetailError
			if test.wantCode {
				if !errors.As(err, &detailErr) || detailErr.Endpoint != test.wantPath {
					t.Fatalf("attachment validation error = %v, want endpoint %q image-detail error", err, test.wantPath)
				}
			} else if err != nil {
				t.Fatalf("compatible attachment was rejected: %v", err)
			}
		})
	}
}

func TestValidateRequestAttachmentsForEndpointUsesClaudeSourceFixture(t *testing.T) {
	body := readLiveBodyFixture(t, "native-attachment-captures/claude-png", "request.json")
	if err := ValidateRequestAttachmentsForEndpoint(sdktranslator.FormatClaude.String(), EndpointMessages, body); err != nil {
		t.Fatalf("captured Claude image request was rejected on its native endpoint: %v", err)
	}

	uploadedFile := []byte(`{"messages":[{"role":"user","content":[{"type":"document","source":{"type":"file","file_id":"file_uploaded"}}]}]}`)
	if err := ValidateRequestAttachmentsForEndpoint(sdktranslator.FormatClaude.String(), EndpointResponses, uploadedFile); err == nil {
		t.Fatal("Claude uploaded file reference was accepted for cross-format translation")
	}
}

func TestSyntheticAttachmentUnsupportedSemanticsReject(t *testing.T) {
	imageData, pdfData := syntheticAttachmentData(t)
	tests := []struct {
		name string
		from sdktranslator.Format
		to   sdktranslator.Format
		part map[string]any
	}{
		{name: "Responses uploaded file", from: sdktranslator.FormatOpenAIResponse, to: sdktranslator.FormatOpenAI, part: map[string]any{"type": "input_file", "file_id": "file_external"}},
		{name: "Responses remote PDF", from: sdktranslator.FormatOpenAIResponse, to: sdktranslator.FormatClaude, part: map[string]any{"type": "input_file", "file_url": "https://example.test/synthetic.pdf"}},
		{name: "Responses PDF detail", from: sdktranslator.FormatOpenAIResponse, to: sdktranslator.FormatOpenAI, part: map[string]any{"type": "input_file", "file_data": pdfData, "detail": "high"}},
		{name: "Responses invalid base64", from: sdktranslator.FormatOpenAIResponse, to: sdktranslator.FormatOpenAI, part: map[string]any{"type": "input_file", "file_data": "data:application/pdf;base64,%%%"}},
		{name: "Responses filename object", from: sdktranslator.FormatOpenAIResponse, to: sdktranslator.FormatClaude, part: map[string]any{"type": "input_file", "file_data": pdfData, "filename": map[string]any{"name": "synthetic.pdf"}}},
		{name: "Responses image detail to Messages", from: sdktranslator.FormatOpenAIResponse, to: sdktranslator.FormatClaude, part: map[string]any{"type": "input_image", "image_url": imageData, "detail": "high"}},
		{name: "Chat uploaded file", from: sdktranslator.FormatOpenAI, to: sdktranslator.FormatClaude, part: map[string]any{"type": "file", "file": map[string]any{"file_id": "file_external"}}},
		{name: "Chat non-PDF document", from: sdktranslator.FormatOpenAI, to: sdktranslator.FormatOpenAIResponse, part: map[string]any{"type": "file", "file": map[string]any{"file_data": "data:text/plain;base64,c3ludGhldGlj"}}},
		{name: "Chat image detail to Messages", from: sdktranslator.FormatOpenAI, to: sdktranslator.FormatClaude, part: map[string]any{"type": "image_url", "image_url": map[string]any{"url": imageData, "detail": "low"}}},
		{name: "Messages document context", from: sdktranslator.FormatClaude, to: sdktranslator.FormatOpenAIResponse, part: map[string]any{"type": "document", "source": map[string]any{"type": "base64", "media_type": "application/pdf", "data": strings.SplitN(pdfData, ",", 2)[1]}, "context": "Preserve this semantic context."}},
		{name: "Messages document citations", from: sdktranslator.FormatClaude, to: sdktranslator.FormatOpenAIResponse, part: map[string]any{"type": "document", "source": map[string]any{"type": "base64", "media_type": "application/pdf", "data": strings.SplitN(pdfData, ",", 2)[1]}, "citations": map[string]any{"enabled": true}}},
		{name: "Messages text document", from: sdktranslator.FormatClaude, to: sdktranslator.FormatOpenAIResponse, part: map[string]any{"type": "document", "source": map[string]any{"type": "text", "media_type": "text/plain", "data": "Synthetic document text."}}},
		{name: "Messages remote PDF to Chat", from: sdktranslator.FormatClaude, to: sdktranslator.FormatOpenAI, part: map[string]any{"type": "document", "source": map[string]any{"type": "url", "url": "https://example.test/synthetic.pdf"}}},
		{name: "Messages uploaded image", from: sdktranslator.FormatClaude, to: sdktranslator.FormatOpenAIResponse, part: map[string]any{"type": "image", "source": map[string]any{"type": "file", "file_id": "file_external"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{test.part}}}}
			if test.from == sdktranslator.FormatOpenAIResponse {
				root = map[string]any{"input": []any{map[string]any{"role": "user", "content": []any{test.part}}}}
			}
			body, err := json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			endpoint := map[sdktranslator.Format]string{sdktranslator.FormatOpenAI: EndpointChatCompletions, sdktranslator.FormatOpenAIResponse: EndpointResponses, sdktranslator.FormatClaude: EndpointMessages}[test.to]
			for _, stream := range []bool{false, true} {
				if _, err := RequestForEndpointFrom(test.from.String(), "exact-upstream-id", body, stream, endpoint); err == nil {
					t.Fatal("unsupported attachment semantics were accepted")
				}
			}
			if test.from == sdktranslator.FormatOpenAIResponse {
				if err := validateResponsesRequestForTarget(body, test.to); err == nil {
					t.Fatal("direct preflight accepted unsupported attachment semantics")
				}
			}
		})
	}
}

func TestSyntheticAttachmentDuplicatePDFsRetainOrderAndText(t *testing.T) {
	_, pdfData := syntheticAttachmentData(t)
	request := map[string]any{"input": []any{map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "input_file", "filename": "first.pdf", "file_data": pdfData},
		map[string]any{"type": "input_text", "text": "Exact intervening text."},
		map[string]any{"type": "input_file", "filename": "second.pdf", "file_data": pdfData},
	}}}}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	converted, err := RequestForEndpointFrom("openai-response", "exact-upstream-id", body, false, EndpointChatCompletions)
	if err != nil {
		t.Fatal(err)
	}
	root, err := decodeObject(converted)
	if err != nil {
		t.Fatal(err)
	}
	parts := arrayValue(objectValue(arrayValue(root["messages"])[0])["content"])
	if len(parts) != 3 || rawStringValue(objectValue(objectValue(parts[0])["file"])["filename"]) != "first.pdf" || rawStringValue(objectValue(parts[1])["text"]) != "Exact intervening text." || rawStringValue(objectValue(objectValue(parts[2])["file"])["filename"]) != "second.pdf" {
		t.Fatalf("duplicate attachment order or text changed: %s", converted)
	}
}

func TestNativeAttachmentReferencesRemainUnchanged(t *testing.T) {
	for _, test := range []struct {
		source   string
		endpoint string
		body     string
	}{
		{source: "openai-response", endpoint: EndpointResponses, body: `{"input":[{"role":"user","content":[{"type":"input_image","file_id":"file_image","detail":"high"},{"type":"input_file","file_id":"file_pdf","detail":"high"}]}]}`},
		{source: "openai", endpoint: EndpointChatCompletions, body: `{"messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"file_pdf"}}]}]}`},
		{source: "claude", endpoint: EndpointMessages, body: `{"messages":[{"role":"user","content":[{"type":"document","source":{"type":"file","file_id":"file_pdf"},"citations":{"enabled":true}}]}]}`},
	} {
		converted, err := RequestForEndpointFrom(test.source, "exact-upstream-id", []byte(test.body), false, test.endpoint)
		if err != nil {
			t.Fatal(err)
		}
		root, err := decodeObject(converted)
		if err != nil {
			t.Fatal(err)
		}
		delete(root, "model")
		delete(root, "stream")
		original, err := decodeObject([]byte(test.body))
		if err != nil {
			t.Fatal(err)
		}
		want, err := json.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(root)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("native attachment references changed: %s", converted)
		}
	}
}

func TestSyntheticAttachmentToolResultsPreserveContent(t *testing.T) {
	imageData, pdfData := syntheticAttachmentData(t)
	formats := []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude}
	for _, from := range formats {
		for _, to := range formats {
			if from == to {
				continue
			}
			for _, withPDF := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s-to-%s-PDF-%t", from, to, withPDF), func(t *testing.T) {
					root, err := decodeObject(syntheticAttachmentRequest(t, from, imageData, pdfData))
					if err != nil {
						t.Fatal(err)
					}
					var parts []any
					if from == sdktranslator.FormatOpenAIResponse {
						parts = arrayValue(objectValue(arrayValue(root["input"])[0])["content"])
					} else {
						parts = arrayValue(objectValue(arrayValue(root["messages"])[0])["content"])
					}
					if !withPDF {
						parts = parts[:2]
					}
					switch from {
					case sdktranslator.FormatOpenAI:
						root["messages"] = []any{
							map[string]any{"role": "user", "content": "Inspect the tool result."},
							map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"type": "function", "id": "call_exact", "function": map[string]any{"name": "inspect", "arguments": "{}"}}}},
							map[string]any{"role": "tool", "tool_call_id": "call_exact", "content": parts},
						}
					case sdktranslator.FormatOpenAIResponse:
						root["input"] = []any{
							map[string]any{"type": "message", "role": "user", "content": "Inspect the tool result."},
							map[string]any{"type": "function_call", "id": "fc_exact", "call_id": "call_exact", "name": "inspect", "arguments": "{}"},
							map[string]any{"type": "function_call_output", "call_id": "call_exact", "output": parts},
						}
					case sdktranslator.FormatClaude:
						root["messages"] = []any{
							map[string]any{"role": "user", "content": "Inspect the tool result."},
							map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "call_exact", "name": "inspect", "input": map[string]any{}}}},
							map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "call_exact", "content": parts}}},
						}
					}
					body, err := json.Marshal(root)
					if err != nil {
						t.Fatal(err)
					}
					endpoint := map[sdktranslator.Format]string{sdktranslator.FormatOpenAI: EndpointChatCompletions, sdktranslator.FormatOpenAIResponse: EndpointResponses, sdktranslator.FormatClaude: EndpointMessages}[to]
					converted, err := RequestForEndpointFrom(from.String(), "exact-upstream-id", body, false, endpoint)
					if withPDF && to == sdktranslator.FormatOpenAI {
						if err == nil {
							t.Fatal("Chat tool-result PDF was silently accepted")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					translated, err := decodeObject(converted)
					if err != nil {
						t.Fatal(err)
					}
					attachments, err := requestAttachments(translated, to)
					if err != nil {
						t.Fatal(err)
					}
					wantCount := 1
					if withPDF {
						wantCount = 2
					}
					if len(attachments) != wantCount || attachments[0].data != imageData || withPDF && (attachments[1].data != pdfData || attachments[1].filename != "synthetic.pdf") {
						t.Fatalf("tool-result attachment content changed: %s", converted)
					}
				})
			}
		}
	}
}
