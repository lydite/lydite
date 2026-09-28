---
about: a mutant's per-suite timeout only kills the direct child (npx/vitest), so a grandchild that keeps stdout/stderr open blocks `cmd.Wait()` forever — the per-mutant --timeout never fires and the process shows 0% CPU with no children
saw:
  - source/cli/internal/executil/executil.go
  - source/cli/internal/mutation/executor.go
  - source/cli/internal/runner/runner.go
---

Investigated a consumer report: `lydite mutation --dir . --affected --component core-common
--base-branch main --stream` on a TypeScript/vitest component stalled 13+ minutes at 0% CPU with
`pgrep -P <lydite pid>` showing **zero** children, and the per-mutant `--timeout` (derived from
the baseline, three times it with a 60s floor — `budgetFactor`/`minimumBudget` in
`source/cli/internal/stages/mutation/budget.go:187,190`) never fired.

**Root cause, grounded in the code:**

- `internal/mutation/executor.go:368-413` (`execute`) wraps the run's context in
  `context.WithTimeout(ctx, opts.Timeout)` and calls
  `executil.RunOutputBounded(within, dir, opts.Env, io.Discard, opts.MaxMemory, inv.Name,
  inv.Args...)` — `RunOutputBounded` reaches `runTo` in
  `source/cli/internal/executil/executil.go:334-370`, which does
  `cmd := exec.CommandContext(ctx, ...)`, `cmd.Start()`, then `res.Err = cmd.Wait()`.
- **No `cmd.SysProcAttr` (`Setpgid`) anywhere in the CLI** (`grep -rn "WaitDelay|Setpgid|SysProcAttr"
  source/cli` → 0 hits) and **no `cmd.WaitDelay` set anywhere either**. On context expiry,
  `exec.CommandContext`'s default `Cancel` sends the kill signal to `cmd.Process` alone — the
  single direct child — never a process group.
- The TypeScript runner's invocation is `Invocation{Name: "npx", Args: []string{"vitest", "run",
  ...}}` (`source/cli/internal/runner/runner.go:939,956-962`). `npx` forks `node`/`vitest`, and
  vitest (pool `forks`, or any spawned helper) can fork further. When the timeout expires, only
  the `npx` process (lydite's direct child) is killed and reaped by `cmd.Wait()`'s internals; any
  grandchild still running is **reparented to init**, not to lydite — so `pgrep -P <lydite pid>`
  correctly shows none, even though a process is still alive and holding the write end of the
  inherited stdout/stderr pipe.
- `runTo` builds `cmd.Stdout`/`cmd.Stderr` as `io.MultiWriter` over real writers (here
  `io.Discard` plus the capture buffer), which os/exec backs with an `os.Pipe()` and a copying
  goroutine. Without `cmd.WaitDelay`, `cmd.Wait()` blocks until those copy goroutines see EOF —
  which requires every holder of the pipe's write end to exit. An orphaned grandchild that is
  merely idle (blocked on I/O, an unresolved promise, a stuck lock — **not** spinning) keeps the
  pipe open indefinitely and explains 0% CPU on the lydite process: it is parked in a blocking
  read inside `cmd.Wait()`, not doing anything itself.
- This is consistent with every piece of the report: 0% CPU (blocked syscall, not a CPU spin —
  which argues against a pure synchronous infinite loop as the mutant's actual behaviour, since
  that would pin a core at 100% somewhere), zero children of lydite (the one direct child was
  already killed+reaped; the surviving process is under init), and the timeout "never firing"
  (the *context* did expire and did kill the direct child — the outward symptom is that lydite
  itself never returns from `Execute`, not that no kill happened at all).

**Confirming evidence a follow-up should gather:** `ps -eo ppid,pid,stat,comm | grep -i vitest`
(or `ps -ef` on macOS) to find a vitest/node process whose PPID is 1 (init/launchd) rather than
lydite's PID; `lsof -p <lydite pid>` to show the pipe fd still open with no reader progress.

**Fix shape (not implemented, no code changed):** `runTo` needs either `cmd.WaitDelay` (Go 1.20+,
forces `cmd.Wait()` to return after a grace period past cancellation, closing the pipes out from
under a lingering grandchild) or `SysProcAttr{Setpgid: true}` plus killing the whole process group
(`syscall.Kill(-pgid, ...)`) on timeout, so an orphaned vitest/tinypool worker cannot hold the
pipe open. Neither exists today in `internal/executil`.

Also checked and confirmed as *not* the cause:
- Timeout is not zero/disabled under `--affected` or in a mutation worker directory —
  `source/cli/internal/stages/mutation/run.go:435` computes `timeout, workers :=
  budget(baseline, in.Timeout), workersFor(...)` on the same path regardless of `--affected`;
  `--affected` only narrows which mutants `generate` produces, not the budget derivation.
  `minimumBudget` (60s, `budget.go:190`) is a hard floor, so a fast/zero baseline cannot yield a
  zero timeout.
- `mutation.log` has no "start" line by design: `internal/mutation/executor.go:415-430` (`log`)
  is only called from the `finish` closure once a `Result` is known, so a hung mutant produces no
  line at all until (if ever) it resolves — matching the report's "log ended at the same mutant"
  in both CI and locally.
- The CI-side "shard cancelled" mechanics (`lydite mutation merge`'s fold via
  `source/cli/cmd/lydite/fold.go`) never name a cause for a missing component row — by design,
  per `agentic/references/mutation.md`'s "The fold" section: "a job killed at its timeout, a
  runner OOM and a failed upload leave identical absence." The actual `lydite.yml` job matrix
  that shards `lydite mutation` lives in `pedromvgomes/gt`'s `reusable-lydite.yml` (called from
  `.github/workflows/ci-orchestration.yml:181`), which itself calls `lydite/actions` — **not in
  this repository** — so any "a required stage was cancelled" GitHub branch-protection text is
  not lydite's own output and is not in this repo's source.
- There is no whole-run timeout/deadline flag anywhere in `cmd/lydite` (`grep -n
  "WithTimeout|deadline|run-timeout" cmd/lydite/*.go` → 0 hits), consistent with
  `agentic/references/mutation.md`'s explicit, deliberate "no runtime budget" rationale (ADR
  0027): a cap that passes/fails/greys out a run is worse than none, so a run too large dies as a
  CI job timeout instead. That rationale does not cover this bug — a CI job timeout only helps if
  the *lydite process itself* eventually gets killed by the runner; a hang inside `cmd.Wait()`
  with no CPU use can still exceed a very long CI job timeout (the report says 60 minutes) before
  the runner's own outer timeout intervenes, and even then the failure signature (SIGTERM/SIGKILL
  from the runner) is different from what `ci.md` documents for the *OOM* case (exit 143, skipped
  post-steps) — worth checking against on the CI run in question.
