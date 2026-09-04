package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestPanelResourceIsStaticAndHardened(t *testing.T) {
	host := &fakeHost{}
	r := newRuntime(host)
	defer r.shutdown()

	response := r.handleManagement(managementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource/plugins/" + pluginID + panelResourcePath,
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, response.Body)
	}
	if got := response.Headers.Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("content-type=%q", got)
	}
	for name, want := range map[string]string{
		"Cache-Control":          "no-store",
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := response.Headers.Get(name); got != want {
			t.Fatalf("%s=%q want %q", name, got, want)
		}
	}
	csp := response.Headers.Get("Content-Security-Policy")
	for _, directive := range []string{"default-src 'none'", "connect-src 'self'", "frame-ancestors 'self'", "object-src 'none'"} {
		if !strings.Contains(csp, directive) {
			t.Fatalf("CSP missing %q: %s", directive, csp)
		}
	}
	host.mu.Lock()
	listCalls := host.listCalls
	host.mu.Unlock()
	if listCalls != 0 {
		t.Fatalf("loading panel invoked auth listing %d times", listCalls)
	}
}

func TestPanelResourceRejectsOtherPathsAndMethods(t *testing.T) {
	r := newRuntime(&fakeHost{})
	defer r.shutdown()
	for _, req := range []managementRequest{
		{Method: http.MethodPost, Path: "/v0/resource/plugins/" + pluginID + panelResourcePath},
		{Method: http.MethodGet, Path: "/v0/resource/plugins/" + pluginID + "/panel/nope"},
	} {
		if response := r.handleManagement(req); response.StatusCode != http.StatusNotFound {
			t.Fatalf("request=%#v response=%#v", req, response)
		}
	}
}

func TestPanelDocumentContainsOnlyConstrainedLocalClient(t *testing.T) {
	doc := panelDocument
	for _, want := range []string{
		"Configuration", "API Explorer", "Documentation",
		quotaRoute, accountRoute, statusRoute, healthRoute, incidentsRoute, historyRoute, profileRoute,
		"cli-proxy-auth", "enc::v1::", "PATCH", "/plugins/" + pluginID + "/config",
		"textContent", "URLSearchParams", "AbortController",
	} {
		if !strings.Contains(doc, want) {
			t.Fatalf("panel missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"<script src=", "<link ", "eval(", "new Function", ".innerHTML", "localStorage.setItem", "sessionStorage", "indexedDB",
	} {
		if strings.Contains(doc, forbidden) {
			t.Fatalf("panel contains forbidden construct %q", forbidden)
		}
	}
}

func TestPanelQuotaOutlookRendersProjectionsAndHeatmap(t *testing.T) {
	doc := panelDocument
	for _, want := range []string{
		// The tab and its scaffolding.
		"Quota Outlook", "tab-usage", "usage-root", "usage-refresh",
		// The outlook client fetches the profile route alongside quotas.
		`profile:"` + profileRoute + `"`,
		// Plain-language projection line surfacing verdict, basis, confidence.
		"projected by reset", "clock alone would say",
		"on track", "tight", "will exhaust",
		"projected_used_percent", "naive_projected_percent", "projected_exhaustion_at",
		// Degraded states are visibly flagged, never hidden.
		"uniform basis", "low confidence",
		// Raw-percentages-only rendering guard for windows without projection.
		"if(window.projection)",
		// Heatmap: 24x2 grid from bucket weights, hours in the profile timezone.
		"heatmap", "hm-cell", "day_type", "bucket_scheme", "weight",
		"weekend_share", "token_coverage", "Hour of day in ",
	} {
		if !strings.Contains(doc, want) {
			t.Fatalf("panel missing %q", want)
		}
	}
	// The heatmap paints intensity with inline style computed from weights —
	// still no external assets involved.
	if !strings.Contains(doc, "color-mix(in srgb,var(--accent2) ") {
		t.Fatal("heatmap cell intensity styling missing")
	}
}
