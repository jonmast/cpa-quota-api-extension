package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestHealthClassificationOnlyActiveRoutableAndExpiredRetry(t *testing.T) {
	now := time.Now().UTC()
	for _, tt := range []struct {
		entry    hostAuthFileEntry
		obs      healthObservation
		state    string
		routable bool
	}{
		{hostAuthFileEntry{Status: "pending"}, healthObservation{}, "unknown", false},
		{hostAuthFileEntry{Status: "refreshing"}, healthObservation{}, "unknown", false},
		{hostAuthFileEntry{Status: "active", NextRetryAfter: now.Add(-time.Second)}, healthObservation{LastFailureAt: now.Add(-time.Minute), LastStatusCode: 429, RetryAt: now.Add(-time.Second)}, "healthy", true},
		{hostAuthFileEntry{Status: "active", Unavailable: true}, healthObservation{}, "unavailable", false},
	} {
		got := classifyAccountHealth(now, tt.entry, tt.obs, 3)
		if got.State != tt.state || got.Routable != tt.routable {
			t.Fatalf("%#v => %#v", tt, got)
		}
	}
}

func TestHealthStoreOrdersEventsAndCountsOnlyAfterLastSuccess(t *testing.T) {
	cfg := defaultConfig()
	cfg.DatabasePath = filepath.Join(t.TempDir(), "health.db")
	cfg.FailureWindow = time.Hour
	store, err := openHealthStore(cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	now := time.Now().UTC()
	if err := store.record(healthEvent{AuthIndex: "a", Provider: "codex", At: now, StatusCode: 500, FailureClass: "failed"}, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.record(healthEvent{AuthIndex: "a", Provider: "codex", At: now.Add(time.Minute), Success: true}, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.record(healthEvent{AuthIndex: "a", Provider: "codex", At: now.Add(-time.Minute), Success: true}, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.record(healthEvent{AuthIndex: "a", Provider: "codex", At: now.Add(-2 * time.Minute), StatusCode: 401, FailureClass: "unauthorized"}, cfg); err != nil {
		t.Fatal(err)
	}
	obs, err := store.observation("a", now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !obs.LastSuccessAt.Equal(now.Add(time.Minute)) || obs.LastStatusCode != 500 || obs.RecentFailures != 0 {
		t.Fatalf("observation %#v", obs)
	}
	if err := store.record(healthEvent{AuthIndex: "a", Provider: "codex", At: now.Add(2 * time.Minute), StatusCode: 500, FailureClass: "upstream_5xx"}, cfg); err != nil {
		t.Fatal(err)
	}
	obs, err = store.observation("a", now.Add(-time.Hour))
	if err != nil || obs.RecentFailures != 1 {
		t.Fatalf("observation after new failure %#v err=%v", obs, err)
	}
}

func TestUsageMalformedAndInvalidRecordsAlwaysSucceedAndCount(t *testing.T) {
	r := newRuntime(&fakeHost{})
	defer r.shutdown()
	r.applyConfig(pluginConfig{DatabasePath: "", HealthQueue: 64})
	for _, raw := range [][]byte{[]byte(`{`), []byte(`{"AuthIndex":""}`)} {
		var env envelope
		if err := json.Unmarshal(r.handleUsage(raw), &env); err != nil || !env.OK {
			t.Fatalf("response=%s", r.handleUsage(raw))
		}
	}
	if got := r.status(); string(got.Body) == "" {
		t.Fatal("missing status")
	}
}

func TestHealthFilterAndOpaqueCursor(t *testing.T) {
	cfg := defaultConfig()
	cfg.DatabasePath = filepath.Join(t.TempDir(), "health.db")
	store, err := openHealthStore(cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	now := time.Now().UTC()
	for _, event := range []healthEvent{{Provider: "codex", AuthIndex: "a", At: now, StatusCode: 401, FailureClass: "unauthorized"}, {Provider: "claude", AuthIndex: "b", At: now, StatusCode: 429, FailureClass: "rate_limited"}} {
		if err := store.record(event, cfg); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := store.incidentsFiltered(incidentFilter{AuthIndex: "a", StatusCode: 401, From: now.Add(-time.Minute), To: now.Add(time.Minute)}, 10, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("%#v %v", rows, err)
	}
	if _, err := opaqueIDCursor("123"); err == nil {
		t.Fatal("plain numeric cursor accepted")
	}
	if got, err := opaqueIDCursor(opaqueCursor(rows[0].ID)); err != nil || got != rows[0].ID {
		t.Fatalf("%d %v", got, err)
	}
}

func TestConfigRegistrationParity(t *testing.T) {
	want := []string{"database-path", "health-refresh-interval", "health-history-interval", "failure-window", "degraded-failure-threshold", "incident-retention", "incident-max-rows", "history-retention", "history-max-rows", "usage-queue-size", "profile-timezone", "webhook-url", "webhook-timeout", "alert-lost-threshold", "alert-degraded-threshold", "alert-cooldown"}
	got := map[string]bool{}
	for _, field := range pluginRegistration().Metadata.ConfigFields {
		got[field.Name] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Fatalf("missing config registration %s", name)
		}
	}
	for _, text := range []string{"health-refresh-interval: 9s", "usage-queue-size: 63", "webhook-url: ftp://bad", "incident-max-rows: 99"} {
		if _, err := decodeLifecycleConfig([]byte(`{"config_yaml":` + jsonQuote(text) + `}`)); err == nil {
			t.Fatalf("accepted invalid config %q", text)
		}
	}
	cfg, err := decodeLifecycleConfig([]byte(`{"config_yaml":` + jsonQuote("health-refresh-interval: 10s\nhealth-history-interval: 10s\nusage-queue-size: 64\nwebhook-url: https://example.test\nalert-lost-threshold: 0") + `}`))
	if err != nil || cfg.LostThreshold != 0 {
		t.Fatalf("%#v %v", cfg, err)
	}
}
func jsonQuote(s string) string { raw, _ := json.Marshal(s); return string(raw) }

func TestHealthStoreMigratesExistingSchemaAndUsesOwnerPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "health.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE observations (auth_index TEXT PRIMARY KEY, provider TEXT NOT NULL, last_status_code INTEGER NOT NULL DEFAULT 0, last_failure_at TEXT, last_success_at TEXT, retry_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := openHealthStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != healthSchemaVersion {
		t.Fatalf("version=%d err=%v", version, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("database permissions %o", info.Mode().Perm())
	}
}

func TestDatabaseInitFailureDoesNotBreakQuotaRoutes(t *testing.T) {
	r := newRuntime(&fakeHost{})
	defer r.shutdown()
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.DatabasePath = filepath.Join(parent, "health.db")
	r.applyConfig(cfg)
	if response := r.handleManagement(managementRequest{Method: http.MethodGet, Path: quotaRoute}); response.StatusCode != http.StatusOK {
		t.Fatalf("quota route status=%d body=%s", response.StatusCode, response.Body)
	}
}

func TestHealthReconfigureAndShutdownConcurrent(t *testing.T) {
	r := newRuntime(&fakeHost{entries: []hostAuthFileEntry{{AuthIndex: "a", Provider: "codex", Status: "active"}}})
	cfg := defaultConfig()
	cfg.DatabasePath = filepath.Join(t.TempDir(), "health.db")
	cfg.HealthQueue = 64
	r.applyConfig(cfg)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				r.enqueueUsage(healthEvent{AuthIndex: "a", Provider: "codex", At: time.Now(), Success: true})
				r.applyConfig(cfg)
			}
		}()
	}
	wg.Wait()
	r.shutdown()
	if resp := r.handleManagement(managementRequest{Method: http.MethodGet, Path: quotaRoute}); resp.StatusCode != http.StatusBadGateway && resp.StatusCode != http.StatusOK {
		t.Fatalf("quota route broke: %d", resp.StatusCode)
	}
}
