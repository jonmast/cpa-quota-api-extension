package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type captureStore struct{ db *sql.DB }

func openCaptureStore(path string) (*captureStore, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, err
		}
		_ = os.Chmod(dir, 0o750)
	}
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	store := &captureStore{db: db}
	if err = store.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		_ = os.Chmod(candidate, 0o600)
	}
	return store, nil
}

func (s *captureStore) migrate() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS requests (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		at TEXT NOT NULL,
		request_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		model TEXT NOT NULL,
		stream INTEGER NOT NULL,
		status_code INTEGER NOT NULL,
		input_tokens INTEGER NOT NULL,
		output_tokens INTEGER NOT NULL,
		cache_read_tokens INTEGER NOT NULL,
		cache_creation_tokens INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_requests_session ON requests(session_id, id);`)
	return err
}

func (s *captureStore) insertRow(row requestRow) error {
	_, err := s.db.Exec(`INSERT INTO requests (
		at, request_id, session_id, model, stream, status_code,
		input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.At.UTC().Format(time.RFC3339Nano), row.RequestID, row.SessionID, row.Model,
		boolToInt(row.Stream), row.StatusCode, row.Input, row.Output, row.CacheRead, row.CacheCreation)
	return err
}

// sessionList returns sessions ordered by most recent activity. last_model is
// the model of the newest row in the session (insertion order breaks timestamp
// ties).
func (s *captureStore) sessionList(limit int) ([]sessionSummary, error) {
	rows, err := s.db.Query(`SELECT o.session_id, COUNT(*) AS request_count, MAX(o.at) AS last_seen,
		(SELECT i.model FROM requests i WHERE i.session_id = o.session_id ORDER BY i.id DESC LIMIT 1) AS last_model
	FROM requests o
	GROUP BY o.session_id
	ORDER BY MAX(o.id) DESC
	LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sessions := make([]sessionSummary, 0, 16)
	for rows.Next() {
		var summary sessionSummary
		var lastSeen string
		if err := rows.Scan(&summary.SessionID, &summary.RequestCount, &lastSeen, &summary.LastModel); err != nil {
			return nil, err
		}
		if parsed, parseErr := time.Parse(time.RFC3339Nano, lastSeen); parseErr == nil {
			summary.LastSeen = parsed
		}
		sessions = append(sessions, summary)
	}
	return sessions, rows.Err()
}

func (s *captureStore) close() {
	if s != nil && s.db != nil {
		_ = s.db.Close()
	}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
