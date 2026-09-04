package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type runtimeState struct {
	mu       sync.Mutex
	cfg      pluginConfig
	store    *captureStore
	storeErr string
	streams  map[string]*streamState
}

var activeRuntime = newRuntime()

func newRuntime() *runtimeState {
	return &runtimeState{cfg: defaultConfig(), streams: map[string]*streamState{}}
}

func (r *runtimeState) applyConfig(cfg pluginConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.store != nil {
		r.store.close()
		r.store = nil
	}
	r.cfg = cfg
	r.storeErr = ""
	r.streams = map[string]*streamState{}
	store, err := openCaptureStore(cfg.DatabasePath)
	if err != nil {
		r.storeErr = err.Error()
		return
	}
	r.store = store
}

func (r *runtimeState) shutdown() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.store != nil {
		r.store.close()
		r.store = nil
	}
	r.streams = map[string]*streamState{}
}

// handleResponseIntercept records Anthropic-format non-streaming responses and
// always answers with an empty intercept response (pass-through). Bodies that
// are not Anthropic messages are skipped silently.
func (r *runtimeState) handleResponseIntercept(raw []byte) ([]byte, error) {
	var req responseInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode response intercept request: %w", err)
	}
	if row, ok := anthropicRow(req); ok {
		r.record(row)
	}
	return okEnvelope(interceptResponse{})
}

// handleStreamChunk captures streamed Anthropic responses and always answers
// with an empty intercept response, so SSE chunks pass through unmodified.
// The header-init chunk (ChunkIndex == -1) opens per-request state; payload
// chunks accumulate usage from message_start/message_delta events; the row
// commits on message_stop. Every dispatch first evicts state whose last chunk
// is older than the stream-state TTL.
func (r *runtimeState) handleStreamChunk(raw []byte) ([]byte, error) {
	var req streamChunkInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode stream chunk request: %w", err)
	}
	now := time.Now().UTC()
	var committed []requestRow
	r.mu.Lock()
	r.evictStaleStreamsLocked(now)
	if req.ChunkIndex == -1 {
		r.streams[req.RequestID] = &streamState{
			sessionID: extractSessionID(req.OriginalRequest, req.RequestHeaders),
			model:     req.Model,
			lastChunk: now,
		}
	} else {
		committed = r.applyStreamEventsLocked(req, now)
	}
	store := r.store
	r.mu.Unlock()
	if store != nil {
		for _, row := range committed {
			// Storage failures are swallowed: the observer must never disturb
			// live traffic.
			_ = store.insertRow(row)
		}
	}
	return okEnvelope(interceptResponse{})
}

// applyStreamEventsLocked folds one payload chunk's SSE events into the
// request's stream state and returns any rows that became complete. A chunk
// for an unknown request (header-init missed or state already evicted) opens a
// fallback state in the "unknown" session bucket; without a message_start it
// can never commit a row, so unparseable streams age out silently.
func (r *runtimeState) applyStreamEventsLocked(req streamChunkInterceptRequest, now time.Time) []requestRow {
	state := r.streams[req.RequestID]
	if state == nil {
		state = &streamState{sessionID: "unknown", model: req.Model}
		r.streams[req.RequestID] = state
	}
	state.lastChunk = now
	var committed []requestRow
	for _, ev := range parseSSEData(req.Body) {
		switch ev.Type {
		case "message_start":
			state.sawStart = true
			state.usage.InputTokens = ev.Message.Usage.InputTokens
			state.usage.CacheReadInputTokens = ev.Message.Usage.CacheReadInputTokens
			state.usage.CacheCreationInputTokens = ev.Message.Usage.CacheCreationInputTokens
			if ev.Message.Model != "" {
				state.model = ev.Message.Model
			}
		case "message_delta":
			if ev.Usage.OutputTokens > 0 {
				state.usage.OutputTokens = ev.Usage.OutputTokens
			}
		case "message_stop":
			if state.sawStart {
				committed = append(committed, requestRow{
					At:            now,
					RequestID:     req.RequestID,
					SessionID:     state.sessionID,
					Model:         state.model,
					Stream:        true,
					StatusCode:    200,
					Input:         state.usage.InputTokens,
					Output:        state.usage.OutputTokens,
					CacheRead:     state.usage.CacheReadInputTokens,
					CacheCreation: state.usage.CacheCreationInputTokens,
				})
			}
			delete(r.streams, req.RequestID)
		}
	}
	return committed
}

// evictStaleStreamsLocked drops stream state whose last chunk is older than
// the configured TTL, covering client disconnects that never deliver
// message_stop.
func (r *runtimeState) evictStaleStreamsLocked(now time.Time) {
	ttl := r.cfg.StreamStateTTL
	if ttl <= 0 {
		ttl = defaultStreamStateTTL
	}
	for id, state := range r.streams {
		if now.Sub(state.lastChunk) > ttl {
			delete(r.streams, id)
		}
	}
}

// anthropicRow parses the response body as an Anthropic message. Only bodies
// with type "message" and a usage block produce a row; anything else reports
// not-ok so the caller skips it without error.
func anthropicRow(req responseInterceptRequest) (requestRow, bool) {
	var body struct {
		Type  string          `json:"type"`
		Model string          `json:"model"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return requestRow{}, false
	}
	if body.Type != "message" || len(body.Usage) == 0 || string(body.Usage) == "null" {
		return requestRow{}, false
	}
	var usage anthropicUsage
	if err := json.Unmarshal(body.Usage, &usage); err != nil {
		return requestRow{}, false
	}
	model := body.Model
	if model == "" {
		model = req.Model
	}
	return requestRow{
		At:            time.Now().UTC(),
		RequestID:     req.RequestID,
		SessionID:     extractSessionID(req.OriginalRequest, req.RequestHeaders),
		Model:         model,
		Stream:        false,
		StatusCode:    req.StatusCode,
		Input:         usage.InputTokens,
		Output:        usage.OutputTokens,
		CacheRead:     usage.CacheReadInputTokens,
		CacheCreation: usage.CacheCreationInputTokens,
	}, true
}

// record inserts a captured row. Storage failures are swallowed: the observer
// must never disturb live traffic.
func (r *runtimeState) record(row requestRow) {
	r.mu.Lock()
	store := r.store
	r.mu.Unlock()
	if store == nil {
		return
	}
	_ = store.insertRow(row)
}

func (r *runtimeState) handleManagement(req managementRequest) managementResponse {
	path := strings.TrimPrefix(req.Path, "/v0/management")
	switch {
	case req.Method == http.MethodGet && path == sessionsRoute:
		return r.sessionListResponse(req.Query)
	default:
		return jsonError(http.StatusNotFound, "not_found", "plugin route not found")
	}
}

func (r *runtimeState) sessionListResponse(query map[string][]string) managementResponse {
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
	limit := parseLimit(firstQuery(query, "limit"), 100, 1000)
	sessions, err := store.sessionList(limit)
	if err != nil {
		return jsonError(http.StatusInternalServerError, "query_failed", err.Error())
	}
	return jsonResponse(http.StatusOK, sessionListResponse{Sessions: sessions})
}

func firstQuery(query map[string][]string, key string) string {
	values := query[key]
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

func parseLimit(raw string, fallback, max int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 1 {
		return fallback
	}
	if value > max {
		return max
	}
	return value
}

func jsonResponse(status int, payload any) managementResponse {
	body, _ := json.Marshal(payload)
	return managementResponse{StatusCode: status, Headers: http.Header{"Content-Type": {"application/json; charset=utf-8"}, "Cache-Control": {"private, no-store"}}, Body: body}
}

func jsonError(status int, code, message string) managementResponse {
	return jsonResponse(status, map[string]any{"error": map[string]any{"code": code, "message": message}})
}
