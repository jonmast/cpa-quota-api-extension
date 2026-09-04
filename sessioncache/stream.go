package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

// streamState accumulates one in-flight streamed request. Session identity is
// captured on the header-init chunk (ChunkIndex == -1), the only chunk
// guaranteed to carry the original request under schema >= 3; token usage
// arrives across message_start (input/cache tokens, model) and message_delta
// (final output tokens); the row commits on message_stop. lastChunk drives TTL
// eviction so abandoned streams (client disconnects that never deliver
// message_stop) cannot leak memory.
type streamState struct {
	sessionID string
	model     string
	usage     anthropicUsage
	sawStart  bool
	lastChunk time.Time
}

type sseEvent struct {
	Type    string `json:"type"`
	Message struct {
		Model string         `json:"model"`
		Usage anthropicUsage `json:"usage"`
	} `json:"message"`
	Usage anthropicUsage `json:"usage"`
}

// parseSSEData extracts Anthropic events from an SSE chunk. Lines that are not
// data payloads, are [DONE] markers, or do not decode to a typed event are
// skipped, so non-Anthropic streams simply yield no events.
func parseSSEData(chunk []byte) []sseEvent {
	var events []sseEvent
	scanner := bufio.NewScanner(bytes.NewReader(chunk))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var ev sseEvent
		if err := json.Unmarshal([]byte(payload), &ev); err == nil && ev.Type != "" {
			events = append(events, ev)
		}
	}
	return events
}
