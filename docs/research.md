# Research notes

Research date: 2026-07-27.

The authoritative upstream source is vendored as a Git submodule at `upstream/CLIProxyAPI`, pinned to commit `ca67caf0872eef65f8eaedf2e127cf214a49621d` (`v7.2.61`).

## Live api2 findings

- CLIProxyAPI `v7.2.61`, commit `ca67caf0`.
- Native plugin files are installed, but global `plugins.enabled` is absent, so no dynamic library is loaded.
- A separate Python `codex-quota-exporter.service` currently exports Codex and Antigravity quota.
- The native plugin ABI and callbacks required by this project already exist in `v7.2.61`.

## Native support

CLIProxyAPI `v7.2.61` contains:

- `sdk/pluginabi/types.go`: ABI/schema version 1 and methods `management.register`, `management.handle`, `host.http.do`, `host.auth.list`, `host.auth.get`, `host.auth.get_runtime`, and `host.auth.save`.
- `internal/pluginhost/host_callbacks.go`: dispatches plugin callbacks to host HTTP and auth capabilities.
- `internal/pluginhost/auth_callbacks.go`: enumerates runtime auth records and reads physical JSON by `auth_index`.
- `examples/plugin/host-callback-auth-files`: official working example for auth callbacks.
- `examples/plugin/management-api`: official working example for authenticated plugin routes.
- `internal/api/handlers/management/api_tools.go`: native generic `/v0/management/api-call` with `$TOKEN$` substitution, proving the core already supports management-driven provider quota calls even without a plugin.

Conclusion: a native pool quota exporter is feasible without modifying CLIProxyAPI core.

## Existing projects

### CLIProxyAPI Quota Inspector

https://github.com/AllenReder/CLIProxyAPI-Quota-Inspector

External Go CLI. It lists auth files through Management API and uses `/v0/management/api-call` per credential. Supports Codex, Gemini CLI, and Antigravity and can print JSON. This is the closest feature match, but it is not a native plugin and does not expose a long-lived HTTP endpoint itself.

Reusable ideas:

- provider endpoint/header definitions
- auth-index-based querying
- bounded dual concurrency
- partial per-account failures

License: MIT.

### Codex Quota Scheduler

https://github.com/JefferyZhang2019/cpa-plugin-codex-quota-scheduler

Native plugin focused on Codex quota-aware scheduling. It uses host auth/HTTP callbacks, caches quota, exposes authenticated management routes, and includes robust refresh deduplication and credential safety. It is provider-specific and not a general quota export API.

### Quota Router

https://github.com/Smarty-Pants-Inc/cpa-plugin-quota-router

Native plugin focused on Claude seven-day quota routing. It reads Claude credential JSON, queries `api.anthropic.com/api/oauth/usage`, caches samples, and exposes authenticated status. It is not a whole-pool exporter.

### CPA Token Usage

https://github.com/zhumengling/codex-token-usage

Native usage/monitoring plugin with Codex quota snapshots, 429 autoban, SQLite persistence, and dashboard. It is Codex-centric and combines request usage with quota rather than exposing a provider-neutral pool API.

### CPA-Manager-Plus

https://github.com/seakee/CPA-Manager-Plus

External management/observability application. Rich quota and account operations, but not a lightweight native exporter plugin.

### SwiftBar quota plugin

https://github.com/LoveEatCandy/CLIProxyAPI-quota-bar

External UI consumer for Codex and Antigravity quota. It consumes a CPA endpoint; it is not a CLIProxyAPI native plugin.

### Official plugin registry

https://github.com/router-for-me/CLIProxyAPI-Plugins-Store

As of registry commit `2a2c87a4f5a75150f49011aa7548458beb7d5344`, the store contains provider-specific quota/scheduling plugins, but no provider-neutral native plugin whose primary contract is a versioned JSON export of the entire pool.

## Provider API evidence

- Codex: `https://chatgpt.com/backend-api/wham/usage`, used by Quota Inspector and Codex quota plugins.
- Gemini CLI: `https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota`, used by Quota Inspector.
- Antigravity: `https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels`, with `quotaInfo.remainingFraction` and `resetTime`.
- Claude OAuth: `https://api.anthropic.com/api/oauth/usage`, used by Quota Router. Public issue reports show aggressive rate limiting, supporting a long cache TTL.

## Decision

Build a native Management API plugin rather than another sidecar:

1. Enumerate every credential with `host.auth.list`.
2. Read raw credential JSON only inside the plugin with `host.auth.get`.
3. Fetch known quota APIs through `host.http.do`.
4. Export sanitized, provider-neutral JSON under a versioned management route.
5. Refresh only on request, cache for 30 minutes, and deduplicate concurrent refreshes.
6. Keep unsupported providers visible.
