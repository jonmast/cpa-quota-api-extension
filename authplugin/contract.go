package main

// Wire contract for the CLIProxyAPI plugin RPC, kept local so this build target
// does not import upstream packages. Field names and JSON keys mirror
// upstream/CLIProxyAPI/sdk/pluginabi and sdk/pluginapi at the pinned tag.
//
// Note: pluginapi types carry no struct tags upstream, so their wire keys are
// the Go field names (PascalCase). The tags below reproduce that exactly.

import (
	"encoding/json"
	"time"
)

const (
	abiVersion    uint32 = 1
	schemaVersion uint32 = 1
)

const (
	methodPluginRegister    = "plugin.register"
	methodPluginReconfigure = "plugin.reconfigure"
	methodPluginShutdown    = "plugin.shutdown"

	methodModelRegister = "model.register"
	methodModelStatic   = "model.static"
	methodModelForAuth  = "model.for_auth"

	methodAuthIdentifier = "auth.identifier"
	methodAuthParse      = "auth.parse"

	// Host callbacks. These are dispatched by Host.callFromPlugin with no
	// capability gate, so any loaded plugin may call them.
	methodHostHTTPDo = "host.http.do"
	methodHostLog    = "host.log"
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
	Logo             string        `json:"Logo,omitempty"`
	ConfigFields     []configField `json:"ConfigFields"`
}

type configField struct {
	Name        string `json:"Name"`
	Type        string `json:"Type"`
	Description string `json:"Description"`
}

type registrationCapabilities struct {
	ModelRegistrar bool `json:"model_registrar"`
	ModelProvider  bool `json:"model_provider"`
	AuthProvider   bool `json:"auth_provider"`
}

type identifierResponse struct {
	Identifier string `json:"identifier"`
}

type modelInfo struct {
	ID                         string   `json:"ID"`
	Object                     string   `json:"Object"`
	OwnedBy                    string   `json:"OwnedBy"`
	Type                       string   `json:"Type,omitempty"`
	DisplayName                string   `json:"DisplayName,omitempty"`
	Name                       string   `json:"Name,omitempty"`
	SupportedGenerationMethods []string `json:"SupportedGenerationMethods,omitempty"`
	ContextLength              int64    `json:"ContextLength,omitempty"`
	MaxCompletionTokens        int64    `json:"MaxCompletionTokens,omitempty"`
	UserDefined                bool     `json:"UserDefined,omitempty"`
}

type modelRegistrationResponse struct {
	Provider string      `json:"Provider"`
	Models   []modelInfo `json:"Models"`
}

type modelResponse struct {
	Provider string      `json:"Provider"`
	Models   []modelInfo `json:"Models"`
}

type authParseRequest struct {
	Provider string `json:"Provider"`
	Path     string `json:"Path"`
	FileName string `json:"FileName"`
	RawJSON  []byte `json:"RawJSON"`
}

type authParseResponse struct {
	Handled bool     `json:"Handled"`
	Auth    authData `json:"Auth"`
}

type authData struct {
	Provider         string            `json:"Provider"`
	ID               string            `json:"ID"`
	FileName         string            `json:"FileName"`
	Label            string            `json:"Label"`
	Prefix           string            `json:"Prefix,omitempty"`
	ProxyURL         string            `json:"ProxyURL,omitempty"`
	Disabled         bool              `json:"Disabled"`
	StorageJSON      []byte            `json:"StorageJSON,omitempty"`
	Metadata         map[string]any    `json:"Metadata,omitempty"`
	Attributes       map[string]string `json:"Attributes,omitempty"`
	NextRefreshAfter time.Time         `json:"NextRefreshAfter"`
}

// authModelRequest is the model.for_auth request. Unlike model.static it
// carries the auth's Attributes -- which is where parseAuth stamped the api_key
// and base_url -- so it is the only ABI entry point with a usable credential.
// HostCallbackID is added by the host's RPC wrapper (rpcAuthModelRequest) and
// must be echoed on host callbacks so they resolve the originating context.
type authModelRequest struct {
	AuthID         string            `json:"AuthID"`
	AuthProvider   string            `json:"AuthProvider"`
	Metadata       map[string]any    `json:"Metadata,omitempty"`
	Attributes     map[string]string `json:"Attributes,omitempty"`
	HostCallbackID string            `json:"host_callback_id,omitempty"`
}

type hostHTTPRequest struct {
	HostCallbackID string              `json:"host_callback_id,omitempty"`
	Method         string              `json:"method"`
	URL            string              `json:"url"`
	Headers        map[string][]string `json:"headers,omitempty"`
	Body           []byte              `json:"body,omitempty"`
}

// hostHTTPResponse mirrors pluginapi.HTTPResponse, which carries no struct
// tags upstream, so its wire keys are the PascalCase Go field names.
type hostHTTPResponse struct {
	StatusCode int                 `json:"StatusCode"`
	Headers    map[string][]string `json:"Headers,omitempty"`
	Body       []byte              `json:"Body,omitempty"`
}

type hostLogRequest struct {
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Fields  map[string]any `json:"fields,omitempty"`
}

// modelListResponse is the provider's OpenAI-shaped GET /models payload.
type modelListResponse struct {
	Object string `json:"object"`
	Data   []struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	} `json:"data"`
}

type lifecycleRequest struct {
	ConfigYAML json.RawMessage `json:"config_yaml"`
}
