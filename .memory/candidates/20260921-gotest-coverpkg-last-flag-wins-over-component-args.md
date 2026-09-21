---
about: internal/runner buildGoTest flag ordering
saw: source/cli/internal/runner/runner.go (buildGoTest, ~line 305-320)
---

`buildGoTest` places lydite's own `-coverprofile` and `-coverpkg=./...` **before** the
component's declared `args:` in the built `go test` invocation. `go test` honours the *last*
occurrence of a repeated flag, so a component that declares its own `-coverpkg=...` in `args:`
silently overrides lydite's default and narrows the package set its coverage is measured
against — no schema key, no error, no rejection needed. This was verified empirically (running
`go test` with two `-coverpkg` flags), not by reading `cmd/go`'s flag parser.

This is the opposite ordering choice from `GoRerun` (same file), which appends its own
`-run`/`-count=1` **after** declared args specifically so a declared duplicate cannot win —
there the filter is the point of the invocation and a component may not overrule it.
`buildGoTest`'s ordering is the opposite because the coverage scope is something the component
is better positioned to know than lydite is.

Tests pinning this: `TestGoInstrumentedCarriesCoverpkg` and
`TestGoInstrumentedLetsADeclaredCoverpkgWin` in `runner_test.go`.
