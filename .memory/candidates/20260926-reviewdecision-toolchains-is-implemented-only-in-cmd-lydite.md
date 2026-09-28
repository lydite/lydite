---
about: toolchain provisioning and environment composition are implemented in internal/test/run (EnsureToolchains, ChildEnv), which lydite test's own stages import directly, while the clearance, review, scan and mutation flows each declare their own interface and take the implementation as a flow input the CLI injects over cmd/lydite's thin wrappers
saw:
  - source/cli/internal/test/run/run.go
  - source/cli/internal/stages/test/toolchains.go
  - source/cli/internal/flows/review/review.go
  - source/cli/cmd/lydite/toolchain.go
  - source/cli/cmd/lydite/test.go
  - source/cli/cmd/lydite/review_apisurface.go
  - source/cli/internal/reviewdecision/surface.go
  - source/cli/internal/flows/clearance/clearance.go
  - source/cli/cmd/lydite/clearance.go
  - source/cli/internal/stages/scan/toolchains.go
  - source/cli/cmd/lydite/scan.go
  - source/cli/internal/stages/mutation/mutation.go
  - source/cli/internal/flows/mutation/mutation.go
  - source/cli/cmd/lydite/mutation.go
---

The logic lives below `cmd/`: `internal/test/run`'s `EnsureToolchains`, `ChildEnv`,
`ComponentUnits`, `SplitPath` and `Env`. `cmd/lydite` keeps same-name wrappers —
`ensureToolchains` (`toolchain.go` line ~15, adapting `*cobra.Command` to its stderr writer) and
`childEnv`/`componentUnits`/`splitPath`/`env` (`test.go` lines ~734-748). `lydite test`'s own
stages call the package directly: `internal/stages/test/toolchains.go`'s `Toolchains` stage calls
`testrun.EnsureToolchains(..., testrun.ComponentUnits(in.Own))`.

The other three flows do not import `internal/test/run` for this; each stage package declares an
interface-typed field in its `In`, the flow binds it from an input, and the CLI supplies an
adapter over the `cmd/lydite` wrappers:

- clearance: `reviewdecision.Toolchains` (`internal/reviewdecision/surface.go`:
  `Ensure(ctx, dir, cfg, components)` and `CheckEnv(tc, c)`), bound from `InputToolchains` in
  `internal/flows/clearance/clearance.go` into `FingerprintIn.Toolchains`, implemented by
  `commandToolchains{cmd}` (`cmd/lydite/review_apisurface.go`) and passed by `clearance.go`.
- review: `review.go` and `review_compare.go` pass the same `commandToolchains{cmd}` as
  `reviewflow.Params.Toolchains`/`CompareParams.Toolchains` (`internal/flows/review/review.go`,
  `InputToolchains`), bound into the `surfaces` and `compare-surfaces` stages.
- scan: `scanstages.Toolchains` (only `Ensure`, taking `[]toolchain.Unit`) and
  `scanstages.Environment` in `internal/stages/scan/toolchains.go`, implemented by
  `scanToolchains{cmd}` and `scanEnvironment{}` (`cmd/lydite/scan.go` lines ~59-60).
- mutation: `mutationstages.Toolchains` (`internal/stages/mutation/mutation.go` line ~34, only
  `Ensure(ctx, dir, cfg, components)`, the same method set `commandToolchains` already has, so
  `cmd/lydite/mutation.go` passes `commandToolchains{cmd}` as `Params.Toolchains`) plus
  `mutationstages.Shape`, whose `Env` the CLI's `mutationShape` answers with `childEnv` and whose
  `Invocation`/`Lang`/`NoSuite`/`Scope` wrap the same helpers `lydite test` uses.

The test fakes are `noToolchains` (`reviewdecision/surface_test.go`), `refusingToolchains`
(`internal/stages/clearance/fake_test.go`) and `fakeToolchains`/`fakeShape`
(`internal/stages/mutation/fake_test.go`).
