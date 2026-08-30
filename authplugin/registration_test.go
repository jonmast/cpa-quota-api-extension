package main

import (
	"encoding/json"
	"testing"
)

func callMethod(t *testing.T, method string, request any, out any) {
	t.Helper()
	var raw []byte
	if request != nil {
		encoded, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		raw = encoded
	}
	response, err := handleMethod(method, raw)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	var env envelope
	if err := json.Unmarshal(response, &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Fatalf("%s failed: %s", method, response)
	}
	if out != nil {
		if err := json.Unmarshal(env.Result, out); err != nil {
			t.Fatalf("%s: decode result %s: %v", method, env.Result, err)
		}
	}
}

func resetConfig(t *testing.T) {
	t.Helper()
	applyConfig(defaultAuthPluginConfig())
	t.Cleanup(func() { applyConfig(defaultAuthPluginConfig()) })
}

func TestPluginRegistrationDeclaresAuthAndModelCapabilities(t *testing.T) {
	var reg registration
	callMethod(t, methodPluginRegister, lifecycleRequest{}, &reg)
	if reg.SchemaVersion != schemaVersion {
		t.Fatalf("schema_version=%d", reg.SchemaVersion)
	}
	if !reg.Capabilities.AuthProvider {
		t.Fatalf("auth_provider must be declared: %#v", reg.Capabilities)
	}
	if !reg.Capabilities.ModelRegistrar {
		t.Fatalf("model_registrar must be declared: %#v", reg.Capabilities)
	}
	if reg.Metadata.Name == "" || reg.Metadata.Version != pluginVersion {
		t.Fatalf("metadata=%#v", reg.Metadata)
	}
}

func TestAuthIdentifierMatchesCredentialType(t *testing.T) {
	var id identifierResponse
	callMethod(t, methodAuthIdentifier, nil, &id)
	if id.Identifier != authType {
		t.Fatalf("identifier=%q want %q", id.Identifier, authType)
	}
}

// A compat auth with no registered models for its provider key is silently
// UnregisterClient'd by the host: it routes but is never selected, and nothing
// errors. Assert the registration response directly.
func TestModelRegistrationIsNonEmptyForProviderKey(t *testing.T) {
	resetConfig(t)
	var reg modelRegistrationResponse
	callMethod(t, methodModelRegister, nil, &reg)
	if reg.Provider != providerKey {
		t.Fatalf("provider=%q want %q", reg.Provider, providerKey)
	}
	if len(reg.Models) == 0 {
		t.Fatal("no models registered: the compat auth would be silently unregistered")
	}
	for _, model := range reg.Models {
		if model.ID == "" {
			t.Fatalf("model with empty ID: %#v", model)
		}
		if model.Object != "model" || model.OwnedBy != providerKey {
			t.Fatalf("model=%#v", model)
		}
	}
}

// The registered provider and the emitted provider_key attribute must be the
// same string, otherwise model lookup misses and the auth is unregistered.
func TestRegisteredProviderMatchesEmittedProviderKeyAttribute(t *testing.T) {
	resetConfig(t)
	var reg modelRegistrationResponse
	callMethod(t, methodModelRegister, nil, &reg)

	var parsed authParseResponse
	callMethod(t, methodAuthParse, authParseRequest{
		Provider: authType,
		Path:     "/auths/opencode-go.json",
		FileName: "opencode-go.json",
		RawJSON:  []byte(`{"type":"opencode-go","api_key":"sk-test"}`),
	}, &parsed)

	if got := parsed.Auth.Attributes["provider_key"]; got != reg.Provider {
		t.Fatalf("provider_key=%q registered provider=%q", got, reg.Provider)
	}
	if len(reg.Models) == 0 {
		t.Fatal("compat auth emitted with no models registered for its provider key")
	}
}

func TestStaticModelsMirrorRegisteredModels(t *testing.T) {
	resetConfig(t)
	var reg modelRegistrationResponse
	callMethod(t, methodModelRegister, nil, &reg)
	var static modelResponse
	callMethod(t, methodModelStatic, nil, &static)
	if static.Provider != reg.Provider || len(static.Models) != len(reg.Models) {
		t.Fatalf("static=%#v register=%#v", static, reg)
	}
}

func TestAuthParseEmitsCompatibilityAuthAttributes(t *testing.T) {
	resetConfig(t)
	var parsed authParseResponse
	callMethod(t, methodAuthParse, authParseRequest{
		Provider: authType,
		Path:     "/auths/opencode-go.json",
		FileName: "opencode-go.json",
		RawJSON:  []byte(`{"type":"opencode-go","api_key":"sk-test","label":"OpenCode Go"}`),
	}, &parsed)

	if !parsed.Handled {
		t.Fatal("credential not handled")
	}
	if parsed.Auth.Provider != authProvider {
		t.Fatalf("provider=%q want %q", parsed.Auth.Provider, authProvider)
	}
	if parsed.Auth.FileName != "opencode-go.json" || parsed.Auth.Label != "OpenCode Go" {
		t.Fatalf("auth=%#v", parsed.Auth)
	}
	want := map[string]string{
		"base_url":     defaultBaseURL,
		"api_key":      "sk-test",
		"compat_name":  compatName,
		"provider_key": providerKey,
	}
	for key, value := range want {
		if got := parsed.Auth.Attributes[key]; got != value {
			t.Fatalf("attribute %s=%q want %q", key, got, value)
		}
	}
}

func TestAuthParseHonoursCredentialBaseURL(t *testing.T) {
	resetConfig(t)
	var parsed authParseResponse
	callMethod(t, methodAuthParse, authParseRequest{
		Provider: authType,
		FileName: "opencode-go.json",
		RawJSON:  []byte(`{"type":"opencode-go","api_key":"sk-test","base_url":"https://example.invalid/v1"}`),
	}, &parsed)
	if got := parsed.Auth.Attributes["base_url"]; got != "https://example.invalid/v1" {
		t.Fatalf("base_url=%q", got)
	}
}

func TestAuthParseIgnoresOtherProviders(t *testing.T) {
	resetConfig(t)
	var parsed authParseResponse
	callMethod(t, methodAuthParse, authParseRequest{
		Provider: "gemini-cli",
		FileName: "gemini.json",
		RawJSON:  []byte(`{"type":"gemini-cli"}`),
	}, &parsed)
	if parsed.Handled {
		t.Fatalf("must not handle other providers: %#v", parsed)
	}
}

func TestAuthParseRejectsCredentialWithoutAPIKey(t *testing.T) {
	resetConfig(t)
	request, err := json.Marshal(authParseRequest{
		Provider: authType,
		FileName: "opencode-go.json",
		RawJSON:  []byte(`{"type":"opencode-go"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleMethod(methodAuthParse, request)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env.OK || env.Error == nil || env.Error.Code != "auth_parse_failed" {
		t.Fatalf("envelope=%s", raw)
	}
}

func TestConfigOverridesModelListAndBaseURL(t *testing.T) {
	resetConfig(t)
	callMethod(t, methodPluginReconfigure, lifecycleRequest{
		ConfigYAML: json.RawMessage(`"models: alpha, beta\nbase-url: https://proxy.invalid/v1\n"`),
	}, nil)

	var reg modelRegistrationResponse
	callMethod(t, methodModelRegister, nil, &reg)
	if len(reg.Models) != 2 || reg.Models[0].ID != "alpha" || reg.Models[1].ID != "beta" {
		t.Fatalf("models=%#v", reg.Models)
	}

	var parsed authParseResponse
	callMethod(t, methodAuthParse, authParseRequest{
		Provider: authType,
		FileName: "opencode-go.json",
		RawJSON:  []byte(`{"type":"opencode-go","api_key":"sk-test"}`),
	}, &parsed)
	if got := parsed.Auth.Attributes["base_url"]; got != "https://proxy.invalid/v1" {
		t.Fatalf("base_url=%q", got)
	}
}

func TestUnknownMethodReportsError(t *testing.T) {
	raw, err := handleMethod("does.not.exist", nil)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env.OK || env.Error == nil || env.Error.Code != "unknown_method" {
		t.Fatalf("envelope=%s", raw)
	}
}
