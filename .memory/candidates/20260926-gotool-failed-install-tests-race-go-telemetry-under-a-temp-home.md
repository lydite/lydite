---
about: internal/gotool's TestAFailedInstallReturnsNoPath (and TestAnInstallThatFailsIsReported) intermittently fail at TempDir cleanup with "directory not empty" under go/telemetry/local — the real `go install` they spawn writes Go telemetry into the temp HOME
saw:
  - source/cli/internal/gotool/gotool_test.go
  - source/cli/internal/gotool/gotool.go
  - source/cli/internal/executil/executil.go
---

Observed as an intermittent failure: `TestAFailedInstallReturnsNoPath`'s `t.TempDir()` cleanup
fails with "directory not empty" on a path under `go/telemetry/local`. Not reproduced in three
runs on 2026-09-26 (darwin/arm64, go1.26.6); the mechanism below is partly verified.

**Verified from the code and by hand:**
- The test sets `HOME` to `t.TempDir()` and calls `gotool.Ensure` with an unresolvable version,
  so `Ensure` really shells out: `executil.RunEnv(ctx, "", env+GOBIN, "go", "install", pkg)`
  (`internal/gotool/gotool.go`). `RunEnv` appends to `os.Environ()`, so the child `go` sees the
  temp `HOME`. `TestAnInstallThatFailsIsReported` has the same shape;
  `TestAnInstalledToolIsNotInstalledAgain` does not (cached binary, no shell-out).
- Running `HOME=<tmp> GOPROXY=off go install gotest.tools/gotestsum@v0.0.0-does-not-exist` by hand
  leaves `<tmp>/Library/Application Support/go/telemetry/{local,upload}` behind, with
  `local/upload.token`, `local/weekends` and a `go@...v1.count` file — even though the install
  fails in milliseconds. Go telemetry defaults to `local` mode under a fresh config dir.
- `GOTELEMETRY=off` in the environment does **not** suppress those files (checked by hand), so it
  is not a fix; the mode is read from the telemetry config under the user config dir.

**Inferred, not verified here:** the go command's telemetry start-up can spawn a detached child
process that keeps writing into `telemetry/local` after `go install` itself has exited; if that
child writes while `testing`'s `RemoveAll` is walking the temp dir, cleanup sees a directory
repopulated under it and reports "directory not empty". That fits the intermittency and the
path, but was not observed directly.

Plausible fixes (untested): write a telemetry mode file of `off` into the temp HOME's
`Library/Application Support/go/telemetry/mode` (darwin; `$XDG_CONFIG_HOME/go/telemetry/mode`
elsewhere) before calling `Ensure`, or point the test's HOME-derived cache elsewhere while
leaving the real user config dir in place for the child `go`.
