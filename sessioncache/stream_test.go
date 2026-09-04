package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// Streaming capture tests drive the plugin exclusively through its RPC
// dispatch surface: header-init and payload chunks in the host's wire shape
// (Go-field-name keys, base64 []byte fields) carrying realistic Anthropic SSE
// fixtures, asserted via the management endpoint and the SQLite capture file.

func registerWithYAML(t *testing.T, yaml string) {
	t.Helper()
	request, err := json.Marshal(map[string]any{"config_yaml": yaml})
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

// headerInitPayload builds the ChunkIndex == -1 chunk: the only chunk
// guaranteed to carry OriginalRequest under schema >= 3.
func headerInitPayload(requestID, model string, originalRequest []byte, headers map[string][]string) []byte {
	payload := map[string]any{
		"RequestID":       requestID,
		"Model":           model,
		"RequestHeaders":  headers,
		"OriginalRequest": base64.StdEncoding.EncodeToString(originalRequest),
		"ChunkIndex":      -1,
	}
	raw, _ := json.Marshal(payload)
	return raw
}

func chunkPayload(requestID, model string, index int, body []byte) []byte {
	payload := map[string]any{
		"RequestID":  requestID,
		"Model":      model,
		"Body":       base64.StdEncoding.EncodeToString(body),
		"ChunkIndex": index,
	}
	raw, _ := json.Marshal(payload)
	return raw
}

func sseMessageStart(model string, input, cacheRead, cacheCreation int64) []byte {
	return []byte(fmt.Sprintf("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_01\",\"type\":\"message\",\"role\":\"assistant\",\"model\":%q,\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":%d,\"output_tokens\":1,\"cache_read_input_tokens\":%d,\"cache_creation_input_tokens\":%d}}}\n\n",
		model, input, cacheRead, cacheCreation))
}

func sseTextDelta(text string) []byte {
	return []byte(fmt.Sprintf("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\n", text))
}

func sseMessageDelta(output int64) []byte {
	return []byte(fmt.Sprintf("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":%d}}\n\n", output))
}

func sseMessageStop() []byte {
	return []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
}

// dispatchStreamChunk sends one chunk and asserts the empty pass-through
// response ({} = host keeps the current body byte-identical).
func dispatchStreamChunk(t *testing.T, payload []byte) {
	t.Helper()
	result := dispatchIntercept(t, methodResponseStreamChunk, payload)
	if string(result) != "{}" {
		t.Fatalf("stream chunk response must be empty pass-through, got %s", result)
	}
}

func TestStreamCaptureProducesSingleRow(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "capture.db")
	registerWithDB(t, dbPath)

	sessionID := "66666666-7777-8888-9999-000000000000"
	dispatchStreamChunk(t, headerInitPayload("req-s1", "claude-sonnet-4", claudeCodeRequest(sessionID), nil))
	dispatchStreamChunk(t, chunkPayload("req-s1", "claude-sonnet-4", 0, sseMessageStart("claude-sonnet-4-5", 1200, 900, 300)))
	dispatchStreamChunk(t, chunkPayload("req-s1", "claude-sonnet-4", 1, sseTextDelta("Hello")))
	dispatchStreamChunk(t, chunkPayload("req-s1", "claude-sonnet-4", 2, sseMessageDelta(85)))
	dispatchStreamChunk(t, chunkPayload("req-s1", "claude-sonnet-4", 3, sseMessageStop()))

	list := fetchSessions(t)
	if len(list.Sessions) != 1 {
		t.Fatalf("sessions=%#v", list.Sessions)
	}
	session := list.Sessions[0]
	if session.SessionID != sessionID || session.RequestCount != 1 || session.LastModel != "claude-sonnet-4-5" {
		t.Fatalf("session=%#v", session)
	}

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
	var gotSession, model string
	var stream, statusCode int
	var input, output, cacheRead, cacheCreation int64
	err = db.QueryRow(`SELECT session_id, model, stream, status_code,
		input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens FROM requests WHERE request_id = 'req-s1'`).
		Scan(&gotSession, &model, &stream, &statusCode, &input, &output, &cacheRead, &cacheCreation)
	if err != nil {
		t.Fatal(err)
	}
	if gotSession != sessionID || model != "claude-sonnet-4-5" || stream != 1 || statusCode != 200 {
		t.Fatalf("row=%q %q stream=%d status=%d", gotSession, model, stream, statusCode)
	}
	if input != 1200 || output != 85 || cacheRead != 900 || cacheCreation != 300 {
		t.Fatalf("tokens=%d/%d/%d/%d", input, output, cacheRead, cacheCreation)
	}
}

func TestInterleavedConcurrentStreamsAttributeCorrectSessions(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "capture.db")
	registerWithDB(t, dbPath)

	dispatchStreamChunk(t, headerInitPayload("req-a", "m", claudeCodeRequest("sess-aaaa"), nil))
	dispatchStreamChunk(t, headerInitPayload("req-b", "m", claudeCodeRequest("sess-bbbb"), nil))
	dispatchStreamChunk(t, chunkPayload("req-a", "m", 0, sseMessageStart("model-a", 100, 10, 1)))
	dispatchStreamChunk(t, chunkPayload("req-b", "m", 0, sseMessageStart("model-b", 200, 20, 2)))
	dispatchStreamChunk(t, chunkPayload("req-b", "m", 1, sseMessageDelta(7)))
	dispatchStreamChunk(t, chunkPayload("req-b", "m", 2, sseMessageStop()))
	dispatchStreamChunk(t, chunkPayload("req-a", "m", 1, sseMessageDelta(5)))
	dispatchStreamChunk(t, chunkPayload("req-a", "m", 2, sseMessageStop()))

	list := fetchSessions(t)
	if len(list.Sessions) != 2 {
		t.Fatalf("sessions=%#v", list.Sessions)
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	want := map[string]struct {
		session string
		model   string
		input   int64
		output  int64
	}{
		"req-a": {"sess-aaaa", "model-a", 100, 5},
		"req-b": {"sess-bbbb", "model-b", 200, 7},
	}
	for requestID, expect := range want {
		var session, model string
		var input, output int64
		err := db.QueryRow(`SELECT session_id, model, input_tokens, output_tokens FROM requests WHERE request_id = ?`, requestID).
			Scan(&session, &model, &input, &output)
		if err != nil {
			t.Fatalf("%s: %v", requestID, err)
		}
		if session != expect.session || model != expect.model || input != expect.input || output != expect.output {
			t.Fatalf("%s: got %q %q %d/%d want %#v", requestID, session, model, input, output, expect)
		}
	}
}

func TestAbandonedStreamEvictedByTTLProducesNoRow(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "capture.db")
	registerWithYAML(t, "database-path: "+dbPath+"\nstream-state-ttl: 50ms")

	dispatchStreamChunk(t, headerInitPayload("req-gone", "m", claudeCodeRequest("sess-gone"), nil))
	dispatchStreamChunk(t, chunkPayload("req-gone", "m", 0, sseMessageStart("claude", 500, 50, 5)))

	// Client disconnects: no message_stop ever arrives. After the TTL, the next
	// dispatch evicts the state, so even a late message_stop cannot commit a row.
	time.Sleep(120 * time.Millisecond)
	dispatchStreamChunk(t, chunkPayload("req-gone", "m", 1, sseMessageStop()))

	list := fetchSessions(t)
	if len(list.Sessions) != 0 {
		t.Fatalf("sessions=%#v", list.Sessions)
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rows=%d", count)
	}
}

func TestStreamSurvivesWithinTTL(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "capture.db")
	registerWithYAML(t, "database-path: "+dbPath+"\nstream-state-ttl: 10s")

	dispatchStreamChunk(t, headerInitPayload("req-slow", "m", claudeCodeRequest("sess-slow"), nil))
	dispatchStreamChunk(t, chunkPayload("req-slow", "m", 0, sseMessageStart("claude", 42, 0, 0)))
	time.Sleep(20 * time.Millisecond)
	dispatchStreamChunk(t, chunkPayload("req-slow", "m", 1, sseMessageDelta(3)))
	dispatchStreamChunk(t, chunkPayload("req-slow", "m", 2, sseMessageStop()))

	list := fetchSessions(t)
	if len(list.Sessions) != 1 || list.Sessions[0].SessionID != "sess-slow" || list.Sessions[0].RequestCount != 1 {
		t.Fatalf("sessions=%#v", list.Sessions)
	}
}

func TestNonAnthropicStreamRecordsNothing(t *testing.T) {
	registerWithDB(t, filepath.Join(t.TempDir(), "capture.db"))

	dispatchStreamChunk(t, headerInitPayload("req-oai", "gpt-x", []byte(`{"messages":[]}`), nil))
	openAIChunk := []byte("data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n")
	dispatchStreamChunk(t, chunkPayload("req-oai", "gpt-x", 0, openAIChunk))

	list := fetchSessions(t)
	if len(list.Sessions) != 0 {
		t.Fatalf("sessions=%#v", list.Sessions)
	}
}

func TestOrphanPayloadChunksFallBackToUnknownWithoutRow(t *testing.T) {
	registerWithDB(t, filepath.Join(t.TempDir(), "capture.db"))

	// message_stop without a prior header-init or message_start: state is
	// created in the unknown bucket but never saw message_start, so no row.
	dispatchStreamChunk(t, chunkPayload("req-orphan", "m", 4, sseMessageStop()))

	list := fetchSessions(t)
	if len(list.Sessions) != 0 {
		t.Fatalf("sessions=%#v", list.Sessions)
	}
}

func TestStreamStateTTLConfigRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"nope", "-1m", "0s", "25h"} {
		request, _ := json.Marshal(map[string]any{"config_yaml": "stream-state-ttl: " + value})
		if _, err := handleMethod(methodPluginRegister, request); err == nil {
			t.Fatalf("stream-state-ttl %q accepted", value)
		}
	}
}
