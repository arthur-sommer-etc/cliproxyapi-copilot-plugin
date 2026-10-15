package provider

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

type directOAuthTestHost struct {
	mu               sync.Mutex
	requests         []transport.Request
	streamRequests   []transport.Request
	closedStreams    []string
	discoveryBody    []byte
	tokenBody        []byte
	discoveryStatus  int
	catalogStatus    int
	responseStatus   int
	httpStatus       int
	streamStatus     int
	responseBody     []byte
	blockDiscovery   bool
	discoveryEntered chan struct{}
	discoveryRelease chan struct{}
	blockResponse    bool
	responseEntered  chan struct{}
	responseRelease  chan struct{}
}

type directOAuthWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *directOAuthWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func newDirectOAuthTestHost() *directOAuthTestHost {
	return &directOAuthTestHost{
		discoveryBody: []byte(`{"endpoints":{"api":"https://api.example"}}`),
		responseBody:  []byte(`{"id":"resp_direct","object":"response","status":"completed","model":"gpt-6-luna","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`),
	}
}

func (h *directOAuthTestHost) Do(ctx context.Context, _ string, request transport.Request) (transport.Response, error) {
	if err := ctx.Err(); err != nil {
		return transport.Response{}, err
	}
	request = cloneTransportRequest(request)
	h.mu.Lock()
	h.requests = append(h.requests, request)
	discoveryBody := append([]byte(nil), h.discoveryBody...)
	tokenBody := append([]byte(nil), h.tokenBody...)
	discoveryStatus := h.discoveryStatus
	catalogStatus := h.catalogStatus
	responseStatus := h.responseStatus
	httpStatus := h.httpStatus
	responseBody := append([]byte(nil), h.responseBody...)
	blockDiscovery := h.blockDiscovery
	discoveryEntered := h.discoveryEntered
	discoveryRelease := h.discoveryRelease
	blockResponse := h.blockResponse
	responseEntered := h.responseEntered
	responseRelease := h.responseRelease
	h.mu.Unlock()

	switch {
	case strings.HasSuffix(request.URL, "/copilot_internal/user"):
		if discoveryEntered != nil {
			select {
			case discoveryEntered <- struct{}{}:
			default:
			}
		}
		if blockDiscovery && discoveryRelease != nil {
			select {
			case <-ctx.Done():
				return transport.Response{}, ctx.Err()
			case <-discoveryRelease:
			}
		}
		return transport.Response{StatusCode: statusOrOK(discoveryStatus), Body: discoveryBody}, nil
	case strings.HasSuffix(request.URL, "/copilot_internal/v2/token"):
		if len(tokenBody) == 0 {
			tokenBody = []byte(`{"token":"synthetic-copilot-token","expires_at":1893456000,"endpoints":{"api":"https://api.example"}}`)
		}
		return transport.Response{StatusCode: http.StatusOK, Body: tokenBody}, nil
	case strings.HasSuffix(request.URL, "/models"):
		return transport.Response{StatusCode: statusOrOK(catalogStatus), Body: directOAuthCatalog()}, nil
	case request.Method == http.MethodPost:
		if responseEntered != nil {
			select {
			case responseEntered <- struct{}{}:
			default:
			}
		}
		if blockResponse && responseRelease != nil {
			select {
			case <-ctx.Done():
				return transport.Response{}, ctx.Err()
			case <-responseRelease:
			}
		}
		return transport.Response{StatusCode: statusOrOK(responseStatus), Body: responseBody}, nil
	default:
		return transport.Response{StatusCode: statusOrOK(httpStatus), Body: []byte(`{"ok":true}`)}, nil
	}
}

func (h *directOAuthTestHost) OpenStream(ctx context.Context, _ string, request transport.Request) (transport.Stream, error) {
	if err := ctx.Err(); err != nil {
		return transport.Stream{}, err
	}
	request = cloneTransportRequest(request)
	h.mu.Lock()
	h.streamRequests = append(h.streamRequests, request)
	status := statusOrOK(h.streamStatus)
	id := "stream-id"
	h.mu.Unlock()
	return transport.Stream{ID: id, StatusCode: status}, nil
}

func (*directOAuthTestHost) ReadStream(context.Context, string) (transport.StreamChunk, error) {
	return transport.StreamChunk{Done: true}, nil
}

func (h *directOAuthTestHost) CloseStream(_ context.Context, id string) error {
	h.mu.Lock()
	h.closedStreams = append(h.closedStreams, id)
	h.mu.Unlock()
	return nil
}

func (*directOAuthTestHost) Emit(context.Context, string, []byte) error { return nil }

func (*directOAuthTestHost) CloseOutput(context.Context, string, string) {}

func (h *directOAuthTestHost) requestSnapshot() ([]transport.Request, []transport.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	requests := make([]transport.Request, len(h.requests))
	for index, request := range h.requests {
		requests[index] = cloneTransportRequest(request)
	}
	streams := make([]transport.Request, len(h.streamRequests))
	for index, request := range h.streamRequests {
		streams[index] = cloneTransportRequest(request)
	}
	return requests, streams
}

func cloneTransportRequest(request transport.Request) transport.Request {
	request.Body = append([]byte(nil), request.Body...)
	request.Headers = request.Headers.Clone()
	return request
}

func statusOrOK(status int) int {
	if status == 0 {
		return http.StatusOK
	}
	return status
}

func directOAuthCatalog() []byte {
	return []byte(`{"data":[{"id":"gpt-6-luna","vendor":"OpenAI","model_picker_enabled":true,"policy":{"state":"enabled"},"capabilities":{"type":"chat"},"supported_endpoints":["/responses"]}]}`)
}

func directOAuthService(t *testing.T, host *directOAuthTestHost) *Service {
	t.Helper()
	service := New(host)
	if err := service.Configure([]byte("auth_mode: direct_oauth\n")); err != nil {
		t.Fatalf("configure direct OAuth: %v", err)
	}
	return service
}

func directOAuthStorage(t *testing.T, credential string, expiresAt int64) ([]byte, authStorage) {
	t.Helper()
	storage := continuityTestStorage(credential)
	storage.Type = providerID
	storage.ExpiresAt = expiresAt
	raw, err := marshalStorage(storage)
	if err != nil {
		t.Fatalf("marshal synthetic auth storage: %v", err)
	}
	return raw, storage
}

func directOAuthExecuteRequest(storage []byte, authID string, streamID string) ExecuteRequest {
	payload := []byte(`{"model":"gpt-6-luna","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)
	return ExecuteRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			AuthID:          authID,
			SourceFormat:    "openai-response",
			Model:           "gpt-6-luna",
			OriginalRequest: append([]byte(nil), payload...),
			Payload:         append([]byte(nil), payload...),
			StorageJSON:     storage,
		},
		StreamID: streamID,
	}
}

func TestAuthModeDefaultsToTokenExchangeAndRejectsUnknownModes(t *testing.T) {
	if got := DefaultConfig().AuthMode; got != authModeTokenExchange {
		t.Fatalf("default auth mode = %q, want %q", got, authModeTokenExchange)
	}
	cfg, err := ParseConfig([]byte("auth_mode: DIRECT_OAUTH\n"))
	if err != nil || cfg.AuthMode != authModeDirectOAuth {
		t.Fatalf("parse direct OAuth mode: config=%#v err=%v", cfg, err)
	}
	if _, err := ParseConfig([]byte("auth_mode: automatic\n")); err == nil {
		t.Fatal("unknown auth mode was accepted")
	}

	host := newDirectOAuthTestHost()
	service := New(host)
	_, err = service.copilotToken(context.Background(), "callback", "auth-id", authStorage{GitHubAccessToken: "synthetic-github-token"})
	if err != nil {
		t.Fatalf("default token exchange: %v", err)
	}
	requests, _ := host.requestSnapshot()
	if len(requests) != 1 || !strings.HasSuffix(requests[0].URL, "/copilot_internal/v2/token") || requests[0].Headers.Get("Authorization") != "token synthetic-github-token" {
		t.Fatalf("default auth request = %#v, want one legacy token exchange", requests)
	}
}

func TestDirectOAuthRequiresDiscoveredEndpointAndUsesBearerDiscovery(t *testing.T) {
	t.Run("valid endpoint", func(t *testing.T) {
		host := newDirectOAuthTestHost()
		service := directOAuthService(t, host)
		_, storage := directOAuthStorage(t, "synthetic-github-token", 0)
		entry, err := service.copilotToken(context.Background(), "host-callback", "auth-id", storage)
		if err != nil {
			t.Fatalf("direct discovery: %v", err)
		}
		if entry.Mode != authModeDirectOAuth || entry.Token != storage.GitHubAccessToken || entry.APIBaseURL != "https://api.example" {
			t.Fatalf("direct entry = %#v", entry)
		}
		if !entry.ExpiresAt.IsZero() || !entry.ContextExpiresAt.After(service.now()) {
			t.Fatalf("unknown credential expiry or discovery TTL was misrepresented: %#v", entry)
		}
		requests, _ := host.requestSnapshot()
		if len(requests) != 1 || requests[0].URL != "https://api.github.com/copilot_internal/user" || requests[0].Method != http.MethodGet {
			t.Fatalf("direct discovery request = %#v", requests)
		}
		if requests[0].Headers.Get("Authorization") != "Bearer synthetic-github-token" || requests[0].Headers.Get("User-Agent") != userAgent() {
			t.Fatalf("direct discovery headers = %#v", requests[0].Headers)
		}
		integration := requests[0].Headers.Get("Copilot-Integration-Id")
		version := requests[0].Headers.Get("X-GitHub-Api-Version")
		accept := requests[0].Headers.Get("Accept")
		if integration != "" || version != copilotAPIVersion || accept != "application/json" {
			t.Fatalf("direct discovery headers integration=%q version=%q wantVersion=%q accept=%q: %#v", integration, version, copilotAPIVersion, accept, requests[0].Headers)
		}
	})

	for _, test := range []struct {
		name string
		body string
	}{
		{name: "missing endpoint", body: `{"endpoints":{}}`},
		{name: "HTTP endpoint", body: `{"endpoints":{"api":"http://api.example"}}`},
		{name: "endpoint userinfo", body: `{"endpoints":{"api":"https://user:pass@api.example"}}`},
		{name: "endpoint query", body: `{"endpoints":{"api":"https://api.example?x=1"}}`},
		{name: "endpoint fragment", body: `{"endpoints":{"api":"https://api.example#fragment"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := newDirectOAuthTestHost()
			host.discoveryBody = []byte(test.body)
			service := New(host)
			if err := service.Configure([]byte("auth_mode: direct_oauth\ncopilot_api_url: https://fallback.example\n")); err != nil {
				t.Fatalf("configure direct OAuth: %v", err)
			}
			_, storage := directOAuthStorage(t, "synthetic-github-token", 0)
			if _, err := service.copilotToken(context.Background(), "callback", "auth-id", storage); err == nil {
				t.Fatal("invalid discovered endpoint was accepted")
			}
			requests, _ := host.requestSnapshot()
			if len(requests) != 1 || !strings.HasSuffix(requests[0].URL, "/copilot_internal/user") {
				t.Fatalf("invalid discovery made unexpected requests: %#v", requests)
			}
		})
	}
}

func TestDirectOAuthExpiryAndDiscoveryTTLStaySeparate(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	host := newDirectOAuthTestHost()
	service := directOAuthService(t, host)
	service.now = func() time.Time { return now }
	if err := service.Configure([]byte("auth_mode: direct_oauth\nmodel_cache_ttl_seconds: 30\n")); err != nil {
		t.Fatalf("configure direct OAuth TTL: %v", err)
	}
	_, storage := directOAuthStorage(t, "synthetic-github-token", now.Add(time.Hour).Unix())
	entry, err := service.copilotToken(context.Background(), "callback", "auth-id", storage)
	if err != nil {
		t.Fatalf("first discovery: %v", err)
	}
	if !entry.ExpiresAt.Equal(time.Unix(storage.ExpiresAt, 0)) || !entry.ContextExpiresAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("credential expiry and context TTL were not kept separate: %#v", entry)
	}
	storage.ExpiresAt = now.Add(2 * time.Hour).Unix()
	entry, err = service.copilotToken(context.Background(), "callback", "auth-id", storage)
	if err != nil || !entry.ExpiresAt.Equal(time.Unix(storage.ExpiresAt, 0)) {
		t.Fatalf("updated storage expiry was not reflected on cache hit: entry=%#v err=%v", entry, err)
	}
	storage.ExpiresAt = 0
	entry, err = service.copilotToken(context.Background(), "callback", "auth-id", storage)
	if err != nil || !entry.ExpiresAt.IsZero() {
		t.Fatalf("unknown storage expiry was reported: entry=%#v err=%v", entry, err)
	}
	requests, _ := host.requestSnapshot()
	if len(requests) != 1 {
		t.Fatalf("cache hits repeated discovery %d times", len(requests))
	}

	now = now.Add(30 * time.Second)
	if _, err := service.copilotToken(context.Background(), "callback", "auth-id", storage); err != nil {
		t.Fatalf("discovery after TTL: %v", err)
	}
	requests, _ = host.requestSnapshot()
	if len(requests) != 2 {
		t.Fatalf("discovery TTL did not expire the cached context: requests=%d", len(requests))
	}
}

func TestDirectOAuthExpiredCredentialRequiresHostRefreshBeforeDiscovery(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	host := newDirectOAuthTestHost()
	service := directOAuthService(t, host)
	service.now = func() time.Time { return now }
	storage, _ := directOAuthStorage(t, "synthetic-github-token", now.Add(-time.Second).Unix())
	_, err := service.Execute(context.Background(), directOAuthExecuteRequest(storage, "auth-id", ""))
	statusErr, ok := err.(*StatusError)
	if !ok || statusErr.Code != "auth_state_refresh_required" || statusErr.HTTPStatus != http.StatusConflict {
		t.Fatalf("expired credential error = %#v, want auth refresh required", err)
	}
	requests, streams := host.requestSnapshot()
	if len(requests) != 0 || len(streams) != 0 {
		t.Fatalf("expired credential made discovery or protected requests: HTTP=%#v streams=%#v", requests, streams)
	}
}

func TestDirectOAuthSingleflightCancellationRotationAndConfigGeneration(t *testing.T) {
	host := newDirectOAuthTestHost()
	host.blockDiscovery = true
	host.discoveryEntered = make(chan struct{}, 1)
	host.discoveryRelease = make(chan struct{})
	service := directOAuthService(t, host)
	_, storageA := directOAuthStorage(t, "credential-a", 0)
	leader := make(chan error, 1)
	go func() {
		_, err := service.copilotToken(context.Background(), "callback", "auth-id", storageA)
		leader <- err
	}()
	<-host.discoveryEntered
	waitContext, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.copilotToken(waitContext, "callback", "auth-id", storageA); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled singleflight waiter error = %v, want context canceled", err)
	}
	close(host.discoveryRelease)
	if err := <-leader; err != nil {
		t.Fatalf("leader was canceled with its waiter: %v", err)
	}
	host.mu.Lock()
	host.blockDiscovery = false
	host.mu.Unlock()

	_, storageB := directOAuthStorage(t, "credential-b", 0)
	if _, err := service.copilotToken(context.Background(), "callback", "auth-id", storageB); err != nil {
		t.Fatalf("credential rotation: %v", err)
	}
	if err := service.Configure([]byte("auth_mode: direct_oauth\nmodel_cache_ttl_seconds: 60\n")); err != nil {
		t.Fatalf("change direct configuration: %v", err)
	}
	entry, err := service.copilotToken(context.Background(), "callback", "auth-id", storageB)
	if err != nil || entry.ConfigGeneration != 2 || entry.Mode != authModeDirectOAuth {
		t.Fatalf("direct entry after config change = %#v err=%v", entry, err)
	}
	requests, _ := host.requestSnapshot()
	if len(requests) != 3 {
		t.Fatalf("singleflight, credential rotation, and config change made %d discovery requests, want 3", len(requests))
	}
}

func TestDirectOAuthSingleflightWaiterUsesItsStorageExpiry(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	for _, test := range []struct {
		name         string
		waiterExpiry int64
		wantExpiry   time.Time
		wantError    bool
	}{
		{name: "known later expiry", waiterExpiry: now.Add(2 * time.Hour).Unix(), wantExpiry: now.Add(2 * time.Hour)},
		{name: "unknown expiry"},
		{name: "earlier expired credential", waiterExpiry: now.Add(-time.Second).Unix(), wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := newDirectOAuthTestHost()
			host.blockDiscovery = true
			host.discoveryEntered = make(chan struct{}, 1)
			host.discoveryRelease = make(chan struct{})
			service := directOAuthService(t, host)
			service.now = func() time.Time { return now }
			if err := service.Configure([]byte("auth_mode: direct_oauth\nmodel_cache_ttl_seconds: 30\n")); err != nil {
				t.Fatalf("configure direct OAuth: %v", err)
			}
			_, leaderStorage := directOAuthStorage(t, "synthetic-github-token", now.Add(time.Hour).Unix())
			_, waiterStorage := directOAuthStorage(t, "synthetic-github-token", test.waiterExpiry)
			type tokenResult struct {
				entry copilotTokenEntry
				err   error
			}
			leaderResult := make(chan tokenResult, 1)
			go func() {
				entry, err := service.copilotToken(context.Background(), "callback", "auth-id", leaderStorage)
				leaderResult <- tokenResult{entry: entry, err: err}
			}()
			<-host.discoveryEntered
			waitContext := &directOAuthWaitContext{Context: context.Background(), waiting: make(chan struct{})}
			waiterResult := make(chan tokenResult, 1)
			go func() {
				entry, err := service.copilotToken(waitContext, "callback", "auth-id", waiterStorage)
				waiterResult <- tokenResult{entry: entry, err: err}
			}()
			<-waitContext.waiting
			close(host.discoveryRelease)
			if result := <-leaderResult; result.err != nil {
				t.Fatalf("singleflight leader: %v", result.err)
			}
			result := <-waiterResult
			if test.wantError {
				statusErr, ok := result.err.(*StatusError)
				if !ok || statusErr.Code != "auth_state_refresh_required" || statusErr.HTTPStatus != http.StatusConflict {
					t.Fatalf("expired waiter result = %#v, want auth refresh required", result)
				}
			} else {
				if result.err != nil {
					t.Fatalf("singleflight waiter: %v", result.err)
				}
				if !result.entry.ExpiresAt.Equal(test.wantExpiry) {
					t.Fatalf("waiter expiry = %s, want %s", result.entry.ExpiresAt, test.wantExpiry)
				}
			}
			requests, _ := host.requestSnapshot()
			if len(requests) != 1 || !strings.HasSuffix(requests[0].URL, "/copilot_internal/user") {
				t.Fatalf("waiter rediscovered context or reached a protected endpoint: %#v", requests)
			}
		})
	}
}

func TestDirectOAuthSingleflightWaiterRejectsExpiredContext(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	var nowMu sync.Mutex
	nowCalls := 0
	host := newDirectOAuthTestHost()
	host.blockDiscovery = true
	host.discoveryEntered = make(chan struct{}, 1)
	host.discoveryRelease = make(chan struct{})
	service := directOAuthService(t, host)
	if err := service.Configure([]byte("auth_mode: direct_oauth\nmodel_cache_ttl_seconds: 30\n")); err != nil {
		t.Fatalf("configure direct OAuth: %v", err)
	}
	service.now = func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		nowCalls++
		if nowCalls >= 5 {
			return now.Add(time.Minute)
		}
		return now
	}
	_, storage := directOAuthStorage(t, "synthetic-github-token", 0)
	leaderResult := make(chan error, 1)
	go func() {
		_, err := service.copilotToken(context.Background(), "callback", "auth-id", storage)
		leaderResult <- err
	}()
	<-host.discoveryEntered
	waitContext := &directOAuthWaitContext{Context: context.Background(), waiting: make(chan struct{})}
	waiterResult := make(chan error, 1)
	go func() {
		_, err := service.copilotToken(waitContext, "callback", "auth-id", storage)
		waiterResult <- err
	}()
	<-waitContext.waiting
	close(host.discoveryRelease)
	if err := <-leaderResult; err != nil {
		t.Fatalf("singleflight leader: %v", err)
	}
	statusErr, ok := (<-waiterResult).(*StatusError)
	if !ok || statusErr.Code != "auth_state_refresh_required" || statusErr.HTTPStatus != http.StatusConflict {
		t.Fatalf("expired-context waiter error = %#v, want auth refresh required", statusErr)
	}
	requests, _ := host.requestSnapshot()
	if len(requests) != 1 || !strings.HasSuffix(requests[0].URL, "/copilot_internal/user") {
		t.Fatalf("expired waiter reused context or rediscovered unexpectedly: %#v", requests)
	}
}

func TestDirectOAuthExecuteResponsesOmitsUnknownExpiryMetadata(t *testing.T) {
	for _, test := range []struct {
		name       string
		expiresAt  int64
		wantExpiry bool
	}{
		{name: "unknown expiry"},
		{name: "known expiry", expiresAt: time.Unix(1_900_000_000, 0).Unix(), wantExpiry: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := newDirectOAuthTestHost()
			service := directOAuthService(t, host)
			storage, _ := directOAuthStorage(t, "synthetic-github-token", test.expiresAt)
			response, err := service.Execute(context.Background(), directOAuthExecuteRequest(storage, "auth-id", ""))
			if err != nil {
				t.Fatalf("execute Responses request: %v", err)
			}
			if gjson.GetBytes(response.Payload, "object").String() != "response" || response.Metadata["copilot_endpoint"] != "/responses" {
				t.Fatalf("Responses output or selected endpoint changed: payload=%s metadata=%#v", response.Payload, response.Metadata)
			}
			value, present := response.Metadata["token_expires_at"]
			if present != test.wantExpiry {
				t.Fatalf("token expiry metadata presence = %v, want %v (%#v)", present, test.wantExpiry, response.Metadata)
			}
			if test.wantExpiry && value != time.Unix(test.expiresAt, 0).UTC().Format(http.TimeFormat) {
				t.Fatalf("token expiry metadata = %v", value)
			}
			requests, _ := host.requestSnapshot()
			if len(requests) != 3 || !strings.HasSuffix(requests[0].URL, "/copilot_internal/user") || !strings.HasSuffix(requests[1].URL, "/models") || !strings.HasSuffix(requests[2].URL, "/responses") {
				t.Fatalf("direct Responses request path sequence = %#v", requests)
			}
			for _, request := range requests {
				authorization := request.Headers.Get("Authorization")
				integration := request.Headers.Get("Copilot-Integration-Id")
				editor := request.Headers.Get("Editor-Version")
				version := request.Headers.Get("X-GitHub-Api-Version")
				accept := request.Headers.Get("Accept")
				if authorization != "Bearer synthetic-github-token" || integration != "" || editor != "" || version != copilotAPIVersion || accept != "application/json" {
					t.Fatalf("direct request headers auth=%q integration=%q editor=%q version=%q wantVersion=%q accept=%q: %#v", authorization, integration, editor, version, copilotAPIVersion, accept, request.Headers)
				}
			}
		})
	}
}

func TestDirectOAuthCatalogBufferedAndStreamErrorsReturnOnceAndInvalidate(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		for _, operation := range []string{"catalog", "buffered", "stream"} {
			t.Run(operation+"_"+http.StatusText(status), func(t *testing.T) {
				host := newDirectOAuthTestHost()
				if operation == "catalog" {
					host.catalogStatus = status
				} else if operation == "stream" {
					host.streamStatus = status
				} else {
					host.responseStatus = status
				}
				service := directOAuthService(t, host)
				storageJSON, storage := directOAuthStorage(t, "synthetic-github-token", 0)
				authID := "auth-id"
				var err error
				if operation == "catalog" {
					_, err = service.ModelsForAuth(context.Background(), "callback", pluginapi.AuthModelRequest{AuthID: authID, StorageJSON: storageJSON})
				} else if operation == "stream" {
					_, err = service.ExecuteStream(context.Background(), directOAuthExecuteRequest(storageJSON, authID, "stream-output"))
				} else {
					_, err = service.Execute(context.Background(), directOAuthExecuteRequest(storageJSON, authID, ""))
				}
				statusErr, ok := err.(*StatusError)
				if !ok || statusErr.HTTPStatus != status {
					t.Fatalf("%s error = %#v, want upstream status %d", operation, err, status)
				}
				requests, streams := host.requestSnapshot()
				count := 0
				discoveryCount := 0
				for _, request := range requests {
					if strings.HasSuffix(request.URL, "/copilot_internal/user") {
						discoveryCount++
					}
					if operation == "catalog" && strings.HasSuffix(request.URL, "/models") || operation == "buffered" && strings.HasSuffix(request.URL, "/responses") {
						count++
					}
					if strings.HasSuffix(request.URL, "/copilot_internal/v2/token") {
						t.Fatal("direct OAuth called the token exchange endpoint")
					}
				}
				if operation == "stream" {
					count = len(streams)
				}
				if count != 1 {
					t.Fatalf("%s made %d protected request(s), want one; HTTP=%#v streams=%#v", operation, count, requests, streams)
				}
				if discoveryCount != 1 {
					t.Fatalf("%s rediscovered authentication %d times, want only initial discovery", operation, discoveryCount)
				}
				if _, exists := service.tokenEntries[authID]; exists {
					t.Fatalf("%s error retained cached authentication", operation)
				}
				if _, exists := service.modelEntries[authID]; exists {
					t.Fatalf("%s error retained cached model context", operation)
				}
				if storage.GitHubAccessToken != "synthetic-github-token" {
					t.Fatal("synthetic storage unexpectedly changed")
				}
			})
		}
	}
}

func TestDirectOAuthInvalidatesFingerprintKeyWhenAuthIDIsEmpty(t *testing.T) {
	host := newDirectOAuthTestHost()
	host.responseStatus = http.StatusUnauthorized
	service := directOAuthService(t, host)
	storageJSON, storage := directOAuthStorage(t, "synthetic-github-token", 0)
	fingerprint := tokenFingerprint(storage.GitHubAccessToken)
	_, err := service.Execute(context.Background(), directOAuthExecuteRequest(storageJSON, "", ""))
	statusErr, ok := err.(*StatusError)
	if !ok || statusErr.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("empty-authID request error = %#v", err)
	}
	if _, exists := service.tokenEntries[fingerprint]; exists {
		t.Fatal("direct 401 retained token cache under the fingerprint fallback key")
	}
	if _, exists := service.modelEntries[fingerprint]; exists {
		t.Fatal("direct 401 retained model cache under the fingerprint fallback key")
	}
}

func TestDirectOAuthHTTP401403InvalidatesMatchingContext(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		for _, authID := range []string{"auth-id", ""} {
			name := "named-auth"
			if authID == "" {
				name = "fingerprint-auth"
			}
			t.Run(name+"_"+http.StatusText(status), func(t *testing.T) {
				host := newDirectOAuthTestHost()
				service := directOAuthService(t, host)
				_, storage := directOAuthStorage(t, "synthetic-github-token", 0)
				if _, _, err := service.models(context.Background(), "callback", authID, storage, false); err != nil {
					t.Fatalf("populate direct token and model context: %v", err)
				}
				key := authID
				if key == "" {
					key = tokenFingerprint(storage.GitHubAccessToken)
				}
				host.mu.Lock()
				host.httpStatus = status
				host.mu.Unlock()
				storageJSON, _ := marshalStorage(storage)
				response, err := service.HTTP(context.Background(), HTTPRequest{ExecutorHTTPRequest: pluginapi.ExecutorHTTPRequest{
					AuthID:      authID,
					Method:      http.MethodGet,
					URL:         "https://api.example/auth-check",
					StorageJSON: storageJSON,
				}})
				if err != nil || response.StatusCode != status {
					t.Fatalf("direct HTTP status response = %#v err=%v, want %d", response, err, status)
				}
				if _, exists := service.tokenEntries[key]; exists {
					t.Fatal("direct HTTP error retained matching auth context")
				}
				if _, exists := service.modelEntries[key]; exists {
					t.Fatal("direct HTTP error retained matching model context")
				}
				requests, _ := host.requestSnapshot()
				var authCheckCount, discoveryCount int
				for _, request := range requests {
					if strings.HasSuffix(request.URL, "/auth-check") {
						authCheckCount++
					}
					if strings.HasSuffix(request.URL, "/copilot_internal/user") {
						discoveryCount++
					}
				}
				if authCheckCount != 1 || discoveryCount != 1 {
					t.Fatalf("direct HTTP retries or rediscovered: requests=%#v", requests)
				}
			})
		}
	}
}

func TestDirectOAuthLateUnauthorizedRequestPreservesRotatedCache(t *testing.T) {
	host := newDirectOAuthTestHost()
	host.responseStatus = http.StatusUnauthorized
	host.blockResponse = true
	host.responseEntered = make(chan struct{}, 1)
	host.responseRelease = make(chan struct{})
	service := directOAuthService(t, host)
	_, storageA := directOAuthStorage(t, "credential-a", 0)
	entryA, err := service.copilotToken(context.Background(), "callback", "auth-id", storageA)
	if err != nil {
		t.Fatalf("discover credential A: %v", err)
	}
	if _, _, err := service.models(context.Background(), "callback", "auth-id", storageA, false); err != nil {
		t.Fatalf("populate credential A model context: %v", err)
	}
	requestResult := make(chan error, 1)
	go func() {
		_, _, err := service.doModelRequest(context.Background(), "callback", "auth-id", storageA, entryA, "/responses", []byte(`{}`), false)
		requestResult <- err
	}()
	<-host.responseEntered

	_, storageB := directOAuthStorage(t, "credential-b", 0)
	entryB, err := service.copilotToken(context.Background(), "callback", "auth-id", storageB)
	if err != nil {
		t.Fatalf("discover rotated credential B: %v", err)
	}
	if _, _, err := service.models(context.Background(), "callback", "auth-id", storageB, false); err != nil {
		t.Fatalf("populate credential B model context: %v", err)
	}
	close(host.responseRelease)
	statusErr, ok := (<-requestResult).(*StatusError)
	if !ok || statusErr.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("late credential A response = %#v, want HTTP 401", statusErr)
	}
	if token := service.tokenEntries["auth-id"]; token.Fingerprint != entryB.Fingerprint {
		t.Fatalf("late credential A error evicted credential B token: %#v", token)
	}
	if models := service.modelEntries["auth-id"]; models.Fingerprint != entryB.Fingerprint {
		t.Fatalf("late credential A error evicted credential B catalog: %#v", models)
	}
}

func TestDirectOAuthHTTPUsesDiscoveredOriginAndHonestHeaders(t *testing.T) {
	host := newDirectOAuthTestHost()
	service := directOAuthService(t, host)
	storageJSON, _ := directOAuthStorage(t, "synthetic-github-token", 0)
	request := HTTPRequest{ExecutorHTTPRequest: pluginapi.ExecutorHTTPRequest{
		AuthID:      "auth-id",
		Method:      http.MethodGet,
		URL:         "https://api.example/models",
		StorageJSON: storageJSON,
		Headers: http.Header{
			"Accept":                     []string{"application/json"},
			"Copilot-Integration-Id":     []string{"vscode-chat"},
			"Editor-Version":             []string{"vscode/test"},
			"OpenAI-Intent":              []string{"conversation-edits"},
			"X-GitHub-Api-Version":       []string{"caller-version"},
			"X-Caller-Correlation-Token": []string{"synthetic-correlation"},
		},
	}}
	response, err := service.HTTP(context.Background(), request)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("direct executor HTTP: response=%#v err=%v", response, err)
	}
	requests, _ := host.requestSnapshot()
	if len(requests) != 2 || requests[1].URL != request.URL {
		t.Fatalf("direct HTTP request sequence = %#v", requests)
	}
	headers := requests[1].Headers
	if headers.Get("Authorization") != "Bearer synthetic-github-token" || headers.Get("User-Agent") != userAgent() || headers.Get("Accept") != "application/json" || headers.Get("X-GitHub-Api-Version") != copilotAPIVersion {
		t.Fatalf("direct HTTP headers = %#v", headers)
	}
	for _, name := range []string{"Copilot-Integration-Id", "Editor-Version", "OpenAI-Intent"} {
		if headers.Get(name) != "" {
			t.Fatalf("direct HTTP forwarded editor integration header %s", name)
		}
	}
	if headers.Get("X-Caller-Correlation-Token") != "synthetic-correlation" {
		t.Fatal("direct HTTP dropped an unrelated caller header")
	}
	request.URL = "https://other.example/models"
	if _, err := service.HTTP(context.Background(), request); err == nil {
		t.Fatal("direct HTTP accepted a URL outside the discovered origin")
	}
	requests, _ = host.requestSnapshot()
	if len(requests) != 2 {
		t.Fatalf("foreign HTTP origin reached the transport: %d requests", len(requests))
	}
}

func TestDefaultTokenExchangeStillExecutesResponsesWithLegacyHeaders(t *testing.T) {
	host := newDirectOAuthTestHost()
	service := New(host)
	storageJSON, _ := directOAuthStorage(t, "synthetic-github-token", 0)
	response, err := service.Execute(context.Background(), directOAuthExecuteRequest(storageJSON, "auth-id", ""))
	if err != nil {
		t.Fatalf("default Responses execution: %v", err)
	}
	if gjson.GetBytes(response.Payload, "object").String() != "response" {
		t.Fatalf("default Responses output = %s", response.Payload)
	}
	requests, _ := host.requestSnapshot()
	if len(requests) != 3 || !strings.HasSuffix(requests[0].URL, "/copilot_internal/v2/token") || requests[0].Headers.Get("Authorization") != "token synthetic-github-token" {
		t.Fatalf("default Responses request sequence = %#v", requests)
	}
	if requests[1].Headers.Get("Copilot-Integration-Id") != "vscode-chat" || requests[2].Headers.Get("Editor-Plugin-Version") != "copilot-chat/0.35.0" {
		t.Fatalf("default mode lost recognized legacy headers: catalog=%#v inference=%#v", requests[1].Headers, requests[2].Headers)
	}
}
