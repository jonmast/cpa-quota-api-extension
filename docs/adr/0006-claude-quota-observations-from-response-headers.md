# Claude quota observations amend polled snapshots, never replace them

Claude quota today comes only from polling `https://api.anthropic.com/api/oauth/usage`,
cached for 30 minutes, so the outlook and its projections can be reading a level up to
half an hour stale. Anthropic also returns `anthropic-ratelimit-unified-*` response
headers on ordinary proxied traffic — utilization, reset, and status per shared window,
plus `representative-claim` naming the binding window — and the plugin ABI hands a plugin
the raw upstream headers (`pluginapi.ResponseInterceptRequest.ResponseHeaders`, populated
from `rawResponseHeaders` in CPA's `handlers_interceptors.go`). We decided to harvest
those headers as **observations** that amend the polled snapshot in place, under a strict
boundary: an observation owns the two shared windows' levels, their resets, and the
binding window — and nothing else.

## Considered options

- **Cache-invalidation trigger** (headers only mark the snapshot stale, forcing an early
  re-poll) — rejected once the header set turned out to be rich rather than coarse. It
  spends a real upstream request to re-derive numbers the account was already handed, and
  it cannot help an account that has never been polled.
- **Observations create snapshot entries** — rejected: a synthesized entry is a second,
  structurally different account shape that every client must handle, and it exists only
  in the narrow window before the first poll, which the observed traffic itself triggers.
  Observations amend only.
- **Mapping the scoped header namespaces** (`7d_oi`, `7d_sonnet`, `7d_opus`) onto the
  polled `limits[]` model entries — rejected: nothing reliably associates a header
  namespace with a model identifier, and CPA itself treats `7d_oi` as an opaque
  model-scoped rejection signal. Guessing would corrupt per-model data that polling
  currently gets right.
- **Runtime detection of the utilization scale** (treat `>1.0` as percent, `<=1.0` as
  fraction) — rejected as actively dangerous: it reads a genuine 0.8% as 80% and would
  fire a `will_exhaust` verdict on an idle account. The scale is settled by one empirical
  capture, pinned as a fixture, with out-of-band values discarded rather than clamped.
- **Codex observations** — out of scope by choice, not by difficulty. CPA harvests
  `x-codex-*` headers too, but this deployment does not use codex.

## Consequences

- This is the first thing in the plugin to run on the request hot path. It is gated by a
  config toggle and bounded by a per-account cooldown, so cost is fixed regardless of
  traffic volume; observations are therefore sampled, not complete — acceptable because
  utilization moves slowly within a window.
- Observations reach the plugin only on **successful** responses: every
  `applyResponseInterceptors` call site passes a hardcoded `http.StatusOK`. Rate-limit
  watermarks and `Retry-After` are consequently invisible to this path, and health
  classification keeps its existing sources.
- A window's level and its account's extra-usage credits can now come from different
  instants, so provenance (source + timestamp) is carried **per window** rather than
  relying on the account-level `fetched_at`.
- Projections and verdicts now change without a poll having happened, breaking any
  assumption that a snapshot change implies a poll.
- Observations are in-memory only, alongside the snapshot they amend, which does not
  survive a restart either.
