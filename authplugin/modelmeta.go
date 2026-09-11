package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// Reasoning capability metadata for the models oc-go serves.
//
// GET {base_url}/models answers with id/object/created/owned_by and nothing
// else -- no reasoning metadata of any kind. OpenCode publishes that separately,
// in models.dev, its own model registry, under an "opencode-go" provider entry
// whose `api` field is exactly this plugin's base URL. models.dev is therefore
// the upstream source of truth for reasoning levels, and this plugin invents
// none: a model the registry does not describe is registered with no thinking
// support rather than with a guess.
//
// This is an enrichment overlay, not a second model list. Which models exist is
// still decided solely by the live /models call (ADR-0002). The two lists
// already disagree slightly in both directions -- oc-go serves hy3-preview,
// which models.dev omits; models.dev carries ox-alpha-free, which oc-go does not
// serve -- so the overlay only ever annotates IDs discovery already returned.
const (
	// modelsDevURL is the only published endpoint. There is no per-provider
	// route: /api/opencode-go.json and friends all 302 away.
	modelsDevURL = "https://models.dev/api.json"

	// modelsDevProviderID is the provider key to read out of the payload. The
	// response describes 200+ providers and is several megabytes, so everything
	// outside this key is discarded without being decoded.
	modelsDevProviderID = "opencode-go"

	// metadataDisabledValue turns the overlay off for installs with no route to
	// models.dev. An empty config value cannot mean this: yamlScalars skips
	// empty values, so "unset" and "set to empty" are indistinguishable.
	metadataDisabledValue = "off"
)

// modelsDevProvider is the slice of one models.dev provider entry this plugin
// reads. Everything else in the entry -- cost, modalities, npm package -- is
// deliberately not decoded.
type modelsDevProvider struct {
	Models map[string]modelsDevModel `json:"models"`
}

// modelsDevModel decodes only the reasoning fields.
//
// The same entry also carries `limit.context` and `limit.output`, which map
// directly onto modelInfo.ContextLength and modelInfo.MaxCompletionTokens --
// both of which this plugin currently reports as zero for every model. They are
// left undecoded because nothing consumes them today, not because they are
// unavailable: adding them is a struct field and one assignment in
// buildModelResponse, with no new request and no new failure mode, since this
// payload is already fetched and parsed. See docs/adr/0007.
type modelsDevModel struct {
	Reasoning        bool                       `json:"reasoning"`
	ReasoningOptions []modelsDevReasoningOption `json:"reasoning_options"`
}

// modelsDevReasoningOption is one entry of a model's reasoning_options array. A
// model may carry several: "toggle" plus "effort" plus "budget_tokens" all
// appear together on the qwen3.8 models.
type modelsDevReasoningOption struct {
	Type   string   `json:"type"`
	Values []string `json:"values"`
}

// modelMetadata caches the last successful models.dev read.
//
// The ETag is retained so a refresh revalidates rather than re-downloads: the
// endpoint serves several megabytes with `cache-control: max-age=0,
// must-revalidate`, and model.for_auth runs on every config reload, auth change
// and 15-minute refresh.
type modelMetadata struct {
	mu       sync.RWMutex
	etag     string
	thinking map[string]*thinkingSupport
}

var metadataStore = &modelMetadata{}

func (m *modelMetadata) snapshot() map[string]*thinkingSupport {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.thinking
}

func (m *modelMetadata) etagHeader() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.etag
}

func (m *modelMetadata) store(etag string, thinking map[string]*thinkingSupport) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.etag = etag
	m.thinking = thinking
}

func (m *modelMetadata) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.etag = ""
	m.thinking = nil
}

// thinkingFor reports the reasoning support recorded for a model ID, or nil if
// the registry does not describe it.
//
// The returned value is shared, never copied per call: the host clones it into
// registry.ModelInfo on receipt, and applyModelPrefixes then shallow-copies that
// ModelInfo to mint the "opencode-go/<id>" alias (sdk/cliproxy/service_models.go:611),
// so both the bare and the prefixed name resolve to the same reasoning levels.
// Under the old openai-compatibility config entry only the bare name did.
func (m *modelMetadata) thinkingFor(id string) *thinkingSupport {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.thinking[strings.TrimSpace(id)]
}

// refreshModelMetadata revalidates the overlay. It reports no error on purpose.
//
// Reasoning levels are enrichment; the model list is the critical path. A
// models.dev outage must degrade to "models registered without thinking
// metadata" -- which is exactly the state this plugin shipped in before -- and
// never to a failed model.for_auth, because that would leave the auth
// unregistered over metadata the provider itself does not serve.
func refreshModelMetadata(client hostClient, url, callbackID string) {
	url = strings.TrimSpace(url)
	if url == "" {
		return
	}
	if err := fetchModelMetadata(client, url, callbackID); err != nil && client != nil {
		client.log("warn", "opencode-go reasoning metadata refresh failed; models keep their last known thinking levels", map[string]any{
			"error": err.Error(),
			"url":   url,
		})
	}
}

func fetchModelMetadata(client hostClient, url, callbackID string) error {
	if client == nil {
		return fmt.Errorf("host callbacks unavailable")
	}
	request := hostHTTPRequest{
		HostCallbackID: callbackID,
		Method:         http.MethodGet,
		URL:            url,
		Headers: map[string][]string{
			"Accept": {"application/json"},
		},
	}
	if etag := metadataStore.etagHeader(); etag != "" {
		request.Headers["If-None-Match"] = []string{etag}
	}

	response, err := callWithTimeout(client, request)
	if err != nil {
		return err
	}
	if response.StatusCode == http.StatusNotModified {
		return nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("model metadata returned HTTP %d", response.StatusCode)
	}

	thinking, err := parseModelsDevThinking(response.Body)
	if err != nil {
		return err
	}
	metadataStore.store(firstHeaderValue(response.Headers, "Etag"), thinking)
	return nil
}

// parseModelsDevThinking decodes only the opencode-go provider out of a
// models.dev payload. The top level is decoded as raw messages so the other
// 200+ providers are skipped rather than materialised.
func parseModelsDevThinking(body []byte) (map[string]*thinkingSupport, error) {
	var providers map[string]json.RawMessage
	if err := json.Unmarshal(body, &providers); err != nil {
		return nil, fmt.Errorf("decode model metadata: %w", err)
	}
	raw, ok := providers[modelsDevProviderID]
	if !ok {
		return nil, fmt.Errorf("model metadata has no %q provider", modelsDevProviderID)
	}
	var provider modelsDevProvider
	if err := json.Unmarshal(raw, &provider); err != nil {
		return nil, fmt.Errorf("decode %q provider metadata: %w", modelsDevProviderID, err)
	}
	if len(provider.Models) == 0 {
		return nil, fmt.Errorf("model metadata lists no %q models", modelsDevProviderID)
	}

	thinking := make(map[string]*thinkingSupport, len(provider.Models))
	for id, model := range provider.Models {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if support := thinkingFromReasoningOptions(model); support != nil {
			thinking[id] = support
		}
	}
	return thinking, nil
}

// thinkingFromReasoningOptions translates one models.dev entry into CPA's
// ThinkingSupport shape.
//
// Three option types occur in the registry. "effort" carries the named levels
// and is the only one CPA's level-based consumers can use. "toggle" means
// reasoning can be switched off but is not otherwise steerable, which is
// ZeroAllowed with no levels. "budget_tokens" is a numeric budget control that
// oc-go exposes no way to set through an OpenAI-compatible request, so it is
// read but not mapped onto Min/Max.
//
// nil means "no reasoning controls". That covers both models.dev saying
// reasoning is unsupported and it saying reasoning happens but is not
// controllable (glm-5, the kimi-k2.x and mimo models all carry
// `reasoning: true` with an empty reasoning_options).
func thinkingFromReasoningOptions(model modelsDevModel) *thinkingSupport {
	if !model.Reasoning {
		return nil
	}
	support := thinkingSupport{}
	seen := make(map[string]struct{}, 8)
	for _, option := range model.ReasoningOptions {
		switch strings.ToLower(strings.TrimSpace(option.Type)) {
		case "toggle":
			support.ZeroAllowed = true
		case "effort":
			for _, value := range option.Values {
				level := strings.ToLower(strings.TrimSpace(value))
				if level == "" {
					continue
				}
				// Mirrors modelconfig.NormalizeThinkingSupport, which the host
				// runs for config-declared models but not for plugin-declared
				// ones: "none" and "auto" stay in Levels *and* set their flag.
				switch level {
				case "none":
					support.ZeroAllowed = true
				case "auto":
					support.DynamicAllowed = true
				}
				if _, exists := seen[level]; exists {
					continue
				}
				seen[level] = struct{}{}
				support.Levels = append(support.Levels, level)
			}
		}
	}
	if len(support.Levels) == 0 && !support.ZeroAllowed && !support.DynamicAllowed {
		return nil
	}
	return &support
}

func firstHeaderValue(headers map[string][]string, name string) string {
	for key, values := range headers {
		if !strings.EqualFold(key, name) || len(values) == 0 {
			continue
		}
		return strings.TrimSpace(values[0])
	}
	return ""
}
