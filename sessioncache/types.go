package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

const (
	pluginID      = "cpa-session-cache"
	pluginVersion = "0.1.0"

	sessionsRoute = "/plugins/cpa-session-cache/v1/sessions"
)

const (
	abiVersion uint32 = 1
	// schemaVersion 4 is required for interceptor capabilities; schema >= 3
	// guarantees OriginalRequest on the stream header-init chunk.
	schemaVersion uint32 = 4
)

const (
	methodPluginRegister     = "plugin.register"
	methodPluginReconfigure  = "plugin.reconfigure"
	methodPluginShutdown     = "plugin.shutdown"
	methodPluginQuiesce      = "plugin.quiesce"
	methodManagementRegister = "management.register"
	methodManagementHandle   = "management.handle"

	// Dispatch methods proven by prototypes/cache-session-poc; do not re-derive.
	methodResponseIntercept   = "response.intercept_after"
	methodResponseStreamChunk = "response.intercept_stream_chunk"
)

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

// Capability keys proven by the prototype: response_interceptor and
// response_stream_interceptor (NOT stream_chunk_interceptor).
type registrationCapabilities struct {
	ResponseInterceptor       bool `json:"response_interceptor"`
	ResponseStreamInterceptor bool `json:"response_stream_interceptor"`
	ManagementAPI             bool `json:"management_api"`
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      metadata                 `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type metadata struct {
	Name             string        `json:"Name"`
	Version          string        `json:"Version"`
	Author           string        `json:"Author"`
	GitHubRepository string        `json:"GitHubRepository"`
	ConfigFields     []configField `json:"ConfigFields"`
}

type configField struct {
	Name        string `json:"Name"`
	Type        string `json:"Type"`
	Description string `json:"Description"`
}

type managementRegistrationResponse struct {
	Routes    []managementRoute `json:"routes,omitempty"`
	Resources []resourceRoute   `json:"resources,omitempty"`
}

type managementRoute struct {
	Method      string `json:"Method"`
	Path        string `json:"Path"`
	Description string `json:"Description,omitempty"`
}

type resourceRoute struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu"`
	Description string `json:"Description,omitempty"`
}

type managementRequest struct {
	Method         string
	Path           string
	Headers        http.Header
	Query          url.Values
	Body           []byte
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type managementResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}

// Interceptor payload JSON keys are the Go field names of the host's pluginapi
// structs; []byte fields travel base64-encoded (Go's default []byte JSON form).
type responseInterceptRequest struct {
	RequestID       string
	Model           string
	Stream          bool
	RequestHeaders  map[string][]string
	OriginalRequest []byte
	Body            []byte
	StatusCode      int
}

type streamChunkInterceptRequest struct {
	RequestID       string
	Model           string
	RequestHeaders  map[string][]string
	OriginalRequest []byte
	Body            []byte
	ChunkIndex      int
}

// interceptResponse marshals to {} when zero-valued; the host keeps the current
// body when the returned body is empty, giving byte-identical pass-through.
type interceptResponse struct {
	Headers      map[string][]string `json:"Headers,omitempty"`
	Body         []byte              `json:"Body,omitempty"`
	ClearHeaders []string            `json:"ClearHeaders,omitempty"`
}

type anthropicUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

type requestRow struct {
	At            time.Time
	RequestID     string
	SessionID     string
	Model         string
	Stream        bool
	StatusCode    int
	Input         int64
	Output        int64
	CacheRead     int64
	CacheCreation int64
}

// sessionSummary carries the per-session aggregates the list endpoint reports.
// CacheHitRate is the token-weighted cache hit rate (CHR): the session's
// cache_read tokens over its context tokens (input + cache_read +
// cache_creation), in [0,1]; it is 0 for a session with no context tokens.
type sessionSummary struct {
	SessionID    string    `json:"session_id"`
	RequestCount int64     `json:"request_count"`
	LastModel    string    `json:"last_model"`
	LastSeen     time.Time `json:"last_seen"`
	CacheHitRate float64   `json:"cache_hit_rate"`
}

type sessionListResponse struct {
	Sessions []sessionSummary `json:"sessions"`
}
