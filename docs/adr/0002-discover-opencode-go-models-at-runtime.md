# ADR-0002: Discover OpenCode Go's model list at runtime

- **Status:** Accepted
- **Date:** 2026-08-30
- **Amends:** [ADR-0001](0001-opencode-go-auth-parser-plugin.md) — supersedes its
  "model list moves into plugin code" consequence, and corrects two of its
  premises about the host.
- **Context issue:** [#4](https://github.com/jonmast/cpa-quota-api-extension/issues/4)

## Context

ADR-0001 concluded that OpenCode Go's model list should move out of `config.yaml`
and into plugin code. That conclusion was reached by reading CPA source without
checking whether the provider exposes a model list of its own. It does:

```
GET https://opencode.ai/zen/go/v1/models     (bearer = the OpenCode Go API key)
```

returns a standard OpenAI-shaped list of 33 models. A recorded response is
committed at `authplugin/testdata/models.json`.

At the point this was noticed there were three drifting snapshots of one list:

| Source | Count | State |
|---|---|---|
| Provider endpoint | 33 | authoritative |
| CPA `openai-compatibility` config block | 31 | stale — missing `hy4-preview`, `qwen3.8-flash` |
| `authplugin` `defaultModels()` | 6 | entirely wrong — **none** of the six still exist |

Hardcoding does not remove the drift, it relocates it. The six-model list had
also survived eleven passing tests, because every test asserted that the plugin
reported what it hardcoded, never that the hardcoded values were correct — the
same failure class recorded in `docs/tracer-bullet-findings.md`.

Two premises inherited from ADR-0001 and its follow-up notes turned out to be
wrong, and both had blocked this option:

1. **"The auth plugin has no host HTTP capability."** False. `host.http.do` is a
   host callback (`sdk/pluginabi/types.go:65-79`), dispatched by
   `Host.callFromPlugin` (`internal/pluginhost/host_callbacks.go:108`) with no
   capability gate, and reachable from a `dlopen`'d plugin through the
   `cliproxy_host_api.call` pointer supplied at init. It routes through
   `NewProxyAwareHTTPClient`, so it honours CPA's proxy configuration — which a
   direct `net/http` call from inside the `.so` would bypass.

2. **"A compat auth with no registered models is unregistered, so
   `model.register` must be non-empty."** True in general but not on the path
   that matters. `registerModelsForAuthWithCache` calls
   `tryRegisterPluginModelsForAuth` (`sdk/cliproxy/service.go:1942`) and
   **returns early** on success, before the compat-auth `UnregisterClient`
   branch at `:2125`. Models supplied by `model.for_auth` register the auth on
   their own.

`model.for_auth` is also the only ABI entry point that receives a credential:
its request carries the auth's `Attributes` (where `parseAuth` stamped `api_key`
and `base_url`) plus a `host_callback_id`. `model.register` and `model.static`
have no credential and so cannot discover.

## Decision

**Discover the model list from the provider at runtime. Ship no hardcoded list.**

- `model.for_auth` performs `GET {base_url}/models` using the credential from
  its own request, via the `host.http.do` callback.
- The most recent successful result is cached per credential. On a failed fetch
  the last known list is served and the degradation is logged.
- With nothing cached, discovery **returns an error rather than an empty list**.
  An error makes the host return early without unregistering
  (`sdk/cliproxy/service.go:1186`); an empty list would drop the auth.
- `model.static` reports the last discovered list, or nothing at all before the
  first successful discovery. Empty is correct here, not a bug.
- The `models` config field is retained as an explicit operator override. When
  set, it is used verbatim and no discovery happens.
- `defaultBaseURL` is corrected to `https://opencode.ai/zen/go/v1`. The previous
  value, `https://opencode.ai/zen/v1`, is a different product's endpoint.

The cache is **not** a fallback list in ADR-0001's sense. It never contains
anything the provider did not serve; it only prevents a transient blip from
collapsing the list to empty.

## Consequences

- The provider is the single source of truth. The `config.yaml`
  `openai-compatibility` block and any baked list become redundant by
  construction rather than by discipline.
- New models appear without a plugin release.
- **The plugin now depends on provider reachability at registration time.** A
  cold start during a provider outage leaves OpenCode Go unregistered until the
  next refresh event (config reload, auth change, or the 15-minute auto-refresh).
  Accepted: the alternative is serving a list that is confidently wrong.
- Discovery is bounded by a 10s timeout. The host applies no deadline to plugin
  calls and `host.http.do` builds its client with no client timeout
  (`NewProxyAwareHTTPClient(..., 0)`), so without this an unresponsive provider
  would block model registration indefinitely. Because `host.http.do` is a
  synchronous C callback with no context parameter, the timeout is enforced
  plugin-side and abandons the in-flight goroutine on expiry.
- The plugin now retains the host API pointer it previously discarded, so it has
  a live host bridge — the same mechanism the quota plugin already uses.
- Tests assert against a recorded real payload, including an explicit assertion
  that none of the six previously hardcoded models are served.

## Alternatives rejected

**Keep a hardcoded list, refreshed by hand.** The status quo. It had already
drifted to zero-percent accuracy while looking healthy in CI.

**Correct the hardcoded list and stop there.** Buys accuracy until the provider
next changes anything, with no signal when that happens.

**Discover, but fall back to a baked list on failure.** Rejected deliberately.
A stale baked list is worse than no list: it routes requests to models that no
longer exist and reports success. The last-good cache gives the same
availability benefit without ever inventing a model.

**Use `net/http` directly from the plugin.** Simpler, and was assumed necessary,
but bypasses CPA's proxy configuration. `host.http.do` is available and
proxy-aware.

**Fetch during `auth.parse` and serve the cache from `model.for_auth`.** The
originally suggested shape. Unnecessary once `model.for_auth` was confirmed to
carry `Attributes`, and it would have put a network call on the credential-load
path.

## Notes

`model.register` is now effectively dead code for this plugin: it declares both
`ModelRegistrar` and `ModelProvider`, and the host prefers the latter
(`internal/pluginhost/adapters.go:260`), so `model.static` is what actually runs.
Retained for ABI completeness.

Removing the `openai-compatibility` block from the live `config.yaml` is #4's
stated goal and the real test of the `UnregisterClient` trap. It is deliberately
**not** part of this change.

Unlike ADR-0001, the host behaviour above was verified by reading CPA source
*and* the recorded provider payload, but the end-to-end path has not yet been
exercised against the live instance.
