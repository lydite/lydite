# Scan runs as eleven single-responsibility stages, each walking components in its own body

`lydite scan` is the second command moved onto the Flow architecture ADR 0059 built. Unlike
`clearance`, which resolves one comment against one pull request, a scan's whole job is to walk
every declared component — so the question this record answers is where that walk happens, given
that `internal/flow` runs a fixed, declared list of stages and cannot fan a stage out once per
component.

## Eleven stages, one responsibility each, in today's execution order

| # | Stage | Responsibility |
|---|---|---|
| 1 | `load-config` | reads `.lydite/config.yml` |
| 2 | `load-components` | reads `components.yml`, and refuses an empty declaration |
| 3 | `resolve-diff-base` | turns `--diff-base` into a SHA, or `""` for a scan of everything |
| 4 | `read-changed-lines` | the lines changed since the base, or empty without one |
| 5 | `provision-toolchains` | each scan unit's language toolchain |
| 6 | `warn-unscanned` | names source no component scans, on the injected diagnostics writer |
| 7 | `plan-components` | pure classification, in declaration order, into `[]Planned` |
| 8 | `run-checks` | each planned component's language checks, streamed as they run |
| 9 | `gate-licences` | each planned component's licence verdict |
| 10 | `semgrep` | Semgrep over the scan root, under `When(SemgrepEnabled)` |
| 11 | `secrets` | gitleaks over the scan root, under `When(SecretsEnabled)` |

`internal/flow.Run` executes a declared list of stages, one call each — it has no notion of
running one stage N times, once per item some earlier stage produced. A stage that has to act on
every declared component therefore walks `[]component.Component` or the `[]Planned` an earlier
stage produced in its own body, in declaration order, the same way `plan-components`,
`run-checks` and `gate-licences` each do. `run-checks` and `gate-licences` both return a slice the
same length as, and at the same index as, `[]Planned`: a plan entry that is not `Scan` reads back
as the zero value in each, so one component's check results or licence verdict can never be read
as another's under a shared but wrongly-offset index.

### Rejected: a stage per language

Splitting `run-checks` (and `gate-licences`) into `run-go-checks`, `run-rust-checks`, and so on was
considered, and rejected because every scanner streams its own output live to stdout — to stderr
under `--json` — in the order its component is reached. A declaration that interleaves languages
(Go, then a raw command, then Rust, then Go again) reached in one pass reproduces that interleaving
exactly; four stages each running their own language start to finish would group the stream by
language instead, however the declaration itself is ordered. One stage, one pass over the plan, is
what keeps the byte stream identical to what the command produced before this move.

### The licence gate is its own stage because its path is quiet

`gate-licences` is a twelfth candidate for `run-checks`'s single pass rather than a stage of its
own, and splitting it out is safe for a reason specific to it: every call on the licence path is
quiet. `go list`, cargo-deny and `git worktree` all run through `executil.RunQuiet*`, and
TypeScript's gate only reads files off disk — none of it writes to the stream a check's own tool
output is streamed to. Moving it to its own pass over the plan, after every component's checks
have already run, changes no streamed byte; the same argument does not hold for a check itself,
which is why checks and the licence gate are two stages and not one.

## Language facts live in the domain; dispatch lives in the stage

`scanlang.Enabled(l runner.Lang, cfg config.Config) bool` is `internal/scanlang`'s answer to
whether a language's checks are switched on, matching the shim `langEnabled` in `cmd/lydite`
keeps over it exactly — including answering `false` for a language `scanlang` has no key for.
`scanlang` stays a leaf importing only `runner` and `config`: no scanner package, so a reader that
only needs the answer does not pull the checks themselves in.

`scannerGates`, which names the gates a language's checks report their findings under, stays in
`cmd/lydite` rather than moving beside `Enabled`. Moving it would make `scanlang` import all four
scanner packages — `rust`, `typescript`, `golang`, `shell` — to read each one's `FindingGates`,
and `internal/orphan` already imports `scanlang` for `Scanned`, so `lydite test`, which imports
`internal/orphan`, would gain a transitive import of every scanner package for a function no scan
stage calls at all. `scannerGates` has exactly one caller outside `cmd/lydite`, in the quality
history command that reads a scan's findings back off a gate's name, and that caller can reach a
`cmd/lydite`-owned shim without the leaf package paying for it.

`run-checks` and `gate-licences` each hold one explicit `switch` naming every language they
handle, refusing any other:

```go
switch lang {
case runner.Go:
    return golang.Check, nil
// ...
default:
    return nil, fmt.Errorf("%q is planned for scanning and has no language checks", lang)
}
```

### Rejected: a `Scanner` interface registry

A `Scanner` interface — `Check(...) []executil.Result`, `Licence(...) LicenceVerdict` — that each
language implements once, dispatched through a map keyed by `runner.Lang`, was considered and
rejected because the four languages' calls do not share one shape to abstract over: `golang.Check`
takes a toolchain key no other language's check needs; Rust's licence check returns a
`rust.PolicySource` naming which of three documents decided its verdict, a value no other language
produces; TypeScript's licence gate reads lockfiles against the scan root and has no
`LicenceCheck` at all, because npm's lockfile states a licence outright and yarn's and pnpm's do
not; and shell has no licence gate to register. A registry built to cover the union of these would
carry fields most implementations leave unused, which is a worse account of what each language
actually does than the explicit switch it would replace.

Classification in `plan-components` checks `scanlang.Scanned` before `scanlang.Enabled`, and not
the reverse: `Enabled` answers `false` for every language it has no key for, so asking it first
would send a language lydite has no scanner for down the "switched off" branch and report "nothing
scans this" as an opt-out the repository never stated.

## Presentation stays in the CLI

Every `ui.Row` a scan renders — the unscanned rows, the off-by-default rows, the duplicate row,
the licence row and its detail, and the "ran nothing" row — is built in `cmd/lydite/scan.go`, and
so is `report()`. No stage in `internal/stages/scan` imports `internal/ui`: a stage's `Out` is
data a verdict is read back from, never a rendered row, which is what lets the same stage answer a
future caller that is not `cmd/lydite` at all. `checkLog` and `saveDocument` stay direct calls from
the CLI rather than injected interfaces, because no stage calls either of them — injecting them
would add a seam nothing below the CLI consumes.

## Packages, and what does not move

`internal/stages/scan` (package `scanstages`) holds one file per concern — `sources.go`,
`diffbase.go`, `toolchains.go`, `plan.go`, `checks.go`, `licences.go`, `rootscans.go` — mirroring
`internal/stages/clearance`'s own layout. A private helper sits beside the stage that uses it
rather than in a shared file: `labelled`, `findingsOf`, `crashesOf`, `declaredEnvNames` and
`steeringEnv` sit in `checks.go` beside `RunChecks`; `scanUnits` sits in `toolchains.go` beside
`ProvisionToolchains`; `semgrepBase` sits in `diffbase.go`, where the base it derives from is
resolved; the licence base tree sits in `licences.go` beside `GateLicences`, the only stage that
opens one.

`shortSHA` — a commit abbreviated to the length a reason names it at — gets its own private copy
in `licences.go`. The original lives in `cmd/lydite/review.go`, a file this move does not own, and
duplicating a three-line helper rather than reaching into another command's file already has a
precedent: `internal/stages/clearance/reply.go` keeps its own copy rather than importing
`review.go`'s.

The merge-base worktree does not move into `internal/licence`. That package runs no tool and reads
no manifest itself — it is the language-neutral core every language's scanner produces
`licence.Dependency` values for — and giving it a worktree and a `git` invocation would be the one
thing in it that reaches outside the process, for a concern (checking out a commit) that has
nothing to do with comparing two sets of dependency-licence pairs.

## Consequences

- **The worktree's own lifetime moves.** Before this, the merge-base worktree was opened the first
  time any component in the walk asked for a base, and closed when the command returned. Now
  `gate-licences` runs after every component's checks have already produced their rows and
  findings, so the worktree opens later in the run than before — after every check, rather than
  interleaved with them — and closes with a `defer` in `GateLicences`'s own body rather than at the
  end of the command, so a panic inside the stage still removes it.
- **A Rust component's base read is asked for under one condition, unchanged in its rendered
  output.** `rustLicence` asks for a base only when `rust.PolicyFor` answers `PolicyFromLydite`,
  which is the same condition the pre-move code already applied — `PolicyFromConsumer` gates
  absolutely, every run, so a base read for it would be a worktree and a cargo-deny invocation
  spent on a comparison its own verdict never makes. The move restates the condition; it decides
  nothing new.
- **`load-config`'s `Out` carries three flat fields — `SemgrepEnabled`, `SecretsEnabled`,
  `SemgrepConfig` — restating three fields `config.Config` already has.** A `When`/`Unless`
  condition and a `With` binding can each only reach a top-level field of a stage's `Out`, not a
  field nested inside a struct that field holds, so `semgrep` and `secrets` conditioning on
  `Config.Semgrep.Enabled` and `Config.Secrets.Enabled` directly is not an option `flow.Build`
  offers; restating each as its own bound field is what the binding can read.
- **`flow.Run` adds a cancellation check the pre-move command never had, at stage granularity.**
  The pre-move command had no explicit `ctx.Err()` check of its own anywhere in its loop over
  components; a cancelled context only stopped anything once the subprocess a check or a licence
  read was currently running noticed it through `exec.CommandContext`, and the loop moved on to
  the next component regardless, to fail fast against an already-cancelled context there too.
  `flow.Run` now checks `ctx.Err()` once before each of the eleven stages, so a run cancelled
  between two stages stops there rather than starting the next one — a coarser check than
  per-subprocess, but one degree finer than the pre-move command had.
- **Stages 1 through 5 read as generic enough to belong somewhere other than `scanstages`, and stay
  here for now.** `load-config`, `load-components`, `resolve-diff-base`, `read-changed-lines` and
  `provision-toolchains` do nothing specific to scanning — a future `test` or `mutation` flow needs
  the same configuration, the same declaration, and the same toolchain provisioning. Promoting them
  to a shared package now would collide with every other command still being moved onto Flow at the
  same time; each is named here, alongside `shortSHA`'s private copy, as a candidate to revisit once
  every command's own move has landed and the shape a shared version should take is visible from
  more than one flow.

See [`agentic/references/architecture.md`](../../agentic/references/architecture.md) for where the
scan flow sits among the four layers, and
[ADR 0068](0068-a-stages-diagnostics-are-written-as-they-arise-not-returned.md) for how a stage
here reports a warning without returning one.
