---
about: internal/runner's TestOnlyTheInvocationThatRunsTheWrapperInstallsIt flakes on macOS with "unlinkat … directory not empty" under the temp HOME's Library/ — the same real `go install` under a temp HOME shape as the gotool install tests, reached through the go-test runner's Prepare
saw:
  - source/cli/internal/runner/staging_test.go
  - source/cli/internal/runner/runner.go
  - source/cli/internal/runner/pins.go
  - source/cli/internal/gotool/gotool.go
  - source/cli/internal/executil/executil.go
targets: gotool-failed-install-tests-race-go-telemetry-under-a-temp-home
verdict: still-true
---

A third test with the shape recorded in
`20260926-gotool-failed-install-tests-race-go-telemetry-under-a-temp-home`, in a different
package, so fixing the gotool tests alone does not remove the flake.

`TestOnlyTheInvocationThatRunsTheWrapperInstallsIt` (`internal/runner/staging_test.go`) sets
`HOME` to `t.TempDir()` and passes `executil.Env{Install: ["GOPROXY=off", "GOFLAGS=-mod=mod"]}` so
an attempted install must fail. For the Instrumented variant it calls `Prepare`, which for
go-test is `installGoTestsum` (`internal/runner/runner.go`): `inv.Name == gotestsumName`, so it
calls `gotool.Ensure(ctx, env.Install, "gotestsum", "v1.13.0", "gotest.tools/gotestsum@v1.13.0",
"")`. `Ensure` (`internal/gotool/gotool.go`) resolves `BinDir` under `os.UserCacheDir()` — on
darwin `$HOME/Library/Caches/lydite/...`, i.e. inside the temp HOME — `MkdirAll`s it, finds no
cached binary, and really shells out: `executil.RunEnv(..., "go", "install", pkg)`, whose child
environment is appended to `os.Environ()` and so sees the temp `HOME`. (It is `go install`, not
`go run`.) The child `go` writes Go telemetry under `$HOME/Library/Application Support/go/telemetry`
even though the install fails at once, and the `HOME` temp dir's cleanup then intermittently hits
"unlinkat …/Library/…: directory not empty". The Plain and BuildOnly loops return early
(`inv.Name != gotestsumName`) and spawn nothing.

What is verified: the call chain above, from the code. What is inferred, as in the gotool
candidate: that a telemetry writer still running after `go install` exits repopulates the tree
while `RemoveAll` walks it. `GOTELEMETRY=off` does not suppress the files (per that candidate).
Any fix should be shared with the gotool tests — e.g. a helper that pre-writes a telemetry `mode`
file of `off` under the temp HOME's user config dir, or points only the cache (not HOME) at a temp
dir.
