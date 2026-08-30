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

// --- Issue #6: Copilot quota adapter ---

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

// --- Issue #7: OpenCode Go quota adapter ---

// openCodeGoEntry is the auth list entry the auth-parser plugin produces: an
// openai-compatibility auth whose ID/file name identify OpenCode Go.
func openCodeGoEntry() hostAuthFileEntry {
	return hostAuthFileEntry{
		AuthIndex: "opencode-go-1",
		ID:        "opencode-go",
		Name:      "opencode-go.json",
		Label:     "OpenCode Go",
		Provider:  "openai-compatibility",
		Type:      "openai-compatibility",
	}
}

const openCodeGoUsagePayload = `{
	"usage": {
		"rolling": {"status": "ok", "percent": 4, "resetsAt": "2026-08-13T16:27:38Z"},
		"weekly":  {"status": "ok", "percent": 30, "resetsAt": "2026-08-17T00:00:00Z"},
		"monthly": {"status": "ok", "percent": 25, "resetsAt": "2026-09-13T06:06:01Z"}
	}
}`

func TestOpenCodeGoQuotaReachesFetcherThroughManagementHandler(t *testing.T) {
	host := newFakeHost().
		withEntry(openCodeGoEntry()).
		withCredential("opencode-go-1", `{"type":"opencode-go","api_key":"oc_test_key","base_url":"https://opencode.ai/zen/v1"}`).
		withJSON(openCodeGoQuotaURL, openCodeGoUsagePayload)

	snapshot := quotaSnapshotJSON(t, host, nil)

	account := accountByProvider(t, snapshot, "opencode-go")
	if !account.Supported || account.Status != "available" || account.Error != nil {
		t.Fatalf("account = %#v", account)
	}
	if len(account.Windows) != 3 {
		t.Fatalf("windows = %#v", account.Windows)
	}
	cases := []struct {
		id        string
		used      float64
		remaining float64
		dollars   float64
		limit     float64
		reset     time.Time
	}{
		{"rolling", 4, 96, 0.48, 12, time.Date(2026, 8, 13, 16, 27, 38, 0, time.UTC)},
		{"weekly", 30, 70, 9, 30, time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)},
		{"monthly", 25, 75, 15, 60, time.Date(2026, 9, 13, 6, 6, 1, 0, time.UTC)},
	}
	for _, tc := range cases {
		window := windowByID(t, account, tc.id)
		if window.UsedPercent == nil || *window.UsedPercent != tc.used {
			t.Fatalf("%s used_percent = %#v, want %v", tc.id, window.UsedPercent, tc.used)
		}
		if window.RemainingPercent == nil || *window.RemainingPercent != tc.remaining {
			t.Fatalf("%s remaining_percent = %#v, want %v", tc.id, window.RemainingPercent, tc.remaining)
		}
		if window.UsedDollars == nil || *window.UsedDollars != tc.dollars {
			t.Fatalf("%s used_dollars = %#v, want %v", tc.id, window.UsedDollars, tc.dollars)
		}
		if window.LimitDollars == nil || *window.LimitDollars != tc.limit {
			t.Fatalf("%s limit_dollars = %#v, want %v", tc.id, window.LimitDollars, tc.limit)
		}
		if window.ResetAt == nil || !window.ResetAt.Equal(tc.reset) {
			t.Fatalf("%s reset_at = %#v, want %v", tc.id, window.ResetAt, tc.reset)
		}
	}
	if host.requestCount(openCodeGoQuotaURL) != 1 {
		t.Fatalf("opencode-go requests = %d", host.requestCount(openCodeGoQuotaURL))
	}
}

func TestOpenCodeGoDollarFiguresSurviveJSONRoundTrip(t *testing.T) {
	host := newFakeHost().
		withEntry(openCodeGoEntry()).
		withCredential("opencode-go-1", `{"type":"opencode-go","api_key":"oc_test_key"}`).
		withJSON(openCodeGoQuotaURL, openCodeGoUsagePayload)

	raw, err := json.Marshal(quotaSnapshotJSON(t, host, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"used_dollars"`, `"limit_dollars"`, `"reset_at"`, `"used_percent"`} {
		if !bytes.Contains(raw, []byte(key)) {
			t.Fatalf("raw snapshot missing %s: %s", key, raw)
		}
	}
}

// Percent semantics must be identical across providers: a Go percent and a
// Claude percent are comparable by plain numeric comparison.
func TestOpenCodeGoPercentSemanticsMatchClaude(t *testing.T) {
	host := newFakeHost().
		withEntry(openCodeGoEntry()).
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withCredential("opencode-go-1", `{"type":"opencode-go","api_key":"oc_test_key"}`).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withJSON(openCodeGoQuotaURL, openCodeGoUsagePayload).
		withJSON(claudeQuotaURL, `{
			"five_hour": {"utilization": 30, "resets_at": "2026-07-27T12:00:00Z"},
			"seven_day": {"utilization": 10, "resets_at": "2026-08-01T00:00:00Z"}
		}`)

	snapshot := quotaSnapshotJSON(t, host, nil)

	goWeekly := windowByID(t, accountByProvider(t, snapshot, "opencode-go"), "weekly")
	claudeFiveHour := windowByID(t, accountByProvider(t, snapshot, "claude"), "five_hour")
	if *goWeekly.UsedPercent != 30 || *claudeFiveHour.UsedPercent != 30 {
		t.Fatalf("used percents = %v / %v", *goWeekly.UsedPercent, *claudeFiveHour.UsedPercent)
	}
	if *goWeekly.RemainingPercent != *claudeFiveHour.RemainingPercent {
		t.Fatalf("remaining percents differ: %v vs %v", *goWeekly.RemainingPercent, *claudeFiveHour.RemainingPercent)
	}
}

func TestOpenCodeGoRateLimitedRecordedAsPerAccountError(t *testing.T) {
	host := newFakeHost().
		withEntry(openCodeGoEntry()).
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withCredential("opencode-go-1", `{"type":"opencode-go","api_key":"oc_test_key"}`).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withRateLimit(openCodeGoQuotaURL).
		withJSON(claudeQuotaURL, `{
			"five_hour": {"utilization": 30, "resets_at": "2026-07-27T12:00:00Z"}
		}`)

	snapshot := quotaSnapshotJSON(t, host, nil)

	account := accountByProvider(t, snapshot, "opencode-go")
	if account.Status != "error" || account.Error == nil {
		t.Fatalf("opencode-go = %#v", account)
	}
	if account.Error.Code != "quota_fetch_failed" {
		t.Fatalf("error code = %s", account.Error.Code)
	}
	if account.Error.UpstreamStatus != http.StatusTooManyRequests {
		t.Fatalf("upstream status = %d, want %d", account.Error.UpstreamStatus, http.StatusTooManyRequests)
	}
	// No retry or suppression: exactly one upstream attempt.
	if host.requestCount(openCodeGoQuotaURL) != 1 {
		t.Fatalf("opencode-go requests = %d", host.requestCount(openCodeGoQuotaURL))
	}
	claude := accountByProvider(t, snapshot, "claude")
	if claude.Status != "available" || claude.Error != nil {
		t.Fatalf("claude = %#v", claude)
	}
}

func TestOpenCodeGoMissingAPIKeyReportsPerAccountError(t *testing.T) {
	host := newFakeHost().
		withEntry(openCodeGoEntry()).
		withCredential("opencode-go-1", `{"type":"opencode-go"}`)

	account := accountByProvider(t, quotaSnapshotJSON(t, host, nil), "opencode-go")
	if account.Status != "error" || account.Error == nil || account.Error.Code != "credential_incomplete" {
		t.Fatalf("account = %#v", account)
	}
	if !account.Supported {
		t.Fatalf("account should stay supported: %#v", account)
	}
}

// A compatibility auth that is not OpenCode Go must keep reporting
// supported: false rather than being fetched as OpenCode Go.
func TestOtherCompatAuthStaysUnsupported(t *testing.T) {
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "compat-1", ID: "some-other", Name: "some-other.json", Provider: "openai-compatibility"}).
		withCredential("compat-1", `{"api_key":"other"}`)

	account := accountByProvider(t, quotaSnapshotJSON(t, host, nil), "openai-compatibility")
	if account.Supported || account.Status != "unsupported" {
		t.Fatalf("account = %#v", account)
	}
	if host.requestCount(openCodeGoQuotaURL) != 0 {
		t.Fatalf("unexpected opencode-go request")
	}
}

func TestOpenCodeGoPartialUsagePayload(t *testing.T) {
	host := newFakeHost().
		withEntry(openCodeGoEntry()).
		withCredential("opencode-go-1", `{"type":"opencode-go","api_key":"oc_test_key"}`).
		withJSON(openCodeGoQuotaURL, `{"usage": {"rolling": {"status": "ok", "percent": 100, "resetsAt": "2026-08-13T16:27:38Z"}}}`)

	account := accountByProvider(t, quotaSnapshotJSON(t, host, nil), "opencode-go")
	if len(account.Windows) != 1 {
		t.Fatalf("windows = %#v", account.Windows)
	}
	rolling := windowByID(t, account, "rolling")
	if *rolling.UsedDollars != 12 || *rolling.LimitDollars != 12 {
		t.Fatalf("rolling dollars = %#v / %#v", rolling.UsedDollars, rolling.LimitDollars)
	}
	if account.Status != "exhausted" {
		t.Fatalf("status = %s", account.Status)
	}
}
