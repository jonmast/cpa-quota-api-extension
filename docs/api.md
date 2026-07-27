# HTTP API contract

Versioning stance: additive changes within `/v1`; breaking changes use `/v2`.

## Authentication

All endpoints are plugin-owned Management API routes and inherit CLIProxyAPI management authentication and remote-management policy.

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

- `400`: invalid query in a future strict-validation revision
- `401`: missing/invalid management key, handled by CLIProxyAPI
- `404`: route not found
- `502`: host callback or pool refresh failure

Provider failures are account-level objects so one bad credential does not fail the entire pool snapshot.
