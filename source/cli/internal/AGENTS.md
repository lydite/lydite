# `internal` — package to reference

Every package here has a narrative at the repository root. Read the one covering the package
you are in before changing it; the doc comment says what the package does, and the reference
says why it is that way and what breaks if it changes.

| Package | Read |
|---|---|
| `component`, `runner`, `nodedeps`, `cargotool` | [`components.md`](../../../.agents/references/components.md) |
| `compose`, `scheduler` | [`services-and-scheduling.md`](../../../.agents/references/services-and-scheduling.md) |
| `orphan`, `affected`, `pathmatch`, `gitdiff` | [`orphan-and-affected.md`](../../../.agents/references/orphan-and-affected.md) |
| `coverage`, `gitstate` | [`coverage.md`](../../../.agents/references/coverage.md) |
| `crap`, `annotation` | [`crap.md`](../../../.agents/references/crap.md) |
| `mutation` | [`mutation.md`](../../../.agents/references/mutation.md) |
| `finding` | [`findings.md`](../../../.agents/references/findings.md) |
| `ledger`, `junit` | [`quality-history.md`](../../../.agents/references/quality-history.md) |
| `typescript` | [`linters.md`](../../../.agents/references/linters.md) |
| `semgrep` | [`semgrep.md`](../../../.agents/references/semgrep.md) |
| `rust`, `golang`, `executil` | [`scanning.md`](../../../.agents/references/scanning.md) |
| `toolchain`, `download` | [`toolchains.md`](../../../.agents/references/toolchains.md) |
| `pins` | [`tool-pins.md`](../../../.agents/references/tool-pins.md) |
| `config` | [`configuration.md`](../../../.agents/references/configuration.md) |
| `ui` | [`output-grammar.md`](../../../.agents/references/output-grammar.md), and [`surface.md`](../../../.agents/references/surface.md) for the comment |
| `referral`, `clearance`, `forge` | [`referral-and-clearance.md`](../../../.agents/references/referral-and-clearance.md) |
| `threads` | [`surface.md`](../../../.agents/references/surface.md), and [`findings.md`](../../../.agents/references/findings.md) for what becomes one |

A `*-pin/` directory is a tool pin, not a component: see
[`tool-pins.md`](../../../.agents/references/tool-pins.md) before adding one.
