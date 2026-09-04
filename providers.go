package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	codexQuotaURL         = "https://chatgpt.com/backend-api/wham/usage"
	geminiQuotaURL        = "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota"
	antigravityQuotaURL   = "https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels"
	antigravityDailyURL   = "https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels"
	antigravitySandboxURL = "https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:fetchAvailableModels"
	claudeQuotaURL        = "https://api.anthropic.com/api/oauth/usage"
	copilotQuotaURL       = "https://api.github.com/copilot_internal/user"
	openCodeGoQuotaURL    = "https://opencode.ai/zen/go/v1/usage"
)

const (
	// openCodeGoProvider is the normalized provider name for OpenCode Go. It
	// matches the provider_key attribute emitted by the auth-parser plugin.
	openCodeGoProvider = "opencode-go"
	// compatProvider is the CPA provider the auth-parser plugin emits so the
	// credential routes through the built-in compatibility executor.
	compatProvider = "openai-compatibility"
)

// openCodeGoWindows are the OpenCode Go usage windows and their dollar caps.
// The API reports a used percent per window; the caps let the plugin retain the
// underlying dollar figures for client display without changing percent
// semantics, which stay identical to every other provider.
var openCodeGoWindows = []struct {
	id            string
	key           string
	capDollars    float64
	windowSeconds int64
}{
	{id: "rolling", key: "rolling", capDollars: 12, windowSeconds: 5 * 60 * 60},
	{id: "weekly", key: "weekly", capDollars: 30, windowSeconds: 7 * 24 * 60 * 60},
	{id: "monthly", key: "monthly", capDollars: 60},
}

type providerFetcher func(context.Context, hostClient, pluginConfig, hostAuthFileEntry, json.RawMessage) accountQuota

func fetchAccountQuota(ctx context.Context, host hostClient, cfg pluginConfig, entry hostAuthFileEntry) accountQuota {
	provider := normalizedProvider(entry)
	base := accountQuota{
		AuthIndex:       entry.AuthIndex,
		Name:            entry.Name,
		Provider:        provider,
		Email:           entry.Email,
		ProjectID:       entry.ProjectID,
		CredentialState: credentialState(entry),
		Status:          "unsupported",
		Supported:       false,
	}
	if entry.AuthIndex == "" {
		base.Status = "error"
		base.Error = &quotaError{Code: "missing_auth_index", Message: "credential has no runtime auth index"}
		return base
	}
	fetcher := map[string]providerFetcher{
		"codex":       fetchCodexQuota,
		"gemini-cli":  fetchGeminiQuota,
		"gemini":      fetchGeminiQuota,
		"antigravity": fetchAntigravityQuota,
		"claude":      fetchClaudeQuota,
		"copilot":     fetchCopilotQuota,

		openCodeGoProvider: fetchOpenCodeGoQuota,
	}[provider]
	if fetcher == nil {
		return base
	}
	raw, err := host.getAuth(ctx, entry.AuthIndex)
	if err != nil {
		base.Supported = true
		base.Status = "error"
		base.Error = &quotaError{Code: "auth_read_failed", Message: err.Error()}
		return base
	}
	return fetcher(ctx, host, cfg, entry, raw)
}

func fetchCodexQuota(ctx context.Context, host hostClient, cfg pluginConfig, entry hostAuthFileEntry, raw json.RawMessage) accountQuota {
	result := supportedBase(entry, "codex")
	doc := parseDocument(raw)
	accessToken := lookupString(doc, "access_token")
	accountID := firstNonEmpty(lookupString(doc, "account_id"), lookupString(doc, "chatgpt_account_id"), accountIDFromJWT(lookupString(doc, "id_token")))
	if accessToken == "" || accountID == "" {
		result.Status = "error"
		result.Error = &quotaError{Code: "credential_incomplete", Message: "Codex access_token or account_id is missing"}
		return result
	}
	result.Plan = firstNonEmpty(lookupString(doc, "plan_type"), lookupString(doc, "chatgpt_plan_type"))
	resp, err := doProviderRequest(ctx, host, cfg, hostHTTPRequest{
		Method: http.MethodGet,
		URL:    codexQuotaURL,
		Headers: map[string][]string{
			"Authorization":      {"Bearer " + accessToken},
			"Chatgpt-Account-Id": {accountID},
			"Accept":             {"application/json"},
			"User-Agent":         {"codex_cli_rs/0.76.0 (linux; amd64)"},
		},
	})
	if err != nil {
		return withFetchError(result, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		return withParseError(result, "invalid Codex quota response")
	}
	result.Plan = firstNonEmpty(stringValue(payload["plan_type"]), result.Plan)
	result.Windows = parseCodexWindows(payload)
	result.FetchedAt = time.Now().UTC()
	result.Status = statusFromWindows(result.Windows)
	return result
}

func fetchGeminiQuota(ctx context.Context, host hostClient, cfg pluginConfig, entry hostAuthFileEntry, raw json.RawMessage) accountQuota {
	result := supportedBase(entry, "gemini-cli")
	doc := parseDocument(raw)
	token := lookupString(doc, "access_token")
	projectID := firstNonEmpty(entry.ProjectID, lookupString(doc, "project_id"))
	if token == "" || projectID == "" {
		result.Status = "error"
		result.Error = &quotaError{Code: "credential_incomplete", Message: "Gemini access_token or project_id is missing"}
		return result
	}
	body, _ := json.Marshal(map[string]any{"project": projectID})
	resp, err := doProviderRequest(ctx, host, cfg, hostHTTPRequest{
		Method: http.MethodPost, URL: geminiQuotaURL, Body: body,
		Headers: googleHeaders(token, "IDE_UNSPECIFIED"),
	})
	if err != nil {
		return withFetchError(result, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		return withParseError(result, "invalid Gemini quota response")
	}
	result.Windows = parseGeminiWindows(payload)
	result.ProjectID = projectID
	result.FetchedAt = time.Now().UTC()
	result.Status = statusFromWindows(result.Windows)
	return result
}

func fetchAntigravityQuota(ctx context.Context, host hostClient, cfg pluginConfig, entry hostAuthFileEntry, raw json.RawMessage) accountQuota {
	result := supportedBase(entry, "antigravity")
	doc := parseDocument(raw)
	token := lookupString(doc, "access_token")
	projectID := firstNonEmpty(entry.ProjectID, lookupString(doc, "project_id"))
	if token == "" || projectID == "" {
		result.Status = "error"
		result.Error = &quotaError{Code: "credential_incomplete", Message: "Antigravity access_token or project_id is missing"}
		return result
	}
	body, _ := json.Marshal(map[string]any{"project": projectID})
	var lastErr error
	for _, endpoint := range []string{antigravityQuotaURL, antigravityDailyURL, antigravitySandboxURL} {
		resp, err := doProviderRequest(ctx, host, cfg, hostHTTPRequest{
			Method: http.MethodPost, URL: endpoint, Body: body,
			Headers: map[string][]string{
				"Authorization": {"Bearer " + token},
				"Content-Type":  {"application/json"},
				"Accept":        {"application/json"},
				"User-Agent":    {"antigravity/1.21.9 linux/amd64"},
			},
		})
		if err != nil {
			lastErr = err
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(resp.Body, &payload); err != nil {
			lastErr = fmt.Errorf("invalid Antigravity quota response")
			continue
		}
		result.Models = parseAntigravityModels(payload)
		result.ProjectID = projectID
		result.FetchedAt = time.Now().UTC()
		result.Status = statusFromModels(result.Models)
		return result
	}
	return withFetchError(result, lastErr)
}

func fetchClaudeQuota(ctx context.Context, host hostClient, cfg pluginConfig, entry hostAuthFileEntry, raw json.RawMessage) accountQuota {
	result := supportedBase(entry, "claude")
	doc := parseDocument(raw)
	token := lookupString(doc, "access_token")
	if token == "" {
		result.Status = "error"
		result.Error = &quotaError{Code: "credential_incomplete", Message: "Claude access_token is missing"}
		return result
	}
	resp, err := doProviderRequest(ctx, host, cfg, hostHTTPRequest{
		Method: http.MethodGet, URL: claudeQuotaURL,
		Headers: map[string][]string{
			"Authorization":  {"Bearer " + token},
			"Accept":         {"application/json"},
			"anthropic-beta": {"oauth-2025-04-20"},
			"User-Agent":     {"claude-code/2.1.0"},
		},
	})
	if err != nil {
		return withFetchError(result, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		return withParseError(result, "invalid Claude quota response")
	}
	// Top-level five_hour and seven_day windows. Upstream reports a reset
	// instant but no duration; the durations are known facts of the window
	// kind, so they are filled here (fixed-cycle windows need a derivable
	// start — see CONTEXT.md and ADR 0004).
	for _, item := range []struct {
		id, key string
		seconds int64
	}{
		{"five_hour", "five_hour", 5 * 60 * 60},
		{"seven_day", "seven_day", 7 * 24 * 60 * 60},
	} {
		window, _ := payload[item.key].(map[string]any)
		if window == nil {
			continue
		}
		used := numberPtr(window["utilization"])
		result.Windows = append(result.Windows, quotaWindow{ID: item.id, UsedPercent: used, RemainingPercent: inversePercent(used), ResetAt: timePtr(window["resets_at"]), WindowSeconds: item.seconds})
	}
	// Scoped weekly model limits from limits[].
	limits, _ := payload["limits"].([]any)
	result.Models = parseClaudeScopedLimits(limits)
	// Extra usage credits against monthly limit.
	result.Windows, result.ExtraUsedCredits, result.ExtraMonthlyLimit = parseClaudeExtraUsage(payload, result.Windows)
	// Binding window: use the API's own active-window indicator.
	if bw := parseClaudeBindingWindow(limits); bw != "" {
		result.BindingWindow = &bindingWindow{ID: bw}
	}
	result.FetchedAt = time.Now().UTC()
	result.Status = statusFromClaudeAccount(result.Windows, result.Models)
	return result
}

func fetchCopilotQuota(ctx context.Context, host hostClient, cfg pluginConfig, entry hostAuthFileEntry, raw json.RawMessage) accountQuota {
	result := supportedBase(entry, "copilot")
	doc := parseDocument(raw)
	// The live CPA Copilot credential (written by the cliproxyapi-copilot auth
	// plugin) stores the OAuth token as "github_access_token", not
	// "access_token", and it is a ghu_* GitHub App user-to-server token rather
	// than the gho_* token spec #1 assumed. Verified against the running
	// instance; "access_token" is kept as a fallback for other writers.
	token := lookupString(doc, "github_access_token")
	if token == "" {
		token = lookupString(doc, "access_token")
	}
	if token == "" {
		result.Status = "error"
		result.Error = &quotaError{Code: "credential_incomplete", Message: "Copilot github_access_token is missing"}
		return result
	}
	resp, err := doProviderRequest(ctx, host, cfg, hostHTTPRequest{
		Method: http.MethodGet, URL: copilotQuotaURL,
		Headers: map[string][]string{
			"Authorization": {"Bearer " + token},
			"Accept":        {"application/json"},
			"User-Agent":    {"GitHubCopilot/1.0"},
		},
	})
	if err != nil {
		return withFetchError(result, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		return withParseError(result, "invalid Copilot quota response")
	}
	// The reset date is reported once at the top level, not per snapshot.
	// Verified against the live API (#2): each snapshot carries only
	// "quota_reset_at": 0, while the real date is "quota_reset_date_utc"
	// (RFC3339) with "quota_reset_date" (YYYY-MM-DD) as the older form. Spec #1's
	// per-snapshot "reset_date" field does not exist, which is why reset times
	// came back null on the first live run.
	resetAt := timePtr(payload["quota_reset_date_utc"])
	if resetAt == nil {
		resetAt = timePtr(payload["quota_reset_date"])
	}
	// Copilot quotas cycle monthly: the current cycle starts one calendar
	// month before the reported reset date. The API carries no duration, so
	// it is derived here from the reset instant; without a reset date the
	// cycle start is not derivable and window_seconds stays unset.
	windowSeconds := monthlyWindowSeconds(resetAt)
	snapshots, _ := payload["quota_snapshots"].(map[string]any)
	for _, item := range []struct{ id, key string }{{"premium_interactions", "premium_interactions"}, {"chat", "chat"}, {"completions", "completions"}} {
		window, _ := snapshots[item.key].(map[string]any)
		if window == nil {
			continue
		}
		// "unlimited" snapshots report percent_remaining 100 with a zero
		// entitlement; they are reported as-is so they never bind the pill.
		remaining := numberPtr(window["percent_remaining"])
		result.Windows = append(result.Windows, quotaWindow{
			ID:               item.id,
			RemainingPercent: remaining,
			UsedPercent:      inversePercent(remaining),
			ResetAt:          resetAt,
			WindowSeconds:    windowSeconds,
		})
	}
	result.FetchedAt = time.Now().UTC()
	result.Status = statusFromWindows(result.Windows)
	return result
}

// monthlyWindowSeconds derives a monthly cycle duration from its reset
// instant: the cycle starts one calendar month before the reset, so the
// duration varies with the month length (28-31 days). Returns 0 when the
// reset instant is unknown.
func monthlyWindowSeconds(resetAt *time.Time) int64 {
	if resetAt == nil {
		return 0
	}
	start := resetAt.AddDate(0, -1, 0)
	return int64(resetAt.Sub(start) / time.Second)
}

// fetchOpenCodeGoQuota reads the OpenCode Go usage endpoint with the API key
// emitted by the auth-parser plugin as a bearer token. The payload is
// {"usage":{"rolling":..,"weekly":..,"monthly":..}} where each window carries a
// status, a used percent and a reset timestamp.
func fetchOpenCodeGoQuota(ctx context.Context, host hostClient, cfg pluginConfig, entry hostAuthFileEntry, raw json.RawMessage) accountQuota {
	result := supportedBase(entry, openCodeGoProvider)
	doc := parseDocument(raw)
	apiKey := firstNonEmpty(lookupString(doc, "api_key"), lookupString(doc, "apiKey"), lookupString(doc, "key"))
	if apiKey == "" {
		result.Status = "error"
		result.Error = &quotaError{Code: "credential_incomplete", Message: "OpenCode Go api_key is missing"}
		return result
	}
	resp, err := doProviderRequest(ctx, host, cfg, hostHTTPRequest{
		Method: http.MethodGet, URL: openCodeGoQuotaURL,
		Headers: map[string][]string{
			"Authorization": {"Bearer " + apiKey},
			"Accept":        {"application/json"},
			"User-Agent":    {"cpa-quota-api-extension/" + pluginVersion},
		},
	})
	if err != nil {
		return withFetchError(result, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		return withParseError(result, "invalid OpenCode Go quota response")
	}
	usage, _ := payload["usage"].(map[string]any)
	result.Windows = parseOpenCodeGoWindows(usage)
	result.FetchedAt = time.Now().UTC()
	result.Status = statusFromWindows(result.Windows)
	return result
}

func parseOpenCodeGoWindows(usage map[string]any) []quotaWindow {
	var windows []quotaWindow
	for _, spec := range openCodeGoWindows {
		raw, _ := usage[spec.key].(map[string]any)
		if raw == nil {
			continue
		}
		used := numberPtr(raw["percent"])
		window := quotaWindow{
			ID:               spec.id,
			UsedPercent:      used,
			RemainingPercent: inversePercent(used),
			ResetAt:          timePtr(firstValueAny(raw["resetsAt"], raw["resets_at"])),
			WindowSeconds:    spec.windowSeconds,
		}
		limit := spec.capDollars
		window.LimitDollars = &limit
		if used != nil {
			spent := math.Round(limit**used) / 100
			window.UsedDollars = &spent
		}
		windows = append(windows, window)
	}
	return windows
}

func supportedBase(entry hostAuthFileEntry, provider string) accountQuota {
	return accountQuota{AuthIndex: entry.AuthIndex, Name: entry.Name, Provider: provider, Email: entry.Email, ProjectID: entry.ProjectID, CredentialState: credentialState(entry), Status: "unknown", Supported: true}
}

func doProviderRequest(ctx context.Context, host hostClient, cfg pluginConfig, request hostHTTPRequest) (hostHTTPResponse, error) {
	requestCtx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout)
	defer cancel()
	resp, err := host.doHTTP(requestCtx, request)
	if err != nil {
		return hostHTTPResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return hostHTTPResponse{}, upstreamError{Status: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
	}
	return resp, nil
}

type upstreamError struct {
	Status  int
	Message string
}

func (e upstreamError) Error() string {
	return fmt.Sprintf("upstream HTTP %d: %s", e.Status, e.Message)
}

func withFetchError(result accountQuota, err error) accountQuota {
	result.Status = "error"
	if err == nil {
		err = fmt.Errorf("quota request failed")
	}
	qe := &quotaError{Code: "quota_fetch_failed", Message: err.Error()}
	if upstream, ok := err.(upstreamError); ok {
		qe.UpstreamStatus = upstream.Status
	}
	result.Error = qe
	return result
}
func withParseError(result accountQuota, message string) accountQuota {
	result.Status = "error"
	result.Error = &quotaError{Code: "invalid_upstream_response", Message: message}
	return result
}

func normalizedProvider(entry hostAuthFileEntry) string {
	provider := strings.ToLower(strings.TrimSpace(firstNonEmpty(entry.Provider, entry.Type)))
	if provider == "gemini" {
		return "gemini-cli"
	}
	// The auth-parser plugin emits OpenCode Go as an openai-compatibility auth
	// so it routes through the built-in compat executor. The host does not
	// expose auth attributes on the list entry, so the credential is identified
	// by its stable auth ID / file name instead of provider_key.
	if provider == compatProvider && isOpenCodeGoEntry(entry) {
		return openCodeGoProvider
	}
	return provider
}

func isOpenCodeGoEntry(entry hostAuthFileEntry) bool {
	for _, candidate := range []string{entry.ID, entry.Name, entry.Label} {
		name := strings.ToLower(strings.TrimSpace(candidate))
		if strings.TrimSuffix(name, ".json") == openCodeGoProvider {
			return true
		}
	}
	return false
}
func credentialState(entry hostAuthFileEntry) string {
	if entry.Disabled {
		return "disabled"
	}
	if entry.Unavailable {
		return "unavailable"
	}
	if entry.Status != "" {
		return entry.Status
	}
	return "active"
}

func parseDocument(raw json.RawMessage) map[string]any {
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	return doc
}
func lookupString(value any, key string) string {
	if m, ok := value.(map[string]any); ok {
		if v := stringValue(m[key]); v != "" {
			return v
		}
		for _, child := range m {
			if v := lookupString(child, key); v != "" {
				return v
			}
		}
	}
	if a, ok := value.([]any); ok {
		for _, child := range a {
			if v := lookupString(child, key); v != "" {
				return v
			}
		}
	}
	return ""
}
func stringValue(v any) string { s, _ := v.(string); return strings.TrimSpace(s) }
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func accountIDFromJWT(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var doc map[string]any
	if json.Unmarshal(payload, &doc) != nil {
		return ""
	}
	if v := stringValue(doc["chatgpt_account_id"]); v != "" {
		return v
	}
	if auth, ok := doc["https://api.openai.com/auth"].(map[string]any); ok {
		return stringValue(auth["chatgpt_account_id"])
	}
	return ""
}

func parseCodexWindows(payload map[string]any) []quotaWindow {
	var out []quotaWindow
	rate, _ := payload["rate_limit"].(map[string]any)
	for _, item := range []struct{ id, key string }{{"primary", "primary_window"}, {"secondary", "secondary_window"}} {
		if w, _ := rate[item.key].(map[string]any); w != nil {
			out = append(out, parsePercentWindow(item.id, w))
		}
	}
	if review, _ := payload["code_review_rate_limit"].(map[string]any); review != nil {
		for _, item := range []struct{ id, key string }{{"code_review_primary", "primary_window"}, {"code_review_secondary", "secondary_window"}} {
			if w, _ := review[item.key].(map[string]any); w != nil {
				out = append(out, parsePercentWindow(item.id, w))
			}
		}
	}
	return out
}
func parsePercentWindow(id string, value map[string]any) quotaWindow {
	used := numberPtr(value["used_percent"])
	return quotaWindow{ID: id, UsedPercent: used, RemainingPercent: inversePercent(used), ResetAt: unixTimePtr(value["reset_at"]), WindowSeconds: int64Value(value["limit_window_seconds"])}
}

func parseGeminiWindows(payload map[string]any) []quotaWindow {
	buckets, _ := payload["buckets"].([]any)
	out := make([]quotaWindow, 0, len(buckets))
	for _, raw := range buckets {
		b, _ := raw.(map[string]any)
		if b == nil {
			continue
		}
		id := firstNonEmpty(stringValue(b["modelId"]), stringValue(b["model_id"]))
		if id == "" {
			continue
		}
		remaining := fractionPercent(numberPtr(firstValueAny(b["remainingFraction"], b["remaining_fraction"])))
		out = append(out, quotaWindow{ID: id, RemainingPercent: remaining, UsedPercent: inversePercent(remaining), ResetAt: timePtr(firstValueAny(b["resetTime"], b["reset_time"]))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func parseAntigravityModels(payload map[string]any) []modelQuota {
	models, _ := payload["models"].(map[string]any)
	out := make([]modelQuota, 0, len(models))
	for id, raw := range models {
		entry, _ := raw.(map[string]any)
		quota, _ := firstValueAny(entry["quotaInfo"], entry["quota_info"]).(map[string]any)
		if quota == nil {
			continue
		}
		remaining := fractionPercent(numberPtr(firstValueAny(quota["remainingFraction"], quota["remaining_fraction"])))
		out = append(out, modelQuota{Model: id, RemainingPercent: remaining, ResetAt: timePtr(firstValueAny(quota["resetTime"], quota["reset_time"]))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

func statusFromWindows(windows []quotaWindow) string {
	if len(windows) == 0 {
		return "unknown"
	}
	observed := false
	for _, w := range windows {
		if w.RemainingPercent != nil {
			observed = true
			if *w.RemainingPercent > 0 {
				return "available"
			}
		}
	}
	if observed {
		return "exhausted"
	}
	return "unknown"
}
func statusFromModels(models []modelQuota) string {
	if len(models) == 0 {
		return "unknown"
	}
	observed := false
	for _, m := range models {
		if m.RemainingPercent != nil {
			observed = true
			if *m.RemainingPercent > 0 {
				return "available"
			}
		}
	}
	if observed {
		return "exhausted"
	}
	return "unknown"
}
func numberPtr(v any) *float64 {
	switch n := v.(type) {
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return nil
		}
		return &n
	case json.Number:
		f, e := n.Float64()
		if e == nil {
			return &f
		}
	case int:
		f := float64(n)
		return &f
	case int64:
		f := float64(n)
		return &f
	}
	return nil
}
func inversePercent(v *float64) *float64 {
	if v == nil {
		return nil
	}
	n := math.Max(0, math.Min(100, 100-*v))
	return &n
}
func fractionPercent(v *float64) *float64 {
	if v == nil {
		return nil
	}
	n := math.Max(0, math.Min(100, *v*100))
	return &n
}
func int64Value(v any) int64 {
	if n := numberPtr(v); n != nil {
		return int64(*n)
	}
	return 0
}
func unixTimePtr(v any) *time.Time {
	n := int64Value(v)
	if n <= 0 {
		return nil
	}
	if n > 1e12 {
		n /= 1000
	}
	t := time.Unix(n, 0).UTC()
	return &t
}
func timePtr(v any) *time.Time {
	s := stringValue(v)
	if s == "" {
		return nil
	}
	t, e := time.Parse(time.RFC3339Nano, s)
	if e != nil {
		return nil
	}
	t = t.UTC()
	return &t
}

// parseClaudeScopedLimits extracts per-model weekly quota from limits[].
func parseClaudeScopedLimits(limits []any) []modelQuota {
	if len(limits) == 0 {
		return nil
	}
	var models []modelQuota
	seen := make(map[string]bool)
	for _, raw := range limits {
		entry, _ := raw.(map[string]any)
		if entry == nil {
			continue
		}
		kind, _ := entry["kind"].(string)
		group, _ := entry["group"].(string)
		if kind != "weekly_scoped" || group != "weekly" {
			continue
		}
		used := numberPtr(entry["percent"])
		if used == nil {
			continue
		}
		scope, _ := entry["scope"].(map[string]any)
		if scope == nil {
			continue
		}
		model, _ := scope["model"].(map[string]any)
		if model == nil {
			continue
		}
		modelID, _ := model["id"].(string)
		modelName, _ := model["display_name"].(string)
		if modelName == "" {
			continue
		}
		slug := claudeSlug(firstNonEmpty(modelID, modelName))
		if slug == "" {
			continue
		}
		id := "claude-weekly-scoped-" + slug
		if seen[id] {
			continue
		}
		seen[id] = true
		models = append(models, modelQuota{
			Model:            id,
			ModelName:        modelName,
			RemainingPercent: inversePercent(used),
			ResetAt:          timePtr(entry["resets_at"]),
		})
	}
	return models
}

// parseClaudeExtraUsage maps used_credits/monthly_limit to an extra-usage window.
func parseClaudeExtraUsage(payload map[string]any, windows []quotaWindow) ([]quotaWindow, *int64, *int64) {
	extra, _ := payload["extra_usage"].(map[string]any)
	if extra == nil {
		return windows, nil, nil
	}
	enabled, _ := extra["is_enabled"].(bool)
	if !enabled {
		return windows, nil, nil
	}
	used := int64Value(extra["used_credits"])
	limit := int64Value(extra["monthly_limit"])
	if limit <= 0 {
		return windows, nil, nil
	}
	usedCredits := used
	monthlyLimit := limit
	var usedPercent float64
	if limit > 0 {
		usedPercent = math.Max(0, math.Min(100, float64(used)*100/float64(limit)))
	}
	up := &usedPercent
	windows = append(windows, quotaWindow{
		ID:               "extra",
		UsedPercent:      up,
		RemainingPercent: inversePercent(up),
	})
	return windows, &usedCredits, &monthlyLimit
}

// parseClaudeBindingWindow identifies the window the API reports as binding.
func parseClaudeBindingWindow(limits []any) string {
	for _, raw := range limits {
		entry, _ := raw.(map[string]any)
		if entry == nil {
			continue
		}
		active, _ := entry["is_active"].(bool)
		if !active {
			continue
		}
		kind, _ := entry["kind"].(string)
		switch kind {
		case "session":
			return "five_hour"
		case "weekly_all":
			return "seven_day"
		case "weekly_scoped":
			scope, _ := entry["scope"].(map[string]any)
			if scope == nil {
				continue
			}
			model, _ := scope["model"].(map[string]any)
			if model == nil {
				continue
			}
			modelID, _ := model["id"].(string)
			modelName, _ := model["display_name"].(string)
			identity := firstNonEmpty(modelID, modelName)
			slug := claudeSlug(identity)
			if slug != "" {
				return "claude-weekly-scoped-" + slug
			}
		}
	}
	return ""
}

// claudeSlug lowercases a model identity and replaces non-alphanumeric runs with dashes.
func claudeSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	var out []rune
	prevDash := true
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			out = append(out, r)
			prevDash = false
		} else if !prevDash {
			out = append(out, '-')
			prevDash = true
		}
	}
	if len(out) > 0 && out[len(out)-1] == '-' {
		out = out[:len(out)-1]
	}
	return string(out)
}

// statusFromClaudeAccount combines windows and models into a single status.
func statusFromClaudeAccount(windows []quotaWindow, models []modelQuota) string {
	for _, w := range windows {
		if w.RemainingPercent != nil && *w.RemainingPercent > 0 {
			return "available"
		}
	}
	for _, m := range models {
		if m.RemainingPercent != nil && *m.RemainingPercent > 0 {
			return "available"
		}
	}
	if len(windows) > 0 || len(models) > 0 {
		observed := false
		for _, w := range windows {
			if w.RemainingPercent != nil {
				observed = true
			}
		}
		for _, m := range models {
			if m.RemainingPercent != nil {
				observed = true
			}
		}
		if observed {
			return "exhausted"
		}
	}
	return "unknown"
}

func firstValueAny(values ...any) any {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

func googleHeaders(token, ideType string) map[string][]string {
	metadata, _ := json.Marshal(map[string]string{"ideType": ideType, "platform": "PLATFORM_UNSPECIFIED", "pluginType": "GEMINI"})
	return map[string][]string{"Authorization": {"Bearer " + token}, "Content-Type": {"application/json"}, "Accept": {"application/json"}, "User-Agent": {"google-api-nodejs-client/9.15.1"}, "X-Goog-Api-Client": {"google-cloud-sdk vscode_cloudshelleditor/0.1"}, "Client-Metadata": {string(metadata)}}
}
