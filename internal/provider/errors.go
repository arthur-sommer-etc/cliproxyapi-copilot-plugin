package provider

import (
	"fmt"
	"net/http"
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

// upstreamStatusError keeps provider response text out of client-visible errors.
func upstreamStatusError(status int, _ string) error {
	message := fmt.Sprintf("Copilot upstream returned HTTP %d", status)
	return &StatusError{
		Code:       "upstream_error",
		Message:    message,
		HTTPStatus: status,
		Retryable:  status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500,
	}
}
