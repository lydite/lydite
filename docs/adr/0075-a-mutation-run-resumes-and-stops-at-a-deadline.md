# A mutation run resumes from its own recorded verdicts, and stops at a deadline rather than a job timeout

[ADR 0027](0027-mutation-is-its-own-command.md) settled two positions this ADR reverses:
**no runtime budget**, and **no whole-repository mode**. Both were right for the executor 0027
described, because that executor could not keep anything it had measured. A run either finished
and wrote `mutants.json`, or died and wrote nothing. Under that constraint, every cap was a bad
choice:

- A cap that passed was a gate that silently checked less.
- A cap that failed punished a change for its size.
- A cap that answered `unmeasured` gave a busy repository a permanently amber row.

0027 therefore let a too-large run die as a CI job timeout, and let
[ADR 0026](0026-a-shard-reports-what-it-owns-and-the-fold-decides-completeness.md)'s fold fail the component it never heard from.

A run that dies at the job timeout discards every verdict it had already measured, and a rerun
starts from zero and dies at the same place. A change large enough to outrun the job once
outruns it on every attempt. What 0027 objected to was an amber row that could never turn
green. Once a run keeps its verdicts, the row stays amber only until the next attempt resumes
from them.

## Every verdict is recorded as it lands, keyed to the exact tree it was measured against

Each component's run keeps a **mutation state** holding two things:

- the baseline it measured: pass or fail, elapsed time, peak RSS, and the executed lines that
  select which lines get mutants;
- one line per mutant verdict, appended the moment that verdict is decided.

A hard kill (a job timeout, `SIGKILL`, Ctrl-C) loses at most the mutants that were in flight.

The state is valid only under its **fingerprint**, which is a hash of:

- the *contents* of every file the executor's worker copy is built from (git's tracked files
  plus the untracked files git is not ignoring — the same list `mutation.Tree` copies);
- the component's runner argv;
- the toolchain versions lydite provisioned;
- lydite's own version;
- the `--timeout` and `--memory` settings.

A mutant is identified by its path, operator, byte offset and length, and its original and
mutated text.

The fingerprint covers file contents rather than a commit SHA so that a dirty local tree is keyed
exactly. Uncommitted edits are part of what a local run measured. A SHA would reuse a verdict
across an edit the SHA does not see.

When a run starts, a matching fingerprint makes it reuse the recorded baseline and every
recorded verdict: killed, survived, unviable, timed out, out of memory, and acknowledged. Only
the mutants with no recorded verdict are run. Timed-out and out-of-memory verdicts are reused as
well. They were measured against a budget derived from the baseline, and the resumed run reuses
that same baseline, so its budget is identical. The only thing that differs is machine load,
and a fresh run is exposed to that noise too.

Any other fingerprint discards the state and starts again. Each component keeps only its latest
fingerprint, so the state never needs pruning. The report says how many verdicts were reused, so
a resumed run is never silent about it. `--fresh` discards the state before running.

The mutation state is a **Cache**, not a **Ledger**. Losing it costs time, never information, so
a failed read or write is a diagnostic rather than a failure. A run whose state cannot be
written measures everything, exactly as it would without one.

### What the fingerprint trusts

This is the first time a verdict one process computed is trusted by another. It is sound only
because the fingerprint covers everything the verdict depended on: the source being mutated, the
suite killing it, and the budget judging it. The base the change is diffed against is
deliberately left out. A different base changes *which* mutants are wanted, not what any one of
them answers, so a mutant's verdict under an identical tree holds regardless of the base.

The state opens no hole the shard does not already have. In CI the state travels through
`actions/cache`, keyed by component, head SHA and run attempt. It is restored only by a run over
that same SHA, and GitHub scopes a pull request's caches to its own ref. The code that could
write a forged verdict into it is the pull request's own code, which already runs in the shard
and could just as easily write a forged `mutants.json`.

## A deadline stops dispatch and answers `incomplete`, never a pass

`lydite mutation --deadline <duration>`, measured from the process's start, is set below the job
timeout. When it is reached:

- no further mutants are dispatched, and the ones in flight are cancelled;
- the state is flushed;
- `mutants.json` is still written, carrying the component as **incomplete** with how many
  mutants were measured out of how many were wanted.

An incomplete component renders as `unmeasured`, reading "N of M measured, rerun to resume".
The process exits non-zero, even under `--no-gate`. `unmeasured` does not vote on its own
([output-grammar](../../agentic/references/output-grammar.md)), so the exit code is how a job
that ran out of time stays red.

The one exception is a survivor already found before the deadline. That row fails outright,
because a known survivor fails whatever is still unmeasured.

`lydite mutation merge` renders a shard's incomplete component the same way. `lydite test record`
never writes a partial count to the quality history, just as it never writes an interrupted run's.

This is still not a runtime budget in 0027's sense. Nothing is capped and then passed, and
nothing is failed for its size. The deadline only moves where the run stops, from a job timeout
that keeps nothing to a point where the run can keep what it measured. A rerun resumes from
there, and the amber row lasts only until then.

## A scheduled sweep of the default branch clears files, and only ever advises

0027 rejected a whole-repository mode because it would run for hours, which made it a mode
nobody would run. With resume, a scheduled run on the default branch can keep advancing through
a component's files until its deadline, pick up next time where it stopped, and eventually
cover the whole component. It does this under a weaker guarantee than the exact resume above,
and that weaker guarantee is what keeps it out of any gate:

- A file is **cleared** while its content hash is unchanged. A kill recorded against it is
  reused across commits, not only across attempts on one tree.
- A kill can go stale without its file changing, when the tests that killed it weaken. A cleared
  kill is therefore reused only while the component's test files hash the same, and never past
  an expiry. The expiry catches a weakened helper or fixture that the test-file classification
  does not cover.
- Survivors are never reused. Every sweep re-runs them.
- Sweep results reach the quality history only through `lydite test record`, as a post-merge
  mutation run's do today ([ADR 0043](0043-mutation-reaches-the-ledger-from-a-post-merge-run.md)).
  A pull request's gate never reads a cleared kill. It resumes only from verdicts measured
  against its own exact fingerprint.

The sweep is anticipated, not built. Its command shape, expiry length and scheduling are
decided when it is.
