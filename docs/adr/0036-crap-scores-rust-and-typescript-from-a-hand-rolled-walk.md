# CRAP scores Rust and TypeScript from a hand-rolled complexity walk, superseding ADR 0028's "Go alone"

[ADR 0028](0028-crap-gates-the-delta-above-the-threshold.md) scored Go alone because Rust and
TypeScript had no complexity source in hand and finding one was research
([#17](https://github.com/lydite/lydite/issues/17)). That research is done: neither language has a
maintained tool worth pinning, and `internal/treesitter` (added for
[ADR 0034](0034-an-exclusion-declaration-is-scoped-by-a-parser-in-every-language.md)) already
parses both without cgo. So `internal/crap` walks the same tree, per language, the way it already
walks `go/ast`.

## Considered and rejected: a pinned complexity tool per language

`rust-code-analysis` is a candidate for Rust: as of this ADR its repository has commits as recent
as April 2026, but no tagged release since `v0.0.25` in January 2023, and 70 open issues — a
project active enough to trust as a one-off oracle, not stable enough to pin without inheriting
its own staleness risk into lydite's release cadence. TypeScript has nothing comparable: Biome's
complexity lint group emits no number, and `typhonjs-escomplex` has had no commit since December
2022 and is dead.

Either pin would also cost the cargo-tool or npm-tool provisioning ritual —
[`tool-pins.md`](../../agentic/references/tool-pins.md)'s manifest-plus-Dependabot-entry
requirement — for a single number neither community has kept current. A hand-rolled walk over a
parser already in the tree costs none of that, and reads the same way Go's `complexity()` already
does: no tool, no pin, no install, no staleness risk.

**Oracles, not dependencies.** `rust-code-analysis-cli`'s cyclomatic metric and ESLint's
`complexity` rule are run once, by hand, over the fixtures this ADR's counting rules were checked
against, and their numbers are recorded in `source/cli/internal/crap/testdata/README.md` beside
the fixtures — not added to any manifest, not invoked by any command lydite ships. They exist to
back the counting rule with a measurement, not to be trusted as a runtime dependency.

## Counting rules

The walk starts at 1, the same as Go, and adds one per construct below. Both languages keep Go's
existing choices unchanged: a closure or callback never gets its own scoring unit and its branches
count toward whichever function's span physically contains it — see Nesting, below, for what that
means when TypeScript's function-like nodes include arrows used as real declarations.

### Rust

One, plus:

- Every `if_expression` and `while_expression` — this covers `if let` and `while let` too, since
  Rust's grammar puts the pattern-matching form and the plain-boolean form in the same node with a
  different condition child. No separate rule is needed for either.
- Every `for_expression`.
- Every `loop_expression`, **whether or not it has a condition to branch on**. This deliberately
  does not follow complexity theory, which would not count an unconditional loop as a branch — it
  follows Go's own precedent instead: `crap.go`'s `complexity()` counts every `ast.ForStmt`
  including a bare `for {}`, because `crap.md`'s stated goal is a number that agrees with gocyclo
  and cyclop, not a number that is theoretically pure. `loop_expression` is Rust's unconditional
  `for {}`, so the same argument applies to it.
- Every `match` arm whose pattern is not the literal `_` wildcard. A bare-identifier catch-all
  binding (`other => ...`) still counts — detecting "this arm matches everything" would need
  exhaustiveness analysis lydite does not do, so the rule is mechanical: only the wildcard token
  itself is treated as `default`. `rust-code-analysis-cli` diverges here and counts every arm, the
  wildcard included, so its number for a `match` with a `_` arm is one higher; lydite keeps its
  own, on the same ground Go's `complexity()` skips a `default` clause — control reaches the
  wildcard only once every other arm has been decided against. The measurement is in
  `testdata/README.md`.
- Every `&&` and `||` `binary_expression`, matching Go exactly.
- Every `?` (`try_expression`). It is scored as a branch because it is semantically the
  `if err != nil { return err }` pattern Go authors write explicitly, which *does* count there as
  an `IfStmt` — eliding the syntax should not erase the branch. This is the one rule this ADR
  expects a real chance of diverging from `rust-code-analysis-cli`'s own number; if it does, the
  divergence and which number lydite kept is recorded in `testdata/README.md` rather than silently
  matched to the oracle.
- `else if` chains need no rule of their own: each `else if` is a nested `if_expression` inside the
  outer's `else` branch, and a plain recursive walk visits and counts it exactly as
  `ast.Inspect` counts each `IfStmt` in a Go `else if` chain today.

A `function_item` nested inside another `function_item` — a real nested `fn`, not a closure, which
Rust permits and Go structurally cannot — is scored as its **own independent function**, not
folded into its parent, on the view that a function containing a named nested function is more
complex than one that does not, and that additional complexity is worth surfacing as its own row
rather than absorbing silently. Concretely:

- The nested `fn`'s own line range is scored as its own `Function`: own span, own complexity walk,
  own coverage.
- The outer function's span excludes the nested function's lines, so no line is scored as
  coverage evidence for two functions at once — the same span/complexity coherence argument
  `crap.md` gives for why a closure's lines belong to the function whose span contains them,
  applied here to say the reverse: lines that get their own scored span leave the parent's.
- The outer function's own complexity gets **+1 for each direct nested `function_item`** it
  contains, as a flat decision-point-like increment — not the nested function's own complexity
  value, which would compound outward through arbitrarily deep nesting for no reason connected to
  what the outer function itself does.

### TypeScript (and TSX, which shares the TypeScript grammar table)

One, plus every node ESLint's own `complexity` rule increments for, in its default ("classic")
mode — chosen because `crap.md`'s stated goal for Go, "a number lydite reports and a number a
developer gets from [gocyclo or cyclop] agree," has a direct TypeScript analogue: ESLint's
`complexity` rule is the tool a TypeScript developer already has, so lydite's number should agree
with it rather than with a narrower rule invented to look more like Go's. Concretely: `if`,
`while`/`do-while`/`for`/`for-in`/`for-of`, `catch`, the ternary (`ConditionalExpression`), every
non-default `case`, `&&`/`||`/`??` (`LogicalExpression` — nullish coalescing counts the same as
the other two), the logical-assignment operators `&&=`/`||=`/`??=`, optional chaining on both
member access and calls (`?.`), and default parameters (`AssignmentPattern`) — the last two count
because both desugar to a real branch (`a?.b` is `a == null ? undefined : a.b`; `function f(x = 1)`
is `x === undefined ? 1 : x`), not because ESLint happens to count them.

**Nesting.** An `arrow_function` nested inside another function-like node — a callback passed to
`arr.map(...)`, not a `const f = () => {}` written at module scope — folds into whichever
function's scoring unit contains it, exactly as a Go closure folds into its enclosing
`FuncDecl`. This is the opposite of the choice made for Rust's nested `fn` above, and the
difference is deliberate: a TypeScript arrow used as a callback has no name and no independent
identity a reader would call "a function in its own right" the way a Rust nested `fn` does — it
exists because `arrow_function` also has to be a valid exclusion-scope target for
`[lydite:exclude_from_<gate>]` (an arrow assigned to a top-level `const` is the ordinary way to
write a real function in TypeScript), not because every arrow is meant to be its own scoring unit.
Folding keeps the outer function's line span — which already contains the nested arrow's lines —
coherent with what its complexity counts, the same argument `crap.md` gives for Go.

One consequence follows directly: an `[lydite:exclude_from_crap]` written immediately above a
*nested* arrow resolves to a real span via `treesitter.DeclaredExclusions` (that resolution is
unchanged by this ADR), but since CRAP never scores that span as its own unit, the declaration is
reported in `Unused` — correctly telling the author to move it to the enclosing function, rather
than doing something quietly inconsistent with what it names.

**A nested named `function_declaration` (or `generator_function_declaration`) is not this case.**
JavaScript and TypeScript both permit a genuine nested function declaration —
`function outer() { function inner() {} ... }` — and unlike a callback arrow it has a name and an
independent identity, the same property that puts Rust's nested `fn` on the other side of this
line. It gets the identical treatment: scored as its own independent function, the parent's span
excludes its lines, and the parent's complexity gets the same flat **+1** per direct nested
declaration. A nested *anonymous* `function_expression` or `generator_function` — the
`function(x) { ... }` form written inline as a callback, with no name — folds like an arrow, on
the same "no independent identity" ground; only a name changes which side of this rule a nested
`function`-shaped node falls on.

### Test code

Go's rule is "whatever the profile covers" — but that is not actually a language-neutral
statement, it rides on a fact specific to Go's own tooling: `go test -coverprofile` never emits an
entry for a `_test.go` file's own statements, so nothing about `crap.go` has ever had to filter
test code — there has never been any in the map to filter. Rust's lcov and TypeScript's coverage
reports do include test-function and test-file hits, so applying Go's literal rule ("don't
filter") to them would score something Go's own rule has never let through. The actually
consistent rule is "don't score test code," achieved for Go by the toolchain and for Rust and
TypeScript by an explicit filter:

- **Rust**: a whole file under `tests/` or `benches/`, and any function found inside a
  `#[cfg(test)] mod ...` node. A bare `#[test] fn` outside a `cfg(test)` module — unconventional,
  but not invalid Rust — is not specially handled and is scored; an unrecognised form failing open
  (scored, and thus visible) rather than silently unscored is the same direction
  `internal/mutation`'s own `isTestModule` doc already commits to.
- **TypeScript**: a whole file with a `.test.` or `.spec.` infix, or under a `__tests__/`
  directory.

Both conventions already exist, verified, as `rustTestFile`, `typeScriptTestFile` and
`isTestModule` in `internal/mutation/treesitter.go` — written for a different reason (mutation
testing test code wastes a run rather than finding anything), but the same classification. Rather
than duplicate it, this ADR moves the three helpers (and the grammar-table fields backing them:
`testFile`, `testModule`, `testAttribute`) into `internal/treesitter`, which `internal/mutation`
already imports — `internal/mutation/treesitter.go:` already aliases `treesitter.ErrUnparsed`, so
this is not a new dependency edge, just relocating an existing one's other half. `internal/mutation`
then imports the moved definitions instead of keeping its own copy. This is a deliberate, called-out
exception to this handoff's stated boundary that `internal/mutation` is not this work's file to
touch — accepted because the alternative is two copies of one classification rule that will drift
the first time either one is edited for its own gate's reason.

## Consequences

- `internal/crap` gains two new per-language walks beside `complexity()`, and a shared `Function`
  type and `Index()` formula that were already language-neutral.
- `internal/treesitter` gains an exported function-span enumerator (task 2 of this work) that
  applies the nesting and test-code rules above, and gains the three test-classification helpers
  moved out of `internal/mutation`.
- `internal/mutation` changes only in that it imports those three helpers rather than defining
  them, with no behavioural change to mutation testing itself.
- A Rust or TypeScript component gets a real `crap` row — `new`, `pass` or `fail` with findings —
  instead of the `context` row ADR 0028 introduced. ADR 0028's "Go alone" section is superseded in
  that one detail and is not rewritten; see the note added there.
- This repository's own `cloud-services` component (TypeScript) is scored by its own CI for the
  first time as of the pull request landing this ADR. Its first baseline reads `new`; the number is
  reported in that pull request's body rather than excluded away.

## Amendment (2026-09-23): Python's counting rule, with radon as its oracle

This extends the decision above to a third language. It is an amendment rather than an ADR of
its own because nothing above is restated: the walk starts at 1, a nested named function is its
own unit and an anonymous one folds, test code is not scored, and the counting rule is validated
against that ecosystem's own tool as an oracle rather than pinned as a dependency. Only *the
counting rule* is language-specific, and this names Python's.

### Python

One, plus:

- Every `if_statement` and every `elif_clause`. An `if`'s `else_clause` counts nothing: it is the
  unconditional remainder, the same thing Go's `complexity()` says about a `default` clause.
- Every `for_statement` and `while_statement` — `async for` is the same node with an extra
  `async` token, and needs no rule of its own.
- Every `else_clause` **on a loop or on a `try`**, which are not that unconditional remainder:
  `for ... else` runs its else exactly when the loop finished without `break`, and `try ... else`
  exactly when nothing was raised. The grammar gives all three elses one node type and says which
  is which by what holds them, so the rule is read off the parent.
- Every `except_clause`, each one separately, the way TypeScript's `catch` counts. A
  `finally_clause` counts nothing — it runs on every path.
- Every `boolean_operator` whose operator is `and` or `or`, matching Go's `&&`/`||` exactly.
- Every `conditional_expression`, Python's ternary.
- Every `for_in_clause` and every `if_clause` **written inside a comprehension**. A comprehension
  is a loop and an optional filter with the syntax compressed, and compressing it does not remove
  either branch. `if_clause` is also how a `match` case writes its guard, which the case already
  counts, so the rule is read off the parent there too.
- Every `assert_statement`. It is `if not x: raise AssertionError` with the branch spelled
  shorter, which is the same ground this ADR counts Rust's `?` on. radon counts it by default and
  offers `--no-assert` to stop; the default is the number a developer sees.
- Every `case_clause` whose pattern is not the bare, unguarded `_`, exactly as Rust's `match` arm
  rule reads. A bare-identifier catch-all (`case other:`) still counts, and a guarded wildcard
  (`case _ if ready:`) counts because it can be reached and declined. radon diverges here in the
  opposite direction from `rust-code-analysis`: it reads every irrefutable pattern as the
  wildcard, so its number for a `match` with a name-binding catch-all is one lower. lydite keeps
  its own, and the measurement is in `testdata/README.md`.
- A `with_statement` counts nothing: it is a call and a cleanup, and no control flow chooses
  between two paths there.

**Nesting.** A `def` nested inside another `def` — which Python permits — is scored as its own
independent function, with the parent's span excluding its lines and the parent's complexity
gaining the flat +1, exactly as Rust's nested `fn` is. A `lambda` is the language's one anonymous
function form, and folds into whichever unit's span contains it when it is written as a callback,
exactly as a TypeScript arrow does. Python has no anonymous `def` and no named `lambda`, so the
name test this ADR already states sorts the two without a rule of its own.

**A decorator is not part of the function it decorates.** Python writes a decorated `def` as a
`decorated_definition` holding every decorator and then the function, where Rust's `#[inline]`
and TypeScript's `@log` precede the declaration as siblings of it. The scored span is the
function's own in every language — a decorator line belongs to no function — so the wrapper is
stepped inside rather than scored: naming it a function-introducing node instead would score
every decorated *class* as a function too, since one node type covers both.

**The oracle is radon**, version 6.0.1, run once by hand over `pyprobe/`, with its numbers
recorded in `testdata/README.md` beside the fixtures. mccabe, which flake8 carries, was run over
the same probes and is not the oracle: it counts no `and`, no `or`, no conditional expression, no
comprehension and no `match` at all, so agreeing with it would make Python the one language here
whose short-circuiting operators are free.

### Test code

A whole file whose module name carries pytest's `test_` prefix or `_test` suffix, or which sits
under a `tests/` directory. Those two filenames are what pytest collects without configuration,
so a file lydite calls the suite is a file pytest would have run; the directory is the same rule
Rust's `tests/` already gets.

There is no in-file equivalent of Rust's `#[cfg(test)] mod`: Python has no form that puts the
suite inside the module under test, so the path rule is the whole of the classification.

### Consequence

- `internal/crap` gains a third per-language walk, and `complexityOf` becomes an exhaustive
  switch that panics on a grammar with no counting rule. A fallback branch there would count one
  language by another's rules — no error, no parse failure, a plausible number under a heading
  naming the wrong language — which is the one failure in that package nothing downstream can
  detect.
- `treesitter.DeclaredTests` gains the same refusal, as `ErrNoTestEnumeration`. A grammar reaches
  this package for the gates that need spans before anything enumerates its tests, and an empty
  slice there reads as a file declaring none: a new-test gate green over every file in the
  language, on the day somebody lists it and never again. Python is that grammar today; reading
  a Python test declaration is not part of this amendment.
