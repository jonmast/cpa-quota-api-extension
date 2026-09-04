package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// Retention tests drive the plugin exclusively through its RPC dispatch
// surface: config through plugin.register/plugin.reconfigure, rows through
// response.intercept_after, outcomes through the management endpoint and the
// SQLite capture file.

func rowCount(t *testing.T, dbPath string) int {
	t.Helper()
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func captureRow(t *testing.T, requestID, sessionID string) {
	t.Helper()
	body := anthropicBody("claude", 10, 5, 0, 0)
	result := dispatchIntercept(t, methodResponseIntercept,
		interceptPayload(requestID, "claude", claudeCodeRequest(sessionID), body, nil))
	if string(result) != "{}" {
		t.Fatalf("intercept response=%s", result)
	}
}

func TestAgePruningRemovesExpiredRowsOnWrite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "capture.db")
	registerWithYAML(t, "database-path: "+dbPath+"\nretention: 200ms")

	captureRow(t, "req-old-1", "sess-old-1")
	captureRow(t, "req-old-2", "sess-old-2")
	time.Sleep(350 * time.Millisecond)
	captureRow(t, "req-fresh", "sess-fresh")

	list := fetchSessions(t)
	if len(list.Sessions) != 1 || list.Sessions[0].SessionID != "sess-fresh" {
		t.Fatalf("sessions=%#v", list.Sessions)
	}
	if count := rowCount(t, dbPath); count != 1 {
		t.Fatalf("rows=%d", count)
	}
}

func TestMaxRowsCapEvictsOldestFirst(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "capture.db")
	registerWithYAML(t, "database-path: "+dbPath+"\nmax-rows: 2")

	for i := 1; i <= 3; i++ {
		captureRow(t, fmt.Sprintf("req-%d", i), fmt.Sprintf("sess-%d", i))
	}

	list := fetchSessions(t)
	if len(list.Sessions) != 2 {
		t.Fatalf("sessions=%#v", list.Sessions)
	}
	got := map[string]bool{}
	for _, session := range list.Sessions {
		got[session.SessionID] = true
	}
	if got["sess-1"] || !got["sess-2"] || !got["sess-3"] {
		t.Fatalf("oldest row must be evicted first: %#v", list.Sessions)
	}
	if count := rowCount(t, dbPath); count != 2 {
		t.Fatalf("rows=%d", count)
	}
}

func TestReconfigureHonorsTightenedRetention(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "capture.db")
	registerWithDB(t, dbPath)

	for i := 1; i <= 3; i++ {
		captureRow(t, fmt.Sprintf("req-%d", i), fmt.Sprintf("sess-%d", i))
	}
	if count := rowCount(t, dbPath); count != 3 {
		t.Fatalf("rows before reconfigure=%d", count)
	}

	// plugin.reconfigure with a tighter cap prunes immediately, without
	// waiting for the next captured request.
	request, err := json.Marshal(map[string]any{"config_yaml": "database-path: " + dbPath + "\nmax-rows: 1"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleMethod(methodPluginReconfigure, request)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Fatalf("reconfigure wire=%s", raw)
	}

	list := fetchSessions(t)
	if len(list.Sessions) != 1 || list.Sessions[0].SessionID != "sess-3" {
		t.Fatalf("sessions=%#v", list.Sessions)
	}
	if count := rowCount(t, dbPath); count != 1 {
		t.Fatalf("rows=%d", count)
	}
}

func TestAllFourConfigFieldsDeclared(t *testing.T) {
	raw, err := handleMethod(methodPluginRegister, nil)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var reg struct {
		Metadata struct {
			ConfigFields []configField `json:"ConfigFields"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(env.Result, &reg); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"database-path": false, "retention": false, "max-rows": false, "stream-state-ttl": false}
	for _, field := range reg.Metadata.ConfigFields {
		if _, ok := want[field.Name]; !ok {
			t.Fatalf("unexpected config field %q", field.Name)
		}
		if field.Description == "" {
			t.Fatalf("config field %q has no description", field.Name)
		}
		want[field.Name] = true
	}
	for name, found := range want {
		if !found {
			t.Fatalf("missing config field %q", name)
		}
	}
	if reg.Metadata.ConfigFields[2].Name != "max-rows" || reg.Metadata.ConfigFields[2].Type != "integer" {
		t.Fatalf("max-rows must be declared as integer: %#v", reg.Metadata.ConfigFields)
	}
	activeRuntime.shutdown()
}

func TestInvalidConfigValuesFallBackWithoutFailingRegistration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "capture.db")
	// Every retention-related value is invalid; only database-path is valid.
	// Registration must still succeed and capture must run on defaults.
	registerWithYAML(t, "database-path: "+dbPath+
		"\nretention: banana\nmax-rows: -3\nstream-state-ttl: nope")

	captureRow(t, "req-1", "sess-1")

	// Default retention (30d) and max-rows (50k) are in effect: the row
	// survives pruning instead of being wiped by the bogus values.
	list := fetchSessions(t)
	if len(list.Sessions) != 1 || list.Sessions[0].SessionID != "sess-1" {
		t.Fatalf("sessions=%#v", list.Sessions)
	}
	if count := rowCount(t, dbPath); count != 1 {
		t.Fatalf("rows=%d", count)
	}
}

func TestGarbageLifecyclePayloadFallsBackToDefaults(t *testing.T) {
	raw, err := handleMethod(methodPluginRegister, []byte("this is not json"))
	if err != nil {
		t.Fatalf("registration must not fail on a malformed lifecycle payload: %v", err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Fatalf("wire=%s", raw)
	}
	activeRuntime.shutdown()
}
