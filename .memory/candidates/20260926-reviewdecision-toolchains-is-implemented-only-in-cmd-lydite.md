---
about: reviewdecision.Toolchains has one real implementation, commandToolchains in cmd/lydite, because it wraps cmd-only helpers (ensureToolchains, childEnv, componentUnits); a stage needing it must take it as a flow input the CLI injects — relevant to moving test/mutation/scan/review onto Flow
saw:
  - source/cli/internal/reviewdecision/surface.go
  - source/cli/cmd/lydite/review_apisurface.go
  - source/cli/cmd/lydite/toolchain.go
  - source/cli/cmd/lydite/test.go
  - source/cli/internal/flows/clearance/clearance.go
  - source/cli/internal/stages/clearance/fingerprint.go
  - source/cli/cmd/lydite/clearance.go
---

`reviewdecision.Toolchains` (`internal/reviewdecision/surface.go`) is the interface an in-process
API-surface comparison provisions through: `Ensure(ctx, dir, cfg, components)` and
`CheckEnv(tc, c)`. Its only production implementation is `commandToolchains{cmd}` in
`cmd/lydite/review_apisurface.go`, which just delegates to helpers that live in `package main`:

- `ensureToolchains(ctx, cmd, dir, cfg, units)` (`cmd/lydite/toolchain.go`) — takes the
  `*cobra.Command` for its diagnostics writer and maps `.lydite/config.yml` to
  `toolchain.Overrides` via `toolchainOverrides`;
- `componentUnits` and `childEnv(tc, c, runner.Invocation{})` (`cmd/lydite/test.go`) — the same
  environment composition `test` uses.

The other implementations are test fakes (`noToolchains` in `reviewdecision/surface_test.go`,
`refusingToolchains` in `internal/stages/clearance/fake_test.go`).

Since `package main` cannot be imported at all (and the Flow layering forbids reaching into
`cmd/` anyway), a stage that needs a
comparison cannot construct one; it declares a `Toolchains reviewdecision.Toolchains` field in
its `In` and the flow binds it from an input the CLI supplies. The clearance flow is the worked
example: `InputToolchains` in `internal/flows/clearance/clearance.go`, bound into
`FingerprintIn.Toolchains` (`internal/stages/clearance/fingerprint.go`), set to
`commandToolchains{cmd}` by `runClearance` (`cmd/lydite/clearance.go`). `review.go` and
`review_compare.go` pass the same value directly.

For migrating `test`, `mutation`, `scan` or `review` onto a Flow: those commands call
`ensureToolchains`/`childEnv` directly (`test.go`, `mutation.go`, `scan.go`, `coverage.go`), so
either the same inject-from-CLI pattern is repeated per flow, or the helpers move below `cmd/`
into an internal package first (which also means `childEnv`, currently in the 2000-line
`test.go`, has to be separated from `test`'s own logic).
