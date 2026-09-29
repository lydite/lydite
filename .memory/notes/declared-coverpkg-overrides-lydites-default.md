---
name: declared-coverpkg-overrides-lydites-default
kind: gotcha
description: buildGoTest places lydite's -coverpkg before the component's args and go test honours the last repeat, so a declared -coverpkg silently narrows coverage; GoRerun orders the opposite way on purpose.
anchors:
  - path: source/cli/internal/runner/runner.go
    blob: 3a3d11a81409
confidence: verified
---

`buildGoTest` (`internal/runner/runner.go`) puts lydite's own `-coverprofile` and `-coverpkg=./...` **before** the component's declared `args:`. `go test` honours the *last* occurrence of a repeated flag (checked empirically with two `-coverpkg` flags), so a component declaring its own `-coverpkg=...` silently overrides lydite's default and narrows the package set coverage is measured against — no schema key, no error.

This is the opposite choice from `GoRerun` (same file, `:465`), which appends its `-run`/`-count=1` **after** declared args so a declared duplicate cannot win: there the filter is the point of the invocation. For coverage the component is better placed to know the scope. Tests pinning it: `TestGoInstrumentedCarriesCoverpkg` and `TestGoInstrumentedLetsADeclaredCoverpkgWin` in `runner_test.go`. Because internal/runner cannot import internal/component (the reverse import exists), a `coverpkg:` schema key would need a `Runner.Build` signature change, which is why `args:` stayed the mechanism (lydite/lydite#185).
