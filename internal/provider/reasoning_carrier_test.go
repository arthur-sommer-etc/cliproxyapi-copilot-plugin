package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/compact"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

func TestReasoningCarrierJSONRoundTripsForClaudeAndResponses(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		format     string
		response   string
		request    string
		responseAt string
		requestAt  string
	}{
		{
			name:       "Claude thinking signature",
			format:     "claude",
			response:   `{"id":"msg_1","content":[{"type":"thinking","thinking":"preserve this","signature":"INNER"},{"type":"text","text":"cpa-copilot-reasoning:v1:ordinary text"}],"usage":{"input_tokens":9007199254740993}}`,
			request:    `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"preserve this","signature":"WRAPPED"}]},{"role":"user","content":"continue"}]}`,
			responseAt: "content.0.signature",
			requestAt:  "messages.0.content.0.signature",
		},
		{
			name:       "Responses encrypted content",
			format:     "openai-response",
			response:   `{"id":"resp_1","output":[{"type":"reasoning","summary":[],"encrypted_content":"INNER"},{"type":"function_call","arguments":"{\"opaque\":\"cpa-copilot-reasoning:v1:ordinary tool data\",\"n\":9007199254740993}"}],"usage":{"input_tokens":9007199254740993}}`,
			request:    `{"input":[{"type":"reasoning","summary":[],"encrypted_content":"WRAPPED"},{"type":"function_call","arguments":"{\"opaque\":\"unchanged\"}"}]}`,
			responseAt: "output.0.encrypted_content",
			requestAt:  "input.0.encrypted_content",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			scope := testReasoningCarrierScope()
			response := []byte(strings.ReplaceAll(test.response, "INNER", testInnerReasoningCarrier()))
			sealed, err := sealResponseReasoningCarriers(test.format, response, scope)
			if err != nil {
				t.Fatalf("seal translated response: %v", err)
			}
			sealedValue := gjson.GetBytes(sealed, test.responseAt).String()
			if !strings.HasPrefix(sealedValue, sealedReasoningCarrierV2Prefix) {
				t.Fatalf("response carrier was not sealed: %s", sealed)
			}
			inner, err := unsealReasoningCarrier(sealedValue, scope)
			if err != nil || inner != testInnerReasoningCarrier() {
				t.Fatalf("response inner carrier = %q, err=%v", inner, err)
			}
			request := []byte(strings.ReplaceAll(test.request, "WRAPPED", sealedValue))
			unwrapped, err := unwrapRequestReasoningCarriers(test.format, request, scope)
			if err != nil {
				t.Fatalf("unwrap request carrier: %v", err)
			}
			if got := gjson.GetBytes(unwrapped, test.requestAt).String(); got != testInnerReasoningCarrier() {
				t.Fatalf("request inner carrier = %q, want exact original", got)
			}
			if got := gjson.GetBytes(sealed, "usage.input_tokens").Raw; got != "9007199254740993" {
				t.Fatalf("unrelated response integer changed: %q", got)
			}
			if test.format == "claude" {
				if got := gjson.GetBytes(unwrapped, "messages.1.content").String(); got != "continue" || gjson.GetBytes(unwrapped, "messages.0.content.0.thinking").String() != "preserve this" {
					t.Fatalf("unrelated Claude content changed: %s", unwrapped)
				}
				if got := gjson.GetBytes(sealed, "content.1.text").String(); got != "cpa-copilot-reasoning:v1:ordinary text" {
					t.Fatalf("ordinary text was rewritten: %s", sealed)
				}
			} else if got := gjson.GetBytes(unwrapped, "input.1.arguments").String(); got != `{"opaque":"unchanged"}` {
				t.Fatalf("tool arguments changed: %s", unwrapped)
			} else if got := gjson.GetBytes(sealed, "output.1.arguments").String(); !strings.Contains(got, "cpa-copilot-reasoning:v1:ordinary tool data") {
				t.Fatalf("ordinary tool data was rewritten: %s", sealed)
			}
		})
	}
}

func TestReasoningCarrierStreamRoundTripsForClaudeAndResponses(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		dest       string
		frame      string
		wantPrefix string
		wantSuffix string
		request    string
		requestAt  string
	}{
		{
			name:       "Claude signature delta keeps CRLF framing",
			dest:       "claude",
			frame:      "event: content_block_delta\r\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"INNER\"}}\r\n\r\n",
			wantPrefix: "event: content_block_delta\r\ndata: ",
			wantSuffix: "\r\n\r\n",
			request:    `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"WRAPPED"}]},{"role":"user","content":"next"}]}`,
			requestAt:  "messages.0.content.0.signature",
		},
		{
			name:       "Responses item keeps event framing",
			dest:       "openai-response",
			frame:      "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"reasoning\",\"summary\":[],\"encrypted_content\":\"INNER\"}}\n\n",
			wantPrefix: "event: response.output_item.done\ndata: ",
			wantSuffix: "\n\n",
			request:    `{"input":[{"type":"reasoning","summary":[],"encrypted_content":"WRAPPED"},{"type":"message","role":"user","content":"next"}]}`,
			requestAt:  "input.0.encrypted_content",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			scope := testReasoningCarrierScope()
			frame := []byte(strings.ReplaceAll(test.frame, "INNER", testInnerReasoningCarrier()))
			sealedFrame, err := sealReasoningCarrierSSEFrame(test.dest, frame, scope)
			if err != nil {
				t.Fatalf("seal translated SSE frame: %v", err)
			}
			if !strings.HasPrefix(string(sealedFrame), test.wantPrefix) || !strings.HasSuffix(string(sealedFrame), test.wantSuffix) {
				t.Fatalf("SSE framing changed: %q", sealedFrame)
			}
			dataJSON := extractReasoningCarrierSSEData(t, sealedFrame)
			data := gjson.Get(dataJSON, "delta.signature")
			if test.dest == "openai-response" {
				data = gjson.Get(dataJSON, "item.encrypted_content")
			}
			if !strings.HasPrefix(data.String(), sealedReasoningCarrierV2Prefix) {
				t.Fatalf("stream carrier was not sealed: %q", sealedFrame)
			}
			request := []byte(strings.ReplaceAll(test.request, "WRAPPED", data.String()))
			unwrapped, err := unwrapRequestReasoningCarriers(test.dest, request, scope)
			if err != nil {
				t.Fatalf("unwrap streamed carrier: %v", err)
			}
			if got := gjson.GetBytes(unwrapped, test.requestAt).String(); got != testInnerReasoningCarrier() {
				t.Fatalf("stream replay carrier = %q, want exact inner value", got)
			}
		})
	}
}

func TestReasoningCarrierRejectsScopeChangesTamperingAndUnsealedValues(t *testing.T) {
	t.Parallel()
	scope := testReasoningCarrierScope()
	sealed, err := sealReasoningCarrier(testInnerReasoningCarrier(), scope)
	if err != nil {
		t.Fatalf("seal carrier: %v", err)
	}
	for _, test := range []struct {
		name   string
		change func(*reasoningCarrierScope)
	}{
		{name: "auth id", change: func(scope *reasoningCarrierScope) { scope.AuthID = "auth-b" }},
		{name: "account key", change: func(scope *reasoningCarrierScope) {
			scope.Keys = testCarrierKeysWithRoot(scope.AuthID, scope.Model, scope.Endpoint, scope.APIBaseURL, bytes.Repeat([]byte{0x55}, 32))
		}},
		{name: "API origin", change: func(scope *reasoningCarrierScope) { scope.APIBaseURL = "https://other.example" }},
		{name: "model", change: func(scope *reasoningCarrierScope) { scope.Model = "other-model" }},
		{name: "endpoint", change: func(scope *reasoningCarrierScope) { scope.Endpoint = translate.EndpointResponses }},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			wrongScope := scope
			test.change(&wrongScope)
			if test.name != "account key" {
				wrongScope.Keys = testCarrierKeys(wrongScope.AuthID, wrongScope.Model, wrongScope.Endpoint, wrongScope.APIBaseURL)
			}
			if _, err := unsealReasoningCarrier(sealed, wrongScope); err == nil {
				t.Fatal("carrier accepted under a different scope")
			}
		})
	}
	tampered := tamperReasoningCarrierMAC(t, sealed)
	if _, err := unsealReasoningCarrier(tampered, scope); err == nil {
		t.Fatal("tampered carrier was accepted")
	}
	request := []byte(`{"input":[{"type":"reasoning","encrypted_content":"` + testInnerReasoningCarrier() + `"}]}`)
	if _, err := unwrapRequestReasoningCarriers("openai-response", request, scope); err == nil {
		t.Fatal("unsealed caller carrier was accepted")
	}
}

func TestReasoningCarrierSurvivesSameCredentialRenewalAndReload(t *testing.T) {
	t.Parallel()
	keyring, err := newContinuityKeyring(4242, "credential-a")
	if err != nil {
		t.Fatalf("make continuity keyring: %v", err)
	}
	storage := authStorage{GitHubAccessToken: "credential-a", GitHubUserID: 4242, ContinuityKeyring: keyring}
	issuedToken := copilotTokenEntry{Token: "ephemeral-before-renewal", APIBaseURL: "https://api.example", ConfigGeneration: 3}
	issuedKeys, err := continuityKeyMaterialsFor(storage, "auth-a", "test-chat", translate.EndpointChatCompletions, issuedToken.APIBaseURL)
	if err != nil {
		t.Fatalf("make issued carrier keys: %v", err)
	}
	issuedScope := reasoningCarrierScopeFor("auth-a", "test-chat", translate.EndpointChatCompletions, issuedToken.APIBaseURL, issuedKeys)
	sealed, err := sealReasoningCarrier(testInnerReasoningCarrier(), issuedScope)
	if err != nil {
		t.Fatalf("seal carrier: %v", err)
	}
	reloadedToken := copilotTokenEntry{Token: "ephemeral-after-renewal", APIBaseURL: "https://api.example", ConfigGeneration: 81}
	reloadedKeys, err := continuityKeyMaterialsFor(storage, "auth-a", "test-chat", translate.EndpointChatCompletions, reloadedToken.APIBaseURL)
	if err != nil {
		t.Fatalf("make reloaded carrier keys: %v", err)
	}
	reloadedScope := reasoningCarrierScopeFor("auth-a", "test-chat", translate.EndpointChatCompletions, reloadedToken.APIBaseURL, reloadedKeys)
	inner, err := unsealReasoningCarrier(sealed, reloadedScope)
	if err != nil || inner != testInnerReasoningCarrier() {
		t.Fatalf("carrier did not survive same-auth reload/token renewal: inner=%q err=%v", inner, err)
	}
}

func TestExecuteRejectsCrossAuthReasoningBeforeModelPost(t *testing.T) {
	t.Parallel()
	host := &reasoningCarrierHost{}
	service, storage := newReasoningCarrierService(host, "auth-b", "credential-b")
	issuedScope := testReasoningCarrierScope()
	sealed, err := sealReasoningCarrier(testInnerReasoningCarrier(), issuedScope)
	if err != nil {
		t.Fatalf("seal cross-auth fixture: %v", err)
	}
	payload := []byte(`{"model":"test-chat","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"prior","signature":"` + sealed + `"}]},{"role":"user","content":"continue"}]}`)
	_, err = service.Execute(context.Background(), ExecuteRequest{ExecutorRequest: pluginapi.ExecutorRequest{
		AuthID:          "auth-b",
		SourceFormat:    "claude",
		Model:           "test-chat",
		OriginalRequest: payload,
		Payload:         payload,
		StorageJSON:     mustMarshalReasoningStorage(t, storage),
	}})
	if err == nil {
		t.Fatal("cross-auth carrier was accepted")
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	for _, request := range host.requests {
		if request.Method == http.MethodPost {
			t.Fatalf("mismatched carrier reached model POST: %s", request.URL)
		}
	}
}

func TestRequestCarrierLeavesNativeSignaturesAndOrdinaryTextUntouched(t *testing.T) {
	t.Parallel()
	scope := testReasoningCarrierScope()
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"native","signature":"native-provider-signature"},{"type":"text","text":"` + testInnerReasoningCarrier() + `"}]}]}`)
	got, err := unwrapRequestReasoningCarriers("claude", payload, scope)
	if err != nil {
		t.Fatalf("normalize native signature: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("native fields changed: %s", got)
	}
}

func TestChatReasoningPreflightRejectsForeignOpaqueHistory(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		format  string
		payload string
		wantErr string
	}{
		{
			name:    "Claude signature",
			format:  "claude",
			payload: `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"prior","signature":"foreign-signature-sentinel"}]}]}`,
			wantErr: "foreign Claude thinking signatures cannot be replayed to Copilot Chat",
		},
		{
			name:    "Claude redacted thinking",
			format:  "claude",
			payload: `{"messages":[{"role":"assistant","content":[{"type":"redacted_thinking","data":"foreign-redacted-sentinel"}]}]}`,
			wantErr: "Claude redacted thinking cannot be replayed to Copilot Chat",
		},
		{
			name:    "Responses encrypted content",
			format:  "openai-response",
			payload: `{"input":[{"type":"reasoning","encrypted_content":"foreign-encrypted-sentinel"}]}`,
			wantErr: "foreign Responses reasoning cannot be replayed to Copilot Chat",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			scope := testReasoningCarrierScope()
			unwrapped, err := unwrapRequestReasoningCarriers(test.format, []byte(test.payload), scope)
			if err != nil {
				t.Fatalf("unwrap foreign opaque history: %v", err)
			}
			err = validateReasoningRequestForEndpoint(test.format, unwrapped, translate.EndpointChatCompletions)
			if err == nil || err.Error() != test.wantErr {
				t.Fatalf("Chat preflight error = %v, want %q", err, test.wantErr)
			}
			if strings.Contains(err.Error(), "sentinel") {
				t.Fatalf("Chat preflight exposed request content: %v", err)
			}
		})
	}
}

func TestChatReasoningPreflightAcceptsAuthenticatedCarriersOnly(t *testing.T) {
	t.Parallel()
	scope := testReasoningCarrierScope()
	sealed := sealCarrierForTest(t, scope)
	for _, test := range []struct {
		name    string
		format  string
		payload string
	}{
		{
			name:    "Claude signature",
			format:  "claude",
			payload: `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"prior","signature":"SEALED"}]}]}`,
		},
		{
			name:    "Responses encrypted content",
			format:  "openai-response",
			payload: `{"input":[{"type":"reasoning","encrypted_content":"SEALED"}]}`,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			payload := []byte(strings.ReplaceAll(test.payload, "SEALED", sealed))
			unwrapped, err := unwrapRequestReasoningCarriers(test.format, payload, scope)
			if err != nil {
				t.Fatalf("unwrap authenticated carrier: %v", err)
			}
			if err := validateReasoningRequestForEndpoint(test.format, unwrapped, translate.EndpointChatCompletions); err != nil {
				t.Fatalf("Chat preflight rejected authenticated carrier: %v", err)
			}
		})
	}
}

func TestChatReasoningPreflightIgnoresOrdinaryTextAndToolData(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		format  string
		payload string
	}{
		{
			name:    "Claude text and tool input",
			format:  "claude",
			payload: `{"messages":[{"role":"user","content":[{"type":"text","text":"` + testInnerReasoningCarrier() + `"},{"type":"tool_use","id":"call_1","name":"tool","input":{"value":"` + testInnerReasoningCarrier() + `"}}]}]}`,
		},
		{
			name:    "Responses message and function arguments",
			format:  "openai-response",
			payload: `{"input":[{"type":"message","role":"user","content":"` + testInnerReasoningCarrier() + `"},{"type":"function_call","arguments":"{\"value\":\"` + testInnerReasoningCarrier() + `\"}"}]}`,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := validateReasoningRequestForEndpoint(test.format, []byte(test.payload), translate.EndpointChatCompletions); err != nil {
				t.Fatalf("Chat preflight inspected ordinary content: %v", err)
			}
		})
	}
}

func TestReasoningPreflightKeepsNativeOpaqueHistory(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		format   string
		endpoint string
		payload  string
	}{
		{
			name:     "Claude Messages signature and redacted data",
			format:   "claude",
			endpoint: translate.EndpointMessages,
			payload:  `{"messages":[{"role":"assistant","content":[{"type":"thinking","signature":"foreign-signature"},{"type":"redacted_thinking","data":"foreign-data"}]}]}`,
		},
		{
			name:     "Responses encrypted content",
			format:   "openai-response",
			endpoint: translate.EndpointResponses,
			payload:  `{"input":[{"type":"reasoning","encrypted_content":"foreign-encrypted"}]}`,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := validateReasoningRequestForEndpoint(test.format, []byte(test.payload), test.endpoint); err != nil {
				t.Fatalf("native endpoint rejected opaque history: %v", err)
			}
		})
	}
}

func TestRequestCarrierRejectsWrongScopeInNonAssistantThinkingSlot(t *testing.T) {
	t.Parallel()
	issued := testReasoningCarrierScope()
	sealed, err := sealReasoningCarrier(testInnerReasoningCarrier(), issued)
	if err != nil {
		t.Fatalf("seal fixture: %v", err)
	}
	receiver := issued
	receiver.AuthID = "auth-b"
	receiver.Keys = testCarrierKeys(receiver.AuthID, receiver.Model, receiver.Endpoint, receiver.APIBaseURL)
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"thinking","thinking":"synthetic","signature":"` + sealed + `"}]}]}`)
	if _, err := unwrapRequestReasoningCarriers("claude", payload, receiver); err == nil {
		t.Fatal("wrong-scope carrier in a non-assistant thinking slot was accepted")
	}
}

func testReasoningCarrierScope() reasoningCarrierScope {
	return reasoningCarrierScopeFor("auth-a", "test-chat", translate.EndpointChatCompletions, "https://api.example", testCarrierKeys("auth-a", "test-chat", translate.EndpointChatCompletions, "https://api.example"))
}

func testCarrierKeys(authID, model, endpoint, apiBaseURL string) artifactKeyMaterials {
	return testCarrierKeysWithRoot(authID, model, endpoint, apiBaseURL, bytes.Repeat([]byte{0x44}, 32))
}

func testCarrierKeysWithRoot(authID, model, endpoint, apiBaseURL string, root []byte) artifactKeyMaterials {
	storage := authStorage{
		GitHubAccessToken: "fixture-credential",
		GitHubUserID:      4242,
		ContinuityKeyring: &continuityKeyring{
			Version:               continuityKeyringVersion,
			KeyID:                 "fixture-key-id",
			AccountID:             4242,
			RootKey:               base64.RawURLEncoding.EncodeToString(root),
			CredentialFingerprint: tokenFingerprint("fixture-credential"),
		},
	}
	keys, err := continuityKeyMaterialsFor(storage, authID, model, endpoint, apiBaseURL)
	if err != nil {
		panic(err)
	}
	return keys
}

func testInnerReasoningCarrier() string {
	const encoded = `eyJ2IjoxLCJwcm92aWRlciI6ImNvcGlsb3QiLCJlbmRwb2ludCI6ImNoYXQvY29tcGxldGlvbnMiLCJtb2RlbCI6InRlc3QtY2hhdCIsInJhd19qc29uX2I2NCI6ImV5SnlaV0Z6YjI1YVgyOXdhV0Z1SWpvaWMyVnhjM1JoWjJWemRHRjBhVzl1SW4wPSIsImFuY2hvciI6eyJjb250ZW50X3NoYTI1NiI6ImFiYyJ9fQ`
	return translatorReasoningCarrierPrefix + encoded
}

func tamperReasoningCarrierMAC(t *testing.T, sealed string) string {
	t.Helper()
	prefix, mac, ok := strings.Cut(sealed, ".")
	if !ok || len(mac) == 0 {
		t.Fatal("sealed fixture has no MAC segment")
	}
	replacement := "A"
	if mac[0] == 'A' {
		replacement = "B"
	}
	return prefix + "." + replacement + mac[1:]
}

type reasoningCarrierHost struct {
	mu       sync.Mutex
	requests []transport.Request
}

func (h *reasoningCarrierHost) Do(_ context.Context, _ string, request transport.Request) (transport.Response, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.requests = append(h.requests, request)
	return transport.Response{StatusCode: http.StatusOK, Body: []byte(`{}`)}, nil
}

func (*reasoningCarrierHost) OpenStream(context.Context, string, transport.Request) (transport.Stream, error) {
	return transport.Stream{}, nil
}

func (*reasoningCarrierHost) ReadStream(context.Context, string) (transport.StreamChunk, error) {
	return transport.StreamChunk{Done: true}, nil
}

func (*reasoningCarrierHost) CloseStream(context.Context, string) error   { return nil }
func (*reasoningCarrierHost) Emit(context.Context, string, []byte) error  { return nil }
func (*reasoningCarrierHost) CloseOutput(context.Context, string, string) {}

func newReasoningCarrierService(host *reasoningCarrierHost, authID, credential string) (*Service, authStorage) {
	now := time.Now().UTC()
	keyring, err := newContinuityKeyring(4242, credential)
	if err != nil {
		panic(err)
	}
	storage := authStorage{Type: providerID, GitHubAccessToken: credential, GitHubUserID: 4242, ContinuityKeyring: keyring}
	fingerprint := tokenFingerprint(credential)
	service := New(host)
	service.now = func() time.Time { return now }
	service.tokenEntries[authID] = copilotTokenEntry{Token: "ephemeral-copilot-token", APIBaseURL: "https://api.example", ExpiresAt: now.Add(time.Hour), Fingerprint: fingerprint}
	service.modelEntries[authID] = modelCacheEntry{
		Fingerprint: fingerprint,
		APIBaseURL:  "https://api.example",
		ExpiresAt:   now.Add(time.Hour),
		Models: []upstreamModel{{
			ID:                 "test-chat",
			Vendor:             "Other",
			ModelPickerEnabled: boolPointer(true),
			Policy:             &modelPolicy{State: "enabled"},
			Capabilities:       modelCapabilities{Type: "chat"},
			SupportedEndpoints: []string{translate.EndpointChatCompletions},
		}},
	}
	return service, storage
}

func mustMarshalReasoningStorage(t *testing.T, storage authStorage) []byte {
	t.Helper()
	body, err := json.Marshal(storage)
	if err != nil {
		t.Fatalf("marshal synthetic auth storage: %v", err)
	}
	return body
}

func extractReasoningCarrierSSEData(t *testing.T, frame []byte) string {
	t.Helper()
	for _, line := range strings.Split(string(frame), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "data: ") {
			return strings.TrimPrefix(line, "data: ")
		}
	}
	t.Fatal("SSE frame has no data line")
	return ""
}

func TestReasoningCarrierRejectsMissingContinuityKey(t *testing.T) {
	t.Parallel()
	scope := testReasoningCarrierScope()
	scope.Keys.Active = compact.KeyMaterial{}
	if _, err := sealReasoningCarrier(testInnerReasoningCarrier(), scope); err == nil {
		t.Fatal("carrier was sealed without a credential-derived key")
	}
}

func sealCarrierForTest(t *testing.T, scope reasoningCarrierScope) string {
	t.Helper()
	sealed, err := sealReasoningCarrier(testInnerReasoningCarrier(), scope)
	if err != nil {
		t.Fatalf("seal fixture: %v", err)
	}
	return sealed
}

func TestReasoningCarrierPayloadEncodingIsCanonical(t *testing.T) {
	t.Parallel()
	scope := testReasoningCarrierScope()
	sealed := sealCarrierForTest(t, scope)
	encoded, _, ok := strings.Cut(strings.TrimPrefix(sealed, sealedReasoningCarrierV2Prefix), ".")
	if !ok {
		t.Fatal("sealed format has no payload separator")
	}
	inner, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || string(inner) != testInnerReasoningCarrier() {
		t.Fatalf("sealed inner payload = %q, err=%v", inner, err)
	}
}

func TestLegacyReasoningCarrierMigratesToStableV2(t *testing.T) {
	t.Parallel()
	storage := continuityTestStorage("credential-after-oauth-refresh")
	storage.ContinuityKeyring.LegacyV1 = &legacyContinuityKey{
		AccountID:             storage.GitHubUserID,
		AuthID:                "auth-a",
		CredentialFingerprint: tokenFingerprint("credential-before-oauth-refresh"),
		APIBaseURL:            "https://api.example",
	}
	keys, err := continuityKeyMaterialsFor(storage, "auth-a", "test-chat", translate.EndpointChatCompletions, "https://api.example")
	if err != nil || len(keys.Legacy) != 1 {
		t.Fatalf("legacy carrier materials = %#v, err=%v", keys, err)
	}
	legacyKey := keys.Legacy[0]
	inner := []byte(testInnerReasoningCarrier())
	mac := reasoningCarrierMAC(legacyKey.Secret, legacyKey.Scope, inner, reasoningCarrierMACDomainV1)
	sealed := sealedReasoningCarrierV1Prefix + base64.RawURLEncoding.EncodeToString(inner) + "." + base64.RawURLEncoding.EncodeToString(mac)
	scope := reasoningCarrierScopeFor("auth-a", "test-chat", translate.EndpointChatCompletions, "https://api.example", keys)
	request := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"prior","signature":"` + sealed + `"}]}]}`)
	unwrapped, err := unwrapRequestReasoningCarriers("claude", request, scope)
	if err != nil || gjson.GetBytes(unwrapped, "messages.0.content.0.signature").String() != testInnerReasoningCarrier() {
		t.Fatalf("legacy carrier unwrap = %s, err=%v", unwrapped, err)
	}
	response := []byte(`{"content":[{"type":"thinking","thinking":"prior","signature":"` + testInnerReasoningCarrier() + `"}]}`)
	migrated, err := sealResponseReasoningCarriers("claude", response, scope)
	if err != nil || !strings.HasPrefix(gjson.GetBytes(migrated, "content.0.signature").String(), sealedReasoningCarrierV2Prefix) {
		t.Fatalf("legacy response carrier migration = %s, err=%v", migrated, err)
	}
}
