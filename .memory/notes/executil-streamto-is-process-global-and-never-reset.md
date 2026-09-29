---
name: executil-streamto-is-process-global-and-never-reset
kind: gotcha
description: executil.StreamTo sets a package-level writer that streamDiagnostics points at stderr under --json and nothing resets, so later commands in one test process stream wherever the last --json command left it.
anchors:
  - path: source/cli/internal/executil/executil.go
    blob: 1f8345f1508d
  - path: source/cli/cmd/lydite/root.go
    blob: c1295374a9aa
  - path: source/cli/cmd/lydite/mutation_test.go
    blob: 17a556c497c0
confidence: verified
---

`executil.StreamTo(w)` assigns the package variable `streamTarget` (default `os.Stdout`), which `Run` mirrors live command output to; its doc says to call it once from the command layer and that it is not safe to change while a command runs. `streamDiagnostics(asJSON)` (`cmd/lydite/root.go:17`) calls `executil.StreamTo(os.Stderr)` when `--json` is set and does nothing otherwise — it never sets it back. It is called at the top of `scan`, `test`, `plan`, `merge`, `mutation`, `mutation merge` and `release check`.

Fine in production (one command per process). In the `cmd/lydite` test binary, the first test running any of those commands with `--json` leaves `streamTarget` at whatever `*os.File` `os.Stderr` held then, for every later test in the process, including ones without `--json`. The only resets in `cmd/lydite` tests are the stream-probe subtests in `mutation_test.go`, which call `executil.StreamTo(os.Stdout)` before and in `t.Cleanup`. A test asserting on streamed output, or one swapping `os.Stderr` for a pipe (`capturedStderr`), must set the target itself rather than assume the default.
