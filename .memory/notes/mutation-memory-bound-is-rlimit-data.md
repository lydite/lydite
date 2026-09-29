---
name: mutation-memory-bound-is-rlimit-data
kind: dead-end
description: lydite mutation bounds a mutant's suite memory with RLIMIT_DATA applied post-Start (via Prlimit on the child pid) — RLIMIT_AS, GOMEMLIMIT and a cgroup v2 scope were all measured and rejected, and Darwin has no working equivalent at all.
anchors:
  - path: source/cli/internal/executil/executil_linux.go
    blob: b5688da9b777
  - path: source/cli/internal/executil/executil_darwin.go
    blob: a887a14017a5
  - path: docs/adr/0027-mutation-is-its-own-command.md
    blob: fca9988bb73c
confidence: verified
---

Measured on a real `ubuntu-latest` GitHub Actions runner (CI run 34933726865) before
choosing the mechanism in `limitMemory` (`executil_linux.go:19-23`), because none of
Linux, a container runtime, or the target measurements were available locally:

- **`RLIMIT_AS`** (address space) is the wrong bound: a Go child aborted in
  `runtime.rt0_go`, before `main` ran, at 2x its own observed peak RSS, and needed 4x
  just to start. The multiplier that would be "safe" is a property of the runtime's
  up-front reservation, not of anything the run measured — a bound derived from a
  peak-based formula (as this one is, 4x the baseline's `MaxRSS`) would kill mutants for
  existing rather than for allocating.
- **`GOMEMLIMIT`** bounds nothing: it is a soft GC target. A child asked to stay under
  half its live set completed in 80ms at full residency.
- **A cgroup v2 delegated scope** is the mechanism with no fallback story: on
  `ubuntu-latest` the job's own cgroup (`/system.slice/hosted-compute-agent.service`)
  has an empty `cgroup.subtree_control` and refuses `mkdir` with `EPERM`;
  `systemd-run --scope` needs interactive auth; only `systemd-run --user --scope` works,
  and that rests on a user manager a container job or self-hosted runner need not have.
- **`RLIMIT_DATA`** (covers anonymous mappings since Linux 4.7 — where a Go/Rust/Node
  heap lives) is set *after* `cmd.Start()` via `golang.org/x/sys/unix.Prlimit` on the
  child's own pid (`executil_linux.go:19-23`) — it is inherited across `fork`/`exec`, so
  `go test`'s per-package binaries and `cargo nextest`'s or `vitest`'s forked workers are
  each held to the same ceiling without lydite knowing the process tree's shape. It is
  per-process, not summed across the tree: N concurrent children under one limit can
  together exceed it.

**Darwin has no equivalent.** `setrlimit` there refuses both `RLIMIT_DATA` and
`RLIMIT_AS` with `EINVAL` at any value (`executil_darwin.go:9-14`) — confirmed by direct
measurement, not by documentation. A memory bound requested on Darwin is therefore
accepted but never applied; `executil.Result.MemoryBounded` is `false` in that case, and
callers (`internal/mutation/executor.go`'s `run()`, `MemoryUnbounded`) must check it
rather than assume a requested bound held. This is stated on the mutation row rather
than left silent, per the project's own "a gate that could not run never renders as one
that passed" rule. See [[hitmemorylimit-ratio-threshold]] for how a hit against this
bound is then told apart from an unrelated failure.
