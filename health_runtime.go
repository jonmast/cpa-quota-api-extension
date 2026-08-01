package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// configureHealth serializes stop, drain, close, open, and start. A queue is only
// visible while its owner loop and database are live.
func (r *runtimeState) configureHealth(cfg pluginConfig) {
	r.healthLifecycle.Lock()
	defer r.healthLifecycle.Unlock()
	r.mu.Lock()
	old, stop, done, queue := r.healthStore, r.healthStop, r.healthDone, r.healthQueue
	webhookStop, webhookDone, webhookCancel := r.webhookStop, r.webhookDone, r.webhookCancel
	r.healthStore, r.healthStop, r.healthDone, r.healthQueue = nil, nil, nil, nil
	r.webhookStop, r.webhookDone, r.webhookQueue, r.webhookCancel = nil, nil, nil, nil
	r.healthSnapshot = healthSnapshot{}
	r.healthError = ""
	closed := r.closed
	r.mu.Unlock()
	if stop != nil {
		close(stop)
		<-done
	}
	if webhookCancel != nil {
		webhookCancel()
	}
	if webhookStop != nil {
		close(webhookStop)
		<-webhookDone
	}
	if queue != nil {
		for {
			select {
			case <-queue:
				atomic.AddUint64(&r.droppedUsage, 1)
			default:
				goto drained
			}
		}
	drained:
	}
	r.healthOps.Lock()
	if old != nil {
		old.close()
	}
	r.healthOps.Unlock()
	if closed || cfg.DatabasePath == "" {
		return
	}
	store, err := openHealthStore(cfg.DatabasePath)
	if err != nil {
		r.setHealthError()
		return
	}
	queue = make(chan healthEvent, cfg.HealthQueue)
	stop, done = make(chan struct{}), make(chan struct{})
	webhookQueue := make(chan healthSnapshot, 1)
	webhookStop, webhookDone = make(chan struct{}), make(chan struct{})
	webhookCtx, webhookCancel := context.WithCancel(context.Background())
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		webhookCancel()
		store.close()
		return
	}
	r.healthStore, r.healthQueue, r.healthStop, r.healthDone = store, queue, stop, done
	r.webhookQueue, r.webhookStop, r.webhookDone, r.webhookCancel = webhookQueue, webhookStop, webhookDone, webhookCancel
	r.mu.Unlock()
	go r.healthLoop(store, queue, cfg, stop, done)
	go r.webhookLoop(store, cfg, webhookCtx, webhookQueue, webhookStop, webhookDone)
}

func (r *runtimeState) healthLoop(store *healthStore, queue <-chan healthEvent, cfg pluginConfig, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	refresh := time.NewTicker(cfg.HealthRefreshInterval)
	defer refresh.Stop()
	history := time.NewTicker(cfg.HistoryInterval)
	defer history.Stop()
	for {
		select {
		case <-stop:
			return
		case event := <-queue:
			if err := store.record(event, cfg); err != nil {
				r.setHealthError()
			} else {
				_, _ = r.refreshHealth(context.Background(), true)
			}
		case <-refresh.C:
			_, _ = r.refreshHealth(context.Background(), true)
		case <-history.C:
			r.mu.Lock()
			snapshot := r.healthSnapshot
			r.mu.Unlock()
			if !snapshot.GeneratedAt.IsZero() && store.saveHistory(snapshot, cfg) != nil {
				r.setHealthError()
			}
		}
	}
}
func (r *runtimeState) setHealthError() {
	r.mu.Lock()
	r.healthError = "health database unavailable"
	r.mu.Unlock()
}
func (r *runtimeState) enqueueUsage(event healthEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.healthStore == nil || r.healthQueue == nil || r.closed {
		atomic.AddUint64(&r.droppedUsage, 1)
		return
	}
	select {
	case r.healthQueue <- event:
	default:
		atomic.AddUint64(&r.droppedUsage, 1)
	}
}

func (r *runtimeState) refreshHealth(ctx context.Context, force bool) (healthSnapshot, error) {
	r.healthOps.Lock()
	defer r.healthOps.Unlock()
	r.mu.Lock()
	store, cfg, current := r.healthStore, r.cfg, r.healthSnapshot
	if store == nil {
		r.mu.Unlock()
		return healthSnapshot{}, errHealthDisabled{}
	}
	if !force && !current.GeneratedAt.IsZero() && time.Since(current.GeneratedAt) < cfg.HealthRefreshInterval {
		r.mu.Unlock()
		return current, nil
	}
	if r.healthRefreshing {
		for r.healthRefreshing {
			r.healthCond.Wait()
		}
		current = r.healthSnapshot
		r.mu.Unlock()
		if !current.GeneratedAt.IsZero() {
			return current, nil
		}
		return healthSnapshot{}, errHealthDisabled{}
	}
	r.healthRefreshing = true
	r.mu.Unlock()
	entries, err := r.host.listAuth(ctx)
	if err == nil {
		now := time.Now().UTC()
		snapshot := healthSnapshot{GeneratedAt: now, ByProvider: map[string]int{}, ByState: map[string]int{}}
		for _, entry := range entries {
			if entry.AuthIndex == "" {
				continue
			}
			obs, observeErr := store.observation(entry.AuthIndex, now.Add(-cfg.FailureWindow))
			if observeErr != nil {
				err = observeErr
				break
			}
			health := classifyAccountHealth(now, entry, obs, cfg.DegradedThreshold)
			provider := normalizedProvider(entry)
			snapshot.Accounts = append(snapshot.Accounts, healthAccount{AuthIndex: entry.AuthIndex, Name: entry.Name, Provider: provider, State: health.State, Routable: health.Routable, HostStatus: entry.Status, Disabled: entry.Disabled, Unavailable: entry.Unavailable, LastStatusCode: obs.LastStatusCode, LastFailureAt: obs.LastFailureAt, LastSuccessAt: obs.LastSuccessAt, RecentFailures: obs.RecentFailures, NextRetryAfter: maxTime(obs.RetryAt, entry.NextRetryAfter)})
			snapshot.Capacity.Total++
			snapshot.ByProvider[provider]++
			snapshot.ByState[health.State]++
			if health.Routable {
				snapshot.Capacity.Routable++
			} else {
				snapshot.Capacity.Lost++
			}
			if health.State == "degraded" {
				snapshot.Capacity.Degraded++
			}
		}
		if err == nil {
			r.mu.Lock()
			r.healthSnapshot = snapshot
			r.mu.Unlock()
			r.maybeAlertForStore(snapshot, cfg, store)
			current = snapshot
		}
	}
	r.mu.Lock()
	r.healthRefreshing = false
	r.healthCond.Broadcast()
	r.mu.Unlock()
	if err != nil {
		r.setHealthError()
		return healthSnapshot{}, err
	}
	return current, nil
}
func maxTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}

type errHealthDisabled struct{}

func (errHealthDisabled) Error() string { return "health database unavailable" }

func (r *runtimeState) maybeAlert(snapshot healthSnapshot, cfg pluginConfig) {
	r.mu.Lock()
	store := r.healthStore
	r.mu.Unlock()
	if store != nil {
		r.maybeAlertForStore(snapshot, cfg, store)
	}
}
func (r *runtimeState) maybeAlertForStore(snapshot healthSnapshot, cfg pluginConfig, store *healthStore) {
	if cfg.WebhookURL == "" {
		return
	}
	breach := (cfg.LostThreshold > 0 && snapshot.Capacity.Lost >= cfg.LostThreshold) || (cfg.DegradedPoolThreshold > 0 && snapshot.Capacity.Degraded >= cfg.DegradedPoolThreshold)
	state := "healthy"
	if breach {
		state = "breach"
	}
	// Persist active state before asynchronous delivery so failed attempts retry
	// and the worker never delays usage persistence or reconciliation.
	r.webhookOps.Lock()
	err := store.updateAlertState(state)
	r.webhookOps.Unlock()
	if err != nil {
		r.setHealthError()
		return
	}
	r.queueWebhookSnapshot(snapshot)
}

func (r *runtimeState) queueWebhookSnapshot(snapshot healthSnapshot) {
	r.mu.Lock()
	queue := r.webhookQueue
	r.mu.Unlock()
	if queue == nil {
		return
	}
	// Keep the newest pool state when refreshes arrive faster than delivery.
	select {
	case queue <- snapshot:
		return
	default:
	}
	select {
	case <-queue:
	default:
	}
	select {
	case queue <- snapshot:
	default:
	}
}

func (r *runtimeState) webhookLoop(store *healthStore, cfg pluginConfig, ctx context.Context, queue <-chan healthSnapshot, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	reminderInterval := cfg.AlertCooldown
	if reminderInterval <= 0 {
		reminderInterval = time.Millisecond
	}
	reminders := time.NewTicker(reminderInterval)
	defer reminders.Stop()
	var latest healthSnapshot
	for {
		select {
		case <-stop:
			return
		case snapshot := <-queue:
			latest = snapshot
			r.deliverPendingAlert(store, cfg, ctx, snapshot)
		case <-reminders.C:
			if !latest.GeneratedAt.IsZero() {
				r.deliverPendingAlert(store, cfg, ctx, latest)
			}
		}
	}
}

func (r *runtimeState) deliverPendingAlert(store *healthStore, cfg pluginConfig, ctx context.Context, snapshot healthSnapshot) {
	r.webhookOps.Lock()
	event, deliver, err := store.nextAlert(cfg.AlertCooldown, time.Now().UTC())
	r.webhookOps.Unlock()
	if err != nil {
		r.setHealthError()
		return
	}
	if !deliver {
		return
	}
	if r.postWebhook(ctx, cfg, map[string]any{"event": event, "at": snapshot.GeneratedAt, "capacity": snapshot.Capacity, "by_state": snapshot.ByState}) {
		r.webhookOps.Lock()
		err := store.recordAlertDelivery(event, time.Now().UTC())
		r.webhookOps.Unlock()
		if err != nil {
			r.setHealthError()
			return
		}
		r.clearWebhookError()
	}
}
func (r *runtimeState) postWebhook(ctx context.Context, cfg pluginConfig, payload any) bool {
	raw, _ := json.Marshal(payload)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.WebhookURL, bytes.NewReader(raw))
	if err != nil {
		r.setWebhookError()
		return false
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: cfg.WebhookTimeout}).Do(request)
	if response != nil {
		response.Body.Close()
	}
	if err != nil || (response != nil && response.StatusCode >= 300) {
		r.setWebhookError()
		return false
	}
	return true
}
func (r *runtimeState) setWebhookError() {
	r.mu.Lock()
	r.webhookError = "webhook delivery failed"
	r.mu.Unlock()
}
func (r *runtimeState) clearWebhookError() {
	r.mu.Lock()
	r.webhookError = ""
	r.mu.Unlock()
}

func (r *runtimeState) healthResponse(query url.Values) managementResponse {
	snapshot, err := r.refreshHealth(context.Background(), parseBool(query.Get("refresh")))
	if err != nil {
		return jsonError(http.StatusServiceUnavailable, "health_unavailable", "health monitoring is unavailable")
	}
	provider, state := strings.ToLower(query.Get("provider")), strings.ToLower(query.Get("state"))
	accounts := make([]healthAccount, 0, len(snapshot.Accounts))
	capacity := healthCapacity{}
	byProvider := map[string]int{}
	byState := map[string]int{}
	for _, account := range snapshot.Accounts {
		if (provider == "" || account.Provider == provider) && (state == "" || account.State == state) {
			accounts = append(accounts, account)
			capacity.Total++
			byProvider[account.Provider]++
			byState[account.State]++
			if account.Routable {
				capacity.Routable++
			} else {
				capacity.Lost++
			}
			if account.State == "degraded" {
				capacity.Degraded++
			}
		}
	}
	return healthPage(snapshot, accounts, capacity, byProvider, byState, query)
}
func healthPage(snapshot healthSnapshot, accounts []healthAccount, capacity healthCapacity, byProvider, byState map[string]int, query url.Values) managementResponse {
	start, err := strictCursor(query.Get("cursor"))
	if err != nil {
		return jsonError(http.StatusBadRequest, "invalid_cursor", "cursor is invalid")
	}
	limit, err := strictLimit(query.Get("limit"))
	if err != nil {
		return jsonError(http.StatusBadRequest, "invalid_limit", "limit must be 1 through 500")
	}
	if start > len(accounts) {
		start = len(accounts)
	}
	end := start + limit
	if end > len(accounts) {
		end = len(accounts)
	}
	next := ""
	if end < len(accounts) {
		next = encodeCursor(end)
	}
	return jsonResponse(http.StatusOK, map[string]any{"generated_at": snapshot.GeneratedAt, "capacity": capacity, "by_provider": byProvider, "by_state": byState, "accounts": accounts[start:end], "page": map[string]any{"count": end - start, "total": len(accounts), "next_cursor": next}})
}
func strictLimit(raw string) (int, error) {
	if raw == "" {
		return 200, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 500 {
		return 0, errHealthDisabled{}
	}
	return n, nil
}
func strictCursor(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	n := decodeCursor(raw)
	if n == 0 && raw != encodeCursor(0) {
		return 0, errHealthDisabled{}
	}
	return n, nil
}
func (r *runtimeState) listIncidents(query url.Values) managementResponse {
	return r.listStored(query, true)
}
func (r *runtimeState) listHistory(query url.Values) managementResponse {
	return r.listStored(query, false)
}
func parseTimeFilter(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, raw)
}
func (r *runtimeState) listStored(query url.Values, incidents bool) managementResponse {
	limit, err := strictLimit(query.Get("limit"))
	if err != nil {
		return jsonError(400, "invalid_limit", "limit must be 1 through 500")
	}
	before, err := opaqueIDCursor(query.Get("cursor"))
	if err != nil {
		return jsonError(400, "invalid_cursor", "cursor is invalid")
	}
	from, err := parseTimeFilter(query.Get("from"))
	if err != nil {
		return jsonError(400, "invalid_filter", "from must be RFC3339")
	}
	to, err := parseTimeFilter(query.Get("to"))
	if err != nil || (!from.IsZero() && !to.IsZero() && to.Before(from)) {
		return jsonError(400, "invalid_filter", "to must be RFC3339 and not before from")
	}
	r.healthOps.Lock()
	defer r.healthOps.Unlock()
	r.mu.Lock()
	store := r.healthStore
	r.mu.Unlock()
	if store == nil {
		return jsonError(503, "health_unavailable", "health monitoring is unavailable")
	}
	if incidents {
		status := 0
		if raw := query.Get("status_code"); raw != "" {
			status, err = strconv.Atoi(raw)
			if err != nil || status < 100 || status > 599 {
				return jsonError(400, "invalid_filter", "status_code must be an HTTP status")
			}
		}
		rows, err := store.incidentsFiltered(incidentFilter{Provider: strings.ToLower(query.Get("provider")), AuthIndex: query.Get("auth_index"), State: strings.ToLower(query.Get("state")), StatusCode: status, From: from, To: to}, limit, before)
		if err != nil {
			return jsonError(503, "health_unavailable", "health monitoring is unavailable")
		}
		return rowPage(rows, limit)
	}
	rows, err := store.historyFiltered(historyFilter{From: from, To: to}, limit, before)
	if err != nil {
		return jsonError(503, "health_unavailable", "health monitoring is unavailable")
	}
	return rowPage(rows, limit)
}
func opaqueIDCursor(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(string(decoded), 10, 64)
	if err != nil || n < 1 {
		return 0, errHealthDisabled{}
	}
	return n, nil
}
func rowPage[T interface{ id() int64 }](rows []T, limit int) managementResponse {
	next := ""
	if len(rows) > limit {
		next = opaqueCursor(rows[limit-1].id())
		rows = rows[:limit]
	}
	return jsonResponse(200, map[string]any{"items": rows, "page": map[string]any{"count": len(rows), "next_cursor": next}})
}
func (row healthIncident) id() int64 { return row.ID }
func (row healthHistory) id() int64  { return row.ID }
