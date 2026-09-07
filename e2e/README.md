# End-to-end harness: `x-opencode-session` forwarding

Proves that a client's `x-opencode-session` header survives the whole path —
client → CLIProxyAPI → plugin executor → opencode-go — using the pinned upstream
in `upstream/CLIProxyAPI` and a stub upstream. No live credential, no deployed
proxy, no OpenCode session to lose.

```bash
./e2e/run.sh        # or: make e2e
```

## Why this exists

The unit tests in `authplugin/` stub the host boundary out, so they pass whether
or not the plugin executor is ever invoked. Every failure mode found in this
area lived *above* that boundary: the host registering a different executor, or
handing the plugin empty headers. Only a real host can catch those.

## Cases

Mirrors the fixed-behaviour spec from the original investigation:

| Case | Request | Expected |
|---|---|---|
| A | oc-go model, session header | 200 |
| B | oc-go model, no session header | 400 `MissingSessionID` |
| C | stub called directly with header | 200 (control) |
| D | oc-go model, streaming, session header | 200 |

B stays 400 on purpose: it proves the stub actually enforces the header, so A
and D passing means something.

## Reading a failure

`run.sh` prints what the stub received and names the cause:

- **`ua=cli-proxy-openai-compat`** — the native OpenAI-compat executor handled
  the call and this plugin's executor never ran. It drops client headers by
  design. Usually means the auth carries an attribute that makes the host infer
  a native compat provider (`base_url`, `compat_name`, `provider_key` — see
  `sdk/cliproxy/service_executors.go:86-101`), or an `openai-compatibility`
  entry in config claims the same provider key.
- **`ua=cpa-opencode-go-auth/<version>` with `session=-`** — our executor ran but
  the host handed it no client headers.
- **nothing reached the stub** — routing or model registration failed earlier.
  Check that `ExecutorModelScope` still admits auth-bound models, since the host
  skips `model.for_auth` otherwise and the provider registers no models at all.

## Pieces

- `stub/` — fake opencode-go. Logs `method path ua= session=` per request and
  returns 400 `MissingSessionID` when the header is absent, like the real one.
- `config.yaml` — proxy config: plugin enabled, no `openai-compatibility` entry.
- `auths/opencode-go.json` — fake credential pointing at the stub.
- `run.sh` — builds plugin, stub and the pinned proxy; runs the cases.

Artifacts and logs land in `/tmp/opencode/oc-go-session-e2e/`.
