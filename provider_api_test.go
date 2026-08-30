package main

import (
	"bytes"
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

// --- Issue #5: Claude scoped weeklies, extra usage, binding window, dead keys ---

const claudeFullPayload = `{
	"five_hour": {"utilization": 40, "resets_at": "2026-07-27T12:00:00Z"},
	"seven_day": {"utilization": 10, "resets_at": "2026-08-01T00:00:00Z"},
	"limits": [
		{"kind": "session", "is_active": true, "percent": 40, "resets_at": "2026-07-27T12:00:00Z"},
		{"kind": "weekly_scoped", "group": "weekly", "percent": 75, "resets_at": "2026-07-30T00:00:00Z", "scope": {"model": {"display_name": "Claude Sonnet", "id": "claude-3-5-sonnet"}}},
		{"kind": "weekly_scoped", "group": "weekly", "percent": 20, "resets_at": "2026-07-28T00:00:00Z", "scope": {"model": {"display_name": "Claude Opus", "id": "claude-3-opus"}}},
		{"kind": "weekly_all", "is_active": false, "percent": 10, "resets_at": "2026-08-01T00:00:00Z"}
	],
	"extra_usage": {"is_enabled": true, "used_credits": 500, "monthly_limit": 5000}
}`

func modelByID(t *testing.T, account accountQuota, modelID string) modelQuota {
	t.Helper()
	for _, m := range account.Models {
		if m.Model == modelID {
			return m
		}
	}
	t.Fatalf("no model %q in %#v", modelID, account.Models)
	return modelQuota{}
}

func TestClaudeScopedWeeklyLimitsSurfacedAsModels(t *testing.T) {
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withJSON(claudeQuotaURL, claudeFullPayload)

	account := accountByProvider(t, quotaSnapshotJSON(t, host, nil), "claude")

	if !account.Supported || account.Status != "available" {
		t.Fatalf("account = %#v", account)
	}

	// Two scoped weekly limits should appear as model quotas.
	if len(account.Models) != 2 {
		t.Fatalf("models = %d, want 2: %#v", len(account.Models), account.Models)
	}

	sonnet := modelByID(t, account, "claude-weekly-scoped-claude-3-5-sonnet")
	if sonnet.ModelName != "Claude Sonnet" {
		t.Fatalf("sonnet ModelName = %q", sonnet.ModelName)
	}
	if sonnet.RemainingPercent == nil || *sonnet.RemainingPercent != 25 {
		t.Fatalf("sonnet RemainingPercent = %#v", sonnet.RemainingPercent)
	}

	opus := modelByID(t, account, "claude-weekly-scoped-claude-3-opus")
	if opus.ModelName != "Claude Opus" {
		t.Fatalf("opus ModelName = %q", opus.ModelName)
	}
	if opus.RemainingPercent == nil || *opus.RemainingPercent != 80 {
		t.Fatalf("opus RemainingPercent = %#v", opus.RemainingPercent)
	}
}

func TestClaudeExtraUsageReportedAgainstMonthlyLimit(t *testing.T) {
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withJSON(claudeQuotaURL, claudeFullPayload)

	account := accountByProvider(t, quotaSnapshotJSON(t, host, nil), "claude")

	extra := windowByID(t, account, "extra")
	if extra.UsedPercent == nil || *extra.UsedPercent != 10 {
		t.Fatalf("extra UsedPercent = %#v", extra.UsedPercent)
	}
	if extra.RemainingPercent == nil || *extra.RemainingPercent != 90 {
		t.Fatalf("extra RemainingPercent = %#v", extra.RemainingPercent)
	}
	if account.ExtraUsedCredits == nil || *account.ExtraUsedCredits != 500 {
		t.Fatalf("ExtraUsedCredits = %#v", account.ExtraUsedCredits)
	}
	if account.ExtraMonthlyLimit == nil || *account.ExtraMonthlyLimit != 5000 {
		t.Fatalf("ExtraMonthlyLimit = %#v", account.ExtraMonthlyLimit)
	}
}

func TestClaudeBindingWindowFromAPI(t *testing.T) {
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withJSON(claudeQuotaURL, claudeFullPayload)

	account := accountByProvider(t, quotaSnapshotJSON(t, host, nil), "claude")

	// The payload has is_active=true on kind=session, so binding window = five_hour.
	if account.BindingWindow == nil || account.BindingWindow.ID != "five_hour" {
		t.Fatalf("binding_window = %#v", account.BindingWindow)
	}
}

func TestClaudeBindingWindowWeeklyScoped(t *testing.T) {
	payload := `{
		"five_hour": {"utilization": 40, "resets_at": "2026-07-27T12:00:00Z"},
		"limits": [
			{"kind": "session", "is_active": false, "percent": 40},
			{"kind": "weekly_scoped", "group": "weekly", "is_active": true, "percent": 75, "resets_at": "2026-07-30T00:00:00Z", "scope": {"model": {"display_name": "Claude Sonnet", "id": "claude-3-5-sonnet"}}}
		]
	}`
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withJSON(claudeQuotaURL, payload)

	account := accountByProvider(t, quotaSnapshotJSON(t, host, nil), "claude")

	if account.BindingWindow == nil || account.BindingWindow.ID != "claude-weekly-scoped-claude-3-5-sonnet" {
		t.Fatalf("binding_window = %#v", account.BindingWindow)
	}
}

func TestClaudeDeadWindowKeysIgnored(t *testing.T) {
	// Payload with the five dead keys present alongside the real ones.
	// Output must contain only five_hour and seven_day windows.
	deadPayload := `{
		"five_hour": {"utilization": 30, "resets_at": "2026-07-27T12:00:00Z"},
		"seven_day": {"utilization": 5, "resets_at": "2026-08-01T00:00:00Z"},
		"seven_day_oauth_apps": {"utilization": 50, "resets_at": "2026-08-01T00:00:00Z"},
		"seven_day_opus": {"utilization": 60, "resets_at": "2026-08-01T00:00:00Z"},
		"seven_day_sonnet": {"utilization": 70, "resets_at": "2026-08-01T00:00:00Z"},
		"seven_day_cowork": {"utilization": 80, "resets_at": "2026-08-01T00:00:00Z"},
		"iguana_necktie": {"utilization": 90, "resets_at": "2026-08-01T00:00:00Z"}
	}`

	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withJSON(claudeQuotaURL, deadPayload)

	account := accountByProvider(t, quotaSnapshotJSON(t, host, nil), "claude")

	// Only the two real windows should be present.
	if len(account.Windows) != 2 {
		t.Fatalf("windows = %d, want 2: %#v", len(account.Windows), account.Windows)
	}
	fiveHour := windowByID(t, account, "five_hour")
	if fiveHour.RemainingPercent == nil || *fiveHour.RemainingPercent != 70 {
		t.Fatalf("five_hour = %#v", fiveHour)
	}
	sevenDay := windowByID(t, account, "seven_day")
	if sevenDay.RemainingPercent == nil || *sevenDay.RemainingPercent != 95 {
		t.Fatalf("seven_day = %#v", sevenDay)
	}
}

func TestClaudeExistingWindowsStillReportCorrectly(t *testing.T) {
	// Regression guard: five_hour and seven_day still work after the new parsing.
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withJSON(claudeQuotaURL, `{
			"five_hour": {"utilization": 50, "resets_at": "2026-07-27T12:00:00Z"},
			"seven_day": {"utilization": 30, "resets_at": "2026-08-01T00:00:00Z"}
		}`)

	account := accountByProvider(t, quotaSnapshotJSON(t, host, nil), "claude")

	fiveHour := windowByID(t, account, "five_hour")
	if fiveHour.RemainingPercent == nil || *fiveHour.RemainingPercent != 50 {
		t.Fatalf("five_hour = %#v", fiveHour)
	}
	sevenDay := windowByID(t, account, "seven_day")
	if sevenDay.RemainingPercent == nil || *sevenDay.RemainingPercent != 70 {
		t.Fatalf("seven_day = %#v", sevenDay)
	}
}

func TestClaudeMissingTokenReportsPerAccountError(t *testing.T) {
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withCredential("claude-1", `{}`)
	snapshot := quotaSnapshotJSON(t, host, nil)
	claude := accountByProvider(t, snapshot, "claude")

	if claude.Error == nil || claude.Error.Code != "credential_incomplete" {
		t.Fatalf("claude error = %#v", claude.Error)
	}
	if !claude.Supported {
		t.Fatalf("supported should be true even on credential error")
	}
}

func TestClaudeScopedLimitsMixedWithOtherProviders(t *testing.T) {
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withJSON(claudeQuotaURL, claudeFullPayload).
		withEntry(hostAuthFileEntry{AuthIndex: "codex-1", Name: "codex.json", Provider: "codex"}).
		withCredential("codex-1", `{"access_token":"codex-token","account_id":"acct-1"}`).
		withJSON(codexQuotaURL, `{"rate_limit":{"primary_window":{"used_percent":25,"reset_at":1800000000}}}`)

	snapshot := quotaSnapshotJSON(t, host, nil)

	claude := accountByProvider(t, snapshot, "claude")
	if len(claude.Models) != 2 {
		t.Fatalf("claude models = %d, want 2", len(claude.Models))
	}
	if claude.BindingWindow == nil {
		t.Fatalf("binding_window = nil")
	}
	if accountByProvider(t, snapshot, "codex").Error != nil {
		t.Fatalf("codex should have no error")
	}
}

func TestClaudeExtraUsageDisabled(t *testing.T) {
	payload := `{
		"five_hour": {"utilization": 40, "resets_at": "2026-07-27T12:00:00Z"},
		"extra_usage": {"is_enabled": false, "used_credits": 500, "monthly_limit": 5000}
	}`
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withJSON(claudeQuotaURL, payload)

	account := accountByProvider(t, quotaSnapshotJSON(t, host, nil), "claude")

	// extra window should NOT appear when is_enabled is false.
	for _, w := range account.Windows {
		if w.ID == "extra" {
			t.Fatalf("extra window present when disabled: %#v", account.Windows)
		}
	}
	if account.ExtraUsedCredits != nil {
		t.Fatalf("ExtraUsedCredits = %v, want nil", account.ExtraUsedCredits)
	}
}

func TestClaudeNoLimitsArray(t *testing.T) {
	// Payload with no limits array and no extra_usage — should still work.
	payload := `{
		"five_hour": {"utilization": 40, "resets_at": "2026-07-27T12:00:00Z"},
		"seven_day": {"utilization": 10, "resets_at": "2026-08-01T00:00:00Z"}
	}`
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withJSON(claudeQuotaURL, payload)

	account := accountByProvider(t, quotaSnapshotJSON(t, host, nil), "claude")

	if len(account.Windows) != 2 {
		t.Fatalf("windows = %d, want 2: %#v", len(account.Windows), account.Windows)
	}
	if len(account.Models) != 0 {
		t.Fatalf("models = %d, want 0", len(account.Models))
	}
	if account.BindingWindow != nil {
		t.Fatalf("binding_window = %v, want nil", account.BindingWindow)
	}
}

func TestClaudeScopedLimitMissingDisplayNameSkipped(t *testing.T) {
	payload := `{
		"five_hour": {"utilization": 40, "resets_at": "2026-07-27T12:00:00Z"},
		"limits": [
			{"kind": "weekly_scoped", "group": "weekly", "percent": 75, "scope": {"model": {"id": "claude-3-5-sonnet"}}}
		]
	}`
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withJSON(claudeQuotaURL, payload)

	account := accountByProvider(t, quotaSnapshotJSON(t, host, nil), "claude")

	if len(account.Models) != 0 {
		t.Fatalf("models = %d, want 0 (display_name missing): %#v", len(account.Models), account.Models)
	}
}

func TestClaudeScopedLimitDuplicateModelDeduped(t *testing.T) {
	payload := `{
		"five_hour": {"utilization": 40, "resets_at": "2026-07-27T12:00:00Z"},
		"limits": [
			{"kind": "weekly_scoped", "group": "weekly", "percent": 50, "scope": {"model": {"display_name": "Claude Sonnet", "id": "claude-3-5-sonnet"}}},
			{"kind": "weekly_scoped", "group": "weekly", "percent": 60, "scope": {"model": {"display_name": "Claude Sonnet", "id": "claude-3-5-sonnet"}}}
		]
	}`
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withJSON(claudeQuotaURL, payload)

	account := accountByProvider(t, quotaSnapshotJSON(t, host, nil), "claude")

	if len(account.Models) != 1 {
		t.Fatalf("models = %d, want 1 (dedup): %#v", len(account.Models), account.Models)
	}
}

func TestClaudeExtraUsageZeroLimitSkipped(t *testing.T) {
	payload := `{
		"five_hour": {"utilization": 40, "resets_at": "2026-07-27T12:00:00Z"},
		"extra_usage": {"is_enabled": true, "used_credits": 100, "monthly_limit": 0}
	}`
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withJSON(claudeQuotaURL, payload)

	account := accountByProvider(t, quotaSnapshotJSON(t, host, nil), "claude")

	for _, w := range account.Windows {
		if w.ID == "extra" {
			t.Fatalf("extra window present when monthly_limit is 0: %#v", account.Windows)
		}
	}
}

func TestSnapshotJSONPreservesNewFields(t *testing.T) {
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withJSON(claudeQuotaURL, claudeFullPayload)

	snapshot := quotaSnapshotJSON(t, host, nil)

	// Verify the raw JSON contains the new fields.
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"binding_window"`, `"extra_used_credits"`, `"extra_monthly_limit"`, `"model_name"`} {
		if !bytes.Contains(raw, []byte(key)) {
			t.Fatalf("raw snapshot missing %s: %s", key, raw)
		}
	}
}
