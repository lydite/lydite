---
about: the stage-layering rule bans ui.Report specifically, not all of internal/ui, and a stage already imports internal/ui in production code
saw:
  - agentic/references/architecture.md
  - .claude/rules/a-stage-is-a-function-of-its-own-in-and-imports-nothing-above-it.md
  - source/cli/internal/stages/clearance/reply.go
  - source/cli/internal/ui/report.go
  - source/cli/internal/ui/comment.go
  - source/cli/internal/stages/publish/build.go
targets: a-stage-is-a-function-of-its-own-in-and-imports-nothing-above-it
verdict: still-true
---

Question: is a stage that builds `ui.Comment`/`ui.Document` a layering violation under
the rule "nothing below the CLI imports ... `internal/ui`'s report type"?

Both `architecture.md:17,28` and the rule file
(`.claude/rules/a-stage-is-a-function-of-its-own-in-and-imports-nothing-above-it.md:8`) say
the CLI is "the only layer that imports ... `ui.Report`" — the wording names the `Report`
type specifically, not the whole package. `internal/ui/report.go:36` defines `Report` as the
live per-run accumulator with unexported fields and a text/JSON renderer tied to one process's
run; `internal/ui/report.go:213` defines `Document` right next to it as a deliberately
separate, parse-back type ("a report read back ... the only supported way into one") built
for a *different* process (the one assembling the PR comment) to consume. `ui.Comment` and
its `Section`/`Row`/`Detail` types live in `comment.go`, importing only `fmt`/`strings` — no
cobra, no CLI type, no `Report`. `ui.Status` and `ui.Row` live in `row.go`, importing only
`strings`/`unicode/utf8`.

Confirming precedent: `source/cli/internal/stages/clearance/reply.go:13` already imports
`lydite/lydite/internal/ui` in a real stage (`clearancestages.DescribeClearance`,
`ComposeReply`), builds `ui.Comment{...}` literals with `ui.VerdictPass`/`ui.VerdictRefer`,
`ui.CommentSection`, `ui.CommentDetail`, and calls `.Render()` — this already shipped and is
not flagged as a violation.

`grep -rn "internal/stages\|internal/flows\|cmd/lydite\|cobra" source/cli/internal/ui/*.go`
-> 0 hits, so `internal/ui` cannot create an import cycle with a stage or flow package.

So: a stage returning `ui.Comment`/`ui.Document`/`ui.Status`/`ui.Row` is not a layering
violation. Only `ui.Report` (and cobra, and a CLI options struct) is CLI-only.
`internal/stages/publish` (`build.go:10`, `gather.go:19`, `write.go:9`) is the second
precedent: it builds a `ui.Comment` from `ui.Document` inputs and references `ui.Report`
nowhere.
