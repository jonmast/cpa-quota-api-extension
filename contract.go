package main

import "encoding/json"

const (
	abiVersion    uint32 = 1
	schemaVersion uint32 = 1
)

const (
	methodPluginRegister     = "plugin.register"
	methodPluginReconfigure  = "plugin.reconfigure"
	methodPluginShutdown     = "plugin.shutdown"
	methodManagementRegister = "management.register"
	methodManagementHandle   = "management.handle"

	methodHostHTTPDo   = "host.http.do"
	methodHostLog      = "host.log"
	methodHostAuthList = "host.auth.list"
	methodHostAuthGet  = "host.auth.get"
)

type hostAuthFileEntry struct {
	ID            string `json:"id,omitempty"`
	AuthIndex     string `json:"auth_index,omitempty"`
	Name          string `json:"name"`
	Type          string `json:"type,omitempty"`
	Provider      string `json:"provider,omitempty"`
	Label         string `json:"label,omitempty"`
	Status        string `json:"status,omitempty"`
	StatusMessage string `json:"status_message,omitempty"`
	Disabled      bool   `json:"disabled,omitempty"`
	Unavailable   bool   `json:"unavailable,omitempty"`
	RuntimeOnly   bool   `json:"runtime_only,omitempty"`
	Email         string `json:"email,omitempty"`
	ProjectID     string `json:"project_id,omitempty"`
	AccountType   string `json:"account_type,omitempty"`
	Account       string `json:"account,omitempty"`
	Priority      int    `json:"priority,omitempty"`
}

type hostAuthListResponse struct {
	Files []hostAuthFileEntry `json:"files"`
}

type hostAuthGetRequest struct {
	AuthIndex string `json:"auth_index"`
}

type hostAuthGetResponse struct {
	AuthIndex string          `json:"auth_index"`
	Name      string          `json:"name,omitempty"`
	Path      string          `json:"path,omitempty"`
	JSON      json.RawMessage `json:"json"`
}

type hostHTTPRequest struct {
	Method  string              `json:"method"`
	URL     string              `json:"url"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    []byte              `json:"body,omitempty"`
}

type hostHTTPResponse struct {
	StatusCode int                 `json:"status_code"`
	Headers    map[string][]string `json:"headers,omitempty"`
	Body       []byte              `json:"body,omitempty"`
}

type hostLogRequest struct {
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Fields  map[string]any `json:"fields,omitempty"`
}
