package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// discoveryTimeout bounds a single GET /models call.
//
// The host applies no deadline of its own to plugin calls, and host.http.do
// builds its client with NewProxyAwareHTTPClient(..., 0) -- i.e. no client
// timeout -- so an unresponsive provider would otherwise block model
// registration indefinitely.
// It is a var only so tests can shrink it.
var discoveryTimeout = 10 * time.Second

// modelCache retains the most recent successful discovery per credential.
//
// There is deliberately no hardcoded model list: the live provider list is the
// only source of truth. The cache exists solely so a transient fetch failure
// does not collapse the model list to empty, which the host treats as
// "unregister this auth" (sdk/cliproxy/service.go:1239).
type modelCache struct {
	mu      sync.RWMutex
	entries map[string][]string
}

var discoveryCache = &modelCache{entries: make(map[string][]string)}

// cacheKey identifies a credential without retaining the API key in a form
// that could be logged. Discovery results differ per base URL and per key.
func cacheKey(baseURL, apiKey string) string {
	return baseURL + "\x00" + apiKey
}

func (c *modelCache) get(key string) ([]string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	models, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	return append([]string(nil), models...), true
}

func (c *modelCache) put(key string, models []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = append([]string(nil), models...)
}

// any returns an arbitrary cached list, used to answer model.static, which is
// called without a credential and so cannot discover on its own.
func (c *modelCache) any() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, models := range c.entries {
		return append([]string(nil), models...)
	}
	return nil
}

func (c *modelCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string][]string)
}

// modelsEndpoint joins the configured base URL with the /models path.
func modelsEndpoint(baseURL string) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/models"
}

// fetchModels performs GET {baseURL}/models and returns the model IDs.
func fetchModels(client hostClient, baseURL, apiKey, callbackID string) ([]string, error) {
	if client == nil {
		return nil, fmt.Errorf("host callbacks unavailable")
	}
	request := hostHTTPRequest{
		HostCallbackID: callbackID,
		Method:         http.MethodGet,
		URL:            modelsEndpoint(baseURL),
		Headers: map[string][]string{
			"Authorization": {"Bearer " + apiKey},
			"Accept":        {"application/json"},
		},
	}

	response, err := callWithTimeout(client, request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("model discovery returned HTTP %d", response.StatusCode)
	}

	var list modelListResponse
	if err := json.Unmarshal(response.Body, &list); err != nil {
		return nil, fmt.Errorf("decode model list: %w", err)
	}
	models := make([]string, 0, len(list.Data))
	seen := make(map[string]struct{}, len(list.Data))
	for _, entry := range list.Data {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, id)
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("model discovery returned no models")
	}
	return models, nil
}

// callWithTimeout bounds a host.http.do callback.
//
// host.http.do is a synchronous C callback with no context parameter, so the
// timeout cannot be pushed down into the host. On expiry we abandon the
// in-flight goroutine; it leaks until the underlying request completes, which
// is bounded and preferable to blocking model registration forever.
func callWithTimeout(client hostClient, request hostHTTPRequest) (hostHTTPResponse, error) {
	type result struct {
		response hostHTTPResponse
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, err := client.doHTTP(request)
		done <- result{response: response, err: err}
	}()

	select {
	case outcome := <-done:
		return outcome.response, outcome.err
	case <-time.After(discoveryTimeout):
		return hostHTTPResponse{}, fmt.Errorf("model discovery timed out after %s", discoveryTimeout)
	}
}

// discoverModels returns the live model list for a credential, falling back to
// the last successful result for that credential if the fetch fails. It
// returns an error only when there is nothing cached to serve, so that a cold
// start failure surfaces as an error rather than as an empty list.
func discoverModels(client hostClient, baseURL, apiKey, callbackID string) ([]string, error) {
	key := cacheKey(baseURL, apiKey)
	models, err := fetchModels(client, baseURL, apiKey, callbackID)
	if err == nil {
		discoveryCache.put(key, models)
		return models, nil
	}
	if cached, ok := discoveryCache.get(key); ok {
		if client != nil {
			client.log("warn", "opencode-go model discovery failed; serving last known model list", map[string]any{
				"error":  err.Error(),
				"models": len(cached),
			})
		}
		return cached, nil
	}
	return nil, err
}
