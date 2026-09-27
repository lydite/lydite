---
about: the scan stages' steeringEnv map is read only to decorate a warning line, never to decide pass/fail/refuse — that is deliberate, not an oversight to "harden" later
saw:
  - source/cli/internal/stages/scan/checks.go
  - docs/adr/0046-a-components-declared-environment-is-named-in-the-scan.md
---

`steeringEnv` (`internal/stages/scan/checks.go`: GOFLAGS, GOVULNDB, GOPRIVATE, RUSTFLAGS,
RUSTC_WRAPPER, CARGO_BUILD_TARGET, NODE_OPTIONS) marks a declared name in `warnDeclaredEnv`'s
stderr line with "(steers a check)". `RunChecks` calls `warnDeclaredEnv` once per `Scan` plan
entry, writing to its `Diagnostics` writer before that component's checks run. ADR 0046 rejects
turning this list into a denylist or a refused-variable gate for the same reason ADR 0020 already
rejects an allowlist of what a component's `env:` may say: the set has to be complete to be safe,
and completeness over "every environment variable that could steer
gosec/govulncheck/clippy/cargo-audit/biome/etc." is the property ADR 0018 refuses to claim
anywhere else in lydite. `declaredEnvNames` reports every declared name unconditionally,
regardless of `steeringEnv` membership — the map only adds a suffix to the string, and a name
falling off the map (or a new steering variable never being added) degrades to "unmarked but
still printed," never to "silently allowed" or "wrongly blocked." The doc comment on `steeringEnv`
says so ("Nothing reads this set to decide whether to fail, refuse or gate anything"). A future
change that makes any code path branch on `steeringEnv[k]` to decide something other than the mark
text would be reintroducing the allowlist ADR 0020/0046 both argued against.

The mark also composes with the "(overridden by the resolved toolchain)" annotation without one
clobbering the other — a name can be both a known steering variable and one the toolchain cancels.
`declaredEnvNames` builds both into one `mark` string (`"steers a check, overridden by the resolved
toolchain"`) rather than two separate appends.
