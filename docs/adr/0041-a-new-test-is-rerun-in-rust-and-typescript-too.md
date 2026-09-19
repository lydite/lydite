# A new test is rerun in Rust and TypeScript too

[ADR 0039](0039-a-new-test-is-rerun-once-in-its-own-process.md) settles what the flaky gate asks
and what it refuses to claim. What it cannot settle for a language it does not cover is the part
every language answers differently: how a test is named, whether that name is unique, how a second
run is filtered to the tests a change introduced, and where that run's report is allowed to land.

**`--gate-flaky` covers cargo-nextest and vitest on the same terms it covers `go test`: one rerun
per component, filtered to the new tests, reading both runs' JUnit reports. A Rust or TypeScript
test is identified by its report's `classname` and `name` together, because in neither language is
the name alone unique. jest is a named gap, not an omission.**

## A name is not an identity outside Go

Go's rerun is already collision-free at the name alone, and that is a property of the unit ADR 0039
chose rather than luck. Its identity is the package directory plus the test's name, one `go test`
process is scoped to one package, and within a package the compiler forbids two functions with one
name. A map keyed by name cannot collide inside that scope.

Neither other runner has a scope like it. `testdata/nextest-suite.xml` in `internal/junit` is one
crate with four test binaries, and it records `shared_name` twice — once under
`classname="nextestprobe::a"`, once under `classname="nextestprobe::b"`. The two are different
tests in different binaries, and `cargo nextest` has no objection to either. Vitest's
`testdata/vitest-probe-suite.xml` records `shared title` twice for the same reason, under
`classname="libs/probe/src/one.test.ts"` and `classname="libs/probe/src/two.test.ts"`: a file is
vitest's unit and a title is unique in nothing wider.

So the rerun path keys on `classname` and `name` together, and `junit.ReadOutcomes` keeps the map
it already returns, keyed by name alone and byte-compatible with every caller that reads it. The
composite lookup is an addition beside it, not a change to it. The ledger and the suite row ask
"what became of the tests this report holds", which the existing map answers; only the gate
comparing two runs of a named test needs to tell `nextestprobe::a`'s `shared_name` from
`nextestprobe::b`'s, and silently merging the two under one key is how a flake in one binary is
masked by a pass in another. Go continues to use the name alone, because a composite key there
would make the gate depend on gotestsum's idea of a classname for no collision it prevents.

## One rerun per component, not per binary or per file

Both runners are invoked once per component in run 1, and run 2 mirrors that. A rerun per nextest
binary would pay the crate's link step's worth of process startup for each, and a rerun per vitest
file would pay a fresh worker pool and module graph per file — for a gate whose entire budget is
meant to be the new tests' own duration.

**Rust** filters with one OR-ed expression of exact predicates, one per new test name in the
component:

```sh
cargo nextest run --profile rerun --no-fail-fast \
  -E 'test(=shared_name) or test(=tests::nested::doubles_deeper) or test(=async_cases::awaits_and_agrees)'
```

`testdata/nextest-rerun.xml` is that run. `test(=...)` is an exact match on the name, which is a
name and not a binary: the single `test(=shared_name)` term selected the test in *both* binaries,
and the report holds both. That is the right behaviour for a gate — a name that is new in two
binaries is two new tests — and it is only legible because the outcomes are read back by
`classname` and `name`.

**TypeScript** names every file contributing a new test as a positional argument and carries one
`--testNamePattern` covering every new test's full name, anchored the way Go's `RunPattern` is:

```sh
vitest run libs/probe/src/one.test.ts libs/probe/src/two.test.ts \
  -t '^(matches \^a \(b\) \[c\] \+ d\$|shared title|outer > inner > holds a title two describes deep)$'
```

**Every regex metacharacter in a test's title is escaped.** `-t` is a regular expression, where
`go test -run`'s input is spliced from Go identifiers that cannot contain one. A title is prose
written by whoever wrote the test, and prose contains parentheses.
`testdata/vitest-probe-rerun-unescaped.xml` is the same invocation with the alternation built
verbatim from the titles: `matches ^a (b) [c] + d$` comes back `<skipped/>`, unmatched by a pattern
assembled out of its own text. Under ADR 0039's rule that a skip is an outcome, that is a
deterministic test reported as flaky by the gate's own filter — a false positive manufactured
entirely by lydite, which is the worst kind there is.

The two runners differ in what a filtered-out test looks like, and the gate reads each on its own
terms. Nextest omits the tests its filter excluded: `nextest-rerun.xml` holds four test cases and
nothing else. Vitest keeps every test in a file it was pointed at and marks the excluded ones
`<skipped/>`. So in a vitest rerun, a `<skipped/>` on a *new* test means either the test skipped
itself or the pattern failed to reach it, and the gate cannot tell which — which is the second
reason the escaping is not optional, since it is what makes the first of those two the only
remaining explanation.

## Run 2's nextest report goes to a second profile

`internal/runner`'s `nextestToolConfigBody` is the tool config lydite stages and passes as
`--tool-config-file lydite:<path>`; it turns the JUnit report on for nextest's default profile and
says nothing else. It gains a second, equally static stanza:

```toml
[profile.default.junit]
path = "junit.xml"

[profile.rerun.junit]
path = "junit-rerun.xml"
```

Run 1 keeps the default profile. Run 2 passes `--profile rerun`. The body stays one fixed string,
identical for every component and every install, so the single file that concurrently running
components already share stays a file they all agree about — a per-component body would turn a
shared path into a race, and this introduces no race the existing arrangement does not already
have.

The separation is belt and braces, and both halves hold. Nextest writes a profile's JUnit under
`target/nextest/<profile>/`, so the rerun's report would already land beside rather than over run
1's; the distinct filename means the two are still distinguishable if a repository ever points both
profiles at one directory. Run 1's `target/nextest/default/junit.xml` is what the ledger records
counts from, and a rerun of four tests overwriting it would put "4 tests" in the quality history of
a component that ran six hundred — the same reason ADR 0039 gives run 2 its own report in Go.

Two failure modes, per [`a-gate-that-could-not-run-never-renders-as-one-that-passed`](../../.claude/rules/a-gate-that-could-not-run-never-renders-as-one-that-passed.md):

- **No cache directory to stage the tool config in.** The component's rerun is `unmeasured`,
  reason "no cache directory to stage the rerun's tool config". Without the config there is no
  `rerun` profile to select and no report to read, and running anyway would produce a filtered
  invocation whose outcome nothing can be compared against.
- **The repository's own nextest config sets a junit path for either profile.** A repository's
  `.config/nextest.toml` outranks a tool config, per profile.
  `internal/flaky/testdata/nextest-tool-config-shadowed.txt` captures it: a repository declaring
  `[profile.default.junit] path = "repo-owned.xml"` gets `target/nextest/default/repo-owned.xml`
  and no `junit.xml`, while `rerun`, which it did not declare, still lands on lydite's
  `junit-rerun.xml`. A shadowed profile contributes no counts and the component is `unmeasured`
  with the profile named — exactly the shape ADR 0039 already documents for run 1's default
  profile, applied to the second one.

## Enumerating the new tests statically

ADR 0039 takes "new" from a set difference of *declared* names at two revisions, never from the
diff's lines, and that does not change. What changes is the parser: `go/ast` answers for Go, and
for the other two it is the tree-sitter grammars `internal/treesitter` already carries.

The enumeration lives beside `TestFile` and `TestModule` in `internal/treesitter/testcode.go`,
not in a package of its own. That file is already the table of how each language writes its tests —
the question mutation and crap both ask — and "which tests does this file declare" is the same
question with a different answer shape. Two tables would agree until one was edited for one gate's
reason, which is the argument `testCode`'s own doc comment makes.

**Rust** walks `function_item` nodes carrying a recognised test attribute as a *preceding sibling*,
the same shape `TestModule` reads rather than looking inside the node, and qualifies each with
every enclosing `mod_item`'s name joined with `::`. That join is nextest's own naming, confirmed
against the capture: `src/lib.rs`'s `mod tests { mod nested { fn doubles_deeper } }` is
`tests::nested::doubles_deeper` in `nextest-suite.xml`, and `tests/c.rs`'s
`mod async_cases { async fn awaits_and_agrees }` is `async_cases::awaits_and_agrees`. The crate's
own binary, not the module path, is the `classname`.

**TypeScript** walks `call_expression` nodes whose callee is `describe`, `it` or `test` with a
string-literal first argument, qualified by every enclosing `describe` call's own string-literal
title. **The separator is `" > "` — a literal space, greater-than, space.** That is what vitest
writes, measured rather than assumed: `vitest-workspace-suite.xml`, captured over
`source/cloud-services` unmodified, records this repository's own
`describe("appJwt") > it("backdates iat …")` as
`appJwt &gt; backdates iat so a clock a second fast is not a rejection`, and the probe's two-level
chain as `outer &gt; inner &gt; holds a title two describes deep`. The `classname` is the file's
path relative to the component root, and the file path is not part of `name`.

**A title that is not a string literal is enumerated as present with its name unreadable.** A
template literal, a `test.each`, a title built from a variable: the node says a test is declared at
that line, and nothing a parser reads says what it will be called. `vitest-probe-suite.xml` is why
the two cases are one — `test.each` expanded to `doubles 1 into 2` and `doubles 2 into 4`, and a
template literal to `a title built from a computed expression`, names that exist only after the
file has run. Each is returned with its line and a marker meaning exactly that, never skipped and
never guessed at, and the gate counts it towards the row's unmeasurable count. Guessing at a name
gives the rerun a filter that matches nothing and reports the test as skipped; skipping it silently
gives a green row for a test the gate never looked at, which is the failure the rule above exists
to prevent.

**A file that fails to parse is an error**, the way `declaredTests` already treats a Go file
`go/parser` rejects. A parse failure is not a file that declares no tests, and a gate that read it
as one would go green on the change that broke the parser.

### Which Rust attributes count

Recognised: `#[test]`, `#[tokio::test]`, `#[async_std::test]`. The first is the language's own and
the other two are how the two async runtimes in wide use spell it; `#[tokio::test]` is in the
capture, and nextest names the function it marks exactly as it names a plain one.

Anything else is **not** recognised, and a function carrying it inside a test module is counted as
unmeasurable rather than assumed inert. `#[rstest]` generates one test per case with names the
attribute's arguments decide. `#[wasm_bindgen_test]` runs under a harness that is not the one lydite
invoked. A proc-macro nobody has seen does whatever it does. Each is a test that exists and whose
name lydite cannot produce, which is the same answer the unreadable TypeScript title gets, for the
same reason: the row says a test went unexamined instead of pretending there was nothing there.

This is the direction `TestModule` already fails in — a form the table does not know about produces
something an author can see and answer, where a looser match quietly stops examining real code.

## jest is a gap with a name

[ADR 0029](0029-the-ledger-appends-what-cannot-be-recomputed.md)'s refusal stands: lydite will not
install `jest-junit` into a workspace it is about to gate, because a scanner that changes what the
repository resolves to has stopped measuring that repository. jest ships no JUnit reporter of its
own, so there is no report for either run to be read from.

A component whose runner is jest is therefore `unmeasured`, reason "jest has no JUnit output lydite
will install". This is a decision and not unshipped scope — there is nothing to schedule, because
the only implementation is the one ADR 0029 refuses. It generalises ADR 0039's "a component whose
runner is not Go" from a language to a runner, which is where the constraint actually sits.

## Consequences

- A repository whose components are Rust and TypeScript gets the gate rather than an amber row.
  jest is the one runner for which ADR 0039's entirely amber gate remains the answer.
- The Rust rerun reruns a new test name in every binary that declares one, not just the binary the
  change touched. That is over-reporting in the same direction ADR 0039 chose for a renamed
  package: the cost is one extra passing test, and the failure avoided is a collision resolving to
  the wrong outcome.
- A vitest rerun's report holds every test in every file it named, most of them `<skipped/>`. Only
  the new tests' rows are consulted, and the report is larger than the thing measured.
- A TypeScript suite that names its tests dynamically is enumerated but not examined. A file built
  entirely out of `test.each` contributes an unmeasurable count and nothing else, which is a
  truthful report of what a static parser can say about it.
- `nextestToolConfigBody` now declares a profile no repository asked for. A repository that
  happens to have its own `rerun` profile has it shadowed by its own config, which is the
  documented `unmeasured` case rather than a surprise.
