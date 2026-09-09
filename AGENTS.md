# lydite — agent guide

Lydite is a Go CLI that unifies code-quality and security scanning — SAST, SCA, linting, and
coverage gates — for Rust, TypeScript, and Go projects. It is the single entry point a developer
runs locally and CI runs identically, so "green locally" and "green in CI" can never drift apart.
It replaces per-repo ad hoc security workflows (CodeQL, standalone cargo-audit jobs, Codecov as a
blocking gate) across `wardnet`, `wardnet-cloud`, and `inforge` with one consistent pipeline.

## Rules

This module has prescriptive rules in `.agents/rules/`. **Read every file in that directory before making changes here, and follow each rule strictly.**
Each file contains one rule. New rules go in that directory — one file per rule, kebab-case filename matching the rule's intent.

## Where the detail is

This file is the map. Every deep narrative lives in `.agents/references/`, one file per
concern, and is read **on demand** — open the one that governs what you are about to
change, before you change it. Directories with their own `AGENTS.md` name their references
again, and that file arrives on its own when you read anything in that subtree.

| Read | When you are changing |
|---|---|
| [`ci.md`](.agents/references/ci.md) | anything in `.github/workflows/`, or reasoning about what blocks a merge |
| [`components.md`](.agents/references/components.md) | `.lydite/components.yml`, `internal/component`, `internal/runner`, `internal/nodedeps`, or how a suite's output is captured |
| [`services-and-scheduling.md`](.agents/references/services-and-scheduling.md) | `internal/compose` or `internal/scheduler` — the services a suite needs, and what may run beside what |
| [`orphan-and-affected.md`](.agents/references/orphan-and-affected.md) | `internal/orphan`, `internal/affected`, `internal/pathmatch` — what goes untested, and what a change could have broken |
| [`shards-and-the-fold.md`](.agents/references/shards-and-the-fold.md) | `lydite test plan`, `test merge`, the conflict predicate, or the CI matrix |
| [`scanning.md`](.agents/references/scanning.md) | `lydite scan`, or which units a language's checks run over |
| [`configuration.md`](.agents/references/configuration.md) | `.lydite/config.yml` or `internal/config` |
| [`linters.md`](.agents/references/linters.md) | `internal/typescript` — Biome, and why it is the only TypeScript linter |
| [`tool-pins.md`](.agents/references/tool-pins.md) | any pinned tool version, `internal/pins`, or `tools/pinsync` |
| [`toolchains.md`](.agents/references/toolchains.md) | `internal/toolchain` — the Go, Rust and Node runtimes lydite provisions |
| [`coverage.md`](.agents/references/coverage.md) | `internal/coverage`, `internal/gitstate`, the baseline, the floor, or the patch gate |
| [`quality-history.md`](.agents/references/quality-history.md) | `internal/ledger` or `lydite test record` |
| [`crap.md`](.agents/references/crap.md) | `internal/crap`, `internal/annotation`, or the `[lydite:exclude_from_<gate>]` grammar |
| [`findings.md`](.agents/references/findings.md) | `internal/finding` — the located claims, their fingerprints and their anchors |
| [`mutation.md`](.agents/references/mutation.md) | `internal/mutation` or `lydite mutation` |
| [`output-grammar.md`](.agents/references/output-grammar.md) | `internal/ui`, a row, a status, or the `--json` document |
| [`referral-and-clearance.md`](.agents/references/referral-and-clearance.md) | `internal/referral`, `internal/clearance`, `internal/forge`, or `.lydite/exemptions.yml` |
| [`surface.md`](.agents/references/surface.md) | `lydite publish`, the standing comment, or `source/cloud-services/pr-relay` |
| [`semgrep.md`](.agents/references/semgrep.md) | `internal/semgrep`, or `--diff-base` |
| [`actions.md`](.agents/references/actions.md) | `.github/actions/`, or anything that has to land in `lydite/actions` too |
| [`design.md`](.agents/references/design.md) | `assets/` or `docs/design/` |
| [`release-notes.md`](.agents/references/release-notes.md) | `docs/release-notes/`, or cutting a release |

The section names the prose still uses — Surface, Coverage, Components — are the rows of
this table. A cross-reference reading "see Coverage" means `coverage.md`.

Six directories carry an `AGENTS.md` of their own, each naming the references that govern
its subtree: [`source/cli`](source/cli/AGENTS.md),
[`source/cli/cmd/lydite`](source/cli/cmd/lydite/AGENTS.md),
[`source/cli/internal`](source/cli/internal/AGENTS.md),
[`source/cloud-services`](source/cloud-services/AGENTS.md), [`.github`](.github/AGENTS.md)
and [`docs/design`](docs/design/AGENTS.md).

**The scoped files are not maintained automatically.** `wrap-session-reviewer` buckets
changed files by module marker — `go.work`, a root `package.json` with workspaces, a
`Cargo.toml` `[workspace]` — and this repository has none at its root, so it resolves to
one bucket and updates this file alone. Until a marker exists, or that skill learns to
bucket on the `AGENTS.md` files already in the tree, keeping a reference current is
something you do rather than something that happens.

## Commands

```sh
go build ./...                 # build the binary
go test -race ./...            # run tests

# The shipped shape. gotreesitter embeds all 206 of its grammars unless
# `grammar_subset` turns the wildcard embed off, and each grammar_subset_<lang>
# tag turns one blob back on — 15MB against 33MB for the same binary. A build
# without them is correct and larger; a build with `grammar_subset` and a
# language's own tag missing panics at that language's first parse, which is why
# ci-test runs the suite under this exact list.
go build -tags 'grammar_subset grammar_subset_rust grammar_subset_typescript grammar_subset_tsx' ./...
golangci-lint run ./...        # lint — must be clean before a PR
go run ./cmd/lydite            # run the CLI locally
go run ./cmd/lydite review     # referral verdict for the current branch (exit 2 = refer)
go run ./cmd/lydite test --dir ../..  # run each declared component's suite and measure its coverage
go run ./cmd/lydite test --dir ../.. --no-coverage  # the fast path: no instrumentation, no coverage rows

# The Go module is rooted at source/cli/, so every command above runs from there —
# and the scan root is the repository root above it, which is where `.lydite/` is.
# A `--dir ..` names `source/`, which declares no components, and every gate there
# reports a repository it never found.

# Release build dry-run (produces dist/):
go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean
```

## Layout

```
source/cli/                       # the Go module (module path lydite/lydite); every go command runs here
source/cli/cmd/lydite/            # the lydite CLI (scan, test, review, version, update)
source/cli/internal/ui/           # the output grammar every command renders through, plus --json
                                  #   and the standing PR comment (see Surface)
source/cli/internal/referral/     # exemptions, disqualifiers, the referral decision (see Referral)
source/cli/internal/clearance/    # the comment surface and the clearance decision (see Clearance)
source/cli/internal/forge/        # the hosting platform: commit statuses, permission, comments
source/cli/internal/component/    # .lydite/components.yml: what a repo builds and tests (see Components)
source/cli/internal/orphan/       # source files under no component and no exclude (see The orphan gate)
source/cli/internal/pathmatch/    # the anchored path-pattern syntax both declarations are written in
source/cli/internal/pins/         # a tool version stated twice: the mirrors, and the drift guard
source/cli/tools/pinsync/         # writes those mirrors from the manifests Dependabot edits
source/cli/internal/runner/       # a runner name to its plain/instrumented/build-only invocations
source/cli/internal/nodedeps/     # how a JavaScript workspace's dependencies get installed
source/cli/internal/cargotool/    # pinned cargo subcommands: version parsing and the install
source/cli/internal/download/     # fetch, checksum-verify and unpack an archive safely
source/cli/internal/compose/      # the services a component's suite needs (see Services)
source/cli/internal/scheduler/    # port locks and the concurrency bound (see The scheduler),
                                  #   and the conflict predicate the planner groups shards with
source/cli/internal/affected/     # which components a change could have broken (see Affected selection)
source/cli/internal/gitdiff/      # the changed paths, and the tracked-file listing both gates read
source/cli/internal/config/       # .lydite/config.yml loading (opt-outs + pipeline shape — see Configuration)
source/cli/internal/toolchain/    # ensures the Go/Rust/Node runtime each component needs (see Toolchains)
source/cli/internal/rust/         # clippy, cargo-audit, cargo-deny
source/cli/internal/typescript/   # pinned Biome, the only TS linter (see Linters)
source/cli/internal/golang/       # gosec, govulncheck (installed into a version-keyed GOBIN dir)
source/cli/internal/semgrep/      # pinned Semgrep, installed via pipx
source/cli/internal/coverage/     # reads a component's coverage report (see Coverage)
source/cli/internal/crap/         # the CRAP index per Go function, from that report (see Complexity)
source/cli/internal/mutation/     # the mutants, the isolation strategies, and what became of each
                                  #   (see Mutation)
source/cli/internal/finding/      # the located claims a gate makes, as data: the fingerprint
                                  #   that identifies one and how precisely it reaches the
                                  #   change (see Findings). A leaf, so the report
                                  #   document and every producer can both import it
source/cli/internal/annotation/   # the exclusion declaration: the [lydite:exclude_from_<gate>]
                                  #   token for mutation, crap and coverage, and how a comment
                                  #   carrying one is read. A leaf, so referral can link it
source/cli/internal/gitstate/     # the base branch, and lydite branch read/write (see Coverage)
source/cli/internal/ledger/       # the quality history: append-only NDJSON and the daily
                                  #   projection the dashboard reads (see Quality history)
source/cli/internal/junit/        # the test counts every runner's JUnit report holds
source/cli/internal/gotool/       # the version-keyed `go install` internal/golang and
                                  #   internal/runner both provision through
source/cli/internal/executil/     # shared external-command runner every scanner package uses
source/cli/.golangci.yml          # lint config (v2 schema)
source/web/                       # the React dashboard (see ADR 0021 for the source/ root)
source/cloud-services/            # the Workers lydite operates, as one npm workspace and so one
                                  #   component: pr-relay posts the PR comment on a consumer's
                                  #   behalf, oauth-exchange is the dashboard's (see Surface)
assets/                           # the shipped logo set, plus the org avatar (see Design)
docs/design/source/               # where the brand is authored; the only live <text> in the tree
docs/design/                      # tokens, surface specs, and the reference prototypes (see Design)
docs/release-notes/               # _header.md + one <tag>.md per release that needs one (see Release notes)
docs/adr/                         # the decision record
.lydite/                          # every file that configures lydite: config.yml, components.yml,
                                  #   exemptions.yml
.goreleaser.yml                   # build/release config (v2 schema)
.gt-repo.yaml                     # repo governance; renders .github/dependabot.yml and the gt workflows
.github/workflows/                # CI stages, the release, the Workers' deploy, lydite-pr.yml —
                                  #   everything lydite says about a PR, as one run (see Surface) —
                                  #   and lydite-baseline.yml, which holds the one job that writes
                                  #   the coverage baseline, after a change has merged (see Coverage)
.github/actions/                  # the local composites both lydite workflows use, which are the
                                  #   shapes lydite/actions packages (that repository is not here)
scripts/install.sh                # curl|sh installer shipped with every release
```

- Module path: `lydite/lydite` (not `github.com/lydite/lydite` — a deliberate deviation from
  the other repos in this org, to be applied there too later; do not "fix" this back).
- `lydite` ships as a single statically-linked binary (`CGO_ENABLED=0`), built for
  linux/darwin × amd64/arm64.

## Status

All eight subcommands (`scan`, `test`, `mutation`, `review`, `publish`, `clearance`, `version`,
`update`) are fully implemented, plus `test plan`, `test merge`, `test record` and
`mutation merge` — every check
is a real tool invocation (not a stub). Every scanner pins its own tool version and installs it into
a lydite-managed cache directory rather than trusting whatever's already on the machine (see each
`internal/<lang>` package's doc comment for why). `update` follows the same pattern as `inforge`'s
self-update (checksum-verified binary replacement, refuses on dev builds, passive update nudge on
every other command). `lydite test` runs each component's suite, starting the compose services and
running the `setup`/`teardown` commands it declares, runs them concurrently under a port-aware
scheduler, and measures and gates each component's coverage. The whole gate — a baseline miss, the
base tree measured in a throwaway worktree, a regression failed, a patch failure, and the cache hit
on the next run — has been exercised end to end against a real git repository, not only a
hand-written report fixture.

`lydite publish` renders the standing pull-request comment from the documents those runs wrote,
and `source/cloud-services/pr-relay` posts it under lydite's own App with no credential in the
CI job (see Surface). The relay is written and tested and **not yet deployed**: until
`vars.LYDITE_RELAY_URL` is set, every comment goes through the `github-token` fallback, which is
a supported path rather than a temporary one.

`lydite test record` also appends this commit's scalars to the quality-history ledger on the
same branch, in the same commit — coverage, CRAP and test counts per component, with an
explicit gap record whenever the commit before it was never recorded (see Quality history
below). Mutation and finding counts are not yet collected, for the reasons ADR 0029 gives. The
findings themselves travel as data beside the rows (see Findings), which is the whole of
what a count and a per-finding history each need.
The dashboard that reads it is a later slice; `source/web/` is still empty.

`lydite coverage` is **removed**, and so are `coverage.source`, `coverage.{go,rust}.report`,
`coverage.rust.lcov`, `--source`, `--tests`, `--go-report`, `--rust-report` and
`--rust-lcov-report`. Each is rejected by name rather than ignored — including the command itself,
which answers with what replaced it rather than cobra's unknown-command message. See
[ADR 0019](docs/adr/0019-coverage-per-component-gated-by-lydite-test.md).

## Conventions

- **Version injection:** `cmd/lydite` exposes `var version = "dev"`, overridden at release via
  `-ldflags "-X main.version=<tag>"`. Keep that variable name and package stable.
- **goreleaser & golangci-lint both use the v2 config schema.** In golangci-lint v2, `gosimple` is
  part of `staticcheck` — don't add it as a separate linter (it will error).
- Lint must pass with zero issues; `errcheck` is on, so check returned errors.

## Boundaries

- **Always:** run `go build ./...`, `go test -race ./...`, and `golangci-lint run ./...` before
  proposing a PR.
- **Ask first:** changing the Go version, renaming the binary/`cmd` dir, altering the release
  archive layout, or editing CI.
- **Never:** introduce cgo, commit `dist/` or secrets, or skip the lint/test gates.

## Worktrees

This repo uses a bare-repo + typed-worktree layout managed by the `gt` CLI — one session, one
`gt wt add <type/name>` worktree; never use raw `git worktree` or edit inside `.bare/`.
