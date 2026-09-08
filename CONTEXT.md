# CPA Quota API Extension

A CLIProxyAPI plugin that observes account health and quota consumption across upstream
providers, and exposes them over a management API and panel.

## Language

### Quota windows

**Quota window**:
One provider-reported quota bucket (e.g. claude `seven_day`), carrying a used percentage
and reset timing.

**Fixed-cycle window**:
A quota window with a determinable start and reset instant. The only kind of window that
can be projected.

**Sliding window**:
A quota window measuring a trailing period with no fixed start (e.g. opencode-go
`rolling`, gemini per-model buckets). Never projected; reported as raw percentages only.
_Avoid_: rolling window (ambiguous — opencode-go names a window "rolling")

**Shared window**:
A quota window that applies to a whole account regardless of model (claude `five_hour`,
`seven_day`).
_Avoid_: unified window (Anthropic's header vocabulary, not ours)

**Scoped limit**:
A quota entry that applies to one model rather than the whole account.

**Binding window**:
The window an account is currently constrained by — the one that will run out first.

### Sources

**Poll**:
A request-triggered fetch of a provider's own quota endpoint. The only source that
yields a complete account snapshot.

**Observation**:
A quota reading harvested passively from provider response headers on live proxied
traffic. Covers shared windows only, and amends an account snapshot rather than
creating one.
_Avoid_: signal (CPA's own term for the raw headers upstream)

**Provenance**:
Whether a window's level came from a poll or an observation, and when that source was
read. Carried per window, since one account's windows can be read at different instants.

### Usage profile

**Usage profile**:
The learned distribution of a user's consumption across time buckets — *when* usage
typically happens, independent of how much.

**Bucket**:
One (day type, hour-of-day) cell of a usage profile. 48 per provider.

**Day type**:
Weekday or weekend, in the profile timezone.

**Shape vs level**:
The profile supplies the shape (when consumption happens, learned from token counts);
the provider's reported used percentage supplies the level (how much quota is gone).
Projection multiplies the two. Tokens are never used as the level.

**Per-occurrence rate**:
A bucket's tokens divided by how many times that (day type, hour) slot elapsed in the
retention window — including zero-usage slots. The projection's only shape input.
_Avoid_: weight (presentational, normalised for the heatmap)

**Profile timezone**:
The configured IANA timezone (default: server local) in which buckets and day types are
assigned.

**Token coverage**:
The fraction of a provider's usage records that carried token counts. Low coverage forces
the uniform basis so degradation is visible rather than silent.

### Projection

**Projection**:
A forecast of a fixed-cycle window's end-of-cycle usage, computed from the level so far
and the profile's expected fraction.

**Expected fraction**:
The share of a cycle's typical consumption that the profile says should have occurred by
now. Replaces elapsed wall-clock fraction in the naive method.

**Basis**:
Whether a projection used the learned profile (`profile`) or the uniform fallback
(`uniform`). Uniform makes the projection collapse exactly to the naive clock-ratio
method.

**Verdict**:
The categorical judgment on a projection: `on_track`, `tight`, or `will_exhaust`.

**Confidence**:
How much observation history backs a profile, graded `low`/`medium`/`high` by distinct
observed days — not event counts.
