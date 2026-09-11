# ADR-0007: Read OpenCode Go reasoning levels from the models.dev registry

- **Status:** Accepted
- **Date:** 2026-09-10
- **Extends:** [ADR-0002](0002-discover-opencode-go-models-at-runtime.md) — same
  "the provider is the source of truth" principle, applied to model metadata
  rather than to the model list.

## Context

Removing the `openai-compatibility` config entry (ADR-0003's prerequisite) took
something with it that nobody noticed at the time: **per-model reasoning
levels**.

Under the config entry, every non-image model went through
`buildOpenAICompatibilityConfigModels`
(`sdk/cliproxy/service_models.go:726-734`), which read `thinking.levels` from
config and, absent that, defaulted to `low/medium/high`:

```go
thinkingSupport := model.Thinking
if thinkingSupport == nil && !model.Image {
    thinkingSupport = &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}}
}
info.Thinking = modelconfig.NormalizeThinkingSupport(thinkingSupport)
```

The plugin path has no equivalent. `pluginModelInfoToRegistryModelInfo`
(`internal/pluginhost/adapters.go:139`) copies `ModelInfo.Thinking` straight
through and supplies no default, and this plugin's local `modelInfo` struct
carried no `Thinking` field at all. So `registry.ModelInfo.Thinking` was nil for
every OpenCode Go model, and `applyCodexClientThinkingMetadata`
(`internal/client/codex/models/models.go:376`) returned early on it.

**Returning early does not mean the client saw no levels.** Running the e2e
harness with `Thinking` forced back to nil shows the Codex catalog serving
`low,medium,high,xhigh` for a model whose registry entry advertises
`low,medium,high,max` — the catalog's own generic fallback, left in place
precisely because the early return never overwrote it. The regression therefore
served *wrong* levels rather than absent ones, which is why nothing looked
broken: clients got a plausible list, just not this model's list.

The symptom that surfaced it: `gpt-5.6-luna` advertised `max` when addressed
bare, because the Codex client's own template happens to carry an entry under
that slug and the template is consulted first (`models.go:254`). The
`opencode-go/`-prefixed alias matched no template, fell through to the nil
registry value, and silently lost every level.

**oc-go does not serve this metadata.** `GET https://opencode.ai/zen/go/v1/models`
returns `id`, `object`, `created`, `owned_by` and nothing else. OpenCode
publishes model capabilities separately, in **models.dev**, its own registry,
under a provider entry whose `api` field is precisely this plugin's base URL:

```json
"opencode-go": {
  "id": "opencode-go", "name": "OpenCode Go",
  "api": "https://opencode.ai/zen/go/v1",
  "models": {
    "gpt-5.6-luna": {
      "reasoning": true,
      "reasoning_options": [
        {"type": "effort",
         "values": ["none", "low", "medium", "high", "xhigh", "max"]}
      ]
    }
  }
}
```

## Decision

**Read reasoning levels from models.dev. Ship no defaults and no hand-written
per-model table.**

- `model.for_auth` revalidates `https://models.dev/api.json` alongside its
  existing `/models` discovery call, through the same `host.http.do` callback
  and the same 10s timeout.
- Only the `opencode-go` provider entry is decoded. The payload describes 200+
  providers and is ~4.5 MB, so the top level is decoded as
  `map[string]json.RawMessage` and everything outside our key is skipped.
- The response's `ETag` is retained and replayed as `If-None-Match`. The
  endpoint serves `cache-control: max-age=0, must-revalidate`, and
  `model.for_auth` runs on every config reload, auth change and 15-minute
  refresh, so the steady state is a 304.
- The three `reasoning_options` types map as follows:

  | models.dev | `registry.ThinkingSupport` | Why |
  | --- | --- | --- |
  | `effort` → `values` | `Levels` | The only form CPA's level-based consumers can use |
  | `toggle` | `ZeroAllowed` | Reasoning can be switched off but not steered |
  | `budget_tokens` | *(not mapped)* | oc-go exposes no way to set a budget over an OpenAI-compatible request |

- Levels are lowercased, de-duplicated, and `none`/`auto` are reflected into
  `ZeroAllowed`/`DynamicAllowed` **in the plugin**, replicating
  `modelconfig.NormalizeThinkingSupport`. The host runs that normalizer on the
  config path only, never on plugin-supplied thinking.
- A model with no usable controls gets `Thinking: nil`, not an invented
  `low/medium/high`. That covers both `reasoning: false` and the several models
  (`glm-5`, `kimi-k2.x`, the `mimo` family) that report `reasoning: true` with an
  empty `reasoning_options`.
- `model-metadata-url` is the operator escape hatch, symmetric with ADR-0002's
  `models`. Setting it to `off` disables the overlay; there is no way to spell
  "empty" instead, because `yamlScalars` cannot distinguish an empty value from
  an unset key.

This is an **enrichment overlay, not a second model list.** Which models exist
is still decided solely by the live `/models` call. The two sources already
disagree in both directions — oc-go serves `hy3-preview`, which models.dev
omits; models.dev carries `ox-alpha-free`, which oc-go does not serve — so the
overlay only ever annotates IDs discovery already returned.

## Consequences

- Both the bare `gpt-5.6-luna` and the `opencode-go/gpt-5.6-luna` alias now
  resolve to the same levels. `applyModelPrefixes` mints the prefixed alias by
  shallow-copying the `ModelInfo` (`sdk/cliproxy/service_models.go:611`), so the
  two share one `*ThinkingSupport` by construction rather than by coincidence.
- Levels are now *more* accurate than the config entry ever was, which gave every
  model the same `low/medium/high`. The registry distinguishes `high/max` for
  `deepseek-v4-pro`, `max`-only for `kimi-k3`, `minimal…xhigh` for the muse-spark
  models, and toggle-only for `longcat-2.0` and `minimax-m3`.
- **A second network dependency at registration time.** Deliberately weaker than
  the first: `refreshModelMetadata` returns no error. A models.dev outage costs
  the thinking metadata and logs a warning; it can never cost the model list,
  which would unregister the auth. Cold start with models.dev unreachable
  reproduces exactly the pre-change behaviour.
- Models absent from the registry register with no reasoning metadata rather
  than with a guess. `hy3-preview` is in that state today.
- `budget_tokens` data is read and discarded. If oc-go ever exposes a budget
  control, `Min`/`Max` are already in the wire contract and the mapping is a
  three-line change.

## Alternatives rejected

**Restore the `low/medium/high` default in the plugin.** Reproduces the old
behaviour exactly, including its inaccuracy: it would tell clients that
`kimi-k3` accepts `low` (it accepts only `max`) and that `longcat-2.0` accepts
named levels at all. It also still cannot express `gpt-5.6-luna`'s `max`, which
is the case that exposed the gap.

**A config field carrying per-model levels**, e.g.
`thinking-levels: "*=low,medium,high; gpt-5.6-luna=low,medium,high,max"`.
Workable within the flat scalar parser, and rejected for exactly the reason
ADR-0002 rejected a hardcoded model list: it relocates the drift into an
operator's `config.yaml` and gives no signal when the provider changes. 36
models would have to be maintained by hand.

**Vendor a YAML parser so the config could nest `thinking.levels` per model, as
the `openai-compatibility` block did.** Faithful to the old shape, but the same
hand-maintenance problem plus a dependency and `.so` weight in a plugin that
currently has neither.

**Bake a snapshot of the registry into the plugin.** The exact failure ADR-0002
documents: the previous baked model list reached zero-percent accuracy while
eleven tests passed against it. The recorded payload in
`authplugin/testdata/models_dev.json` is test data, never a runtime fallback.

## Verification

Case F of the e2e harness asserts the whole path against the pinned upstream:
client → CPA → plugin → registry → `/v1/models?client_version=…`, for both the
bare and the prefixed name. The stub serves the models.dev shape itself, so the
harness stays hermetic. Forcing `Thinking` back to nil fails that case, which is
what makes it worth having.

## Notes

There is no narrower models.dev endpoint. `/api/opencode-go.json`,
`/api/opencode-go` and `/opencode-go/api.json` all 302 away; `api.json` is the
whole published API.

The plugin's own executor does not run CPA's thinking pipeline (ADR-0003 dropped
thinking-suffix handling), so today these levels are consumed as *advertised
capability* — the Codex client's `supported_reasoning_levels` and
`default_reasoning_level` — rather than as validation on an outbound request.
That is what the config entry supplied before, and it is what regressed.

`limit.context` and `limit.output` sit in the same payload and would populate
`ContextLength`/`MaxCompletionTokens`, both of which this plugin currently
reports as zero for every OpenCode Go model. Left undecoded because **nothing
consumes them today** — not because they are unavailable. The payload is already
fetched and parsed, so picking them up later is a struct field and one
assignment, with no new request and no new failure mode. Recorded on
`modelsDevModel` in `authplugin/modelmeta.go` so the next reader of the parser
finds it without coming here first.
