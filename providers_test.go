package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func TestParseCodexWindows(t *testing.T) {
	payload := map[string]any{"rate_limit": map[string]any{
		"primary_window":   map[string]any{"used_percent": 25.0, "reset_at": 1_800_000_000.0, "limit_window_seconds": 18000.0},
		"secondary_window": map[string]any{"used_percent": 75.0, "reset_at": 1_800_100_000.0},
	}}
	windows := parseCodexWindows(payload)
	if len(windows) != 2 || windows[0].RemainingPercent == nil || *windows[0].RemainingPercent != 75 {
		t.Fatalf("windows = %#v", windows)
	}
	if got := statusFromWindows(windows); got != "available" {
		t.Fatalf("status = %q", got)
	}
}

func TestParseAntigravityModels(t *testing.T) {
	payload := map[string]any{"models": map[string]any{
		"claude-sonnet": map[string]any{"quotaInfo": map[string]any{"remainingFraction": 0.25, "resetTime": "2026-07-27T12:00:00Z"}},
	}}
	models := parseAntigravityModels(payload)
	if len(models) != 1 || models[0].RemainingPercent == nil || *models[0].RemainingPercent != 25 {
		t.Fatalf("models = %#v", models)
	}
}

func TestMonthlyWindowSeconds(t *testing.T) {
	cases := []struct {
		name  string
		reset *time.Time
		want  int64
	}{
		{"nil reset", nil, 0},
		{"31-day month", timeRef(2026, 9, 1), 31 * 24 * 60 * 60},
		{"30-day month", timeRef(2026, 10, 1), 30 * 24 * 60 * 60},
		{"february non-leap", timeRef(2026, 3, 1), 28 * 24 * 60 * 60},
		{"february leap", timeRef(2028, 3, 1), 29 * 24 * 60 * 60},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := monthlyWindowSeconds(tc.reset); got != tc.want {
				t.Fatalf("monthlyWindowSeconds(%v) = %d, want %d", tc.reset, got, tc.want)
			}
		})
	}
}

func timeRef(year int, month time.Month, day int) *time.Time {
	t := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	return &t
}

func TestAccountIDFromJWT(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-1"}})
	token := "x." + base64.RawURLEncoding.EncodeToString(payload) + ".x"
	if got := accountIDFromJWT(token); got != "acct-1" {
		t.Fatalf("account id = %q", got)
	}
}

func TestHostHTTPResponseMatchesPluginAPIWireShape(t *testing.T) {
	var response hostHTTPResponse
	if err := json.Unmarshal([]byte(`{"StatusCode":200,"Headers":{"Content-Type":["application/json"]},"Body":"e30="}`), &response); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || string(response.Body) != "{}" {
		t.Fatalf("response = %#v", response)
	}
}

func TestConcurrentSnapshotRefreshIsDeduplicated(t *testing.T) {
	host := newFakeHost().withEntry(hostAuthFileEntry{AuthIndex: "a", Name: "unknown.json", Provider: "unknown"})
	runtime := newRuntime(host)
	runtime.applyConfig(pluginConfig{CacheTTL: 30 * time.Minute, RequestTimeout: time.Second, MaxConcurrency: 2})

	results := make(chan quotaResponse, 8)
	for i := 0; i < 8; i++ {
		go func() {
			snapshot, _, err := runtime.getSnapshot(context.Background(), false)
			if err != nil {
				t.Error(err)
				return
			}
			results <- snapshot
		}()
	}
	for i := 0; i < 8; i++ {
		<-results
	}
	if calls := host.listAuthCount(); calls != 1 {
		t.Fatalf("host list calls = %d, want 1", calls)
	}
}
