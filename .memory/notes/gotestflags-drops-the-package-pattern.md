---
name: gotestflags-drops-the-package-pattern
kind: gotcha
description: goTestFlags drops every non-flag argument because its caller re-supplies the package pattern, so stripping coverage flags for a variant that keeps its pattern must go through dropCoverage(args, true).
anchors:
  - path: source/cli/internal/runner/runner.go
    blob: 3a3d11a81409
confidence: verified
---

`goTestFlags` (`internal/runner/runner.go:506`, used by `GoRerun`) drops every argument not beginning with `-`, because its caller supplies the package pattern afterward. Reusing it in `buildGoTest` to strip coverage flags for Plain/BuildOnly would also drop a component's declared pattern (e.g. `./internal/...`), silently narrowing the run to the current directory — a larger silent narrowing than the coverage leak it fixes, and no test fed a pattern through Plain at the time.

The fix (lydite/lydite#212) factored the scan into `dropCoverage(args, keepPackages bool)` (`runner.go:519`) with named wrappers `goTestFlags` (`keepPackages=false`) and `goTestUninstrumented` (`:513`, `true`). It must be one pass: pairing a flag with its value (`-timeout 5m`) needs the same pass that classifies a bare word, since a later pass over the survivors cannot tell `5m` from a package pattern. `-coverpkg=X` alone enables instrumentation with no `-cover`, which is why a declared `args:` could instrument the Plain/BuildOnly variants.
