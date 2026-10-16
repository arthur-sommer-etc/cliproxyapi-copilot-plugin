package integration

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/provider"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const liveCopilotClientKey = "fixture-client-key"

var liveCopilotRoutes = map[string]string{
	"gemini-3.8-flash": "/chat/completions",
	"gpt-6-luna":       "/responses",
	"claude-haiku-5.5": "/v1/messages",
}

type liveCatalogModel struct {
	ID                 string   `json:"id"`
	SupportedEndpoints []string `json:"supported_endpoints"`
}

type liveCatalogResponse struct {
	Data []liveCatalogModel `json:"data"`
}

type liveProfile struct {
	Source      string
	AuthMode    string
	StorageJSON []byte
	Catalog     map[string][]string
}

type liveCatalogHost struct {
	client *http.Client
	mu     sync.Mutex
	body   []byte
}

func TestLiveCopilotProtocolMatrix(t *testing.T) {
	if os.Getenv("CPA_LIVE_COPILOT_MATRIX") != "1" {
		t.Skip("set CPA_LIVE_COPILOT_MATRIX=1 to run real Copilot requests")
	}
	binary := os.Getenv("CPA_BINARY")
	if binary == "" {
		t.Fatal("CPA_BINARY is required for the live native host matrix")
	}
	profile := liveProfileFromEnvironment(t)
	overrides := liveEndpointOverrides(profile.Catalog)
	for model, endpoint := range liveCopilotRoutes {
		advertised := profile.Catalog[model]
		t.Logf("model=%s advertised_endpoints=%v selected_endpoint=%s selected_by=native endpoint override", model, advertised, endpoint)
	}
	base, stop := startLiveNativeHost(t, binary, profile.StorageJSON, profile.AuthMode, overrides)
	defer stop()

	valid := 0
	attempted := 0
	for _, model := range []string{"gemini-3.8-flash", "gpt-6-luna", "claude-haiku-5.5"} {
		for _, api := range []string{"Chat", "Responses", "Claude Messages"} {
			name := fmt.Sprintf("%s/%s/stream=false", model, strings.ReplaceAll(api, " ", "_"))
			if !t.Run(name, func(t *testing.T) {
				attempted++
				if runLiveMatrixCell(t, base, model, api, false) {
					valid++
				}
			}) {
				continue
			}
			streamName := fmt.Sprintf("%s/%s/stream=true", model, strings.ReplaceAll(api, " ", "_"))
			if t.Run(streamName, func(t *testing.T) {
				attempted++
				if runLiveMatrixCell(t, base, model, api, true) {
					valid++
				}
			}) {
				continue
			}
		}
	}
	t.Logf("live matrix requests attempted=%d valid=%d", attempted, valid)
	t.Run("UnsupportedModel", func(t *testing.T) {
		body, status, _, err := liveProxyCall(base+"/v1/responses", map[string]any{
			"model": "cpa-live-model-not-in-catalog-7f8c",
			"input": "Reply with one word.",
		})
		if err != nil {
			t.Errorf("unsupported-model request failed before receiving a response")
			return
		}
		if status < 400 || status >= 500 || safeLiveErrorCode(body) != "model_not_found" {
			t.Errorf("unsupported model returned HTTP %d, error_code=%q; expected CPA model_not_found", status, safeLiveErrorCode(body))
		}
	})
	t.Run("UnsupportedCompactionFeature", func(t *testing.T) {
		body, status, _, err := liveProxyCall(base+"/v1/responses/compact", map[string]any{
			"model": "claude-haiku-5.5",
			"input": "Summarize this short request.",
		})
		if err != nil {
			t.Errorf("unsupported-feature request failed before receiving a response")
			return
		}
		if status != http.StatusUnprocessableEntity || safeLiveErrorType(body) != "invalid_request_error" {
			t.Errorf("unsupported compaction returned HTTP %d, error_type=%q; expected CPA's 422 request error", status, safeLiveErrorType(body))
		}
	})
}

func runLiveMatrixCell(t *testing.T, base, model, api string, stream bool) bool {
	t.Helper()
	path, payload := liveMatrixRequest(model, api, stream)
	body, status, contentType, callErr := liveProxyCall(base+path, payload)
	captureLiveMatrixBodies(t, payload, body, status, contentType)
	if callErr != nil {
		t.Errorf("request failed before receiving a response")
		return false
	}
	if status != http.StatusOK {
		t.Errorf("request returned HTTP %d, error_class=%q", status, safeLiveErrorClass(body, status))
		return false
	}
	text, returnedModel, schemaOK, terminalOK := validateLiveResponse(api, stream, contentType, body)
	if !schemaOK {
		t.Errorf("response did not match the %s client schema", api)
		return false
	}
	if !liveMatrixResponseModelMatches(model, returnedModel) {
		t.Errorf("response model=%q does not identify requested model %q", returnedModel, model)
		return false
	}
	if !strings.Contains(strings.ToUpper(text), "LIVE_MATRIX_OK") {
		t.Errorf("assistant text did not include the requested marker")
		return false
	}
	if stream && !terminalOK {
		t.Errorf("stream did not include the protocol terminal event")
		return false
	}
	t.Logf("validated requested model and text response; response_model=%s observed_native_canonical=%t chars=%d terminal=%t", returnedModel, returnedModel != model, len([]rune(text)), terminalOK)
	return true
}

func liveMatrixResponseModelMatches(requested, returned string) bool {
	return requested == returned || requested == "claude-haiku-5.5" && returned == "claude-haiku-5-5"
}

func liveDebugArtifactDirectory(t *testing.T, prefix string) string {
	t.Helper()
	root := strings.TrimSpace(os.Getenv("CPA_LIVE_COPILOT_DEBUG_DIR"))
	if root == "" {
		return ""
	}
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal("could not resolve private live diagnostics directory")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal("could not create private live diagnostics directory")
	}
	directory, err := os.MkdirTemp(root, prefix+"-")
	if err != nil {
		t.Fatal("could not create retained live diagnostics directory")
	}
	return directory
}

var liveLogSection = regexp.MustCompile(`^=== API (REQUEST|RESPONSE) ([0-9]+) ===$`)
var liveAuthenticationTokenField = regexp.MustCompile(`(?i)"(?:token|access_token|refresh_token|github_token|copilot_token|oauth_token)"\s*:`)
var liveModelBodyIdentity = regexp.MustCompile(`"(?:model|choices|output)"\s*:|"type"\s*:\s*"(?:message|response[.a-z_]*|chat[.a-z_]*)"`)
var liveCredentialHeader = regexp.MustCompile(`(?i)^\s*(authorization|proxy-authorization|x-api-key|api-key|x-auth-token|cookie|set-cookie|auth):`)

func sanitizeLiveHostLog(body []byte) []byte {
	lines := strings.SplitAfter(string(body), "\n")
	authAttempts := make(map[string]bool)
	attempt := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if section := liveLogSection.FindStringSubmatch(trimmed); len(section) > 0 {
			attempt = section[2]
		} else if strings.HasPrefix(trimmed, "Upstream URL:") {
			endpoint, err := url.Parse(strings.TrimSpace(strings.TrimPrefix(trimmed, "Upstream URL:")))
			if err == nil && (strings.Contains(endpoint.Path, "/copilot_internal/") || strings.Contains(endpoint.Path, "/login/") || strings.HasSuffix(endpoint.Path, "/token") || strings.HasSuffix(endpoint.Path, "/user")) {
				authAttempts[attempt] = true
			}
		}
	}
	var sanitized strings.Builder
	inAuthAttempt, omitBody := false, false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "=== ") {
			omitBody = false
			inAuthAttempt = false
			if section := liveLogSection.FindStringSubmatch(trimmed); len(section) > 0 {
				inAuthAttempt = authAttempts[section[2]]
			}
		}
		if liveCredentialHeader.MatchString(line) {
			continue
		}
		if inAuthAttempt && trimmed == "Body:" {
			sanitized.WriteString(line)
			sanitized.WriteString("[AUTHENTICATION BODY OMITTED]\n")
			omitBody = true
			continue
		}
		if !omitBody {
			sanitized.WriteString(line)
		}
	}
	remaining := sanitized.String()
	var protected strings.Builder
	for {
		bodyAt := strings.Index(remaining, "Body:\n")
		if bodyAt < 0 {
			break
		}
		protected.WriteString(remaining[:bodyAt+len("Body:\n")])
		remaining = remaining[bodyAt+len("Body:\n"):]
		end := strings.Index(remaining, "\n=== ")
		if end < 0 {
			end = len(remaining)
		}
		section := remaining[:end]
		if liveAuthenticationTokenField.MatchString(section) && !liveModelBodyIdentity.MatchString(section) {
			protected.WriteString("[AUTHENTICATION BODY OMITTED]\n")
		} else {
			protected.WriteString(section)
		}
		remaining = remaining[end:]
	}
	if liveAuthenticationTokenField.MatchString(remaining) && !liveModelBodyIdentity.MatchString(remaining) {
		protected.WriteString("[AUTHENTICATION BODY OMITTED]\n")
	} else {
		protected.WriteString(remaining)
	}
	return []byte(protected.String())
}

func scrubLiveHostLogs(directory string) error {
	return filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Chmod(path, 0700)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, sanitizeLiveHostLog(body), 0600); err != nil {
			return err
		}
		return os.Chmod(path, 0600)
	})
}

func captureLiveMatrixBodies(t *testing.T, payload any, response []byte, status int, contentType string) {
	t.Helper()
	directory := liveDebugArtifactDirectory(t, "matrix-body")
	if directory == "" {
		return
	}
	request, err := json.Marshal(payload)
	if err != nil {
		t.Fatal("could not encode captured matrix request")
	}
	metadata, err := json.Marshal(map[string]any{"test": t.Name(), "http_status": status, "content_type": contentType})
	if err != nil {
		t.Fatal("could not encode captured matrix metadata")
	}
	for name, body := range map[string][]byte{"client-request.json": request, "client-response.body": response, "metadata.json": metadata} {
		if err := os.WriteFile(filepath.Join(directory, name), body, 0600); err != nil {
			t.Fatal("could not retain private live matrix body")
		}
	}
	t.Logf("retained client request and response bodies: %s", directory)
}

func TestLiveResponsesStreamValidation(t *testing.T) {
	const model = "gpt-6-luna"
	const marker = "LIVE_MATRIX_OK"
	completed := `data: {"type":"response.completed","response":{"object":"response","status":"completed","model":"gpt-6-luna","output":[{"type":"message","content":[{"type":"output_text","text":"LIVE_MATRIX_OK"}]}]}}` + "\n\n"
	tests := []struct {
		name         string
		body         string
		wantText     string
		wantModel    string
		wantSchema   bool
		wantTerminal bool
	}{
		{
			name: "delta text wins over completed snapshot",
			body: `data: {"type":"response.created","response":{"object":"response","status":"in_progress","model":"gpt-6-luna"}}` + "\n\n" +
				`data: {"type":"response.output_text.delta","delta":"LIVE_MATRIX_"}` + "\n\n" +
				`data: {"type":"response.output_text.delta","delta":"OK"}` + "\n\n" + completed,
			wantText: marker, wantModel: model, wantSchema: true, wantTerminal: true,
		},
		{
			name:         "completed snapshot supplies missing delta text",
			body:         completed,
			wantText:     marker,
			wantModel:    model,
			wantSchema:   true,
			wantTerminal: true,
		},
		{
			name:         "failed completion is not a valid response schema",
			body:         `data: {"type":"response.completed","response":{"object":"response","status":"failed","model":"gpt-6-luna","output":[]}}` + "\n\n",
			wantModel:    model,
			wantTerminal: true,
		},
		{
			name:         "malformed event invalidates stream",
			body:         completed + "data: {broken json}\n\n",
			wantText:     marker,
			wantModel:    model,
			wantTerminal: true,
		},
		{
			name:         "event after completion invalidates stream",
			body:         completed + `data: {"type":"response.output_text.delta","delta":"late"}` + "\n\n",
			wantText:     marker,
			wantModel:    model,
			wantTerminal: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			text, returnedModel, schemaOK, terminalOK := validateLiveResponse("Responses", true, "text/event-stream", []byte(test.body))
			if text != test.wantText {
				t.Errorf("stream text=%q, want %q", text, test.wantText)
			}
			if returnedModel != test.wantModel {
				t.Errorf("stream model=%q, want exact requested model %q", returnedModel, test.wantModel)
			}
			if schemaOK != test.wantSchema {
				t.Errorf("schema valid=%t, want %t", schemaOK, test.wantSchema)
			}
			if terminalOK != test.wantTerminal {
				t.Errorf("terminal event=%t, want %t", terminalOK, test.wantTerminal)
			}
		})
	}
}

func TestLiveChatAndClaudeStreamValidation(t *testing.T) {
	tests := []struct {
		name         string
		api          string
		body         string
		wantModel    string
		wantText     string
		wantSchema   bool
		wantTerminal bool
	}{
		{
			name:      "Chat chunk schema and done marker",
			api:       "Chat",
			body:      `data: {"object":"chat.completion.chunk","model":"gemini-3.8-flash","choices":[{"index":0,"delta":{"role":"assistant","content":"LIVE_MATRIX_OK"},"finish_reason":null}]}` + "\n\n" + `data: {"object":"chat.completion.chunk","model":"gemini-3.8-flash","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" + "data: [DONE]\n\n",
			wantModel: "gemini-3.8-flash", wantText: "LIVE_MATRIX_OK", wantSchema: true, wantTerminal: true,
		},
		{
			name:         "Chat rejects missing finish reason",
			api:          "Chat",
			body:         `data: {"object":"chat.completion.chunk","model":"gemini-3.8-flash","choices":[{"index":0,"delta":{"role":"assistant","content":"LIVE_MATRIX_OK"},"finish_reason":null}]}` + "\n\n" + "data: [DONE]\n\n",
			wantModel:    "gemini-3.8-flash",
			wantText:     "LIVE_MATRIX_OK",
			wantTerminal: true,
		},
		{
			name:         "Chat rejects non-success finish reason",
			api:          "Chat",
			body:         `data: {"object":"chat.completion.chunk","model":"gemini-3.8-flash","choices":[{"index":0,"delta":{"role":"assistant","content":"LIVE_MATRIX_OK"},"finish_reason":"length"}]}` + "\n\n" + "data: [DONE]\n\n",
			wantModel:    "gemini-3.8-flash",
			wantText:     "LIVE_MATRIX_OK",
			wantTerminal: true,
		},
		{
			name:         "Chat rejects non chunk object",
			api:          "Chat",
			body:         `data: {"object":"chat.completion","model":"gemini-3.8-flash","choices":[{"delta":{"content":"LIVE_MATRIX_OK"}}]}` + "\n\n" + "data: [DONE]\n\n",
			wantModel:    "gemini-3.8-flash",
			wantText:     "LIVE_MATRIX_OK",
			wantTerminal: true,
		},
		{
			name:         "Chat data after done marker invalidates stream",
			api:          "Chat",
			body:         `data: {"object":"chat.completion.chunk","model":"gemini-3.8-flash","choices":[{"index":0,"delta":{"role":"assistant","content":"LIVE_MATRIX_OK"},"finish_reason":null}]}` + "\n\n" + "data: [DONE]\n\n" + `data: {"object":"chat.completion.chunk","model":"gemini-3.8-flash","choices":[{"index":0,"delta":{"content":"late"},"finish_reason":null}]}` + "\n\n",
			wantModel:    "gemini-3.8-flash",
			wantText:     "LIVE_MATRIX_OK",
			wantTerminal: true,
		},
		{
			name: "Claude message lifecycle and text delta",
			api:  "Claude Messages",
			body: `data: {"type":"message_start","message":{"type":"message","role":"assistant","model":"claude-haiku-5.5","content":[]}}` + "\n\n" +
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"LIVE_MATRIX_OK"}}` + "\n\n" +
				`data: {"type":"content_block_stop","index":0}` + "\n\n" +
				`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null}}` + "\n\n" +
				`data: {"type":"message_stop"}` + "\n\n",
			wantModel: "claude-haiku-5.5", wantText: "LIVE_MATRIX_OK", wantSchema: true, wantTerminal: true,
		},
		{
			name: "Claude rejects message stop without end turn reason",
			api:  "Claude Messages",
			body: `data: {"type":"message_start","message":{"type":"message","role":"assistant","model":"claude-haiku-5.5","content":[]}}` + "\n\n" +
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"LIVE_MATRIX_OK"}}` + "\n\n" +
				`data: {"type":"message_delta","delta":{"stop_reason":null,"stop_sequence":null}}` + "\n\n" +
				`data: {"type":"message_stop"}` + "\n\n",
			wantModel: "claude-haiku-5.5", wantText: "LIVE_MATRIX_OK", wantTerminal: true,
		},
		{
			name: "Claude rejects tool-use stop reason in text-only matrix",
			api:  "Claude Messages",
			body: `data: {"type":"message_start","message":{"type":"message","role":"assistant","model":"claude-haiku-5.5","content":[]}}` + "\n\n" +
				`data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null}}` + "\n\n" +
				`data: {"type":"message_stop"}` + "\n\n",
			wantModel: "claude-haiku-5.5", wantTerminal: true,
		},
		{
			name: "Claude rejects missing root identity",
			api:  "Claude Messages",
			body: `data: {"type":"message_start","message":{"type":"message","role":"user","content":[]}}` + "\n\n" +
				`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"LIVE_MATRIX_OK"}}` + "\n\n" +
				`data: {"type":"message_stop"}` + "\n\n",
			wantText:     "LIVE_MATRIX_OK",
			wantTerminal: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			text, returnedModel, schemaOK, terminalOK := validateLiveResponse(test.api, true, "text/event-stream", []byte(test.body))
			if text != test.wantText {
				t.Errorf("stream text=%q, want %q", text, test.wantText)
			}
			if returnedModel != test.wantModel {
				t.Errorf("stream model=%q, want exact requested model %q", returnedModel, test.wantModel)
			}
			if schemaOK != test.wantSchema {
				t.Errorf("schema valid=%t, want %t", schemaOK, test.wantSchema)
			}
			if terminalOK != test.wantTerminal {
				t.Errorf("terminal event=%t, want %t", terminalOK, test.wantTerminal)
			}
		})
	}
}

func TestLiveJSONResponsesRequireSuccessfulTerminalReason(t *testing.T) {
	tests := []struct {
		name  string
		api   string
		body  string
		valid bool
	}{
		{name: "Chat completed", api: "Chat", body: `{"object":"chat.completion","model":"gemini-3.8-flash","choices":[{"message":{"role":"assistant","content":"LIVE_MATRIX_OK"},"finish_reason":"stop"}]}`, valid: true},
		{name: "Chat missing finish reason", api: "Chat", body: `{"object":"chat.completion","model":"gemini-3.8-flash","choices":[{"message":{"role":"assistant","content":"LIVE_MATRIX_OK"}}]}`},
		{name: "Chat rejected finish reason", api: "Chat", body: `{"object":"chat.completion","model":"gemini-3.8-flash","choices":[{"message":{"role":"assistant","content":"LIVE_MATRIX_OK"},"finish_reason":"length"}]}`},
		{name: "Messages completed", api: "Claude Messages", body: `{"type":"message","role":"assistant","model":"claude-haiku-5.5","content":[{"type":"text","text":"LIVE_MATRIX_OK"}],"stop_reason":"end_turn"}`, valid: true},
		{name: "Messages missing stop reason", api: "Claude Messages", body: `{"type":"message","role":"assistant","model":"claude-haiku-5.5","content":[{"type":"text","text":"LIVE_MATRIX_OK"}]}`},
		{name: "Messages rejected stop reason", api: "Claude Messages", body: `{"type":"message","role":"assistant","model":"claude-haiku-5.5","content":[{"type":"text","text":"LIVE_MATRIX_OK"}],"stop_reason":"tool_use"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, valid, terminal := validateLiveResponse(test.api, false, "application/json", []byte(test.body))
			if valid != test.valid || terminal != test.valid {
				t.Fatalf("schema valid=%t terminal=%t, want schema valid=%t terminal=%t", valid, terminal, test.valid, test.valid)
			}
		})
	}
}

func liveProfileFromEnvironment(t *testing.T) liveProfile {
	t.Helper()
	storageJSON, source, authMode, err := liveCredentialSourceFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	return discoverLiveProfile(t, storageJSON, source, authMode)
}

func liveCredentialSourceFromEnvironment() ([]byte, string, string, error) {
	authPath, authFileConfigured := os.LookupEnv("CPA_LIVE_COPILOT_AUTH_FILE")
	modeOverride, modeOverrideConfigured := os.LookupEnv("CPA_LIVE_COPILOT_AUTH_MODE")
	authMode, err := selectLiveAuthMode(authFileConfigured, modeOverride, modeOverrideConfigured)
	if err != nil {
		return nil, "", "", err
	}
	if authFileConfigured {
		if strings.TrimSpace(authPath) == "" {
			return nil, "", "", errors.New("CPA_LIVE_COPILOT_AUTH_FILE must name a Copilot auth file")
		}
		storageJSON, err := readLiveCopilotAuthFile(authPath)
		if err != nil {
			return nil, "", "", err
		}
		return storageJSON, "auth-file", authMode, nil
	}
	token, err := readCopilotCLIKeychainToken()
	if err != nil {
		return nil, "", "", errors.New("Copilot CLI credential is unavailable")
	}
	login, userID, err := liveGitHubIdentity(token)
	if err != nil {
		return nil, "", "", errors.New("could not resolve authenticated Copilot identity")
	}
	storageJSON, err := liveCopilotStorage(token, login, userID)
	if err != nil {
		return nil, "", "", errors.New("could not prepare in-memory live auth state")
	}
	return storageJSON, "copilot-cli-keychain", authMode, nil
}

func selectLiveAuthMode(authFileConfigured bool, override string, overrideConfigured bool) (string, error) {
	if overrideConfigured {
		switch strings.TrimSpace(override) {
		case "token_exchange", "direct_oauth":
			return strings.TrimSpace(override), nil
		default:
			return "", errors.New("CPA_LIVE_COPILOT_AUTH_MODE must be token_exchange or direct_oauth")
		}
	}
	if authFileConfigured {
		return "token_exchange", nil
	}
	return "direct_oauth", nil
}

func readLiveCopilotAuthFile(path string) ([]byte, error) {
	storageJSON, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("Copilot auth file could not be read")
	}
	if err := validateLiveCopilotAuthStorage(storageJSON); err != nil {
		return nil, err
	}
	return storageJSON, nil
}

func validateLiveCopilotAuthStorage(storageJSON []byte) error {
	var header struct {
		Disabled *bool `json:"disabled"`
	}
	if err := json.Unmarshal(storageJSON, &header); err != nil {
		return errors.New("Copilot auth file is invalid")
	}
	if header.Disabled != nil && *header.Disabled {
		return errors.New("Copilot auth file is disabled")
	}
	parsed, err := provider.New(nil).ParseAuth(pluginapi.AuthParseRequest{
		Provider: "copilot",
		RawJSON:  storageJSON,
	})
	if err != nil || !parsed.Handled || parsed.Auth.Provider != "copilot" || parsed.Auth.Disabled || len(parsed.Auth.StorageJSON) == 0 {
		return errors.New("Copilot auth file is not valid enabled Copilot auth data")
	}
	return nil
}

func discoverLiveProfile(t *testing.T, storageJSON []byte, source, authMode string) liveProfile {
	t.Helper()
	catalog, status, err := probeLiveModelCatalog(storageJSON, authMode)
	if err != nil {
		t.Fatalf("Copilot catalog discovery failed with status %d", status)
	}
	count := 0
	for model := range liveCopilotRoutes {
		if _, ok := catalog[model]; ok {
			count++
		}
	}
	t.Logf("credential profile accepted: source=%s auth_mode=%s catalog_rows=%d exact_target_models=%d", source, authMode, len(catalog), count)
	return liveProfile{Source: source, AuthMode: authMode, StorageJSON: storageJSON, Catalog: catalog}
}

func probeLiveModelCatalog(storageJSON []byte, authMode string) (map[string][]string, int, error) {
	callback := &liveCatalogHost{client: &http.Client{Timeout: 30 * time.Second}}
	service := provider.New(callback)
	if err := service.Configure([]byte("auth_mode: " + authMode + "\n")); err != nil {
		return nil, 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	if _, err := service.ModelsForAuth(ctx, "live-matrix", pluginapi.AuthModelRequest{AuthID: "live-matrix", StorageJSON: storageJSON}); err != nil {
		var statusErr *provider.StatusError
		if errors.As(err, &statusErr) {
			return nil, statusErr.HTTPStatus, err
		}
		return nil, 0, err
	}
	body := callback.modelCatalog()
	var catalog liveCatalogResponse
	if len(body) == 0 || json.Unmarshal(body, &catalog) != nil {
		return nil, 0, errors.New("model catalog response was unavailable")
	}
	models := make(map[string][]string, len(catalog.Data))
	for _, model := range catalog.Data {
		models[model.ID] = append([]string(nil), model.SupportedEndpoints...)
	}
	return models, http.StatusOK, nil
}

func (h *liveCatalogHost) Do(ctx context.Context, _ string, request transport.Request) (transport.Response, error) {
	upstreamRequest, err := http.NewRequestWithContext(ctx, request.Method, request.URL, bytes.NewReader(request.Body))
	if err != nil {
		return transport.Response{}, errors.New("could not construct Copilot catalog callback")
	}
	upstreamRequest.Header = request.Headers.Clone()
	response, err := h.client.Do(upstreamRequest)
	if err != nil {
		return transport.Response{}, errors.New("Copilot catalog callback failed")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return transport.Response{}, errors.New("Copilot catalog response could not be read")
	}
	if parsed, parseErr := url.Parse(request.URL); parseErr == nil && strings.HasSuffix(parsed.Path, "/models") && response.StatusCode >= 200 && response.StatusCode < 300 {
		h.mu.Lock()
		h.body = append([]byte(nil), body...)
		h.mu.Unlock()
	}
	return transport.Response{StatusCode: response.StatusCode, Headers: response.Header.Clone(), Body: body}, nil
}

func (*liveCatalogHost) OpenStream(context.Context, string, transport.Request) (transport.Stream, error) {
	return transport.Stream{}, errors.New("catalog discovery does not open streams")
}

func (*liveCatalogHost) ReadStream(context.Context, string) (transport.StreamChunk, error) {
	return transport.StreamChunk{}, errors.New("catalog discovery does not read streams")
}

func (*liveCatalogHost) CloseStream(context.Context, string) error {
	return nil
}

func (*liveCatalogHost) Emit(context.Context, string, []byte) error {
	return nil
}

func (*liveCatalogHost) CloseOutput(context.Context, string, string) {}

func (h *liveCatalogHost) modelCatalog() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]byte(nil), h.body...)
}

func liveEndpointOverrides(catalog map[string][]string) map[string]string {
	_ = catalog // Catalog values are logged separately; the matrix pins every assigned protocol route.
	overrides := make(map[string]string, len(liveCopilotRoutes))
	for model, endpoint := range liveCopilotRoutes {
		overrides[model] = endpoint
	}
	return overrides
}

func readCopilotCLIKeychainToken() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "security", "find-generic-password", "-s", "copilot-cli", "-w").Output()
	if err != nil {
		return "", errors.New("Copilot CLI Keychain entry is unavailable")
	}
	token := strings.TrimSpace(string(output))
	if token == "" {
		return "", errors.New("Copilot CLI Keychain entry is empty")
	}
	return token, nil
}

func liveGitHubIdentity(token string) (string, int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/copilot_internal/user", nil)
	if err != nil {
		return "", 0, errors.New("could not construct GitHub identity request")
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "token "+token)
	request.Header.Set("User-Agent", "copilot")
	request.Header.Set("X-GitHub-Api-Version", "2025-04-01")
	response, err := (&http.Client{Timeout: 18 * time.Second}).Do(request)
	if err != nil {
		return "", 0, errors.New("GitHub identity request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", 0, errors.New("GitHub identity request was rejected")
	}
	var identity struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&identity); err != nil || strings.TrimSpace(identity.Login) == "" || identity.ID <= 0 {
		return "", 0, errors.New("GitHub identity response was incomplete")
	}
	return identity.Login, identity.ID, nil
}

func liveCopilotStorage(token, login string, userID int64) ([]byte, error) {
	rootKey := make([]byte, sha256.Size)
	keyID := make([]byte, 16)
	if _, err := rand.Read(rootKey); err != nil {
		return nil, errors.New("could not create temporary continuity material")
	}
	if _, err := rand.Read(keyID); err != nil {
		return nil, errors.New("could not create temporary continuity material")
	}
	fingerprint := sha256.Sum256([]byte(token))
	storage := map[string]any{
		"type":                "copilot",
		"github_access_token": token,
		"github_login":        login,
		"github_user_id":      userID,
		"continuity_keyring": map[string]any{
			"version":                1,
			"key_id":                 hex.EncodeToString(keyID),
			"account_id":             userID,
			"root_key":               base64.RawURLEncoding.EncodeToString(rootKey),
			"credential_fingerprint": hex.EncodeToString(fingerprint[:]),
		},
	}
	body, err := json.Marshal(storage)
	if err != nil {
		return nil, errors.New("could not encode temporary auth state")
	}
	return body, nil
}

func TestSelectLiveAuthMode(t *testing.T) {
	tests := []struct {
		name             string
		authFile         bool
		override         string
		overrideProvided bool
		want             string
		wantErr          bool
	}{
		{name: "keychain default", want: "direct_oauth"},
		{name: "auth file default", authFile: true, want: "token_exchange"},
		{name: "explicit auth file mode", authFile: true, override: "direct_oauth", overrideProvided: true, want: "direct_oauth"},
		{name: "explicit keychain mode", override: "token_exchange", overrideProvided: true, want: "token_exchange"},
		{name: "unknown mode", override: "custom", overrideProvided: true, wantErr: true},
		{name: "empty explicit mode", overrideProvided: true, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := selectLiveAuthMode(test.authFile, test.override, test.overrideProvided)
			if (err != nil) != test.wantErr {
				t.Fatalf("selectLiveAuthMode() error = %t, want %t", err != nil, test.wantErr)
			}
			if got != test.want {
				t.Errorf("selectLiveAuthMode() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestLiveCopilotAuthFileValidation(t *testing.T) {
	const secret = "fixture-live-secret-token"
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{name: "valid auth storage", body: `{"type":"copilot","github_access_token":"` + secret + `","github_login":"fixture-user"}`},
		{name: "invalid JSON", body: `{"type":`, wantErr: "Copilot auth file is invalid"},
		{name: "disabled auth", body: `{"type":"copilot","github_access_token":"` + secret + `","disabled":true}`, wantErr: "Copilot auth file is disabled"},
		{name: "wrong provider", body: `{"type":"other","github_access_token":"` + secret + `"}`, wantErr: "Copilot auth file is not valid enabled Copilot auth data"},
		{name: "missing token", body: `{"type":"copilot","github_login":"fixture-user"}`, wantErr: "Copilot auth file is not valid enabled Copilot auth data"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			original := []byte(test.body)
			gotErr := validateLiveCopilotAuthStorage(original)
			if test.wantErr == "" {
				if gotErr != nil {
					t.Fatalf("validateLiveCopilotAuthStorage() error = %v", gotErr)
				}
			} else {
				if gotErr == nil || gotErr.Error() != test.wantErr {
					t.Fatalf("validateLiveCopilotAuthStorage() error = %v, want %q", gotErr, test.wantErr)
				}
				if strings.Contains(gotErr.Error(), secret) || strings.Contains(gotErr.Error(), "fixture-user") {
					t.Fatal("auth validation error exposed credential data")
				}
			}
			if !bytes.Equal(original, []byte(test.body)) {
				t.Fatal("auth validation changed the original storage bytes")
			}
		})
	}
}

func TestReadLiveCopilotAuthFilePreservesBytesAndHidesPath(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fixture-sensitive-name.json")
	want := []byte("{\n  \"github_access_token\": \"fixture-token\",\n  \"type\": \"copilot\"\n}\n")
	if err := os.WriteFile(path, want, 0600); err != nil {
		t.Fatal("could not write synthetic Copilot auth fixture")
	}
	t.Setenv("CPA_LIVE_COPILOT_AUTH_FILE", path)
	t.Setenv("CPA_LIVE_COPILOT_AUTH_MODE", "token_exchange")
	got, source, authMode, err := liveCredentialSourceFromEnvironment()
	if err != nil {
		t.Fatalf("liveCredentialSourceFromEnvironment() error = %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("auth file bytes changed while loading")
	}
	if source != "auth-file" || authMode != "token_exchange" {
		t.Fatalf("live credential selection = source %q, mode %q", source, authMode)
	}

	missingPath := filepath.Join(root, "fixture-private-path.json")
	if _, err := readLiveCopilotAuthFile(missingPath); err == nil || strings.Contains(err.Error(), missingPath) {
		t.Fatal("auth file read error was missing or exposed its path")
	}
}

func startLiveNativeHost(t *testing.T, binary string, storageJSON []byte, authMode string, endpointOverrides map[string]string) (string, func()) {
	return startLiveNativeHostWithGate(t, binary, storageJSON, authMode, endpointOverrides, "")
}

func startLiveNativeHostWithGate(t *testing.T, binary string, storageJSON []byte, authMode string, endpointOverrides map[string]string, gateURL string, readiness ...func([]byte)) (string, func()) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal("could not create live host root")
	}
	portListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("could not reserve live host port")
	}
	port := portListener.Addr().(*net.TCPAddr).Port
	if err := portListener.Close(); err != nil {
		t.Fatal("could not release live host port")
	}
	pluginRoot := strings.TrimSpace(os.Getenv("CPA_LIVE_EXISTING_PLUGIN_DIR"))
	if pluginRoot != "" {
		if err := validateLiveExistingPluginDirectory(pluginRoot); err != nil {
			t.Fatalf("existing live plugin directory is invalid: %v", err)
		}
		if selected := strings.TrimSpace(os.Getenv("CPA_LIVE_PLUGIN_PATH")); selected != "" && filepath.Clean(selected) != filepath.Join(pluginRoot, "cliproxyapi-copilot"+livePluginExtension()) {
			t.Fatal("declared live plugin path does not match the artifact loaded in place")
		}
	} else {
		pluginRoot = filepath.Join(root, "plugins")
		pluginDir := filepath.Join(pluginRoot, runtime.GOOS, runtime.GOARCH)
		if err := os.MkdirAll(pluginDir, 0700); err != nil {
			t.Fatal("could not create live plugin directory")
		}
		pluginPath := strings.TrimSpace(os.Getenv("CPA_LIVE_PLUGIN_PATH"))
		if pluginPath == "" {
			pluginPath = filepath.Join("..", "build", "plugins", runtime.GOOS, runtime.GOARCH, "cliproxyapi-copilot"+livePluginExtension())
		}
		plugin, err := os.ReadFile(pluginPath)
		if err != nil {
			t.Fatal("live native plugin artifact is unavailable")
		}
		if err := os.WriteFile(filepath.Join(pluginDir, "cliproxyapi-copilot"+livePluginExtension()), plugin, 0600); err != nil {
			t.Fatal("could not stage live native plugin")
		}
	}
	authDir := filepath.Join(root, "auths")
	if err := os.MkdirAll(authDir, 0700); err != nil {
		t.Fatal("could not create task-owned auth directory")
	}
	if err := os.WriteFile(filepath.Join(authDir, "live-matrix.json"), storageJSON, 0600); err != nil {
		t.Fatal("could not write task-owned auth state")
	}
	config := nativeTemplateConfig(t)
	debugDirectory := liveDebugArtifactDirectory(t, "native-host")
	if debugDirectory != "" {
		logs := nativeMap(t, nativeMap(t, config["observability"])["logs"])
		logs["debug"] = true
		logs["logging-to-file"] = true
		logs["request-log"] = true
		logs["logs-max-total-size-mb"] = 0
		logs["error-logs-max-files"] = 0
		if err := os.MkdirAll(filepath.Join(debugDirectory, "logs"), 0700); err != nil {
			t.Fatal("could not create retained private host logs")
		}
	}
	server := nativeMap(t, config["server"])
	server["host"] = "127.0.0.1"
	server["port"] = port
	management := nativeMap(t, config["management"])
	management["allow-remote"] = false
	management["disable-control-panel"] = true
	management["secret-key"] = "live-matrix-management-secret"
	access := nativeMap(t, config["access"])
	access["api-keys"] = []string{liveCopilotClientKey}
	oauth := nativeMap(t, config["oauth"])
	oauth["auth-dir"] = authDir
	plugins := nativeMap(t, config["plugins"])
	plugins["dir"] = pluginRoot
	pluginConfigs := nativeMap(t, plugins["configs"])
	pluginConfig := nativeMap(t, pluginConfigs["cliproxyapi-copilot"])
	pluginConfig["auth_mode"] = authMode
	pluginConfig["model_endpoint_overrides"] = endpointOverrides
	if gateURL != "" {
		pluginConfig["github_api_url"] = gateURL
		pluginConfig["copilot_api_url"] = gateURL
		pluginConfig["allow_insecure_base_urls"] = true
	}
	routing := nativeMap(t, config["routing"])
	retry := nativeMap(t, routing["retry"])
	retry["request-retry"] = 0
	configBody, err := yaml.Marshal(config)
	if err != nil {
		t.Fatal("could not encode live host config")
	}
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, configBody, 0600); err != nil {
		t.Fatal("could not write live host config")
	}
	logDirectory := root
	if debugDirectory != "" {
		logDirectory = debugDirectory
	}
	logFile, err := os.OpenFile(filepath.Join(logDirectory, "host.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatal("could not create private host log")
	}
	ctx, cancel := context.WithCancel(context.Background())
	commandPath, commandArgs := livePacketExecutable(binary, "--config", configPath, "--local-model")
	command := exec.CommandContext(ctx, commandPath, commandArgs...)
	command.Env = liveChildEnvironment(root)
	if debugDirectory != "" {
		command.Env = append(command.Env, "WRITABLE_PATH="+debugDirectory)
	}
	command.Dir = logDirectory
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		cancel()
		_ = logFile.Close()
		t.Fatal("could not start disposable native host")
	}
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			if err := command.Process.Signal(os.Interrupt); err != nil {
				cancel()
			}
			exited := make(chan error, 1)
			go func() { exited <- command.Wait() }()
			timer := time.NewTimer(5 * time.Second)
			select {
			case <-exited:
			case <-timer.C:
				cancel()
				<-exited
			}
			timer.Stop()
			cancel()
			_ = logFile.Close()
			if debugDirectory != "" {
				if err := scrubLiveHostLogs(debugDirectory); err != nil {
					t.Error("could not finalize sanitized private host logs")
				} else {
					t.Logf("retained sanitized host debug and request logs: %s", debugDirectory)
				}
			}
		})
	}
	t.Cleanup(stop)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	if os.Getenv("CPA_LIVE_CLIENT_ATTACHMENTS") == "1" {
		deadline := time.Now().Add(20 * time.Second)
		for {
			connection, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
			if dialErr == nil {
				_ = connection.Close()
				break
			}
			if command.Process.Signal(syscall.Signal(0)) != nil || time.Now().After(deadline) {
				t.Fatal("attachment packet host did not open its local port")
			}
			time.Sleep(100 * time.Millisecond)
		}
		pollContext, pollCancel := context.WithTimeout(context.Background(), 95*time.Second)
		defer pollCancel()
		observed := func(body []byte) error {
			if len(readiness) > 0 && readiness[0] != nil {
				readiness[0](append([]byte(nil), body...))
			}
			return nil
		}
		_, polls, pollErr := pollFreshLocalRegistry(pollContext, &http.Client{Timeout: 3 * time.Second}, base, 300, 100*time.Millisecond, func() bool { return command.Process.Signal(syscall.Signal(0)) == nil }, observed)
		if pollErr != nil {
			t.Fatalf("attachment packet local registry failed after %d polls: %v", polls, pollErr)
		}
		return base, stop
	}
	client := &http.Client{Timeout: 3 * time.Second}
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("disposable native host did not become ready; private host log was withheld")
		case <-ticker.C:
			request, err := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
			if err != nil {
				continue
			}
			request.Header.Set("Authorization", "Bearer "+liveCopilotClientKey)
			response, err := client.Do(request)
			if err != nil {
				continue
			}
			body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK && liveModelCatalogHasTargets(body) {
				return base, stop
			}
		}
	}
}

func liveModelCatalogHasTargets(body []byte) bool {
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &response) != nil {
		return false
	}
	registered := make(map[string]struct{}, len(response.Data))
	for _, model := range response.Data {
		registered[model.ID] = struct{}{}
	}
	for model := range liveCopilotRoutes {
		if _, ok := registered[model]; !ok {
			return false
		}
	}
	return true
}

func liveChildEnvironment(home string) []string {
	filtered := filteredChildEnvironment(os.Environ())
	out := make([]string, 0, len(filtered)+1)
	for _, entry := range filtered {
		if !strings.HasPrefix(strings.ToUpper(entry), "HOME=") {
			out = append(out, entry)
		}
	}
	return append(out, "HOME="+home)
}

func liveMatrixRequest(model, api string, stream bool) (string, map[string]any) {
	prompt := "Reply with exactly LIVE_MATRIX_OK."
	switch api {
	case "Chat":
		return "/v1/chat/completions", map[string]any{
			"model": model, "stream": stream, "messages": []any{map[string]any{"role": "user", "content": prompt}},
		}
	case "Responses":
		return "/v1/responses", map[string]any{
			"model": model, "stream": stream, "store": false, "input": prompt,
		}
	default:
		return "/v1/messages", map[string]any{
			"model": model, "stream": stream, "max_tokens": 512, "messages": []any{map[string]any{"role": "user", "content": prompt}},
		}
	}
}

func liveProxyCall(endpoint string, payload any) ([]byte, int, string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, "", errors.New("could not encode live request")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, "", errors.New("could not construct live request")
	}
	request.Header.Set("Authorization", "Bearer "+liveCopilotClientKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Session-Id", "live-copilot-matrix")
	response, err := (&http.Client{Timeout: 95 * time.Second}).Do(request)
	if err != nil {
		return nil, 0, "", errors.New("native host request failed")
	}
	defer response.Body.Close()
	result, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return result, response.StatusCode, response.Header.Get("Content-Type"), errors.New("native host response could not be read")
	}
	return result, response.StatusCode, response.Header.Get("Content-Type"), nil
}

func validateLiveResponse(api string, stream bool, contentType string, body []byte) (string, string, bool, bool) {
	if stream {
		if !strings.Contains(strings.ToLower(contentType), "text/event-stream") {
			return "", "", false, false
		}
		events, done, validStream := parseLiveSSE(body)
		text := ""
		model := ""
		terminal := false
		modelConsistent := true
		responseCompletionValid := false
		for _, event := range events {
			if currentModel := liveStreamModel(api, event); currentModel != "" {
				if model != "" && currentModel != model {
					modelConsistent = false
				}
				model = currentModel
			}
			switch api {
			case "Chat":
				text += liveChatDelta(event)
			case "Responses":
				text += liveResponsesDelta(event)
				if event["type"] == "response.completed" {
					terminal = true
					if response, ok := event["response"].(map[string]any); ok {
						completedModel, modelOK := response["model"].(string)
						if modelOK && completedModel != "" {
							if model != "" && completedModel != model {
								modelConsistent = false
							}
							model = completedModel
						}
						responseCompletionValid = response["object"] == "response" && response["status"] == "completed" && modelOK && completedModel != ""
						if text == "" {
							text = liveResponseText(response)
						}
					}
				}
			case "Claude Messages":
				text += liveClaudeDelta(event)
				if event["type"] == "message_stop" {
					terminal = true
				}
			}
		}
		if api == "Chat" {
			terminal = done
		}
		schemaOK := validStream && len(events) > 0 && modelConsistent
		switch api {
		case "Chat":
			schemaOK = schemaOK && terminal && liveChatStreamValid(events)
		case "Responses":
			schemaOK = schemaOK && responseCompletionValid && model != ""
		case "Claude Messages":
			schemaOK = schemaOK && liveClaudeStreamValid(events)
		}
		return text, model, schemaOK, terminal
	}
	var response map[string]any
	if json.Unmarshal(body, &response) != nil {
		return "", "", false, false
	}
	model, _ := response["model"].(string)
	switch api {
	case "Chat":
		choices, ok := response["choices"].([]any)
		if response["object"] != "chat.completion" || !ok || len(choices) == 0 {
			return "", model, false, false
		}
		choice, ok := choices[0].(map[string]any)
		if !ok || choice["finish_reason"] != "stop" {
			return "", model, false, false
		}
		message, ok := choice["message"].(map[string]any)
		if !ok || message["role"] != "assistant" {
			return "", model, false, false
		}
		text, ok := message["content"].(string)
		return text, model, ok, true
	case "Responses":
		if response["status"] != "completed" || response["object"] != "response" {
			return "", model, false, false
		}
		return liveResponseText(response), model, true, true
	default:
		content, contentOK := response["content"].([]any)
		if response["type"] != "message" || response["role"] != "assistant" || !contentOK || response["stop_reason"] != liveClaudeStopReason(content) {
			return "", model, false, false
		}
		return liveContentText(response["content"]), model, true, true
	}
}

func liveStreamModel(api string, event map[string]any) string {
	if model, ok := event["model"].(string); ok {
		return model
	}
	if api == "Responses" {
		if response, ok := event["response"].(map[string]any); ok {
			model, _ := response["model"].(string)
			return model
		}
	}
	if api == "Claude Messages" {
		if message, ok := event["message"].(map[string]any); ok {
			model, _ := message["model"].(string)
			return model
		}
	}
	return ""
}

func parseLiveSSE(body []byte) ([]map[string]any, bool, bool) {
	var events []map[string]any
	done := false
	valid := true
	terminalSeen := false
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if terminalSeen {
			valid = false
			continue
		}
		if data == "[DONE]" {
			done = true
			terminalSeen = true
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(data), &event) != nil {
			return events, done, false
		}
		events = append(events, event)
		switch event["type"] {
		case "response.completed", "response.failed", "response.incomplete", "message_stop":
			terminalSeen = true
		}
	}
	if scanner.Err() != nil {
		valid = false
	}
	return events, done, valid
}

func liveChatDelta(event map[string]any) string {
	choices, _ := event["choices"].([]any)
	if len(choices) == 0 {
		return ""
	}
	choice, _ := choices[0].(map[string]any)
	delta, _ := choice["delta"].(map[string]any)
	text, _ := delta["content"].(string)
	return text
}

func liveChatStreamValid(events []map[string]any) bool {
	if len(events) == 0 {
		return false
	}
	hasFinish := false
	for index, event := range events {
		if event["object"] != "chat.completion.chunk" {
			return false
		}
		model, modelOK := event["model"].(string)
		choices, choicesOK := event["choices"].([]any)
		if !modelOK || model == "" || !choicesOK {
			return false
		}
		if len(choices) == 0 {
			usage, ok := event["usage"].(map[string]any)
			if !ok || !hasFinish || index != len(events)-1 {
				return false
			}
			for _, field := range []string{"prompt_tokens", "completion_tokens", "total_tokens"} {
				tokens, ok := usage[field].(float64)
				if !ok || tokens < 0 || tokens != math.Trunc(tokens) {
					return false
				}
			}
			continue
		}
		for _, value := range choices {
			choice, ok := value.(map[string]any)
			if !ok {
				return false
			}
			if _, ok := choice["delta"].(map[string]any); !ok {
				return false
			}
			if finish, exists := choice["finish_reason"]; exists && finish != nil {
				if reason, ok := finish.(string); !ok || reason != "stop" {
					return false
				}
				hasFinish = true
			}
		}
	}
	return hasFinish
}

func liveClaudeStreamValid(events []map[string]any) bool {
	starts, stops := 0, 0
	stopReasons := 0
	stopReason := "end_turn"
	messageModel := ""
	for _, event := range events {
		if event["type"] == "content_block_start" {
			block, _ := event["content_block"].(map[string]any)
			if block["type"] == "tool_use" {
				stopReason = "tool_use"
			}
		}
	}
	for index, event := range events {
		switch event["type"] {
		case "ping":
		case "message_start":
			starts++
			message, ok := event["message"].(map[string]any)
			if !ok || message["type"] != "message" || message["role"] != "assistant" {
				return false
			}
			messageModel, _ = message["model"].(string)
			if messageModel == "" {
				return false
			}
			if _, ok := message["content"].([]any); !ok {
				return false
			}
		case "content_block_start":
			block, ok := event["content_block"].(map[string]any)
			if !ok || !liveClaudeBlockStartValid(block) {
				return false
			}
		case "content_block_delta":
			delta, ok := event["delta"].(map[string]any)
			if !ok || !liveClaudeDeltaValid(delta) {
				return false
			}
		case "content_block_stop":
			if _, ok := event["index"].(float64); !ok {
				return false
			}
		case "message_delta":
			if stops != 0 || stopReasons != 0 {
				return false
			}
			delta, ok := event["delta"].(map[string]any)
			if !ok || delta["stop_reason"] != stopReason {
				return false
			}
			stopReasons++
		case "message_stop":
			stops++
			if index != len(events)-1 || stopReasons != 1 {
				return false
			}
		default:
			return false
		}
	}
	return starts == 1 && stops == 1 && stopReasons == 1 && messageModel != ""
}

func liveClaudeStopReason(content []any) string {
	for _, item := range content {
		block, _ := item.(map[string]any)
		if block["type"] == "tool_use" {
			return "tool_use"
		}
	}
	return "end_turn"
}

func liveClaudeBlockStartValid(block map[string]any) bool {
	switch block["type"] {
	case "text":
		_, ok := block["text"].(string)
		return ok
	case "thinking":
		_, ok := block["thinking"].(string)
		return ok
	case "redacted_thinking":
		_, ok := block["data"].(string)
		return ok
	case "tool_use":
		id, idOK := block["id"].(string)
		name, nameOK := block["name"].(string)
		_, inputOK := block["input"].(map[string]any)
		return idOK && id != "" && nameOK && name != "" && inputOK
	default:
		return false
	}
}

func liveClaudeDeltaValid(delta map[string]any) bool {
	switch delta["type"] {
	case "text_delta":
		_, ok := delta["text"].(string)
		return ok
	case "thinking_delta":
		_, ok := delta["thinking"].(string)
		return ok
	case "signature_delta":
		_, ok := delta["signature"].(string)
		return ok
	case "input_json_delta":
		_, ok := delta["partial_json"].(string)
		return ok
	default:
		return false
	}
}

func liveResponsesDelta(event map[string]any) string {
	if event["type"] != "response.output_text.delta" {
		return ""
	}
	text, _ := event["delta"].(string)
	return text
}

func liveClaudeDelta(event map[string]any) string {
	if event["type"] != "content_block_delta" {
		return ""
	}
	delta, _ := event["delta"].(map[string]any)
	text, _ := delta["text"].(string)
	return text
}

func liveResponseText(response map[string]any) string {
	return liveContentText(response["output"])
}

func liveContentText(value any) string {
	var text strings.Builder
	switch content := value.(type) {
	case []any:
		for _, item := range content {
			if object, ok := item.(map[string]any); ok {
				if itemType, _ := object["type"].(string); itemType == "output_text" || itemType == "text" {
					if value, ok := object["text"].(string); ok {
						text.WriteString(value)
					}
				}
				if nested, ok := object["content"]; ok {
					text.WriteString(liveContentText(nested))
				}
			}
		}
	case string:
		text.WriteString(content)
	}
	return text.String()
}

func safeLiveErrorCode(body []byte) string {
	code := safeLiveErrorField(body, "code")
	if code != "" {
		return code
	}
	return safeLiveErrorField(body, "type")
}

func safeLiveErrorType(body []byte) string {
	return safeLiveErrorField(body, "type")
}

func safeLiveErrorField(body []byte, field string) string {
	var response map[string]any
	if json.Unmarshal(body, &response) != nil {
		return ""
	}
	errorObject, _ := response["error"].(map[string]any)
	if value, ok := errorObject[field].(string); ok {
		var safe strings.Builder
		for _, character := range value {
			if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' || character == '.' {
				safe.WriteRune(character)
			}
		}
		return safe.String()
	}
	return ""
}

func safeLiveErrorClass(body []byte, status int) string {
	var response struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &response) == nil && response.Error.Message == fmt.Sprintf("Copilot upstream returned HTTP %d", status) {
		return fmt.Sprintf("copilot_upstream_http_%d", status)
	}
	if errorType := safeLiveErrorType(body); errorType != "" {
		return "public_" + errorType
	}
	return "unknown"
}

func TestLiveChatStreamUsageValidation(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "internal", "translate", "testdata", "live-matrix-gpt-chat-stream", "client-response.sse"))
	if err != nil {
		t.Fatal(err)
	}
	frames := strings.Split(strings.TrimSpace(string(body)), "\n\n")
	if len(frames) < 4 {
		t.Fatal("captured Chat usage stream has too few frames")
	}
	content := strings.Join(frames[:len(frames)-3], "\n\n") + "\n\n"
	finished := frames[len(frames)-3] + "\n\n"
	usage := frames[len(frames)-2] + "\n\n"
	done := frames[len(frames)-1] + "\n\n"
	mutateUsage := func(change func(map[string]any)) string {
		var event map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(usage, "data:"))), &event) != nil {
			t.Fatal("captured usage chunk is invalid JSON")
		}
		change(event)
		body, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		return "data: " + string(body) + "\n\n"
	}
	tests := []struct {
		name  string
		body  string
		valid bool
	}{
		{name: "terminal usage-only chunk", body: content + finished + usage + done, valid: true},
		{name: "empty choices without usage", body: content + finished + mutateUsage(func(event map[string]any) { delete(event, "usage") }) + done},
		{name: "missing token field", body: content + finished + mutateUsage(func(event map[string]any) { delete(event["usage"].(map[string]any), "total_tokens") }) + done},
		{name: "negative token count", body: content + finished + mutateUsage(func(event map[string]any) { event["usage"].(map[string]any)["completion_tokens"] = -7 }) + done},
		{name: "fractional token count", body: content + finished + mutateUsage(func(event map[string]any) { event["usage"].(map[string]any)["completion_tokens"] = 0.7 }) + done},
		{name: "string token count", body: content + finished + mutateUsage(func(event map[string]any) { event["usage"].(map[string]any)["completion_tokens"] = "7" }) + done},
		{name: "usage before completion", body: content + usage + finished + done},
		{name: "usage without finish reason", body: content + usage + done},
		{name: "usage without assistant chunks", body: usage + done},
		{name: "usage without done marker", body: content + finished + usage},
		{name: "usage changes reported model", body: content + finished + strings.ReplaceAll(usage, "gpt-6-luna", "other-model") + done},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, valid, _ := validateLiveResponse("Chat", true, "text/event-stream", []byte(test.body))
			if valid != test.valid {
				t.Fatalf("stream schema valid=%t, want %t", valid, test.valid)
			}
		})
	}
}

func TestLiveHostLogRetentionScrubsCredentialsAndPreservesBodies(t *testing.T) {
	const request = `{"model":"claude-haiku-5.5","stream":false,"messages":[{"role":"user","content":"LIVE_MATRIX_OK"}]}`
	const response = `{"error":{"type":"invalid_request_error","message":"fixture failure"},"model":"claude-haiku-5-5"}`
	original := "=== HEADERS ===\nAuthorization: Bearer fixture-client-secret\nX-Api-Key: fixture-api-secret\nContent-Type: application/json\n\n=== REQUEST BODY ===\n" + request + "\n\n" +
		"=== API REQUEST 1 ===\nUpstream URL: https://api.github.com/copilot_internal/v2/token\nAuth: provider=copilot account=fixture-account\nHeaders:\nAuthorization: token fixture-oauth-secret\n\nBody:\n{\"login\":\"fixture-identity\"}\n\n" +
		"=== API REQUEST 2 ===\nUpstream URL: https://api.githubcopilot.com/v1/messages\nHeaders:\nAuthorization: Bearer fixture-model-secret\n\nBody:\n" + request + "\n\n" +
		"=== API RESPONSE 1 ===\nStatus: 200\nHeaders:\nSet-Cookie: fixture-cookie-secret\nBody:\n{\"token\":\"fixture-token-secret\",\"login\":\"fixture-identity\"}\n\n" +
		"=== API RESPONSE 2 ===\nStatus: 400\nHeaders:\nContent-Type: application/json\nBody:\n" + response + "\n\n=== RESPONSE ===\nStatus: 400\n" + response + "\n"
	root := t.TempDir()
	path := filepath.Join(root, "failure.log")
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if err := scrubLiveHostLogs(root); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"fixture-client-secret", "fixture-api-secret", "fixture-account", "fixture-oauth-secret", "fixture-model-secret", "fixture-cookie-secret", "fixture-token-secret", "fixture-identity", "Authorization:", "X-Api-Key:", "Set-Cookie:"} {
		if bytes.Contains(body, []byte(secret)) {
			t.Fatalf("retained log includes synthetic credential or identity %q", secret)
		}
	}
	if bytes.Count(body, []byte(request)) != 2 || bytes.Count(body, []byte(response)) != 2 || !bytes.Contains(body, []byte("Status: 400")) {
		t.Fatal("retained log changed original request/response shapes or lost failure status")
	}
	file, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if file.Mode().Perm() != 0600 {
		t.Fatalf("retained log permissions=%o", file.Mode().Perm())
	}
}

func TestLiveHostLogRetentionScrubsUnpairedAuthenticationSpools(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "client model request plus unpaired auth response", body: "=== REQUEST BODY ===\n{\"model\":\"gpt-6-luna\",\"input\":\"LIVE_MATRIX_OK\"}\n=== API RESPONSE 1 ===\nStatus: 200\nBody:\n{\"token\":\"fixture-token-secret\",\"login\":\"fixture-identity\"}\n"},
		{name: "complete response without request URL", body: "=== API RESPONSE 1 ===\nStatus: 200\nBody:\n{\"token\":\"fixture-token-secret\",\"login\":\"fixture-identity\"}\n"},
		{name: "unterminated response token prefix", body: "=== API RESPONSE 1 ===\nStatus: 200\nBody:\n{\"token\":\"fixture-token-secret"},
		{name: "multiline response token prefix", body: "=== API RESPONSE 1 ===\nStatus: 200\nBody:\n{\n \"expires_at\": 0,\n \"token\": \"fixture-token-secret"},
		{name: "raw auth body spool", body: `{"access_token":"fixture-token-secret","login":"fixture-identity"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := sanitizeLiveHostLog([]byte(test.body))
			if bytes.Contains(body, []byte("fixture-token-secret")) || bytes.Contains(body, []byte("fixture-identity")) {
				t.Fatal("unpaired auth spool retained a synthetic token or identity")
			}
		})
	}
	const failedModel = "=== API RESPONSE 1 ===\nStatus: 400\nBody:\n{\"model\":\"gpt-6-luna\",\"error\":{\"message\":\"fixture failure\"},\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"{\\\"token\\\":\\\"example\\\"}\"}]}]}"
	if got := string(sanitizeLiveHostLog([]byte(failedModel))); got != failedModel {
		t.Fatal("unpaired failed model response body changed")
	}
	const partialModel = "=== API RESPONSE 1 ===\nStatus: 200\nBody:\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"LIVE_MATRIX_"
	if got := string(sanitizeLiveHostLog([]byte(partialModel))); got != partialModel {
		t.Fatal("partial model response body changed")
	}
}

func TestLiveProxyCallRetainsInterruptedResponseBody(t *testing.T) {
	const partial = "data: {\"object\":\"chat.completion.chunk\",\"model\":\"gpt-6-luna\",\"choices\":["
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set("Content-Length", "9999")
		if _, err := writer.Write([]byte(partial)); err != nil {
			t.Error("fixture response could not be written")
		}
	}))
	defer server.Close()
	body, status, contentType, err := liveProxyCall(server.URL, map[string]any{"model": "gpt-6-luna", "input": "LIVE_MATRIX_OK"})
	if err == nil || string(body) != partial || status != http.StatusOK || contentType != "text/event-stream" {
		t.Fatal("interrupted live request lost its partial body or response metadata")
	}
}

func TestLiveMatrixObservedNativeCanonicalIdentity(t *testing.T) {
	root := filepath.Join("..", "internal", "translate", "testdata", "live-matrix-claude-claude")
	request, err := os.ReadFile(filepath.Join(root, "upstream-request.json"))
	if err != nil {
		t.Fatal(err)
	}
	response, err := os.ReadFile(filepath.Join(root, "upstream-response.json"))
	if err != nil {
		t.Fatal(err)
	}
	var nativeRequest, nativeResponse map[string]any
	if json.Unmarshal(request, &nativeRequest) != nil || json.Unmarshal(response, &nativeResponse) != nil {
		t.Fatal("captured native identity fixture is invalid")
	}
	requested, _ := nativeRequest["model"].(string)
	returned, _ := nativeResponse["model"].(string)
	if requested != "claude-haiku-5.5" || returned != "claude-haiku-5-5" || liveCopilotRoutes[requested] != "/v1/messages" {
		t.Fatal("captured canonical identity does not match the exact requested native route")
	}
	tests := []struct {
		requested string
		returned  string
		matches   bool
	}{
		{requested: requested, returned: returned, matches: true},
		{requested: requested, returned: requested, matches: true},
		{requested: requested, returned: "claude-haiku-5", matches: false},
		{requested: "claude-opus-5.5", returned: "claude-opus-5-5", matches: false},
		{requested: "claude-haiku-5-5", returned: requested, matches: false},
		{requested: "gpt-6-luna", returned: "gpt-6-luna", matches: true},
		{requested: "gpt-6-luna", returned: requested, matches: false},
	}
	for _, test := range tests {
		if got := liveMatrixResponseModelMatches(test.requested, test.returned); got != test.matches {
			t.Fatalf("requested=%q returned=%q matches=%t, want %t", test.requested, test.returned, got, test.matches)
		}
	}
}

func TestLiveMatrixClaudeBudgetMatchesCapturedRequest(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "internal", "translate", "testdata", "live-matrix-gemini-claude", "client-request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var captured map[string]any
	if json.Unmarshal(body, &captured) != nil {
		t.Fatal("captured Claude budget request is invalid")
	}
	_, payload := liveMatrixRequest("gemini-3.8-flash", "Claude Messages", false)
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var generated map[string]any
	if json.Unmarshal(encoded, &generated) != nil {
		t.Fatal("generated Claude budget request is invalid")
	}
	capturedJSON, err := json.Marshal(captured)
	if err != nil {
		t.Fatal(err)
	}
	generatedJSON, err := json.Marshal(generated)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(capturedJSON, generatedJSON) || generated["max_tokens"] != float64(512) {
		t.Fatal("matrix request differs from the successful captured 512-token request")
	}
}

func TestLiveMatrixCapturedNineProtocolPairs(t *testing.T) {
	for _, native := range []struct{ name, model, api string }{
		{"gemini", "gemini-3.8-flash", "Chat"},
		{"gpt", "gpt-6-luna", "Responses"},
		{"claude", "claude-haiku-5.5", "Claude Messages"},
	} {
		for _, client := range []struct{ name, api string }{
			{"chat", "Chat"}, {"responses", "Responses"}, {"claude", "Claude Messages"},
		} {
			t.Run(native.name+"/"+client.name, func(t *testing.T) {
				root := filepath.Join("..", "internal", "translate", "testdata", "live-matrix-"+native.name+"-"+client.name)
				bodies := make(map[string][]byte)
				for _, name := range []string{"client-request", "upstream-request", "upstream-response", "client-response"} {
					body, err := os.ReadFile(filepath.Join(root, name+".json"))
					if err != nil || !json.Valid(body) {
						t.Fatalf("captured %s body is unavailable or invalid", name)
					}
					bodies[name] = body
				}
				_, payload := liveMatrixRequest(native.model, client.api, false)
				generated, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				var generatedRequest, capturedRequest map[string]any
				if json.Unmarshal(generated, &generatedRequest) != nil || json.Unmarshal(bodies["client-request"], &capturedRequest) != nil {
					t.Fatal("matrix request is invalid")
				}
				generated, _ = json.Marshal(generatedRequest)
				captured, _ := json.Marshal(capturedRequest)
				if !bytes.Equal(generated, captured) {
					t.Fatal("matrix request differs from its captured successful original")
				}
				var upstreamRequest map[string]any
				if json.Unmarshal(bodies["upstream-request"], &upstreamRequest) != nil || upstreamRequest["model"] != native.model || upstreamRequest["stream"] != false {
					t.Fatal("upstream request substituted the requested model or mode")
				}
				for _, response := range []struct{ name, api string }{{"client-response", client.api}, {"upstream-response", native.api}} {
					body := bodies[response.name]
					// The captured native Gemini reply predates the object-field repair.
					if response.name == "upstream-response" && native.name == "gemini" {
						var reply map[string]any
						if json.Unmarshal(body, &reply) != nil {
							t.Fatal("native Gemini capture is invalid")
						}
						if _, exists := reply["object"]; exists {
							t.Fatal("original missing-object provider evidence changed")
						}
						reply["object"] = "chat.completion"
						body, err = json.Marshal(reply)
						if err != nil {
							t.Fatal(err)
						}
					}
					text, model, valid, terminal := validateLiveResponse(response.api, false, "application/json", body)
					if !valid || !terminal || !strings.Contains(text, "LIVE_MATRIX_OK") || !liveMatrixResponseModelMatches(native.model, model) {
						t.Fatalf("captured %s failed schema, terminal, prompt, or identity validation", response.name)
					}
				}
			})
		}
	}
}
