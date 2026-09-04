package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
)

// The tests in this file drive the plugin exclusively through its RPC dispatch
// surface — the same JSON payloads the host sends (Go-field-name keys, base64
// []byte fields) — and assert on externally visible outcomes: management
// endpoint JSON and rows in the plugin's SQLite database file.

func registerWithDB(t *testing.T, dbPath string) {
	t.Helper()
	request, err := json.Marshal(map[string]any{"config_yaml": "database-path: " + dbPath})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleMethod(methodPluginRegister, request)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Fatalf("register wire=%s", raw)
	}
	t.Cleanup(func() { activeRuntime.shutdown() })
}

// interceptPayload builds the exact host wire shape for response.intercept_after.
func interceptPayload(requestID, model string, originalRequest, body []byte, headers map[string][]string) []byte {
	payload := map[string]any{
		"RequestID":       requestID,
		"Model":           model,
		"Stream":          false,
		"RequestHeaders":  headers,
		"OriginalRequest": base64.StdEncoding.EncodeToString(originalRequest),
		"Body":            base64.StdEncoding.EncodeToString(body),
		"StatusCode":      200,
	}
	raw, _ := json.Marshal(payload)
	return raw
}

func dispatchIntercept(t *testing.T, method string, payload []byte) json.RawMessage {
	t.Helper()
	raw, err := handleMethod(method, payload)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Fatalf("intercept wire=%s", raw)
	}
	return env.Result
}

func fetchSessions(t *testing.T) sessionListResponse {
	t.Helper()
	request, err := json.Marshal(managementRequest{Method: "GET", Path: "/v0/management" + sessionsRoute})
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
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	var list sessionListResponse
	if err := json.Unmarshal(resp.Body, &list); err != nil {
		t.Fatal(err)
	}
	return list
}

func anthropicBody(model string, input, output, cacheRead, cacheCreation int64) []byte {
	return []byte(fmt.Sprintf(`{"id":"msg_01","type":"message","role":"assistant","model":%q,`+
		`"content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn",`+
		`"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d}}`,
		model, input, output, cacheRead, cacheCreation))
}

func claudeCodeRequest(sessionID string) []byte {
	return []byte(fmt.Sprintf(`{"model":"claude-sonnet-4","metadata":{"user_id":"user_f00ba4_account__session_%s"},"messages":[]}`, sessionID))
}

func TestNonStreamCaptureProducesRowAndSession(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "capture.db")
	registerWithDB(t, dbPath)

	body := anthropicBody("claude-sonnet-4-5", 1200, 85, 900, 300)
	result := dispatchIntercept(t, methodResponseIntercept,
		interceptPayload("req-1", "claude-sonnet-4", claudeCodeRequest("11111111-2222-3333-4444-555555555555"), body, nil))
	if string(result) != "{}" {
		t.Fatalf("intercept response must be empty pass-through, got %s", result)
	}

	list := fetchSessions(t)
	if len(list.Sessions) != 1 {
		t.Fatalf("sessions=%#v", list.Sessions)
	}
	session := list.Sessions[0]
	if session.SessionID != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("session_id=%q", session.SessionID)
	}
	if session.RequestCount != 1 || session.LastModel != "claude-sonnet-4-5" || session.LastSeen.IsZero() {
		t.Fatalf("session=%#v", session)
	}

	// The database file is the plugin's externally visible capture artifact:
	// exactly one row with the session ID and all four token counts.
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rows=%d", count)
	}
	var requestID, sessionID, model string
	var stream, statusCode int
	var input, output, cacheRead, cacheCreation int64
	err = db.QueryRow(`SELECT request_id, session_id, model, stream, status_code,
		input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens FROM requests`).
		Scan(&requestID, &sessionID, &model, &stream, &statusCode, &input, &output, &cacheRead, &cacheCreation)
	if err != nil {
		t.Fatal(err)
	}
	if requestID != "req-1" || sessionID != "11111111-2222-3333-4444-555555555555" || model != "claude-sonnet-4-5" {
		t.Fatalf("row=%q %q %q", requestID, sessionID, model)
	}
	if stream != 0 || statusCode != 200 {
		t.Fatalf("stream=%d status=%d", stream, statusCode)
	}
	if input != 1200 || output != 85 || cacheRead != 900 || cacheCreation != 300 {
		t.Fatalf("tokens=%d/%d/%d/%d", input, output, cacheRead, cacheCreation)
	}
}

func TestSessionListOrderedByLastActivity(t *testing.T) {
	registerWithDB(t, filepath.Join(t.TempDir(), "capture.db"))

	for i, sessionID := range []string{"aaa", "bbb", "aaa"} {
		body := anthropicBody(fmt.Sprintf("model-%d", i), 10, 5, 0, 0)
		dispatchIntercept(t, methodResponseIntercept,
			interceptPayload(fmt.Sprintf("req-%d", i), "m", claudeCodeRequest(sessionID), body, nil))
	}

	list := fetchSessions(t)
	if len(list.Sessions) != 2 {
		t.Fatalf("sessions=%#v", list.Sessions)
	}
	if list.Sessions[0].SessionID != "aaa" || list.Sessions[0].RequestCount != 2 || list.Sessions[0].LastModel != "model-2" {
		t.Fatalf("first=%#v", list.Sessions[0])
	}
	if list.Sessions[1].SessionID != "bbb" || list.Sessions[1].RequestCount != 1 || list.Sessions[1].LastModel != "model-1" {
		t.Fatalf("second=%#v", list.Sessions[1])
	}
}

func TestSessionFallbackChain(t *testing.T) {
	registerWithDB(t, filepath.Join(t.TempDir(), "capture.db"))

	cases := []struct {
		requestID       string
		originalRequest []byte
		headers         map[string][]string
		want            string
	}{
		{"req-meta", claudeCodeRequest("deadbeef"), nil, "deadbeef"},
		{"req-conv", []byte(`{"conversation_id":"conv-42"}`), nil, "conv:conv-42"},
		{"req-header", []byte(`{"messages":[]}`), map[string][]string{"X-Session-Id": {"hdr-7"}}, "header:hdr-7"},
		{"req-none", []byte(`{"messages":[]}`), nil, "unknown"},
	}
	for _, tc := range cases {
		body := anthropicBody("claude", 1, 1, 0, 0)
		dispatchIntercept(t, methodResponseIntercept,
			interceptPayload(tc.requestID, "claude", tc.originalRequest, body, tc.headers))
	}

	list := fetchSessions(t)
	got := map[string]bool{}
	for _, session := range list.Sessions {
		got[session.SessionID] = true
	}
	for _, tc := range cases {
		if !got[tc.want] {
			t.Fatalf("missing session %q in %#v", tc.want, list.Sessions)
		}
	}
	if len(list.Sessions) != len(cases) {
		t.Fatalf("sessions=%#v", list.Sessions)
	}
}

func TestUnparseableBodiesAreSkippedSilently(t *testing.T) {
	registerWithDB(t, filepath.Join(t.TempDir(), "capture.db"))

	bodies := [][]byte{
		[]byte("not json at all"),
		[]byte(`{"object":"chat.completion","model":"gpt-x","usage":{"prompt_tokens":5,"completion_tokens":2}}`),
		[]byte(`{"type":"message","model":"claude"}`), // no usage block
		{},
	}
	for i, body := range bodies {
		result := dispatchIntercept(t, methodResponseIntercept,
			interceptPayload(fmt.Sprintf("req-%d", i), "m", claudeCodeRequest("skip"), body, nil))
		if string(result) != "{}" {
			t.Fatalf("body %d: response=%s", i, result)
		}
	}

	list := fetchSessions(t)
	if len(list.Sessions) != 0 {
		t.Fatalf("sessions=%#v", list.Sessions)
	}
}

func TestStreamChunkIsPassThroughAndRecordsNothing(t *testing.T) {
	registerWithDB(t, filepath.Join(t.TempDir(), "capture.db"))

	headerInit := map[string]any{
		"RequestID":       "req-s1",
		"Model":           "claude",
		"RequestHeaders":  map[string][]string{},
		"OriginalRequest": base64.StdEncoding.EncodeToString(claudeCodeRequest("stream-session")),
		"ChunkIndex":      -1,
	}
	payloadChunk := map[string]any{
		"RequestID":  "req-s1",
		"Model":      "claude",
		"Body":       base64.StdEncoding.EncodeToString([]byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\n")),
		"ChunkIndex": 0,
	}
	for _, chunk := range []map[string]any{headerInit, payloadChunk} {
		raw, _ := json.Marshal(chunk)
		result := dispatchIntercept(t, methodResponseStreamChunk, raw)
		if string(result) != "{}" {
			t.Fatalf("stream chunk response=%s", result)
		}
	}

	list := fetchSessions(t)
	if len(list.Sessions) != 0 {
		t.Fatalf("sessions=%#v", list.Sessions)
	}
}

func TestUnknownManagementRouteReturns404(t *testing.T) {
	registerWithDB(t, filepath.Join(t.TempDir(), "capture.db"))

	request, _ := json.Marshal(managementRequest{Method: "GET", Path: "/v0/management/plugins/cpa-session-cache/nope"})
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
	if resp.StatusCode != 404 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}
