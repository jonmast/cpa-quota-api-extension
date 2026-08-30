package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// fakeHost is a programmable test double for hostClient.
//
// It serves credential JSON per auth index and HTTP responses per URL, and can
// be told to fail a given URL with a transport error or a rate-limited (429)
// response. Tests use it to drive real provider fetchers through
// runtimeState.handleManagement and assert on the emitted JSON.
type fakeHost struct {
	mu        sync.Mutex
	entries   []hostAuthFileEntry
	listCalls int
	listErr   error
	listDelay time.Duration

	credentials map[string]json.RawMessage
	authErrors  map[string]error

	responses  map[string]hostHTTPResponse
	httpErrors map[string]error
	httpCalls  map[string]int
}

func newFakeHost() *fakeHost {
	return &fakeHost{
		listDelay:   20 * time.Millisecond,
		credentials: map[string]json.RawMessage{},
		authErrors:  map[string]error{},
		responses:   map[string]hostHTTPResponse{},
		httpErrors:  map[string]error{},
		httpCalls:   map[string]int{},
	}
}

// withEntry registers a credential entry returned by listAuth.
func (f *fakeHost) withEntry(entry hostAuthFileEntry) *fakeHost {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, entry)
	return f
}

// withCredential serves credential JSON for an auth index.
func (f *fakeHost) withCredential(authIndex, credentialJSON string) *fakeHost {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.credentials[authIndex] = json.RawMessage(credentialJSON)
	return f
}

// withCredentialError makes host.auth.get fail for an auth index.
func (f *fakeHost) withCredentialError(authIndex string, err error) *fakeHost {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authErrors[authIndex] = err
	return f
}

// withResponse serves a canned HTTP response for a URL.
func (f *fakeHost) withResponse(url string, status int, body string) *fakeHost {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses[url] = hostHTTPResponse{
		StatusCode: status,
		Headers:    map[string][]string{"Content-Type": {"application/json"}},
		Body:       []byte(body),
	}
	return f
}

// withJSON serves a 200 JSON response for a URL.
func (f *fakeHost) withJSON(url, body string) *fakeHost {
	return f.withResponse(url, http.StatusOK, body)
}

// withRateLimit serves a 429 response for a URL.
func (f *fakeHost) withRateLimit(url string) *fakeHost {
	return f.withResponse(url, http.StatusTooManyRequests, `{"error":"rate limited"}`)
}

// withTransportError makes doHTTP return an error for a URL.
func (f *fakeHost) withTransportError(url string, err error) *fakeHost {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.httpErrors[url] = err
	return f
}

// requestCount reports how many doHTTP calls targeted a URL.
func (f *fakeHost) requestCount(url string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.httpCalls[url]
}

// listAuthCount reports how many times listAuth was called.
func (f *fakeHost) listAuthCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listCalls
}

func (f *fakeHost) listAuth(context.Context) ([]hostAuthFileEntry, error) {
	f.mu.Lock()
	f.listCalls++
	entries := append([]hostAuthFileEntry(nil), f.entries...)
	err, delay := f.listErr, f.listDelay
	f.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func (f *fakeHost) getAuth(_ context.Context, authIndex string) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.authErrors[authIndex]; err != nil {
		return nil, err
	}
	if raw, ok := f.credentials[authIndex]; ok {
		return raw, nil
	}
	return nil, nil
}

func (f *fakeHost) getAuthRuntime(_ context.Context, authIndex string) (hostAuthFileEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, entry := range f.entries {
		if entry.AuthIndex == authIndex {
			return entry, nil
		}
	}
	return hostAuthFileEntry{}, nil
}

func (f *fakeHost) doHTTP(_ context.Context, request hostHTTPRequest) (hostHTTPResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.httpCalls[request.URL]++
	if err := f.httpErrors[request.URL]; err != nil {
		return hostHTTPResponse{}, err
	}
	if resp, ok := f.responses[request.URL]; ok {
		return resp, nil
	}
	return hostHTTPResponse{}, fmt.Errorf("fakeHost: no canned response for %s", request.URL)
}

func (f *fakeHost) log(string, string, map[string]any) {}
