# ADR-0001: Make OpenCode Go a real auth via an auth-parser plugin

- **Status:** Accepted
- **Date:** 2026-08-29
- **Context issue:** [#1](https://github.com/jonmast/cpa-quota-api-extension/issues/1)

## Context

The quota plugin reads credentials through the host bridge: `host.auth.get(authIndex)`
returns raw auth JSON, from which a fetcher pulls the token it needs.

OpenCode Go cannot be read this way. It exists as an `openai-compatibility`
*config* entry rather than an auth, so it has no `path` attribute.
`authPhysicalJSONByIndex` (`internal/pluginhost/auth_callbacks.go:236-244`)
requires `authAttribute(auth, "path")` and reads the credential from disk — with
no path, the call fails. The obvious fallback, `host.auth.get_runtime`, returns
metadata only: no `Attributes`, no `Metadata`.

So OpenCode Go quota cannot be fetched at all without changing how its credential
is represented to CPA.

## Decision

Ship an **auth-parser plugin** that emits an auth from `auths/opencode-go.json`:

- `Provider: "openai-compatibility"`
- `Attributes{ base_url, api_key, compat_name, provider_key }`

This works because:

- `internal/watcher/synthesizer/file.go:121` stamps `AttributePath` automatically
  for plugin-parsed auths, so `host.auth.get` succeeds
- a `base_url` attribute alone routes to the compat executor
  (`sdk/cliproxy/service_executors.go:92`)
- `resolveCredentials`
  (`internal/runtime/executor/openai_compat_executor.go:918-927`) reads `base_url`
  and `api_key` directly from `Attributes`, with no `config.yaml` lookup

The built-in compat executor is sufficient. No custom executor is needed.

The auth plugin is a **second build target in this repo**, sharing the submodule.
It is coupled to the quota adapter through the provider key and the attribute
names, so separate repos would mean coordinating two releases to change one string.

### Required companion behaviour

Setting `compat_name` makes `isCompatAuth` return true
(`sdk/cliproxy/service_models.go:188-191`). With no matching `config.yaml` entry,
if `appendPluginModels` returns nothing the auth is **`UnregisterClient`'d**
(`:250-258`).

The plugin **must** register OpenCode Go's model list for its provider key
(`appendPluginModels` → `pluginModelsForProvider`,
`sdk/cliproxy/service_executors.go:456`).

## Consequences

- OpenCode Go's model list moves out of `config.yaml` and into plugin code. Accepted.
- The repo gains a second build artifact and a second thing to deploy.
- **Failure mode to watch:** if model registration is missing or wrong, the auth
  routes but is never selected, and nothing obviously errors. This is silent and
  should be asserted in tests rather than checked by hand.
- The credential lives in exactly one place; nothing is duplicated into config.

## Alternatives rejected

**Plain auth file, no plugin.** The generic file branch (`file.go:185-201`) sets
`Provider` from `type` but carries no `base_url` or `compat_name`, so the auth
cannot route.

**OpenCode Go key in plugin config.** Works, but duplicates a credential that
already exists on disk — two copies to rotate.

**Patch the host bridge** to expose `Attributes["api_key"]` on `get_runtime`,
mirroring `api_tools.go:236-240`. Was the fallback plan; unnecessary once the
auth-parser route was validated. Still a reasonable upstream PR on its own merits.

**Fork CPA.** Rejected for the broader quota effort and it does not help here
either — see the quota-signals reasoning in #1.

## Notes

The mechanism was validated by reading CPA v7.2.144 source, not by running it.
The first implementation step is a build-and-deploy tracer bullet against the live
instance, which is where this reasoning gets confirmed or falsified.
