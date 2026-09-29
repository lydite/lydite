---
description: "A bounded or cancellable command that may spawn children runs as the leader of its own process group, killed whole on cancel, with a WaitDelay bounding the wait for stragglers."
---

# A cancelled command's descendants are killed with its own process group, not the leader alone

`exec.CommandContext`'s own cancellation reaches only the process lydite started. A suite is
rarely one process — `npx vitest` starts node, which starts workers, and every one of them
inherits the parent's stdout and stderr pipes — so killing the leader alone reparents the rest
to init, still holding those pipes open, and `cmd.Wait`'s output copy then blocks on them
forever. `executil.RunOutputBounded` fixes this the way any future bounded or cancellable
runner variant must: run the command as the leader of a process group of its own
(`Setpgid: true`), replace `cmd.Cancel` with a kill of the whole group (`SIGKILL` to the negative
pgid, so every member dies at once), and set `cmd.WaitDelay` so a descendant that leaves the
group or ignores the signal cannot hang the wait forever either. A grouped run that only reaches
its `WaitDelay` — `errors.Is(err, exec.ErrWaitDelay)` with the command's own exit otherwise clean
— is not evidence the run was clean: it is evidence something it started outlived it, and that
has to reach the caller as its own signal (`Result.OutputHeldOpen`), not fold into `Err` or `Ok`.

## Applies to

`source/cli/internal/executil`'s `runTo`, `ownGroup`/`killGroup` (`procgroup_unix.go`,
`procgroup_other.go`), and any new `Run*` variant added there whose caller bounds or cancels a
command that may itself start a suite, a build, or anything else with children of its own —
`mutation.Execute` is the one caller today.

## Example

```go
// wrong: exec.CommandContext's own cancellation kills only the leader; a
// worker tree it started keeps the output pipes open and Wait never returns
cmd := exec.CommandContext(ctx, name, args...)
cmd.Stdout, cmd.Stderr = out, out

// right: the whole group dies together, and a straggler cannot hang the wait
cmd := exec.CommandContext(ctx, name, args...)
cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
cmd.Cancel = func() error { return killGroup(cmd.Process.Pid) } // SIGKILL to -pgid
cmd.WaitDelay = groupWaitDelay
```

Reasoning: [`agentic/references/mutation.md`](../references/mutation.md).
