package provider

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
)

func TestQuotaViewFromUserKeepsIncludedLanesOnly(t *testing.T) {
	t.Parallel()

	hasQuota := false
	view := quotaViewFromUser(copilotUserResponse{
		CopilotPlan:       "pro",
		QuotaResetDateUTC: "2026-11-01T00:00:00.000Z",
		QuotaSnapshots: map[string]quotaSnapshot{
			"premium_interactions": {Entitlement: 300, QuotaRemaining: 93, PercentRemaining: 31, HasQuota: boolPtr(true)},
			"chat":                 {Unlimited: true, PercentRemaining: 100, HasQuota: boolPtr(true)},
			"completions":          {HasQuota: &hasQuota, PercentRemaining: 0},
		},
	})

	if view.Subscription == nil || view.Subscription.Plan != "pro" {
		t.Fatalf("plan = %#v, want pro", view.Subscription)
	}
	if len(view.Groups) != 1 || len(view.Groups[0].Buckets) != 1 {
		t.Fatalf("groups = %#v, want one premium bucket", view.Groups)
	}
	bucket := view.Groups[0].Buckets[0]
	if bucket.Window != "Premium requests" || bucket.RemainingFraction < 0.309 || bucket.RemainingFraction > 0.311 {
		t.Fatalf("bucket = %#v, want premium requests at about 0.31 remaining", bucket)
	}
	if bucket.ResetTime != "2026-11-01T00:00:00Z" {
		t.Fatalf("reset = %q, want RFC3339 UTC", bucket.ResetTime)
	}
	if bucket.Description != "93 of 300 remaining" {
		t.Fatalf("description = %q", bucket.Description)
	}
}

func TestQuotaViewFromUserShowsExhaustedAllowance(t *testing.T) {
	t.Parallel()

	view := quotaViewFromUser(copilotUserResponse{
		CopilotPlan: "pro",
		QuotaSnapshots: map[string]quotaSnapshot{
			"premium_interactions": {Entitlement: 300, HasQuota: boolPtr(false), PercentRemaining: 0},
		},
	})
	if len(view.Groups) != 1 || len(view.Groups[0].Buckets) != 1 {
		t.Fatalf("groups = %#v, want the exhausted premium meter", view.Groups)
	}
	bucket := view.Groups[0].Buckets[0]
	if bucket.Window != "Premium requests" || bucket.RemainingFraction != 0 || bucket.Description != "0 of 300 remaining" {
		t.Fatalf("bucket = %#v", bucket)
	}
}

func boolPtr(value bool) *bool { return &value }

func TestQuotaViewFromUserUsesDateOnlyReset(t *testing.T) {
	t.Parallel()

	view := quotaViewFromUser(copilotUserResponse{
		QuotaResetDate: "2026-11-01",
		QuotaSnapshots: map[string]quotaSnapshot{
			"chat": {Entitlement: 200, Remaining: 0, PercentRemaining: 0},
		},
	})
	bucket := view.Groups[0].Buckets[0]
	if bucket.Window != "Chat" || bucket.RemainingFraction != 0 || bucket.ResetTime != "2026-11-01T00:00:00Z" {
		t.Fatalf("bucket = %#v", bucket)
	}
}

func TestFetchQuotaUsesGitHubTokenAgainstInternalUser(t *testing.T) {
	t.Parallel()

	host := &recordingHost{status: http.StatusOK, body: []byte(`{
		"copilot_plan": "individual",
		"quota_reset_date": "2026-11-01",
		"quota_snapshots": {"completions": {"entitlement": 2000, "remaining": 1500, "percent_remaining": 75}}
	}`)}
	service := New(host)
	view, errFetch := service.FetchQuota(context.Background(), "callback-1", []byte(`{
		"type": "copilot",
		"github_access_token": "github-token",
		"github_login": "octocat"
	}`))
	if errFetch != nil {
		t.Fatalf("FetchQuota: %v", errFetch)
	}
	if host.method != http.MethodGet || !strings.HasSuffix(host.url, "/copilot_internal/user") {
		t.Fatalf("request = %s %s", host.method, host.url)
	}
	if got := host.headers.Get("Authorization"); got != "token github-token" {
		t.Fatalf("Authorization = %q", got)
	}
	if len(view.Groups) != 1 || view.Groups[0].Buckets[0].Window != "Completions" {
		t.Fatalf("view = %#v", view)
	}
}

func TestFetchQuotaReportsUpstreamStatus(t *testing.T) {
	t.Parallel()

	service := New(&recordingHost{status: http.StatusUnauthorized, body: []byte(`{"message":"bad credentials"}`)})
	_, errFetch := service.FetchQuota(context.Background(), "", []byte(`{
		"type": "copilot",
		"github_access_token": "github-token",
		"github_login": "octocat"
	}`))
	statusErr, ok := errFetch.(*StatusError)
	if !ok || statusErr.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("error = %#v, want HTTP 401", errFetch)
	}
	if strings.Contains(statusErr.Message, "github-token") {
		t.Fatalf("error leaked the token: %s", statusErr.Message)
	}
}

type recordingHost struct {
	status  int
	body    []byte
	method  string
	url     string
	headers http.Header
}

func (h *recordingHost) Do(_ context.Context, _ string, req transport.Request) (transport.Response, error) {
	h.method = req.Method
	h.url = req.URL
	h.headers = req.Headers.Clone()
	return transport.Response{StatusCode: h.status, Body: append([]byte(nil), h.body...)}, nil
}

func (h *recordingHost) OpenStream(context.Context, string, transport.Request) (transport.Stream, error) {
	return transport.Stream{}, nil
}

func (h *recordingHost) ReadStream(context.Context, string) (transport.StreamChunk, error) {
	return transport.StreamChunk{Done: true}, nil
}

func (h *recordingHost) CloseStream(context.Context, string) error { return nil }

func (h *recordingHost) Emit(context.Context, string, []byte) error { return nil }

func (h *recordingHost) CloseOutput(context.Context, string, string) {}
