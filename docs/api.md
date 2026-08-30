# HTTP API contract

Versioning stance: additive changes within `/v1`; breaking changes use `/v2`.

## CPAMGMT Resource Panel

The plugin registers one browser resource so CPAMGMT can add **CPA Quota** to its sidebar:

```http
GET /v0/resource/plugins/cpa-quota-api-extension/panel
```

The resource response is a static self-contained HTML document with no credentials or operational data. Resource routes are not Management API-authenticated; all configuration and data operations initiated by the page use authenticated same-origin Management API requests. The panel reads configuration with `GET /v0/management/plugins/cpa-quota-api-extension/config` and saves only changed allowlisted keys with `PATCH` to that host endpoint.

The panel exposes no arbitrary request method, URL, headers, or body. Its API Explorer is limited to the six read-only plugin routes documented below. Same-origin CPAMGMT with a remembered management key is required for interactive use.

## Authentication

All data endpoints are plugin-owned Management API routes and inherit CLIProxyAPI management authentication and remote-management policy.

## Quota list

```http
GET /v0/management/plugins/cpa-quota-api-extension/v1/quotas?provider=codex&limit=200&cursor=...
Authorization: Bearer <management-key>
```

Success: `200 OK`.

```json
{
  "generated_at": "2026-07-27T12:00:00Z",
  "cache_ttl": "30m0s",
  "cached": true,
  "refresh_mode": "request-triggered",
  "summary": {
    "total": 32,
    "supported": 28,
    "available": 21,
    "exhausted": 3,
    "disabled": 0,
    "errors": 4,
    "by_provider": {"codex": 12, "antigravity": 10},
    "by_status": {"available": 21, "error": 4}
  },
  "providers": {
    "claude": {
      "status": "available",
      "supported": true,
      "credential_state": "active",
      "error": null,
      "windows": [
        {"id": "five_hour", "used_percent": 30, "remaining_percent": 70, "reset_at": "2026-07-27T12:00:00Z"},
        {"id": "seven_day", "used_percent": 10, "remaining_percent": 90, "reset_at": "2026-08-01T00:00:00Z"},
        {"id": "extra",     "used_percent": 10, "remaining_percent": 90}
      ],
      "models": [
        {"model": "claude-weekly-scoped-claude-3-5-sonnet", "model_name": "Claude Sonnet", "remaining_percent": 25, "reset_at": "2026-07-30T00:00:00Z"}
      ],
      "binding_window": {"id": "five_hour"},
      "extra_used_credits": 500,
      "extra_monthly_limit": 5000,
      "accounts": [{"auth_index": "claude-1", "provider": "claude", "status": "available", "supported": true, "credential_state": "active", "windows": [...]}]
    },
    "copilot": {
      "status": "available",
      "supported": true,
      "credential_state": "active",
      "windows": [
        {"id": "premium_interactions", "remaining_percent": 50, "used_percent": 50, "reset_at": "2026-09-01T00:00:00Z"},
        {"id": "chat",                 "remaining_percent": 40, "used_percent": 60, "reset_at": "2026-09-01T00:00:00Z"},
        {"id": "completions",          "remaining_percent": 50, "used_percent": 50, "reset_at": "2026-09-01T00:00:00Z"}
      ],
      "accounts": [...]
    },
    "opencode-go": {
      "status": "available",
      "supported": true,
      "credential_state": "active",
      "windows": [
        {"id": "rolling",  "used_percent": 4,  "remaining_percent": 96, "used_dollars": 0.48, "limit_dollars": 12, "window_seconds": 18000,  "reset_at": "2026-08-13T16:27:38Z"},
        {"id": "weekly",   "used_percent": 30, "remaining_percent": 70, "used_dollars": 9,    "limit_dollars": 30, "window_seconds": 604800, "reset_at": "2026-08-17T00:00:00Z"},
        {"id": "monthly",  "used_percent": 25, "remaining_percent": 75, "used_dollars": 15,   "limit_dollars": 60,                          "reset_at": "2026-09-13T06:06:01Z"}
      ],
      "accounts": [...]
    }
  },
  "accounts": [],
  "page": {
    "count": 0,
    "total": 32,
    "next_cursor": "MjAw"
  }
}
```

### `providers` map

The `providers` object is the primary structured view for thin clients. Each key is a normalized provider name. The value contains:

| field | description |
|---|---|
| `status` | `available`, `exhausted`, `error`, `unknown`, or `unsupported` |
| `supported` | `false` for providers the plugin has no fetcher for |
| `credential_state` | `active`, `disabled`, or `unavailable` |
| `error` | present when `status == "error"` — carries `code`, `message`, and optionally `upstream_status` |
| `windows` | quota windows with `remaining_percent` and `used_percent` on a 0–100 scale; semantics are identical across all providers so numeric comparison works across `providers` keys |
| `models` | Claude scoped-weekly per-model limits (absent on other providers) |
| `binding_window` | Claude only: the window the API reports as currently binding |
| `extra_used_credits` / `extra_monthly_limit` | Claude only: extra-usage credit figures |
| `accounts` | the full per-account list; a single account per provider is assumed for display but the list is not collapsed |

The `providers` map is always complete — it is not affected by `?provider=` or `?status=` query filters. Those filters apply to the `accounts` list only.

A client computing a tightest-across-all-providers pill iterates `providers`, finds the minimum `remaining_percent` across all windows for each provider, then picks the provider with the lowest minimum. The provider key is the prefix. Percent semantics are identical across Claude, Copilot, and OpenCode Go; OpenCode Go's dollar amounts are normalised to a percent before being stored in `remaining_percent`.

A provider with `status == "error"` is still present in the map with its `error` field populated, so a partial failure is visible rather than silent.

`refresh=true` bypasses cache freshness but does not create parallel duplicate refreshes. Concurrent callers during a refresh share a single upstream fetch.

## Account inventory

```http
GET /v0/management/plugins/cpa-quota-api-extension/v1/accounts?provider=antigravity&limit=200
```

The response uses the redacted metadata returned by `host.auth.list`; raw credential JSON is never returned.

## Status

```http
GET /v0/management/plugins/cpa-quota-api-extension/v1/status
```

Returns cache state and effective extension configuration.

## Account-level pool health

```http
GET /v0/management/plugins/cpa-quota-api-extension/v1/health?provider=codex&state=rate_limited&limit=200&cursor=...
```

Success: `200 OK`. The response contains filtered `capacity`, `by_state`, `by_provider`, and paginated account health records.

Exclusive states, in precedence order:

1. `disabled`
2. `unauthorized`
3. `forbidden`
4. `rate_limited`
5. `unavailable`
6. `degraded`
7. `healthy`
8. `unknown`

Capacity invariants:

```text
total = routable + lost
degraded <= routable
```

`refresh=true` forces a current host inventory reconciliation. Health monitoring is read-only and never changes credential state.

## Incidents

```http
GET /v0/management/plugins/cpa-quota-api-extension/v1/incidents?provider=codex&status_code=429&from=2026-08-01T00:00:00Z&limit=100
```

Supported filters: `auth_index`, `provider`, `state`, `status_code`, `from`, `to`, `limit`, and opaque `cursor`. Time filters use RFC3339. Limits default to 200 and are capped at 500.

Incident rows contain sanitized account index, provider, failure class, HTTP status, opened time, and optional resolution time. Failure bodies, API keys, tokens, and raw headers are not stored.

## Capacity history

```http
GET /v0/management/plugins/cpa-quota-api-extension/v1/history?from=2026-08-01T00:00:00Z&limit=100
```

Returns SQLite-backed total/routable/lost/degraded capacity points plus `by_state` and `by_provider` aggregates. Supports `from`, `to`, `limit`, and opaque `cursor`.

## Error shape

HTTP status codes are honest. Example:

```json
{
  "error": {
    "code": "quota_refresh_failed",
    "message": "host callback failed"
  }
}
```

Possible route-level statuses:

- `400`: invalid limit, cursor, RFC3339 time range, or incident status-code filter
- `401`: missing/invalid management key, handled by CLIProxyAPI
- `404`: route not found
- `502`: host callback or quota pool refresh failure
- `503`: health storage or reconciliation is unavailable; legacy quota routes remain available

Provider failures are account-level objects so one bad credential does not fail the entire pool snapshot.
