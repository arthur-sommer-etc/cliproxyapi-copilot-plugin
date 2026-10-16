package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/redact"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/tidwall/gjson"
)

type StatusError struct {
	Code       string
	Message    string
	HTTPStatus int
	Retryable  bool
}

func (e *StatusError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func statusError(code, message string, status int) error {
	if status == 0 {
		status = http.StatusInternalServerError
	}
	return &StatusError{Code: code, Message: message, HTTPStatus: status}
}

func translationStatusError(err error) error {
	if err == nil {
		return nil
	}
	var imageDetailErr *translate.UnsupportedImageDetailError
	if errors.As(err, &imageDetailErr) {
		return statusError("unsupported_image_detail", imageDetailErr.Error(), http.StatusUnprocessableEntity)
	}
	return statusError("translation_error", err.Error(), http.StatusUnprocessableEntity)
}

// upstreamRejectionEvidenceHeaders restricts retained rejection evidence to
// non-sensitive response headers. Authentication and request headers never
// appear here.
var upstreamRejectionEvidenceHeaders = map[string]bool{
	"content-type":          true,
	"retry-after":           true,
	"x-github-request-guid": true,
	"x-github-request-id":   true,
	"x-ratelimit-limit":     true,
	"x-ratelimit-remaining": true,
	"x-ratelimit-reset":     true,
	"x-ratelimit-resource":  true,
	"x-ratelimit-type":      true,
	"x-ratelimit-used":      true,
	"x-request-id":          true,
}

// recordUpstreamRejection retains a private redacted record of a non-2xx
// Copilot response when CPA_COPILOT_UPSTREAM_EVIDENCE_FILE is set. The body is
// re-redacted defensively; evidence failures never affect inference.
func recordUpstreamRejection(status int, endpoint string, header http.Header, body string, secrets ...string) {
	path := strings.TrimSpace(os.Getenv("CPA_COPILOT_UPSTREAM_EVIDENCE_FILE"))
	if path == "" {
		return
	}
	kept := make(map[string]string, len(upstreamRejectionEvidenceHeaders))
	for name, values := range header {
		if upstreamRejectionEvidenceHeaders[strings.ToLower(name)] {
			kept[strings.ToLower(name)] = redact.Text(strings.Join(values, ", "), secrets...)
		}
	}
	entry, err := json.Marshal(struct {
		Time     string            `json:"time"`
		Status   int               `json:"status"`
		Endpoint string            `json:"endpoint"`
		Headers  map[string]string `json:"headers"`
		Body     string            `json:"body"`
	}{Time: time.Now().UTC().Format(time.RFC3339Nano), Status: status, Endpoint: endpoint, Headers: kept, Body: redact.Text(body, secrets...)})
	if err != nil {
		return
	}
	file, errOpen := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if errOpen != nil {
		return
	}
	defer file.Close()
	if errChmod := file.Chmod(0600); errChmod != nil {
		return
	}
	_, _ = file.Write(append(entry, '\n'))
}

// upstreamStatusError keeps provider response text out of client-visible errors.
func upstreamStatusError(status int, body string) error {
	code := "upstream_error"
	message := fmt.Sprintf("Copilot upstream returned HTTP %d", status)
	if (status == http.StatusBadRequest || status == http.StatusNotFound || status == http.StatusUnprocessableEntity) &&
		gjson.Valid(body) &&
		gjson.Get(body, "error.code").String() == "model_not_supported" &&
		gjson.Get(body, "error.param").String() == "model" &&
		gjson.Get(body, "error.type").String() == "invalid_request_error" {
		code = "model_not_supported"
		message = "The requested model is not supported."
	}
	if status == http.StatusBadRequest && gjson.Valid(body) &&
		gjson.Get(body, "error.code").String() == "unsupported_value" &&
		gjson.Get(body, "error.message").String() == "The use of the web search tool is not supported." {
		code = "unsupported_value"
		message = `{"error":{"message":"The use of the web search tool is not supported.","code":"unsupported_value","type":"invalid_request_error"}}`
	}
	return &StatusError{
		Code:       code,
		Message:    message,
		HTTPStatus: status,
		Retryable:  status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500,
	}
}
