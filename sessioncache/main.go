// Package main is the CPA Session Cache plugin: a self-contained CLIProxyAPI
// sibling plugin that passively observes Anthropic-format response traffic,
// records one SQLite row per model request (session ID plus the four token
// counts), and serves session data over the host's management API. It is
// strictly pass-through: every interceptor response is empty, so the host keeps
// the live body byte-identical.
package main

import "net/http"

func main() {}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: schemaVersion,
		Metadata: metadata{
			Name:             "CPA Session Cache",
			Version:          pluginVersion,
			Author:           "jonmast",
			GitHubRepository: "https://github.com/jonmast/cpa-quota-api-extension",
			ConfigFields: []configField{
				{Name: "database-path", Type: "string", Description: "SQLite session capture database path. Default: " + defaultDatabasePath + "."},
				{Name: "stream-state-ttl", Type: "string", Description: "Eviction TTL for in-memory state of abandoned streams, measured since the last chunk. Default: 10m."},
			},
		},
		Capabilities: registrationCapabilities{
			ResponseInterceptor:       true,
			ResponseStreamInterceptor: true,
			ManagementAPI:             true,
		},
	}
}

func managementRegistration() managementRegistrationResponse {
	return managementRegistrationResponse{
		Routes: []managementRoute{
			{Method: http.MethodGet, Path: sessionsRoute, Description: "Returns recent sessions ordered by last activity with request count, last model, and last-seen time."},
		},
	}
}
