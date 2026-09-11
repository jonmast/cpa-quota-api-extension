package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recordedMetadataPayload is a real models.dev response, narrowed to the
// opencode-go provider entry (verbatim) plus a neighbouring provider, so the
// "decode only our provider" path is exercised by recorded data rather than by
// a fixture written to match the parser.
func recordedMetadataPayload(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "models_dev.json"))
	if err != nil {
		t.Fatalf("read recorded metadata payload: %v", err)
	}
	return raw
}

func resetMetadata(t *testing.T) {
	t.Helper()
	metadataStore.reset()
	t.Cleanup(metadataStore.reset)
}

// metadataHost answers per-URL, because model.for_auth now makes two different
// calls: the provider's /models list and the metadata registry.
type metadataHost struct {
	fakeHost
	responses map[string]hostHTTPResponse
}

func (m *metadataHost) doHTTP(request hostHTTPRequest) (hostHTTPResponse, error) {
	m.mu.Lock()
	m.requests = append(m.requests, request)
	m.mu.Unlock()
	if response, ok := m.responses[request.URL]; ok {
		return response, nil
	}
	return hostHTTPResponse{}, fmt.Errorf("no stubbed response for %s", request.URL)
}

func metadataOKHost(t *testing.T) *metadataHost {
	t.Helper()
	return &metadataHost{
		responses: map[string]hostHTTPResponse{
			modelsEndpoint(defaultBaseURL): {StatusCode: 200, Body: recordedModelsPayload(t)},
			modelsDevURL: {
				StatusCode: 200,
				Body:       recordedMetadataPayload(t),
				Headers:    map[string][]string{"Etag": {`"recorded-etag"`}},
			},
		},
	}
}

// The whole point of the overlay: reasoning levels must come from the registry,
// not from a default this plugin makes up. gpt-5.6-luna is the model the old
// openai-compatibility config entry gave "max" to.
func TestMetadataSuppliesReasoningLevelsFromTheRecordedRegistry(t *testing.T) {
	resetMetadata(t)

	thinking, err := parseModelsDevThinking(recordedMetadataPayload(t))
	if err != nil {
		t.Fatalf("parse metadata: %v", err)
	}

	luna := thinking["gpt-5.6-luna"]
	if luna == nil {
		t.Fatal("gpt-5.6-luna has no thinking support")
	}
	want := []string{"none", "low", "medium", "high", "xhigh", "max"}
	if strings.Join(luna.Levels, ",") != strings.Join(want, ",") {
		t.Fatalf("levels=%v want %v", luna.Levels, want)
	}
	// "none" among the levels must also raise ZeroAllowed: the host does not run
	// NormalizeThinkingSupport over plugin-supplied thinking.
	if !luna.ZeroAllowed {
		t.Fatalf("listing \"none\" must set ZeroAllowed: %#v", luna)
	}
}

// Models the registry describes as reasoning-capable but not steerable, and
// models it does not describe at all, must report no thinking support rather
// than a fabricated low/medium/high.
func TestMetadataInventsNoLevels(t *testing.T) {
	thinking, err := parseModelsDevThinking(recordedMetadataPayload(t))
	if err != nil {
		t.Fatalf("parse metadata: %v", err)
	}

	// glm-5 carries `reasoning: true` with an empty reasoning_options.
	if got, ok := thinking["glm-5"]; ok {
		t.Fatalf("glm-5 has no reasoning options; got %#v", got)
	}
	// hy3-preview is served by oc-go but absent from the registry.
	if got, ok := thinking["hy3-preview"]; ok {
		t.Fatalf("hy3-preview is not in the registry; got %#v", got)
	}
}

// A toggle-only model can have reasoning switched off but not steered.
func TestToggleOnlyModelIsZeroAllowedWithNoLevels(t *testing.T) {
	thinking, err := parseModelsDevThinking(recordedMetadataPayload(t))
	if err != nil {
		t.Fatalf("parse metadata: %v", err)
	}
	longcat := thinking["longcat-2.0"]
	if longcat == nil {
		t.Fatal("longcat-2.0 has a toggle option and must report thinking support")
	}
	if len(longcat.Levels) != 0 || !longcat.ZeroAllowed {
		t.Fatalf("longcat-2.0=%#v want ZeroAllowed with no levels", longcat)
	}
}

// qwen3.8-max carries toggle + effort + budget_tokens at once. The effort values
// are the levels; the toggle contributes ZeroAllowed; budget_tokens is not
// mapped onto Min/Max because oc-go exposes no way to set a budget.
func TestCombinedReasoningOptionsMergeIntoOneSupport(t *testing.T) {
	thinking, err := parseModelsDevThinking(recordedMetadataPayload(t))
	if err != nil {
		t.Fatalf("parse metadata: %v", err)
	}
	qwen := thinking["qwen3.8-max"]
	if qwen == nil {
		t.Fatal("qwen3.8-max must report thinking support")
	}
	if strings.Join(qwen.Levels, ",") != "low,medium,xhigh" {
		t.Fatalf("levels=%v", qwen.Levels)
	}
	if !qwen.ZeroAllowed {
		t.Fatalf("a toggle option must set ZeroAllowed: %#v", qwen)
	}
	if qwen.Min != 0 || qwen.Max != 0 {
		t.Fatalf("budget_tokens must not be mapped onto Min/Max: %#v", qwen)
	}
}

func TestReasoningLevelsAreNormalisedLikeTheConfigPath(t *testing.T) {
	support := thinkingFromReasoningOptions(modelsDevModel{
		Reasoning: true,
		ReasoningOptions: []modelsDevReasoningOption{
			{Type: "effort", Values: []string{" HIGH ", "auto", "high", "", "low"}},
		},
	})
	if support == nil {
		t.Fatal("support must not be nil")
	}
	if strings.Join(support.Levels, ",") != "high,auto,low" {
		t.Fatalf("levels=%v want lowercased, de-duplicated, order preserved", support.Levels)
	}
	if !support.DynamicAllowed {
		t.Fatalf("listing \"auto\" must set DynamicAllowed: %#v", support)
	}
}

func TestModelsWithoutReasoningReportNoSupport(t *testing.T) {
	if got := thinkingFromReasoningOptions(modelsDevModel{Reasoning: false}); got != nil {
		t.Fatalf("got %#v want nil", got)
	}
	if got := thinkingFromReasoningOptions(modelsDevModel{
		Reasoning:        false,
		ReasoningOptions: []modelsDevReasoningOption{{Type: "effort", Values: []string{"high"}}},
	}); got != nil {
		t.Fatalf("got %#v want nil", got)
	}
}

// The registered models are what the host turns into registry.ModelInfo, so the
// thinking support has to survive onto the model.for_auth response itself.
func TestRegisteredModelsCarryThinkingSupport(t *testing.T) {
	resetConfig(t)
	resetDiscovery(t)
	resetMetadata(t)
	withHost(t, metadataOKHost(t))

	var models modelResponse
	callMethod(t, methodModelForAuth, authModelRequest{
		AuthID:     "opencode-go",
		Attributes: map[string]string{"api_key": "sk-test", baseURLAttribute: defaultBaseURL},
	}, &models)

	byID := make(map[string]modelInfo, len(models.Models))
	for _, model := range models.Models {
		byID[model.ID] = model
	}

	luna, ok := byID["gpt-5.6-luna"]
	if !ok {
		t.Fatal("gpt-5.6-luna was not registered")
	}
	if luna.Thinking == nil {
		t.Fatal("gpt-5.6-luna registered with no thinking support")
	}
	if strings.Join(luna.Thinking.Levels, ",") != "none,low,medium,high,xhigh,max" {
		t.Fatalf("levels=%v", luna.Thinking.Levels)
	}
}

// The wire keys have to be the PascalCase Go field names: pluginapi carries no
// struct tags, so a lowercase key would decode into a zero-valued
// ThinkingSupport and silently drop every level.
func TestThinkingSupportUsesPascalCaseWireKeys(t *testing.T) {
	raw, err := json.Marshal(modelInfo{
		ID:       "example",
		Thinking: &thinkingSupport{Levels: []string{"high"}, ZeroAllowed: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"Thinking"`, `"Levels"`, `"ZeroAllowed"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("missing wire key %s in %s", key, raw)
		}
	}
}

// Metadata is enrichment. Losing it must cost the thinking levels and nothing
// else -- never the model list, which would unregister the auth.
func TestMetadataFailureDoesNotCostTheModelList(t *testing.T) {
	resetConfig(t)
	resetDiscovery(t)
	resetMetadata(t)

	host := &metadataHost{
		responses: map[string]hostHTTPResponse{
			modelsEndpoint(defaultBaseURL): {StatusCode: 200, Body: recordedModelsPayload(t)},
			modelsDevURL:                   {StatusCode: 503},
		},
	}
	withHost(t, host)

	var models modelResponse
	callMethod(t, methodModelForAuth, authModelRequest{
		AuthID:     "opencode-go",
		Attributes: map[string]string{"api_key": "sk-test", baseURLAttribute: defaultBaseURL},
	}, &models)

	if len(models.Models) != len(recordedModelIDs(t)) {
		t.Fatalf("registered %d models, provider serves %d", len(models.Models), len(recordedModelIDs(t)))
	}
	for _, model := range models.Models {
		if model.Thinking != nil {
			t.Fatalf("no metadata was available, so %q must carry none: %#v", model.ID, model.Thinking)
		}
	}
	if len(host.logs) == 0 {
		t.Fatal("a degraded metadata fetch must be logged")
	}
}

// The payload is several megabytes and model.for_auth runs on every refresh, so
// a repeat fetch must revalidate with the recorded ETag rather than re-download.
func TestMetadataRefreshRevalidatesWithETag(t *testing.T) {
	resetMetadata(t)
	host := metadataOKHost(t)

	refreshModelMetadata(host, modelsDevURL, "cb-1")
	if metadataStore.thinkingFor("gpt-5.6-luna") == nil {
		t.Fatal("first fetch did not populate the overlay")
	}

	host.responses[modelsDevURL] = hostHTTPResponse{StatusCode: 304}
	refreshModelMetadata(host, modelsDevURL, "cb-2")

	calls := host.calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if got := calls[1].Headers["If-None-Match"]; len(got) != 1 || got[0] != `"recorded-etag"` {
		t.Fatalf("if-none-match=%v", got)
	}
	// A 304 carries no body; the previously parsed overlay must survive it.
	if metadataStore.thinkingFor("gpt-5.6-luna") == nil {
		t.Fatal("a 304 must not discard the cached overlay")
	}
}

func TestMetadataOverlayCanBeDisabled(t *testing.T) {
	resetConfig(t)
	resetMetadata(t)

	callMethod(t, methodPluginReconfigure, lifecycleRequest{
		ConfigYAML: json.RawMessage(`"model-metadata-url: off\n"`),
	}, nil)
	if got := currentConfig().MetadataURL; got != "" {
		t.Fatalf("metadata url=%q want disabled", got)
	}

	host := metadataOKHost(t)
	refreshModelMetadata(host, currentConfig().MetadataURL, "")
	if len(host.calls()) != 0 {
		t.Fatalf("a disabled overlay must issue no requests: %#v", host.calls())
	}
}

func TestMetadataURLIsValidated(t *testing.T) {
	if _, err := decodeLifecycleConfig(mustJSON(t, lifecycleRequest{
		ConfigYAML: json.RawMessage(`"model-metadata-url: notaurl\n"`),
	})); err == nil {
		t.Fatal("a non-URL must be rejected rather than silently ignored")
	}
}

func TestMetadataDefaultsToTheOpenCodeRegistry(t *testing.T) {
	if got := defaultAuthPluginConfig().MetadataURL; got != modelsDevURL {
		t.Fatalf("default metadata url=%q want %q", got, modelsDevURL)
	}
	if modelsDevProviderID != providerKey {
		t.Fatalf("registry provider id %q must match the provider key %q", modelsDevProviderID, providerKey)
	}
}
