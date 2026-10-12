package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type oauthDeviceLifecycleHost struct {
	mu sync.Mutex

	deviceRequests int
	tokenRequests  int
	userRequests   int
	tokenResponses []transport.Response
	userResponses  []transport.Response
	userErrors     []error
	deviceEntered  chan struct{}
	deviceRelease  <-chan struct{}
	userEntered    chan struct{}
	userRelease    chan struct{}
	activeUsers    int
	maxActiveUsers int
}

func (h *oauthDeviceLifecycleHost) Do(ctx context.Context, _ string, request transport.Request) (transport.Response, error) {
	switch request.URL {
	case "https://github.com/login/device/code":
		h.mu.Lock()
		h.deviceRequests++
		entered := h.deviceEntered
		release := h.deviceRelease
		h.mu.Unlock()
		if entered != nil {
			entered <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return transport.Response{}, ctx.Err()
			}
		}
		return transport.Response{StatusCode: http.StatusOK, Body: []byte(`{"device_code":"synthetic-device-code","user_code":"ABCD-EFGH","verification_uri":"https://github.com/login/device","expires_in":300,"interval":5}`)}, nil
	case "https://github.com/login/oauth/access_token":
		h.mu.Lock()
		defer h.mu.Unlock()
		h.tokenRequests++
		if len(h.tokenResponses) > 0 {
			response := h.tokenResponses[0]
			h.tokenResponses = h.tokenResponses[1:]
			return response, nil
		}
		return transport.Response{StatusCode: http.StatusOK, Body: []byte(`{"access_token":"synthetic-access-token","refresh_token":"synthetic-refresh-token","token_type":"bearer","scope":"read:user","expires_in":3600,"refresh_token_expires_in":7200}`)}, nil
	case "https://api.github.com/user":
		h.mu.Lock()
		h.userRequests++
		h.activeUsers++
		if h.activeUsers > h.maxActiveUsers {
			h.maxActiveUsers = h.activeUsers
		}
		entered := h.userEntered
		release := h.userRelease
		var response transport.Response
		var requestErr error
		if len(h.userErrors) > 0 {
			requestErr = h.userErrors[0]
			h.userErrors = h.userErrors[1:]
		}
		if len(h.userResponses) > 0 {
			response = h.userResponses[0]
			h.userResponses = h.userResponses[1:]
		} else {
			response = transport.Response{StatusCode: http.StatusOK, Body: []byte(`{"login":"synthetic-user","id":4242}`)}
		}
		h.mu.Unlock()
		if entered != nil {
			entered <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				requestErr = ctx.Err()
			}
		}
		h.mu.Lock()
		h.activeUsers--
		h.mu.Unlock()
		return response, requestErr
	default:
		return transport.Response{}, fmt.Errorf("unexpected synthetic request %s %s", request.Method, request.URL)
	}
}

func (h *oauthDeviceLifecycleHost) OpenStream(context.Context, string, transport.Request) (transport.Stream, error) {
	return transport.Stream{}, errors.New("unexpected synthetic stream request")
}

func (h *oauthDeviceLifecycleHost) ReadStream(context.Context, string) (transport.StreamChunk, error) {
	return transport.StreamChunk{}, errors.New("unexpected synthetic stream read")
}

func (h *oauthDeviceLifecycleHost) CloseStream(context.Context, string) error { return nil }

func (h *oauthDeviceLifecycleHost) Emit(context.Context, string, []byte) error { return nil }

func (h *oauthDeviceLifecycleHost) CloseOutput(context.Context, string, string) {}

func (h *oauthDeviceLifecycleHost) counts() (device, token, user int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.deviceRequests, h.tokenRequests, h.userRequests
}

func (h *oauthDeviceLifecycleHost) maxConcurrentUsers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.maxActiveUsers
}

func TestPollLoginRetriesRateLimitedAccountVerificationAtProviderTime(t *testing.T) {
	issuedAt := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	for _, test := range []struct {
		name       string
		status     int
		headers    http.Header
		body       string
		retryAfter time.Duration
	}{
		{
			name:       "retry after",
			headers:    http.Header{"Retry-After": []string{"30"}},
			body:       `{"message":"synthetic provider detail","private":"synthetic-response-marker"}`,
			retryAfter: 30 * time.Second,
		},
		{
			name: "primary reset",
			headers: http.Header{
				"X-Ratelimit-Remaining": []string{"0"},
				"X-Ratelimit-Reset":     []string{strconv.FormatInt(issuedAt.Add(45*time.Second).Unix(), 10)},
			},
			body:       `{"message":"synthetic provider detail"}`,
			retryAfter: 45 * time.Second,
		},
		{
			name:       "documented secondary limit response",
			body:       `{"message":"You have exceeded a secondary rate limit.","private":"synthetic-response-marker"}`,
			retryAfter: time.Minute,
		},
		{
			name:       "429 retry after",
			status:     http.StatusTooManyRequests,
			headers:    http.Header{"Retry-After": []string{"30"}},
			body:       `{"message":"synthetic provider detail"}`,
			retryAfter: 30 * time.Second,
		},
		{
			name:       "429 primary reset",
			status:     http.StatusTooManyRequests,
			headers:    http.Header{"X-Ratelimit-Remaining": []string{"0"}, "X-Ratelimit-Reset": []string{strconv.FormatInt(issuedAt.Add(45*time.Second).Unix(), 10)}},
			body:       `{"message":"synthetic provider detail"}`,
			retryAfter: 45 * time.Second,
		},
		{
			name:       "429 without timing",
			status:     http.StatusTooManyRequests,
			body:       `{"message":"synthetic provider detail"}`,
			retryAfter: time.Minute,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			status := test.status
			if status == 0 {
				status = http.StatusForbidden
			}
			now := issuedAt
			var clockMu sync.Mutex
			host := &oauthDeviceLifecycleHost{userResponses: []transport.Response{
				{StatusCode: status, Headers: test.headers, Body: []byte(test.body)},
				{StatusCode: http.StatusOK, Body: []byte(`{"login":"synthetic-user","id":4242}`)},
			}}
			service := New(host)
			service.now = func() time.Time {
				clockMu.Lock()
				defer clockMu.Unlock()
				return now
			}
			start, err := service.StartLogin(context.Background(), "synthetic-callback")
			if err != nil {
				t.Fatalf("StartLogin: %v", err)
			}

			first, err := service.PollLogin(context.Background(), "synthetic-callback", start.State)
			if err != nil || first.Status != pluginapi.AuthLoginStatusPending || first.Auth.StorageJSON != nil {
				t.Fatalf("rate limited poll = %#v, error=%v", first, err)
			}
			service.oauthMu.Lock()
			pending := service.oauthSession[start.State]
			if pending == nil || pending.PendingToken == nil || !pending.TokenIssuedAt.Equal(issuedAt) {
				service.oauthMu.Unlock()
				t.Fatal("rate limited account verification did not retain the original device token")
			}
			if want := issuedAt.Add(test.retryAfter); !pending.NextPoll.Equal(want) {
				service.oauthMu.Unlock()
				t.Fatalf("next account verification = %s, want provider retry time %s", pending.NextPoll, want)
			}
			service.oauthMu.Unlock()

			tooSoon, err := service.PollLogin(context.Background(), "synthetic-callback", start.State)
			if err != nil || tooSoon.Status != pluginapi.AuthLoginStatusPending || tooSoon.Auth.StorageJSON != nil {
				t.Fatalf("poll before provider retry time = %#v, error=%v", tooSoon, err)
			}
			_, tokenRequests, userRequests := host.counts()
			if tokenRequests != 1 || userRequests != 1 {
				t.Fatalf("early poll repeated upstream work: token=%d user=%d", tokenRequests, userRequests)
			}

			clockMu.Lock()
			now = issuedAt.Add(test.retryAfter)
			clockMu.Unlock()
			recovered, err := service.PollLogin(context.Background(), "synthetic-callback", start.State)
			if err != nil || recovered.Status != pluginapi.AuthLoginStatusSuccess {
				t.Fatalf("poll at provider retry time = %#v, error=%v", recovered, err)
			}
			storage, err := parseStorage(recovered.Auth.StorageJSON)
			if err != nil {
				t.Fatalf("parse verified credentials: %v", err)
			}
			if want := issuedAt.Add(time.Hour).Unix(); storage.ExpiresAt != want {
				t.Fatalf("access token expiry = %d, want original issuance expiry %d", storage.ExpiresAt, want)
			}
			if want := issuedAt.Add(2 * time.Hour).Unix(); storage.RefreshTokenExpiresAt != want {
				t.Fatalf("refresh token expiry = %d, want original issuance expiry %d", storage.RefreshTokenExpiresAt, want)
			}
			_, tokenRequests, userRequests = host.counts()
			if tokenRequests != 1 || userRequests != 2 {
				t.Fatalf("rate limit recovery exchanged device token again: token=%d user=%d", tokenRequests, userRequests)
			}
			service.oauthMu.Lock()
			_, retained := service.oauthSession[start.State]
			service.oauthMu.Unlock()
			if retained {
				t.Fatal("successful rate limit recovery retained the device session")
			}
		})
	}
}

func TestPollLoginEscalatesRepeatedRateLimitsUntilSessionExpiry(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
			host := &oauthDeviceLifecycleHost{userResponses: []transport.Response{
				{StatusCode: status, Body: []byte(`{"message":"You have exceeded a secondary rate limit."}`)},
				{StatusCode: status, Body: []byte(`{"message":"You have exceeded a secondary rate limit."}`)},
				{StatusCode: status, Body: []byte(`{"message":"You have exceeded a secondary rate limit."}`)},
			}}
			service := New(host)
			service.now = func() time.Time { return now }
			start, err := service.StartLogin(context.Background(), "synthetic-callback")
			if err != nil {
				t.Fatal(err)
			}
			for index, wantDelay := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute} {
				response, err := service.PollLogin(context.Background(), "synthetic-callback", start.State)
				if err != nil || response.Status != pluginapi.AuthLoginStatusPending || response.Auth.StorageJSON != nil {
					t.Fatalf("throttled poll %d = %#v, error=%v", index, response, err)
				}
				service.oauthMu.Lock()
				session := service.oauthSession[start.State]
				next := session.NextPoll
				if remaining := session.ExpiresAt.Sub(now); wantDelay > remaining {
					wantDelay = remaining
				}
				service.oauthMu.Unlock()
				if want := now.Add(wantDelay); !next.Equal(want) {
					t.Fatalf("poll %d retries at %s, want %s", index, next, want)
				}
				if early, err := service.PollLogin(context.Background(), "synthetic-callback", start.State); err != nil || early.Status != pluginapi.AuthLoginStatusPending {
					t.Fatalf("early poll %d = %#v, error=%v", index, early, err)
				}
				_, tokens, users := host.counts()
				if tokens != 1 || users != index+1 {
					t.Fatalf("poll %d repeated upstream work: tokens=%d users=%d", index, tokens, users)
				}
				now = next
			}
			response, err := service.PollLogin(context.Background(), "synthetic-callback", start.State)
			if err != nil || response.Status != pluginapi.AuthLoginStatusError || response.Auth.StorageJSON != nil {
				t.Fatalf("expired throttled login = %#v, error=%v", response, err)
			}
			_, tokens, users := host.counts()
			if tokens != 1 || users != 3 {
				t.Fatalf("expiry repeated upstream work: tokens=%d users=%d", tokens, users)
			}
			service.oauthMu.Lock()
			defer service.oauthMu.Unlock()
			if len(service.oauthSession) != 0 {
				t.Fatal("expiry retained throttled login state")
			}
		})
	}

}

func TestPollLoginKeepsPermanentUserDenialsTerminal(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"message":"synthetic unauthorized response"}`},
		{name: "forbidden without rate limit evidence", status: http.StatusForbidden, body: `{"message":"Resource not accessible by integration"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := &oauthDeviceLifecycleHost{userResponses: []transport.Response{{StatusCode: test.status, Body: []byte(test.body)}}}
			service := New(host)
			start, err := service.StartLogin(context.Background(), "synthetic-callback")
			if err != nil {
				t.Fatalf("StartLogin: %v", err)
			}
			poll, err := service.PollLogin(context.Background(), "synthetic-callback", start.State)
			if err == nil || poll.Status == pluginapi.AuthLoginStatusPending || poll.Auth.StorageJSON != nil {
				t.Fatalf("permanent denial poll = %#v, error=%v", poll, err)
			}
			if strings.Contains(err.Error(), "synthetic") {
				t.Fatal("permanent denial exposed provider response text")
			}
			service.oauthMu.Lock()
			_, retained := service.oauthSession[start.State]
			service.oauthMu.Unlock()
			if retained {
				t.Fatal("permanent denial retained the device session")
			}
			_, tokenRequests, userRequests := host.counts()
			if tokenRequests != 1 || userRequests != 1 {
				t.Fatalf("permanent denial caused extra upstream work: token=%d user=%d", tokenRequests, userRequests)
			}
		})
	}
}

func TestPollLoginSerializesBlockedAccountVerification(t *testing.T) {
	issuedAt := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	host := &oauthDeviceLifecycleHost{
		userEntered: make(chan struct{}, 1),
		userRelease: make(chan struct{}),
	}
	service := New(host)
	service.now = func() time.Time { return issuedAt }
	start, err := service.StartLogin(context.Background(), "synthetic-callback")
	if err != nil {
		t.Fatalf("StartLogin: %v", err)
	}

	firstResult := make(chan struct {
		poll pluginapi.AuthLoginPollResponse
		err  error
	}, 1)
	go func() {
		poll, err := service.PollLogin(context.Background(), "synthetic-callback", start.State)
		firstResult <- struct {
			poll pluginapi.AuthLoginPollResponse
			err  error
		}{poll: poll, err: err}
	}()
	select {
	case <-host.userEntered:
	case <-time.After(5 * time.Second):
		close(host.userRelease)
		t.Fatal("account verification did not reach the blocked /user request")
	}

	secondResult := make(chan struct {
		poll pluginapi.AuthLoginPollResponse
		err  error
	}, 1)
	go func() {
		poll, err := service.PollLogin(context.Background(), "synthetic-callback", start.State)
		secondResult <- struct {
			poll pluginapi.AuthLoginPollResponse
			err  error
		}{poll: poll, err: err}
	}()
	var second struct {
		poll pluginapi.AuthLoginPollResponse
		err  error
	}
	select {
	case second = <-secondResult:
	case <-time.After(5 * time.Second):
		close(host.userRelease)
		<-firstResult
		<-secondResult
		t.Fatal("overlapping poll waited on account verification instead of returning pending")
	}
	if second.err != nil || second.poll.Status != pluginapi.AuthLoginStatusPending || second.poll.Auth.StorageJSON != nil {
		close(host.userRelease)
		t.Fatalf("overlapping poll = %#v, error=%v", second.poll, second.err)
	}
	select {
	case premature := <-firstResult:
		close(host.userRelease)
		t.Fatalf("account verification returned before /user completed: %#v, error=%v", premature.poll, premature.err)
	default:
	}
	service.oauthMu.Lock()
	pending := service.oauthSession[start.State]
	if pending == nil || pending.PendingToken == nil || !pending.Polling || !pending.TokenIssuedAt.Equal(issuedAt) {
		service.oauthMu.Unlock()
		close(host.userRelease)
		t.Fatal("blocked account verification did not retain the issued token while polling")
	}
	service.oauthMu.Unlock()
	_, tokenRequests, userRequests := host.counts()
	if tokenRequests != 1 || userRequests != 1 || host.maxConcurrentUsers() != 1 {
		close(host.userRelease)
		t.Fatalf("overlapping poll started upstream work: token=%d user=%d max concurrent users=%d", tokenRequests, userRequests, host.maxConcurrentUsers())
	}

	close(host.userRelease)
	select {
	case result := <-firstResult:
		if result.err != nil || result.poll.Status != pluginapi.AuthLoginStatusSuccess || result.poll.Auth.StorageJSON == nil {
			t.Fatalf("blocked verification completion = %#v, error=%v", result.poll, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked account verification did not complete")
	}
	_, tokenRequests, userRequests = host.counts()
	if tokenRequests != 1 || userRequests != 1 || host.maxConcurrentUsers() != 1 {
		t.Fatalf("verification completion repeated upstream work: token=%d user=%d max concurrent users=%d", tokenRequests, userRequests, host.maxConcurrentUsers())
	}
	service.oauthMu.Lock()
	_, retained := service.oauthSession[start.State]
	service.oauthMu.Unlock()
	if retained {
		t.Fatal("completed blocked verification retained the device session")
	}
}

func TestPollLoginShutdownDuringBlockedAccountVerificationClearsState(t *testing.T) {
	host := &oauthDeviceLifecycleHost{
		userEntered: make(chan struct{}, 1),
		userRelease: make(chan struct{}),
	}
	service := New(host)
	start, err := service.StartLogin(context.Background(), "synthetic-callback")
	if err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	result := make(chan struct {
		poll pluginapi.AuthLoginPollResponse
		err  error
	}, 1)
	go func() {
		poll, err := service.PollLogin(context.Background(), "synthetic-callback", start.State)
		result <- struct {
			poll pluginapi.AuthLoginPollResponse
			err  error
		}{poll: poll, err: err}
	}()
	select {
	case <-host.userEntered:
	case <-time.After(5 * time.Second):
		close(host.userRelease)
		t.Fatal("account verification did not reach the blocked /user request")
	}

	service.Shutdown()
	service.oauthMu.Lock()
	remainingSessions := len(service.oauthSession)
	service.oauthMu.Unlock()
	if remainingSessions != 0 {
		close(host.userRelease)
		t.Fatalf("Shutdown retained %d OAuth sessions", remainingSessions)
	}
	close(host.userRelease)
	select {
	case completed := <-result:
		if completed.err != nil || completed.poll.Status != pluginapi.AuthLoginStatusError || completed.poll.Auth.StorageJSON != nil {
			t.Fatalf("poll after shutdown = %#v, error=%v", completed.poll, completed.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked account verification did not stop after shutdown")
	}
	_, tokenRequests, userRequests := host.counts()
	if tokenRequests != 1 || userRequests != 1 || host.maxConcurrentUsers() != 1 {
		t.Fatalf("shutdown verification repeated upstream work: token=%d user=%d max concurrent users=%d", tokenRequests, userRequests, host.maxConcurrentUsers())
	}
	service.oauthMu.Lock()
	remainingSessions = len(service.oauthSession)
	service.oauthMu.Unlock()
	if remainingSessions != 0 {
		t.Fatalf("in-flight verification recreated %d OAuth sessions after shutdown", remainingSessions)
	}
}

func TestPollLoginRetainsTokenUntilAccountVerificationRecovers(t *testing.T) {
	issuedAt := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	now := issuedAt
	var clockMu sync.Mutex
	host := &oauthDeviceLifecycleHost{userResponses: []transport.Response{
		{StatusCode: http.StatusServiceUnavailable, Body: []byte(`{"message":"synthetic temporary failure"}`)},
		{StatusCode: http.StatusOK, Body: []byte(`{"login":"synthetic-user","id":4242}`)},
	}}
	service := New(host)
	service.now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	start, err := service.StartLogin(context.Background(), "synthetic-callback")
	if err != nil {
		t.Fatalf("StartLogin: %v", err)
	}

	first, err := service.PollLogin(context.Background(), "synthetic-callback", start.State)
	if err != nil || first.Status != pluginapi.AuthLoginStatusPending || first.Auth.StorageJSON != nil {
		t.Fatalf("poll after transient verification failure = %#v, error=%v", first, err)
	}
	service.oauthMu.Lock()
	pending := service.oauthSession[start.State]
	if pending == nil || pending.PendingToken == nil || !pending.TokenIssuedAt.Equal(issuedAt) {
		service.oauthMu.Unlock()
		t.Fatal("successful token exchange was not retained with its original issuance time")
	}
	service.oauthMu.Unlock()

	tooSoon, err := service.PollLogin(context.Background(), "synthetic-callback", start.State)
	if err != nil || tooSoon.Status != pluginapi.AuthLoginStatusPending {
		t.Fatalf("poll before interval = %#v, error=%v", tooSoon, err)
	}
	_, tokenRequests, userRequests := host.counts()
	if tokenRequests != 1 || userRequests != 1 {
		t.Fatalf("early poll repeated upstream work: token=%d user=%d", tokenRequests, userRequests)
	}

	clockMu.Lock()
	now = issuedAt.Add(5 * time.Second)
	clockMu.Unlock()
	recovered, err := service.PollLogin(context.Background(), "synthetic-callback", start.State)
	if err != nil || recovered.Status != pluginapi.AuthLoginStatusSuccess {
		t.Fatalf("verification retry = %#v, error=%v", recovered, err)
	}
	storage, err := parseStorage(recovered.Auth.StorageJSON)
	if err != nil {
		t.Fatalf("parse verified credentials: %v", err)
	}
	if storage.GitHubLogin != "synthetic-user" || storage.GitHubUserID != 4242 {
		t.Fatalf("verified identity = %q/%d", storage.GitHubLogin, storage.GitHubUserID)
	}
	if want := issuedAt.Add(time.Hour).Unix(); storage.ExpiresAt != want {
		t.Fatalf("access token expiry = %d, want original issuance expiry %d", storage.ExpiresAt, want)
	}
	if want := issuedAt.Add(2 * time.Hour).Unix(); storage.RefreshTokenExpiresAt != want {
		t.Fatalf("refresh token expiry = %d, want original issuance expiry %d", storage.RefreshTokenExpiresAt, want)
	}
	if storage.UpdatedAt != issuedAt.Format(time.RFC3339) {
		t.Fatalf("credential timestamp = %q, want issuance timestamp %q", storage.UpdatedAt, issuedAt.Format(time.RFC3339))
	}
	if _, tokenRequests, userRequests = host.counts(); tokenRequests != 1 || userRequests != 2 {
		t.Fatalf("verification retry exchanged device token again: token=%d user=%d", tokenRequests, userRequests)
	}
	service.oauthMu.Lock()
	_, retained := service.oauthSession[start.State]
	service.oauthMu.Unlock()
	if retained {
		t.Fatal("successful verification retained the device session")
	}
}

func TestPollLoginExpiresPendingTokenAndClearsSession(t *testing.T) {
	issuedAt := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	now := issuedAt
	var clockMu sync.Mutex
	host := &oauthDeviceLifecycleHost{
		tokenResponses: []transport.Response{{StatusCode: http.StatusOK, Body: []byte(`{"access_token":"synthetic-access-token","refresh_token":"synthetic-refresh-token","expires_in":4}`)}},
		userErrors:     []error{errors.New("synthetic temporary network failure")},
	}
	service := New(host)
	service.now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	start, err := service.StartLogin(context.Background(), "synthetic-callback")
	if err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	first, err := service.PollLogin(context.Background(), "synthetic-callback", start.State)
	if err != nil || first.Status != pluginapi.AuthLoginStatusPending {
		t.Fatalf("first poll = %#v, error=%v", first, err)
	}
	clockMu.Lock()
	now = issuedAt.Add(5 * time.Second)
	clockMu.Unlock()
	expired, err := service.PollLogin(context.Background(), "synthetic-callback", start.State)
	if err != nil || expired.Status != pluginapi.AuthLoginStatusError {
		t.Fatalf("poll with expired access token = %#v, error=%v", expired, err)
	}
	service.oauthMu.Lock()
	_, retained := service.oauthSession[start.State]
	service.oauthMu.Unlock()
	if retained {
		t.Fatal("expired access token retained its device session")
	}
	_, tokenRequests, userRequests := host.counts()
	if tokenRequests != 1 || userRequests != 1 {
		t.Fatalf("expired token caused another upstream request: token=%d user=%d", tokenRequests, userRequests)
	}
}

func TestPollLoginExpiresDeviceSessionAndShutdownClearsPendingToken(t *testing.T) {
	now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	var clockMu sync.Mutex
	host := &oauthDeviceLifecycleHost{userErrors: []error{errors.New("synthetic temporary network failure")}}
	service := New(host)
	service.now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	start, err := service.StartLogin(context.Background(), "synthetic-callback")
	if err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	first, err := service.PollLogin(context.Background(), "synthetic-callback", start.State)
	if err != nil || first.Status != pluginapi.AuthLoginStatusPending {
		t.Fatalf("first poll = %#v, error=%v", first, err)
	}
	service.Shutdown()
	service.oauthMu.Lock()
	remaining := len(service.oauthSession)
	service.oauthMu.Unlock()
	if remaining != 0 {
		t.Fatalf("Shutdown retained %d OAuth sessions", remaining)
	}

	second, err := service.StartLogin(context.Background(), "synthetic-callback")
	if err != nil {
		t.Fatalf("second StartLogin: %v", err)
	}
	clockMu.Lock()
	now = second.ExpiresAt
	clockMu.Unlock()
	expired, err := service.PollLogin(context.Background(), "synthetic-callback", second.State)
	if err != nil || expired.Status != pluginapi.AuthLoginStatusError {
		t.Fatalf("poll after device expiry = %#v, error=%v", expired, err)
	}
	service.oauthMu.Lock()
	_, retained := service.oauthSession[second.State]
	service.oauthMu.Unlock()
	if retained {
		t.Fatal("expired device session was not cleared")
	}
}

func TestStartLoginPrunesExpiredSessionsWithoutEvictingActiveSessions(t *testing.T) {
	now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	host := &oauthDeviceLifecycleHost{}
	service := New(host)
	service.now = func() time.Time { return now }
	active := &deviceSession{DeviceCode: "synthetic-active", ExpiresAt: now.Add(time.Minute)}
	service.oauthMu.Lock()
	service.oauthSession["synthetic-active-state"] = active
	for index := 0; index < maxOAuthSessions-1; index++ {
		service.oauthSession[fmt.Sprintf("synthetic-expired-state-%d", index)] = &deviceSession{ExpiresAt: now.Add(-time.Second)}
	}
	service.oauthMu.Unlock()

	start, err := service.StartLogin(context.Background(), "synthetic-callback")
	if err != nil {
		t.Fatalf("StartLogin after pruning: %v", err)
	}
	service.oauthMu.Lock()
	defer service.oauthMu.Unlock()
	if service.oauthSession["synthetic-active-state"] != active {
		t.Fatal("pruning evicted an active OAuth session")
	}
	if got := len(service.oauthSession); got != 2 {
		t.Fatalf("OAuth session count after pruning and start = %d, want 2", got)
	}
	if service.oauthSession[start.State] == nil {
		t.Fatal("new OAuth session was not retained")
	}
}

func TestStartLoginBoundsConcurrentSessionsAndReturns429WhenFull(t *testing.T) {
	release := make(chan struct{})
	host := &oauthDeviceLifecycleHost{deviceEntered: make(chan struct{}, maxOAuthSessions), deviceRelease: release}
	service := New(host)
	responses := make(chan struct {
		start pluginapi.AuthLoginStartResponse
		err   error
	}, maxOAuthSessions+1)
	for index := 0; index < maxOAuthSessions+1; index++ {
		go func() {
			start, err := service.StartLogin(context.Background(), "synthetic-callback")
			responses <- struct {
				start pluginapi.AuthLoginStartResponse
				err   error
			}{start: start, err: err}
		}()
	}

	for index := 0; index < maxOAuthSessions; index++ {
		select {
		case <-host.deviceEntered:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("concurrent StartLogin calls did not fill the session bound")
		}
	}
	var saturated error
	select {
	case result := <-responses:
		saturated = result.err
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("saturated StartLogin did not return promptly")
	}
	var status *StatusError
	if !errors.As(saturated, &status) || status.HTTPStatus != http.StatusTooManyRequests || status.Message != "Too many active GitHub device login sessions" {
		close(release)
		t.Fatalf("saturated StartLogin error = %v, want static HTTP 429", saturated)
	}
	service.oauthMu.Lock()
	if got := len(service.oauthSession); got != maxOAuthSessions {
		service.oauthMu.Unlock()
		close(release)
		t.Fatalf("reserved OAuth sessions = %d, want %d", got, maxOAuthSessions)
	}
	existing := make(map[string]*deviceSession, len(service.oauthSession))
	for state, session := range service.oauthSession {
		existing[state] = session
	}
	service.oauthMu.Unlock()

	close(release)
	for index := 0; index < maxOAuthSessions; index++ {
		select {
		case result := <-responses:
			if result.err != nil || result.start.State == "" {
				t.Fatalf("accepted concurrent StartLogin failed: %#v, error=%v", result.start, result.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("accepted concurrent StartLogin did not finish")
		}
	}
	service.oauthMu.Lock()
	defer service.oauthMu.Unlock()
	if len(service.oauthSession) != maxOAuthSessions {
		t.Fatalf("completed OAuth sessions = %d, want %d", len(service.oauthSession), maxOAuthSessions)
	}
	for state, session := range existing {
		if service.oauthSession[state] != session {
			t.Fatal("concurrent saturation evicted an accepted session")
		}
	}
}
