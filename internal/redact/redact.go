package redact

import (
	"regexp"
	"strings"
)

var tokenPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization\s*[:=]\s*(?:bearer|token)\s+)[^\s,;"']+`),
	regexp.MustCompile(`(?i)("(?:access_token|refresh_token|github_access_token|github_refresh_token|token)"\s*:\s*")[^"]+(")`),
	regexp.MustCompile(`\b(?:gh[opurs]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,})\b`),
}

func Text(value string, secrets ...string) string {
	out := value
	for _, secret := range secrets {
		if secret = strings.TrimSpace(secret); secret != "" {
			out = strings.ReplaceAll(out, secret, "[REDACTED]")
		}
	}
	for _, pattern := range tokenPatterns {
		out = pattern.ReplaceAllString(out, `${1}[REDACTED]${2}`)
	}
	return out
}

func ErrorBody(body []byte, secrets ...string) string {
	const maxErrorBody = 2048
	text := strings.TrimSpace(string(body))
	if len(text) > maxErrorBody {
		text = text[:maxErrorBody] + "…"
	}
	return Text(text, secrets...)
}
