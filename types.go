package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

const (
	pluginID      = "cpa-quota-api-extension"
	pluginVersion = "0.3.0"

	panelResourcePath = "/panel"
	quotaRoute        = "/plugins/cpa-quota-api-extension/v1/quotas"
	accountRoute      = "/plugins/cpa-quota-api-extension/v1/accounts"
	statusRoute       = "/plugins/cpa-quota-api-extension/v1/status"
	healthRoute       = "/plugins/cpa-quota-api-extension/v1/health"
	incidentsRoute    = "/plugins/cpa-quota-api-extension/v1/incidents"
	historyRoute      = "/plugins/cpa-quota-api-extension/v1/history"
)

type pluginConfig struct {
	CacheTTL              time.Duration
	RequestTimeout        time.Duration
	MaxConcurrency        int
	IncludeDisabled       bool
	DatabasePath          string
	HealthRefreshInterval time.Duration
	HistoryInterval       time.Duration
	FailureWindow         time.Duration
	DegradedThreshold     int
	IncidentRetention     time.Duration
	IncidentMaxRows       int
	HistoryRetention      time.Duration
	HistoryMaxRows        int
	HealthQueue           int
	WebhookURL            string
	WebhookTimeout        time.Duration
	LostThreshold         int
	DegradedPoolThreshold int
	AlertCooldown         time.Duration
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
	UsagePlugin   bool `json:"usage_plugin"`
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
	Routes    []managementRoute `json:"routes,omitempty"`
	Resources []resourceRoute   `json:"resources,omitempty"`
}

type resourceRoute struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu"`
	Description string `json:"Description,omitempty"`
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
	AuthIndex         string            `json:"auth_index,omitempty"`
	Name              string            `json:"name"`
	Provider          string            `json:"provider"`
	Email             string            `json:"email,omitempty"`
	ProjectID         string            `json:"project_id,omitempty"`
	Plan              string            `json:"plan,omitempty"`
	CredentialState   string            `json:"credential_state"`
	Status            string            `json:"status"`
	Supported         bool              `json:"supported"`
	FetchedAt         time.Time         `json:"fetched_at,omitempty"`
	Windows           []quotaWindow     `json:"windows,omitempty"`
	Models            []modelQuota      `json:"models,omitempty"`
	BindingWindow     *bindingWindow    `json:"binding_window,omitempty"`
	ExtraUsedCredits  *int64            `json:"extra_used_credits,omitempty"`
	ExtraMonthlyLimit *int64            `json:"extra_monthly_limit,omitempty"`
	Error             *quotaError       `json:"error,omitempty"`
	Metadata          map[string]string `json:"metadata,omitempty"`
}

type quotaWindow struct {
	ID               string     `json:"id"`
	UsedPercent      *float64   `json:"used_percent,omitempty"`
	RemainingPercent *float64   `json:"remaining_percent,omitempty"`
	ResetAt          *time.Time `json:"reset_at,omitempty"`
	WindowSeconds    int64      `json:"window_seconds,omitempty"`
	// UsedDollars and LimitDollars are optional monetary figures retained for
	// client display. Percent fields stay authoritative for comparison across
	// providers; these are informational only.
	UsedDollars  *float64 `json:"used_dollars,omitempty"`
	LimitDollars *float64 `json:"limit_dollars,omitempty"`
}

type modelQuota struct {
	Model            string     `json:"model"`
	ModelName        string     `json:"model_name,omitempty"`
	RemainingPercent *float64   `json:"remaining_percent,omitempty"`
	ResetAt          *time.Time `json:"reset_at,omitempty"`
}

type bindingWindow struct {
	ID string `json:"id"`
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
	PluginID          string    `json:"plugin_id"`
	Version           string    `json:"version"`
	CacheTTL          string    `json:"cache_ttl"`
	RequestTimeout    string    `json:"request_timeout"`
	MaxConcurrency    int       `json:"max_concurrency"`
	IncludeDisabled   bool      `json:"include_disabled"`
	HasSnapshot       bool      `json:"has_snapshot"`
	GeneratedAt       time.Time `json:"generated_at,omitempty"`
	Refreshing        bool      `json:"refreshing"`
	HealthEnabled     bool      `json:"health_enabled"`
	HealthSnapshotAt  time.Time `json:"health_snapshot_at,omitempty"`
	DroppedUsageCount uint64    `json:"dropped_usage_count"`
	DatabaseError     string    `json:"database_error,omitempty"`
	WebhookConfigured bool      `json:"webhook_configured"`
	WebhookError      string    `json:"webhook_error,omitempty"`
}
