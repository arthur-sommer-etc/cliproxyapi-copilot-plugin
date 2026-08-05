package redact

import (
	"strings"
	"testing"
)

func TestTextRedactsTokens(t *testing.T) {
	t.Parallel()

	secret := "opaque-copilot-token"
	input := `Authorization: Bearer ghp_abcdefghijklmnopqrstuvwxyz1234 ` +
		`{"access_token":"github_pat_abcdefghijklmnopqrstuvwxyz1234","refresh_token":"refresh-value"} ` +
		"upstream said " + secret
	output := Text(input, secret)

	for _, forbidden := range []string{
		"ghp_abcdefghijklmnopqrstuvwxyz1234",
		"github_pat_abcdefghijklmnopqrstuvwxyz1234",
		"refresh-value",
		secret,
	} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("redacted output still contains %q: %s", forbidden, output)
		}
	}
	if strings.Count(output, "[REDACTED]") < 4 {
		t.Fatalf("expected all tokens to be visibly redacted: %s", output)
	}
}

func TestErrorBodyTruncatesBeforeLogging(t *testing.T) {
	t.Parallel()

	output := ErrorBody([]byte(strings.Repeat("x", 3000)+`{"token":"secret-token"}`), "secret-token")
	if len(output) > 2051 {
		t.Fatalf("error body was not bounded: %d bytes", len(output))
	}
	if strings.Contains(output, "secret-token") {
		t.Fatal("error body exposed a token")
	}
}
