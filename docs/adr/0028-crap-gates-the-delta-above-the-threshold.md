# CRAP gates the delta above the threshold, and Go alone

Coverage measures execution and nothing else. A test that calls a function and
asserts nothing scores full marks on every line it touches, and a function
nobody can follow scores full marks by being called once. Mutation
([ADR 0027](0027-mutation-is-its-own-command.md)) answers the first half of
that; the second half is complexity, and lydite has gated on none.

The CRAP index is the pair, per function: `comp² × (1 − cov)³ + comp`, where
`comp` is cyclomatic complexity and `cov` is the fraction of the function's
statements a test executed. The cubed term makes it a cliff rather than a
slope — a complexity-12 function is 12 fully tested, exactly 30 half tested and
156 untested — which is what makes a single threshold arguable at all. 30 is the
value Savoia's 2007 definition names, and it is the value lydite uses.

It is a **Gate** in CONTEXT.md's sense rather than a Referral: the author clears
it by adding tests or by taking the function apart, and both of those are the
work you wanted.

## The delta, and not the count

The gate is that a change may not raise how many of a component's functions sit
above the threshold.

**Absolute was rejected.** A repository upgrading to a lydite that computes CRAP
has debt it has always had, and a gate that failed on the total would fail every
one of them on the day of the upgrade — over code nobody in that change wrote.
`coverage.floor` already follows this rule by defaulting to `0`: upgrading
lydite must never start failing a repository over a gap it has always had.

**Diff-scoping was rejected too**, and this is the less obvious of the two. A
change can push a function over the threshold *without touching it*, by
complicating a call site or by deleting the test that covered it. Both are
exactly the shape a change optimising for a green pipeline takes, and a
diff-scoped gate is blind to both — the same blind spot aggregate coverage
exists to cover, reintroduced one metric over.

Delta against a baseline is the only one of the three that grandfathers the
debt and still catches the change that adds to it.

**The worst value is recorded and never gated.** A change that takes the worst
function from 400 to 380 has improved nothing anybody can act on, and one that
adds a well-tested complex function raises it without adding a thing to fix. It
is a ledger scalar ([#26](https://github.com/lydite/lydite/issues/26)), not a
gate.

**Per component, and no repository-wide gate.** The per-component rule is
strictly the stricter one: a change that adds a function above the threshold to
one component and removes one from another fails there and nets to zero over
the repository. The `crap` row a whole run and a fold emit is `context`, and
carries the two ledger scalars summed.

## Go alone, and that is a property of the language

Complexity is free here: lydite is Go, walks `go/ast` in-process, and so needs
no tool, no pin, no install and no staleness risk. Per-function coverage is the
intersection of a function's line span with the per-line hits
`internal/coverage` already parses out of the profile the instrumented run
wrote — so there is no second run and no second artefact, which is the rule the
patch gate already follows.

Rust and TypeScript have no equivalent in hand, and finding one is research with
a real chance of ending at "hand-roll a cyclomatic walk per language"
([#17](https://github.com/lydite/lydite/issues/17)). A `lang`-shaped abstraction
invented now, from one implementation, would be an abstraction fitted to Go. So
`internal/crap` is a Go package that says so, and every other component gets one
`context` row naming the limit — present, because a component silently absent
reads as one that scored clean, and context rather than amber because nothing
about that repository could make the row green.

Cyclomatic complexity is counted the way gocyclo and golangci-lint's own cyclop
count it: one, plus every `if`, `for` and `range`, every non-default `case`,
every communicating `select` clause, and every `&&` and `||`. A number lydite
reports and a number a developer gets from either agree, which is most of what
makes a threshold arguable. A closure counts towards the function that declares
it, because the coverage half of the score is that function's whole line span
and contains the closure's lines; excluding its branches would score one span's
coverage against another span's complexity.

Generated files are excluded, and by reuse rather than by a second rule: they
are already absent from the hit map, because `internal/coverage` drops them on
Go's own `// Code generated ... DO NOT EDIT.` convention. Blank and comment
lines are gone from it for the same reason.

## Its own baseline document

The scalars live at `crap/v1/<tree>.json` on the `lydite` branch, beside — and
never inside — the coverage baseline at `v4/<tree>.json`.

**A wider `v4` entry was rejected, and the trap is worth stating.** An entry
written before the field is read back with the field at its zero value, so the
delta would be `current − 0` — the absolute count, failing every repository on
the day it upgrades. That is the exact thing the delta exists to prevent,
arriving through the mechanism meant to prevent it. Widening therefore forces a
`v5`, and a `v5` costs every consumer a full **coverage** cache miss for a
**CRAP** feature: two quantities with different producers and different costs to
re-measure, coupled so that every later change to one is a miss in the other.
`gitstate.StatePath`'s directory is already documented as keyed to the metric and
to the unit it is measured over, and this is that rule applied.

Separate documents, one writer. `gitstate.WriteBaseline` stages every metric's
document in one commit, so the single `WriteBaseline` call site
([ADR 0025](0025-a-baseline-records-its-producer-and-only-record-writes-it.md))
stays the only place a baseline reaches the branch, and a tree's state cannot
half-land.

**A CRAP miss does not measure the base tree.** Every repository has a coverage
baseline and no CRAP one the first time it runs a lydite that computes CRAP, and
re-measuring the base tree — every component's suite, every compose service —
for a metric that would gate nothing on that run would charge all of them for
it. The components report `new` and gate nothing for one change, which is the
shape a changed producer already has and readers already know. A coverage miss
*does* measure, and that measurement answers both metrics at once, since the
score is computed from the coverage report.

**The entry carries its producer**, for the reason a coverage entry does: the
coverage half of every score was taken by an instrument, and a toolchain that
counts statements differently moves every function's coverage. A difference is a
change of definition rather than debt anybody added, so it reports `new`.

**The threshold is not stored, and not configurable.** A knob added before
anyone has asked is a knob whose default is the only value anyone uses. Moving
it would change what the stored count means, which is a change to what is
measured — and so a bump of the metric's directory rather than a field beside the
number.

## Consequences

- `lydite test` scores every Go component it measures, and `--gate-coverage`
  compares the score against the baseline. The flag is named for coverage and
  now turns on two gates; what it means is "read the recorded state and compare
  against it", and the recorded state is one document per metric.
- `lydite test record` lands both documents in one commit. `lydite test merge`
  folds the per-component rows and emits the `crap` figure over the repository.
- A repository that records read-only, as lydite's own does, gets its first
  CRAP baseline from the run after the merge — so the first pull request
  following the upgrade reports every component `new`, and the one after it
  gates.
- Rust and TypeScript components carry a `context` row saying lydite scores Go
  alone, until #17 says otherwise.
