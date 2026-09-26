# `internal` — package to reference

Every package here has a narrative at the repository root. Read the one covering the package
you are in before changing it; the doc comment says what the package does, and the reference
says why it is that way and what breaks if it changes.

| Package | Read |
|---|---|
| `flow`, `stages/*`, `flows/*`, `trust` | [`architecture.md`](../../../agentic/references/architecture.md) |
| `component`, `runner`, `nodedeps`, `cargotool` | [`components.md`](../../../agentic/references/components.md) |
| `compose`, `scheduler` | [`services-and-scheduling.md`](../../../agentic/references/services-and-scheduling.md) |
| `orphan`, `affected`, `pathmatch`, `gitdiff` | [`orphan-and-affected.md`](../../../agentic/references/orphan-and-affected.md) |
| `coverage`, `gitstate` | [`coverage.md`](../../../agentic/references/coverage.md), and [`mutation.md`](../../../agentic/references/mutation.md) for `gitstate`'s `--base-sha` revision resolution; [`referral-and-clearance.md`](../../../agentic/references/referral-and-clearance.md) and [`ci.md`](../../../agentic/references/ci.md) for `gitstate`'s `Tags`/`PreviousTag`, which `lydite release check` reads |
| `crap`, `annotation` | [`crap.md`](../../../agentic/references/crap.md) |
| `mutation` | [`mutation.md`](../../../agentic/references/mutation.md) |
| `treesitter` | [`crap.md`](../../../agentic/references/crap.md) and [`mutation.md`](../../../agentic/references/mutation.md) for the shared grammar tables, function spans and test-code classification both gates read; [`coverage.md`](../../../agentic/references/coverage.md) for exclusion resolution |
| `finding` | [`findings.md`](../../../agentic/references/findings.md) |
| `ledger`, `junit` | [`quality-history.md`](../../../agentic/references/quality-history.md) |
| `typescript` | [`linters.md`](../../../agentic/references/linters.md) |
| `semgrep` | [`semgrep.md`](../../../agentic/references/semgrep.md) |
| `rust`, `golang`, `shell`, `executil` | [`scanning.md`](../../../agentic/references/scanning.md) |
| `licence` | [`scanning.md`](../../../agentic/references/scanning.md) for the merge-base delta, and [`findings.md`](../../../agentic/references/findings.md) for how a pair is identified |
| `toolchain`, `download` | [`toolchains.md`](../../../agentic/references/toolchains.md) |
| `pins` | [`tool-pins.md`](../../../agentic/references/tool-pins.md) |
| `config` | [`configuration.md`](../../../agentic/references/configuration.md) |
| `secrets` | [`scanning.md`](../../../agentic/references/scanning.md), and [`tool-pins.md`](../../../agentic/references/tool-pins.md) for the gitleaks pin |
| `ui` | [`output-grammar.md`](../../../agentic/references/output-grammar.md), and [`surface.md`](../../../agentic/references/surface.md) for the comment |
| `referral`, `clearance`, `forge`, `depdelta`, `apisurface`, `rustapisurface`, `tsapisurface`, `declaration`, `reviewdecision` | [`referral-and-clearance.md`](../../../agentic/references/referral-and-clearance.md) |
| `threads` | [`surface.md`](../../../agentic/references/surface.md), and [`findings.md`](../../../agentic/references/findings.md) for what becomes one |

A `*-pin/` directory is a tool pin, not a component: see
[`tool-pins.md`](../../../agentic/references/tool-pins.md) before adding one.
