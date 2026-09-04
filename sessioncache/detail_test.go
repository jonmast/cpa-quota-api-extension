package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"testing"
)

// Like the capture tests, everything here drives the plugin through its RPC
// dispatch surface — synthetic host payloads in, management endpoint JSON out.

func dispatchManagement(t *testing.T, path string, query url.Values) managementResponse {
	t.Helper()
	request, err := json.Marshal(managementRequest{Method: "GET", Path: "/v0/management" + path, Query: query})
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
	if !env.OK {
		t.Fatalf("management wire=%s", raw)
	}
	var resp managementResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func fetchDetail(t *testing.T, sessionID string) sessionDetailPayload {
	t.Helper()
	resp := dispatchManagement(t, sessionDetailRoute, url.Values{"session_id": {sessionID}})
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	var payload sessionDetailPayload
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

// capture inserts one request row through the non-stream intercept dispatch.
func capture(t *testing.T, requestID, sessionID string, input, output, cacheRead, cacheCreation int64) {
	t.Helper()
	body := anthropicBody("claude-sonnet-4-5", input, output, cacheRead, cacheCreation)
	dispatchIntercept(t, methodResponseIntercept,
		interceptPayload(requestID, "claude-sonnet-4-5", claudeCodeRequest(sessionID), body, nil))
}

func classifications(payload sessionDetailPayload) []string {
	out := make([]string, 0, len(payload.Requests))
	for _, req := range payload.Requests {
		out = append(out, req.Classification)
	}
	return out
}

func TestSessionDetailOrderedRowsAndClassification(t *testing.T) {
	registerWithDB(t, filepath.Join(t.TempDir(), "capture.db"))

	// A realistic session shape: cold start writes the cache (neutral), the
	// next turn reads exactly what the prior turn left (hit, boundary
	// equality), then a mid-session cache blowout — cache_read drops below the
	// prior turn's cache_read + cache_creation (miss) — then recovery (hit).
	capture(t, "req-1", "sess-1", 1000, 50, 0, 900)   // neutral: first request
	capture(t, "req-2", "sess-1", 40, 60, 900, 300)   // hit: 900 >= 0+900
	capture(t, "req-3", "sess-1", 1400, 70, 200, 950) // miss: 200 < 900+300
	capture(t, "req-4", "sess-1", 30, 80, 1150, 100)  // hit: 1150 >= 200+950

	payload := fetchDetail(t, "sess-1")
	if payload.SessionID != "sess-1" || len(payload.Requests) != 4 {
		t.Fatalf("payload=%#v", payload)
	}
	got := classifications(payload)
	want := []string{"neutral", "hit", "miss", "hit"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("classifications=%v want %v", got, want)
		}
	}
	for i, req := range payload.Requests {
		if req.RequestID != fmt.Sprintf("req-%d", i+1) {
			t.Fatalf("order broken: %#v", payload.Requests)
		}
		wantContext := req.InputTokens + req.CacheReadTokens + req.CacheCreationTokens
		if req.ContextTokens != wantContext {
			t.Fatalf("context_tokens=%d want %d for %#v", req.ContextTokens, wantContext, req)
		}
		if req.Model != "claude-sonnet-4-5" || req.At.IsZero() || req.StatusCode != 200 {
			t.Fatalf("row=%#v", req)
		}
	}
	if payload.Requests[2].OutputTokens != 70 || payload.Requests[2].CacheCreationTokens != 950 {
		t.Fatalf("breakdown=%#v", payload.Requests[2])
	}
}

// TestSessionDetailJSONContract pins the exact wire keys the panel consumes.
func TestSessionDetailJSONContract(t *testing.T) {
	registerWithDB(t, filepath.Join(t.TempDir(), "capture.db"))
	capture(t, "req-1", "sess-json", 10, 5, 0, 20)

	resp := dispatchManagement(t, sessionDetailRoute, url.Values{"session_id": {"sess-json"}})
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	var raw struct {
		SessionID string                       `json:"session_id"`
		Requests  []map[string]json.RawMessage `json:"requests"`
	}
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		t.Fatal(err)
	}
	if raw.SessionID != "sess-json" || len(raw.Requests) != 1 {
		t.Fatalf("body=%s", resp.Body)
	}
	for _, key := range []string{
		"at", "request_id", "model", "stream", "status_code",
		"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens",
		"context_tokens", "classification",
	} {
		if _, ok := raw.Requests[0][key]; !ok {
			t.Fatalf("missing key %q in %s", key, resp.Body)
		}
	}
}

func TestFirstRequestOfEachSessionIsNeutral(t *testing.T) {
	registerWithDB(t, filepath.Join(t.TempDir(), "capture.db"))

	// Interleaved sessions: classification must compare against the previous
	// request of the SAME session, so session B's first request is neutral even
	// though session A already has rows, and A's second request classifies
	// against A's first, not B's.
	capture(t, "req-a1", "sess-a", 100, 5, 0, 500)
	capture(t, "req-b1", "sess-b", 100, 5, 0, 800) // would be a miss vs a1 if sessions leaked
	capture(t, "req-a2", "sess-a", 20, 5, 500, 40) // hit vs a1 (500 >= 0+500); miss vs b1 (500 < 800)

	if got := classifications(fetchDetail(t, "sess-a")); got[0] != "neutral" || got[1] != "hit" {
		t.Fatalf("sess-a=%v", got)
	}
	if got := classifications(fetchDetail(t, "sess-b")); len(got) != 1 || got[0] != "neutral" {
		t.Fatalf("sess-b=%v", got)
	}
}

func TestMidSessionCacheBlowoutIsMiss(t *testing.T) {
	registerWithDB(t, filepath.Join(t.TempDir(), "capture.db"))

	// Growth, then compaction/cache-bust: the fourth request re-reads less than
	// the third left behind — even though its own cache_read is far from zero.
	capture(t, "req-1", "sess-blow", 900, 10, 0, 850)
	capture(t, "req-2", "sess-blow", 50, 10, 850, 200)
	capture(t, "req-3", "sess-blow", 60, 10, 1050, 300)
	capture(t, "req-4", "sess-blow", 70, 10, 1300, 100) // 1300 < 1050+300 -> miss

	got := classifications(fetchDetail(t, "sess-blow"))
	want := []string{"neutral", "hit", "hit", "miss"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("classifications=%v want %v", got, want)
		}
	}
}

func TestSessionDetailRequiresSessionID(t *testing.T) {
	registerWithDB(t, filepath.Join(t.TempDir(), "capture.db"))
	if resp := dispatchManagement(t, sessionDetailRoute, nil); resp.StatusCode != 400 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
}

func TestSessionDetailUnknownSessionIs404(t *testing.T) {
	registerWithDB(t, filepath.Join(t.TempDir(), "capture.db"))
	capture(t, "req-1", "sess-known", 10, 5, 0, 20)
	if resp := dispatchManagement(t, sessionDetailRoute, url.Values{"session_id": {"sess-missing"}}); resp.StatusCode != 404 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
}
