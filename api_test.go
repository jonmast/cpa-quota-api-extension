package main

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestQuotaEndpointPaginates(t *testing.T) {
	runtime := newRuntime(&fakeHost{})
	runtime.cfg = defaultConfig()
	runtime.hasSnapshot = true
	runtime.snapshot = quotaResponse{GeneratedAt: time.Now(), CacheTTL: "30m0s", Accounts: []accountQuota{
		{Name: "a", Provider: "codex", Status: "available"},
		{Name: "b", Provider: "codex", Status: "exhausted"},
	}}
	resp := runtime.handleManagement(managementRequest{Method: http.MethodGet, Path: quotaRoute, Query: map[string][]string{"limit": {"1"}}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var payload struct {
		Accounts []accountQuota `json:"accounts"`
		Page     struct {
			NextCursor string `json:"next_cursor"`
		} `json:"page"`
	}
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Accounts) != 1 || payload.Page.NextCursor == "" {
		t.Fatalf("payload=%s", resp.Body)
	}
}

func TestUnknownRouteReturns404(t *testing.T) {
	runtime := newRuntime(&fakeHost{})
	resp := runtime.handleManagement(managementRequest{Method: http.MethodGet, Path: "/plugins/cpa-quota-api-extension/nope"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}
