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
beside the mutant. All three languages spell a line comment `//`, so one form
covers them.

Which mutants it covers is decided by what each one replaces, not by where each
is reported: the declaration covers the mutants whose replaced text contains its
line, and of those the ones replacing the least. Position alone cannot say what
an author meant, because one line holds mutants at several scopes — beside
`println(a < b)` sit two mutants of the comparison and one that deletes the
whole call. The innermost is what somebody annotating that line is looking at,
so a claim about an operator acknowledges the operator and leaves the deletion a
mutant they have not answered. Deciding by containment is also what lets a
declaration written inside a multi-line statement work, since it sits on no line
that statement opens or closes on.

A declaration that covers no mutant is reported. Its author believes they have
answered a survivor and nothing they can see says otherwise, which is the
failure a required reason already exists to prevent, arriving one step later.

It reaches no further, and that bound is what makes the composition below hold
rather than merely usually hold. A declaration already in the tree that covered
the line beneath it would acknowledge code a later change adds there: the mutant
is excluded and never run, while the change itself adds no line carrying the
token, so nothing refers it and it merges unread. Confined this way the property
is structural — a mutant exists only on a changed line, so a declaration that
acknowledges one is itself on a changed line, which `internal/referral` sees as
added.

What counts as a comment is each language's own parser to say, never a scan of
the bytes. One scan would have to lex three languages correctly to be right
once, and each case it got wrong would either honour a declaration nobody made
or drop one somebody did. `internal/annotation` holds the token and the rule for
reading a declaration out of comments a parser supplies; it is a leaf, because
`internal/referral` decides what merges unread and must not link a language
parser to obtain one string.

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

## What the executor settled

The measurements below are of this repository, on one machine, and are recorded
because ADR 0016's operator catalogue and this document's refusal of a runtime
budget both rest on cost being known rather than assumed.

**The test cache is load-bearing, and `-count=1` must never be passed.** Go's
plain variant is `go test ./...` with no `-count=1`, so a mutant's overlay
changes the build hash of the mutated package and its dependents and of nothing
else — exactly the right set re-runs and the rest is served from cache.
Mutation gets incremental test selection for free without lydite implementing
any. Measured over five distinct mutants of one leaf package: 1.67s each
(0.55s build-only, 1.12s test) against 79s with the cache disabled. Adding
`-count=1` would multiply the cost of mutation by roughly fifty.

**Per-mutant cost is bimodal, and the discriminator is which suite the mutated
package's dependents hold.** Not how many dependents there are: a mutant in a
package imported only by fast packages costs 1.67s, and one in a package
imported by this repository's slowest suite costs 76s, nearly all of which is
that one suite: it alone accounts for 73s of the 79s the module takes uncached.
A mutant is therefore cheap or nearly a whole suite, with little between.

**A mutant is run against its own package first, and against the rest only if
it survives there.** A mutant its own package's tests kill is killed, and
nothing further can change that answer; a mutant that survives them has to be
held against every test that could kill it, because a mutant in a library is
routinely killed only by its caller's tests. Ordering the two turns the
expensive case from 76s into 2.36s for a killed mutant and costs a survivor
nothing, since the second run reads the first's cached result for the package
they share. Kills are the common outcome in a tested repository, so this is the
common path.

**A component whose baseline suite fails is `unmeasured` and does not vote.**
The row says the suite did not pass. Failing instead would report one broken
suite as two red gates, the second naming a cause its author clears by fixing
the first. The cost is stated rather than hidden: a component flaky enough to
fail here and pass in the test matrix leaves mutation silently having examined
nothing, and the amber glyph is the only thing that separates that from a pass.

**A component declaring `mutation: false` gets one `context` row.** Present, so
[ADR 0026](0026-a-shard-reports-what-it-owns-and-the-fold-decides-completeness.md)'s
completeness rule holds with no exception and the fold needs no second copy of
the opt-out rule to know which absences are legitimate. It is `context` and not
`unmeasured` because the amber tag is for a gate that could not run, and
spending it on a decision the repository stated deliberately is what teaches a
reader to skim past it.

**Mutants are bounded by `--concurrency`, which is one number.** A component
stays one scheduler item, so its compose stack is started and torn down inside
that item as it already is; its mutants dispatch against the same limiter, and
`--concurrency` means suite executions in flight in `lydite mutation` exactly as
it does in `lydite test`. Two bounds would multiply into N components times M
mutants, which is the quadratic oversubscription `defaultConcurrency` is a
constant rather than `NumCPU` to avoid. Serial-where-services is asked of
`scheduler.Conflicts` with two of the component's mutants as items: they carry
its published ports so they conflict, and a component publishing none holds no
directory either, so they do not.

**The per-mutant timeout is derived from the measured baseline.** A multiple of
the component's own observed baseline elapsed time, with a floor for suites too
fast to measure, and `--timeout` overriding. The refusal of a runtime budget
above is a refusal to cap work at an invented number; a timeout that multiplies
something this run measured is not that, and without one `TimedOut` is an
outcome nothing can produce.

**The fold emits a `mutation` summary row, never `mutation(repo)`.** There is no
repository-wide figure only the fold can compute: `survived == 0` for every
component is `survived == 0` for the repository, so a gating row could only
restate the conjunction of the rows above it. The summary row is `context`,
gates nothing, and carries the counts and elapsed time that make a later budget
a measured decision.

**Worker directories are per concurrency slot, and they owe containment.** One
directory per slot rather than per mutant — a tree copy and, for TypeScript, a
dependency install per mutant is not affordable — reused with the mutated file
restored between mutants. Mutating the component's own tree in place is
rejected: it is faster than either and an interrupt leaves mutated source in the
tree lydite is measuring. `checkPath` is lexical and says so, and a lexical
check is not enough here: a worker directory is a copy of a scanned repository,
so it holds symlinks nobody vetted, and a committed `evil -> /etc` makes
`<worker>/evil/passwd` pass every prefix comparison before the write follows it
out. Writes are therefore *confined* rather than checked — `os.Root`, opened on
the worker directory, which refuses an escape at the syscall instead of in a
string, and leaves no window between resolving a path and writing to it.
`internal/download`'s `safeJoin` is deliberately not the model: it is lexical
too, and is sufficient there only because that code separately rejects absolute
link targets, containment-checks resolved relative ones, and unpacks into a
directory it created rather than one it was handed.

**The grammar blobs are selected by build tag, and the binary is 15MB rather
than 33MB.** gotreesitter embeds all 206 of its grammars by default, which is
20MB of parse tables in a tool that parses three languages. `grammar_subset`
turns the wildcard embed off and each `grammar_subset_<lang>` tag turns one blob
back on. The tags are in `.goreleaser.yml`, in both lydite workflows and in
`ci-test.yml` — and `ci-test.yml` runs the *suite* under them, because with
`grammar_subset` set a language whose own tag is missing fails at its first
parse rather than at build time, while a bare `go test` embeds everything and so
could never see one missing. A build without the tags is correct and larger,
which is the right way round: the failure is a fat binary, not a wrong verdict.

**A worker directory is a copy of git's own file list.** Tracked, plus untracked
files git is not ignoring — the list `internal/orphan` already reads. That is
what keeps `node_modules`, `target` and `dist` out of the copy without lydite
holding a second copy of a judgement `.gitignore` already states, and the copy
that drifts is the one that starts copying half a gigabyte of build output per
slot. The runner's own `Prepare` then runs in the worker, once, which is what
makes the copy affordable at all.

**Rust's inline test module is excluded from the tree, not from the path.** Rust
puts unit tests in a `#[cfg(test)] mod tests` inside the file they test, and no
path rule can see one. Mutating it reports an assertion nobody asserts as a
survivor whose only answer is an equivalence declaration, and a gate that fires
on ordinary work is one that gets switched off. The attribute is a preceding
sibling of the module in the tree, so the rule reads the tree's own order. A
module behind a broader condition — `#[cfg(all(test, unix))]` — is not
recognised and is mutated, which is the direction this has to fail in: a form
the rule does not know about produces survivors an author can see and answer,
where a looser match would silently stop mutating code that ships.

**Rust and TypeScript get one phase where Go gets two.** Neither has a unit both
cheaper than the component and derivable from a file path the way a Go package
directory is: a crate needs its manifest read, and a JavaScript test file is
related to the source it exercises by convention rather than by structure. A
second phase that narrowed wrongly would cost the run it exists to save.

**A golden-mutant test holds the grammars.** Rust and TypeScript are parsed
through a pre-1.0 dependency whose grammar tables are regenerated on a schedule,
and a bump changes which mutants exist. This repository has no Rust component,
so its own CI cannot otherwise see that change and it would reach a consumer
unobserved. Committed fixtures assert the exact mutant set — offsets, operators,
replaced text — in `go test`, which is what the merge gate blocks on.

## Considered and rejected

**Taking gotreesitter's default all-grammars build.** Nothing to forget and one
uniform `go build`, at the cost of nearly quadrupling lydite's download for every
consumer and every CI job that installs it. Rejected for the size; the build tags
are documented in three places and `ci-test.yml` executes the shape that ships.

**Vendoring the three grammar blobs into this repository.** It looks like the
smallest option and is not: the blobs are 365KB between them, and the 13MB is the
runtime and the grammar/scanner Go code, which both approaches link. The external
scanners live in the `grammars` package, so lydite imports it either way — and
importing it is what drags in the wildcard embed unless a build tag turns it off.
So it needs the same tag, saves 0.4MB, and buys three committed blobs Dependabot
would never bump.

**Hard-linking the component's tree into a worker instead of copying it.** Cheap
even for a `node_modules`, and unsafe: a suite that writes to a fixture in place
writes through the link into the tree lydite is measuring, which is a worse
version of the failure worker directories exist to prevent.

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
- Mutants are run by rebuilding. In-process was never available for Go: it
  compiles to a native test binary and has no bytecode layer to rewrite in a
  live process, so the real choice was `-overlay` against copying trees.
  `-overlay` wins and is why `Apply` returns bytes.
