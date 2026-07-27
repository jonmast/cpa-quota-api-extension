# CPA Quota Extension

Native CLIProxyAPI dynamic-library plugin that exports a machine-readable quota snapshot for the whole credential pool.

## Status

This is a standalone Git repository. The official [router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) repository is included at `upstream/CLIProxyAPI` as a Git submodule pinned to `v7.2.61`, matching the current `api2` runtime.

The extension targets the native ABI available in CLIProxyAPI `v7.2.61` and later.

Implemented provider adapters:

- Codex: `chatgpt.com/backend-api/wham/usage`
- Gemini CLI: `cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota`
- Antigravity: `cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels`
- Claude OAuth: `api.anthropic.com/api/oauth/usage`

Every physical runtime credential appears in the exported inventory. Providers without a verified quota API are returned as `supported: false`, `status: "unsupported"` rather than silently omitted.

## Why a native plugin

CLIProxyAPI already exposes the required host callbacks to native plugins:

- `host.auth.list`: enumerate runtime credentials and state
- `host.auth.get`: read the selected physical credential JSON
- `host.http.do`: make upstream requests through the host transport
- `management.register` / `management.handle`: expose management-key-protected routes

This avoids a separate sidecar process and avoids hard-coding the auth directory.

## API

All routes require the normal CLIProxyAPI management key.

### `GET /v0/management/plugins/cpa-quota-extension/v1/quotas`

Returns the pool quota snapshot. Refresh is request-triggered:

- fresh snapshot: return cache
- stale/missing snapshot: query the pool
- concurrent stale calls: share one refresh

Query parameters:

- `refresh=true`: bypass freshness once; concurrent refreshes still deduplicate
- `provider=codex`: filter provider
- `status=available`: filter status
- `limit=200`: page size, maximum 1000
- `cursor=<opaque>`: next page cursor

### `GET /v0/management/plugins/cpa-quota-extension/v1/accounts`

Returns redacted host credential inventory for coverage diagnostics. Supports `provider`, `limit`, and `cursor`.

### `GET /v0/management/plugins/cpa-quota-extension/v1/status`

Returns plugin configuration, cache timestamp, and refresh state.

## Configuration

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    cpa-quota-extension:
      enabled: true
      priority: 100
      cache-ttl: 30m
      request-timeout: 30s
      max-concurrency: 8
      include-disabled: false
```

## Clone

```bash
git clone --recurse-submodules <this-repository-url>
cd cpa-quota-extension
```

For an existing clone:

```bash
git submodule update --init --recursive
```

## Build

```bash
make verify-upstream
make test
make build
```

The Linux artifact is written to:

```text
dist/cpa-quota-extension.so
```

Copy it to the configured CLIProxyAPI plugin directory and restart CLIProxyAPI.

To research or test a newer CPA release without changing the production compatibility baseline, create a branch, update `upstream/CLIProxyAPI`, run `make verify-upstream` with the intended `CPA_COMPAT_TAG`, and commit the submodule pointer explicitly.

## Security model

- Export routes use CLIProxyAPI's management authentication.
- Responses never contain access, refresh, ID tokens, cookies, or raw upstream bodies.
- Resource-page routes are intentionally not registered because plugin resources are unauthenticated.
- Upstream errors are reduced to status/category and do not echo potentially sensitive bodies.
- The plugin is trusted in-process code; install only reviewed builds.

## Known limitations

- OAuth token refresh is not implemented in this initial scaffold. Expired credentials return an account-level error. A later version can refresh and persist credentials with `host.auth.save`, following provider-specific rules.
- Gemini/Antigravity quota APIs are internal Google endpoints and may change.
- Antigravity `remainingFraction` may not always match generation-time `429` behavior.
- Claude's OAuth usage endpoint is aggressively rate-limited; the 30-minute cache intentionally minimizes calls.
- API-key providers often expose billing rather than subscription quota; they remain `unsupported` until a reliable adapter is added.

## Research

See [`docs/research.md`](docs/research.md) and [`docs/api.md`](docs/api.md).
