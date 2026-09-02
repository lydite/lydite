# Coverage is per component, gated by `lydite test`, and stored as counts

[ADR 0016](0016-components-and-lydite-run-tests.md) makes the component the unit
lydite builds, tests, schedules and selects, and removes `coverage.source`. It
does not say what coverage becomes once the component is the unit. This does.

**Coverage is measured per component, from the instrumented variant the
component's runner already derives. `lydite test` measures it and gates it, and
`lydite coverage` is removed. A baseline is per-component line counts, keyed by
tree.**

## Measuring is not gating, and only one of them may touch the network

Measuring is local: run each component's instrumented variant, write the report
the runner names. Gating needs the remote — fetch the `lydite` branch, read the
baseline for the base tree, record a new entry.

Folding both into `lydite test` unconditionally would have a developer's local
run push to a shared branch. Inferring "am I in CI" to avoid that is the thing
[ADR 0018](0018-selection-widens-on-ignorance.md) already refused for selection,
for the same reason: every signal is unreliable where lydite runs, and the caller
already knows.

So `lydite test` always measures, and `--gate-coverage` turns on the baseline
interaction, exactly as `--affected` and `--diff-base auto` are passed by a
caller that knows its event.

**A run that measured but did not gate says so.** The coverage rows render either
way, and the ungated ones carry a status that distinguishes them from a pass.
Without that, a workflow missing the flag reports the same green as one that
gated — the failure wardnet/wardnet#957 shipped, where patch coverage never ran
and the pull-request comment read as though it had.

`--no-coverage` opts out of instrumentation entirely and emits no coverage rows
at all. Rows saying `unmeasured` on every fast local run would train readers to
ignore the tag that exists to be noticed, which is the argument already made for
types-only TypeScript packages. The state that must stay visible is *measured and
not gated*, because that is the one a misconfigured pipeline lands in.

Instrumentation is on by default despite costing real time — `cargo llvm-cov`
forces a separate instrumented build, and Go's `-coverpkg=./...` recompiles every
package per test binary. The fast inner loop is `go test` or `cargo nextest` run
directly; nothing reaches for lydite to re-run one package. A gate that is opt-in
is a gate that is off where it matters.

## A baseline is counts, not percentages

An entry is each component's covered and total line counts, and the three figures
lydite reports — per component, per language, global — are all `Σ covered / Σ
total` over a subset of them.

Percentages cannot be re-weighted. Composing a language or global figure from
components needs each component's size, and so does a run that measured only some
of them under affected selection. Storing percentages would make the language
figure underivable from the components that produce it, leaving two quantities to
disagree.

Counts also make a partial run compose honestly rather than needing machinery for
it: measured components contribute fresh counts, unmeasured ones contribute the
counts already recorded, and the row says how many of each. A composed figure
that does not say what it measured is indistinguishable from one that measured
everything.

This is a different quantity and a different shape, so by ADR 0007's own rule
baselines move to `v3/<tree>.json` and every consumer takes one clean cache miss.

## The baseline chain runs through pull requests, not through the default branch

Keying by tree — already true before this — means the number a pull request
measures is the number for the commit it becomes: CI builds `refs/pull/N/merge`,
and a squash merge lands a commit carrying that same tree. The next pull request's
merge-base resolves to that commit, and the lookup hits the entry the previous
one wrote.

So no run on the default branch is required to record a baseline, which matters
because requiring one is a consumer obligation that a repository running CI only
on pull requests satisfies never. It is also strictly cheaper: one instrumented
run per change rather than one on the branch and another on main.

**A miss is resolved by measuring, never by substituting.** The base tree is
checked out into a throwaway worktree and measured there. Gating against the
nearest ancestor baseline was rejected: it makes the number a change is judged
against depend on how far back history happened to have an entry, which is not
reproducible from the change itself.

Recomputing is affordable here in a way it was not before. It runs the same path
`lydite test` runs, so it starts the compose services the component declares —
where the previous mechanism invoked coverage tooling directly with no services,
producing a failed measurement for any suite needing a database, which is one of
the roads to a `{}` baseline cached as real. It also requires `cargo-llvm-cov`,
which lydite now installs rather than hoping for.

## The floor's unit is the component

`coverage.floor` gates each measured component. The unit is coarser than the
crate or package it gated before, and that is a change in what is measured rather
than a weakening of it: an untested crate inside a workspace still contributes its
lines as uncovered and still drags its component's figure down in proportion to
its size.

What a component-level floor cannot catch is a *small* untested sub-unit, since a
small one cannot move the component's figure much — the residual ADR 0007
identified when it introduced the floor. A repository that wants crate-level
floors declares those crates as components, which is a statement about what it
wants tested, made in the file whose history records exactly that.

## Consequences

- `lydite coverage` is removed and `coverage.source`,
  `coverage.{go,rust}.report`, `--source`, `--tests`, `--go-report`,
  `--rust-report` and `--rust-lcov-report` go with it. Each existed to locate a
  report some other job produced; lydite writes every report itself, to a path it
  chose. Removed keys are rejected with an error naming the removal, never
  ignored — the stance `config.validateLinter` takes for `linter: eslint`, since
  a dropped key means a repository measuring something other than what its author
  wrote while every run reports a pass.
- **One artefact per language serves both gates.** Go's profile, Rust's lcov,
  TypeScript's lcov. Rust's `--json` export is dropped: an lcov's summed `LF`/`LH`
  equal the JSON's `totals.lines.{count,covered}`, so the aggregate is derivable
  from the lcov and the per-line hits are not derivable from the JSON. Only one
  of the two is load-bearing, and keeping both is what produced an invocation
  carrying `--output-path` twice, which cargo-llvm-cov refuses to parse.
- A component declaring a raw `command:` has no instrumented variant to ask for
  and is reported `unmeasured`, which does not vote. Excluding it instead would
  drop it from the global figure silently, leaving a gate that measured fewer
  components than the repository has reading as a complete one. Adding a key
  naming where its coverage lands was rejected as `coverage.{go,rust}.report`
  returning under a new name, one decision after being deleted.
- Patch coverage gates per component against that component's own baseline, and
  its per-line data comes from the same instrumented run rather than a second one.
- `internal/detect` no longer decides what a coverage unit is. Deleting it belongs
  with scan.

## Considered and rejected

**`lydite coverage` as a gate-only command consuming what `lydite test` wrote.**
Preserves the published name and re-runs nothing, but makes the numbers travel
through the filesystem between two processes. Every failure of that handoff — a
mismatched `--dir`, a cleaned workspace, a matrix job that ran one and not the
other — yields a coverage gate that saw nothing, which is `coverage.source:
report`'s failure surface rebuilt immediately after deleting the key that created
it.

**Keeping `lydite coverage` as it is.** It re-runs every suite to measure what the
test run just built, which is the duplication `coverage.source: report` was
invented to avoid, with no remaining way to opt out of it.

**Deriving sub-component units for the floor from lcov's per-file records.** Would
preserve crate-level strictness, but rebuilds a shadow unit model immediately
after deleting the real one, and has no analogue for Go — a profile is
package-qualified and has no notion of a crate — so it would put per-language
special-casing into the one gate that has none.
