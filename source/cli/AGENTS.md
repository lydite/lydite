# `source/cli` — the Go module

Module path `lydite/lydite`, deliberately not `github.com/lydite/lydite`. Every `go` and
`golangci-lint` command runs from this directory, and the suite runs under the release build
tags — a language whose own `grammar_subset_<lang>` tag is missing panics at its first parse,
and a bare `go test` can never see it:

```sh
go test -race -tags 'grammar_subset grammar_subset_rust grammar_subset_typescript grammar_subset_tsx' ./...
```

The scan root is the repository root *above* this one: `lydite scan --dir ../..`. A `--dir ..`
names `source/`, which declares no components.

## Where the detail is

The narratives live at the repository root, in `agentic/references/`. Open the one that
governs what you are about to change, before you change it.

| Read | For |
|---|---|
| [`components.md`](../../agentic/references/components.md) | `internal/component`, `internal/runner`, `internal/nodedeps`, `internal/cargotool` |
| [`services-and-scheduling.md`](../../agentic/references/services-and-scheduling.md) | `internal/compose`, `internal/scheduler` |
| [`orphan-and-affected.md`](../../agentic/references/orphan-and-affected.md) | `internal/orphan`, `internal/affected`, `internal/pathmatch`, `internal/gitdiff` |
| [`shards-and-the-fold.md`](../../agentic/references/shards-and-the-fold.md) | `test plan`, `test merge`, the conflict predicate |
| [`coverage.md`](../../agentic/references/coverage.md) | `internal/coverage`, `internal/gitstate`, the baseline, the floor, the patch gate |
| [`crap.md`](../../agentic/references/crap.md) | `internal/crap`, `internal/annotation` |
| [`mutation.md`](../../agentic/references/mutation.md) | `internal/mutation` |
| [`crap.md`](../../agentic/references/crap.md), [`mutation.md`](../../agentic/references/mutation.md) | `internal/treesitter` — the shared grammar tables, function spans and test-code classification both gates read |
| [`findings.md`](../../agentic/references/findings.md) | `internal/finding` |
| [`quality-history.md`](../../agentic/references/quality-history.md) | `internal/ledger`, `internal/junit` |
| [`scanning.md`](../../agentic/references/scanning.md) | which units each language's checks run over, and `internal/secrets`, the root-scoped secret scan |
| [`linters.md`](../../agentic/references/linters.md) | `internal/typescript` |
| [`semgrep.md`](../../agentic/references/semgrep.md) | `internal/semgrep` |
| [`toolchains.md`](../../agentic/references/toolchains.md) | `internal/toolchain`, `internal/download` |
| [`tool-pins.md`](../../agentic/references/tool-pins.md) | `internal/pins`, `tools/pinsync`, every `*-pin/` manifest |
| [`configuration.md`](../../agentic/references/configuration.md) | `internal/config` |
| [`output-grammar.md`](../../agentic/references/output-grammar.md) | `internal/ui` |
| [`referral-and-clearance.md`](../../agentic/references/referral-and-clearance.md) | `internal/referral`, `internal/clearance`, `internal/forge`, `internal/depdelta`, `internal/apisurface`, `internal/rustapisurface`, `internal/tsapisurface`, `internal/declaration`, and `internal/gitstate`'s tag resolution that `lydite release check` reads |
| [`surface.md`](../../agentic/references/surface.md) | `lydite publish` and the standing comment, `lydite threads` and `internal/threads` |

Conventions, boundaries and the repository layout are in the root
[`AGENTS.md`](../../AGENTS.md); the prescriptive rules are in
[`agentic/rules/`](../../agentic/rules/).
