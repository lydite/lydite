# Mutation: `lydite mutation`

> **The reference for `internal/mutation` and `lydite mutation`.**

Coverage measures execution. A test that calls a function and asserts nothing scores full marks on
every line it touches, which is exactly the test something optimising for a green pipeline
produces. A mutant is one deliberate change to one line, and the suite is asked whether anything
fails. `lydite mutation` is that question, per component, over the lines this change touched. See
[ADR 0027](../../docs/adr/0027-mutation-is-its-own-command.md).

**Both commands run as Flow flows.** `cmd/lydite/mutation.go` and `cmd/lydite/mutation_merge.go`
parse flags, build a flow's `Params`, run it and render the result into rows; the stages
themselves live in `internal/stages/mutation` (`mutationstages`), the generic shard read
`lydite mutation merge` shares with `lydite test merge` lives in `internal/stages/shards`
(`shardstages`), and `internal/flows/mutation` (`mutationflow`) declares `New()` and
`NewRecord()` for `lydite mutation` and `NewMerge()` for `lydite mutation merge`. See
[`architecture.md`](architecture.md)'s "Mutation flows" section for the stage-by-stage account,
and [ADR 0065](../../docs/adr/0065-a-stage-reports-its-outcome-as-data-and-the-cli-alone-decides-the-rows.md)
and [ADR 0066](../../docs/adr/0066-shard-documents-are-read-by-one-generic-stage-both-folds-share.md)
for why the boundary between a stage and the CLI sits where it does. `mutants.json` — its
document, `ReadCounts`, `WriteCounts` and `FoldCounts` — lives in `internal/mutation/counts.go`,
and `cmd/lydite/mutants.go` keeps the aliases and wrappers (`readMutants`, `foldMutants`,
`writeMutants`) a caller outside the mutation command still uses.

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
whole-repository mode today: it would run for hours on any mature codebase, and it would give the
catalogue and the gate a second scope to be reasoned about against. A run that resumes and stops at
`--deadline` is what makes one affordable, and [ADR
0075](../../docs/adr/0075-a-mutation-run-resumes-and-stops-at-a-deadline.md) decides a scheduled
default-branch sweep that only ever advises. That sweep is decided and not built. A
mutant on an uncovered line cannot be killed by construction, and reporting one restates what patch
coverage already said about the same line. Half of that bound is knowable before anything runs, so
a component the change does not touch pays for no baseline, no compose stack and no setup command —
which on the default branch is every component.

**Mutation has no baseline of its own to gate against.** There is no per-tree quantity to compare
this run against last time's; the gate is absolute the way `coverage.floor` is. It still reaches
the ledger, through a different door: a post-merge run's `mutants.json` is one more document
`lydite test record` reads, alongside `measurements.json` and `scan.json`, into
`Component.Mutation` — and the single `gitstate.Write` call site stays the one place any of it
lands on the `lydite` branch. See [`quality-history.md`](../../agentic/references/quality-history.md)
and [ADR 0043](../../docs/adr/0043-mutation-reaches-the-ledger-from-a-post-merge-run.md).

**Five outcomes, and only one fails.** A **survivor** makes its component's row `✗` and the run
exit 1 — a Gate in CONTEXT.md's sense, cleared by writing the assertion that kills it. `--no-gate`
turns off exactly that vote: a completed component's row renders `StatusContext` under the flag,
whether or not it had survivors — never `StatusPass` for a clean run, since nothing gated it, and
never `StatusFail` for a run with survivors, since the exit code is not asked to speak for them.
Recording, `mutants.json` and the findings in `--json` and the document are identical with or
without the flag; only the row's vote changes. Everything in the "could not run" family — a
variant that is not runnable, an unresolvable base revision, a baseline that did not pass, a
component with no headroom under the memory bound, a run interrupted before it finished — fails or
renders `unmeasured` exactly as it does without the flag, because those rows were never voting on a
survivor to begin with. See
[ADR 0048](../../docs/adr/0048-a-post-merge-mutation-run-records-its-survivors.md). A mutant
whose run **hangs** or whose run **allocates without stopping** both count as killed: a runaway is
a behaviour change something noticed, whether the harness caught it by clock or by peak memory,
and the two are kept as separate outcomes — `TimedOut` and `OutOfMemory` — rather than folded into
one, because a suite full of either is worth seeing even when every one of them scores correctly.
An **unviable** mutant — one that does not compile — is excluded from the denominator entirely and
never counted as killed: it is evidence about the generator rather than about the tests, and
scoring it as a kill inflates the score silently and permanently. Telling it from a kill is the
whole reason a runner derives a build-only variant, since both exit non-zero. A component whose
**baseline suite fails** is `unmeasured`, not zero-killed — failing would report one broken suite
as two red gates whose second names a cause its author clears by fixing the first.

**A component declaring `mutation: false` gets one `context` row.** Present, so ADR 0026's
completeness rule holds with no exception and the fold needs no second copy of the opt-out rule to
know which absences are legitimate. `context` and not `unmeasured`, because the amber tag is for a
gate that could not run and spending it on a decision the repository stated is what teaches a
reader to skim past it.

## The acknowledgement lives in the source

An equivalent mutant is one no test could kill. Equivalence is undecidable, so lydite never tries
to detect one: the author declares it in a `[lydite:exclude_from_mutation][<reason>]` comment
trailing the mutated line — the same line, never the line above it or below it. Go, Rust,
TypeScript and TSX spell a line comment `//`; Python spells it `#`. `annotation.body` strips
either, so one function still covers every language's declaration line — mutation, crap and
coverage all read the same declaration through it. A formatter that relocates a trailing comment
off its own line (Biome does this to a line ending in `{`) needs a `// biome-ignore format`
comment above it, or the declaration lands on a line the mutation gate never reads it from.

**A declaration covers every mutant whose replaced range contains its line and is innermost among
those: no other such mutant's range sits strictly inside theirs.** Position alone cannot tell what
an author meant, because one line holds mutants at several scopes — beside `println(a < b)` sit two
mutants of the comparison and one that deletes the whole call. The call's range encloses the
comparison's, so the declaration acknowledges the operator, and deleting the call remains a mutant
its author has not answered. Innermost is decided by containment, never by width: mutants replacing
the same range — an operator's boundary shift and its negation — are all innermost, and so are
mutants replacing disjoint ranges on the line, since neither encloses the other. `i <= n + 1` has a
comparison mutant and an addition mutant on disjoint ranges, so one declaration answers both,
however many bytes each replaces. Reaching by containment rather than by a window of lines is also
what lets a declaration written inside a multi-line statement work: it sits on no line the statement
opens or closes on, and it is still written inside it. A declaration that covers no mutant is
counted as `Unmatched` on the component's `mutation.Summary` and named on the row itself as
"N declaration(s) cover no mutant" — its author believes they have answered a survivor and nothing
lydite can see says otherwise, and it gates nothing.

**A relational operator whose boundary shift and negation are not equally observable is a line one
declaration cannot honestly split.** Acknowledging either acknowledges both, since both mutants
replace the same range and are therefore both innermost — so a line where only one of the two can
be killed is answered by leaving no comparison to shift: a clamp states itself as `min`/`max`, and
a containment test whose edges only one node could ever reach becomes a walk that prunes that
subtree instead — which is why `visit` skips a test module rather than `emit` filtering one out, and
why `internal/mutation` and `cmd/lydite` write their clamps the way they do. This is the cost of
deciding coverage by containment rather than by operator, and it falls on exactly one shape: a
relational operator whose two ends are not equally observable.

The reason is required, and its absence is an error rather than a silent non-honouring. What counts
as a comment is each language's own parser to say, never a scan of the bytes: one scan would have
to lex four languages correctly to be right once. `internal/annotation` holds the token and the
rule, and is a **leaf** — `internal/referral` decides what merges unread and must not link a
language parser to obtain one string.

The composition is the point. The annotation is a suppression, and `internal/referral` reads
suppressions off the diff, so declaring a mutant equivalent clears the gate *and* refers the
change. Kill the mutant and merge unattended; declare it unkillable and a human reads the claim.
The bound that makes it structural rather than usually-true: a mutant exists only on a changed
line, so a declaration acknowledging one is itself on a changed line, which referral sees as added.

## Rust, TypeScript and Python are parsed with tree-sitter

Go is parsed with `go/ast`, because the language ships its own parser and nothing could be more
faithful. The other three have no parser in the standard library, and lydite must stay a single
statically-linked `CGO_ENABLED=0` binary for four platforms — which every C-backed tree-sitter
binding rules out. `github.com/odvcencio/gotreesitter` is a pure-Go tree-sitter runtime, so the
grammar tables are the only input.

**The shipped build is a `grammar_subset`.** `gotreesitter` embeds all 206 of its grammars
unless the `grammar_subset` build tag turns the wildcard embed off, and each
`grammar_subset_<lang>` tag turns one blob back on — 15MB against 33MB for the same binary. Five
tags ship today: `grammar_subset_rust`, `_typescript` and `_tsx` for mutation's own grammars, and
`_python` for both mutation's and crap.md's Python walk (a ~60KB blob) — a single tag serves both
gates, since neither owns the tables. A build without them is correct and larger; a build with
`grammar_subset` and a language's own tag missing panics at that language's first parse, which is
why `ci-test` runs the suite under the exact tag list `go build` uses (see the root
[`AGENTS.md`](../../AGENTS.md) Commands section).

**The node types are the whole of what lydite knows per language**, held in one `grammar` table
each: `binary` is a small table of node type, field name and arity — Rust and TypeScript each write
one infix node type with a singular `operator` field, and Python splits the same rewrites across
three: `binary_operator` and `boolean_operator`, each with its own singular `operator` field, and
`comparison_operator`, whose `operators` field holds one token per link of a chain, so `a < b < c`
is one node yielding two independent mutants rather than one covering an ambiguous span. The table
also holds the return node, the statement node, which inner expressions a statement deletion applies
to, the literal node types and what each holds, and the line-comment node. A table rather than a
traversal per language, because a language needing its own walk would be evidence the catalogue had
stopped being one taxonomy — field and arity is as far as that catalogue has had to bend so far. Six
operators exist today: the three shared by every grammar (`ConditionalBoundary`,
`NegateConditional`, `ArithmeticOperator`), Python's own `ConditionalConnective` for its `and`/`or`
swap (reported under its own kind rather than as a relational shift, since it inverts no
comparison), plus `RemoveStatement` and `ReplaceReturn`. A conjunct-removal mutant (`a and b` → `a`)
was considered and deliberately not shipped: Python's short-circuit semantics make it equivalent far
more often than a token swap, dominated by the operator's own semantics rather than by a coverage
gap — see
[ADR 0054](../../docs/adr/0054-pythons-operators-join-the-mutation-catalogue-by-field-arity.md).
Every entry was verified against the grammars themselves rather than read off documentation.

**A removable statement is reached through its wrapper, or as its wrapper's collapsed remnant.**
Rust's and TypeScript's `expression_statement` always survives as a node to delete; Python's own
tree-sitter runtime collapses one holding a single named child into that child, so an ordinary
call, assignment or augmented assignment ends up a direct child of `module` or `block` instead of
wrapped. `statementParents` names those parent node types for a grammar whose wrapper does not
always survive, and a removable node reached that way is matched by position (a direct child of one
of them) rather than by finding a wrapper that is no longer there.

**A whole-statement deletion is restricted to a call, an assignment or an increment.** In every
grammar, whatever wraps a statement — Rust's and TypeScript's `expression_statement`, or the
`module`/`block` parent a collapsed one leaves behind in Python — also carries `if` and `return`,
and deleting one of those is a control-flow change that mostly fails to compile — an unviable
mutant per branch, which is noise about the generator rather than evidence about the tests. It is
the same restriction Go's own `ExprStmt`/`IncDecStmt` already imposes.

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

**The classification lives in `internal/treesitter`, not here.** `Grammar.TestFile` and
`Grammar.TestModule` are the one answer both this gate and `internal/crap` ask: mutating an
assertion and scoring one are both "is this the suite", and a second, gate-local copy would agree
with this one only until somebody edited it for one gate's own reason. See
[`crap.md`](crap.md) for the other caller.

**The golden fixtures are what hold the grammars.** `internal/mutation/testdata/` carries a Rust, a
TypeScript, a TSX and a Python fixture beside the exact mutant set each produces — offsets,
operators and replaced text, because a count alone passes on tables that have started reading a
different node. A grammar bump changes which mutants exist, and this repository declares no Rust
component, so `TestTheGoldenMutantsAreUnchanged` in `go test` is the only place its own CI can see
that change before it reaches a consumer. Regenerate with
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

**`lydite test --gate-flaky`'s rerun mandates `-count=1`, and that is not a contradiction of this
rule but its mirror image.** Mutation wants the cache because an overlay invalidates exactly the
mutated package and its dependents, and nothing else changed. The flaky gate wants to defeat the
cache because its second run is, by construction, a cacheable repeat of a suite that just ran —
without `-count=1` Go would serve the first run's own result and the gate would report determinism
having executed nothing. See
[ADR 0039](../../docs/adr/0039-a-new-test-is-rerun-once-in-its-own-process.md) for the detail;
the two rules are stated once each so that neither gate gets "corrected" to match the other.

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

Cargo, every JavaScript runner and pytest read source from the filesystem and take no instruction
about reading one file from somewhere else, so for Rust, TypeScript and Python the mutated file has
to exist as a file. `internal/mutation.Tree` is that: **one directory per concurrency slot**, reused
with the original restored between mutants. Not one per mutant — a tree copy, and for a JavaScript workspace
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

**Rust, TypeScript and Python get one phase, not two.** None of the three has a unit both cheaper
than the component and derivable from a file path the way a Go package directory is: a crate needs
its manifest read, a JavaScript test file is related to the source it exercises by convention rather
than by structure, and Python has no compiled unit narrower than the component either. A second
phase that narrowed wrongly would cost the run it exists to save.

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

## The timeout and the memory ceiling are both derived, and a deadline is not a runtime budget

Nothing caps how long a run takes, and nothing is failed for its size. A budget would be an
invented number and every way of exceeding one is bad: capping and passing is a gate that silently
checked less, capping and failing punishes a change for its size, and capping to `unmeasured` gives
a busy repository a permanently amber row. `--deadline` is a different thing: measured from the
process's start and set below the job timeout, it stops dispatch, cancels the mutants in flight and
keeps every verdict already recorded, so a rerun resumes from them and the amber row lasts only
until then. A run with no `--deadline` that is too large dies as a CI job timeout, the shard
produces no document, and the fold already fails a declared component with no row.

What a run does instead is state its cost before paying it. Once mutants are generated it writes
one line to the component's live log, mirrored to stderr under `--stream`: `N mutant(s), budget Xs
each, W worker(s): at most Ys` (`costProjection` in `cmd/lydite/mutation.go`). It is a worst case,
`ceil(mutants/workers)` times the per-mutant budget, and a projection rather than a cap: it tells the
reader what the run would cost, and only `--deadline` stops one. It survives
a killed job because the log does and the final document does not. Elapsed time (baseline plus
every mutant) is recorded afterwards as `elapsed_seconds` in each component's entry in
`mutants.json`.

**Each mutant also writes its own `start` line before it runs, not only a line when it finishes.**
A mutant cancelled or timed out mid-run leaves no finish line at all, so the start line is the only
trace in the log of what was in flight when the run stopped — `lydite mutation merge`'s
`progressDetail` reads exactly this to name, on a shard with no report at all, the mutants that
started and never finished against how many finished cleanly. A log naming no mutant is a run that
stopped before the first one: a build, an install or a baseline that never returned.

The **per-mutant** timeout is a different thing, and is a multiple of what this run measured: three
times the component's own observed baseline, with a 60-second floor for a suite too fast to measure
and `--timeout` overriding. Without one, `TimedOut` is an outcome nothing can produce.

**A mutant's suite runs as the leader of a process group of its own, and a timed-out context kills
the whole group, not only the process the executor started.** A suite is a tree — npx, node, its own
worker processes — and a deadline that reaches only the root leaves the rest of that tree holding
the output pipes open, so the wait for them never returns and the worker never frees its slot. The
signal goes to the negative pgid, reaching every member at once, and `WaitDelay` bounds how long
`Wait` keeps reading a grouped command's output after that signal before giving up on it, so a
descendant that ignores the signal cannot hang the mutant executor forever either.

**The per-mutant memory ceiling is derived the same way, off the same baseline run, and bounds a
different failure.** It is four times the baseline's own peak — `Result.MaxRSS` — with a 2GiB
floor and `--memory` overriding. The multiplier is larger than the timeout's three because memory
is the less elastic of the two: a suite is routinely slower under a mutation and is rarely much
larger, so a ceiling this close to the baseline is still generous. It also has to be, because the
error it can make is one-sided in a way the timeout's is not — a bound too tight kills a mutant
nothing about the tests killed, which is an inflated score that is permanent and silent, where a
false survivor merely costs an author the afternoon spent tracing it. A baseline whose own peak
would not fit under its derived ceiling is reported `unmeasured` rather than run at all, for the
same reason a `mutation: false` component gets a row that says so instead of one that quietly
passed: every mutant would then die of the bound before its tests ran. See
[ADR 0027](../../docs/adr/0027-mutation-is-its-own-command.md) for the measurements the
multipliers were chosen against.

**The ceiling is a Linux thing.** It is applied through `RLIMIT_DATA`, which Darwin's `setrlimit`
refuses at any value with `EINVAL` — so on that platform a mutant still runs under the timeout but
under no memory bound at all, and the row says so with a note rather than staying silent about it:
a bound quietly not applied would render exactly the green of one that held.

## Resume

A run keeps a **mutation state** per component and a rerun measures only what is missing. It is a
**Cache**, never a **Ledger**: losing it costs time and nothing else. See
[ADR 0075](../../docs/adr/0075-a-mutation-run-resumes-and-stops-at-a-deadline.md) and the rule
[`a-state-consulted-to-skip-work-is-a-cache-never-a-ledger.md`](../rules/a-state-consulted-to-skip-work-is-a-cache-never-a-ledger.md).

**What it holds.** The baseline (pass or fail, elapsed time, peak RSS, the executed lines that
select where mutants go) and one line per mutant verdict, appended the moment it is decided
(`internal/mutation/state.go`). A component's directory under the state root is its escaped name
(`mutation.StateDir`), so a name containing `/` or `..` never escapes the root.

**Where it lives.** The state root resolves flag, then environment, then the user cache:
`--state-dir`, then `LYDITE_MUTATION_STATE`, then `os.UserCacheDir()/lydite/mutation/<sha256 of
the absolute scan root>`, so two checkouts never share state. A machine with no resolvable cache
directory runs with resume off and says so on stderr.

**The fingerprint** (`stateFingerprint` in `internal/stages/mutation/run.go`) hashes the tree
digest (`internal/treedigest`, the contents of every path git knows under the scan root), the
component and its three runner variants, the provisioned toolchains' key, lydite's version (a
dev build is named by a hash of its executable), `--timeout`, `--memory`, the platform, and the
environment the baseline and the suite run under, hashed and never written. Every field is
length-framed. The base is left out: it decides which mutants are wanted, never what one answers.
A component keeps only its latest fingerprint, so a state never needs pruning.

**The tree digest's scope is deliberate.** It covers every file git lists under the scan root —
tracked plus untracked-and-not-ignored, minus the state root — not only the selected components'
directories, because a verdict can depend on any file the suite reads, and a narrower key would
reuse a verdict after a file outside the component changed. The cost is one listing and one hash
of the tree per run: tens of milliseconds for a thousand files, small next to one suite execution.

**What is recorded.** Only a fresh verdict the run decided itself, and never a mutant cut short by
cancellation (`Result.CutShort`). A recorded baseline is reused, so the per-mutant budget a
resumed run derives is the one the recorded verdicts were judged against. An **acknowledged**
mutant is answered by its declaration (see "The acknowledgement lives in the source"): it is
never recorded, and never counted among the reused verdicts, so removing an acknowledgement takes
effect on the next run. The report says how many verdicts were reused.

**`--fresh`** discards each component's state before it runs.

**The state root is kept out of the tree digest.** The root can sit inside the scan root, so
`ScopeChange` writes a `.gitignore` containing `*` into it and drops any path beneath it from the
listing; a digest that included the state would change with every verdict recorded.

**A state failure is a diagnostic, never a failure.** A state that cannot be opened, read or
written is reported and the component measures everything, as it would with no state.

**An incomplete run exits 3.** A component `--deadline` stopped before every mutant had a verdict
renders `unmeasured`, "N of M measured, rerun to resume", and so does one the deadline reached
before its baseline or before it started. `unmeasured` does not vote, so `cmd/lydite` marks the
report incomplete (`ui.Report.MarkIncomplete`) and the verdict becomes `ui.VerdictIncomplete`,
exit `ui.ExitIncomplete` (3) — under `--no-gate` too, since that flag silences a survivor's vote,
not a measurement that never finished. A failure outranks it: a survivor found before the deadline
fails its row, is never withdrawn (a deadline is not an interrupt), and the run exits 1. Exit 3 is
a public contract a workflow reads to tell a run cut short, which a rerun resumes, from one that
failed.

## The fold

`lydite mutation merge` folds a matrix of shards, through the same implementation `lydite test
merge` uses: every shard reports exactly the components it was responsible for, so a declared
component with no row is a shard whose job died and one with two rows is two jobs running the same
work. That rule lives in `cmd/lydite/fold.go` with two consumers rather than in two copies that
agree until one learns something.

For a component with no row the fold says only that. When the component's uploaded mutation log
survived and holds the projection line, it also quotes that line verbatim as what the run said it
was about to cost. It never names a cause: a job killed at its timeout, a runner OOM and a failed
upload leave identical absence, so a fold saying "too large" would be a guess.

**The fold emits a `mutation` summary row, never `mutation(repo)`.** There is no repository-wide
figure only a fold can compute — `survived == 0` for every component is `survived == 0` for the
repository — so a gating row could only restate the conjunction of the rows above it. It is
`context`, gates nothing, and carries the counts and the elapsed time (summed as machine time,
not wall-clock, leaving out components that recorded none and saying how many did) that make a
later budget a measured decision rather than a guess. A run responsible for only part of the declaration emits no
summary row, for the reason it emits no `coverage(repo)`.

It reads each component's counts and `elapsed_seconds` out of `mutants.json`, folded across
shards by `foldMutants`, because the counts are typed data there and arithmetic over data does not care how a row happens
to be worded. A shard that wrote no such document — an older lydite in the matrix, or one whose
document would not parse — falls back to reading the score back out of the row that shard
rendered, the same trade `foldedScheduleRow` already makes for `max N concurrent`.
`TestTheFoldReadsBackTheScoreARunRendered` is what holds that fallback's renderer and reader
together, since a wording change would otherwise be a fold that silently stops counting a shard
with no document to fall back on.

**A component the deadline stopped is written apart from the complete ones.** `mutants.json` holds
it under `incomplete_components`, never under `components`, with its counts over the verdicts it
reached and `incomplete: {measured, wanted}` beside them. The separate key is the compatibility
decision: every reader of the document ignores keys it does not know, so a marker alone on an entry
under `components` would read, to a lydite older than the marker, as a complete score — folded into
a total, or landed in the quality history for good. Under its own key the same reader finds the
component absent, the answer it already gives a component nothing measured to completion.
`ReadCounts` refuses a marked entry under `components`, an unmarked one under
`incomplete_components`, and a component under both. A document with no such key is one where every
component finished, which is every document an older lydite wrote.

**Incomplete wins the fold.** `FoldCounts` folds a component any shard left incomplete into
`incomplete_components` wherever a complete entry for it stands, keeping the first incomplete entry
whole rather than summing a second — two entries for one component are the same mutants measured
twice. The component's row is the one its shard rendered: `unmeasured` "N of M measured, rerun to
resume", or `fail` on a survivor found before the deadline (context under that shard's
`--no-gate`), with the progress beneath; a row reading `pass` beside counts saying the component did
not finish is replaced by the unmeasured one. The `mutation` summary leaves every such component out
of its total and says how many it left out. `mutation merge` marks its report incomplete —
exit 3, or 1 when a survivor fails a row — whenever the folded counts hold an incomplete component,
a shard's own report reads `incomplete` (which is how a component the deadline reached before its
baseline is known, since it has no counts), or a component's row carries the progress line.
That last is also what keeps the prose fallback honest: an incomplete row that found a survivor
states its score in the words a complete one does, so `rowIncomplete` recognises the progress line
and the fallback never reads that row back as a finished score. `recordedMutants` reads
`components` alone, so `lydite test record` never lands an incomplete component's partial count.

