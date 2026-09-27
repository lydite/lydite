---
about: the v0.3.1 migration issues (scan #272, record #273, ledger #279) describe a Service/StageComponent/Sink shape the clearance pilot never built
saw:
  - agentic/references/architecture.md
  - source/cli/internal/flow/*.go
  - source/cli/internal/flows/clearance/*.go
---

Checked while planning the `scan` migration (issue #272).

Issue #281 (the pilot issue) framed the whole project as building
"Flow/Stage/StageComponent/Context/Sink/SCMRepository/TrustedContext". Issue #272
("flow: migrate scan onto Flow") still uses that vocabulary: "each becomes a Service a
StageComponent adapts." Issue #279 talks about a future `LedgerSink` for `record`.

`grep -rn "Sink\|StageComponent" source/cli/internal/flow/*.go source/cli/internal/flows/*/*.go`
-> 0 hits. The pilot that actually landed (PR #282, `agentic/references/architecture.md`) has
no `Sink`, no `StageComponent`, no `Service` concept at all — just `Builder`/`Stage`/`Binding`/
`Policy`/`flow.Result`, four layers (CLI, flow definitions, stages, domain). PR #282's own body
confirms the simplification: "No new `.lydite-reports/clearance.json` artifact — explicitly out
of scope for this slice," i.e. no Sink was even exercised in the pilot.

So a reader planning `scan`'s migration off issue #272's text alone would be designing around a
shape (`Service`, `StageComponent`) that was proposed before the pilot and simplified away once
it landed. The authoritative shape is `agentic/references/architecture.md`, not the issue bodies
under milestone v0.3.1 — issue #280 says the same thing generically: whether/how each command
adopts Flow is "a judgment call, not a mandate," decided per command by reading the architecture
doc and the command's own shape.

Also worth noting for `scan` specifically: `agentic/references/architecture.md:5-9` frames Flow's
purpose as "a command that answers a webhook by reading a platform live, deciding something, and
writing back to the platform" — the pattern `clearance` was chosen for ("the smallest command
exercising every kind of GitHub read/write", per issue #281). `scan` does not answer a webhook and
its primary write is a local report/exit code, not a live GitHub read-decide-write cycle — the
architecture doc explicitly leaves the migration decision command-by-command rather than assuming
every command fits the pattern that motivated the pilot.
