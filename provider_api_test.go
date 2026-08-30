package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"
)

// quotaSnapshotJSON drives the management handler and decodes the quota route body.
func quotaSnapshotJSON(t *testing.T, host *fakeHost, query map[string][]string) quotaResponse {
	t.Helper()
	runtime := newRuntime(host)
	runtime.applyConfig(pluginConfig{CacheTTL: 30 * time.Minute, RequestTimeout: time.Second, MaxConcurrency: 4})
	resp := runtime.handleManagement(managementRequest{Method: http.MethodGet, Path: quotaRoute, Query: query})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, resp.Body)
	}
	var payload quotaResponse
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatalf("decode body %s: %v", resp.Body, err)
	}
	return payload
}

func accountByProvider(t *testing.T, snapshot quotaResponse, provider string) accountQuota {
	t.Helper()
	for _, account := range snapshot.Accounts {
		if account.Provider == provider {
			return account
		}
	}
	t.Fatalf("no %s account in %#v", provider, snapshot.Accounts)
	return accountQuota{}
}

func windowByID(t *testing.T, account accountQuota, id string) quotaWindow {
	t.Helper()
	for _, window := range account.Windows {
		if window.ID == id {
			return window
		}
	}
	t.Fatalf("no window %q in %#v", id, account.Windows)
	return quotaWindow{}
}

func TestClaudeQuotaReachesFetcherThroughManagementHandler(t *testing.T) {
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude", Email: "user@example.com"}).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withJSON(claudeQuotaURL, `{
			"five_hour": {"utilization": 40, "resets_at": "2026-07-27T12:00:00Z"},
			"seven_day": {"utilization": 10, "resets_at": "2026-08-01T00:00:00Z"}
		}`)

	snapshot := quotaSnapshotJSON(t, host, nil)

	account := accountByProvider(t, snapshot, "claude")
	if !account.Supported || account.Status != "available" {
		t.Fatalf("account = %#v", account)
	}
	if account.Error != nil {
		t.Fatalf("unexpected error: %#v", account.Error)
	}
	fiveHour := windowByID(t, account, "five_hour")
	if fiveHour.RemainingPercent == nil || *fiveHour.RemainingPercent != 60 {
		t.Fatalf("five_hour = %#v", fiveHour)
	}
	if fiveHour.ResetAt == nil || !fiveHour.ResetAt.Equal(time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("five_hour reset = %#v", fiveHour.ResetAt)
	}
	sevenDay := windowByID(t, account, "seven_day")
	if sevenDay.RemainingPercent == nil || *sevenDay.RemainingPercent != 90 {
		t.Fatalf("seven_day = %#v", sevenDay)
	}
	if host.requestCount(claudeQuotaURL) != 1 {
		t.Fatalf("claude requests = %d", host.requestCount(claudeQuotaURL))
	}
}

func TestProviderFailuresAreIsolatedPerAccount(t *testing.T) {
	cases := []struct {
		name           string
		program        func(*fakeHost)
		wantStatus     int
		wantErrorCode  string
		wantHasWindows bool
	}{
		{
			name:          "transport error",
			program:       func(f *fakeHost) { f.withTransportError(claudeQuotaURL, errors.New("dial tcp: refused")) },
			wantErrorCode: "quota_fetch_failed",
		},
		{
			name:          "rate limited",
			program:       func(f *fakeHost) { f.withRateLimit(claudeQuotaURL) },
			wantStatus:    http.StatusTooManyRequests,
			wantErrorCode: "quota_fetch_failed",
		},
		{
			name:          "unreadable credential",
			program:       func(f *fakeHost) { f.withCredentialError("claude-1", errors.New("permission denied")) },
			wantErrorCode: "auth_read_failed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host := newFakeHost().
				withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
				withEntry(hostAuthFileEntry{AuthIndex: "codex-1", Name: "codex.json", Provider: "codex"}).
				withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
				withCredential("codex-1", `{"access_token":"codex-token","account_id":"acct-1"}`).
				withJSON(codexQuotaURL, `{"rate_limit":{"primary_window":{"used_percent":25,"reset_at":1800000000}}}`)
			tc.program(host)

			snapshot := quotaSnapshotJSON(t, host, nil)

			claude := accountByProvider(t, snapshot, "claude")
			if claude.Status != "error" || claude.Error == nil {
				t.Fatalf("claude = %#v", claude)
			}
			if claude.Error.Code != tc.wantErrorCode {
				t.Fatalf("claude error = %#v", claude.Error)
			}
			if claude.Error.UpstreamStatus != tc.wantStatus {
				t.Fatalf("claude upstream status = %d, want %d", claude.Error.UpstreamStatus, tc.wantStatus)
			}

			codex := accountByProvider(t, snapshot, "codex")
			if codex.Status != "available" || codex.Error != nil {
				t.Fatalf("codex = %#v", codex)
			}
			primary := windowByID(t, codex, "primary")
			if primary.RemainingPercent == nil || *primary.RemainingPercent != 75 {
				t.Fatalf("codex primary = %#v", primary)
			}
		})
	}
}

func TestUnsupportedProviderStaysUnsupported(t *testing.T) {
	host := newFakeHost().withEntry(hostAuthFileEntry{AuthIndex: "x", Name: "mystery.json", Provider: "mystery"})

	account := accountByProvider(t, quotaSnapshotJSON(t, host, nil), "mystery")

	if account.Supported || account.Status != "unsupported" || account.Error != nil {
		t.Fatalf("account = %#v", account)
	}
}

func TestCopilotQuotaReachesFetcherThroughManagementHandler(t *testing.T) {
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "copilot-1", Name: "copilot.json", Provider: "copilot", Email: "user@example.com"}).
		withCredential("copilot-1", `{"access_token":"gho_test_token"}`).
		withJSON(copilotQuotaURL, `{
			"quota_snapshots": {
				"premium_interactions": {"entitlement": 1000, "remaining": 500, "percent_remaining": 50.0, "reset_date": "2026-09-01T00:00:00Z"},
				"chat": {"entitlement": 500, "remaining": 200, "percent_remaining": 40.0, "reset_date": "2026-09-01T00:00:00Z"},
				"completions": {"entitlement": 2000, "remaining": 1000, "percent_remaining": 50.0, "reset_date": "2026-09-01T00:00:00Z"}
			}
		}`)

	snapshot := quotaSnapshotJSON(t, host, nil)

	account := accountByProvider(t, snapshot, "copilot")
	if !account.Supported || account.Status != "available" {
		t.Fatalf("account = %#v", account)
	}
	if account.Error != nil {
		t.Fatalf("unexpected error: %#v", account.Error)
	}
	premium := windowByID(t, account, "premium_interactions")
	if premium.RemainingPercent == nil || *premium.RemainingPercent != 50.0 {
		t.Fatalf("premium_interactions remaining = %#v", premium.RemainingPercent)
	}
	if premium.ResetAt == nil || !premium.ResetAt.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("premium_interactions reset = %#v", premium.ResetAt)
	}
	chat := windowByID(t, account, "chat")
	if chat.RemainingPercent == nil || *chat.RemainingPercent != 40.0 {
		t.Fatalf("chat remaining = %#v", chat.RemainingPercent)
	}
	completions := windowByID(t, account, "completions")
	if completions.RemainingPercent == nil || *completions.RemainingPercent != 50.0 {
		t.Fatalf("completions remaining = %#v", completions.RemainingPercent)
	}
	if host.requestCount(copilotQuotaURL) != 1 {
		t.Fatalf("copilot requests = %d", host.requestCount(copilotQuotaURL))
	}
}

func TestCopilotFailureIsolatedFromOtherProviders(t *testing.T) {
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "copilot-1", Name: "copilot.json", Provider: "copilot"}).
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withCredential("copilot-1", `{"access_token":"gho_test_token"}`).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withJSON(claudeQuotaURL, `{
			"five_hour": {"utilization": 40, "resets_at": "2026-07-27T12:00:00Z"},
			"seven_day": {"utilization": 10, "resets_at": "2026-08-01T00:00:00Z"}
		}`).
		withTransportError(copilotQuotaURL, errors.New("dial tcp: connection refused"))

	snapshot := quotaSnapshotJSON(t, host, nil)

	copilot := accountByProvider(t, snapshot, "copilot")
	if copilot.Status != "error" || copilot.Error == nil {
		t.Fatalf("copilot = %#v", copilot)
	}
	if copilot.Error.Code != "quota_fetch_failed" {
		t.Fatalf("copilot error code = %s", copilot.Error.Code)
	}

	claude := accountByProvider(t, snapshot, "claude")
	if claude.Status != "available" || claude.Error != nil {
		t.Fatalf("claude = %#v", claude)
	}
	fiveHour := windowByID(t, claude, "five_hour")
	if fiveHour.RemainingPercent == nil || *fiveHour.RemainingPercent != 60 {
		t.Fatalf("claude five_hour = %#v", fiveHour)
	}
}

func TestCopilotRateLimited(t *testing.T) {
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "copilot-1", Name: "copilot.json", Provider: "copilot"}).
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withCredential("copilot-1", `{"access_token":"gho_test_token"}`).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withRateLimit(copilotQuotaURL).
		withJSON(claudeQuotaURL, `{
			"five_hour": {"utilization": 30, "resets_at": "2026-07-27T12:00:00Z"},
			"seven_day": {"utilization": 10, "resets_at": "2026-08-01T00:00:00Z"}
		}`)

	snapshot := quotaSnapshotJSON(t, host, nil)

	copilot := accountByProvider(t, snapshot, "copilot")
	if copilot.Status != "error" || copilot.Error == nil {
		t.Fatalf("copilot = %#v", copilot)
	}
	if copilot.Error.Code != "quota_fetch_failed" {
		t.Fatalf("copilot error code = %s", copilot.Error.Code)
	}
	if copilot.Error.UpstreamStatus != http.StatusTooManyRequests {
		t.Fatalf("copilot upstream status = %d, want %d", copilot.Error.UpstreamStatus, http.StatusTooManyRequests)
	}

	claude := accountByProvider(t, snapshot, "claude")
	if claude.Status != "available" || claude.Error != nil {
		t.Fatalf("claude = %#v", claude)
	}
}
