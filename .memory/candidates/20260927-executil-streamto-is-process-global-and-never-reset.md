---
about: executil.StreamTo sets a package-level writer; cmd/lydite's streamDiagnostics sets it to os.Stderr under --json and nothing resets it, so in one test process every later command streams tool output wherever the last --json command pointed it
saw:
  - source/cli/internal/executil/executil.go
  - source/cli/cmd/lydite/root.go
  - source/cli/cmd/lydite/mutation_test.go
  - source/cli/internal/stages/scan/checks_test.go
---

`executil.StreamTo(w)` assigns the package variable `streamTarget` (default `os.Stdout`), which
`Run` mirrors live command output to; its doc says to call it once from the command layer and that
it is not safe to change while a command runs. `streamDiagnostics(asJSON)` (`cmd/lydite/root.go`)
calls `executil.StreamTo(os.Stderr)` when `--json` is set and does nothing otherwise — it never
sets it back to stdout. It is called at the top of `scan`, `test`, `plan`, `merge`, `mutation`,
`mutation merge` and `release check`.

In production that is fine (one command per process). In the `cmd/lydite` test binary, the first
test that runs any of those commands with `--json` leaves `streamTarget` pointing at whatever
`*os.File` `os.Stderr` held at that moment, for every later test in the process, including ones run
without `--json`. The only resets in `cmd/lydite` tests are the stream-probe subtests in
`mutation_test.go`, which call `executil.StreamTo(os.Stdout)` before and in `t.Cleanup` after.
`internal/executil/executil_test.go` and `internal/stages/scan/checks_test.go` also restore
`os.Stdout` in `t.Cleanup`, but those are separate test binaries. A test asserting on streamed
output, or one that swaps `os.Stderr` for a pipe (`capturedStderr` in `mutation_test.go`), has to
set the target itself rather than assume the default.
