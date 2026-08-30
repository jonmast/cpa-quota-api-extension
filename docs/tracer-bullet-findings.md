# Tracer bullet findings (issue #2, build-and-verify half)

Date: 2026-08-29. Branch: `issue-2-tracer`. Worktree HEAD at time of run: `2fcbb3d`.

**Scope note.** This run was explicitly restricted by the operator to local
build-and-verify. Nothing was deployed to `cpamp.homelab.jonmast.com`, no
kwallet secret was read, and no network call was made to that host. The
deployment / live-load / route-response / real-credential acceptance criteria on
issue #2 remain **unverified** and are deferred to the human.

## Results of the three make targets

| Target | Result | Notes |
|---|---|---|
| `make verify-upstream` | **PASS** | All 10 assertions pass against the pinned submodule. |
| `make test` | **PASS** | `ok github.com/dinhkarate/cpa-quota-api-extension 0.460s` |
| `make build` | **PASS** | Produced `dist/cpa-quota-api-extension.so`, 8.8 MB, ELF 64-bit LSB shared object, x86-64, stripped. |

### Toolchain wrinkle (not a code defect)

`go` is not on the default `PATH` in this environment (`make: go: Not a
directory`). The targets were run with
`PATH=/nix/store/xrgn6m0c5vbz06467q06zdwnk6qqdl7z-go-1.26.5/bin:$PATH`. `cc`/`gcc`
were already present, so `CGO_ENABLED=1` worked. No Makefile or source change was
made — this is an environment/shell-profile issue, not a build breakage. Anyone
reproducing must put a Go 1.24+ toolchain on `PATH` first.

### Submodule pin

`git submodule status` reports `ca67caf0872eef65f8eaedf2e127cf214a49621d
upstream/CLIProxyAPI (v7.2.31-104-gca67caf0)`. The `git describe` string is
cosmetic; `verify-upstream`'s check that HEAD equals `git rev-list -n 1 v7.2.61`
passes, so the pin **is** v7.2.61 as intended.

### Loadability

`dist/cpa-quota-api-extension.so` was `dlopen`'d locally via `ctypes.CDLL` and
both required symbols resolved:

- `cliproxy_plugin_init` — the symbol the host looks up
  (`internal/pluginhost/loader_unix.go:120`)
- `cliproxyPluginCall`, `cliproxyPluginFree`, `cliproxyPluginShutdown`

So the artifact is a loadable shared library with the correct entry point. This
proves the cgo `c-shared` toolchain, *not* that CPA accepts it at runtime.

## Version numbers actually declared

| Property | This plugin | Pinned submodule (v7.2.61) | Dev checkout `/home/jon/CLIProxyAPI` (v7.2.144, 2026-08-28) |
|---|---|---|---|
| ABI version | `abiVersion uint32 = 1` (`contract.go:9`) | `ABIVersion uint32 = 1` (`sdk/pluginabi/types.go:7`) | `ABIVersion uint32 = 1` (`sdk/pluginabi/types.go:7`) |
| Schema version | `schemaVersion uint32 = 1` (`contract.go:10`) | `SchemaVersion uint32 = 1` (`sdk/pluginabi/types.go:11`) | `SchemaVersion uint32 = 4` (`sdk/pluginabi/types.go:14`) |

The plugin writes its ABI version into the shared struct at `abi.go:50`
(`plugin.abi_version = C.uint32_t(abiVersion)`).

Issue #1's claim that "CPA's `SchemaVersion` is now 4 (`:14`)" is **confirmed**
against the newer checkout. Note the pinned submodule is still at 1, so
`verify-upstream` cannot detect schema drift — it asserts method-name presence
only.

## Is there really a legacy `schema_version < 3` path?

Partly. The framing in issue #1 is imprecise but the conclusion holds.

1. **There is no lower-bound rejection at all.** The host only rejects a plugin
   whose declared schema version is *greater* than its own:
   `if resp.SchemaVersion > pluginabi.SchemaVersion { ... "plugin schema version %d is not supported" }`
   (`internal/pluginhost/rpc_client.go:69-70`, identical in both versions). So a
   plugin declaring 1 against a host at 4 is accepted — not because of a
   "legacy path", but because no minimum is enforced.
2. A missing/zero `schema_version` is explicitly normalised to 1
   ("Missing schema_version is treated as the original contract",
   `rpc_client.go:73-76`) — an explicit legacy shim.
3. The only *behavioural* fork on `< 3` is
   `streamChunkOmitsRequestBodies(schemaVersion) => schemaVersion >= 3`
   (`internal/pluginhost/adapters_interceptors.go:272,321,329`): schema <3 plugins
   still receive `OriginalRequest`/`RequestBody` clones on every stream chunk.
   **This plugin registers no stream interceptors, so that fork is irrelevant to
   it.** Declaring 1 costs nothing here.
4. Upstream tests corroborate acceptance:
   `internal/pluginhost/rpc_schema_test.go:134` (`...AcceptsModelRouterOnSchema1`)
   and `:122` (rejects `SchemaVersion + 1`).

Unrelated things also named `schema_version` — `internal/pluginstore/registry.go:18-19`
(store registry, accepts 1 or 2) and `internal/pluginstore/manifest.go:112`
(installed-plugin manifest, requires v2) — are **different versioning concerns**
and do not gate the RPC handshake. If the plugin is ever installed through the
plugin *store* rather than pointed at directly by config, those constraints apply
and are a separate check.

## ABI struct layout across the version gap

`internal/pluginhost/loader_unix.go` is **byte-for-byte identical** between
v7.2.61 and v7.2.144 (232 lines, no diff). The plugin's hand-copied cgo preamble
(`abi.go:7-36`) matches the host's `cliproxy_buffer`, `cliproxy_host_api`, and
`cliproxy_plugin_api` field-for-field. The only difference is an omitted `const`
qualifier on function-pointer parameters, which has no effect on layout or
calling convention.

Version validation is a strict equality check that fails loudly with a clean
teardown, not silent corruption:
`if uint32(client.api.abi_version) != pluginHostABIVersion { client.Shutdown(); return nil, fmt.Errorf(...) }`
(`loader_unix.go:154-157`), plus a nil-function-pointer check at `:158-161`.

So issue #1's core structural claim — the pin affects only `verify-upstream`
because ABI types are hand-copied and unchanged — **holds for the C layer** across
v7.2.61 → v7.2.144.

## Risks that could still falsify issue #1's compatibility reasoning

Honest list; none of these are closed by this run.

1. **The live box was not touched.** Everything above is source comparison plus a
   local `dlopen`. The handshake against a *running* v7.2.145 process is still
   unproven. The gating claim in #1 ("step 1 gates everything") is only half
   discharged.
2. **The dev checkout is v7.2.144, the box is ~v7.2.145.** One commit of drift I
   could not inspect. Low risk, non-zero.
3. **`verify-upstream` is blind to the drift that matters.** It pins to a
   submodule whose `SchemaVersion` is 1 while the deployment target is at 4, and
   it only greps for method-name presence. It would not catch a changed *payload
   shape* for `MethodHostAuthGet` / `MethodHostAuthList` / `MethodHostHTTPDo`, nor
   a changed management-registration schema, between v7.2.61 and v7.2.145. Passing
   `verify-upstream` should not be read as "compatible with the running box".
   Consider bumping the submodule to the deployed tag as follow-up work.
4. **Semantics behind stable method names are unverified.** The JSON exchanged
   over `MethodHostAuthGet` (issue #1 depends on it returning a readable Copilot
   credential) was not exercised — no existing test reaches a provider fetcher,
   as #1 itself records. A field rename in the auth payload between v7.2.61 and
   v7.2.145 would compile and load fine and fail only at request time.
5. **Go toolchain skew.** Built with Go 1.26.5 against a `go 1.24.0` module. cgo
   `c-shared` plugins are `dlopen`'d into a host that has its own Go runtime;
   two Go runtimes in one process is supported by this design but is a class of
   problem that only shows up at runtime under load, not at build time.
6. **Plugin-store constraints** (item 3 in the previous section) apply if
   installation goes through the store path rather than a direct config
   reference. Unknown which the box uses.

## Recommendation for sequencing

The build-and-toolchain half of #2 is green and the compatibility reasoning in #1
survives source-level scrutiny — with the caveat that "no legacy path is needed"
is a better description than "CPA retains legacy paths for schema_version < 3".
The remaining acceptance criteria are a live deploy, and until that runs, items 1
and 4 above are the real residual risk for tickets that depend on
`host.auth.get` behaviour (Copilot, OpenCode Go).

---

# Deployment environment (added 2026-08-30)

Investigation of the actual target ahead of the deploy half of #2. This
**corrects the environment note in spec #1** and replaces guesswork about the
host with what is actually deployed.

## The spec names the wrong host

Spec #1 states "The running CPA is at `http://cpamp.homelab.jonmast.com`". That
host is a *different application*:

| Host | Image | What it is | Defined in |
|---|---|---|---|
| `cpamp.homelab.jonmast.com` | `seakee/cpa-manager-plus:v1.12.6` | Separate management UI (CPAMGMT) | `k8s-conf/apps/cpamp/release.yaml` |
| `cliproxy.homelab.jonmast.com` | `eceasy/cli-proxy-api:v7.2.137` | **The actual CLIProxyAPI** | `k8s-conf/apps/cliproxyapi/release.yaml` |

Two corrections follow: the deploy target is `cliproxy...`, not `cpamp...`; and
the running version is **v7.2.137**, not the "~v7.2.145" assumed in #1. The
schema-version conclusion is unaffected (see above — there is no lower-bound
rejection at all), but any future assertion about upstream behaviour should be
checked against v7.2.137.

## How it is deployed

Kubernetes, via Flux GitOps — a `HelmRelease` using the `bjw-s/app-template`
chart. There is no host to `scp` to; changes normally flow through the
`k8s-conf` repo.

Two properties of the config matter for this work:

- The config is seeded onto a PVC by an initContainer using `cp -n`, so it
  **never overwrites**. Panel edits persist and the live config intentionally
  drifts from the SOPS-encrypted seed in git. Reading `config.sops.yaml` does
  not tell you what the live config says.
- The app rewrites the file on startup to bcrypt-hash the management secret key,
  so it is never byte-identical to the seed even before any edit.

## The blocker: no path into the container for the artifact

CPA loads plugins from `plugins.dir`, resolved relative to the workdir
`/CLIProxyAPI` — so `/CLIProxyAPI/plugins/linux/amd64/`. In the live deployment:

- the `plugins:` block is **absent from the config entirely**;
- the image is stock upstream, with no plugin directory;
- the only PVC (`auth`) mounts `/root/.cli-proxy-api`, **not** the workdir.

The artifact therefore has nowhere to live today. This is an infrastructure
decision, not a code change, and it gates every remaining acceptance criterion
on #2.

Options considered:

1. **Custom image** `FROM eceasy/cli-proxy-api` that `COPY`s the `.so` into the
   plugin directory, pinned by digest in the `HelmRelease`. GitOps-native and
   reproducible; survives restart and rescheduling. Costs a registry and a build
   pipeline.
2. **Plugins PVC + initContainer** fetching the artifact. No image build, but
   adds a fetch dependency to pod startup.
3. **Manual `kubectl cp` into the running pod.** Lost on any reschedule, so it
   is a *verification* technique rather than a deployment.

**Decision (operator, 2026-08-30):** use option 3 for now — a throwaway copy to
answer the open runtime questions on #2. A durable mechanism is deferred until
after the plugin is known to load and work.

## Binary compatibility: checked and clear

The risk that would have invalidated the whole approach is a libc mismatch
between the NixOS build host and the container. It is fine:

- The artifact requires at most `GLIBC_2.34` (`objdump -T`); the container is
  `debian:bookworm`, which ships glibc 2.36.
- `NEEDED` entries are plain sonames (`libc.so.6`, `libdl.so.2`,
  `libpthread.so.0`, `libresolv.so.2`).
- The nix `RUNPATH` pointing at `/nix/store/...` is harmless: absent directories
  are skipped and the loader falls back to the container's search path.

So the `.so` built here should `dlopen` inside the running container. That is a
static-analysis conclusion; the live copy is what proves it.

## What the live run still has to answer

Unchanged from the list above, now with a concrete target:

1. That the plugin loads under **v7.2.137** with `schemaVersion` 1.
2. That `host.auth.get` returns a readable **Copilot** credential with the field
   names the #6 fetcher expects.
3. That the **OpenCode Go** auth is not silently `UnregisterClient`'d — spec #1
   calls this the likeliest silent failure.
4. That the three provider adapters, built entirely against *recorded* payloads,
   match what the real APIs return.

---

# Live verification results (2026-08-30)

The plugin was loaded and exercised on the running instance. Method: `kubectl cp`
the artifact into `/CLIProxyAPI/plugins/linux/amd64/` under a *new* version
filename, then bump `plugins.configs.cpa-quota-api-extension.store.version`
through the management API. That triggers `Host.ApplyConfig`
(`internal/pluginhost/host.go:199`) via `reloadConfigAfterManagementSave`
(`internal/api/handlers/management/handler.go:206`), which re-selects plugin
files by version and **hot-reloads without a pod restart**.

A new filename is required: a currently-loaded library cannot be overwritten
(`internal/pluginstore/install.go:35`).

Management calls go through `cpamp.homelab.jonmast.com`, which fronts the CPA
management API — this is how `ai-quota.py` already reaches it.

## Confirmed working

- **The fork loads on the live v7.2.137 host** with `abiVersion` 1 and
  `schemaVersion` 1. The central open question of #2 is answered: yes.
- **Claude (#5) is correct against the real API.** `five_hour` 90%, `seven_day`
  63%, `extra` 32.8% (4034 of 6000 credits), a `binding_window` of `seven_day`,
  and one scoped model window — *Fable* at 65%. Scoped weeklies, extra usage and
  the API-reported binding window all behave as designed on real data.
- **The provider-nested shape (#8) is emitted live**, keyed by provider, with the
  `accounts` list retained alongside.
- **Copilot (#6) works after two fixes** (below).

## Two real bugs, both invisible to the unit tests

Both came from spec #1 describing payloads that do not match reality. Both were
caught only by running against the live instance — the exact justification for
#2 existing.

### 1. Wrong credential field name

The Copilot fetcher read `access_token`; the live credential written by the
`cliproxyapi-copilot` auth plugin stores it as **`github_access_token`**. The
first live run returned `credential_incomplete: Copilot access_token is missing`
and no Copilot data at all.

The token is also a **`ghu_`** GitHub App user-to-server token, not the `gho_`
OAuth token spec #1 describes.

Fixed by reading `github_access_token` with `access_token` as a fallback.

### 2. Reset date is top-level, not per snapshot

Spec #1 states each `quota_snapshots` entry carries `entitlement`, `remaining`,
`percent_remaining` and `reset_date`. The real payload has no `reset_date`
anywhere. Each snapshot carries `"quota_reset_at": 0`, and the actual date is
top-level:

```json
"quota_reset_date": "2026-09-01",
"quota_reset_date_utc": "2026-09-01T00:00:00.000Z"
```

So every Copilot window reported `reset_at: null` on the first run, silently
failing the "monthly reset date is reported" criterion on #6 while looking
healthy.

Fixed by reading top-level `quota_reset_date_utc`, falling back to
`quota_reset_date`, and applying it to all Copilot windows.

Also observed, not a bug: `chat` and `completions` come back
`"unlimited": true` with `percent_remaining: 100` and a zero entitlement. They
report as 100% and therefore never bind the pill, which is the desired outcome.

Post-fix live output:

```
claude   five_hour 90%   seven_day 63%   extra 32.8%   model Fable 65%
copilot  premium_interactions 56.6%   chat 100%   completions 100%
         all reset 2026-09-01T00:00:00Z
```

## Still unverified: OpenCode Go (#7)

OpenCode Go did not appear, as expected. Two things are missing and neither is a
defect in #7:

1. The auth-parser plugin from #4 is a **separate artifact that was never
   installed** on the box.
2. There is **no OpenCode Go credential file** in the auth directory — it holds
   only `claude-jon@jonmast.com.json` and `copilot-jonmast.json`. The Go key
   still lives in the `openai-compatibility` config section, which is precisely
   the problem #4 exists to solve.

Validating #7 live therefore means installing a second plugin *and* creating a
credential, which changes routing-relevant state. That is beyond a throwaway
verification and was deliberately not attempted.

Given both Copilot bugs, **#7's OpenCode Go payload assumptions should be
treated as unverified** until it runs against the real endpoint.

## Live state after this exercise

Reverted. The config is back to `version: 0.3.0`, the upstream
`cpa-quota-api-extension-v0.3.0.so` is the loaded artifact, and the staged
v0.4.0/v0.5.0/v0.6.0 files were deleted from the pod. The plugin directory is on
the container's ephemeral layer, so a restart would have discarded them anyway —
but leaving the config pointing at a version that no longer exists would have
broken the widget on the next restart.
