package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type usageRecord struct {
	Provider        string
	AuthIndex       string
	RequestedAt     time.Time
	Latency         time.Duration
	Failed          bool
	Failure         usageFailure
	ResponseHeaders http.Header
}
type usageFailure struct {
	StatusCode int
	Body       string
}

type healthEvent struct {
	Provider     string    `json:"provider"`
	AuthIndex    string    `json:"auth_index"`
	At           time.Time `json:"at"`
	StatusCode   int       `json:"status_code,omitempty"`
	FailureClass string    `json:"failure_class,omitempty"`
	LatencyMS    int64     `json:"latency_ms,omitempty"`
	RetryAt      time.Time `json:"retry_at,omitempty"`
	Success      bool      `json:"success"`
}

type healthObservation struct {
	LastStatusCode int
	LastFailureAt  time.Time
	LastSuccessAt  time.Time
	RecentFailures int
	RetryAt        time.Time
}
type accountHealth struct {
	State    string `json:"state"`
	Routable bool   `json:"routable"`
}

type healthAccount struct {
	AuthIndex      string    `json:"auth_index"`
	Name           string    `json:"name"`
	Provider       string    `json:"provider"`
	State          string    `json:"state"`
	Routable       bool      `json:"routable"`
	HostStatus     string    `json:"host_status,omitempty"`
	Disabled       bool      `json:"disabled"`
	Unavailable    bool      `json:"unavailable"`
	LastStatusCode int       `json:"last_status_code,omitempty"`
	LastFailureAt  time.Time `json:"last_failure_at,omitempty"`
	LastSuccessAt  time.Time `json:"last_success_at,omitempty"`
	RecentFailures int       `json:"recent_failures"`
	NextRetryAfter time.Time `json:"next_retry_after,omitempty"`
}
type healthCapacity struct {
	Total    int `json:"total"`
	Routable int `json:"routable"`
	Lost     int `json:"lost"`
	Degraded int `json:"degraded"`
}
type healthSnapshot struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Capacity    healthCapacity  `json:"capacity"`
	ByProvider  map[string]int  `json:"by_provider"`
	ByState     map[string]int  `json:"by_state"`
	Accounts    []healthAccount `json:"accounts"`
}

func classifyAccountHealth(now time.Time, entry hostAuthFileEntry, observation healthObservation, threshold int) accountHealth {
	if entry.Disabled {
		return accountHealth{State: "disabled"}
	}
	// Auth and active retry failures are the latest runtime signal; a stale 429
	// after its retry deadline must not make an otherwise active account stuck.
	unresolved := !observation.LastFailureAt.IsZero() && (observation.LastSuccessAt.IsZero() || observation.LastSuccessAt.Before(observation.LastFailureAt))
	if unresolved && observation.LastStatusCode == http.StatusUnauthorized {
		return accountHealth{State: "unauthorized"}
	}
	if unresolved && observation.LastStatusCode == http.StatusForbidden {
		return accountHealth{State: "forbidden"}
	}
	hasRetryDeadline := !entry.NextRetryAfter.IsZero() || !observation.RetryAt.IsZero()
	if entry.NextRetryAfter.After(now) || observation.RetryAt.After(now) || (unresolved && observation.LastStatusCode == http.StatusTooManyRequests && !hasRetryDeadline) {
		return accountHealth{State: "rate_limited"}
	}
	// Host flags are authoritative. Only active entries can be healthy or routed.
	if entry.Unavailable || entry.Status == "error" {
		return accountHealth{State: "unavailable"}
	}
	if entry.Status != "active" {
		return accountHealth{State: "unknown"}
	}
	if threshold > 0 && observation.RecentFailures >= threshold {
		return accountHealth{State: "degraded", Routable: true}
	}
	return accountHealth{State: "healthy", Routable: true}
}

func sanitizeUsageRecord(record usageRecord, receivedAt time.Time) (healthEvent, bool) {
	if strings.TrimSpace(record.AuthIndex) == "" {
		return healthEvent{}, false
	}
	at := record.RequestedAt.UTC()
	if at.IsZero() {
		at = receivedAt.UTC()
	}
	event := healthEvent{Provider: strings.ToLower(strings.TrimSpace(record.Provider)), AuthIndex: strings.TrimSpace(record.AuthIndex), At: at, Success: !record.Failed, LatencyMS: record.Latency.Milliseconds()}
	if !record.Failed {
		return event, true
	}
	event.StatusCode = record.Failure.StatusCode
	switch event.StatusCode {
	case 401:
		event.FailureClass = "unauthorized"
	case 403:
		event.FailureClass = "forbidden"
	case 429:
		event.FailureClass = "rate_limited"
	default:
		switch {
		case event.StatusCode == 0:
			event.FailureClass = "transport_error"
		case event.StatusCode >= 500 && event.StatusCode <= 599:
			event.FailureClass = "upstream_5xx"
		case event.StatusCode >= 400 && event.StatusCode <= 499:
			event.FailureClass = "request_error"
		default:
			event.FailureClass = "execution_error"
		}
	}
	retryRaw := strings.TrimSpace(record.ResponseHeaders.Get("Retry-After"))
	if retryAfter, err := strconv.Atoi(retryRaw); err == nil && retryAfter > 0 {
		event.RetryAt = event.At.Add(record.Latency).Add(time.Duration(retryAfter) * time.Second)
	} else if retryAt, err := http.ParseTime(retryRaw); err == nil {
		event.RetryAt = retryAt.UTC()
	}
	return event, true
}

func containsSensitiveJSON(raw []byte) bool {
	lower := strings.ToLower(string(raw))
	return strings.Contains(lower, "apikey") || strings.Contains(lower, "api_key") || strings.Contains(lower, "token") || strings.Contains(lower, "body") || strings.Contains(lower, "headers")
}

func (r *runtimeState) handleUsage(raw []byte) []byte {
	var record usageRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		atomic.AddUint64(&r.droppedUsage, 1)
		return mustOKEmpty()
	}
	event, ok := sanitizeUsageRecord(record, time.Now())
	if !ok {
		atomic.AddUint64(&r.droppedUsage, 1)
		return mustOKEmpty()
	}
	r.enqueueUsage(event)
	return mustOKEmpty()
}

func mustOKEmpty() []byte { raw, _ := okEnvelope(struct{}{}); return raw }
