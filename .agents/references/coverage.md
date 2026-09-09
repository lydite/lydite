# Coverage

> **The reference for `internal/coverage`, `internal/gitstate`, and every coverage gate `lydite test` applies.**

`lydite test` measures every component's coverage from the instrumented variant its runner
already derives, and `--gate-coverage` compares it against a baseline cached on a dedicated
`lydite` branch (never `main` — bot-owned generated cache data, not source, needs no PR/review and
never pollutes main's history). See
[ADR 0019](../../docs/adr/0019-coverage-per-component-gated-by-lydite-test.md).

**The component is the unit.** Nothing decides what a coverage unit is except the
declaration. Two altitudes are reported and both are gated: per component, and over the
repository — `coverage(cli)` and `coverage(repo)`. Both are `Σ covered / Σ total` over subsets of
the same stored per-component entries, so they cannot disagree or drift apart.

**A language is not an altitude**, and was one until
[ADR 0024](../../docs/adr/0024-coverage-gates-the-component-and-the-repository.md). It is a grouping
derived from each component's runner rather than a unit anybody declared, so gating one holds a
repository to a number it never stated — and where a language holds one component, which is the
common shape, the figure restates that component's own row exactly. Skipping it only in that case
was rejected too: a gate whose presence depends on how many components a language happens to have
is gated differently before and after an unrelated component is declared.

**A gating job holds a writable token and runs the repository's own code, and that is a real
tension** ([#49](https://github.com/lydite/lydite/issues/49)). Recording the baseline is a push to
the `lydite` branch; measuring runs each component's suite and any `setup`/`teardown` shell the
declaration carries. On a pull request that is the pull request's own code, in a job with a token
that can push anywhere.

**`lydite test` writes nothing to the `lydite` branch** — not the tree it measured, and not the
base tree it measured in a throwaway worktree. It reads the baseline, gates, and leaves what it
would record in `.lydite-reports/measurements.json`. `lydite test record --reports <dir>…` lands that,
executing no suite, no `setup`/`teardown` command and no compose service. One invariant, checkable
by grepping for the single `gitstate.Write` call site — the one place either a baseline or a
quality-history record reaches the branch.

**That narrows the tension and does not close it**, which is worth stating exactly because
[ADR 0022](../../docs/adr/0022-a-vendor-operated-app-and-an-oidc-relay.md) once claimed more than it
delivered about this same issue. A branch can edit its own workflow and its own component
declaration, so it can make the measuring job emit a fabricated measurement — and a command that
executes nothing verifies nothing. A poisoned *baseline* is worse than one bad verdict: it
persists, and every later change gates against it.

So the write still belongs on a tree that has already merged. `.github/workflows/lydite-baseline.yml`
is that job here: it runs on a push to the default branch, measures with `--gate-coverage` and lands
the candidate with `lydite test record`, holding `contents: write` and running code that has already
passed review and every gate. Both workflows shard: `plan` groups the components, a matrix measures
them, and one `record` job folds every shard's measurements. `lydite-pr.yml`'s `test` matrix holds
`contents: read`, runs `--affected --component <slice>`, and records nothing.

**What the command buys that the workflow split does not is the shard.** Under a shard matrix no
single process sees every component, so nothing can record a complete baseline — each shard measured
part of a tree and cannot claim the tree. Folding N documents is the only thing that can. Folding
needs the one fact no report carries: which entries a run *measured* and which it *carried forward*,
since each shard carries every component it did not run and only one copy came from a suite that
executed. `record` folds the `--reports` directories itself, because it runs post-merge in a workflow
`lydite test merge` does not precede.

`measurements.json` is that document. It holds each component's counts and producer — the baseline
candidate — plus its patch part and the baseline entry it was gated against, which are what
`lydite test merge` composes the repository-wide figures from.

**A candidate names the tree it measured, and `record` refuses to land it anywhere else.** It is the
only integrity property available to a command that executes nothing: without it a mis-wired workflow
lands one tree's numbers under another tree's key, silently, and that entry gates every later change
whose merge-base is that tree. `record` also re-checks completeness against the declaration read from
the tree being recorded — never from the candidate, which would let the document answer the question
its own completeness is in doubt about.

**A consumer wiring `--gate-coverage` and no `record` step stops recording.** Every run is then a
cache miss: correct, slower, and the `record` row says so.

**Measuring is local; gating touches the network.** `lydite test` always measures. `--no-coverage`
opts out of instrumentation entirely and emits no coverage row at all. `--gate-coverage` turns on
the baseline read, the comparison and the record. The flag is explicit, exactly as `--affected` is:
folding gating in unconditionally would have a developer's local run push to a shared branch, and
inferring "am I in CI" to avoid that is the thing [ADR 0018](../../docs/adr/0018-selection-widens-on-ignorance.md)
already refused for selection, for the same reason — every signal is unreliable where lydite runs,
and the caller already knows.

**A run that measured but did not gate renders distinctly from a pass.** The coverage rows are
`context` (`→`), never `pass` (`✓`), and the global row says so in its detail. Without that, a
workflow missing the flag reports exactly the green a gated run reports — the failure
wardnet/wardnet#957 shipped, where patch coverage never ran and the pull-request comment read as
though it had. `--json` carries the status as its own name, so a consumer separates the two without
parsing prose.

Instrumentation is on by default despite costing real time — `cargo llvm-cov` forces a separate
instrumented build, and Go's `-coverpkg=./...` recompiles every package per test binary. The fast
inner loop is `go test` or `cargo nextest` run directly; nothing reaches for lydite to re-run one
package. A gate that is opt-in is a gate that is off where it matters. `--no-coverage` is for the
case that genuinely wants the plain variant, and it emits no rows rather than a row per component
saying `unmeasured` — that would train readers to ignore the tag that exists to be noticed, the
same argument that keeps a types-only TypeScript package from being reported.

## A baseline is per-component counts, keyed by tree

`v4/<tree>.json` on the `lydite` branch, an object of component name to
`{covered, total, producer}`.

- **Counts, not percentages.** A percentage cannot be re-weighted: composing a language or global
  figure from components needs each component's size, and so does a run that measured only some of
  them. Storing percentages would make the language figure underivable from the components that
  produce it, leaving two quantities free to disagree.
- **Keyed by the component's name, not its directory.** `component.validate` enforces unique names
  and not unique directories, so the name is the only one of the two unique by construction.
- **No language is stored.** The component declaration already states it, and a second statement
  could only drift.
- **The producer is what wrote the report**, not what lydite believes it invoked: the Go toolchain,
  `cargo-llvm-cov` with the Rust toolchain whose LLVM wrote the line records, or a JavaScript
  workspace's own runner and coverage provider read back from `node_modules` after the install. It
  is compared verbatim, and a difference reports the component `new` rather than `regressed` — see
  [ADR 0025](../../docs/adr/0025-a-baseline-records-its-producer-and-only-record-writes-it.md).
- **`v4`, so every consumer takes one clean cache miss.** `gitstate.StatePath`'s directory is keyed
  to the metric and to the unit it is measured over; entries recorded under the old per-language
  percentages are a different quantity, and are simply never found. A gained field bumps it too
  whenever an entry written without it would read as a *hit* — a `v3` entry carries no producer, so
  it would match nothing, gate nothing, and never self-heal.
- **An empty baseline is never cached, and an empty cached baseline is a miss.** Cached, `{}` is
  indistinguishable from a real entry: every later change hits it, reports every component as `new`,
  and the gate enforces nothing — silently, permanently, with no way to self-heal. wardnet's branch
  accumulated nine of them. Treating one as a miss heals the already-written entries with no manual
  purge.

**A run on the default branch measures rather than reads.** There, HEAD is its own merge-base, so
the tree the run just measured is the tree a baseline would be read for. Reading it would miss on
the first build and measure the whole repository a second time, in a throwaway worktree, to
reproduce numbers already in hand. There is nothing to compare against either — the current commit
*is* the baseline — so the figures render the way an ungated run's do, and the candidate is left for
`lydite test record`.

**Keying by tree is what lets a pull request's own measurement become the baseline.** CI builds
`refs/pull/N/merge`, a squash merge lands a commit carrying that same tree, and the next pull
request's merge-base resolves to it — so the number a change measured *is* the baseline for the
commit it becomes, and a repository that records on pull requests needs no run on the default branch
at all.

**A repository that records read-only, as this one does, needs one.** Its pull-request job holds no
token that can push, so nothing there lands the candidate, and the chain has exactly one writer:
`lydite-baseline.yml` after the merge. It measures the tree the pull request already measured — the
cost of not trusting a branch with the write, and the reason the recorder failing silently is
expensive rather than harmless. Every run against an unrecorded base tree measures that tree again.

**A cache miss measures, and never substitutes.** `measureBaseTree` checks the base commit out into
a throwaway worktree — **at the scan root inside it, not at the worktree root** — and runs it
through the same path `lydite test` just took. A worktree holds the whole repository and the scan
root may sit below it, which is the shape `ChangedLines` and `selectAffected` already account for;
measuring at the worktree root instead finds no component declaration, hands back an empty
baseline, and the gate passes having compared nothing. Verified by injecting it: a change that
regresses coverage 16.7% and adds an untested line reports `test passed` with every row `new`.

**A base tree's configuration is read leniently**, through `config.LoadHistorical`, which parses and
merges exactly as `Load` does and validates nothing. Every validation exists to tell an author what
they wrote is stale, and the author of a historical tree is not being addressed and cannot act —
while the rejection is guaranteed to fire on the one tree it must not: the base tree of the pull
request that removes a retired key still carries it, and the metric version bump makes that run a
cache miss. Validating it would fail every migration and keep failing every branch cut before the
removal landed. Nothing read from a historical tree reaches a verdict; it supplies what the suites
need in order to run there, and the gate's own knobs come from the tree being gated.

A base tree lydite could not measure **completely** is not a baseline either, and is refused the
same way an empty one is. A bare worktree is where a measurement most often fails — no container
runtime for a component's services, an install that fails there — and a partial entry, once cached,
reads as a hit for every later change — so the compose
services each component declares actually start, each runner's `Prepare` runs, and the report read
back is the one the instrumented variant wrote. Invoking coverage tooling directly instead is what
produced a failed measurement for any suite needing a database, one of the roads to an empty
baseline cached as real. It reads the **base tree's own** `components.yml` and `config.yml`, never
the branch's: a component this change adds did not exist there, and one it renames is a different
component. Gating against the nearest ancestor that happens to have an entry was considered and
rejected — which number a change is judged against would depend on how far back history had one,
which is not reproducible from the change itself.

**A component the run did not *select* carries its baseline entry forward.** Under `--affected` most
components do not run, and recording only what was measured would drop them — every later change
would see them as `new` and gate them against nothing, permanently, since each run would drop them
again.

**Only that one, and the distinction is load-bearing.** A component that ran and failed produced the
same absence and means the opposite thing: its content may be exactly what changed, so its old entry
is a guess — and carrying it renders as a pass, so a language whose only component failed to build
would report that component's last good figure with a `✓` beside it. `measurement.Carryable` is set
in the one place that builds a measurement for a component this invocation never reached, so no path
through a run that did reach one can produce it.

A component the declaration no longer holds is not carried either: its entry dies with it rather
than leaving the baseline a tail of components nobody can measure.

**A composed figure names what it measured.** `72.5% (145/200 lines), 3 of 4 component(s), 1
carried forward`. A figure that does not say how much of it this run measured is indistinguishable
from one that measured everything. The baseline side of a composed comparison sums exactly the
components the current side covers — summing the whole baseline would compare this run's three
components against the base tree's four, so every narrowed run would read as a regression the size
of the component it did not run. A figure whose baseline does not cover every component in it is
reported as `new` rather than compared.

**Its denominator counts what could have been measured**, so a component nothing could ever
measure — a raw `command:`, a runner naming no report — is in neither number. `N of M` exists so a
partial run cannot read as a repository-wide pass, and an `M` counting one renders a complete run
as `1 of 2` while the `floor` row beside it says `1 of 1`: two rows in one report disagreeing about
the same repository, and the coverage row signalling a gap nothing left. The same distinction keeps
a base-tree measurement from warning about one on every cache miss, forever, about a state the
declaration states on purpose.

**A tolerated dip does not lower the baseline.** `coverage.tolerance` absorbs sub-tenth measurement
noise; recording a dipped number verbatim would turn it into an unbounded downward ratchet, each
change dipping by up to the tolerance and the next one measured from the lower floor. Within-tolerance
dips are restored to the baseline's own counts, capping total drift at one tolerance. A dip beyond
the tolerance is recorded as measured: it failed visibly on the change that introduced it, so
accepting it is a deliberate reset rather than leakage.

**A run that could not measure a component it was supposed to records nothing.** Recording a
partial baseline is worse than recording none: any non-empty entry reads as a cache hit, so the
missing component is `new` on every later change — and because a composed figure refuses to compare
unless the baseline covers every component in it, that language's row and the global row stop
gating too, silently. Recording nothing leaves the next change a clean cache miss, which measures
the base tree; that is slower and correct. A component nothing could ever measure — a raw
`command:`, a runner naming no report — does not block it: it contributes to neither side of any
comparison, so its absence is permanent and expected rather than a gap one run created.

**Only a passing component contributes a measurement.** A report written by a suite that failed,
was killed, or never started describes an unfinished run, and recording it would put a number in
the baseline nothing can be compared against honestly. The rule is enforced over the final rows, so
no path out of `runComponent` can forget it.

**A component declaring a raw `command:` is `unmeasured`**, with the reason said out loud. It has no
instrumented variant to ask for, and there is deliberately no key naming where its coverage lands —
that would be `coverage.{go,rust}.report` returning under a new name one decision after being
deleted. Excluding it instead would drop it from the composed figures silently, leaving a gate that
covered fewer components than the repository has reading as a complete one.

**Generated Go files are excluded from both the aggregate and the patch gate**, matched on Go's
`// Code generated ... DO NOT EDIT.` convention (<https://golang.org/s/generatedcode>) rather than a
filename pattern — the same signal golangci-lint and Codecov use. Without it the gate measures code
generation rather than testing: wardnet's regenerated REST client was 983 of one PR's 1007 changed
Go lines and pinned its SDK module's aggregate at 2%.

## `coverage.floor`: the one gate with no baseline

`coverage.floor` is the minimum any single measured component must reach. It has no baseline and no
comparison against last time — the aggregate asks "is this worse than it was", the floor asks "is
this below the bar", which the repository states once and every component meets or does not. That is
why it gates whether or not the run reads a baseline at all, and why ratcheting it against a prior
value is refused: it would make a component that has never had tests permanently acceptable, which
is the gap it exists to close. It defaults to `0` (off), so upgrading never starts failing a
repository over a gap it has always had.

**The unit is now the component**, coarser than the crate or package it gated before. That is a
change in what is measured rather than a weakening: an untested crate inside a workspace still
contributes its lines as uncovered and still drags its component's figure down in proportion to its
size. What a component-level floor cannot catch is a *small* untested sub-unit — the residual
[ADR 0007](../../docs/adr/0007-line-weighted-coverage-aggregation.md) identified when it introduced the
floor. A repository wanting crate-level floors declares those crates as components, which is a
statement about what it wants tested made in the file whose history records exactly that.

An unmeasured component is reported as `unmeasured` and never folded into the passing count, and
the summary line reads `N of M component(s)` — the two numbers differ exactly when something went
ungated, so a partial run cannot read as a repository-wide pass.

## Aggregation is line-weighted

A figure is the ratio of its components' **summed line counts**, never the mean of their
percentages. The mean is not a mild approximation: on a nine-package monorepo, one commit that added
a 39-line untested file to the smallest package while adding ~1,250 well-tested lines elsewhere read
as **−2.2** under the mean and **+0.44** by line count — the gate failed a change that improved
coverage, by five times the true magnitude, in the opposite direction. Widening
`coverage.tolerance` is explicitly not the fix: it exists for sub-tenth instrumentation noise, and a
tolerance wide enough to hide a 2.2-point artefact hides a genuine two-point regression too. See
[ADR 0007](../../docs/adr/0007-line-weighted-coverage-aggregation.md).

The language and global figures blend units that are not quite identical — a Go profile counts
statements where lcov counts lines, and the global figure blends across languages. This is accepted
and stated rather than hidden; the alternative is the mean ADR 0007 rejected.

**A component with no measurable lines is unmeasured, never 0%.** An empty Go profile, a crate
llvm-cov reports zero lines for, an lcov with no `LF` records — each is reported with its reason,
so no 0/0 reaches an aggregate and no floor comparison fails a component no work could clear.

## Patch coverage

Aggregate and patch coverage catch disjoint regression classes: aggregate catches coverage lost in
code the change never touches (a deleted test file — none of those lines are in the diff, so the
aggregate is the only gate that notices); patch coverage catches untested new code even when the
codebase is big enough that it does not move the aggregate. Neither bounds the other, so both run.

**Patch coverage gates per component, against that component's own aggregate baseline**
(`patch% >= baseline% - coverage.patch.tolerance`), from the same instrumented run — no second
execution, and no second artefact. Per component and not repository-wide: a change to a well-tested
component held to the repository's average is held to nothing, and one to a poorly tested component
is failed for reaching the standard it already has. A component with no baseline yet is reported
`new`, not failed. Its tolerance is deliberately independent of `coverage.tolerance`, so loosening
the noisy aggregate knob never weakens the untested-new-code check. It is opt-out per language:

```yaml
coverage:
  patch:
    go:
      enabled: false   # defaults to true
```

**Patch is composed at both altitudes the aggregate is** — per component and over the repository —
and both gate. The per-component rows and the aggregate between them still leave a hole: the aggregate says the repository did not get worse overall, and each per-component
row says that component's new code met that component's own standard, so a change adding untested
code to three components can clear every row on tolerance and still be the change that should not
merge. `patch(repo): 89.3% (740/829 new lines), 2 component(s), baseline 80.9%` is the row that answers
the question a reviewer actually has about a change spanning components.

Composed by **summing changed lines**, never by averaging the components' percentages — the same
weighting [ADR 0007](../../docs/adr/0007-line-weighted-coverage-aggregation.md) requires of the
aggregate, and for the same reason: a mean lets a two-line component outvote a two-hundred-line
one. The baseline side sums exactly the components the current side covers, and a figure whose
baseline does not cover all of them is reported `new` rather than compared — a component with no
baseline contributes its new lines to the numerator and nothing to the comparison, which would
render as movement nobody caused. A change touching no measurable line emits no row at all.

**A diff is scoped to the files a component's report could speak for**: under its directory, in a
language its runner implies. Both halves are needed — a repository declaring a Go and a TypeScript
component over one root would otherwise score each against the other's changed files. One
`ChangedLines` call serves every component, partitioned afterwards, since all of them measure the
same range.

**A component whose files the diff touched but which produced no per-line data is `unmeasured`,
never skipped in silence.** A silent skip reads as "patch coverage passed" in the pull-request
comment: wardnet/wardnet#957 shipped a green lydite summary that way while Codecov, fed the very
same lcov export, failed that diff. A stderr warning names the reason. A component the diff did not
touch stays silent — a row per untouched component is the noise that trains readers to skip the
rows that matter.

Changed lines come from a hand-rolled unified-diff hunk parser (`internal/coverage.ChangedLines`,
`git diff --relative --unified=0 <merge-base>..HEAD`) — deliberately not a diff library, since the
format needed is a small, stable subset (hunk headers + `+` lines). `--relative` matters: the
command runs in `--dir`, and every consumer of the changed-line map works in `--dir`-relative paths.
Without it git emits repository-root-relative paths, so with `--dir` pointing at a subdirectory
every changed file fails the prefix match and the patch gate silently measures nothing. The diff
runs through `executil.RunQuiet`: it is data this parses, not output anyone watches, and streamed it
lands in the middle of the report — and under `--json` in the middle of the document.

The parser does no language-aware filtering of comments, blank lines or imports — that happens when
changed lines are intersected with the report, since `PatchPercent` counts only lines the report
actually mentions.

**A Go coverage profile is the exception, and it bit us.** lcov lists only executable lines, so
"absent from the report" safely means "not executable, don't count it". A Go profile records
*blocks*, not statements — every line between a block's braces is in the report, comments and blank
lines included. The dividing line is the *format*, not the tooling: Vitest's default `v8` provider
is range-based exactly like Go's profile, and `llvm-cov`'s own text report does print counts beside
comment lines inside a function. Both still emit clean lcov (v8 maps ranges back onto statements via
`v8-to-istanbul`; `llvm-cov --lcov` only emits `DA:` for lines carrying a coverage segment) —
verified directly against both producers with a comment and a blank line inside an uncovered
function, neither of which appeared in the resulting lcov. So Go needs the filtering below and the
other two genuinely do not. Without it a comment added inside an uncovered function counted as an
uncovered new line, and a comment-only PR scored 0% patch coverage and failed the gate
(`wardnet/inforge#216`, whose entire diff was `nosemgrep` annotations and workflow YAML).
`internal/coverage.ParseGoProfile` therefore reads each profiled source file and drops blank and
`//`-comment lines before they ever reach `LineHits`. It deliberately does **not** track `/* */`
comments (that needs a lexer — `/*` inside a string literal opens nothing) or treat a leading `*` as
a comment continuation (`*p = x` is a pointer assignment): over-counting a rare block comment merely
understates patch coverage, while wrongly dropping a statement would let genuinely untested code
through the gate.

## The base branch

Every gate that measures a change against "before it" resolves one merge-base, through
`gitstate.BaseSHA`: the coverage baseline, `--affected`, `scan --diff-base auto` and
`review --base auto`. They must agree on which branch that is — a scan and a coverage gate
disagreeing about what this change contains is worse than either being wrong alone — so the flag,
the usage string and the resolution live in one place.

Resolution is explicit before discovered:

1. `--base-branch`, which is the caller's own statement.
2. `git symbolic-ref refs/remotes/origin/HEAD` — git's local record. Free and offline where a clone
   has it, and `actions/checkout` does not create it.
3. `git ls-remote --symref origin HEAD` — the remote's own answer. Authoritative, needs nothing set
   up locally, and is the step that actually resolves in CI.
4. Whichever of `main` and `master` the remote has, for a remote reporting no HEAD at all. Exactly
   one, or it is an error: a repository carrying both has not said which is the default.

Every failure names the flag. Falling back to `main` whatever the remote holds is what left a
repository whose default branch is `master` unable to run any of the four, each failing with a
merge-base error naming neither the cause nor the fix.

**Step 3 is what keeps step 4 from being that same guess wearing a different hat.** A repository
whose default is `develop` and which still carries a stale `master` has exactly one candidate, so
step 4 alone answers `master` with no diagnostic — silently measuring every change against a branch
nobody chose, which is worse than the hardcoded `main` it replaced. Refs are compared whole rather
than by substring, so a branch called `not-main` is not a candidate either.

**The remote stays `origin`, deliberately.** A repository with two remotes is real and lydite cannot
guess which one a pull request targets; discovering it would be a second inference with the same
failure mode and no flag to escape it. Naming the limit is better than half-solving it. What
actually fixes a stacked pull request is a caller passing the branch it targets, which
`lydite/actions` does: every job it runs takes `--base-branch` from `github.base_ref`, on
`pull_request` events and never on a push.

