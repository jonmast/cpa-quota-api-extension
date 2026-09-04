package main

import (
	"encoding/json"
	"testing"
)

func TestConfigFieldsUseIntegerTypesForIntegralValues(t *testing.T) {
	want := map[string]bool{
		"max-concurrency": false, "degraded-failure-threshold": false,
		"incident-max-rows": false, "history-max-rows": false,
		"usage-queue-size": false, "alert-lost-threshold": false,
		"alert-degraded-threshold": false,
	}
	for _, field := range pluginRegistration().Metadata.ConfigFields {
		if _, ok := want[field.Name]; ok {
			if field.Type != "integer" {
				t.Fatalf("field %s type=%q", field.Name, field.Type)
			}
			want[field.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("missing config field %s", name)
		}
	}
}

func TestRegistrationWire(t *testing.T) {
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
	wantRoutes := map[string]bool{
		quotaRoute: false, accountRoute: false, statusRoute: false,
		healthRoute: false, incidentsRoute: false, historyRoute: false, profileRoute: false,
	}
	if len(reg.Routes) != len(wantRoutes) {
		t.Fatalf("wire=%s result=%s routes=%#v", raw, env.Result, reg.Routes)
	}
	for _, route := range reg.Routes {
		if route.Method != "GET" {
			t.Fatalf("unexpected route method: %#v", route)
		}
		if _, ok := wantRoutes[route.Path]; !ok {
			t.Fatalf("unexpected route: %#v", route)
		}
		wantRoutes[route.Path] = true
	}
	for path, found := range wantRoutes {
		if !found {
			t.Fatalf("missing route %s", path)
		}
	}
	if len(reg.Resources) != 1 {
		t.Fatalf("resources=%#v", reg.Resources)
	}
	resource := reg.Resources[0]
	if resource.Path != panelResourcePath || resource.Menu != "CPA Quota" || resource.Description == "" {
		t.Fatalf("resource=%#v", resource)
	}
	if pluginRegistration().Metadata.Version != "0.3.0" {
		t.Fatalf("version=%q", pluginRegistration().Metadata.Version)
	}
}
