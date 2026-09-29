---
name: steeringenv-list-must-never-gate
kind: rationale
description: The scan stages' steeringEnv map is read only to decorate a warning line, never to decide pass/fail/refuse — deliberate, not an oversight to "harden" later, because completeness over "every env var that could steer a scanner" isn't a property lydite claims anywhere else.
anchors:
  - path: source/cli/internal/stages/scan/checks.go
    blob: 3e020ff657c6
  - path: docs/adr/0046-a-components-declared-environment-is-named-in-the-scan.md
    blob: d73f5cab4c9e
confidence: verified
---

`steeringEnv` (`checks.go:157`: GOFLAGS, GOVULNDB, GOPRIVATE, RUSTFLAGS, RUSTC_WRAPPER,
CARGO_BUILD_TARGET, NODE_OPTIONS) marks a declared name in `warnDeclaredEnv`'s stderr line with
"(steers a check)". ADR 0046 rejects turning this list into a denylist or a refused-variable gate,
for the same reason ADR 0020 rejects an allowlist of what a component's `env:` may say: the set has
to be complete to be safe, and completeness over "every environment variable that could steer
gosec/govulncheck/clippy/cargo-audit/biome/etc." is a property ADR 0018 refuses to claim anywhere
else. `declaredEnvNames` reports every declared name unconditionally regardless of `steeringEnv`
membership — the map only adds a suffix to the string, so a name falling off the map degrades to
"unmarked but still printed," never to "silently allowed" or "wrongly blocked." A future change
that makes any code path branch on `steeringEnv[k]` to decide something other than the mark text
would be reintroducing the allowlist ADR 0020/0046 both argued against.
</content>
