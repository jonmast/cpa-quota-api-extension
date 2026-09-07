// Command stub is a stand-in for the opencode-go upstream.
//
// It exists to make one question answerable without a live credential: what
// exactly does CLIProxyAPI put on the wire when a client calls it with an
// x-opencode-session header? Every inbound request is appended to a log file in
// a greppable one-line form, so the harness can assert on the outbound
// User-Agent (which executor ran) and the outbound session header (whether it
// survived the host->plugin boundary).
//
// The session-header check mirrors the real upstream: /chat/completions answers
// 400 MissingSessionID when x-opencode-session is absent. That is what makes
// the end-to-end status codes meaningful rather than merely internally
// consistent.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
)

const sessionHeader = "x-opencode-session"

// modelID is served by /models and is what the plugin discovers and registers.
const modelID = "mimo-v2.5"

type recorder struct {
	mu   sync.Mutex
	file *os.File
}

// record appends one line per inbound request. The format is deliberately flat
// text rather than JSON: the harness greps it, and a human reading a failure
// should not need a JSON parser to see what happened.
func (r *recorder) record(req *http.Request) {
	session := req.Header.Get(sessionHeader)
	if session == "" {
		session = "-"
	}
	agent := req.Header.Get("User-Agent")
	if agent == "" {
		agent = "-"
	}
	line := fmt.Sprintf("%s %s ua=%s session=%s\n", req.Method, req.URL.Path, agent, session)

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.file.WriteString(line); err != nil {
		log.Printf("stub: write record: %v", err)
	}
	if err := r.file.Sync(); err != nil {
		log.Printf("stub: sync record: %v", err)
	}
}

func main() {
	addr := flag.String("addr", "127.0.0.1:9998", "listen address")
	logPath := flag.String("log", "upstream-requests.log", "request log path")
	flag.Parse()

	file, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		log.Printf("stub: open log: %v", err)
		os.Exit(1)
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			log.Printf("stub: close log: %v", errClose)
		}
	}()
	rec := &recorder{file: file}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		writeJSON(w, http.StatusOK, map[string]any{
			"object": "list",
			"data":   []any{map[string]any{"id": modelID, "object": "model", "owned_by": "opencode-go"}},
		})
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		if r.Header.Get(sessionHeader) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": map[string]any{"message": "MissingSessionID", "type": "invalid_request_error"},
			})
			return
		}
		if requestedStream(r) {
			writeStream(w)
			return
		}
		writeJSON(w, http.StatusOK, completionBody())
	})

	log.Printf("stub: listening on %s, logging to %s", *addr, *logPath)
	server := &http.Server{Addr: *addr, Handler: mux}
	if errServe := server.ListenAndServe(); errServe != nil && errServe != http.ErrServerClosed {
		log.Printf("stub: serve: %v", errServe)
		os.Exit(1)
	}
}

func requestedStream(r *http.Request) bool {
	var body struct {
		Stream bool `json:"stream"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return strings.Contains(r.Header.Get("Accept"), "text/event-stream")
	}
	return body.Stream
}

func completionBody() map[string]any {
	return map[string]any{
		"id":      "chatcmpl-stub",
		"object":  "chat.completion",
		"model":   modelID,
		"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": "pong"}}},
		"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("stub: encode response: %v", err)
	}
}

// writeStream emits a minimal OpenAI-shaped SSE completion. The content arrives
// as a single delta because the harness asserts on headers and status, not on
// chunk boundaries.
func writeStream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	chunks := []map[string]any{
		{"id": "chatcmpl-stub", "object": "chat.completion.chunk", "model": modelID,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "pong"}}}},
		{"id": "chatcmpl-stub", "object": "chat.completion.chunk", "model": modelID,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}},
	}
	for _, chunk := range chunks {
		encoded, err := json.Marshal(chunk)
		if err != nil {
			log.Printf("stub: encode chunk: %v", err)
			return
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", encoded); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
	if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
		return
	}
	if flusher != nil {
		flusher.Flush()
	}
}
