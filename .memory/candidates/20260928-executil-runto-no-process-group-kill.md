---
about: executil.runTo is the single choke point for every child process, has no WaitDelay/Setpgid, and nothing else in the tree relies on a detached background process
saw:
  - source/cli/internal/executil/executil.go
  - source/cli/internal/executil/executil_linux.go
  - source/cli/internal/executil/executil_darwin.go
  - source/cli/internal/executil/executil_other.go
  - source/cli/internal/compose/compose.go
  - source/cli/cmd/lydite/mutation.go
  - source/cli/cmd/lydite/test.go
---

Explored for a mutation-hang fix (orphaned `npx vitest` grandchild holding stdout/stderr pipe
after a timeout, `cmd.Wait()` blocking forever).

`runTo` (executil.go:334-370) builds `exec.CommandContext` with no `WaitDelay` and no
`Setpgid`/`Cancel` override — the only process-launch site in the module; every `Run*` helper
(`Run`, `RunEnv`, `RunOutput`, `RunOutputBounded`, `RunQuiet`, `RunQuietEnv`,
`RunQuietIsolatedEnv`) funnels through it (executil.go:159-257). The three build-tagged files
(`executil_linux.go`, `executil_darwin.go`, `executil_other.go`) only implement `limitMemory`/
`peakRSS` — no process-group code exists anywhere in `source/cli` today (confirmed:
`grep -rn 'Setpgid|Setsid'` finds nothing in `source/cli`).

`internal/compose.Stack.Up`/`Down` (compose.go:466-494) run `docker/podman compose up
--detach` / `down` through `executil.RunOutput`. `--detach` means the compose CLI process
itself exits once containers are started; the containers are owned by the container runtime
daemon, not held open as children of the exec.Cmd. So giving `runTo`'s child its own process
group (for a group-kill on timeout) does not affect compose's correctness — the compose
processes it groups are short-lived and unrelated to the daemon-managed containers.

`lydite test` and `lydite mutation` (cmd/lydite/test.go:67, cmd/lydite/mutation.go:94) both
install `signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)` and cancel the
flow's `ctx` on a first signal (a second signal restores default disposition via `stop()`).
Today, because no child gets its own process group, a terminal Ctrl-C already reaches every
descendant for free (they inherit lydite's own foreground process group). **If a fix puts
mutation's children into their own process group, that free propagation stops** — the fix then
must itself convert `ctx.Done()` into an explicit group kill (e.g. `cmd.Cancel` doing
`syscall.Kill(-pgid, syscall.SIGKILL)`), or Ctrl-C during mutation would leak the grandchildren
it currently reaps by accident.

No code path checks `ctx.Err()`/`context.Canceled` in `cmd/lydite/mutation.go` to write a
partial report — a cancelled run returns the flow's error straight through `mutationError`,
`main.go` prints `lydite: <err>` and exits 1; no `.lydite-reports/.../mutation.json` is
written on SIGINT/SIGTERM today.
