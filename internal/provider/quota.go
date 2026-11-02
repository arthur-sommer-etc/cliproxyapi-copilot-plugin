package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/redact"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/transport"
)

// quotaView is the normalized shape the v8 management UI renders. Field names
// match CLIProxyAPI v8 pluginapi.QuotaFetchResponse; the v7 SDK this plugin
// compiles against does not declare the quota provider yet.
type quotaView struct {
	Subscription *quotaSubscription `json:"subscription,omitempty"`
	Groups       []quotaGroup       `json:"groups,omitempty"`
}

type quotaSubscription struct {
	Plan string `json:"plan,omitempty"`
}

type quotaGroup struct {
	DisplayName string        `json:"displayName,omitempty"`
	Buckets     []quotaBucket `json:"buckets,omitempty"`
}

type quotaBucket struct {
	Window            string  `json:"window,omitempty"`
	RemainingFraction float64 `json:"remainingFraction"`
	ResetTime         string  `json:"resetTime,omitempty"`
	Description       string  `json:"description,omitempty"`
}

type quotaDescribe struct {
	SupportedProviders []string `json:"supported_providers,omitempty"`
	DisplayName        string   `json:"display_name,omitempty"`
	SupportsReset      bool     `json:"supports_reset,omitempty"`
}

type copilotUserResponse struct {
	CopilotPlan       string                   `json:"copilot_plan"`
	QuotaResetDate    string                   `json:"quota_reset_date"`
	QuotaResetDateUTC string                   `json:"quota_reset_date_utc"`
	QuotaSnapshots    map[string]quotaSnapshot `json:"quota_snapshots"`
}

type quotaSnapshot struct {
	Entitlement      float64 `json:"entitlement"`
	Remaining        float64 `json:"remaining"`
	QuotaRemaining   float64 `json:"quota_remaining"`
	PercentRemaining float64 `json:"percent_remaining"`
	Unlimited        bool    `json:"unlimited"`
	HasQuota         *bool   `json:"has_quota"`
}

// quotaLanes is the order the management UI should show the meters GitHub
// returns. A lane the plan does not include is dropped rather than drawn empty.
var quotaLanes = []struct {
	key   string
	label string
}{
	{"premium_interactions", "Premium requests"},
	{"chat", "Chat"},
	{"completions", "Completions"},
}

func (s *Service) DescribeQuota() quotaDescribe {
	return quotaDescribe{
		SupportedProviders: []string{providerID},
		DisplayName:        "GitHub Copilot",
		SupportsReset:      false,
	}
}

// FetchQuota reads the account's remaining Copilot allowance from GitHub's
// internal user endpoint, the same one editor plugins use for the status badge.
// GitHub does not offer a reset, so Reset is intentionally unsupported.
func (s *Service) FetchQuota(ctx context.Context, callbackID string, storageJSON []byte) (quotaView, error) {
	storage, errParse := parseStorage(storageJSON)
	if errParse != nil {
		return quotaView{}, errParse
	}
	cfg := s.Config()
	resp, errDo := s.host.Do(ctx, callbackID, transport.Request{
		Method: http.MethodGet,
		URL:    cfg.GitHubAPIURL + "/copilot_internal/user",
		Headers: http.Header{
			"Accept":                []string{"application/json"},
			"Authorization":         []string{"token " + storage.GitHubAccessToken},
			"Editor-Plugin-Version": []string{copilotPluginVersion},
			"Editor-Version":        []string{copilotEditorVersion},
			"User-Agent":            []string{copilotUserAgent},
			"X-GitHub-Api-Version":  []string{copilotAPIVersion},
		},
	})
	if errDo != nil {
		return quotaView{}, fmt.Errorf("fetch Copilot quota: %w", errDo)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return quotaView{}, upstreamStatusError(resp.StatusCode, redact.ErrorBody(resp.Body, storage.GitHubAccessToken))
	}
	var user copilotUserResponse
	if errUnmarshal := json.Unmarshal(resp.Body, &user); errUnmarshal != nil {
		return quotaView{}, fmt.Errorf("decode Copilot quota response: %w", errUnmarshal)
	}
	return quotaViewFromUser(user), nil
}

func quotaViewFromUser(user copilotUserResponse) quotaView {
	reset := strings.TrimSpace(user.QuotaResetDateUTC)
	if reset == "" {
		reset = strings.TrimSpace(user.QuotaResetDate)
	}
	if reset != "" {
		if parsed, errParse := time.Parse(time.RFC3339, reset); errParse == nil {
			reset = parsed.UTC().Format(time.RFC3339)
		} else if parsed, errDate := time.Parse("2006-01-02", reset); errDate == nil {
			reset = parsed.UTC().Format(time.RFC3339)
		}
	}
	buckets := make([]quotaBucket, 0, len(quotaLanes))
	for _, lane := range quotaLanes {
		snapshot, ok := user.QuotaSnapshots[lane.key]
		if !ok || !snapshot.included() {
			continue
		}
		fraction := snapshot.remainingFraction()
		if fraction < 0 {
			continue
		}
		buckets = append(buckets, quotaBucket{
			Window:            lane.label,
			RemainingFraction: fraction,
			ResetTime:         reset,
			Description:       snapshot.description(),
		})
	}
	view := quotaView{}
	if plan := strings.TrimSpace(user.CopilotPlan); plan != "" {
		view.Subscription = &quotaSubscription{Plan: plan}
	}
	if len(buckets) > 0 {
		view.Groups = []quotaGroup{{DisplayName: "Copilot", Buckets: buckets}}
	}
	return view
}

// included reports whether the plan actually grants this lane. An unlimited
// lane is not a meter. A lane with has_quota false and nothing issued is a
// placeholder. The same flag with an issued allowance means the allowance is
// spent, which is exactly what the usage card needs to show.
func (s quotaSnapshot) included() bool {
	if s.Unlimited {
		return false
	}
	if s.Entitlement > 0 || s.QuotaRemaining > 0 || s.Remaining > 0 {
		return true
	}
	if s.HasQuota != nil && !*s.HasQuota {
		return false
	}
	if s.PercentRemaining >= 100 {
		return false
	}
	return true
}

func (s quotaSnapshot) remainingFraction() float64 {
	var fraction float64
	switch {
	case s.PercentRemaining > 0 || (s.PercentRemaining == 0 && (s.Entitlement > 0 || s.QuotaRemaining > 0 || s.Remaining > 0)):
		fraction = s.PercentRemaining / 100
	case s.Entitlement > 0:
		fraction = s.remainingAmount() / s.Entitlement
	default:
		return -1
	}
	if math.IsNaN(fraction) || math.IsInf(fraction, 0) {
		return -1
	}
	if fraction < 0 {
		return 0
	}
	if fraction > 1 {
		return 1
	}
	return fraction
}

func (s quotaSnapshot) remainingAmount() float64 {
	if s.QuotaRemaining > 0 {
		return s.QuotaRemaining
	}
	if s.Remaining > 0 {
		return s.Remaining
	}
	if s.PercentRemaining > 0 && s.Entitlement > 0 {
		return s.Entitlement * s.PercentRemaining / 100
	}
	return 0
}

func (s quotaSnapshot) description() string {
	if s.Entitlement <= 0 {
		return ""
	}
	return fmt.Sprintf("%.0f of %.0f remaining", s.remainingAmount(), s.Entitlement)
}
