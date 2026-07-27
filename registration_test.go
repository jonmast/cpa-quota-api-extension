package main

import (
	"encoding/json"
	"testing"
)

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
	if len(reg.Routes) != 3 {
		t.Fatalf("wire=%s result=%s routes=%#v", raw, env.Result, reg.Routes)
	}
}
