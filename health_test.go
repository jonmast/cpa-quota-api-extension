package main

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestClassifyAccountHealthPrecedence(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		entry       hostAuthFileEntry
		observation healthObservation
		wantState   string
		wantRoute   bool
	}{
		{name: "disabled wins", entry: hostAuthFileEntry{Disabled: true, Unavailable: true}, observation: healthObservation{LastStatusCode: 429, LastFailureAt: now}, wantState: "disabled"},
		{name: "unauthorized", entry: hostAuthFileEntry{}, observation: healthObservation{LastStatusCode: 401, LastFailureAt: now}, wantState: "unauthorized"},
		{name: "forbidden", entry: hostAuthFileEntry{}, observation: healthObservation{LastStatusCode: 403, LastFailureAt: now}, wantState: "forbidden"},
		{name: "rate limited with host retry", entry: hostAuthFileEntry{Unavailable: true, NextRetryAfter: now.Add(time.Minute)}, observation: healthObservation{LastStatusCode: 429, LastFailureAt: now}, wantState: "rate_limited"},
		{name: "rate limited without retry timestamp", entry: hostAuthFileEntry{Status: "active"}, observation: healthObservation{LastStatusCode: 429, LastFailureAt: now}, wantState: "rate_limited"},
		{name: "unavailable", entry: hostAuthFileEntry{Unavailable: true}, wantState: "unavailable"},
		{name: "degraded remains routable", entry: hostAuthFileEntry{Status: "active"}, observation: healthObservation{RecentFailures: 3}, wantState: "degraded", wantRoute: true},
		{name: "healthy", entry: hostAuthFileEntry{Status: "active"}, wantState: "healthy", wantRoute: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyAccountHealth(now, tt.entry, tt.observation, 3)
			if got.State != tt.wantState || got.Routable != tt.wantRoute {
				t.Fatalf("health = %#v, want state=%q routable=%v", got, tt.wantState, tt.wantRoute)
			}
		})
	}
}

func TestUsageRecordWireShapeAndSanitization(t *testing.T) {
	raw := []byte(`{"Provider":"codex","Model":"gpt-5","AuthIndex":"auth-1","APIKey":"secret","RequestedAt":"2026-08-02T12:00:00Z","Latency":3000000000,"Failed":true,"Failure":{"StatusCode":429,"Body":"token secret"},"ResponseHeaders":{"Retry-After":["60"]}}`)
	var record usageRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	event, ok := sanitizeUsageRecord(record, time.Date(2026, 8, 2, 12, 0, 4, 0, time.UTC))
	if !ok {
		t.Fatal("failed record was not accepted")
	}
	if event.StatusCode != 429 || event.FailureClass != "rate_limited" || event.LatencyMS != 3000 {
		t.Fatalf("event = %#v", event)
	}
	if !event.RetryAt.Equal(time.Date(2026, 8, 2, 12, 1, 3, 0, time.UTC)) {
		t.Fatalf("retry_at = %v", event.RetryAt)
	}
	encoded, _ := json.Marshal(event)
	if string(encoded) == "" || containsSensitiveJSON(encoded) {
		t.Fatalf("sanitized event leaked sensitive data: %s", encoded)
	}
}

func TestUsageFailureClassification(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	for status, want := range map[int]string{0: "transport_error", 400: "request_error", 500: "upstream_5xx"} {
		event, ok := sanitizeUsageRecord(usageRecord{AuthIndex: "a", Failed: true, Failure: usageFailure{StatusCode: status}}, now)
		if !ok || event.FailureClass != want {
			t.Fatalf("status %d => %#v, want %s", status, event, want)
		}
	}
	retryAt := now.Add(2 * time.Minute).UTC().Format(http.TimeFormat)
	event, _ := sanitizeUsageRecord(usageRecord{AuthIndex: "a", RequestedAt: now, Failed: true, Failure: usageFailure{StatusCode: 429}, ResponseHeaders: http.Header{"Retry-After": {retryAt}}}, now)
	if !event.RetryAt.Equal(now.Add(2 * time.Minute)) {
		t.Fatalf("retry_at = %v", event.RetryAt)
	}
}

func TestHealthRoutesAreRegistered(t *testing.T) {
	routes := managementRegistration().Routes
	want := map[string]bool{healthRoute: false, incidentsRoute: false, historyRoute: false}
	for _, route := range routes {
		if route.Method == http.MethodGet {
			if _, ok := want[route.Path]; ok {
				want[route.Path] = true
			}
		}
	}
	for route, found := range want {
		if !found {
			t.Fatalf("route %s is not registered", route)
		}
	}
}

func TestRegistrationIncludesUsageCapability(t *testing.T) {
	registration := pluginRegistration()
	if !registration.Capabilities.ManagementAPI || !registration.Capabilities.UsagePlugin {
		t.Fatalf("capabilities = %#v", registration.Capabilities)
	}
}
