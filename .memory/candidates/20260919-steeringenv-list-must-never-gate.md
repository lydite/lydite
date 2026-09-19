---
about: cmd/lydite/scan.go's steeringEnv map is read only to decorate a warning line, never to decide pass/fail/refuse — that is deliberate, not an oversight to "harden" later
saw:
  - source/cli/cmd/lydite/scan.go
  - docs/adr/0046-a-components-declared-environment-is-named-in-the-scan.md
---

`steeringEnv` (GOFLAGS, GOVULNDB, GOPRIVATE, RUSTFLAGS, RUSTC_WRAPPER, CARGO_BUILD_TARGET,
NODE_OPTIONS) marks a declared name in `warnDeclaredEnv`'s stderr line with "(steers a check)".
ADR 0046 rejects turning this list into a denylist or a refused-variable gate for the same
reason ADR 0020 already rejects an allowlist of what a component's `env:` may say: the set has
to be complete to be safe, and completeness over "every environment variable that could steer
gosec/govulncheck/clippy/cargo-audit/eslint/etc." is the property ADR 0018 refuses to claim
anywhere else in lydite. `declaredEnvNames` reports every declared name unconditionally,
regardless of `steeringEnv` membership — the map only adds a suffix to the string, and a name
falling off the map (or a new steering variable never being added) degrades to "unmarked but
still printed," never to "silently allowed" or "wrongly blocked." A future change that makes any
code path branch on `steeringEnv[k]` to decide something other than the mark text would be
reintroducing the allowlist ADR 0020/0046 both already argued against.

The mark also has to compose with the pre-existing "(overridden by the resolved toolchain)"
annotation without one clobbering the other — a name can be both a known steering variable and
one the toolchain cancels (e.g. a hypothetical declared `GOFLAGS` that some future toolchain
composition also sets). `declaredEnvNames` builds both into one `mark` string
(`"steers a check, overridden by the resolved toolchain"`) rather than two separate appends, so
the two are always readable together on one name instead of only the second write winning.
