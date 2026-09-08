package main

import (
	"net/http"
	"net/url"
	"time"
)

const sessionDetailRoute = "/plugins/cpa-session-cache/v1/session"

const (
	classificationHit     = "hit"
	classificationMiss    = "miss"
	classificationNeutral = "neutral"
)

// requestDetail is one ordered request row in the session-detail response,
// classified at read time. ContextTokens is the bar height the panel renders:
// input + cache_read + cache_creation.
type requestDetail struct {
	At                  time.Time `json:"at"`
	RequestID           string    `json:"request_id"`
	Model               string    `json:"model"`
	Stream              bool      `json:"stream"`
	StatusCode          int       `json:"status_code"`
	InputTokens         int64     `json:"input_tokens"`
	OutputTokens        int64     `json:"output_tokens"`
	CacheReadTokens     int64     `json:"cache_read_tokens"`
	CacheCreationTokens int64     `json:"cache_creation_tokens"`
	ContextTokens       int64     `json:"context_tokens"`
	Classification      string    `json:"classification"`
}

type sessionDetailPayload struct {
	SessionID    string          `json:"session_id"`
	CacheHitRate float64         `json:"cache_hit_rate"`
	Requests     []requestDetail `json:"requests"`
}

// cacheHitRate is the token-weighted CHR shared by the list and detail
// endpoints: cache_read tokens over context tokens (input + cache_read +
// cache_creation). A session with no context tokens has a rate of 0 rather
// than an undefined ratio.
func cacheHitRate(cacheRead, contextTokens int64) float64 {
	if contextTokens <= 0 {
		return 0
	}
	return float64(cacheRead) / float64(contextTokens)
}

// sessionCacheHitRate aggregates CHR over one session's rows, matching the
// SQL aggregate the list endpoint computes.
func sessionCacheHitRate(rows []requestRow) float64 {
	var cacheRead, contextTokens int64
	for _, row := range rows {
		cacheRead += row.CacheRead
		contextTokens += row.Input + row.CacheRead + row.CacheCreation
	}
	return cacheHitRate(cacheRead, contextTokens)
}

// classifyRows applies the cache-miss rule decided on issue #17, computed at
// read time from stored rows — never stored — so the rule can evolve without
// migration: a request is a miss when its cache_read_input_tokens is less than
// the previous same-session request's cache_read + cache_creation (the cache no
// longer contains what the prior turn left in it); the first request of a
// session has no predecessor and is neutral; everything else is a hit.
func classifyRows(rows []requestRow) []requestDetail {
	details := make([]requestDetail, 0, len(rows))
	for i, row := range rows {
		classification := classificationNeutral
		if i > 0 {
			previous := rows[i-1]
			if row.CacheRead < previous.CacheRead+previous.CacheCreation {
				classification = classificationMiss
			} else {
				classification = classificationHit
			}
		}
		details = append(details, requestDetail{
			At:                  row.At,
			RequestID:           row.RequestID,
			Model:               row.Model,
			Stream:              row.Stream,
			StatusCode:          row.StatusCode,
			InputTokens:         row.Input,
			OutputTokens:        row.Output,
			CacheReadTokens:     row.CacheRead,
			CacheCreationTokens: row.CacheCreation,
			ContextTokens:       row.Input + row.CacheRead + row.CacheCreation,
			Classification:      classification,
		})
	}
	return details
}

// sessionRequests returns every stored row of one session in capture order
// (insertion order breaks timestamp ties, matching the list endpoint).
func (s *captureStore) sessionRequests(sessionID string) ([]requestRow, error) {
	rows, err := s.db.Query(`SELECT at, request_id, model, stream, status_code,
		input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens
	FROM requests WHERE session_id = ? ORDER BY id ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]requestRow, 0, 16)
	for rows.Next() {
		var row requestRow
		var at string
		var stream int
		if err := rows.Scan(&at, &row.RequestID, &row.Model, &stream, &row.StatusCode,
			&row.Input, &row.Output, &row.CacheRead, &row.CacheCreation); err != nil {
			return nil, err
		}
		if parsed, parseErr := time.Parse(time.RFC3339Nano, at); parseErr == nil {
			row.At = parsed
		}
		row.SessionID = sessionID
		row.Stream = stream != 0
		out = append(out, row)
	}
	return out, rows.Err()
}

func (r *runtimeState) sessionDetailResponse(query url.Values) managementResponse {
	sessionID := firstQuery(query, "session_id")
	if sessionID == "" {
		return jsonError(http.StatusBadRequest, "missing_session_id", "session_id query parameter is required")
	}
	r.mu.Lock()
	store, storeErr := r.store, r.storeErr
	r.mu.Unlock()
	if store == nil {
		message := "session capture database is unavailable"
		if storeErr != "" {
			message = storeErr
		}
		return jsonError(http.StatusServiceUnavailable, "store_unavailable", message)
	}
	rows, err := store.sessionRequests(sessionID)
	if err != nil {
		return jsonError(http.StatusInternalServerError, "query_failed", err.Error())
	}
	if len(rows) == 0 {
		return jsonError(http.StatusNotFound, "session_not_found", "no requests recorded for this session")
	}
	return jsonResponse(http.StatusOK, sessionDetailPayload{
		SessionID:    sessionID,
		CacheHitRate: sessionCacheHitRate(rows),
		Requests:     classifyRows(rows),
	})
}
