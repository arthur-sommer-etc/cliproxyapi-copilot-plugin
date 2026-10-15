package provider

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/tidwall/gjson"
)

func TestTranslationStatusErrorClassifiesUnsupportedImageDetail(t *testing.T) {
	imageDetailErr := &translate.UnsupportedImageDetailError{Endpoint: translate.EndpointMessages, Detail: "high"}
	err := translationStatusError(fmt.Errorf("translate captured request: %w", imageDetailErr))
	statusErr, ok := err.(*StatusError)
	if !ok || statusErr.Code != "unsupported_image_detail" || statusErr.HTTPStatus != http.StatusUnprocessableEntity || statusErr.Message != imageDetailErr.Error() {
		t.Fatalf("unexpected image detail status: %#v", statusErr)
	}
	var got *translate.UnsupportedImageDetailError
	if !errors.As(imageDetailErr, &got) || got.Endpoint != translate.EndpointMessages || got.Detail != "high" {
		t.Fatalf("typed image detail context changed: %#v", got)
	}
}

func TestTranslationStatusErrorKeepsGeneralTranslationError(t *testing.T) {
	err := translationStatusError(errors.New("unsupported input"))
	statusErr, ok := err.(*StatusError)
	if !ok || statusErr.Code != "translation_error" || statusErr.HTTPStatus != http.StatusUnprocessableEntity || statusErr.Message != "unsupported input" {
		t.Fatalf("unexpected translation status: %#v", statusErr)
	}
}

func TestUpstreamStatusErrorOmitsResponseText(t *testing.T) {
	detail := "echoed SYNTHETIC_PROMPT_MARKER synthetic/path/file.txt synthetic-token-marker"
	tests := []struct {
		name      string
		status    int
		retryable bool
	}{
		{name: "gateway failure", status: http.StatusBadGateway, retryable: true},
		{name: "unauthorized", status: http.StatusUnauthorized, retryable: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := upstreamStatusError(test.status, detail)
			statusErr, ok := err.(*StatusError)
			if !ok {
				t.Fatal("upstreamStatusError did not return a structured status error")
			}
			if statusErr.Code != "upstream_error" || statusErr.HTTPStatus != test.status || statusErr.Retryable != test.retryable {
				t.Fatal("upstream error metadata changed")
			}
			if statusErr.Message != fmt.Sprintf("Copilot upstream returned HTTP %d", test.status) {
				t.Fatal("upstream error message changed")
			}
			for _, marker := range []string{"SYNTHETIC_PROMPT_MARKER", "synthetic/path/file.txt", "synthetic-token-marker"} {
				if strings.Contains(statusErr.Error(), marker) {
					t.Fatal("upstream error exposed provider response text")
				}
			}
		})
	}
}

func TestUpstreamModelSupportErrorClassification(t *testing.T) {
	body := `{"error":{"message":"echoed SYNTHETIC_PROMPT_MARKER synthetic-token-marker","code":"model_not_supported","param":"model","type":"invalid_request_error"}}`
	for _, test := range []struct {
		name      string
		status    int
		body      string
		wantCode  string
		retryable bool
	}{
		{name: "native model rejection", status: http.StatusBadRequest, body: body, wantCode: "model_not_supported"},
		{name: "model missing", status: http.StatusNotFound, body: body, wantCode: "model_not_supported"},
		{name: "unsupported model", status: http.StatusUnprocessableEntity, body: body, wantCode: "model_not_supported"},
		{name: "authentication stays credential scoped", status: http.StatusUnauthorized, body: body, wantCode: "upstream_error"},
		{name: "server failure stays retryable", status: http.StatusBadGateway, body: body, wantCode: "upstream_error", retryable: true},
		{name: "unstructured provider text", status: http.StatusBadRequest, body: "model_not_supported", wantCode: "upstream_error"},
		{name: "truncated error", status: http.StatusBadRequest, body: `{"error":{"code":"model_not_supported"`, wantCode: "upstream_error"},
		{name: "different request error", status: http.StatusBadRequest, body: `{"error":{"code":"unsupported_value","param":"model"}}`, wantCode: "upstream_error"},
		{name: "different rejected field", status: http.StatusBadRequest, body: `{"error":{"code":"model_not_supported","param":"tools","type":"invalid_request_error"}}`, wantCode: "upstream_error"},
		{name: "different error type", status: http.StatusBadRequest, body: `{"error":{"code":"model_not_supported","param":"model","type":"authentication_error"}}`, wantCode: "upstream_error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := upstreamStatusError(test.status, test.body)
			statusErr, ok := err.(*StatusError)
			if !ok || statusErr.Code != test.wantCode || statusErr.HTTPStatus != test.status || statusErr.Retryable != test.retryable {
				t.Fatalf("unexpected structured error: %#v", statusErr)
			}
			if test.wantCode == "model_not_supported" && statusErr.Message != "The requested model is not supported." {
				t.Fatalf("model rejection lost the CPA-recognized message: %q", statusErr.Message)
			}
			for _, marker := range []string{"SYNTHETIC_PROMPT_MARKER", "synthetic-token-marker"} {
				if strings.Contains(statusErr.Error(), marker) {
					t.Fatal("model rejection exposed provider response text")
				}
			}
		})
	}
}

func TestObservedClaudeWebSearchRejectionRemainsRequestScoped(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "translate", "testdata", "live-claude-web-search-unsupported", "upstream-response.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		status int
		body   string
		code   string
	}{
		{name: "captured unsupported hosted search", status: http.StatusBadRequest, body: string(body), code: "unsupported_value"},
		{name: "provider extra private fields are omitted", status: http.StatusBadRequest, body: strings.ReplaceAll(string(body), `"code":"unsupported_value"`, `"code":"unsupported_value","private":"synthetic-token-marker"`), code: "unsupported_value"},
		{name: "same code with unknown message", status: http.StatusBadRequest, body: `{"error":{"code":"unsupported_value","message":"synthetic-token-marker"}}`, code: "upstream_error"},
		{name: "same message with unknown code", status: http.StatusBadRequest, body: strings.ReplaceAll(string(body), "unsupported_value", "unknown_code"), code: "upstream_error"},
		{name: "credential rejection remains credential scoped", status: http.StatusUnauthorized, body: string(body), code: "upstream_error"},
		{name: "server rejection remains retryable", status: http.StatusBadGateway, body: string(body), code: "upstream_error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			statusErr, ok := upstreamStatusError(test.status, test.body).(*StatusError)
			if !ok || statusErr.Code != test.code || statusErr.HTTPStatus != test.status || statusErr.Retryable != (test.status >= 500) {
				t.Fatalf("unexpected hosted search rejection: %#v", statusErr)
			}
			if test.code == "unsupported_value" && (gjson.Get(statusErr.Message, "error.type").String() != "invalid_request_error" || gjson.Get(statusErr.Message, "error.message").String() != "The use of the web search tool is not supported.") {
				t.Fatal("observed hosted search rejection lost its fixed request error classification")
			}
			if strings.Contains(statusErr.Message, "synthetic-token-marker") {
				t.Fatal("provider response private content leaked")
			}
		})
	}
}
