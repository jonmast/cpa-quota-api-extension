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

	methodExecutorIdentifier    = "executor.identifier"
	methodExecutorExecute       = "executor.execute"
	methodExecutorExecuteStream = "executor.execute_stream"
	methodExecutorCountTokens   = "executor.count_tokens"

	// Host callbacks. These are dispatched by Host.callFromPlugin with no
	// capability gate, so any loaded plugin may call them.
	methodHostHTTPDo          = "host.http.do"
	methodHostHTTPDoStream    = "host.http.do_stream"
	methodHostHTTPStreamRead  = "host.http.stream_read"
	methodHostHTTPStreamClose = "host.http.stream_close"
	methodHostStreamEmit      = "host.stream.emit"
	methodHostStreamClose     = "host.stream.close"
	methodHostLog             = "host.log"
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

// registrationCapabilities mirrors internal/pluginhost.rpcCapabilities. Unlike
// the pluginapi types, that struct carries json tags, so these keys are
// snake_case.
type registrationCapabilities struct {
	ModelRegistrar        bool     `json:"model_registrar"`
	ModelProvider         bool     `json:"model_provider"`
	AuthProvider          bool     `json:"auth_provider"`
	Executor              bool     `json:"executor"`
	ExecutorModelScope    string   `json:"executor_model_scope,omitempty"`
	ExecutorInputFormats  []string `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats []string `json:"executor_output_formats,omitempty"`
}

type identifierResponse struct {
	Identifier string `json:"identifier"`
}

type modelInfo struct {
	ID                         string           `json:"ID"`
	Object                     string           `json:"Object"`
	OwnedBy                    string           `json:"OwnedBy"`
	Type                       string           `json:"Type,omitempty"`
	DisplayName                string           `json:"DisplayName,omitempty"`
	Name                       string           `json:"Name,omitempty"`
	SupportedGenerationMethods []string         `json:"SupportedGenerationMethods,omitempty"`
	ContextLength              int64            `json:"ContextLength,omitempty"`
	MaxCompletionTokens        int64            `json:"MaxCompletionTokens,omitempty"`
	Thinking                   *thinkingSupport `json:"Thinking,omitempty"`
	UserDefined                bool             `json:"UserDefined,omitempty"`
}

// thinkingSupport mirrors pluginapi.ThinkingSupport. Leaving it nil is not
// neutral: the host copies it straight onto registry.ModelInfo.Thinking
// (internal/pluginhost/adapters.go:139), and every consumer downstream treats
// nil as "this model has no reasoning controls" and returns early -- see
// applyCodexClientThinkingMetadata (internal/client/codex/models/models.go:376).
//
// The host does *not* run modelconfig.NormalizeThinkingSupport over what a
// plugin sends, unlike the config path (sdk/cliproxy/service_models.go:734), so
// the levels here must already be lowercased and de-duplicated and must already
// have had "none"/"auto" reflected into ZeroAllowed/DynamicAllowed.
type thinkingSupport struct {
	Min            int      `json:"Min,omitempty"`
	Max            int      `json:"Max,omitempty"`
	ZeroAllowed    bool     `json:"ZeroAllowed,omitempty"`
	DynamicAllowed bool     `json:"DynamicAllowed,omitempty"`
	Levels         []string `json:"Levels,omitempty"`
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

// executorRequest mirrors pluginapi.ExecutorRequest (PascalCase, untagged
// upstream) wrapped by internal/pluginhost.rpcExecutorRequest, which adds the
// two tagged snake_case fields below.
//
// Headers carries the *client's* request headers. It is the only place the
// inbound x-opencode-session header is visible to us, and forwarding it to
// oc-go is the reason this plugin owns an executor at all instead of relying on
// the built-in openai-compatibility executor, which drops these headers.
type executorRequest struct {
	AuthID          string              `json:"AuthID"`
	AuthProvider    string              `json:"AuthProvider"`
	Model           string              `json:"Model"`
	Format          string              `json:"Format"`
	Stream          bool                `json:"Stream"`
	Alt             string              `json:"Alt"`
	Headers         map[string][]string `json:"Headers,omitempty"`
	Query           map[string][]string `json:"Query,omitempty"`
	OriginalRequest []byte              `json:"OriginalRequest,omitempty"`
	SourceFormat    string              `json:"SourceFormat"`
	Payload         []byte              `json:"Payload,omitempty"`
	Metadata        map[string]any      `json:"Metadata,omitempty"`
	StorageJSON     []byte              `json:"StorageJSON,omitempty"`
	AuthMetadata    map[string]any      `json:"AuthMetadata,omitempty"`
	AuthAttributes  map[string]string   `json:"AuthAttributes,omitempty"`

	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// executorResponse mirrors pluginapi.ExecutorResponse (untagged upstream).
type executorResponse struct {
	Payload  []byte              `json:"Payload,omitempty"`
	Headers  map[string][]string `json:"Headers,omitempty"`
	Metadata map[string]any      `json:"Metadata,omitempty"`
}

// executorStreamResponse mirrors internal/pluginhost.rpcExecutorStreamResponse,
// which is tagged, hence lowercase keys. Chunks is left empty: this plugin
// streams asynchronously through host.stream.emit.
type executorStreamResponse struct {
	Headers map[string][]string `json:"headers,omitempty"`
}

// hostHTTPStreamResponse mirrors internal/pluginhost.rpcHostHTTPStreamResponse.
type hostHTTPStreamResponse struct {
	StatusCode int                 `json:"status_code"`
	Headers    map[string][]string `json:"headers,omitempty"`
	StreamID   string              `json:"stream_id,omitempty"`
}

type hostHTTPStreamReadRequest struct {
	StreamID string `json:"stream_id"`
}

type hostHTTPStreamReadResponse struct {
	Payload []byte `json:"payload,omitempty"`
	Error   string `json:"error,omitempty"`
	Done    bool   `json:"done,omitempty"`
}

type hostHTTPStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
}

type hostStreamEmitRequest struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
	Error    string `json:"error,omitempty"`
}

type hostStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
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
