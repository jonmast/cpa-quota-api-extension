package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestHealthStoreResolvesIncidentAndListsPages(t *testing.T) {
	cfg := defaultConfig()
	cfg.DatabasePath = filepath.Join(t.TempDir(), "health.db")
	store, err := openHealthStore(cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	now := time.Now().UTC()
	if err := store.record(healthEvent{Provider: "codex", AuthIndex: "a", At: now, StatusCode: 429, FailureClass: "rate_limited"}, cfg); err != nil {
		t.Fatal(err)
	}
	items, err := store.incidents("codex", "", 10, 0)
	if err != nil || len(items) != 1 || items[0].ResolvedAt != nil {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	if err := store.record(healthEvent{Provider: "codex", AuthIndex: "a", At: now.Add(time.Second), Success: true}, cfg); err != nil {
		t.Fatal(err)
	}
	items, err = store.incidents("", "", 10, 0)
	if err != nil || items[0].ResolvedAt == nil {
		t.Fatalf("items=%#v err=%v", items, err)
	}
}

func TestHealthHistoryPersistsAggregates(t *testing.T) {
	cfg := defaultConfig()
	cfg.DatabasePath = filepath.Join(t.TempDir(), "health.db")
	store, err := openHealthStore(cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	snapshot := healthSnapshot{GeneratedAt: time.Now().UTC(), Capacity: healthCapacity{Total: 3, Routable: 2, Lost: 1, Degraded: 1}, ByState: map[string]int{"healthy": 1, "degraded": 1, "disabled": 1}, ByProvider: map[string]int{"codex": 2, "claude": 1}}
	if err := store.saveHistory(snapshot, cfg); err != nil {
		t.Fatal(err)
	}
	rows, err := store.history(10, 0)
	if err != nil || len(rows) != 1 || rows[0].ByState["disabled"] != 1 || rows[0].ByProvider["codex"] != 2 {
		t.Fatalf("rows=%#v err=%v", rows, err)
	}
}

func TestHealthRoutesAndInvalidPagination(t *testing.T) {
	cfg := defaultConfig()
	cfg.DatabasePath = filepath.Join(t.TempDir(), "health.db")
	r := newRuntime(&fakeHost{entries: []hostAuthFileEntry{{AuthIndex: "a", Name: "a.json", Provider: "codex", Status: "active"}}})
	defer r.shutdown()
	r.applyConfig(cfg)
	resp := r.handleManagement(managementRequest{Method: http.MethodGet, Path: healthRoute, Query: map[string][]string{"limit": {"501"}}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	resp = r.handleManagement(managementRequest{Method: http.MethodGet, Path: healthRoute, Query: map[string][]string{}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	resp = r.handleManagement(managementRequest{Method: http.MethodGet, Path: incidentsRoute, Query: map[string][]string{"cursor": {"bad"}}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestPoolWebhookHasNoAccountIdentity(t *testing.T) {
	requests := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var value map[string]any
		_ = json.NewDecoder(r.Body).Decode(&value)
		raw, _ := json.Marshal(value)
		requests <- raw
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	cfg := defaultConfig()
	cfg.DatabasePath = filepath.Join(t.TempDir(), "health.db")
	cfg.WebhookURL = server.URL
	cfg.AlertCooldown = 0
	r := newRuntime(&fakeHost{})
	defer r.shutdown()
	r.applyConfig(cfg)
	// Establishing a healthy baseline must not emit a recovery.
	r.maybeAlert(healthSnapshot{GeneratedAt: time.Now(), Capacity: healthCapacity{}}, cfg)
	select {
	case raw := <-requests:
		t.Fatalf("unexpected baseline webhook: %s", raw)
	case <-time.After(50 * time.Millisecond):
	}
	r.maybeAlert(healthSnapshot{GeneratedAt: time.Now(), Capacity: healthCapacity{Lost: 1}}, cfg)
	select {
	case raw := <-requests:
		if string(raw) == "" || containsSensitiveJSON(raw) || string(raw) == "" {
			t.Fatalf("unsafe webhook: %s", raw)
		}
	case <-time.After(time.Second):
		t.Fatal("webhook not sent")
	}
}

func TestWebhookBreachRecoveryCooldownAndFailedRetry(t *testing.T) {
	requests := make(chan map[string]any, 8)
	var failFirst bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		requests <- payload
		if failFirst {
			failFirst = false
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	cfg := defaultConfig()
	cfg.DatabasePath = filepath.Join(t.TempDir(), "health.db")
	cfg.WebhookURL = server.URL
	cfg.AlertCooldown = time.Hour
	r := newRuntime(&fakeHost{})
	defer r.shutdown()
	r.applyConfig(cfg)
	breach := healthSnapshot{GeneratedAt: time.Now(), Capacity: healthCapacity{Lost: 1}}
	healthy := healthSnapshot{GeneratedAt: time.Now(), Capacity: healthCapacity{}}

	r.maybeAlert(breach, cfg)
	assertWebhookEvent(t, requests, "breach")
	r.maybeAlert(breach, cfg)
	assertNoWebhook(t, requests)
	r.maybeAlert(healthy, cfg)
	assertWebhookEvent(t, requests, "recovery")
	r.maybeAlert(breach, cfg)
	select {
	case payload := <-requests:
		if payload["event"] != "breach" {
			t.Fatalf("event=%v", payload["event"])
		}
	case <-time.After(time.Second):
		store := r.healthStore
		state, _ := store.alert("pool_breach")
		pending, _ := store.alert("pool_breach_pending")
		recovery, _ := store.alert("pool_recovery_pending")
		t.Fatalf("timed out: state=%q pending=%q recovery=%q", state, pending, recovery)
	}
	store := r.healthStore
	r.webhookOps.Lock()
	if err := store.setAlert("pool_alert_at", time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		r.webhookOps.Unlock()
		t.Fatal(err)
	}
	r.webhookOps.Unlock()
	r.deliverPendingAlert(store, cfg, context.Background(), breach)
	select {
	case payload := <-requests:
		if payload["event"] != "breach" {
			t.Fatalf("event=%v", payload["event"])
		}
	case <-time.After(time.Second):
		state, _ := store.alert("pool_breach")
		pending, _ := store.alert("pool_breach_pending")
		last, _ := store.alert("pool_alert_at")
		t.Fatalf("reminder timed out: state=%q pending=%q last=%q", state, pending, last)
	}

	// A failed initial breach remains pending and is retried on the next refresh.
	r.shutdown()
	failFirst = true
	r = newRuntime(&fakeHost{})
	defer r.shutdown()
	cfg.DatabasePath = filepath.Join(t.TempDir(), "retry.db")
	r.applyConfig(cfg)
	r.maybeAlert(breach, cfg)
	assertWebhookEvent(t, requests, "breach")
	r.maybeAlert(breach, cfg)
	assertWebhookEvent(t, requests, "breach")
	deadline := time.Now().Add(time.Second)
	for {
		r.mu.Lock()
		webhookError := r.webhookError
		r.mu.Unlock()
		if webhookError == "" || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	var status statusResponse
	if err := json.Unmarshal(r.status().Body, &status); err != nil || status.WebhookError != "" {
		t.Fatalf("status=%#v err=%v", status, err)
	}
}

func assertWebhookEvent(t *testing.T, requests <-chan map[string]any, want string) {
	t.Helper()
	select {
	case payload := <-requests:
		if payload["event"] != want {
			t.Fatalf("event=%v, want %s", payload["event"], want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s webhook", want)
	}
}
func assertNoWebhook(t *testing.T, requests <-chan map[string]any) {
	t.Helper()
	select {
	case payload := <-requests:
		t.Fatalf("unexpected webhook: %#v", payload)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestUsageHandleAlwaysSucceeds(t *testing.T) {
	r := newRuntime(&fakeHost{})
	defer r.shutdown()
	r.applyConfig(pluginConfig{DatabasePath: "", HealthQueue: 1})
	raw, err := handleMethod(methodUsageHandle, []byte(`{"AuthIndex":"a","APIKey":"secret","Failed":true,"Failure":{"StatusCode":500,"Body":"secret"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if json.Unmarshal(raw, &env) != nil || !env.OK {
		t.Fatalf("response=%s", raw)
	}
	_ = context.Background()
}
