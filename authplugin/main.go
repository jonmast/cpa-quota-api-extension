package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
)

const (
	pluginID      = "cpa-opencode-go-auth"
	pluginVersion = "0.1.0"

	// authType is the value of the "type" field in auths/opencode-go.json.
	// The host derives AuthParseRequest.Provider from it and routes the parse
	// call to the auth provider whose identifier matches.
	authType = "opencode-go"

	// providerKey is the key under which models are registered and the value of
	// the provider_key attribute. It must match the model registration provider,
	// otherwise the compat auth is silently UnregisterClient'd.
	providerKey = "opencode-go"

	// compatName marks the emitted auth as an OpenAI-compatibility auth.
	compatName = "opencode-go"

	// authProvider is the CPA provider that owns the built-in compatibility executor.
	authProvider = "openai-compatibility"

	defaultBaseURL = "https://opencode.ai/zen/v1"
)

// defaultModels is OpenCode Go's model list. It lives here rather than in
// config.yaml because a compat auth with no registered models is unregistered
// by the host without an error (see docs/adr/0001).
func defaultModels() []string {
	return []string{
		"grok-code",
		"code-supernova",
		"qwen3-coder",
		"kimi-k2",
		"glm-4.6",
		"minimax-m2",
	}
}

type authPluginConfig struct {
	BaseURL string
	Models  []string
}

func defaultAuthPluginConfig() authPluginConfig {
	return authPluginConfig{BaseURL: defaultBaseURL, Models: defaultModels()}
}

var (
	configMu     sync.RWMutex
	activeConfig = defaultAuthPluginConfig()
)

func currentConfig() authPluginConfig {
	configMu.RLock()
	defer configMu.RUnlock()
	return activeConfig
}

func applyConfig(cfg authPluginConfig) {
	configMu.Lock()
	activeConfig = cfg
	configMu.Unlock()
}

func main() {}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: schemaVersion,
		Metadata: metadata{
			Name:             "CPA OpenCode Go Auth",
			Version:          pluginVersion,
			Author:           "dinhkarate",
			GitHubRepository: "https://github.com/dinhkarate/cpa-quota-api-extension",
			ConfigFields: []configField{
				{Name: "base-url", Type: "string", Description: "OpenAI-compatible base URL for OpenCode Go. Default: " + defaultBaseURL + "."},
				{Name: "models", Type: "string", Description: "Comma-separated model IDs registered for the opencode-go provider key. Defaults to the built-in OpenCode Go list."},
			},
		},
		Capabilities: registrationCapabilities{
			ModelRegistrar: true,
			ModelProvider:  true,
			AuthProvider:   true,
		},
	}
}

func modelRegistration() modelRegistrationResponse {
	cfg := currentConfig()
	models := make([]modelInfo, 0, len(cfg.Models))
	for _, id := range cfg.Models {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		models = append(models, modelInfo{
			ID:                         id,
			Object:                     "model",
			OwnedBy:                    providerKey,
			Type:                       "model",
			DisplayName:                id,
			Name:                       id,
			SupportedGenerationMethods: []string{"chat"},
			UserDefined:                true,
		})
	}
	return modelRegistrationResponse{Provider: providerKey, Models: models}
}

// opencodeGoCredential is the on-disk shape of auths/opencode-go.json.
type opencodeGoCredential struct {
	Type       string `json:"type"`
	APIKey     string `json:"api_key"`
	APIKeyAlt  string `json:"apiKey"`
	Key        string `json:"key"`
	BaseURL    string `json:"base_url"`
	BaseURLAlt string `json:"baseURL"`
	Label      string `json:"label"`
	Email      string `json:"email"`
	Disabled   bool   `json:"disabled"`
}

func (c opencodeGoCredential) apiKey() string {
	for _, candidate := range []string{c.APIKey, c.APIKeyAlt, c.Key} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func (c opencodeGoCredential) baseURL(fallback string) string {
	for _, candidate := range []string{c.BaseURL, c.BaseURLAlt} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return fallback
}

// parseAuth converts an OpenCode Go credential file into a compatibility auth.
// The host stamps the path attribute afterwards, which is what makes the
// credential readable through host.auth.get.
func parseAuth(req authParseRequest) (authParseResponse, error) {
	if !strings.EqualFold(strings.TrimSpace(req.Provider), authType) {
		return authParseResponse{Handled: false}, nil
	}
	var cred opencodeGoCredential
	if err := json.Unmarshal(req.RawJSON, &cred); err != nil {
		return authParseResponse{}, fmt.Errorf("decode opencode-go credential: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(cred.Type), authType) {
		return authParseResponse{Handled: false}, nil
	}
	apiKey := cred.apiKey()
	if apiKey == "" {
		return authParseResponse{}, fmt.Errorf("opencode-go credential is missing api_key")
	}
	cfg := currentConfig()
	label := strings.TrimSpace(cred.Label)
	if label == "" {
		label = strings.TrimSpace(cred.Email)
	}
	if label == "" {
		label = "OpenCode Go"
	}
	fileName := strings.TrimSpace(req.FileName)
	if fileName == "" {
		fileName = authType + ".json"
	}
	metadataMap := map[string]any{"type": authType}
	if email := strings.TrimSpace(cred.Email); email != "" {
		metadataMap["email"] = email
	}
	return authParseResponse{
		Handled: true,
		Auth: authData{
			Provider:    authProvider,
			ID:          authType,
			FileName:    fileName,
			Label:       label,
			Disabled:    cred.Disabled,
			StorageJSON: append([]byte(nil), req.RawJSON...),
			Metadata:    metadataMap,
			Attributes: map[string]string{
				"base_url":     cred.baseURL(cfg.BaseURL),
				"api_key":      apiKey,
				"compat_name":  compatName,
				"provider_key": providerKey,
				"auth_kind":    "apikey",
			},
		},
	}, nil
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case methodPluginRegister, methodPluginReconfigure:
		cfg, err := decodeLifecycleConfig(request)
		if err != nil {
			return nil, err
		}
		applyConfig(cfg)
		return okEnvelope(pluginRegistration())
	case methodModelRegister:
		return okEnvelope(modelRegistration())
	case methodModelStatic, methodModelForAuth:
		registered := modelRegistration()
		return okEnvelope(modelResponse{Provider: registered.Provider, Models: registered.Models})
	case methodAuthIdentifier:
		return okEnvelope(identifierResponse{Identifier: authType})
	case methodAuthParse:
		var req authParseRequest
		if err := json.Unmarshal(request, &req); err != nil {
			return nil, fmt.Errorf("decode auth parse request: %w", err)
		}
		resp, err := parseAuth(req)
		if err != nil {
			return errorEnvelope("auth_parse_failed", err.Error(), false, 0), nil
		}
		return okEnvelope(resp)
	case methodPluginShutdown:
		return okEnvelope(struct{}{})
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method, false, 0), nil
	}
}

func decodeLifecycleConfig(raw []byte) (authPluginConfig, error) {
	cfg := defaultAuthPluginConfig()
	if len(raw) == 0 {
		return cfg, nil
	}
	var req lifecycleRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return cfg, fmt.Errorf("decode lifecycle request: %w", err)
	}
	text, err := lifecycleConfigText(req.ConfigYAML)
	if err != nil {
		return cfg, err
	}
	values := yamlScalars(text)
	if value := values["base-url"]; value != "" {
		parsed, parseErr := url.Parse(value)
		if parseErr != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return cfg, fmt.Errorf("base-url must be an http or https URL")
		}
		cfg.BaseURL = value
	}
	if value := values["models"]; value != "" {
		models := make([]string, 0, 4)
		for _, part := range strings.Split(value, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				models = append(models, trimmed)
			}
		}
		if len(models) == 0 {
			return cfg, fmt.Errorf("models must list at least one model ID")
		}
		cfg.Models = models
	}
	return cfg, nil
}

func lifecycleConfigText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if decoded, decodeErr := base64.StdEncoding.DecodeString(text); decodeErr == nil && strings.Contains(string(decoded), ":") {
			return string(decoded), nil
		}
		return text, nil
	}
	var bytes []byte
	if err := json.Unmarshal(raw, &bytes); err == nil {
		return string(bytes), nil
	}
	return "", fmt.Errorf("config_yaml must be a string or byte array")
}

func yamlScalars(raw string) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || !strings.Contains(trimmed, ":") {
			continue
		}
		parts := strings.SplitN(trimmed, ":", 2)
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(strings.SplitN(parts[1], "#", 2)[0])
		value = strings.Trim(value, `"'`)
		if key != "" {
			values[key] = value
		}
	}
	return values
}
