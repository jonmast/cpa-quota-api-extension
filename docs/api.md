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
  "accounts": [],
  "page": {
    "count": 0,
    "total": 32,
    "next_cursor": "MjAw"
  }
}
```

`refresh=true` bypasses cache freshness but does not create parallel duplicate refreshes.

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
