# ADR-0005: Plugin versions are independent of release tags

- **Status:** Accepted
- **Date:** 2026-09-05

## Context

Each plugin declares its own version constant:

- quota — `pluginVersion` in `types.go`
- auth — `pluginVersion` in `authplugin/main.go`
- session-cache — `pluginVersion` in `sessioncache/types.go`

These are read for registration metadata, the `/status` payload, and outbound
`User-Agent` headers.

Releases are cut as git tags (`v0.6.0`, `v0.6.1`, …) which also name the
container image. Because the constants are hand-maintained, a release can ship
an image tagged `v0.6.1` containing a plugin that reports `0.6.0`. This was
first noticed when the quota constant had drifted three patch versions behind
the tag, and it cannot be fixed by a single bump: tag `v0.6.0` was already taken,
so the commit that set the constant to `0.6.0` could only ship as `v0.6.1`,
reintroducing the mismatch immediately.

The obvious fix is to stop hardcoding the version and inject the tag at build
time via `-ldflags "-X main.pluginVersion=..."`, making disagreement impossible.
That option was considered and rejected.

## Decision

**Plugin versions are a separate axis from release tags, and are not derived
from them.**

A plugin version denotes that plugin's *feature epoch* — what its interface and
behaviour are — not which image it happens to be baked into. A plugin reporting
`0.6.0` inside an image tagged `v0.6.1` is therefore correct, not stale.

Tag injection was rejected because the three plugins are versioned
independently (`0.6.0`, `0.2.0`, `0.1.0`) while a build produces exactly one git
tag. Injecting it would collapse all three onto a single number, so the auth
plugin would jump `0.2.0` → `0.6.1` without any change to its code. That trades
an occasional cosmetic mismatch for the permanent loss of per-plugin semver.

## Consequences

- Plugin version constants are bumped deliberately, when that plugin's
  behaviour changes — not on every release.
- Plugin version and image tag will routinely differ. This is expected and
  should not be reported as a bug.
- Do not add `-ldflags -X` version injection to the Makefile or Dockerfile.
  Doing so would require changing the constants to `var`s, which additionally
  breaks `authplugin/executor.go`, where `executorUserAgent` is a `const`
  expression built from `pluginVersion`.
- There is no automated check that a constant was bumped, because there is no
  correct value to check against. Version accuracy is a review concern.
- `registration_test.go` previously asserted the registration version against a
  hardcoded `"0.6.0"`. That assertion was removed rather than repaired: it read
  `pluginRegistration()` directly rather than the decoded wire response, making
  it a tautology against the source, and `managementRegistrationResponse`
  carries no `Metadata` field to assert against instead.
