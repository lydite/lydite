---
name: ui-report-times-the-run-from-its-own-creation
kind: gotcha
description: ui.NewReport stamps the run's start when created, so a command building its Report after running a flow prints about 0.0s; create it before Run, as runReviewFlow does.
anchors:
  - path: source/cli/internal/ui/report.go
    blob: d562c5be7354
  - path: source/cli/cmd/lydite/review.go
    blob: 66301c7db2ab
confidence: verified
---

`ui.NewReport` (`internal/ui/report.go:48`) sets `started: time.Now()`, and the report's duration (`:165`, `:273`) is measured from that. A command moved onto a Flow naturally reads its stage outputs first and builds rows afterwards; creating the `Report` at that point makes every run report roughly `0.0s`. Nothing fails and no test asserts the duration, so only a byte comparison of the output against the pre-Flow binary shows it. `runReviewFlow` in `cmd/lydite/review.go` creates the `Report` (`ui.NewReport("review")`, line 128) before `review.Run` for this reason; any other command moved onto a Flow has to do the same.
