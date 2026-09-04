package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type runtimeState struct {
	mu               sync.Mutex
	cond             *sync.Cond
	host             hostClient
	cfg              pluginConfig
	snapshot         quotaResponse
	hasSnapshot      bool
	refreshing       bool
	closed           bool
	healthStore      *healthStore
	healthStop       chan struct{}
	healthDone       chan struct{}
	healthQueue      chan healthEvent
	webhookStop      chan struct{}
	webhookDone      chan struct{}
	webhookQueue     chan healthSnapshot
	webhookCancel    context.CancelFunc
	healthSnapshot   healthSnapshot
	healthRefreshing bool
	healthCond       *sync.Cond
	healthLifecycle  sync.Mutex
	healthOps        sync.Mutex
	webhookOps       sync.Mutex
	healthError      string
	webhookError     string
	droppedUsage     uint64
}

var activeRuntime = newRuntime(cgoHostClient{})

func newRuntime(host hostClient) *runtimeState {
	r := &runtimeState{host: host, cfg: defaultConfig()}
	r.cond = sync.NewCond(&r.mu)
	r.healthCond = sync.NewCond(&r.mu)
	return r
}

func (r *runtimeState) applyConfig(cfg pluginConfig) {
	r.mu.Lock()
	r.cfg = cfg
	r.hasSnapshot = false
	if r.closed {
		r.closed = false
	}
	r.mu.Unlock()
	r.configureHealth(cfg)
}

func (r *runtimeState) shutdown() {
	r.healthLifecycle.Lock()
	defer r.healthLifecycle.Unlock()
	r.mu.Lock()
	r.closed = true
	store, stop, done, queue := r.healthStore, r.healthStop, r.healthDone, r.healthQueue
	webhookStop, webhookDone, webhookQueue, webhookCancel := r.webhookStop, r.webhookDone, r.webhookQueue, r.webhookCancel
	r.healthStore, r.healthStop, r.healthDone, r.healthQueue = nil, nil, nil, nil
	r.webhookStop, r.webhookDone, r.webhookQueue, r.webhookCancel = nil, nil, nil, nil
	r.cond.Broadcast()
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
	if webhookQueue != nil {
		for {
			select {
			case <-webhookQueue:
			default:
				goto webhookDrained
			}
		}
	webhookDrained:
	}
	r.healthOps.Lock()
	if store != nil {
		store.close()
	}
	r.healthOps.Unlock()
}

func (r *runtimeState) handleManagement(req managementRequest) managementResponse {
	if req.Method == http.MethodGet && req.Path == "/v0/resource/plugins/"+pluginID+panelResourcePath {
		return panelResponse()
	}
	path := strings.TrimPrefix(req.Path, "/v0/management")
	switch {
	case req.Method == http.MethodGet && path == quotaRoute:
		force := parseBool(req.Query.Get("refresh"))
		snapshot, cached, err := r.getSnapshot(context.Background(), force)
		if err != nil {
			return jsonError(http.StatusBadGateway, "quota_refresh_failed", err.Error())
		}
		snapshot.Cached = cached
		return paginatedQuotaResponse(snapshot, req.Query)
	case req.Method == http.MethodGet && path == accountRoute:
		entries, err := r.host.listAuth(context.Background())
		if err != nil {
			return jsonError(http.StatusBadGateway, "auth_list_failed", err.Error())
		}
		return accountListResponse(entries, req.Query)
	case req.Method == http.MethodGet && path == statusRoute:
		return r.status()
	case req.Method == http.MethodGet && path == healthRoute:
		return r.healthResponse(req.Query)
	case req.Method == http.MethodGet && path == incidentsRoute:
		return r.listIncidents(req.Query)
	case req.Method == http.MethodGet && path == historyRoute:
		return r.listHistory(req.Query)
	case req.Method == http.MethodGet && path == profileRoute:
		return r.profileEndpoint()
	default:
		return jsonError(http.StatusNotFound, "not_found", "plugin route not found")
	}
}

func (r *runtimeState) getSnapshot(ctx context.Context, force bool) (quotaResponse, bool, error) {
	r.mu.Lock()
	for {
		if r.closed {
			r.mu.Unlock()
			return quotaResponse{}, false, context.Canceled
		}
		fresh := r.hasSnapshot && time.Since(r.snapshot.GeneratedAt) < r.cfg.CacheTTL
		if !force && fresh {
			snapshot := cloneSnapshot(r.snapshot)
			r.mu.Unlock()
			return snapshot, true, nil
		}
		if !r.refreshing {
			r.refreshing = true
			cfg := r.cfg
			r.mu.Unlock()
			snapshot, err := r.refresh(ctx, cfg)
			r.mu.Lock()
			if err == nil {
				r.snapshot = snapshot
				r.hasSnapshot = true
			}
			r.refreshing = false
			r.cond.Broadcast()
			r.mu.Unlock()
			return snapshot, false, err
		}
		r.cond.Wait()
		force = false
	}
}

func (r *runtimeState) refresh(ctx context.Context, cfg pluginConfig) (quotaResponse, error) {
	entries, err := r.host.listAuth(ctx)
	if err != nil {
		return quotaResponse{}, err
	}
	selected := make([]hostAuthFileEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.RuntimeOnly || entry.AuthIndex == "" {
			continue
		}
		if entry.Disabled && !cfg.IncludeDisabled {
			continue
		}
		selected = append(selected, entry)
	}
	sort.Slice(selected, func(i, j int) bool {
		left := normalizedProvider(selected[i]) + "\x00" + strings.ToLower(selected[i].Name)
		right := normalizedProvider(selected[j]) + "\x00" + strings.ToLower(selected[j].Name)
		return left < right
	})

	accounts := make([]accountQuota, len(selected))
	sem := make(chan struct{}, cfg.MaxConcurrency)
	var wg sync.WaitGroup
	for index := range selected {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			accounts[index] = fetchAccountQuota(ctx, r.host, cfg, selected[index])
		}()
	}
	wg.Wait()

	// Enrich fixed-cycle windows with usage-profile projections before the
	// provider rollup so both views carry them (ADR 0004).
	r.mu.Lock()
	store := r.healthStore
	r.mu.Unlock()
	attachProjections(accounts, store, cfg, time.Now())

	generatedAt := time.Now().UTC()
	response := quotaResponse{
		GeneratedAt: generatedAt,
		CacheTTL:    cfg.CacheTTL.String(),
		RefreshMode: "request-triggered",
		Accounts:    accounts,
		Providers:   buildProviders(accounts),
	}
	response.Summary = summarizeAccounts(accounts)
	r.host.log("info", "quota pool snapshot refreshed", map[string]any{
		"accounts": len(accounts), "providers": len(response.Summary.ByProvider),
	})
	return response, nil
}

func (r *runtimeState) status() managementResponse {
	r.mu.Lock()
	defer r.mu.Unlock()
	payload := statusResponse{
		PluginID: pluginID, Version: pluginVersion,
		CacheTTL: r.cfg.CacheTTL.String(), RequestTimeout: r.cfg.RequestTimeout.String(),
		MaxConcurrency: r.cfg.MaxConcurrency, IncludeDisabled: r.cfg.IncludeDisabled,
		HasSnapshot: r.hasSnapshot, Refreshing: r.refreshing, HealthEnabled: r.healthStore != nil,
		HealthSnapshotAt: r.healthSnapshot.GeneratedAt, DroppedUsageCount: atomic.LoadUint64(&r.droppedUsage),
		DatabaseError: r.healthError, WebhookConfigured: r.cfg.WebhookURL != "", WebhookError: r.webhookError,
	}
	if r.hasSnapshot {
		payload.GeneratedAt = r.snapshot.GeneratedAt
	}
	return jsonResponse(http.StatusOK, payload)
}

func summarizeAccounts(accounts []accountQuota) poolSummary {
	summary := poolSummary{Total: len(accounts), ByProvider: map[string]int{}, ByStatus: map[string]int{}}
	for _, account := range accounts {
		summary.ByProvider[account.Provider]++
		summary.ByStatus[account.Status]++
		if account.Supported {
			summary.Supported++
		}
		switch account.Status {
		case "available":
			summary.Available++
		case "exhausted":
			summary.Exhausted++
		case "error":
			summary.Errors++
		}
		if account.CredentialState == "disabled" {
			summary.Disabled++
		}
	}
	return summary
}

// buildProviders groups accounts by provider key. Top-level fields come from
// the first account in each group; accounts arrive already sorted by
// provider+name from refresh so the first entry is stable. The full per-account
// list is preserved under Accounts.
func buildProviders(accounts []accountQuota) map[string]providerQuota {
	if len(accounts) == 0 {
		return nil
	}
	m := make(map[string]providerQuota, 4)
	for _, account := range accounts {
		key := account.Provider
		p := m[key]
		p.Accounts = append(p.Accounts, account)
		if len(p.Accounts) == 1 {
			// First account in this provider group sets the provider-level fields.
			p.Status = account.Status
			p.Supported = account.Supported
			p.CredentialState = account.CredentialState
			p.Error = account.Error
			p.Windows = account.Windows
			p.Models = account.Models
			p.BindingWindow = account.BindingWindow
			p.ExtraUsedCredits = account.ExtraUsedCredits
			p.ExtraMonthlyLimit = account.ExtraMonthlyLimit
		}
		m[key] = p
	}
	return m
}

func cloneSnapshot(snapshot quotaResponse) quotaResponse {
	raw, _ := json.Marshal(snapshot)
	var clone quotaResponse
	_ = json.Unmarshal(raw, &clone)
	return clone
}

func paginatedQuotaResponse(snapshot quotaResponse, query map[string][]string) managementResponse {
	accounts := filterAccounts(snapshot.Accounts, firstQuery(query, "provider"), firstQuery(query, "status"))
	start := decodeCursor(firstQuery(query, "cursor"))
	if start > len(accounts) {
		start = len(accounts)
	}
	limit := parseLimit(firstQuery(query, "limit"), 200, 1000)
	end := start + limit
	if end > len(accounts) {
		end = len(accounts)
	}
	nextCursor := ""
	if end < len(accounts) {
		nextCursor = encodeCursor(end)
	}
	payload := struct {
		quotaResponse
		Page struct {
			Count      int    `json:"count"`
			Total      int    `json:"total"`
			NextCursor string `json:"next_cursor,omitempty"`
		} `json:"page"`
	}{quotaResponse: snapshot}
	payload.Accounts = accounts[start:end]
	payload.Page.Count = len(payload.Accounts)
	payload.Page.Total = len(accounts)
	payload.Page.NextCursor = nextCursor
	return jsonResponse(http.StatusOK, payload)
}

func accountListResponse(entries []hostAuthFileEntry, query map[string][]string) managementResponse {
	provider := strings.ToLower(firstQuery(query, "provider"))
	filtered := make([]hostAuthFileEntry, 0, len(entries))
	for _, entry := range entries {
		if provider != "" && normalizedProvider(entry) != provider {
			continue
		}
		filtered = append(filtered, entry)
	}
	start := decodeCursor(firstQuery(query, "cursor"))
	if start > len(filtered) {
		start = len(filtered)
	}
	limit := parseLimit(firstQuery(query, "limit"), 200, 1000)
	end := start + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	next := ""
	if end < len(filtered) {
		next = encodeCursor(end)
	}
	return jsonResponse(http.StatusOK, map[string]any{"accounts": filtered[start:end], "page": map[string]any{"count": end - start, "total": len(filtered), "next_cursor": next}})
}

func filterAccounts(accounts []accountQuota, provider, status string) []accountQuota {
	provider = strings.ToLower(strings.TrimSpace(provider))
	status = strings.ToLower(strings.TrimSpace(status))
	out := make([]accountQuota, 0, len(accounts))
	for _, account := range accounts {
		if provider != "" && account.Provider != provider {
			continue
		}
		if status != "" && account.Status != status {
			continue
		}
		out = append(out, account)
	}
	return out
}
func firstQuery(query map[string][]string, key string) string {
	values := query[key]
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}
func parseBool(raw string) bool { value, _ := strconv.ParseBool(strings.TrimSpace(raw)); return value }
func parseLimit(raw string, fallback, max int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 1 {
		return fallback
	}
	if value > max {
		return max
	}
	return value
}
func encodeCursor(index int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(index)))
}
func decodeCursor(raw string) int {
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return 0
	}
	index, err := strconv.Atoi(string(decoded))
	if err != nil || index < 0 {
		return 0
	}
	return index
}

func jsonResponse(status int, payload any) managementResponse {
	body, _ := json.Marshal(payload)
	return managementResponse{StatusCode: status, Headers: http.Header{"Content-Type": {"application/json; charset=utf-8"}, "Cache-Control": {"private, no-store"}}, Body: body}
}
func jsonError(status int, code, message string) managementResponse {
	return jsonResponse(status, map[string]any{"error": map[string]any{"code": code, "message": message}})
}
