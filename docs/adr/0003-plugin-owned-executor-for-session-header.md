# ADR-0003: Own the executor so `x-opencode-session` reaches oc-go

Status: accepted

## Context

oc-go will require an `x-opencode-session` header on chat completion requests.
The opencode client already sends it; CPA has to forward it.

Until now the plugin only parsed the credential and emitted a compatibility auth
(`compat_name` + `provider_key` attributes, provider `openai-compatibility`),
leaving execution to CPA's built-in OpenAI-compatibility executor. That executor
cannot forward the header:

- It sets only `Content-Type`, `Authorization`, `User-Agent`, plus `Accept` and
  `Cache-Control` for SSE, then applies the static `header:*` auth attributes
  (`internal/runtime/executor/openai_compat_executor.go:133`, `:332`).
- The client's headers *do* reach it as `opts.Headers` (gin ctx →
  `headersFromContext`, `sdk/api/handlers/handlers.go:335` → `opts.Headers`,
  `model_execution.go:198` → conductor → executor), but they are used only for
  payload-rule matching and content-type sniffing. Nothing copies them onto the
  outbound request. `passthrough-headers` is response-side only.
- A `header:x-opencode-session` attribute is per-credential and constant, so it
  cannot carry a per-request session ID.
- `upstream/CLIProxyAPI` is pinned to `v7.2.61` and checked by
  `make verify-upstream`, so patching the built-in executor is not shippable.

## Decision

The plugin registers its own provider executor for `opencode-go` and builds the
oc-go request itself, copying an allow-list of client headers — currently just
`x-opencode-session` — onto it.

Consequences for the auth: `compat_name` and `provider_key` are no longer
emitted, and the auth's provider becomes `opencode-go` instead of
`openai-compatibility`. This is required, not cosmetic: the conductor routes by
`executorKeyFromAuth` (`sdk/cliproxy/auth/conductor.go:5961`), which sends any
auth carrying `compat_name` back to the built-in compat executor. Model
registration follows the same key (`sdk/cliproxy/service.go:1217`), and the
executor binds to the provider from `model.register`
(`internal/pluginhost/adapters.go:965`), so provider key, auth provider and
executor identifier are one string, `providerKey`.

Declared formats are `openai` in and `openai` out, so the host translates
between the client's protocol and OpenAI chat completions on our behalf.

## Streaming chunk shape

The pinned host defines the wire shape of an `openai`-output plugin executor's
stream chunks inconsistently, so the plugin picks per request:

- Translated path (client is not OpenAI chat completions): the host feeds our
  chunks to the openai source translators, which drop anything without an SSE
  `data:` prefix (`internal/translator/openai/claude/openai_claude_response.go:106`).
  It also appends its own `data: [DONE]` tail
  (`internal/pluginhost/adapters.go:1574`).
- Passthrough path (client requested format equals ours): translation is skipped
  (`internal/pluginhost/adapters.go:1498`) and the handler writes `data: %s`
  around our chunk (`sdk/api/handlers/openai/openai_handlers.go:668`), so a
  prefix would be duplicated.

The executor request exposes only our own normalized formats, so the client's
entry protocol is inferred from the `request_path` metadata key
(`sdk/api/handlers/handlers.go:277`). Terminal `[DONE]` markers are dropped in
both cases; the host or the handler emits its own.

## Token counting: not implemented, on purpose

Counting never involves an upstream call. oc-go's surface is three
OpenAI-compatible endpoints — `GET /models`, `POST /chat/completions`,
`GET /usage` — with no counting route and no Anthropic-style API. The built-in
compatibility executor does not call one either: it tokenizes the translated
payload locally with tiktoken and returns a usage document
(`openai_compat_executor.go:579`).

`executor.count_tokens` is only ever invoked by CPA's own Claude
(`/v1/messages/count_tokens`) and Gemini (`:countTokens`) routes. Clients in this
deployment speak OpenAI chat completions, which has no counting route, so the
method is unreachable and returns an error.

It was implemented and then reverted. A working implementation costs ~21 MB:
the tiktoken vocabularies take `cpa-opencode-go-auth.so` from ~4.4 MB to
~25.8 MB, and the tables link wholesale (verified — neither narrowing the model
mapping nor dropping `cl100k_base` reduces it). If a Claude- or Gemini-protocol
client ever needs this, the recipe is:

- Depend on `github.com/tiktoken-go/tokenizer` at the version upstream pins, and
  mirror `helps.CountOpenAIChatTokens`'s segment collection so counts agree with
  the other providers in the instance.
- Resolve encodings with `tokenizer.Get`, not `tokenizer.ForModel`: everything
  upstream routes through `ForModel` resolves to `o200k_base` except `gpt-4` and
  `gpt-3.5`, which resolve to `cl100k_base`
  (`tiktoken-go/tokenizer@v0.7.0/tokenizer.go:208`). Two branches cover the nine;
  verified equal counts across all nine model classes on text where the two
  encodings disagree.
- Emit the shape the client's protocol expects (`{"input_tokens":N}` for Claude,
  `{"totalTokens":N}` for Gemini). Both handlers write the executor payload
  verbatim, and the plugin adapter's response translator passes usage-shaped
  documents through unchanged (verified against the pinned tree). Note this is
  better than the built-in, which returns its OpenAI usage document to every
  caller regardless of protocol.

## Image endpoints

Not lost. The built-in executor's image branch only triggers when the source
format is `openai-image`, i.e. the client called `/v1/images/generations` or
`/v1/images/edits` (`openai_compat_executor.go:618`). This plugin registers
every OpenCode Go model as a chat model, so those routes never selected this
provider.

## Usage records and account health

Built-in executors publish a `usage.Record` (provider, model, tokens, latency,
failure status) through `helps.NewExecutorUsageReporter`. Plugin executors
publish nothing: the plugin host's `executorAdapter` creates no reporter, and the
ABI has no host callback for publishing one — `usage.handle` only *delivers*
records to usage plugins. So this repo's own health feature, which is fed by
those records (`health.go:154` → `healthStore`), loses one of its two inputs for
OpenCode Go accounts.

It does not go dark. `classifyAccountHealth` (`health.go:78`) reads the host auth
inventory as well as the local observation, and the conductor's `MarkResult` sets
`Status`, `Unavailable` and `NextRetryAfter` for *any* executor because it lives
in the conductor, not in the built-in executors.

| Still classified correctly | Lost for OpenCode Go |
| --- | --- |
| `disabled`, `unavailable`, `healthy` | `degraded` never triggers — `RecentFailures` comes only from usage events |
| `rate_limited`, via the host's `NextRetryAfter` on 429s | `/v1/incidents` and `failure_events` rows |
| Host success/failure counters | Latency, `last_success_at`, `last_failure_at` |
| Non-routable on hard failures, via `Status=error` | Precise `unauthorized`/`forbidden` labels, which degrade to `unavailable` |

Accepted as-is. The residual risk is a false green: an account failing
intermittently below the hard-failure bar reads `healthy` where another provider
would read `degraded`. The fix, if that ever bites, is either a
`host.usage.publish` callback upstream, or having this plugin post health events
to the quota plugin's management API over loopback.

OpenCode Go *quota* is unaffected: it is polled directly from oc-go's usage
endpoint with the credential's API key (`providers.go:311`), independent of who
executes the traffic.

## Other consequences

- Also dropped relative to the built-in executor: thinking-suffix handling,
  `ApplyPayloadConfig` rewrite rules, and upstream request logging.
- The chunk-shape inference is a workaround for an upstream inconsistency. If a
  future CPA release adds real client-header passthrough or a client-format
  field on `ExecutorRequest`, most of `executor.go` can be deleted.
