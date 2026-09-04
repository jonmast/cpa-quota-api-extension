package main

import (
	"encoding/json"
	"strings"
)

// extractSessionID resolves the session identity from the original client
// request, using the fallback chain decided in issue #17:
// metadata.user_id suffix after "session_" (Claude Code format
// user_{hash}_account__session_{uuid}) -> conversation_id -> X-Session-ID
// header -> the literal "unknown" bucket.
func extractSessionID(originalRequest []byte, headers map[string][]string) string {
	var req struct {
		Metadata struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
		ConversationID string `json:"conversation_id"`
	}
	_ = json.Unmarshal(originalRequest, &req)
	if uid := strings.TrimSpace(req.Metadata.UserID); uid != "" {
		if idx := strings.LastIndex(uid, "session_"); idx >= 0 {
			return uid[idx+len("session_"):]
		}
		return uid
	}
	if req.ConversationID != "" {
		return "conv:" + req.ConversationID
	}
	for key, values := range headers {
		if strings.EqualFold(key, "X-Session-ID") && len(values) > 0 {
			return "header:" + values[0]
		}
	}
	return "unknown"
}
