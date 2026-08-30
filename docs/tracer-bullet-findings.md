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
