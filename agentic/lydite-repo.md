# lydite — agent guide

Lydite is a Go CLI that unifies code-quality and security scanning — SAST, SCA, linting, and
coverage gates — for Rust, TypeScript, and Go projects. It is the single entry point a developer
runs locally and CI runs identically, so "green locally" and "green in CI" can never drift apart.
It replaces per-repo ad hoc security workflows (CodeQL, standalone cargo-audit jobs, Codecov as a
blocking gate) across `wardnet`, `wardnet-cloud`, and `inforge` with one consistent pipeline.

## Rules

This module has prescriptive rules in `agentic/rules/`. **Read every file in that directory before making changes here, and follow each rule strictly.**
Each file contains one rule. New rules go in that directory — one file per rule, kebab-case filename matching the rule's intent.

## Where the detail is

This file is the map. Every deep narrative lives in `agentic/references/`, one file per
concern, and is read **on demand** — open the one that governs what you are about to
change, before you change it. Directories with their own `AGENTS.md` name their references
again, and that file arrives on its own when you read anything in that subtree.

| Read | When you are changing |
|---|---|
| [`layout.md`](agentic/references/layout.md) | anything — the annotated directory tree, and which reference governs each package |
| [`ci.md`](agentic/references/ci.md) | anything in `.github/workflows/`, or reasoning about what blocks a merge |
| [`components.md`](agentic/references/components.md) | `.lydite/components.yml`, `internal/component`, `internal/runner`, `internal/nodedeps`, or how a suite's output is captured |
| [`services-and-scheduling.md`](agentic/references/services-and-scheduling.md) | `internal/compose` or `internal/scheduler` — the services a suite needs, and what may run beside what |
| [`orphan-and-affected.md`](agentic/references/orphan-and-affected.md) | `internal/orphan`, `internal/affected`, `internal/pathmatch` — what goes untested, and what a change could have broken |
| [`shards-and-the-fold.md`](agentic/references/shards-and-the-fold.md) | `lydite test plan`, `test merge`, the conflict predicate, or the CI matrix |
| [`scanning.md`](agentic/references/scanning.md) | `lydite scan`, or which units a language's checks run over |
| [`configuration.md`](agentic/references/configuration.md) | `.lydite/config.yml` or `internal/config` |
| [`linters.md`](agentic/references/linters.md) | `internal/typescript` — Biome, and why it is the only TypeScript linter |
| [`tool-pins.md`](agentic/references/tool-pins.md) | any pinned tool version, `internal/pins`, or `tools/pinsync` |
| [`toolchains.md`](agentic/references/toolchains.md) | `internal/toolchain` — the Go, Rust and Node runtimes lydite provisions |
| [`coverage.md`](agentic/references/coverage.md) | `internal/coverage`, `internal/gitstate`, the baseline, the floor, or the patch gate |
| [`quality-history.md`](agentic/references/quality-history.md) | `internal/ledger` or `lydite test record` |
| [`crap.md`](agentic/references/crap.md) | `internal/crap`, `internal/annotation`, or the `[lydite:exclude_from_<gate>]` grammar |
| [`findings.md`](agentic/references/findings.md) | `internal/finding` — the located claims, their fingerprints and their anchors |
| [`mutation.md`](agentic/references/mutation.md) | `internal/mutation` or `lydite mutation` |
| [`output-grammar.md`](agentic/references/output-grammar.md) | `internal/ui`, a row, a status, or the `--json` document |
| [`referral-and-clearance.md`](agentic/references/referral-and-clearance.md) | `internal/referral`, `internal/clearance`, `internal/forge`, or `.lydite/exemptions.yml` |
| [`surface.md`](agentic/references/surface.md) | `lydite publish`, the standing comment, `lydite threads`, `internal/threads`, or `source/cloud-services/pr-relay` |
| [`semgrep.md`](agentic/references/semgrep.md) | `internal/semgrep`, or `--diff-base` |
| [`actions.md`](agentic/references/actions.md) | `.github/actions/`, or anything that has to land in `lydite/actions` too |
| [`design.md`](agentic/references/design.md) | `assets/` or `docs/design/` |
| [`release-notes.md`](agentic/references/release-notes.md) | `docs/release-notes/`, or cutting a release |

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

# The shipped shape (see mutation.md for why: gotreesitter's grammar_subset tags).
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
source/cli/          # the Go module (module path lydite/lydite); every go command runs here
source/web/          # the React dashboard (see ADR 0021 for the source/ root) — still empty
source/cloud-services/  # the Workers lydite operates: pr-relay, oauth-exchange (see surface.md)
assets/, docs/design/   # the shipped logo set and the brand (see design.md)
docs/adr/, docs/release-notes/  # the decision record; the release notes
.lydite/              # config.yml, components.yml, exemptions.yml
.goreleaser.yml, .gt-repo.yaml   # build/release config; repo governance
.github/workflows/, .github/actions/  # CI stages and the composites (see ci.md, actions.md)
scripts/install.sh    # curl|sh installer shipped with every release
```

See [`layout.md`](agentic/references/layout.md) for the full annotated tree — every
`internal/` package and which reference governs it.

## Status

All nine subcommands (`scan`, `test`, `mutation`, `review`, `publish`, `threads`, `clearance`,
`version`, `update`) are fully implemented, plus `test plan`, `test merge`, `test record` and
`mutation merge` — every check is a real tool invocation, not a stub. `lydite coverage` is
**removed** (see [`coverage.md`](agentic/references/coverage.md)); the relay in
`source/cloud-services/pr-relay` is deployed and live (see
[`surface.md`](agentic/references/surface.md)); the quality-history dashboard is a later
slice and `source/web/` is still empty (see
[`quality-history.md`](agentic/references/quality-history.md)).

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
