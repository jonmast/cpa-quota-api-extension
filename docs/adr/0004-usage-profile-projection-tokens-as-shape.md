# Usage-profile quota projection: tokens as shape, not level

Provider APIs report a quota window's `used_percent` but nothing about pace, and naive
projection (`used / elapsed_wall_clock`) is badly wrong for a schedule-driven user whose
consumption is concentrated in working hours (~4% weekend share vs the 28.6% a clock
assumes). We decided to learn a **usage profile** — per-provider token consumption
aggregated into 48 buckets (hour-of-day × weekday/weekend, in a configurable timezone
defaulting to server local) — and project fixed-cycle windows as
`used × (expected cycle total) / (expected consumption so far)`, where expectations come
from the profile. Token counts (already present in the host's usage payload, currently
discarded by the plugin) supply only the **shape**; the provider-reported `used_percent`
remains the **level**. Any roughly-constant tokens→quota factor cancels in the ratio.

## Considered options

- **Linear regression on quota samples** — rejected: assumes a constant burn rate
  (precisely the wrong assumption here), cannot cross window-reset discontinuities, and
  samples are not evenly spaced.
- **Tokens as the level** (projecting token totals directly against a token budget) —
  rejected: reintroduces per-model token→quota weighting error. Tokens are trustworthy
  for *when*, not *how much*.
- **Display weights as projection input** — rejected: a bucket's raw `token_sum`
  accumulates ~22 weekday vs ~8 weekend days of history, and a weekly cycle then visits
  weekday buckets 5× vs weekend 2×; summing raw weights over calendar hours compounds
  both imbalances (~2.4× weekday over-weighting), landing the error exactly on the
  weekend-share signal the feature exists to capture. Projection instead uses the
  **per-occurrence rate** `token_sum / calendar_occurrences` walked over the cycle's
  actual calendar hours. The 1.0-normalised `weight` in `/v1/profile` is presentational
  only.
- **In-place 48-row accumulation** — rejected: unbounded history means a schedule change
  takes months to wash out, contradicting the ~30-day noise arithmetic that justified
  48 buckets. Storage is **day-grain rows** `(provider, auth_index, day_type, hour,
  date)` pruned at ~30 days; profiles roll up to provider level at read time.

## Consequences

- Cold start uses uniform weights, collapsing the formula exactly to the naive
  clock-ratio method — no separate insufficient-data code path; surfaced as
  `basis: "uniform" | "profile"`.
- Only fixed-cycle windows (derivable start) are projected; sliding windows and
  credit pools stay raw percentages. Missing `WindowSeconds` for claude/copilot windows
  are filled at the fetcher, where the durations are known facts.
- Shape signal is the uncached token sum (input + output + reasoning; cache counters
  excluded) because cache-hit rate plausibly varies by hour — the one systematic way the
  tokens→quota factor could drift intra-day. Providers whose records lack token detail
  are forced to `basis: "uniform"` via a token-coverage guard rather than silently
  degrading.
