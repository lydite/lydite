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

The narratives live at the repository root, in `.agents/references/`. Open the one that
governs what you are about to change, before you change it.

| Read | For |
|---|---|
| [`components.md`](../../.agents/references/components.md) | `internal/component`, `internal/runner`, `internal/nodedeps`, `internal/cargotool` |
| [`services-and-scheduling.md`](../../.agents/references/services-and-scheduling.md) | `internal/compose`, `internal/scheduler` |
| [`orphan-and-affected.md`](../../.agents/references/orphan-and-affected.md) | `internal/orphan`, `internal/affected`, `internal/pathmatch`, `internal/gitdiff` |
| [`shards-and-the-fold.md`](../../.agents/references/shards-and-the-fold.md) | `test plan`, `test merge`, the conflict predicate |
| [`coverage.md`](../../.agents/references/coverage.md) | `internal/coverage`, `internal/gitstate`, the baseline, the floor, the patch gate |
| [`crap.md`](../../.agents/references/crap.md) | `internal/crap`, `internal/annotation` |
| [`mutation.md`](../../.agents/references/mutation.md) | `internal/mutation` |
| [`findings.md`](../../.agents/references/findings.md) | `internal/finding` |
| [`quality-history.md`](../../.agents/references/quality-history.md) | `internal/ledger`, `internal/junit` |
| [`scanning.md`](../../.agents/references/scanning.md) | which units each language's checks run over |
| [`linters.md`](../../.agents/references/linters.md) | `internal/typescript` |
| [`semgrep.md`](../../.agents/references/semgrep.md) | `internal/semgrep` |
| [`toolchains.md`](../../.agents/references/toolchains.md) | `internal/toolchain`, `internal/download` |
| [`tool-pins.md`](../../.agents/references/tool-pins.md) | `internal/pins`, `tools/pinsync`, every `*-pin/` manifest |
| [`configuration.md`](../../.agents/references/configuration.md) | `internal/config` |
| [`output-grammar.md`](../../.agents/references/output-grammar.md) | `internal/ui` |
| [`referral-and-clearance.md`](../../.agents/references/referral-and-clearance.md) | `internal/referral`, `internal/clearance`, `internal/forge` |
| [`surface.md`](../../.agents/references/surface.md) | `lydite publish` and the standing comment, `lydite threads` and `internal/threads` |

Conventions, boundaries and the repository layout are in the root
[`AGENTS.md`](../../AGENTS.md); the prescriptive rules are in
[`.agents/rules/`](../../.agents/rules/).
