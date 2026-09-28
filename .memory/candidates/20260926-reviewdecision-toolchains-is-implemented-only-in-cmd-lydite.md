---
about: toolchain provisioning and environment composition are implemented only in cmd/lydite (ensureToolchains, childEnv), so every flow that needs them declares its own interface and takes the implementation as a flow input the CLI injects — clearance and review via reviewdecision.Toolchains, scan via scanstages.Toolchains/Environment
saw:
  - source/cli/internal/reviewdecision/surface.go
  - source/cli/cmd/lydite/review_apisurface.go
  - source/cli/internal/flows/review/review.go
  - source/cli/cmd/lydite/toolchain.go
  - source/cli/cmd/lydite/test.go
  - source/cli/internal/flows/clearance/clearance.go
  - source/cli/internal/stages/clearance/fingerprint.go
  - source/cli/cmd/lydite/clearance.go
  - source/cli/internal/stages/scan/toolchains.go
  - source/cli/internal/flows/scan/scan.go
  - source/cli/cmd/lydite/scan.go
---

`reviewdecision.Toolchains` (`internal/reviewdecision/surface.go`) is the interface an in-process
API-surface comparison provisions through: `Ensure(ctx, dir, cfg, components)` and
`CheckEnv(tc, c)`. Its only production implementation is `commandToolchains{cmd}` in
`cmd/lydite/review_apisurface.go`, which delegates to helpers that live in `package main`:

- `ensureToolchains(ctx, cmd, dir, cfg, units)` (`cmd/lydite/toolchain.go`) — takes the
  `*cobra.Command` for its diagnostics writer and maps `.lydite/config.yml` to
  `toolchain.Overrides` via `toolchainOverrides`;
- `componentUnits` and `childEnv(tc, c, runner.Invocation{})` (`cmd/lydite/test.go`) — the same
  environment composition `test` uses.

`package main` cannot be imported, and the Flow layering forbids stages reaching into `cmd/`
anyway, so a stage that needs provisioning declares an interface-typed field in its `In` and the
flow binds it from an input the CLI supplies. Three flows do this, with two interfaces:

- clearance: `InputToolchains` in `internal/flows/clearance/clearance.go` → `FingerprintIn.Toolchains`
  (`internal/stages/clearance/fingerprint.go`, type `reviewdecision.Toolchains`), set to
  `commandToolchains{cmd}` by `runClearance` (`cmd/lydite/clearance.go`).
- review: `review.go` and `review_compare.go` set the same `commandToolchains{cmd}` as
  `reviewflow.Params.Toolchains`/`CompareParams.Toolchains` (`internal/flows/review`), bound into
  the `surfaces` and `compare-surfaces` stages (`internal/stages/review`).
- scan: `scanstages.Toolchains` (only `Ensure`, taking `[]toolchain.Unit`) and
  `scanstages.Environment` (`Compose` and `Declared`) in `internal/stages/scan/toolchains.go`,
  bound from `InputToolchains`/`InputEnvironment` in `internal/flows/scan/scan.go`, implemented by
  `scanToolchains{cmd}` and `scanEnvironment{}` in `cmd/lydite/scan.go` — which wrap the same
  `ensureToolchains`, `childEnv`, `splitPath` and `env` helpers.

`test`, `mutation` and `coverage` still call `ensureToolchains`/`childEnv` directly (`test.go`,
`mutation.go`, `coverage.go`). Migrating them onto a Flow means either a third per-flow adapter
over the same helpers, or moving the helpers below `cmd/` into an internal package first (which
also means `childEnv`, currently in the ~2300-line `test.go`, has to be separated from `test`'s own
logic). The test fakes are `noToolchains` (`reviewdecision/surface_test.go`) and
`refusingToolchains` (`internal/stages/clearance/fake_test.go`).
