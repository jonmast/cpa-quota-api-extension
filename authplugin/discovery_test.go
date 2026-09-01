package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// recordedModelsPayload is a real GET /zen/go/v1/models response, captured from
// the provider. Tests assert against this rather than against whatever the
// plugin happens to hardcode -- the failure mode that let a stale six-model
// list survive eleven passing tests.
func recordedModelsPayload(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "models.json"))
	if err != nil {
		t.Fatalf("read recorded payload: %v", err)
	}
	return raw
}

func recordedModelIDs(t *testing.T) []string {
	t.Helper()
	var list modelListResponse
	if err := json.Unmarshal(recordedModelsPayload(t), &list); err != nil {
		t.Fatalf("decode recorded payload: %v", err)
	}
	ids := make([]string, 0, len(list.Data))
	for _, entry := range list.Data {
		ids = append(ids, entry.ID)
	}
	return ids
}

type fakeHost struct {
	mu       sync.Mutex
	requests []hostHTTPRequest
	logs     []string

	response hostHTTPResponse
	err      error
	block    chan struct{}
}

func (f *fakeHost) doHTTP(request hostHTTPRequest) (hostHTTPResponse, error) {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	block := f.block
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	return f.response, f.err
}

func (f *fakeHost) log(level, message string, _ map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append(f.logs, level+": "+message)
}

func (f *fakeHost) calls() []hostHTTPRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]hostHTTPRequest(nil), f.requests...)
}

func okHost(t *testing.T) *fakeHost {
	t.Helper()
	return &fakeHost{response: hostHTTPResponse{StatusCode: 200, Body: recordedModelsPayload(t)}}
}

// withHost installs a fake host bridge for the duration of a test, standing in
// for the cgo client that cliproxy_plugin_init would normally install.
func withHost(t *testing.T, host hostClient) {
	t.Helper()
	original := activeHost
	activeHost = host
	t.Cleanup(func() { activeHost = original })
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func resetDiscovery(t *testing.T) {
	t.Helper()
	discoveryCache.reset()
	t.Cleanup(discoveryCache.reset)
}

func TestDiscoveryReturnsEveryModelFromTheRecordedPayload(t *testing.T) {
	resetDiscovery(t)
	want := recordedModelIDs(t)

	got, err := discoverModels(okHost(t), defaultBaseURL, "sk-test", "")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("discovered %d models, recorded payload has %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("model %d = %q want %q", i, got[i], want[i])
		}
	}
}

// The list this plugin used to hardcode has no overlap with what the provider
// actually serves. Discovery must not resurrect any of it.
func TestPreviouslyHardcodedModelsAreNotServed(t *testing.T) {
	resetDiscovery(t)
	retired := []string{"grok-code", "code-supernova", "qwen3-coder", "kimi-k2", "glm-4.6", "minimax-m2"}

	got, err := discoverModels(okHost(t), defaultBaseURL, "sk-test", "")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	live := make(map[string]struct{}, len(got))
	for _, id := range got {
		live[id] = struct{}{}
	}
	for _, id := range retired {
		if _, found := live[id]; found {
			t.Fatalf("retired model %q is still being served", id)
		}
	}
}

func TestDiscoveryRequestsModelsEndpointWithBearerToken(t *testing.T) {
	resetDiscovery(t)
	host := okHost(t)

	if _, err := discoverModels(host, "https://opencode.ai/zen/go/v1/", "sk-secret", "cb-7"); err != nil {
		t.Fatalf("discover: %v", err)
	}
	calls := host.calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 request, got %d", len(calls))
	}
	request := calls[0]
	if request.URL != "https://opencode.ai/zen/go/v1/models" {
		t.Fatalf("url=%q", request.URL)
	}
	if request.Method != "GET" {
		t.Fatalf("method=%q", request.Method)
	}
	if got := request.Headers["Authorization"]; len(got) != 1 || got[0] != "Bearer sk-secret" {
		t.Fatalf("authorization=%v", got)
	}
	// The callback ID must be echoed so the host resolves the originating
	// request context (internal/pluginhost/host_callbacks.go:148).
	if request.HostCallbackID != "cb-7" {
		t.Fatalf("host_callback_id=%q", request.HostCallbackID)
	}
}

// The default base URL must be the OpenCode *Go* endpoint. Pointing at the
// plain /zen/v1 host silently yields the wrong product's models.
func TestDefaultBaseURLTargetsTheGoEndpoint(t *testing.T) {
	if defaultBaseURL != "https://opencode.ai/zen/go/v1" {
		t.Fatalf("defaultBaseURL=%q", defaultBaseURL)
	}
	if got := modelsEndpoint(defaultBaseURL); got != "https://opencode.ai/zen/go/v1/models" {
		t.Fatalf("models endpoint=%q", got)
	}
}

func TestDiscoveryServesLastGoodListWhenFetchFails(t *testing.T) {
	resetDiscovery(t)
	want := recordedModelIDs(t)

	if _, err := discoverModels(okHost(t), defaultBaseURL, "sk-test", ""); err != nil {
		t.Fatalf("seed discover: %v", err)
	}

	broken := &fakeHost{err: fmt.Errorf("connection refused")}
	got, err := discoverModels(broken, defaultBaseURL, "sk-test", "")
	if err != nil {
		t.Fatalf("expected cached fallback, got error: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("cached list has %d models, want %d", len(got), len(want))
	}
	if len(broken.logs) == 0 {
		t.Fatal("degraded fallback must be logged")
	}
}

// Without a cached list there is nothing honest to serve. Returning an error
// (rather than an empty list) is what stops the host unregistering the auth.
func TestDiscoveryErrorsOnColdStartFailure(t *testing.T) {
	resetDiscovery(t)
	if _, err := discoverModels(&fakeHost{err: fmt.Errorf("connection refused")}, defaultBaseURL, "sk-test", ""); err == nil {
		t.Fatal("cold start failure must surface as an error, not an empty list")
	}
}

func TestDiscoveryCacheIsScopedPerCredential(t *testing.T) {
	resetDiscovery(t)
	if _, err := discoverModels(okHost(t), defaultBaseURL, "sk-one", ""); err != nil {
		t.Fatalf("seed discover: %v", err)
	}
	broken := &fakeHost{err: fmt.Errorf("connection refused")}
	if _, err := discoverModels(broken, defaultBaseURL, "sk-two", ""); err == nil {
		t.Fatal("a different credential must not reuse another credential's cache")
	}
}

func TestDiscoveryRejectsNonSuccessStatus(t *testing.T) {
	resetDiscovery(t)
	host := &fakeHost{response: hostHTTPResponse{StatusCode: 401, Body: []byte(`{"error":"unauthorized"}`)}}
	if _, err := discoverModels(host, defaultBaseURL, "sk-bad", ""); err == nil {
		t.Fatal("HTTP 401 must not be treated as a model list")
	}
}

func TestDiscoveryRejectsEmptyModelList(t *testing.T) {
	resetDiscovery(t)
	host := &fakeHost{response: hostHTTPResponse{StatusCode: 200, Body: []byte(`{"object":"list","data":[]}`)}}
	if _, err := discoverModels(host, defaultBaseURL, "sk-test", ""); err == nil {
		t.Fatal("an empty data array must not register as zero models")
	}
}

// The host applies no deadline to plugin calls and host.http.do has no client
// timeout, so a hung provider must not block model registration forever.
func TestDiscoveryTimesOutRatherThanBlocking(t *testing.T) {
	resetDiscovery(t)
	original := discoveryTimeout
	discoveryTimeout = 20 * time.Millisecond
	t.Cleanup(func() { discoveryTimeout = original })

	blocked := make(chan struct{})
	defer close(blocked)

	host := &fakeHost{block: blocked}
	done := make(chan error, 1)
	go func() {
		_, err := discoverModels(host, defaultBaseURL, "sk-test", "")
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a blocked host call must not report success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("discovery did not return within its own timeout budget")
	}
}

func TestNilHostIsTreatedAsFailureNotPanic(t *testing.T) {
	resetDiscovery(t)
	if _, err := discoverModels(nil, defaultBaseURL, "sk-test", ""); err == nil {
		t.Fatal("a missing host bridge must surface as an error")
	}
}
