---
about: a raw command: component that joins nodedeps.Install gets no provisioned pnpm/yarn on PATH, because toolchain units are derived from runner-implied language only
saw:
  - source/cli/internal/test/run/env.go
  - source/cli/internal/test/run/prepare.go
  - source/cli/internal/toolchain/require.go
  - source/cli/internal/toolchain/toolchain.go
  - source/cli/internal/nodedeps/nodedeps.go
  - source/cli/internal/stages/mutation/run.go
---
Chain, each step read at HEAD 85d10d1 (= v0.4.0):

1. `run.ComponentUnits` (env.go:114-124) skips any component whose `measure.LangOf(c)` is "" (measure.go:199: only `runner.Lookup(c.Runner)` yields a Lang). A `command:` component (no runner) yields no `toolchain.Unit`.
2. `toolchain.Requirements` (require.go:122-181) only builds Node + package-manager requirements per Unit; the manager requirement (`managerRequirement`, require.go:201) is emitted only inside the `u.Lang == TypeScript` branch (require.go:168-178). So no Node/pnpm is resolved or provisioned for a command component.
3. `Envs.For(name)` (toolchain.go:212) returns nil for it; `run.go:306` passes that nil `*Env` to `runComponent` -> `Prepare`. `ChildEnv(nil, ...)` (env.go:52-59) and `(*Env)(nil).Environ()` (toolchain.go:120) add no PathDirs.
4. `prepareCommand` (prepare.go:87) still calls `nodedeps.Install` when `installsNodeDeps` (prepare.go:110) holds (own-dir package.json). `nodedeps.Install` (nodedeps.go:398) checks `managerOnPath` (nodedeps.go:319) against `env.Check`'s PATH, so a pnpm-pinned workspace only works for a command component when an ambient pnpm exists; otherwise "pnpm: not on PATH - lydite could not provision it". Runner-based components (vitest/jest) get the pinned pnpm alongside Node via `alongside` and are fine.
5. Also `refusedManager` (prepare.go:62) does apply to command components (pnpm < 12), but nothing provisions the pin for them.

Mutation never reaches this: `prepareTarget` stops any `len(c.Command) > 0 || lang == ""` component at `KindRawCommand` (stages/mutation/run.go:353-354) before any worker or Prepare exists. The `""`-root worker call is run.go:389; the baseline call passes `in.Dir` (run.go:451). `prepareCommand` has no `declarationRoot` fallback (unlike `runner.installNodeDeps`, runner.go:929-934), so it is only safe today because mutation never gets there.

Scan side: `scanUnits` (stages/scan/toolchains.go:81) uses ScanLang, so a command component with `lang: typescript` gets Node+pm requirements in scan, but scan never installs node deps (see note scan-never-installs-nodejs-dependencies). `lydite test` uses ComponentUnits (no DeclaredLang), so a `command:` + `lang: typescript` component still gets nothing in test.

git log: no commit addresses this. Relevant history: bee8ca8 (#250 package manager provisioned), 9a81b3a (#255 override coalesces on root), 54bf1b1 (#302 pnpm 12+ native), a732154 (#303 failed probe = failed provision). `git log --grep=command --grep=raw` finds only an unrelated brand commit.

Nearest tests: prepare/install (internal/test/run/run_test.go) TestACommandComponentWhoseInstallFailsDoesNotRunItsSuite:135, TestNoInstallRowWhereThereIsNothingToSayAboutOne:154, TestInstallsNodeDepsAnswersFalseWithNeitherRunnerNorCommand:238, helper nodeCommandComponent:100. Provisioning: internal/toolchain/provision_test.go TestEnsureProvisionsThePinnedManagerAlongsideNode:550, TestAlongsideLeavesTheSharedRuntimeUntouched:702.
