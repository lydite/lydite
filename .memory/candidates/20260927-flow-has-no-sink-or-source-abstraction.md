---
about: internal/flow has no Sink or Source type; reading and writing the platform (or a local file) are both just ordinary stages
saw:
  - source/cli/internal/flow/flow.go
  - source/cli/internal/flow/run.go
  - source/cli/internal/stages/scm/scm.go
  - source/cli/internal/stages/clearance/statuses.go
  - agentic/references/architecture.md
  - docs/adr/0059-a-flow-is-a-hand-rolled-engine-of-typed-bindings-not-a-pipeline-library-or-a-shared-context.md
  - docs/adr/0060-stages-read-and-write-the-platform-through-an-scmrepository-interface-ahead-of-a-second-vendor.md
---

Checked while scoping a migration of `publish` onto Flow, on the premise that the clearance
pilot introduced named "Sink" types (`SCMSink`, `JsonFileSink`) for writing.

`grep -rn "Sink" internal/ cmd/` (source/cli) returns nothing. Neither `architecture.md` nor
ADRs 0059/0060/0061 use the word "Sink" or "Source" at all.

What actually exists: every stage, read or write, is the same shape —
`func(context.Context, In) (Out, error)` — and there is no separate category for one that
happens to do I/O. `scmstages.LoadComment`/`ResolveHead`/`CheckPermission`/`ReadStatus` read
through `forge.SCMRepository`; `scmstages.PostComment` and `clearancestages.RecordStatuses`
write through the same interface; `clearancestages.RenderStatuses`
(`source/cli/internal/stages/clearance/statuses.go:71`) writes two JSON documents to local
paths via `forge.WriteStatus`, with no different treatment from any other stage — it is bound
into the flow with ordinary `With(...)`/`When(...)` calls exactly like `Decide` or
`Fingerprint`. ADR 0060 explains why *reading and writing the platform* both go through one
`forge.SCMRepository` interface (8 methods, both directions) rather than through separate
reader/writer abstractions — the interface is the test seam, not a read/write split.

So there is no gap to fill for "reading" — a reading stage is just a stage whose `Out` happens
to come from an interface call rather than a pure computation. A future migration (of
`publish` or anything else) that wants a `Source`/`Sink` vocabulary would be inventing a
distinction the architecture deliberately does not draw; the existing shape is "stage that does
I/O" vs "stage that doesn't," with no separate engine-level type for either.
