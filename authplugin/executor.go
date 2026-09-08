package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// This file implements the plugin-owned provider executor for opencode-go.
//
// Why not the built-in openai-compatibility executor? Because oc-go requires
// the client's x-opencode-session header to be forwarded upstream, and the
// built-in executor never copies request headers onto the upstream call: it
// sets only Content-Type, Authorization, User-Agent and (for SSE) Accept plus
// Cache-Control, then applies the static header:* auth attributes
// (upstream/CLIProxyAPI/internal/runtime/executor/openai_compat_executor.go:133
// and :332). A static attribute cannot carry a per-request session ID, and the
// pinned upstream offers no request-header passthrough, so the only way to
// forward it is to own the outbound request.

// forwardedClientHeaders are copied verbatim from the inbound client request to
// the oc-go request when present. Matching is case-insensitive.
var forwardedClientHeaders = []string{
	"x-opencode-session",
}

const executorUserAgent = "cpa-opencode-go-auth/" + pluginVersion

// chatCompletionsEndpoint mirrors the path the built-in compat executor uses.
func chatCompletionsEndpoint(baseURL string) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/chat/completions"
}

// executorCredentials pulls the base URL and API key stamped onto the auth by
// parseAuth. The host echoes them back to us in AuthAttributes.
func executorCredentials(req executorRequest) (baseURL, apiKey string, err error) {
	apiKey = strings.TrimSpace(req.AuthAttributes["api_key"])
	if apiKey == "" {
		return "", "", fmt.Errorf("auth %q has no api_key attribute", req.AuthID)
	}
	baseURL = strings.TrimSpace(req.AuthAttributes[baseURLAttribute])
	if baseURL == "" {
		baseURL = currentConfig().BaseURL
	}
	if baseURL == "" {
		return "", "", fmt.Errorf("auth %q has no base_url", req.AuthID)
	}
	return baseURL, apiKey, nil
}

// upstreamHeaders builds the oc-go request headers, forwarding the allow-listed
// client headers on top of the fixed set.
func upstreamHeaders(req executorRequest, apiKey string, stream bool) map[string][]string {
	headers := map[string][]string{
		"Content-Type":  {"application/json"},
		"Authorization": {"Bearer " + apiKey},
		"User-Agent":    {executorUserAgent},
	}
	if stream {
		headers["Accept"] = []string{"text/event-stream"}
		headers["Cache-Control"] = []string{"no-cache"}
	} else {
		headers["Accept"] = []string{"application/json"}
	}
	for _, name := range forwardedClientHeaders {
		if value := lookupHeader(req.Headers, name); value != "" {
			headers[http.CanonicalHeaderKey(name)] = []string{value}
		}
	}
	// Static header:* attributes remain honoured for parity with the built-in
	// compat executor, which applies them via ApplyCustomHeadersFromAttrs.
	for key, value := range req.AuthAttributes {
		name, ok := strings.CutPrefix(key, "header:")
		if !ok || strings.TrimSpace(name) == "" {
			continue
		}
		headers[http.CanonicalHeaderKey(strings.TrimSpace(name))] = []string{value}
	}
	return headers
}

// lookupHeader does a case-insensitive lookup, since the host delivers headers
// as a plain map decoded from JSON rather than as an http.Header.
func lookupHeader(headers map[string][]string, name string) string {
	for key, values := range headers {
		if !strings.EqualFold(key, name) {
			continue
		}
		for _, value := range values {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// withStreamFlag forces the "stream" field to match what the host asked for.
// The host has already translated the payload into OpenAI chat-completions
// format, but the flag is the one field where a mismatch silently changes the
// upstream response shape, so it is pinned rather than trusted.
func withStreamFlag(payload []byte, stream bool) []byte {
	if len(bytes.TrimSpace(payload)) == 0 {
		return payload
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(payload, &body); err != nil {
		return payload
	}
	flag := []byte("false")
	if stream {
		flag = []byte("true")
	}
	body["stream"] = flag
	encoded, err := json.Marshal(body)
	if err != nil {
		return payload
	}
	return encoded
}

// withBareModel strips the auth's model prefix from the payload's model field.
//
// Models are registered under both "foo" and "<prefix>/foo"
// (sdk/cliproxy/service_models.go:600-614), and the host resolves whichever the
// client asked for to the prefixed canonical ID before handing us the payload.
// oc-go has never heard of the prefix -- it is a local routing namespace -- so
// sending it through unmodified earns "Model opencode-go/foo is not supported".
func withBareModel(payload []byte, prefix string) []byte {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || len(bytes.TrimSpace(payload)) == 0 {
		return payload
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(payload, &body); err != nil {
		return payload
	}
	raw, ok := body["model"]
	if !ok {
		return payload
	}
	var model string
	if err := json.Unmarshal(raw, &model); err != nil {
		return payload
	}
	bare, found := strings.CutPrefix(model, prefix+"/")
	if !found || strings.TrimSpace(bare) == "" {
		return payload
	}
	encoded, err := json.Marshal(bare)
	if err != nil {
		return payload
	}
	body["model"] = encoded
	rebuilt, err := json.Marshal(body)
	if err != nil {
		return payload
	}
	return rebuilt
}

// upstreamBody prepares the payload oc-go actually receives.
func upstreamBody(req executorRequest, stream bool) []byte {
	return withBareModel(withStreamFlag(req.Payload, stream), req.AuthAttributes[modelPrefixAttribute])
}

// executeUpstream runs the non-streaming chat completion.
func executeUpstream(host hostClient, req executorRequest) (executorResponse, *envelopeError) {
	if host == nil {
		return executorResponse{}, &envelopeError{Code: "executor_error", Message: "host callbacks unavailable"}
	}
	baseURL, apiKey, err := executorCredentials(req)
	if err != nil {
		return executorResponse{}, &envelopeError{Code: "executor_error", Message: err.Error(), HTTPStatus: http.StatusUnauthorized}
	}
	response, err := host.doHTTP(hostHTTPRequest{
		HostCallbackID: req.HostCallbackID,
		Method:         http.MethodPost,
		URL:            chatCompletionsEndpoint(baseURL),
		Headers:        upstreamHeaders(req, apiKey, false),
		Body:           upstreamBody(req, false),
	})
	if err != nil {
		return executorResponse{}, &envelopeError{Code: "executor_error", Message: err.Error(), Retryable: true}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return executorResponse{}, upstreamStatusError(response.StatusCode, response.Body)
	}
	return executorResponse{Payload: response.Body, Headers: response.Headers}, nil
}

// upstreamStatusError maps a non-2xx upstream reply onto the envelope error the
// host turns back into a client-facing status.
func upstreamStatusError(status int, body []byte) *envelopeError {
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = fmt.Sprintf("opencode-go returned HTTP %d", status)
	}
	return &envelopeError{
		Code:       "upstream_error",
		Message:    message,
		HTTPStatus: status,
		Retryable:  status == http.StatusTooManyRequests || status >= http.StatusInternalServerError,
	}
}

// executeUpstreamStream opens the SSE call and hands the pump off to a
// goroutine. It returns as soon as the upstream status is known, which is what
// lets the host surface an immediate upstream failure as a normal error rather
// than as a broken stream.
func executeUpstreamStream(host hostClient, req executorRequest) (executorStreamResponse, *envelopeError) {
	if host == nil {
		return executorStreamResponse{}, &envelopeError{Code: "executor_error", Message: "host callbacks unavailable"}
	}
	if strings.TrimSpace(req.StreamID) == "" {
		return executorStreamResponse{}, &envelopeError{Code: "executor_error", Message: "stream_id is required"}
	}
	baseURL, apiKey, err := executorCredentials(req)
	if err != nil {
		return executorStreamResponse{}, &envelopeError{Code: "executor_error", Message: err.Error(), HTTPStatus: http.StatusUnauthorized}
	}
	raw, err := host.invoke(methodHostHTTPDoStream, hostHTTPRequest{
		HostCallbackID: req.HostCallbackID,
		Method:         http.MethodPost,
		URL:            chatCompletionsEndpoint(baseURL),
		Headers:        upstreamHeaders(req, apiKey, true),
		Body:           upstreamBody(req, true),
	})
	if err != nil {
		return executorStreamResponse{}, &envelopeError{Code: "executor_error", Message: err.Error(), Retryable: true}
	}
	var opened hostHTTPStreamResponse
	if err := json.Unmarshal(raw, &opened); err != nil {
		return executorStreamResponse{}, &envelopeError{Code: "executor_error", Message: "decode host stream response: " + err.Error()}
	}
	if opened.StatusCode < 200 || opened.StatusCode >= 300 {
		body := drainHTTPStream(host, opened.StreamID)
		return executorStreamResponse{}, upstreamStatusError(opened.StatusCode, body)
	}
	if strings.TrimSpace(opened.StreamID) == "" {
		return executorStreamResponse{}, &envelopeError{Code: "executor_error", Message: "host returned an empty stream_id"}
	}

	bare := emitsBareJSONChunks(req)
	go pumpUpstreamStream(host, opened.StreamID, req.StreamID, bare)

	return executorStreamResponse{Headers: opened.Headers}, nil
}

// emitsBareJSONChunks decides the wire shape of the chunks we emit, which the
// pinned upstream defines inconsistently for the openai output format:
//
//   - When the client's requested format differs from ours, the host runs our
//     chunks through the openai source translators, which require an SSE
//     "data:" prefix and discard anything without one
//     (internal/translator/openai/claude/openai_claude_response.go:106).
//   - When the requested format is also openai chat completions, the host
//     skips translation entirely (internal/pluginhost/adapters.go:1498) and the
//     handler writes `data: %s` around whatever we emit
//     (sdk/api/handlers/openai/openai_handlers.go:668), so a prefixed chunk
//     would be double-prefixed on the wire.
//
// The executor request carries only our own normalized formats, so the client's
// entry protocol is inferred from the request path metadata instead.
func emitsBareJSONChunks(req executorRequest) bool {
	path, _ := req.Metadata["request_path"].(string)
	path = strings.ToLower(strings.TrimSpace(path))
	if path == "" {
		return false
	}
	return strings.HasSuffix(path, "/chat/completions") || strings.HasSuffix(path, "/completions")
}

// drainHTTPStream reads an errored upstream stream to completion so the body is
// available for the error message and the host-side reader is released.
func drainHTTPStream(host hostClient, streamID string) []byte {
	if strings.TrimSpace(streamID) == "" {
		return nil
	}
	defer closeHTTPStream(host, streamID)
	var body bytes.Buffer
	for body.Len() < 64*1024 {
		chunk, err := readHTTPStream(host, streamID)
		if err != nil || chunk.Error != "" {
			break
		}
		body.Write(chunk.Payload)
		if chunk.Done {
			break
		}
	}
	return body.Bytes()
}

func readHTTPStream(host hostClient, streamID string) (hostHTTPStreamReadResponse, error) {
	raw, err := host.invoke(methodHostHTTPStreamRead, hostHTTPStreamReadRequest{StreamID: streamID})
	if err != nil {
		return hostHTTPStreamReadResponse{}, err
	}
	var chunk hostHTTPStreamReadResponse
	if err := json.Unmarshal(raw, &chunk); err != nil {
		return hostHTTPStreamReadResponse{}, fmt.Errorf("decode host stream chunk: %w", err)
	}
	return chunk, nil
}

func closeHTTPStream(host hostClient, streamID string) {
	_, _ = host.invoke(methodHostHTTPStreamClose, hostHTTPStreamCloseRequest{StreamID: streamID})
}

// pumpUpstreamStream copies the upstream SSE body into the plugin stream one
// data line at a time. host.http.stream_read yields arbitrary byte
// boundaries, so lines are reassembled here before being emitted.
func pumpUpstreamStream(host hostClient, httpStreamID, pluginStreamID string, bare bool) {
	defer closeHTTPStream(host, httpStreamID)

	var buffer []byte
	emitFailure := ""
	for emitFailure == "" {
		chunk, err := readHTTPStream(host, httpStreamID)
		if err != nil {
			emitFailure = err.Error()
			break
		}
		if chunk.Error != "" {
			emitFailure = chunk.Error
			break
		}
		buffer = append(buffer, chunk.Payload...)
		for {
			index := bytes.IndexByte(buffer, '\n')
			if index < 0 {
				break
			}
			line := buffer[:index]
			buffer = buffer[index+1:]
			if streamErr := emitSSELine(host, pluginStreamID, line, bare); streamErr != "" {
				emitFailure = streamErr
				break
			}
		}
		if chunk.Done {
			if emitFailure == "" {
				emitFailure = emitSSELine(host, pluginStreamID, buffer, bare)
			}
			break
		}
	}
	closePluginStream(host, pluginStreamID, emitFailure)
}

// emitSSELine forwards a single SSE line, returning a non-empty string when the
// stream must be torn down. Terminal "[DONE]" markers are dropped: the host
// appends its own tail for translated streams
// (internal/pluginhost/adapters.go:1574) and the OpenAI handler writes one on
// channel close, so forwarding ours would duplicate it.
func emitSSELine(host hostClient, pluginStreamID string, line []byte, bare bool) string {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return ""
	}
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		switch {
		case bytes.HasPrefix(trimmed, []byte(":")),
			bytes.HasPrefix(trimmed, []byte("event:")),
			bytes.HasPrefix(trimmed, []byte("id:")),
			bytes.HasPrefix(trimmed, []byte("retry:")):
			return ""
		case bytes.HasPrefix(trimmed, []byte("{")), bytes.HasPrefix(trimmed, []byte("[")):
			// A bare JSON body in place of SSE means the upstream reported an
			// error after committing to a 2xx stream.
			return string(trimmed)
		default:
			return ""
		}
	}
	payload := bytes.TrimSpace(trimmed[len("data:"):])
	if bytes.Equal(payload, []byte("[DONE]")) {
		return ""
	}
	if len(payload) == 0 {
		return ""
	}
	if !bare {
		payload = append([]byte("data: "), payload...)
	}
	if err := emitPluginStreamChunk(host, pluginStreamID, payload); err != nil {
		return err.Error()
	}
	return ""
}

func emitPluginStreamChunk(host hostClient, streamID string, payload []byte) error {
	_, err := host.invoke(methodHostStreamEmit, hostStreamEmitRequest{StreamID: streamID, Payload: payload})
	return err
}

func closePluginStream(host hostClient, streamID, errMessage string) {
	_, _ = host.invoke(methodHostStreamClose, hostStreamCloseRequest{
		StreamID: streamID,
		Error:    strings.TrimSpace(errMessage),
	})
}
