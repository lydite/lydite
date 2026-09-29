---
about: line numbers moved in prepare.go and the two-requirement/mutation-worker claims still hold
saw:
  - source/cli/internal/test/run/prepare.go
  - source/cli/internal/toolchain/require.go
  - source/cli/internal/stages/mutation/run.go
targets: command-component-joins-node-install-only-with-its-own-package-json
verdict: still-true
---
Claim holds (prepare.go:110-119, `package.json` stat in the component's own dir). Pointers moved: prepareCommand is now :87 (not :61), installsNodeDeps :110 (not :84), installNote :154 (not :128). installNote still keys off installsNodeDeps (:156). Also `refusedManager` (:62) now guards the same predicate.
