# Contributing

## Upstream baseline

The production compatibility baseline is the official CLIProxyAPI submodule:

```text
upstream/CLIProxyAPI @ v7.2.61
```

Do not copy internal CLIProxyAPI packages into this repository. The plugin keeps its ABI wire contract local so it can build with the host's Go-independent C ABI, while the submodule remains the authoritative reference for method names, schemas, examples, and integration tests.

Verify the baseline before changing ABI-facing code:

```bash
make verify-upstream
```

Relevant upstream references:

- `upstream/CLIProxyAPI/sdk/pluginabi/types.go`
- `upstream/CLIProxyAPI/sdk/pluginapi/types.go`
- `upstream/CLIProxyAPI/internal/pluginhost/host_callbacks.go`
- `upstream/CLIProxyAPI/internal/pluginhost/auth_callbacks.go`
- `upstream/CLIProxyAPI/examples/plugin/host-callback-auth-files/`
- `upstream/CLIProxyAPI/examples/plugin/management-api/`

## Development loop

```bash
make fmt
make test
make verify-upstream
make build
```

For ABI behavior, smoke-load `dist/cpa-quota-api-extension.so` with the pinned upstream CLIProxyAPI binary or a production-matching binary before release.

## Updating the submodule

Only update the submodule in an explicit compatibility change:

```bash
cd upstream/CLIProxyAPI
git fetch --tags origin
git checkout <tag-or-commit>
cd ../..
```

Then update `CPA_COMPAT_TAG` in `Makefile`, rerun all tests and smoke tests, and document any wire-contract changes.
