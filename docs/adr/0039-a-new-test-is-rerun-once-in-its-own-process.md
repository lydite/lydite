# A new test is rerun once, in its own process

Nothing lydite runs asks whether a test the change introduces agrees with itself. A
non-deterministic test is worse than no test at all: it teaches everyone who meets it to press
re-run, and it corrupts every gate downstream of the suite — a mutant "killed" by a test that
fails at random was never killed, and a coverage number produced by a run that sometimes takes a
different branch is a number nobody can compare against a baseline. Where the tests are written
by an agent, at the rate an agent writes them, that is not a rare accident.
[#22](https://github.com/lydite/lydite/issues/22) asks for the gate.

**`lydite test --gate-flaky` reruns every test the change introduces, once, in a separate `go
test` process filtered to those tests and carrying `-count=1`. A new test whose two outcomes
disagree fails the gate and produces a finding on its declaration. "New" is a test name the
merge-base's tree does not declare and HEAD's does, taken from `go/ast` rather than from the
diff's lines. This slice is Go only.**

## What "new" means

A test is new when its name is declared at HEAD in a package the merge-base either did not have
or had without that name. Both trees are parsed with `go/ast` and the answer is a set
difference of declared names, never a scan of the diff's added lines: a test moved between files,
reindented, or caught by a formatter would otherwise read as new, and the gate would spend its
budget rerunning tests nobody wrote.

**The identity is the package directory plus the test's name**, relative to the scan root, and
not the file. Within a package, `go test -run` cannot distinguish two names anyway, and a test
moved from `a_test.go` to `b_test.go` in the same package keeps its identity — which is the case
this rule exists for. The consequence is that moving or renaming a *package* makes every test in
it new. That is over-reporting, and it is the right direction to be wrong in: the cost is one
rerun of tests that already pass, and the failure it avoids is a package-level move quietly
carrying an untested new test through the gate.

Only the test files the diff touched need parsing at either revision. No other file can declare a
test name that was not there before, so the two-tree parse is bounded by the change rather than
by the repository.

**The unit is the top-level `func TestXxx(t *testing.T)`, never a subtest.** A `t.Run` name is
computed at runtime — `testdata/flakyprobe/head/probe_test.go.txt` builds two from a slice — and
nothing a parser reads can enumerate them. The captured report in `testdata/run1-suite.xml`
records `TestNewSubtests/one`, `TestNewSubtests/two` *and* `TestNewSubtests`, and the parent's
outcome already aggregates its children, so the top-level name is both the only statically
knowable unit and a sufficient one.

**A modified existing test is not new.** Its name was declared at the merge-base, so the set
difference does not hold it, and the gate says nothing about it. This is a known gap and a real
one: a rewrite of a test's body under an unchanged name introduces exactly the flake this gate
exists to catch, invisibly. Closing it needs a per-test identity that survives an edit — a body
hash, or the fingerprint machinery `internal/finding` already has — and that is a decision of its
own, not a detail of this one.

`FuzzXxx`, `ExampleXxx` and `BenchmarkXxx` are outside the definition in this slice. A fuzz
target's seed corpus runs under a plain `go test` and can flake, and it is the first extension;
an example and a benchmark each bring their own filtering and reporting quirk. Naming them here
is what keeps their absence a decision rather than an oversight.

## Two runs, and they are two processes

Run 1 is the ordinary suite run, which is already happening. Run 2 is one extra invocation per
package that holds a new test:

```
go test -run '^(TestDeterministicNew|TestFlakyNew|TestNewSubtests)$' -count=1 -race <pkg>
```

A test's outcome in run 1 is read from the JUnit report the run already writes, per `<testcase>`
name; run 2 writes its own report to its own path, because the ledger records run 1's counts and
a rerun of five tests overwriting them would put "5 tests" in the quality history of a component
that ran six hundred.

**`-count=1` is mandatory here, and that is the exact mirror image of
[ADR 0027](0027-mutation-is-its-own-command.md)'s rule that mutation must never pass it.** For
mutation the test cache is load-bearing — a mutant's overlay invalidates the mutated package and
its dependents and nothing else, which buys incremental test selection for free and is worth a
factor of fifty. For this gate the cache is fatal: run 2's argv is, by construction, a cacheable
subset of a run that just happened, so without `-count=1` Go serves the answer from run 1's own
result and the gate reports determinism having executed nothing.
`testdata/cached-rerun.txt` is that, captured:

```
$ go test -run '^(TestDeterministicNew|TestNewSubtests)$' .
ok  	flakyprobe	0.607s
$ go test -run '^(TestDeterministicNew|TestNewSubtests)$' .
ok  	flakyprobe	(cached)
```

The two rules do not conflict and neither is a mistake in the other's direction. Mutation asks
"does this suite notice a changed program", where a cached answer about an unchanged package is
correct. This gate asks "does this test agree with itself", where a cached answer is a tautology.

**The rerun is a separate process and never `-count=2` folded into run 1.** `-count=N` re-runs a
test inside one process, which cannot resample anything that process fixed once: the map hash
seed, a `sync.Once`, a package-level generator seeded in `init`, a port a listener was given, the
name of a temporary directory. This repository has already paid for that distinction. A test of
`treesitter.DeclaredExclusions` ranged over a six-entry `map[int]annotation.Declaration` and
asserted ascending order; it passed every local run and failed the mutation gate on CI, because a
map small enough for one bucket iterates as a rotation of a single cyclic order rather than as a
draw from all `n!` permutations. `testdata/map-iteration-order.txt` measures it: over twenty
processes, a six-entry map produced six distinct orders, every one a rotation of the same cycle,
and six ascending. Under `-test.count=2`, three of six processes drew the *identical* order
twice. A second iteration in the same process is not a second sample.

There is a second, independent reason, which holds even where `-count` would resample: folding
`-count=2` into the suite invocation reruns every test rather than the new ones, doubles the test
counts the JUnit report gives the ledger, and perturbs the coverage profile the baseline is
compared against. The gate must not change what run 1 measures.

Run 2 otherwise copies run 1's argv — `-race` included, since a race detector present in one run
and absent in the other makes the two outcomes disagree for a reason that is not the test's — and
drops the coverage flags, because a second profile written to the component's `coverage.out` would
overwrite the measurement the coverage gate is about to read.

## What two runs still cannot catch

A flake with a low per-run probability needs more samples than two, and this gate takes two: it
catches the coin that lands differently, not the one that lands wrong once in fifty. A flake that
depends on wall-clock timing, on machine load, or on another test running beside it may simply not
reproduce in an isolated rerun that takes a tenth of the time. A flake that is deterministic given
something both processes share — a file left on disk, a row committed to a service's database —
reproduces identically in both runs and reads as agreement.

The gate makes the narrow claim it can prove, and its wording says so: two runs disagreed. It
never says a test is random, and a passing `flaky` row never says a test is deterministic.

The converse is the one that will be argued about. Run 2 runs the new tests filtered and alone, so
**a test that only passes because another test ran first is reported as flaky.** That is
deliberate. Go gives no ordering guarantee across packages, `-shuffle` exists precisely because it
gives none within one either, and a test whose outcome depends on a neighbour is non-deterministic
in exactly the sense that matters. The finding names both runs' outcomes and the argv of the
second, so the author can reproduce it with one command rather than being told their test is
haunted.

`-shuffle=on` is refused for this slice. It would add an ordering sample cheaply, and it would add
a second axis on which run 2 differs from run 1 — every extra axis turns into a disagreement whose
cause the author has to guess at, and the seed would have to reach the finding for the rerun to be
reproducible at all. Ordering is a gate of its own if it is wanted.

## A test that fails both times is not this gate's business

The suite row already failed on it, and reporting the same defect twice under two labels gives the
author a second red row whose remedy is to fix the first. Two failures agree, so the gate reports
agreement and says nothing — the same shape [ADR 0027](0027-mutation-is-its-own-command.md) used
when it refused to report one broken suite as two red gates.

The rerun still happens when the suite failed. It is when the suite failed that the disagreement is
most likely and most valuable: a new test that failed the suite and passes alone is the gate's
whole subject, and a gate that skipped the rerun on a red suite would miss it every time.

## What could not be measured says so

Per [`a-gate-that-could-not-run-never-renders-as-one-that-passed`](../../.claude/rules/a-gate-that-could-not-run-never-renders-as-one-that-passed.md),
each of these is a count and a reason in the row's detail, never a silent drop and never a pass:

- **A new test run 1's report does not record.** Decided by absence from the report rather than by
  a parser guessing at build tags, `TestMain`, or a package the component's argv excludes — the
  report is what actually ran, and it answers all of those at once without lydite reimplementing
  any of them.
- **A new test skipped in both runs.** Its determinism was not examined. A skip is an outcome, so
  a test skipped in one run and run in the other *is* a disagreement and fails the gate.
- **A merge-base that will not resolve.** There is no "new" without it, and the whole row is
  `unmeasured`, the way `--gate-coverage` already fails towards a named cause.
- **A component whose runner is not Go.** `unmeasured`, naming the language. A repository that
  asked for the gate and got silence in two of its three languages must be able to see that it
  did. [ADR 0041](0041-a-new-test-is-rerun-in-rust-and-typescript-too.md) covers cargo-nextest
  and vitest on these same terms, leaving jest the one runner this answer still names.

## The flag, the row, and the finding

`--gate-flaky`, a sibling of `--gate-coverage`: explicit, because a gate inferred from "am I in
CI" is what [ADR 0018](0018-selection-widens-on-ignorance.md) already refused. Asking for it makes
the run write a JUnit report whichever variant it ran — through the pinned wrapper, even for the
plain variant that `--no-coverage` selects. ADR 0027 keeps that wrapper away from `go test` because
mutation pays for it once per mutant, thousands of times; here it is one extra process per
component, which is not the same trade.

One `flaky(<component>)` row per component, labelled the way `test(<component>)` is so no component
name can collide with a gate's:

| Row | When |
|---|---|
| `pass` — "no new tests" | the change declares no new test in this component |
| `pass` — "N new tests, 2 runs each" | every new test's two outcomes agreed |
| `fail` | at least one new test's outcomes disagreed |
| `unmeasured` | nothing could be examined — no merge-base, no report, not a Go component |
| `context` | `--gate-flaky` was not passed |

A run with both disagreements and unexaminable tests is `fail`, with the unmeasured count in the
detail: the strongest verdict owns the row and the weaker one still gets said.

Each disagreement is one `finding.Finding` with gate `flaky`, `Path` the test's file and `Line`
the `func TestX` declaration, so [ADR 0031](0031-a-located-finding-is-a-review-thread.md) puts it
on the line the author just wrote as a review thread. `Site` is the package directory and the test
name, which is what identifies the claim if the file is reformatted above it; `Ordinal` is zero,
since a name is unique within a package by the compiler's own rule. `Detail` carries both
outcomes and the rerun's argv.

## The rerun happens inside the component's run

Before the component's compose services are torn down, in the runner, not in a later pass over the
report. A service-dependent test rerun after teardown fails because nothing is listening, and the
gate would report the most reliable test in the repository as flaky — a false positive of the worst
kind, since the evidence handed to the author is a failure they cannot reproduce.

This inherits the converse honestly: run 2 meets the services in whatever state run 1 left them.
A new test that assumes an empty table will fail the rerun and be reported. It is, again, a test
whose outcome depends on running first.

## Mutation is not sequenced against this

[#22](https://github.com/lydite/lydite/issues/22) frames the gate as protecting mutation's inputs.
It cannot, and this slice does not pretend to: `lydite test` and `lydite mutation` are parallel
matrix jobs, and [ADR 0027](0027-mutation-is-its-own-command.md) made mutation wait on nothing on
purpose — it rebuilds its own baseline rather than consuming the test job's.

So the true statement is recorded rather than implemented: **a flaky new test invalidates that pull
request's mutation result**, and on a pull request with a red `flaky` row a surviving or killed
mutant is not evidence of anything. Sequencing the two jobs would cost every pull request the test
matrix's wall-clock time before mutation could start, which is the cost ADR 0027 paid the duplicate
baseline to avoid, and it would buy correctness only for the run where the gate already failed and
the author is already reading a red row. If that trade is ever worth revisiting it is its own
issue, with its own measurement.

## Consequences

- One extra `go test` process per package holding a new test. A change with no new tests pays
  nothing; the common change pays one filtered invocation whose duration is the new tests' own.
- A test that depends on another test's side effects, or on a service's leftover state, starts
  failing a gate it never faced. That is the gate finding something real, and it will read as noise
  to whoever meets it first.
- The `flaky` row is green on a change that modified an existing test into a coin flip. The set
  difference cannot see it, and nothing else in the pipeline can either.
- A component this slice's parser cannot enumerate renders `unmeasured` under `--gate-flaky`, and a
  repository with none it can gets an entirely amber gate — a truthful report of a gate that covers
  none of its code. [ADR 0041](0041-a-new-test-is-rerun-in-rust-and-typescript-too.md) settles what
  enumerating, filtering and reporting mean for cargo-nextest and vitest.
- The probe under `source/cli/internal/flaky/testdata/` is deterministically flaky, by a marker
  file rather than a clock or a generator. A probe that flakes at random would make the test that
  proves this gate the gate's own first false positive.
