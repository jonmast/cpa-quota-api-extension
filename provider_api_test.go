package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
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
	if fiveHour.WindowSeconds != 18000 {
		t.Fatalf("five_hour window_seconds = %d, want 18000", fiveHour.WindowSeconds)
	}
	sevenDay := windowByID(t, account, "seven_day")
	if sevenDay.RemainingPercent == nil || *sevenDay.RemainingPercent != 90 {
		t.Fatalf("seven_day = %#v", sevenDay)
	}
	if sevenDay.WindowSeconds != 604800 {
		t.Fatalf("seven_day window_seconds = %d, want 604800", sevenDay.WindowSeconds)
	}
	if host.requestCount(claudeQuotaURL) != 1 {
		t.Fatalf("claude requests = %d", host.requestCount(claudeQuotaURL))
	}
}

func TestClaudeWindowSecondsFilledWhenResetTimestampOmitted(t *testing.T) {
	// The window durations are known facts of the window kind, so they are
	// filled even when upstream omits the reset instant.
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withJSON(claudeQuotaURL, `{
			"five_hour": {"utilization": 40},
			"seven_day": {"utilization": 10}
		}`)

	account := accountByProvider(t, quotaSnapshotJSON(t, host, nil), "claude")

	fiveHour := windowByID(t, account, "five_hour")
	if fiveHour.WindowSeconds != 18000 {
		t.Fatalf("five_hour window_seconds = %d, want 18000", fiveHour.WindowSeconds)
	}
	if fiveHour.ResetAt != nil {
		t.Fatalf("five_hour reset = %#v, want nil", fiveHour.ResetAt)
	}
	sevenDay := windowByID(t, account, "seven_day")
	if sevenDay.WindowSeconds != 604800 {
		t.Fatalf("seven_day window_seconds = %d, want 604800", sevenDay.WindowSeconds)
	}
	if sevenDay.ResetAt != nil {
		t.Fatalf("seven_day reset = %#v, want nil", sevenDay.ResetAt)
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
	// The extra credit pool has no fixed cycle: no duration is invented for it.
	if extra.WindowSeconds != 0 {
		t.Fatalf("extra window_seconds = %d, want 0", extra.WindowSeconds)
	}
	if extra.ResetAt != nil {
		t.Fatalf("extra reset = %#v, want nil", extra.ResetAt)
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

// The real CPA Copilot credential stores the token under
// "github_access_token" with a ghu_* prefix — not "access_token"/gho_* as spec
// #1 assumed. Caught by deploying to the live instance (#2), where Copilot came
// back as credential_incomplete. This pins the real-world field name.
func TestCopilotReadsGithubAccessTokenField(t *testing.T) {
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "copilot-1", Name: "copilot-jonmast.json", Provider: "copilot"}).
		withCredential("copilot-1", `{"github_access_token":"ghu_test_token","token_type":"bearer","type":"copilot"}`).
		withJSON(copilotQuotaURL, `{
			"quota_snapshots": {
				"premium_interactions": {"entitlement": 1000, "remaining": 500, "percent_remaining": 50.0, "reset_date": "2026-09-01T00:00:00Z"}
			}
		}`)

	account := accountByProvider(t, quotaSnapshotJSON(t, host, nil), "copilot")

	if account.Error != nil {
		t.Fatalf("unexpected error: %#v", account.Error)
	}
	if account.Status != "available" {
		t.Fatalf("status = %q, want available", account.Status)
	}
	premium := windowByID(t, account, "premium_interactions")
	if premium.RemainingPercent == nil || *premium.RemainingPercent != 50.0 {
		t.Fatalf("premium_interactions remaining = %#v", premium.RemainingPercent)
	}
}

func TestCopilotQuotaReachesFetcherThroughManagementHandler(t *testing.T) {
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "copilot-1", Name: "copilot.json", Provider: "copilot", Email: "user@example.com"}).
		withCredential("copilot-1", `{"access_token":"gho_test_token"}`).
		withJSON(copilotQuotaURL, `{
			"quota_reset_date_utc": "2026-09-01T00:00:00Z",
			"quota_reset_date": "2026-09-01",
			"quota_snapshots": {
				"premium_interactions": {"entitlement": 1000, "remaining": 500, "percent_remaining": 50.0, "quota_reset_at": 0},
				"chat": {"entitlement": 500, "remaining": 200, "percent_remaining": 40.0, "quota_reset_at": 0},
				"completions": {"entitlement": 2000, "remaining": 1000, "percent_remaining": 50.0, "quota_reset_at": 0}
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
	// The cycle starts one calendar month before the reset: 2026-08-01 to
	// 2026-09-01 spans 31 days.
	wantSeconds := int64(31 * 24 * 60 * 60)
	if premium.WindowSeconds != wantSeconds {
		t.Fatalf("premium_interactions window_seconds = %d, want %d", premium.WindowSeconds, wantSeconds)
	}
	chat := windowByID(t, account, "chat")
	if chat.RemainingPercent == nil || *chat.RemainingPercent != 40.0 {
		t.Fatalf("chat remaining = %#v", chat.RemainingPercent)
	}
	if chat.WindowSeconds != wantSeconds {
		t.Fatalf("chat window_seconds = %d, want %d", chat.WindowSeconds, wantSeconds)
	}
	completions := windowByID(t, account, "completions")
	if completions.RemainingPercent == nil || *completions.RemainingPercent != 50.0 {
		t.Fatalf("completions remaining = %#v", completions.RemainingPercent)
	}
	if completions.WindowSeconds != wantSeconds {
		t.Fatalf("completions window_seconds = %d, want %d", completions.WindowSeconds, wantSeconds)
	}
	if host.requestCount(copilotQuotaURL) != 1 {
		t.Fatalf("copilot requests = %d", host.requestCount(copilotQuotaURL))
	}
}

func TestCopilotWindowSecondsOmittedWhenResetDateMissing(t *testing.T) {
	// Without a reset date the monthly cycle start is not derivable, so no
	// duration is invented.
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "copilot-1", Name: "copilot.json", Provider: "copilot"}).
		withCredential("copilot-1", `{"access_token":"gho_test_token"}`).
		withJSON(copilotQuotaURL, `{
			"quota_snapshots": {
				"premium_interactions": {"entitlement": 1000, "remaining": 500, "percent_remaining": 50.0, "quota_reset_at": 0}
			}
		}`)

	account := accountByProvider(t, quotaSnapshotJSON(t, host, nil), "copilot")

	premium := windowByID(t, account, "premium_interactions")
	if premium.RemainingPercent == nil || *premium.RemainingPercent != 50.0 {
		t.Fatalf("premium_interactions remaining = %#v", premium.RemainingPercent)
	}
	if premium.ResetAt != nil {
		t.Fatalf("premium_interactions reset = %#v, want nil", premium.ResetAt)
	}
	if premium.WindowSeconds != 0 {
		t.Fatalf("premium_interactions window_seconds = %d, want 0", premium.WindowSeconds)
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

// --- Issue #8: provider-nested response shape ---

// providerByKey returns the providerQuota for a key, failing the test if absent.
func providerByKey(t *testing.T, snapshot quotaResponse, key string) providerQuota {
	t.Helper()
	p, ok := snapshot.Providers[key]
	if !ok {
		t.Fatalf("providers map missing key %q; have %v", key, mapKeys(snapshot.Providers))
	}
	return p
}

func mapKeys(m map[string]providerQuota) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// TestResponseHasProvidersMap checks that the quota response carries a providers
// map and that each provider present in the accounts list appears as a key.
func TestResponseHasProvidersMap(t *testing.T) {
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withEntry(hostAuthFileEntry{AuthIndex: "copilot-1", Name: "copilot.json", Provider: "copilot"}).
		withEntry(openCodeGoEntry()).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withCredential("copilot-1", `{"access_token":"gho_test_token"}`).
		withCredential("opencode-go-1", `{"type":"opencode-go","api_key":"oc_test_key"}`).
		withJSON(claudeQuotaURL, `{"five_hour":{"utilization":30,"resets_at":"2026-07-27T12:00:00Z"}}`).
		withJSON(copilotQuotaURL, `{"quota_snapshots":{"premium_interactions":{"percent_remaining":50,"reset_date":"2026-09-01T00:00:00Z"}}}`).
		withJSON(openCodeGoQuotaURL, openCodeGoUsagePayload)

	snapshot := quotaSnapshotJSON(t, host, nil)

	if snapshot.Providers == nil {
		t.Fatal("providers map is nil")
	}
	for _, key := range []string{"claude", "copilot", "opencode-go"} {
		if _, ok := snapshot.Providers[key]; !ok {
			t.Errorf("providers map missing key %q", key)
		}
	}
}

// TestProvidersMapWindowsNestedUnderKey checks that windows from each provider
// are nested under the provider key, not just in the flat accounts list.
func TestProvidersMapWindowsNestedUnderKey(t *testing.T) {
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withEntry(hostAuthFileEntry{AuthIndex: "copilot-1", Name: "copilot.json", Provider: "copilot"}).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withCredential("copilot-1", `{"access_token":"gho_test_token"}`).
		withJSON(claudeQuotaURL, `{"five_hour":{"utilization":30,"resets_at":"2026-07-27T12:00:00Z"},"seven_day":{"utilization":10,"resets_at":"2026-08-01T00:00:00Z"}}`).
		withJSON(copilotQuotaURL, `{"quota_snapshots":{"premium_interactions":{"percent_remaining":50,"reset_date":"2026-09-01T00:00:00Z"},"chat":{"percent_remaining":40,"reset_date":"2026-09-01T00:00:00Z"}}}`)

	snapshot := quotaSnapshotJSON(t, host, nil)

	// Claude windows appear under providers["claude"].
	claude := providerByKey(t, snapshot, "claude")
	found := false
	for _, w := range claude.Windows {
		if w.ID == "five_hour" {
			found = true
			if w.RemainingPercent == nil || *w.RemainingPercent != 70 {
				t.Fatalf("claude five_hour remaining = %#v", w.RemainingPercent)
			}
		}
	}
	if !found {
		t.Fatalf("claude providers entry missing five_hour window; have %#v", claude.Windows)
	}

	// Copilot windows appear under providers["copilot"].
	copilot := providerByKey(t, snapshot, "copilot")
	found = false
	for _, w := range copilot.Windows {
		if w.ID == "premium_interactions" {
			found = true
			if w.RemainingPercent == nil || *w.RemainingPercent != 50 {
				t.Fatalf("copilot premium_interactions remaining = %#v", w.RemainingPercent)
			}
		}
	}
	if !found {
		t.Fatalf("copilot providers entry missing premium_interactions window; have %#v", copilot.Windows)
	}
}

// TestProvidersMapCarriesEnoughForTightestPill checks that the providers map
// carries remaining_percent on each window so a client can compute the
// tightest-across-all-providers value with a provider prefix.
func TestProvidersMapCarriesEnoughForTightestPill(t *testing.T) {
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withEntry(openCodeGoEntry()).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withCredential("opencode-go-1", `{"type":"opencode-go","api_key":"oc_test_key"}`).
		withJSON(claudeQuotaURL, `{"five_hour":{"utilization":30,"resets_at":"2026-07-27T12:00:00Z"}}`).
		withJSON(openCodeGoQuotaURL, openCodeGoUsagePayload)

	snapshot := quotaSnapshotJSON(t, host, nil)

	// For each provider in the map a client can derive a tightest remaining_percent.
	for key, p := range snapshot.Providers {
		if !p.Supported {
			continue
		}
		if p.Status == "error" {
			continue
		}
		for _, w := range p.Windows {
			if w.RemainingPercent == nil {
				t.Errorf("provider %q window %q has nil remaining_percent", key, w.ID)
			}
		}
	}

	// Spot-check: claude five_hour and opencode-go weekly both have remaining_percent.
	claudeProvider := providerByKey(t, snapshot, "claude")
	var claudeFiveHour *quotaWindow
	for i := range claudeProvider.Windows {
		if claudeProvider.Windows[i].ID == "five_hour" {
			claudeFiveHour = &claudeProvider.Windows[i]
		}
	}
	if claudeFiveHour == nil || claudeFiveHour.RemainingPercent == nil {
		t.Fatal("claude five_hour missing remaining_percent")
	}

	goProvider := providerByKey(t, snapshot, "opencode-go")
	var goWeekly *quotaWindow
	for i := range goProvider.Windows {
		if goProvider.Windows[i].ID == "weekly" {
			goWeekly = &goProvider.Windows[i]
		}
	}
	if goWeekly == nil || goWeekly.RemainingPercent == nil {
		t.Fatal("opencode-go weekly missing remaining_percent")
	}

	// The two remaining percents are numerically comparable (same semantics).
	if *claudeFiveHour.RemainingPercent != 70 || *goWeekly.RemainingPercent != 70 {
		t.Fatalf("percents not comparable: claude=%v go=%v", *claudeFiveHour.RemainingPercent, *goWeekly.RemainingPercent)
	}
}

// TestProvidersMapRetainsPerAccountFields checks that the provider-level entry
// keeps status, supported, credential_state, and error visible so a client can
// detect a partial failure without walking the accounts list.
func TestProvidersMapRetainsPerAccountFields(t *testing.T) {
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withTransportError(claudeQuotaURL, errors.New("dial tcp: connection refused"))

	snapshot := quotaSnapshotJSON(t, host, nil)

	p := providerByKey(t, snapshot, "claude")
	if p.Status != "error" {
		t.Fatalf("provider status = %q, want error", p.Status)
	}
	if !p.Supported {
		t.Fatalf("provider supported should be true even on fetch error")
	}
	if p.Error == nil || p.Error.Code != "quota_fetch_failed" {
		t.Fatalf("provider error = %#v", p.Error)
	}
	// The error must also be visible via the accounts list.
	if len(p.Accounts) != 1 || p.Accounts[0].Error == nil {
		t.Fatalf("provider accounts = %#v", p.Accounts)
	}
}

// TestProvidersMapShowsErroredProviderAlongsideHealthy checks that one
// provider erroring does not hide others from the providers map.
func TestProvidersMapShowsErroredProviderAlongsideHealthy(t *testing.T) {
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "copilot-1", Name: "copilot.json", Provider: "copilot"}).
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withCredential("copilot-1", `{"access_token":"gho_test_token"}`).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withTransportError(copilotQuotaURL, errors.New("dial tcp: connection refused")).
		withJSON(claudeQuotaURL, `{"five_hour":{"utilization":40,"resets_at":"2026-07-27T12:00:00Z"}}`)

	snapshot := quotaSnapshotJSON(t, host, nil)

	copilot := providerByKey(t, snapshot, "copilot")
	if copilot.Status != "error" || copilot.Error == nil {
		t.Fatalf("copilot provider = %#v", copilot)
	}

	claude := providerByKey(t, snapshot, "claude")
	if claude.Status != "available" || claude.Error != nil {
		t.Fatalf("claude provider = %#v", claude)
	}
	found := false
	for _, w := range claude.Windows {
		if w.ID == "five_hour" {
			found = true
			if w.RemainingPercent == nil || *w.RemainingPercent != 60 {
				t.Fatalf("claude five_hour remaining = %#v", w.RemainingPercent)
			}
		}
	}
	if !found {
		t.Fatalf("claude five_hour window missing from providers map")
	}
}

// TestProvidersMapCarriesClaudeModelsAndBindingWindow checks that Claude-specific
// fields (scoped models, binding window, extra usage) propagate to the providers
// map, not just the flat accounts list.
func TestProvidersMapCarriesClaudeModelsAndBindingWindow(t *testing.T) {
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withJSON(claudeQuotaURL, claudeFullPayload)

	snapshot := quotaSnapshotJSON(t, host, nil)

	p := providerByKey(t, snapshot, "claude")
	if len(p.Models) != 2 {
		t.Fatalf("provider models = %d, want 2: %#v", len(p.Models), p.Models)
	}
	if p.BindingWindow == nil || p.BindingWindow.ID != "five_hour" {
		t.Fatalf("provider binding_window = %#v", p.BindingWindow)
	}
	if p.ExtraUsedCredits == nil || *p.ExtraUsedCredits != 500 {
		t.Fatalf("provider extra_used_credits = %#v", p.ExtraUsedCredits)
	}
	if p.ExtraMonthlyLimit == nil || *p.ExtraMonthlyLimit != 5000 {
		t.Fatalf("provider extra_monthly_limit = %#v", p.ExtraMonthlyLimit)
	}
}

// TestProvidersMapAccountsListPreserved checks that the providers map carries
// the per-account list so no information is collapsed.
func TestProvidersMapAccountsListPreserved(t *testing.T) {
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withJSON(claudeQuotaURL, `{"five_hour":{"utilization":30,"resets_at":"2026-07-27T12:00:00Z"}}`)

	snapshot := quotaSnapshotJSON(t, host, nil)

	p := providerByKey(t, snapshot, "claude")
	if len(p.Accounts) != 1 {
		t.Fatalf("provider accounts len = %d, want 1", len(p.Accounts))
	}
	if p.Accounts[0].AuthIndex != "claude-1" {
		t.Fatalf("provider accounts[0].auth_index = %q", p.Accounts[0].AuthIndex)
	}
	if p.Accounts[0].Provider != "claude" {
		t.Fatalf("provider accounts[0].provider = %q", p.Accounts[0].Provider)
	}
}

// TestProvidersMapInRawJSON checks that the providers map is present and
// properly keyed in the raw response JSON so a client reading the wire format
// sees the nested structure.
func TestProvidersMapInRawJSON(t *testing.T) {
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withEntry(hostAuthFileEntry{AuthIndex: "copilot-1", Name: "copilot.json", Provider: "copilot"}).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withCredential("copilot-1", `{"access_token":"gho_test_token"}`).
		withJSON(claudeQuotaURL, `{"five_hour":{"utilization":30,"resets_at":"2026-07-27T12:00:00Z"}}`).
		withJSON(copilotQuotaURL, `{"quota_snapshots":{"premium_interactions":{"percent_remaining":50,"reset_date":"2026-09-01T00:00:00Z"}}}`)

	runtime := newRuntime(host)
	runtime.applyConfig(pluginConfig{CacheTTL: 30 * time.Minute, RequestTimeout: time.Second, MaxConcurrency: 4})
	resp := runtime.handleManagement(managementRequest{Method: http.MethodGet, Path: quotaRoute})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	for _, needle := range []string{`"providers"`, `"claude"`, `"copilot"`, `"remaining_percent"`} {
		if !bytes.Contains(resp.Body, []byte(needle)) {
			t.Errorf("response body missing %q", needle)
		}
	}
}

// TestCachingSecondCallWithinTTLDoesNotRefetch checks that a second management
// handler call within the TTL does not re-hit upstream providers.
func TestCachingSecondCallWithinTTLDoesNotRefetch(t *testing.T) {
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withJSON(claudeQuotaURL, `{"five_hour":{"utilization":30,"resets_at":"2026-07-27T12:00:00Z"}}`)

	runtime := newRuntime(host)
	runtime.applyConfig(pluginConfig{CacheTTL: 30 * time.Minute, RequestTimeout: time.Second, MaxConcurrency: 4})

	// First call — cache miss.
	resp1 := runtime.handleManagement(managementRequest{Method: http.MethodGet, Path: quotaRoute})
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first call status = %d", resp1.StatusCode)
	}
	var snap1 quotaResponse
	if err := json.Unmarshal(resp1.Body, &snap1); err != nil {
		t.Fatal(err)
	}
	if snap1.Cached {
		t.Fatal("first call should not be cached")
	}

	// Second call — should be served from cache.
	resp2 := runtime.handleManagement(managementRequest{Method: http.MethodGet, Path: quotaRoute})
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("second call status = %d", resp2.StatusCode)
	}
	var snap2 quotaResponse
	if err := json.Unmarshal(resp2.Body, &snap2); err != nil {
		t.Fatal(err)
	}
	if !snap2.Cached {
		t.Fatal("second call should be cached")
	}
	// Providers map must still be present in cached response.
	if _, ok := snap2.Providers["claude"]; !ok {
		t.Fatal("providers map missing claude key in cached response")
	}
	// Only one upstream request despite two handler calls.
	if host.requestCount(claudeQuotaURL) != 1 {
		t.Fatalf("expected 1 upstream request, got %d", host.requestCount(claudeQuotaURL))
	}
}

// TestForcedRefreshBypasessTTL checks that ?refresh=true bypasses the cache
// and triggers a new upstream fetch.
func TestForcedRefreshBypassesTTL(t *testing.T) {
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withJSON(claudeQuotaURL, `{"five_hour":{"utilization":30,"resets_at":"2026-07-27T12:00:00Z"}}`)

	runtime := newRuntime(host)
	runtime.applyConfig(pluginConfig{CacheTTL: 30 * time.Minute, RequestTimeout: time.Second, MaxConcurrency: 4})

	// First call fills the cache.
	runtime.handleManagement(managementRequest{Method: http.MethodGet, Path: quotaRoute})

	// Second call with refresh=true forces a new upstream fetch.
	resp := runtime.handleManagement(managementRequest{
		Method: http.MethodGet,
		Path:   quotaRoute,
		Query:  map[string][]string{"refresh": {"true"}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var snap quotaResponse
	if err := json.Unmarshal(resp.Body, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Cached {
		t.Fatal("forced refresh response should not be cached")
	}
	if host.requestCount(claudeQuotaURL) != 2 {
		t.Fatalf("expected 2 upstream requests after forced refresh, got %d", host.requestCount(claudeQuotaURL))
	}
}

// TestConcurrentManagementCallersShareOneRefresh checks that concurrent
// management handler calls during an active refresh share a single upstream
// fetch (no duplicate provider calls).
func TestConcurrentManagementCallersShareOneRefresh(t *testing.T) {
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withJSON(claudeQuotaURL, `{"five_hour":{"utilization":30,"resets_at":"2026-07-27T12:00:00Z"}}`)

	runtime := newRuntime(host)
	runtime.applyConfig(pluginConfig{CacheTTL: 30 * time.Minute, RequestTimeout: time.Second, MaxConcurrency: 4})

	const callers = 8
	var wg sync.WaitGroup
	errs := make(chan string, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := runtime.handleManagement(managementRequest{Method: http.MethodGet, Path: quotaRoute})
			if resp.StatusCode != http.StatusOK {
				errs <- "status != 200"
			}
		}()
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}

	// listAuth (and by extension all provider HTTP calls) must have been issued
	// exactly once — the fakeHost listDelay creates the concurrent window.
	if calls := host.listAuthCount(); calls != 1 {
		t.Fatalf("listAuth calls = %d, want 1", calls)
	}
	if host.requestCount(claudeQuotaURL) != 1 {
		t.Fatalf("claude upstream requests = %d, want 1", host.requestCount(claudeQuotaURL))
	}
}
