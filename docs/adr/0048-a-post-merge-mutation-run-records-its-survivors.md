# A post-merge mutation run records its survivors instead of gating on them

[ADR 0043](0043-mutation-reaches-the-ledger-from-a-post-merge-run.md) already states the intent in
its consequences — *"the pull-request run gates and records nothing; the post-merge run records and
gates nothing"* — and the run does not behave that way. `lydite mutation` has no gate flag at all;
it always gates. The `mutate` matrix job in `.github/workflows/lydite-baseline.yml` invokes it bare,
so a survivor on the merge commit turns the whole `lydite-baseline` run red after the change has
landed, where the number is history rather than a verdict.

Measured on the merge that introduced the job:

```
mutate (cli)             failure    mutation failed in 780.7s
mutate (cloud-services)  success
record coverage baseline success
```

The recording worked — the ledger entry carries `{"killed": 38, "survived": 5, "unviable": 7, …}`.
What failed is the workflow, on the one thing nobody can act on. The branch is gone, and the remedy
the run prints — *"write the assertion that fails when the code changes this way"* — belongs to a
pull request that no longer exists. The red also sits beside `record`, the only job allowed to write
the `lydite` branch, and a permanently red workflow beside that job teaches everyone to skim past
the one failure there that would matter.

**The distinction this keeps:** a run that *could not run* — a variant that is not runnable, an
unresolvable base revision, a bad flag — still fails. Only a completed measurement stops voting.

## The CLI decides this, not the workflow

`continue-on-error: true` on the step, or a trailing `|| true`, is the change that touches no Go
code, and it is the wrong one. It neutralises the process's exit code without knowing what produced
it, so it swallows "this component declares a raw command and has no build-only variant" and
"`--base-sha HEAD~1` did not resolve" along with the survivor — the exact family this decision
undertakes to keep failing. ADR 0037 settled the same shape for the relay: a deterministic
misconfiguration fails the step rather than falling back, because a suppression that cannot tell a
measurement from a mistake hides the mistake.

A workflow-level suppression is also untestable. Whether a survivor votes is then a property of a
YAML key that only a real merge with a real survivor exercises, and the branch that would prove it
is deleted by the time the job runs. Inside the CLI it is one flag, and the four cases — a survivor
under the flag, a not-runnable variant under the flag, a baseline that did not pass, and the default
— are four ordinary tests.

So the workflow states the policy (which flag to pass), and the CLI carries the meaning (what a
survivor is worth). That split is the one every other gate in this repository already uses.

## `--no-gate`, and there is nothing for it to contradict

`lydite mutation` gains a boolean **`--no-gate`**. A run that passes it measures every mutant it
would otherwise have measured, writes every document it would otherwise have written, and renders a
completed component's outcome without voting on the exit code.

It is spelled as the negation of a default rather than as a `--gate-mutation` opt-in, because
mutation gates by default and `lydite test`'s `--no-coverage` is the established spelling for
turning a default off. `--gate-mutation` would have to default to true to preserve the current
behaviour, and a `--gate-*` flag that is on unless asked otherwise reads as the opposite of the
three that already exist.

`--record-only` was rejected as a name: recording does not depend on this flag or on the exit code
at all. `recordMutants` writes `mutants.json` unconditionally, before any row is consulted, and
`lydite test record` reads only the documents on disk. A name implying the flag causes the recording
would send a reader looking for a connection that is not there.

**No flag-conflict check is added, and that is deliberate.** `lydite test` makes `--no-coverage`
with `--gate-coverage` a hard error for a stated reason — *"silently ignoring one of the two flags
would leave a workflow believing it gates."* `--no-gate` has no partner that claims gating: there is
no `--gate-mutation` to contradict, and `--declined` (ADR 0044) short-circuits before anything runs
and already gates nothing, so the pair points one way and misleads nobody. The convention demands an
error for a combination that leaves a caller believing it gates, not for every combination in which
one flag has nothing to add.

## A measurement nobody gated is `context`, survivors and all

Under `--no-gate`, a component whose run completed renders `StatusContext` — **whether or not it had
survivors**. The row's value still names what was found (`5 of 43 mutant(s) survived in 12m03s`), and
a survivor's detail lines still name each one.

This is not a new status. `StatusContext` already means *measured, deliberately not gating* for the
per-component `mutation: false` row and for `mutation merge`'s folded row, and `--json` and the glyph
table already carry it, so nothing in `internal/ui/row.go` changes. Neither `StatusUnmeasured` nor
`StatusDeclined` fits: the first means an expected input went missing, and the run measured
everything it was asked to; the second means the repository chose not to run this concern, and it
ran.

The harder half is the component that killed everything. Leaving it `StatusPass` under the flag was
the obvious reading of "make survivors non-voting", and `ungatedRows` in `cmd/lydite/coverage.go`
settled the identical question for coverage the other way:

> StatusContext and never StatusPass: nothing was gated, so a run that measured 40% would otherwise
> render the same glyph as one that measured 95%, and a workflow missing `--gate-coverage` would
> report the green a gated run reports.

The repository's standing rule says the same thing in one line — *a gate that could not run never
renders as one that passed; nor does a measurement taken without being gated.* A ✓ on a post-merge
mutation row would be a claim that a gate examined this component and cleared it, when no gate
examined anything. So every completed row goes to `→`, and the numbers beside it are what the reader
acts on.

`--no-gate` therefore changes exactly one thing: the status a **completed** measurement carries.
`prepareMutation`'s "not runnable" row stays `StatusFail` under the flag, an unresolvable base
revision still fails before any report renders, and the rows that were already non-voting — a
denominator of zero, a baseline suite that did not pass, no headroom under the memory bound, an
interrupted run — are untouched, because they were never voting to begin with.

## The findings are still emitted

Every survivor still produces its `finding.Finding`, in the rendered document and in `--json`.

A post-merge survivor's finding anchors to a line in the merge commit, which is a commit that exists
on the default branch and does not move, so the located claim is as well-formed as a pull request's.
Findings never vote — `Report.Verdict()` reads rows and nothing else — so keeping them costs the
exit code nothing, and dropping them would make the document's account of the run depend on which
flag produced it. The ledger and any later quality-history surface then have the whole answer to
*what survived on this merge*, rather than a count with no locations.

## `lydite-pr.yml` does not change, and this is not #75

The pull request is where the mutation gate belongs, and nothing here touches it. The `mutation`
matrix job keeps invoking `lydite mutation` bare, a survivor there keeps failing the job, and the
remedy it prints is addressed to an author who still has the branch to act on.

**This ADR must not be read as having settled whether that gate blocks a merge.** It is advisory
today for a separate reason: `lydite-pr.yml` is not a gt stage and cannot be one, so nothing it
reports blocks a merge — which is what
[#75](https://github.com/lydite/lydite/issues/75) records, alongside the coverage gate and the scan
findings it costs the same way. #168 neither fixes that nor depends on it, and the two questions
look alike from a distance without being alike: one
is what a survivor is worth after the merge, the other is what a survivor is worth before it, and
the second needs a decision this one is not entitled to make.

## Consequences

- `cmd/lydite/mutation.go` gains `--no-gate` and the status branch in `mutationRow`. Nothing in
  `internal/ui`, `internal/ledger` or `lydite mutation merge` changes: the status already exists, and
  recording never read the exit code.
- The default behaviour is byte-identical. A run without the flag produces the same rows, the same
  document and the same exit code it produces today.
- A post-merge mutation report under the flag contains no `StatusPass` row. Were such a document ever
  fed to `lydite publish`, its section would read `unmeasured` at the section level, for the reason
  ADR 0044 records about an all-`context` section. Nothing publishes a post-merge document today;
  named here as a known edge rather than left to be rediscovered.
- `.github/workflows/lydite-baseline.yml` passes `--no-gate` on the `mutate` step. No
  `continue-on-error`: the job still fails on everything #168 keeps failing.
- [`lydite/actions`#10](https://github.com/lydite/actions/issues/10) specifies the consumer's
  post-merge job in the gating shape this ADR removes, and is amended to carry `--no-gate` before it
  is implemented. Without that, every repository adopting the reusable baseline workflow inherits a
  red run on its first merge with a survivor — on the cutover, all of them at once.
