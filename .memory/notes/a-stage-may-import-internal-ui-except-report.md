---
name: a-stage-may-import-internal-ui-except-report
kind: rationale
description: The stage-layering rule bans ui.Report specifically, not all of internal/ui; stages already import ui for Comment, Document, Status and Row.
anchors:
  - path: source/cli/internal/stages/clearance/reply.go
    blob: a7b39fabacc1
  - path: source/cli/internal/stages/publish/build.go
    blob: c64e185569a6
  - path: source/cli/internal/ui/report.go
    blob: d562c5be7354
confidence: verified
---

`agentic/references/architecture.md` and `.claude/rules/a-stage-is-a-function-of-its-own-in-and-imports-nothing-above-it.md` say the CLI is "the only layer that imports ... `ui.Report`" — the wording names the type, not the package. `internal/ui/report.go` defines `Report` (the live per-run accumulator with unexported fields and a renderer tied to one process) beside `Document`, a deliberately separate parse-back type built for a *different* process (the one assembling the PR comment). `ui.Comment` and its section/row/detail types live in `comment.go` importing only `fmt`/`strings`; `ui.Status` and `ui.Row` in `row.go`.

Precedent: `internal/stages/clearance/reply.go:13` already imports `lydite/lydite/internal/ui` in a real stage (`DescribeClearance`, `ComposeReply`, building `ui.Comment` literals), and `internal/stages/publish/build.go:10` builds a `ui.Comment` from `ui.Document` inputs, referencing `ui.Report` nowhere. `grep -rn "internal/stages\|internal/flows\|cmd/lydite\|cobra" source/cli/internal/ui/*.go` has no hits, so `internal/ui` cannot create an import cycle with a stage or flow. A stage returning `ui.Comment`/`Document`/`Status`/`Row` is therefore not a layering violation; only `ui.Report`, cobra and CLI option structs are CLI-only.
