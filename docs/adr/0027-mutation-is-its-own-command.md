# Mutation is its own command, and what it gates on

[ADR 0016](0016-components-and-lydite-run-tests.md) settled two things about
mutation and deferred the rest. It settled that lydite **owns** the engine in
every language rather than delegating to a tool per language, and that mutation
runs per component against the same declaration everything else reads. It
deferred the operator catalogue by name, to "the mutation slice, which sits on
top of this one". This is that slice.

Both settled points stand. What changes is where mutation runs: ADR 0016 put it
inside the component's test job, and that placement rests on a claim which is
true of scanning and coverage and false of mutation.

## Mutation does not share the test job

ADR 0016's reason for one job per shard containing all of its checks is that
"scanning, testing, coverage and mutation for that component share a job because
they share a compilation". Scanning and coverage do. Mutation does not: the
coverage gate builds the **instrumented** variant once, and mutation builds the
**plain** variant once per mutant. They share a checkout and nothing else.

So mutation is `lydite mutation`, a peer of `scan`, `test` and `review`, run as
its own matrix beside the test matrix rather than as a phase inside it. A flag
on `lydite test` would have made each test job serially longer by the whole cost
of mutation, which is the most expensive thing lydite runs — inheriting a
coupling whose only justification does not apply.

Nothing forces the coupling back. Mutation needs to know which lines coverage
reports as executed, which looks like a dependency on the test job's output, but
it needs a **passing baseline** before it can mutate anything at all — mutants
run against an already-red suite are meaningless — and ADR 0016 already names
the instrumented variant as "the coverage gate, and mutation's baseline". So a
mutation run executes the instrumented variant once for its own baseline and
reads the line coverage out of that same run. It waits on nothing.

The cost is one duplicated instrumented run per component. That is exactly what
the shared-compilation argument was trying to save, and full parallelism is what
it buys instead. On a matrix the two jobs run at once, so the duplication costs
wall-clock nothing and machine time once.

`lydite mutation` reuses `lydite test plan`'s matrix verbatim. A shard is the
conflict closure over published host ports and overlapping directories, and that
grouping is the same question whichever command consumes it, so there is one
planner with two consumers rather than a second one that agrees until it does
not. The fold is shared the same way: the completeness rule — every declared
component appears exactly once across the shards, a missing row fails and a
duplicate fails — is generic, so `lydite mutation merge` and `lydite test merge`
are two thin commands over one implementation.

## Diff-scoped always, and there is no baseline

Mutants are generated only from lines in `<merge-base>..HEAD`, sharing the scope
coverage and referral already resolve through `gitstate.BaseSHA`, and only from
lines coverage reports as executed. A mutant on an uncovered line cannot be
killed, and reporting it restates what patch coverage already said.

There is no whole-repo mode. It would run for hours on any mature codebase,
which makes it a mode nobody runs, and it would give the operator catalogue and
the gate a second scope to be reasoned about against.

A run with nothing to mutate — the default branch, where HEAD is its own
merge-base, or a commit whose tree matches its base — reports `unmeasured` and
passes. It must not report a pass: a green row from a gate that examined nothing
is indistinguishable from one that examined everything, which is the failure
this repository has already shipped once.

Mutation therefore has **no baseline**. There is no per-tree quantity to record,
nothing to compare against last time, and no reason to read or write the
`lydite` branch. It writes nothing to `measurements.json`, so `lydite test
record` is untouched and the single `gitstate.WriteBaseline` call site stays the
one place a baseline is written. The gate is absolute rather than comparative,
the way `coverage.floor` is.

## Four outcomes, and only one of them fails

A **survivor** — the code changed and every test still passed — makes its
component's row `✗` and the run exit 1. It is a Gate in CONTEXT.md's sense, and
the author clears it by writing the assertion that kills it, which is work that
improves the code. It is deliberately not a referral: a referral is for what an
author cannot clear, and an unkilled mutant is precisely what they can.

A mutant whose run **hangs** counts as killed. An infinite loop is a behaviour
change something noticed, and a per-mutant timeout is how that is observed.

An **unviable** mutant — one that does not compile — is excluded from the
denominator entirely and reported on its own line, never counted as killed. A
mutant nothing could build is evidence about the generator rather than about the
tests, and scoring it as a kill inflates the score silently and permanently.
Telling it from a kill is the whole reason the runner derives a build-only
variant, since both exit non-zero.

A component whose **baseline suite fails** is `unmeasured`, not zero-killed.
Nothing can be concluded about tests that were not passing before the mutation.

## The acknowledgement lives in the source

An equivalent mutant is one no test could kill, because the change it makes is
unobservable. Equivalence is undecidable in general, so lydite never tries to
detect one: the author declares it, in a `//lydite:equivalent <reason>` comment
at the site. All three languages spell a line comment `//`, so one form covers
them.

The reason is required and its absence is an error rather than a silent
non-honouring — an annotation quietly disregarded reads as the engine ignoring
the author. It is required at all for the reason `.lydite/exemptions.yml`
requires one: the annotation is the entire risk record for a mutant nobody can
kill, and a bare token is not reviewable.

The composition this buys is the point. The annotation is a suppression, and
`internal/referral` reads suppressions off the diff, so declaring a mutant
equivalent clears the gate *and* refers the change. Kill the mutant and merge
unattended; declare it unkillable and a human looks at the claim. The author
always has a way forward and never a way around, and — per
[ADR 0014](0014-evidence-only-referral-matching.md) — the annotation can add a
referral but can never remove one. It costs one token in `suppressionTokens`
beside `#nosec` and `biome-ignore`, and no new mechanism.

There is no file-level or function-level form. `mutation: false` in the
declaration is already the coarse control, and it lives where its history is the
review record; a broad in-code escape would be a second coarse control in a
place with no such record.

## The operator catalogue, and why it is fixed

Conditional boundaries (`<` ↔ `<=`), negated conditionals, arithmetic
operators, removed statements and replaced return values. Each is expressible as
a syntax-node rewrite in Go, Rust and TypeScript alike, so the single operator
taxonomy that justified owning the engine survives the catalogue intact.

It is not configurable. A catalogue a repository can empty is not a floor:
emptying it passes mutation trivially while every run still reports green, which
is the argument the built-in disqualifiers already win. It also makes a mutation
verdict mean the same thing in two repositories, which is what owning the engine
was for.

The cost accepted here is not runtime. Under a zero-unacknowledged-survivors
boolean an operator's real cost is its **equivalent-mutant rate**: a survivor
nothing can kill costs an author an annotation, a written reason and a referral.
Removed statements and arithmetic operators are the two largest sources of
equivalents in real code — deleting a log line is unobservable to any honest
test — and they are in the set anyway, because they are also the two the
literature rates highest for signal. What that trade actually costs is the
number the proving ground is there to produce.

## Mutants run concurrently, except where their suites would share a database

Go builds each mutant with `go build -overlay` and never writes the tree; Rust
and TypeScript get a worker directory each, sharing the build cache. Because no
mutant mutates shared state, mutants of one component are independent and run
concurrently up to `--concurrency`.

Except when their suites are not independent. ADR 0016 rejects sharing one
running service between concurrent suites — "two suites against one database
truncate each other's tables, and the failure is non-deterministic and reads as
a bad test" — and eight mutants against one component's compose stack is that
exactly. It would surface as mutants surviving at random, so the score would
vary run to run, which is worse than a slow one.

So a component declaring compose services runs its mutants strictly serially,
and one declaring none runs them concurrently. The rule is derived from
`scheduler.Conflicts`, which gains a third caller rather than a second
implementation.

## No runtime budget

Each component's row carries its mutant count and elapsed time, and nothing caps
either. A budget shipped now would be an invented number, and every way of
exceeding one is bad: capping and passing is a gate that silently checked less,
capping and failing punishes a change for its size rather than for being
untested, and capping to `unmeasured` gives a busy repository a permanently
amber row — the same failure in a different colour.

A run genuinely too large dies as a CI job timeout. The shard then produces no
document, and [ADR 0026](0026-a-shard-reports-what-it-owns-and-the-fold-decides-completeness.md)'s
fold already fails a declared component with no row. Loud, and by machinery that
exists. The reported timings are what make a later budget a measured decision
rather than a guess.

## Considered and rejected

**Delegating Rust and TypeScript to `cargo-mutants` and Stryker.** The original
scope for those two languages, and the cost argument for it is real: the pin
machinery, `internal/cargotool` and `internal/nodedeps` already exist, so
delegation's marginal infrastructure is close to zero, and both tools bring
diff-scoping and skip annotations of their own. It loses what ADR 0016 keeps —
one operator taxonomy, one acknowledgement model, one definition of a survivor —
and buys instead the job of normalising three foreign result models. The
acknowledgement composition would have survived delegation, since the
disqualifier reads the diff rather than the tool, so that was not the deciding
factor; consistency of the verdict was.

**A `--mutation` flag on `lydite test`.** Rejected above: it inherits a coupling
whose justification does not apply to mutation, and makes each test job serially
longer instead of adding jobs that run beside it.

**A central registry of acknowledged mutants.** It must key a mutant by file,
line and operator, and every edit above the site silently invalidates the claim
or transfers it to a different mutant. An in-code annotation moves with the code
it is about and is reviewable next to the claim it makes.

**A survivor as a referral rather than a failure.** Softer to adopt, and wrong
by CONTEXT.md's own definitions, which use this exact case to say what a Gate
is.

**Counting an unviable mutant as killed.** Simpler bookkeeping, and it inflates
the score by exactly the mutants the generator produced badly.

## Consequences

- ADR 0016's "one job per shard, containing all of its checks" no longer holds
  for mutation. It holds unchanged for scanning, testing and coverage, which do
  share a compilation.
- `lydite mutation` is the fifth command that writes a report document, so the
  standing pull-request comment gains a fourth section through the mechanism
  `lydite publish` already has, and needs no change to know about it.
- `.github/assert-proving-ground.py` gains a mode, and it is asserted against a
  **planted survivor** in each language: a function whose test calls it and
  asserts nothing, beside a properly tested neighbour. The assertion requires
  the planted mutant to survive and the neighbour's to be killed, so it fails if
  the engine generates nothing, kills spuriously, scopes the diff wrongly, or
  filters coverage wrongly. Counts alone would pass on an engine producing two
  trivial mutants, which is the vacuous assertion this repository has shipped
  before.
- `lydite/proving-ground` therefore changes too, in all three languages. That is
  the only place the three-language claim is falsifiable at all: this repository
  has no Rust component, so a Rust engine validated only here would merge with
  its argv asserted and never once executed.
- Whether to run mutants in-process or by rebuilding remains open and is to be
  benchmarked. `-overlay` shortens the odds for rebuilding: only the mutated
  package recompiles, and the build cache holds the rest.
