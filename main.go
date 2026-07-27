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
				{Name: "max-concurrency", Type: "number", Description: "Maximum concurrent account quota queries. Default: 8."},
				{Name: "include-disabled", Type: "boolean", Description: "Include disabled credentials in quota scans. Default: false."},
			},
		},
		Capabilities: registrationCapabilities{ManagementAPI: true},
	}
}

func managementRegistration() managementRegistrationResponse {
	return managementRegistrationResponse{Routes: []managementRoute{
		{Method: http.MethodGet, Path: quotaRoute, Description: "Returns a cached, request-triggered quota snapshot for the credential pool."},
		{Method: http.MethodGet, Path: accountRoute, Description: "Returns redacted runtime credential inventory for quota coverage diagnostics."},
		{Method: http.MethodGet, Path: statusRoute, Description: "Returns extension configuration and cache state."},
	}}
}
