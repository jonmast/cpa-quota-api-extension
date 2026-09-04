package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// executorHost fakes the host callbacks the executor uses: one HTTP call (plain
// or streaming), a scripted sequence of stream reads, and a recorder for the
// chunks the plugin emits back to the host.
type executorHost struct {
	mu sync.Mutex

	request  hostHTTPRequest
	response hostHTTPResponse
	err      error

	openStream hostHTTPStreamResponse
	reads      []hostHTTPStreamReadResponse
	readIndex  int

	emitted    [][]byte
	closeErr   string
	closed     bool
	httpClosed bool
	done       chan struct{}
}

func newExecutorHost() *executorHost {
	return &executorHost{done: make(chan struct{})}
}

func (h *executorHost) doHTTP(request hostHTTPRequest) (hostHTTPResponse, error) {
	h.mu.Lock()
	h.request = request
	h.mu.Unlock()
	return h.response, h.err
}

func (h *executorHost) invoke(method string, payload any) (json.RawMessage, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch method {
	case methodHostHTTPDoStream:
		h.request = payload.(hostHTTPRequest)
		if h.err != nil {
			return nil, h.err
		}
		return mustMarshal(h.openStream), nil
	case methodHostHTTPStreamRead:
		if h.readIndex >= len(h.reads) {
			return mustMarshal(hostHTTPStreamReadResponse{Done: true}), nil
		}
		chunk := h.reads[h.readIndex]
		h.readIndex++
		return mustMarshal(chunk), nil
	case methodHostHTTPStreamClose:
		h.httpClosed = true
		return mustMarshal(struct{}{}), nil
	case methodHostStreamEmit:
		emit := payload.(hostStreamEmitRequest)
		h.emitted = append(h.emitted, emit.Payload)
		return mustMarshal(struct{}{}), nil
	case methodHostStreamClose:
		h.closeErr = payload.(hostStreamCloseRequest).Error
		if !h.closed {
			h.closed = true
			close(h.done)
		}
		return mustMarshal(struct{}{}), nil
	default:
		return nil, fmt.Errorf("unexpected host callback %s", method)
	}
}

func (h *executorHost) log(string, string, map[string]any) {}

func (h *executorHost) sentRequest() hostHTTPRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.request
}

func (h *executorHost) chunks(t *testing.T) []string {
	t.Helper()
	select {
	case <-h.done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream never closed")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.emitted))
	for _, chunk := range h.emitted {
		out = append(out, string(chunk))
	}
	return out
}

func mustMarshal(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

func baseExecutorRequest() executorRequest {
	return executorRequest{
		AuthID:         "opencode-go",
		AuthProvider:   providerKey,
		Model:          "claude-sonnet-4",
		Payload:        []byte(`{"model":"claude-sonnet-4","messages":[]}`),
		AuthAttributes: map[string]string{"api_key": "sk-test", "base_url": "https://example.invalid/v1"},
		Headers: map[string][]string{
			"X-Opencode-Session": {"ses_abc123"},
			"Authorization":      {"Bearer client-token"},
		},
	}
}

// The whole reason this plugin owns an executor.
func TestExecuteForwardsSessionHeaderUpstream(t *testing.T) {
	host := newExecutorHost()
	host.response = hostHTTPResponse{StatusCode: 200, Body: []byte(`{"id":"chatcmpl"}`)}

	if _, execErr := executeUpstream(host, baseExecutorRequest()); execErr != nil {
		t.Fatalf("execute: %+v", execErr)
	}

	sent := host.sentRequest()
	if got := sent.Headers["X-Opencode-Session"]; len(got) != 1 || got[0] != "ses_abc123" {
		t.Fatalf("x-opencode-session header=%#v", sent.Headers)
	}
	// The client's own Authorization must not leak through; the credential's
	// key is what authenticates to oc-go.
	if got := sent.Headers["Authorization"]; len(got) != 1 || got[0] != "Bearer sk-test" {
		t.Fatalf("authorization=%#v", got)
	}
	if sent.URL != "https://example.invalid/v1/chat/completions" {
		t.Fatalf("url=%q", sent.URL)
	}
}

func TestExecuteStreamForwardsSessionHeaderUpstream(t *testing.T) {
	host := newExecutorHost()
	host.openStream = hostHTTPStreamResponse{StatusCode: 200, StreamID: "1"}
	host.reads = []hostHTTPStreamReadResponse{{Payload: []byte("data: {\"a\":1}\n"), Done: true}}

	req := baseExecutorRequest()
	req.Stream = true
	req.StreamID = "plugin-1"
	if _, execErr := executeUpstreamStream(host, req); execErr != nil {
		t.Fatalf("execute stream: %+v", execErr)
	}
	host.chunks(t)

	if got := host.sentRequest().Headers["X-Opencode-Session"]; len(got) != 1 || got[0] != "ses_abc123" {
		t.Fatalf("x-opencode-session header=%#v", got)
	}
}

// Header names arrive from JSON in whatever case the client sent them.
func TestSessionHeaderMatchIsCaseInsensitive(t *testing.T) {
	host := newExecutorHost()
	host.response = hostHTTPResponse{StatusCode: 200}
	req := baseExecutorRequest()
	req.Headers = map[string][]string{"x-opencode-session": {"ses_lower"}}

	if _, execErr := executeUpstream(host, req); execErr != nil {
		t.Fatalf("execute: %+v", execErr)
	}
	if got := host.sentRequest().Headers["X-Opencode-Session"]; len(got) != 1 || got[0] != "ses_lower" {
		t.Fatalf("header=%#v", got)
	}
}

func TestExecuteOmitsSessionHeaderWhenClientSendsNone(t *testing.T) {
	host := newExecutorHost()
	host.response = hostHTTPResponse{StatusCode: 200}
	req := baseExecutorRequest()
	req.Headers = nil

	if _, execErr := executeUpstream(host, req); execErr != nil {
		t.Fatalf("execute: %+v", execErr)
	}
	if _, present := host.sentRequest().Headers["X-Opencode-Session"]; present {
		t.Fatal("empty session header must not be sent")
	}
}

func TestExecuteMapsUpstreamStatusToEnvelopeError(t *testing.T) {
	host := newExecutorHost()
	host.response = hostHTTPResponse{StatusCode: 429, Body: []byte(`{"error":"slow down"}`)}

	_, execErr := executeUpstream(host, baseExecutorRequest())
	if execErr == nil {
		t.Fatal("expected an error")
	}
	if execErr.HTTPStatus != 429 || !execErr.Retryable {
		t.Fatalf("error=%+v", execErr)
	}
}

func TestExecuteStreamSurfacesUpstreamFailureBeforeStreaming(t *testing.T) {
	host := newExecutorHost()
	host.openStream = hostHTTPStreamResponse{StatusCode: 401, StreamID: "1"}
	host.reads = []hostHTTPStreamReadResponse{{Payload: []byte(`{"error":"bad key"}`), Done: true}}

	req := baseExecutorRequest()
	req.StreamID = "plugin-1"
	_, execErr := executeUpstreamStream(host, req)
	if execErr == nil || execErr.HTTPStatus != 401 {
		t.Fatalf("error=%+v", execErr)
	}
	if !strings.Contains(execErr.Message, "bad key") {
		t.Fatalf("message=%q", execErr.Message)
	}
}

// SSE lines are reassembled across the arbitrary byte boundaries that
// host.http.stream_read hands back.
func TestStreamReassemblesLinesAcrossReads(t *testing.T) {
	host := newExecutorHost()
	host.openStream = hostHTTPStreamResponse{StatusCode: 200, StreamID: "1"}
	host.reads = []hostHTTPStreamReadResponse{
		{Payload: []byte("data: {\"i\":")},
		{Payload: []byte("1}\n\ndata: {\"i\":2}\n\n")},
		{Payload: []byte("data: [DONE]\n\n"), Done: true},
	}

	req := baseExecutorRequest()
	req.StreamID = "plugin-1"
	if _, execErr := executeUpstreamStream(host, req); execErr != nil {
		t.Fatalf("execute stream: %+v", execErr)
	}

	got := host.chunks(t)
	want := []string{`data: {"i":1}`, `data: {"i":2}`}
	if len(got) != len(want) {
		t.Fatalf("chunks=%#v want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("chunk %d=%q want %q", i, got[i], want[i])
		}
	}
	if host.closeErr != "" {
		t.Fatalf("stream closed with error %q", host.closeErr)
	}
	if !host.httpClosed {
		t.Fatal("upstream http stream was not closed")
	}
}

// An OpenAI chat-completions client bypasses host-side translation, and the
// handler adds the "data: " prefix itself, so chunks must be bare JSON.
func TestStreamEmitsBareJSONForOpenAIChatCompletionsClients(t *testing.T) {
	host := newExecutorHost()
	host.openStream = hostHTTPStreamResponse{StatusCode: 200, StreamID: "1"}
	host.reads = []hostHTTPStreamReadResponse{
		{Payload: []byte("data: {\"i\":1}\n\ndata: [DONE]\n\n"), Done: true},
	}

	req := baseExecutorRequest()
	req.StreamID = "plugin-1"
	req.Metadata = map[string]any{"request_path": "/v1/chat/completions"}
	if _, execErr := executeUpstreamStream(host, req); execErr != nil {
		t.Fatalf("execute stream: %+v", execErr)
	}

	got := host.chunks(t)
	if len(got) != 1 || got[0] != `{"i":1}` {
		t.Fatalf("chunks=%#v", got)
	}
}

// The host and the OpenAI handler each emit their own terminal marker.
func TestStreamDropsDoneMarker(t *testing.T) {
	host := newExecutorHost()
	host.openStream = hostHTTPStreamResponse{StatusCode: 200, StreamID: "1"}
	host.reads = []hostHTTPStreamReadResponse{{Payload: []byte("data: [DONE]\n\n"), Done: true}}

	req := baseExecutorRequest()
	req.StreamID = "plugin-1"
	if _, execErr := executeUpstreamStream(host, req); execErr != nil {
		t.Fatalf("execute stream: %+v", execErr)
	}
	if got := host.chunks(t); len(got) != 0 {
		t.Fatalf("chunks=%#v", got)
	}
}

func TestStreamPropagatesMidStreamError(t *testing.T) {
	host := newExecutorHost()
	host.openStream = hostHTTPStreamResponse{StatusCode: 200, StreamID: "1"}
	host.reads = []hostHTTPStreamReadResponse{
		{Payload: []byte("data: {\"i\":1}\n")},
		{Error: "upstream reset"},
	}

	req := baseExecutorRequest()
	req.StreamID = "plugin-1"
	if _, execErr := executeUpstreamStream(host, req); execErr != nil {
		t.Fatalf("execute stream: %+v", execErr)
	}
	host.chunks(t)
	if host.closeErr != "upstream reset" {
		t.Fatalf("close error=%q", host.closeErr)
	}
}

func TestExecuteRejectsAuthWithoutAPIKey(t *testing.T) {
	host := newExecutorHost()
	req := baseExecutorRequest()
	req.AuthAttributes = map[string]string{"base_url": "https://example.invalid/v1"}

	_, execErr := executeUpstream(host, req)
	if execErr == nil || execErr.HTTPStatus != 401 {
		t.Fatalf("error=%+v", execErr)
	}
}

func TestExecutePinsStreamFlagToHostIntent(t *testing.T) {
	host := newExecutorHost()
	host.response = hostHTTPResponse{StatusCode: 200}
	req := baseExecutorRequest()
	req.Payload = []byte(`{"model":"m","stream":true}`)

	if _, execErr := executeUpstream(host, req); execErr != nil {
		t.Fatalf("execute: %+v", execErr)
	}
	var body map[string]any
	if err := json.Unmarshal(host.sentRequest().Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["stream"] != false {
		t.Fatalf("stream=%v want false", body["stream"])
	}
}

func TestStaticHeaderAttributesStillApply(t *testing.T) {
	host := newExecutorHost()
	host.response = hostHTTPResponse{StatusCode: 200}
	req := baseExecutorRequest()
	req.AuthAttributes["header:x-tenant"] = "acme"

	if _, execErr := executeUpstream(host, req); execErr != nil {
		t.Fatalf("execute: %+v", execErr)
	}
	if got := host.sentRequest().Headers["X-Tenant"]; len(got) != 1 || got[0] != "acme" {
		t.Fatalf("header=%#v", got)
	}
}
