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
}

var activeRuntime = newRuntime()

func newRuntime() *runtimeState {
	return &runtimeState{cfg: defaultConfig()}
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

// handleStreamChunk is pass-through only in this slice; streaming capture
// (header-init session extraction, message_start/delta/stop accumulation)
// arrives in a later slice.
func (r *runtimeState) handleStreamChunk(raw []byte) ([]byte, error) {
	var req streamChunkInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode stream chunk request: %w", err)
	}
	return okEnvelope(interceptResponse{})
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
