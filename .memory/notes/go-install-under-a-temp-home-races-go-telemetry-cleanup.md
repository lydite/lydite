---
name: go-install-under-a-temp-home-races-go-telemetry-cleanup
kind: gotcha
description: Tests that run a real go install under a t.TempDir() HOME (gotool's two failed-install tests, and runner's wrapper-install test) flake at cleanup with directory not empty under the Go telemetry directory, and GOTELEMETRY=off does not prevent the files.
anchors:
  - path: source/cli/internal/gotool/gotool.go
    blob: 736ee234b088
  - path: source/cli/internal/gotool/gotool_test.go
    blob: 05dbea0a887f
  - path: source/cli/internal/runner/staging_test.go
    blob: dbd109eca9b5
  - path: source/cli/internal/runner/runner.go
    blob: 3a3d11a81409
confidence: suspect
---

Three tests share one shape, so fixing one package does not remove the flake:

- `TestAnInstallThatFailsIsReported` and `TestAFailedInstallReturnsNoPath` (`internal/gotool/gotool_test.go:90`, `:106`) set `HOME` to `t.TempDir()` and call `gotool.Ensure` with an unresolvable version, so `Ensure` really shells out: `executil.RunEnv(ctx, "", env+GOBIN, "go", "install", pkg)` (`internal/gotool/gotool.go:70`). `RunEnv` appends to `os.Environ()`, so the child `go` sees the temp `HOME`. `TestAnInstalledToolIsNotInstalledAgain` does not shell out.
- `TestOnlyTheInvocationThatRunsTheWrapperInstallsIt` (`internal/runner/staging_test.go:97`) sets `HOME` to a temp dir with an `Install` env of `GOPROXY=off` so an install must fail; for the Instrumented variant `Prepare` reaches `installGoTestsum` (`internal/runner/runner.go`) and so `gotool.Ensure`, whose `BinDir` sits under `os.UserCacheDir()` (`gotool.go:31`) inside the temp HOME.

Observed intermittently on macOS: `t.TempDir()` cleanup fails "directory not empty" under `<home>/Library/Application Support/go/telemetry/local`. Verified by hand once: `HOME=<tmp> GOPROXY=off go install gotest.tools/gotestsum@v0.0.0-does-not-exist` leaves `telemetry/{local,upload}` behind though the install fails in milliseconds, and `GOTELEMETRY=off` in the environment does not suppress those files. Not reproduced in three later runs.

**Inferred, not verified:** the go command's telemetry start-up spawns a detached child that keeps writing into `telemetry/local` after `go install` exits, repopulating the tree while `RemoveAll` walks it. Untested fixes: pre-write a telemetry `mode` file of `off` under the temp HOME's user config dir (`Library/Application Support/go/telemetry/mode` on darwin, `$XDG_CONFIG_HOME/go/telemetry/mode` elsewhere), or point only the cache, not HOME, at a temp dir.
