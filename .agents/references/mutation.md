# Mutation: `lydite mutation`

> **The reference for `internal/mutation` and `lydite mutation`.**

Coverage measures execution. A test that calls a function and asserts nothing scores full marks on
every line it touches, which is exactly the test something optimising for a green pipeline
produces. A mutant is one deliberate change to one line, and the suite is asked whether anything
fails. `lydite mutation` is that question, per component, over the lines this change touched. See
[ADR 0027](../../docs/adr/0027-mutation-is-its-own-command.md).

**It is a peer of `scan`, `test` and `review`, not a flag on `lydite test`.** ADR 0016 puts every
check for one component in one job because they share a compilation; mutation does not — the
coverage gate builds the instrumented variant once, and mutation builds the plain variant once per
mutant. A flag would have made each test job serially longer by the most expensive thing lydite
runs. It waits on nothing either: a mutant against an already-red suite is meaningless, so a run
needs a passing baseline before it mutates anything, and the instrumented variant is both that
baseline and where the executed lines come from. So the mutation matrix runs *beside* the test
matrix and duplicates one instrumented run per component, which on a matrix costs wall-clock
nothing and machine time once.

**Mutants come from the change, and only from lines coverage reports as executed.** There is no
whole-repository mode: it would run for hours on any mature codebase, which makes it a mode nobody
runs, and it would give the catalogue and the gate a second scope to be reasoned about against. A
mutant on an uncovered line cannot be killed by construction, and reporting one restates what patch
coverage already said about the same line. Half of that bound is knowable before anything runs, so
a component the change does not touch pays for no baseline, no compose stack and no setup command —
which on the default branch is every component.

**Mutation has no baseline and writes nothing to the `lydite` branch.** There is no per-tree
quantity to record and nothing to compare against last time; the gate is absolute the way
`coverage.floor` is. `lydite test record` is untouched, and the single `gitstate.Write`
call site stays the one place a baseline is written.

**Four outcomes, and only one fails.** A **survivor** makes its component's row `✗` and the run
exit 1 — a Gate in CONTEXT.md's sense, cleared by writing the assertion that kills it. A mutant
whose run **hangs** counts as killed, because an infinite loop is a behaviour change something
noticed. An **unviable** mutant — one that does not compile — is excluded from the denominator
entirely and never counted as killed: it is evidence about the generator rather than about the
tests, and scoring it as a kill inflates the score silently and permanently. Telling it from a kill
is the whole reason a runner derives a build-only variant, since both exit non-zero. A component
whose **baseline suite fails** is `unmeasured`, not zero-killed — failing would report one broken
suite as two red gates whose second names a cause its author clears by fixing the first.

**A component declaring `mutation: false` gets one `context` row.** Present, so ADR 0026's
completeness rule holds with no exception and the fold needs no second copy of the opt-out rule to
know which absences are legitimate. `context` and not `unmeasured`, because the amber tag is for a
gate that could not run and spending it on a decision the repository stated is what teaches a
reader to skim past it.

## The acknowledgement lives in the source

An equivalent mutant is one no test could kill. Equivalence is undecidable, so lydite never tries
to detect one: the author declares it in a `[lydite:exclude_from_mutation][<reason>]` comment
**beside** the mutant. All three languages spell a line comment `//`, so one form covers them. The declaration
covers the mutants whose replaced *text* contains its line, and of those the ones replacing the
least — beside `println(a < b)` sit two mutants of the comparison and one that deletes the whole
call, and the innermost is what somebody annotating that line is looking at. Deciding by
containment is also what lets a declaration written inside a multi-line statement work. A
declaration that covers no mutant is named on stderr: its author believes they have answered a
survivor and nothing they can see says otherwise.

**It covers every mutant replacing that least amount, which for one operator is both its boundary
shift and its negation.** So a line whose boundary cannot be observed and whose negation can is a
line a declaration cannot honestly answer: it would acknowledge the killable mutant too, and stop
counting a kill the tests are still making. Such a line is answered by leaving no comparison to
shift: a clamp states itself as `min`/`max`, and a containment test whose edges only one node could
ever reach becomes a walk that prunes that subtree instead — which is why `visit` skips a test module
rather than `emit` filtering one out, and why `internal/mutation` and `cmd/lydite` write their clamps
the way they do. This is the cost of deciding coverage by containment rather than by operator, and it
falls on exactly one shape: a relational operator whose two ends are not equally observable.

The reason is required, and its absence is an error rather than a silent non-honouring. What counts
as a comment is each language's own parser to say, never a scan of the bytes: one scan would have
to lex three languages correctly to be right once. `internal/annotation` holds the token and the
rule, and is a **leaf** — `internal/referral` decides what merges unread and must not link a
language parser to obtain one string.

The composition is the point. The annotation is a suppression, and `internal/referral` reads
suppressions off the diff, so declaring a mutant equivalent clears the gate *and* refers the
change. Kill the mutant and merge unattended; declare it unkillable and a human reads the claim.
The bound that makes it structural rather than usually-true: a mutant exists only on a changed
line, so a declaration acknowledging one is itself on a changed line, which referral sees as added.

## Rust and TypeScript are parsed with tree-sitter

Go is parsed with `go/ast`, because the language ships its own parser and nothing could be more
faithful. The other two have no parser in the standard library, and lydite must stay a single
statically-linked `CGO_ENABLED=0` binary for four platforms — which every C-backed tree-sitter
binding rules out. `github.com/odvcencio/gotreesitter` is a pure-Go tree-sitter runtime, so the
grammar tables are the only input.

**The node types are the whole of what lydite knows per language**, held in one `grammar` table
each: the infix node whose `operator` field the three operator rewrites replace, the return node,
the statement node, which inner expressions a statement deletion applies to, the literal node types
and what each holds, and the line-comment node. A table rather than a traversal per language,
because the operators are the same five and only the names differ — a language needing its own walk
would be evidence the catalogue had stopped being one taxonomy. Every entry was verified against the
grammars themselves rather than read off documentation.

**A whole-statement deletion is restricted to a call, an assignment or an increment.** In both
grammars the statement node also wraps `if` and `return`, and deleting one of those is a
control-flow change that mostly fails to compile — an unviable mutant per branch, which is noise
about the generator rather than evidence about the tests. It is the same restriction Go's own
`ExprStmt`/`IncDecStmt` already imposes.

**`.tsx` gets its own grammar.** `<T>(x) => x` is a type assertion in TypeScript and an opening JSX
tag in TSX, so upstream ships two parse tables and a `.tsx` file read by the TypeScript tables is a
sequence of syntax errors. The grammar is therefore chosen by file extension and not by the
component's language.

**A tree carrying a syntax error is refused, not partly mutated.** tree-sitter recovers and returns
a tree either way, and a mutant taken from a misreading has a byte range an author cannot tell from
a real one. `ErrUnparsed` says so; an empty mutant set would render as a component whose suite
killed everything.

**Rust's inline test module is excluded from the tree, not from the path.** Rust puts unit tests in
a `#[cfg(test)] mod tests` inside the file they test, which no path rule can see; mutating one
reports an assertion nobody asserts as a survivor an author can answer only by declaring it
equivalent, and a gate that fires on ordinary work is one that gets switched off. The attribute is a
*preceding sibling* of the module in the tree, so the rule reads the tree's own order rather than
looking inside the module, and it compares the attribute with its whitespace removed — `#[cfg( test
)]` is recognised and `#[cfg(feature = "test")]` is not. A module behind a broader condition,
`#[cfg(all(test, unix))]`, is **not** recognised and is mutated: that is the direction this has to
fail in, since a form the rule does not know about produces survivors an author can see and answer,
where a looser match would silently stop mutating code that ships. `tests/` and `benches/` are
recognised by path as well, being whole files. TypeScript needs none of this and has the conventions
every runner lydite ships supports: a `.test.` or `.spec.` infix, and a `__tests__` directory.

**The golden fixtures are what hold the grammars.** `internal/mutation/testdata/` carries a Rust, a
TypeScript and a TSX fixture beside the exact mutant set each produces — offsets, operators and
replaced text, because a count alone passes on tables that have started reading a different node. A
grammar bump changes which mutants exist, and this repository declares no Rust component, so
`TestTheGoldenMutantsAreUnchanged` in `go test` is the only place its own CI can see that change
before it reaches a consumer. Regenerate with
`go test ./internal/mutation -run Golden -update`, and read the diff: it is the record of what the
bump changed.

`internal/mutation/sites.go` holds the two rules every generator owes whatever tree it walked — the
line bound, and which mutants a declaration covers. One collector rather than one per language,
because both rules are about the bargain rather than about the grammar, and the mutant a drifted
copy silently excluded is one nobody would ever see reported.

## Rebuild, never in-process

In-process was never available for Go: it compiles to a native test binary and has no bytecode
layer to rewrite in a live process, so the real choice was `go build -overlay` against copying
trees. The overlay wins, and that is why `Mutant.Apply` returns bytes rather than writing them —
nothing edits the component's own tree, so an interrupt cannot leave mutated source in the
repository lydite is measuring.

**The overlay is keyed on the path with its symlinks followed, and every command runs in that same
resolved directory.** The go command reads source through resolved paths and matches an overlay on
the path it read, so a key naming the same file by an unresolved route matches nothing: the compiler
reads the original and *every mutant survives*. That is the worst failure available here — the gate
fails correct code, silently, because a survivor is indistinguishable from a test that does not
assert. It is the common case rather than an exotic one, since macOS puts `/tmp` behind a symlink,
so any component under a temporary tree is reached through one.
`TestTheOverlayNamesTheFileTheCompilerWillRead` is what holds it.

**The suite is never given `-count=1`, and that is load-bearing.** An overlay changes the build
hash of the mutated package and its dependents and of nothing else, so exactly the right set
re-runs and the rest is served from the test cache: mutation gets incremental test selection
without lydite implementing any. Measured over five distinct mutants of one leaf package in this
repository, 1.67s each against 79s with the cache disabled — `-count=1` would multiply the cost of
mutation by roughly fifty.

**A mutant runs against its own package first, and against the closure only if it survives there.**
A mutant its own package's tests kill is killed, and nothing wider could change that; a mutant that
survives them has to be held against every test that could kill it, because a mutant in a library
is routinely killed only by its caller's tests. Ordering the two turns the expensive kill in this
repository from 76s into 2.36s and costs a survivor nothing, since the second run reads the first's
cached result for the package they share. Per-mutant cost is **bimodal**, and the discriminator is
not how many dependents a package has but whether the repository's slowest suite is among them: 5
dependents cost 1.67s and 8 cost 76s, of which 73 is one suite.

The narrowing rewrites the package patterns in the component's own argv and leaves every other
argument alone, so a flag and its separately written value survive. What makes that safe rather
than merely usually right is that **the narrowed run is never authoritative**: a mutant it fails to
kill is run again against the unnarrowed phase, so the worst a misread argument can cost is the
second run this exists to avoid — never a wrong verdict.

`internal/mutation`'s `Backend` is the language-agnostic seam: it opens one **worker** per
concurrency slot, and a worker stages one mutant at a time and returns the build-only invocation
and the phases. One worker per slot and never one per mutant — a tree copy, and for TypeScript a
dependency install, is not affordable per mutant. Go needs no directory at all, since an overlay
names the mutated file wherever it is written, and the same interface covers both. Opening one
takes the run's context, because for a JavaScript component it is an `npm ci`: an interrupt that
could not reach it would leave the run waiting on an install for a component it has stopped
mutating.

## Worker directories, and where containment is owed

Cargo and every JavaScript runner read source from the filesystem and take no instruction about
reading one file from somewhere else, so for Rust and TypeScript the mutated file has to exist as a
file. `internal/mutation.Tree` is that: **one directory per concurrency slot**, reused with the
original restored between mutants. Not one per mutant — a tree copy, and for a JavaScript workspace
a dependency install, is not affordable that often. Mutating the component's own tree in place is
faster than either and is rejected: an interrupt leaves mutated source in the tree lydite is
measuring, which is a repository somebody then commits.

**What gets copied is git's own file list** — tracked, plus untracked files git is not ignoring, the
list `internal/orphan` already reads. That is what keeps `node_modules`, `target`, `dist` and `.git`
out of the copy without lydite holding a second copy of a judgement `.gitignore` already states; the
copy that drifts is the one that starts copying half a gigabyte of build output per slot. The
runner's own `Prepare` then runs **in the worker**, once — a JavaScript workspace copied without its
`node_modules` fails at import, naming the tests rather than the absent dependencies.

**A worker holds the whole scan root, and the component's commands run at its own directory inside
the copy.** A component's build routinely reads a file above itself — the proving ground's npm
workspace imports a `docs/openapi.json` two levels up, and its `tally-cli` crate embeds the root
`VERSION` with `include_str!` — so a copy narrowed to the component fails to compile every one of
its mutants. The cost of getting that wrong is the worst shape available: an unviable mutant is
excluded from the denominator, so the component reports `unmeasured`, which does not vote, and the
run is green having examined nothing. Nothing in `go test` or in any run against this repository can
see that failure — Go needs no worker directory at all, and lydite declares no Rust component — so
`ci-end2end.yml`'s mutation probe is the only thing that catches it.

**The scan root and not the enclosing repository**, which is a real bound rather than an oversight:
`gitdiff.Tracked` asks git for the files under the directory it is given, and that is `--dir`. A
component's `dir` cannot escape the scan root, so nothing lydite is responsible for sits above it —
but a build that reaches further up is one lydite was not pointed at, and its mutants are unviable
for that reason. A repository whose components read files above `--dir` is scanned from the root
that contains them.

**Every write into a worker goes through its `os.Root`, the copy included.** The copy is where the
containment is hardest to see and easiest to lose: `checkPath` is lexical, so a listing naming a
path beneath a committed symlink is spotless, and an unconfined open follows the link and truncates
a file outside the worker. The root is therefore opened before the copy rather than after it — one
opened afterwards confines the one write that was never in doubt and none of the ones that are.

**Writes are confined, not checked.** A worker holds a copy of a scanned repository, so it holds
symlinks nobody vetted: a committed `evil -> /etc` makes `<worker>/evil/passwd` pass every prefix
comparison a lexical check can make, and the write then follows the link out. `os.Root`, opened on
the worker directory, refuses that at the syscall and leaves no window between resolving a path and
writing to it. `checkPath` is lexical and says so in its own comment — it stops a name that cannot
possibly be right from travelling this far, and establishes nothing about containment.
`internal/download`'s `safeJoin` is deliberately **not** the model: it is lexical too, and is
sufficient where it stands only because that code separately rejects absolute link targets,
containment-checks resolved relative ones, and unpacks into a directory it created rather than one
it was handed. `TestAWriteThatFollowsASymlinkOutOfTheWorkerIsRefused` asserts the syscall refused
it, not merely that something failed.

**Each mutant is applied to the component's own source rather than to the worker's copy**, so a
worker whose previous mutant was somehow not restored cannot compound one mutant onto another — and
a file that changed between generation and execution is refused by `Apply` rather than spliced at an
offset that now holds something else.

**A worker directory lives outside the component**, under the OS temporary directory: one inside the
tree being measured is a directory the component's own `./...` would compile, its own coverage would
report, and the orphan gate would see as source under no component.

**Workers do not share a build cache, and ADR 0027 says they do.** `target/`, `node_modules` and
`dist` are gitignored, so the copy excludes them and each worker starts cold — a full `cargo build`
or `npm ci` per slot rather than per mutant. What *is* shared is each toolchain's own package cache,
since `~/.cargo/registry` and `~/.npm` sit outside the copy, so dependencies are fetched once however
many workers there are; it is compilation that repeats. Closing it means pointing every worker at one
`CARGO_TARGET_DIR`, which cargo serialises on with a lock — so it trades N cold builds for one build
at a time, and which is faster is a property of the crate rather than something to guess at. It is
stated rather than chosen because this repository declares no Rust component and so cannot measure
it; the proving ground ([#95](https://github.com/lydite/lydite/issues/95)) is where a number could
come from.

**Rust and TypeScript get one phase, not two.** Neither has a unit both cheaper than the component
and derivable from a file path the way a Go package directory is: a crate needs its manifest read,
and a JavaScript test file is related to the source it exercises by convention rather than by
structure. A second phase that narrowed wrongly would cost the run it exists to save.

## One concurrency bound, and serial where services are shared

A component stays one scheduler item, so its compose stack is started and torn down inside that
item exactly as `lydite test` does, and two components publishing one host port are serialised
there. Its **mutants** dispatch against slots shared by the whole run — and so does each
component's own **baseline**, because a baseline is a suite execution exactly as a mutant is. A
bound counting only mutants would let three components in their baseline run beside a fourth
executing four mutants, which is seven suites in flight under `--concurrency 4`. With both counted,
`--concurrency` means suite executions in flight in `lydite mutation` exactly as it does in `lydite
test`. Two independent bounds would multiply into components times mutants, which is the quadratic
oversubscription `defaultConcurrency` is a constant rather than `NumCPU` to avoid.

**A component declaring compose services runs its mutants strictly serially.** ADR 0016 rejects
sharing a running service between concurrent suites — two suites against one database truncate each
other's tables — and eight mutants against one stack is that exactly; it would surface as mutants
surviving at random, so the score would vary run to run, which is worse than a slow one. The
question is asked of `scheduler.Conflicts` with two of the component's mutants as items rather than
of the port list directly, so the predicate that decides what may run beside what has one
implementation: they carry the component's published ports so they conflict exactly when it
publishes one, and they carry no directory, because a mutant is not a second tree.

## The timeout is derived, and there is no runtime budget

Nothing caps how long a run takes. A budget shipped now would be an invented number and every way
of exceeding one is bad: capping and passing is a gate that silently checked less, capping and
failing punishes a change for its size, and capping to `unmeasured` gives a busy repository a
permanently amber row. A run genuinely too large dies as a CI job timeout, the shard produces no
document, and the fold already fails a declared component with no row.

The **per-mutant** timeout is a different thing, and is a multiple of what this run measured: three
times the component's own observed baseline, with a 60-second floor for a suite too fast to measure
and `--timeout` overriding. Without one, `TimedOut` is an outcome nothing can produce.

## The fold

`lydite mutation merge` folds a matrix of shards, through the same implementation `lydite test
merge` uses: every shard reports exactly the components it was responsible for, so a declared
component with no row is a shard whose job died and one with two rows is two jobs running the same
work. That rule lives in `cmd/lydite/fold.go` with two consumers rather than in two copies that
agree until one learns something.

**The fold emits a `mutation` summary row, never `mutation(repo)`.** There is no repository-wide
figure only a fold can compute — `survived == 0` for every component is `survived == 0` for the
repository — so a gating row could only restate the conjunction of the rows above it. It is
`context`, gates nothing, and carries the counts and the elapsed time that make a later budget a
measured decision rather than a guess. A run responsible for only part of the declaration emits no
summary row, for the reason it emits no `coverage(repo)`.

It reads each component's score back out of the row the run rendered, because a report's rows carry
rendered prose and mutation writes no measurements document beside them — the same trade
`foldedScheduleRow` already makes for `max N concurrent`. `TestTheFoldReadsBackTheScoreARunRendered`
is what holds the renderer and the reader together, since a wording change would otherwise be a
fold that silently stops counting.

