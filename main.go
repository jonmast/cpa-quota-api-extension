package main

import "net/http"

func main() {}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: schemaVersion,
		Metadata: metadata{
			Name:             "CPA Quota API Extension",
			Version:          pluginVersion,
			Author:           "dinhkarate",
			GitHubRepository: "https://github.com/dinhkarate/cpa-quota-api-extension",
			ConfigFields: []configField{
				{Name: "cache-ttl", Type: "string", Description: "Request-triggered quota cache duration. Default: 30m."},
				{Name: "request-timeout", Type: "string", Description: "Per-account upstream request timeout. Default: 30s."},
				{Name: "max-concurrency", Type: "integer", Description: "Maximum concurrent account quota queries. Default: 8."},
				{Name: "include-disabled", Type: "boolean", Description: "Include disabled credentials in quota scans. Default: false."},
				{Name: "database-path", Type: "string", Description: "SQLite health database path. Default: ./data/cpa-quota-api-extension.db."},
				{Name: "health-refresh-interval", Type: "string", Description: "Health refresh interval (minimum 10s)."},
				{Name: "health-history-interval", Type: "string", Description: "Health history interval (minimum 10s)."},
				{Name: "failure-window", Type: "string", Description: "Recent failure counting window."},
				{Name: "degraded-failure-threshold", Type: "integer", Description: "Failures in window before degraded; zero disables."},
				{Name: "incident-retention", Type: "string", Description: "Incident retention (minimum 1h)."},
				{Name: "incident-max-rows", Type: "integer", Description: "Maximum incident rows (100 through 1000000)."},
				{Name: "history-retention", Type: "string", Description: "History retention (minimum 1h)."},
				{Name: "history-max-rows", Type: "integer", Description: "Maximum history rows (100 through 1000000)."},
				{Name: "usage-queue-size", Type: "integer", Description: "Usage event queue size (64 through 65536)."},
				{Name: "profile-timezone", Type: "string", Description: "IANA timezone for usage-profile buckets. Default: server local."},
				{Name: "webhook-url", Type: "string", Description: "Optional HTTP(S) pool-health alert webhook URL."},
				{Name: "webhook-timeout", Type: "string", Description: "Webhook delivery timeout."},
				{Name: "alert-lost-threshold", Type: "integer", Description: "Lost accounts before alert; zero disables."},
				{Name: "alert-degraded-threshold", Type: "integer", Description: "Degraded accounts before alert; zero disables."},
				{Name: "alert-cooldown", Type: "string", Description: "Breach delivery cooldown."},
			},
		},
		Capabilities: registrationCapabilities{ManagementAPI: true, UsagePlugin: true},
	}
}

func managementRegistration() managementRegistrationResponse {
	return managementRegistrationResponse{
		Routes: []managementRoute{
			{Method: http.MethodGet, Path: quotaRoute, Description: "Returns a cached, request-triggered quota snapshot for the credential pool."},
			{Method: http.MethodGet, Path: accountRoute, Description: "Returns redacted runtime credential inventory for quota coverage diagnostics."},
			{Method: http.MethodGet, Path: statusRoute, Description: "Returns extension configuration and cache state."},
			{Method: http.MethodGet, Path: healthRoute, Description: "Returns account-level pool health and capacity."},
			{Method: http.MethodGet, Path: incidentsRoute, Description: "Returns sanitized health incidents."},
			{Method: http.MethodGet, Path: historyRoute, Description: "Returns pool health capacity history."},
			{Method: http.MethodGet, Path: profileRoute, Description: "Returns per-provider usage profiles rolled up across auth indexes."},
		},
		Resources: []resourceRoute{{
			Path:        panelResourcePath,
			Menu:        "CPA Quota",
			Description: "Configure and inspect CPA Quota API Extension.",
		}},
	}
}
