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
	profileRoute      = "/plugins/cpa-quota-api-extension/v1/profile"
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
	// ProfileTimezone is the configured IANA zone for usage-profile bucket
	// assignment; empty means server local (CONTEXT.md: profile timezone).
	// profileLoc is the resolved location, populated at config decode time.
	ProfileTimezone string
	profileLoc      *time.Location
}

// profileLocation returns the timezone in which profile buckets and day types
// are assigned: the configured IANA zone, defaulting to server local.
func (c pluginConfig) profileLocation() *time.Location {
	if c.profileLoc != nil {
		return c.profileLoc
	}
	return time.Local
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
	// Projection is the end-of-cycle forecast for fixed-cycle windows with a
	// derivable start (ADR 0004). Sliding windows and credit pools omit it.
	Projection *windowProjection `json:"projection,omitempty"`
}

// windowProjection forecasts a fixed-cycle window's end-of-cycle usage from
// the provider-reported level and the usage profile's expected pace
// (CONTEXT.md: projection). NaiveProjectedPercent is kept deliberately:
// profile-vs-clock divergence is how the profile's work stays visible.
type windowProjection struct {
	ElapsedFraction       float64 `json:"elapsed_fraction"`
	ExpectedFraction      float64 `json:"expected_fraction"`
	ProjectedUsedPercent  float64 `json:"projected_used_percent"`
	NaiveProjectedPercent float64 `json:"naive_projected_percent"`
	// ProjectedExhaustionAt is null unless projected usage crosses 100 before
	// the reset instant.
	ProjectedExhaustionAt *time.Time `json:"projected_exhaustion_at"`
	Verdict               string     `json:"verdict"`
	Confidence            string     `json:"confidence"`
	Basis                 string     `json:"basis"`
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

// providerQuota is the per-provider entry in the provider-nested response shape.
// It groups one or more accounts sharing the same provider key and exposes the
// fields a thin client needs to render a tightest-constraint pill and a
// per-provider tooltip without needing to know any provider-specific payload
// format.
//
// Top-level fields (status, supported, credential_state, error, windows, …) come
// from the first account in the sorted account list. A single account per
// provider is assumed for display; the full account list is retained under
// Accounts so that assumption stays a display convenience, not a structural one.
type providerQuota struct {
	// Status, Supported, CredentialState, and Error mirror the corresponding
	// accountQuota fields so a client can detect a partial failure at the
	// provider level without inspecting Accounts.
	Status          string      `json:"status"`
	Supported       bool        `json:"supported"`
	CredentialState string      `json:"credential_state"`
	Error           *quotaError `json:"error,omitempty"`
	// Windows, Models, BindingWindow, ExtraUsedCredits, and ExtraMonthlyLimit
	// expose the quota data a client needs to compute a tightest-across-all-providers
	// value. Percent semantics are identical across all providers.
	Windows           []quotaWindow  `json:"windows,omitempty"`
	Models            []modelQuota   `json:"models,omitempty"`
	BindingWindow     *bindingWindow `json:"binding_window,omitempty"`
	ExtraUsedCredits  *int64         `json:"extra_used_credits,omitempty"`
	ExtraMonthlyLimit *int64         `json:"extra_monthly_limit,omitempty"`
	// Accounts is the full per-account list. The underlying account list is not
	// collapsed so multiple accounts per provider remain accessible.
	Accounts []accountQuota `json:"accounts"`
}

type quotaResponse struct {
	GeneratedAt time.Time   `json:"generated_at"`
	CacheTTL    string      `json:"cache_ttl"`
	Cached      bool        `json:"cached"`
	RefreshMode string      `json:"refresh_mode"`
	Summary     poolSummary `json:"summary"`
	// Providers is the provider-nested view of the snapshot. Each key is a
	// normalized provider name; the value carries the windows a thin client needs
	// to compute a tightest-across-all-providers pill and the per-account list for
	// failure visibility. This map is always complete; it is not affected by the
	// ?provider or ?status query filters that apply to the Accounts list.
	Providers map[string]providerQuota `json:"providers,omitempty"`
	Accounts  []accountQuota           `json:"accounts"`
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
