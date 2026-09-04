package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// panelRequest drives the resource route through the RPC dispatch surface.
func panelRequest(t *testing.T, method, path string) managementResponse {
	t.Helper()
	request, err := json.Marshal(managementRequest{Method: method, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleMethod(methodManagementHandle, request)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var resp managementResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestPanelResourceIsStaticAndHardened(t *testing.T) {
	registerWithDB(t, filepath.Join(t.TempDir(), "capture.db"))

	resp := panelRequest(t, "GET", "/v0/resource/plugins/"+pluginID+panelResourcePath)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	if got := resp.Headers.Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("content-type=%q", got)
	}
	for name, want := range map[string]string{
		"Cache-Control":          "no-store",
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := resp.Headers.Get(name); got != want {
			t.Fatalf("%s=%q want %q", name, got, want)
		}
	}
	csp := resp.Headers.Get("Content-Security-Policy")
	for _, directive := range []string{"default-src 'none'", "connect-src 'self'", "frame-ancestors 'self'", "object-src 'none'"} {
		if !strings.Contains(csp, directive) {
			t.Fatalf("CSP missing %q: %s", directive, csp)
		}
	}
}

func TestPanelResourceRejectsOtherPathsAndMethods(t *testing.T) {
	registerWithDB(t, filepath.Join(t.TempDir(), "capture.db"))
	for _, tc := range []struct{ method, path string }{
		{"POST", "/v0/resource/plugins/" + pluginID + panelResourcePath},
		{"GET", "/v0/resource/plugins/" + pluginID + "/panel/nope"},
		{"GET", "/v0/resource/plugins/other-plugin" + panelResourcePath},
	} {
		if resp := panelRequest(t, tc.method, tc.path); resp.StatusCode != 404 {
			t.Fatalf("%s %s status=%d", tc.method, tc.path, resp.StatusCode)
		}
	}
}

func TestPanelDocumentIsSelfContainedAndConstrained(t *testing.T) {
	doc := panelDocument
	// The panel must consume the same JSON endpoints as any other client and
	// carry both views plus the classification color scheme.
	for _, want := range []string{
		sessionsRoute, sessionDetailRoute, "session_id=",
		"sessions-view", "detail-view", ".bar.miss", ".bar.neutral",
		"context_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens",
		"cli-proxy-auth", "enc::v1::", "textContent", "AbortController",
	} {
		if !strings.Contains(doc, want) {
			t.Fatalf("panel missing %q", want)
		}
	}
	// No external assets, no dynamic code evaluation, no unsafe DOM sinks.
	for _, forbidden := range []string{
		"<script src=", "<link ", "@import", "eval(", "new Function", ".innerHTML",
		"localStorage.setItem", "sessionStorage", "indexedDB", "http://", "https://",
	} {
		if strings.Contains(doc, forbidden) {
			t.Fatalf("panel contains forbidden %q", forbidden)
		}
	}
}
