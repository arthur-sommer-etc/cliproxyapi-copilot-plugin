package provider

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

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
