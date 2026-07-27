package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

const (
	pluginID      = "cpa-quota-extension"
	pluginVersion = "0.1.0-dev"

	quotaRoute   = "/plugins/cpa-quota-extension/v1/quotas"
	accountRoute = "/plugins/cpa-quota-extension/v1/accounts"
	statusRoute  = "/plugins/cpa-quota-extension/v1/status"
)

type pluginConfig struct {
	CacheTTL        time.Duration
	RequestTimeout  time.Duration
	MaxConcurrency  int
	IncludeDisabled bool
}

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

type registrationCapabilities struct {
	ManagementAPI bool `json:"management_api"`
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
	Logo             string        `json:"Logo"`
	ConfigFields     []configField `json:"ConfigFields"`
}

type configField struct {
	Name        string `json:"Name"`
	Type        string `json:"Type"`
	Description string `json:"Description"`
}

type managementRegistrationResponse struct {
	Routes []managementRoute `json:"routes,omitempty"`
}

type managementRoute struct {
	Method      string `json:"Method"`
	Path        string `json:"Path"`
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

type accountQuota struct {
	AuthIndex       string            `json:"auth_index,omitempty"`
	Name            string            `json:"name"`
	Provider        string            `json:"provider"`
	Email           string            `json:"email,omitempty"`
	ProjectID       string            `json:"project_id,omitempty"`
	Plan            string            `json:"plan,omitempty"`
	CredentialState string            `json:"credential_state"`
	Status          string            `json:"status"`
	Supported       bool              `json:"supported"`
	FetchedAt       time.Time         `json:"fetched_at,omitempty"`
	Windows         []quotaWindow     `json:"windows,omitempty"`
	Models          []modelQuota      `json:"models,omitempty"`
	Error           *quotaError       `json:"error,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

type quotaWindow struct {
	ID               string     `json:"id"`
	UsedPercent      *float64   `json:"used_percent,omitempty"`
	RemainingPercent *float64   `json:"remaining_percent,omitempty"`
	ResetAt          *time.Time `json:"reset_at,omitempty"`
	WindowSeconds    int64      `json:"window_seconds,omitempty"`
}

type modelQuota struct {
	Model            string     `json:"model"`
	RemainingPercent *float64   `json:"remaining_percent,omitempty"`
	ResetAt          *time.Time `json:"reset_at,omitempty"`
}

type quotaError struct {
	Code           string `json:"code"`
	Message        string `json:"message"`
	UpstreamStatus int    `json:"upstream_status,omitempty"`
}

type poolSummary struct {
	Total      int            `json:"total"`
	Supported  int            `json:"supported"`
	Available  int            `json:"available"`
	Exhausted  int            `json:"exhausted"`
	Disabled   int            `json:"disabled"`
	Errors     int            `json:"errors"`
	ByProvider map[string]int `json:"by_provider"`
	ByStatus   map[string]int `json:"by_status"`
}

type quotaResponse struct {
	GeneratedAt time.Time      `json:"generated_at"`
	CacheTTL    string         `json:"cache_ttl"`
	Cached      bool           `json:"cached"`
	RefreshMode string         `json:"refresh_mode"`
	Summary     poolSummary    `json:"summary"`
	Accounts    []accountQuota `json:"accounts"`
}

type statusResponse struct {
	PluginID        string    `json:"plugin_id"`
	Version         string    `json:"version"`
	CacheTTL        string    `json:"cache_ttl"`
	RequestTimeout  string    `json:"request_timeout"`
	MaxConcurrency  int       `json:"max_concurrency"`
	IncludeDisabled bool      `json:"include_disabled"`
	HasSnapshot     bool      `json:"has_snapshot"`
	GeneratedAt     time.Time `json:"generated_at,omitempty"`
	Refreshing      bool      `json:"refreshing"`
}
