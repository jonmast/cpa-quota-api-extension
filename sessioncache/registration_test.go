package main

import (
	"encoding/json"
	"testing"
)

// TestRegistrationWire drives plugin.register through the dispatch surface and
// asserts the exact wire keys the host expects: schema version 4 and the three
// capability keys proven by the prototype.
func TestRegistrationWire(t *testing.T) {
	raw, err := handleMethod(methodPluginRegister, nil)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Fatalf("wire=%s", raw)
	}
	var reg struct {
		SchemaVersion uint32          `json:"schema_version"`
		Metadata      map[string]any  `json:"metadata"`
		Capabilities  map[string]bool `json:"capabilities"`
	}
	if err := json.Unmarshal(env.Result, &reg); err != nil {
		t.Fatal(err)
	}
	if reg.SchemaVersion != 4 {
		t.Fatalf("schema_version=%d", reg.SchemaVersion)
	}
	for _, key := range []string{"response_interceptor", "response_stream_interceptor", "management_api"} {
		if !reg.Capabilities[key] {
			t.Fatalf("capability %s missing or false: %s", key, env.Result)
		}
	}
	if reg.Capabilities["stream_chunk_interceptor"] {
		t.Fatalf("wrong capability key stream_chunk_interceptor present: %s", env.Result)
	}
	if reg.Metadata["Name"] != "CPA Session Cache" || reg.Metadata["Version"] != pluginVersion {
		t.Fatalf("metadata=%v", reg.Metadata)
	}
}

func TestManagementRegistrationWire(t *testing.T) {
	raw, err := handleMethod(methodManagementRegister, nil)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var reg managementRegistrationResponse
	if err := json.Unmarshal(env.Result, &reg); err != nil {
		t.Fatal(err)
	}
	if len(reg.Routes) != 2 {
		t.Fatalf("routes=%#v", reg.Routes)
	}
	want := map[string]bool{sessionsRoute: false, sessionDetailRoute: false}
	for _, route := range reg.Routes {
		if route.Method != "GET" || route.Description == "" {
			t.Fatalf("route=%#v", route)
		}
		if _, ok := want[route.Path]; !ok {
			t.Fatalf("unexpected route=%#v", route)
		}
		want[route.Path] = true
	}
	for path, found := range want {
		if !found {
			t.Fatalf("missing route %s", path)
		}
	}
	if len(reg.Resources) != 1 {
		t.Fatalf("resources=%#v", reg.Resources)
	}
	resource := reg.Resources[0]
	if resource.Path != panelResourcePath || resource.Menu != "Session Cache" || resource.Description == "" {
		t.Fatalf("resource=%#v", resource)
	}
}

func TestUnknownMethodReturnsErrorEnvelope(t *testing.T) {
	raw, err := handleMethod("no.such.method", nil)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env.OK || env.Error == nil || env.Error.Code != "unknown_method" {
		t.Fatalf("wire=%s", raw)
	}
}
