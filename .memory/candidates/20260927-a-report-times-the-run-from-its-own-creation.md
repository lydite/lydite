---
about: ui.NewReport stamps the run's start when it is created, so a command that builds its Report after running a flow prints a duration of about 0.0s; create the Report before Run
saw:
  - source/cli/internal/ui/report.go
  - source/cli/cmd/lydite/review.go
---

`ui.NewReport` (`internal/ui/report.go`) sets `started: time.Now()` and the report's duration
is measured from that. A command moved onto a Flow naturally reads its stage outputs first and
builds rows afterwards, and creating the `Report` at that point makes every run report roughly
`0.0s` — nothing fails, and no test asserts the duration, so only a byte comparison of the
command's output against the pre-Flow binary shows it. `runReviewFlow` in
`cmd/lydite/review.go` creates the `Report` before `review.Run` for this reason; any other command
moved onto a Flow has to do the same.
