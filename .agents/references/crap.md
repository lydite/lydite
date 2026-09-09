# Complexity: the CRAP index

> **The reference for `internal/crap` and `internal/annotation`** — the complexity gate, and how a function says which gate it is not evidence for.

Coverage measures execution and nothing else — a test that calls a function and asserts nothing
scores full marks on every line it touches. Mutation answers that half; complexity is the other,
and `internal/crap` is it: `comp² × (1 − cov)³ + comp` per function, scored from the per-line hits
`internal/coverage` already parsed. No second run and no second artefact, the same rule the patch
gate follows. See [ADR 0028](../../docs/adr/0028-crap-gates-the-delta-above-the-threshold.md).

**The gate is the delta of the count above 30, per component.** A change may not raise how many of
a component's functions sit above the threshold. Absolute would fail every repository on the day
it upgrades, over debt it has always had — the rule `coverage.floor` follows by defaulting to `0`.
Diff-scoping was rejected too, and that is the less obvious half: a change can push a function
over the threshold **without touching it**, by complicating a call site or deleting the test that
covered it, which is the blind spot aggregate coverage exists to cover. It is a **Gate** in
CONTEXT.md's sense — the author clears it by testing the function or taking it apart, both of
which are the work you wanted.

**The worst value is recorded and never gated.** It and the count are the two ledger scalars
[#26](https://github.com/lydite/lydite/issues/26) needs. A change taking the worst function from
400 to 380 has improved nothing anybody can act on, and one adding a well-tested complex function
raises it without adding a thing to fix.

**There is no repository-wide gate**, because the per-component rule is strictly stricter: a change
adding a function above the threshold to one component and removing one from another fails there
and nets to zero over the repository. The `crap` row a whole run and `lydite test merge` emit
carries the scalars summed, over a denominator of the components CRAP could apply to. It is
`context` when anything was scored — and `unmeasured` when a repository lydite could have scored
produced no score at all, for the reason `floorSummaryRow` is: a figure over a repository nothing
was examined in must not read as a clean one.

**The figure counts what this run carried forward, and says how many.** A component affected
selection did not run still has a score, so leaving it out would make the number swing with
whatever a change happened to touch — and a figure that does not say how much of itself this run
measured is indistinguishable from one that measured everything, which is the rule `composedValue`
already follows. `carriedScore` is the one implementation of which entry a component keeps, read by
both the row a run renders and the document it hands the fold; two copies would have one tree
report one figure sharded and another unsharded.

**Go alone, and that is a property of the language.** lydite is Go and walks `go/ast` in-process —
no tool, no pin, no install, no staleness risk. Rust and TypeScript have no equivalent in hand and
are [#17](https://github.com/lydite/lydite/issues/17), which is research; a `lang`-shaped
abstraction invented from one implementation is an abstraction fitted to Go. Every other component
gets one **`context`** row naming the limit — present, because a component silently absent reads as
one that scored clean, and context rather than amber because nothing about that repository could
make the row green. A Go component whose score could not be taken is the opposite, and is amber.

**Complexity is counted the way gocyclo and cyclop count it**: one, plus every `if`, `for` and
`range`, every non-default `case`, every communicating `select` clause, and every `&&` and `||`.
A number lydite reports and a number a developer gets from either agree, which is most of what
makes a threshold arguable. A closure counts towards the function that declares it, because the
coverage half of the score is that function's whole line span and contains the closure's lines —
excluding its branches would score one span's coverage against another span's complexity.

**Generated files are excluded by reuse and not by a second rule.** The hit map bounds what is
scored, and `ParseGoProfile` has already dropped generated files and the blank and comment lines a
Go profile's block spans sweep up. A function the report knows no line of is not scored at all,
never scored 0% — the `0/0` that `coverage.LineCount.Measured` keeps out of every other figure.

## A function says which gate it is not evidence for

Every gate that reports per-site findings can be right about the code and wrong about what the code
is for. `internal/annotation` is where an author says which, in one grammar:
`[lydite:exclude_from_<gate>][<reason>]`, over `mutation`, `crap` and `coverage`.

**The gate is inside the token, and the two coverage-shaped gates take separate declarations.** A
function whose coverage is taken in another process has not thereby become unmutable, and a score
that is not evidence says nothing about whether the lines ran. `[lydite:exclude_from_coverage]`
drops a function's lines from **both sides** of every coverage figure — out of the numerator so
nothing claims to have covered them, and out of the denominator so an author's own statement is not
reported back as a hole they have to fill, which is the reading that would make the declaration
worth nothing. `[lydite:exclude_from_crap]` drops a function from the score alone: it keeps its
coverage, and its complexity is simply not held against it.

**A coverage declaration answers the coverage gates and nothing else.** `coverage.Report` carries
two maps: `Hits`, which the aggregate, the patch gate and the score read and from which a declared
line is absent, and `Executed`, which is the same map with those lines put back. Mutation bounds
its mutants by lines coverage reports as executed, so reading `Hits` there would let one
declaration silence a second gate — and a function whose coverage is taken in another process has
not thereby become unmutable. A language with no declaration form has one map under both names.

**A declaration follows Go's own attachment, and that is a stated limit.** A function written
directly beneath an existing declaration — no blank line, no doc comment of its own — takes that
declaration, because `go/parser` attaches the group to the nearer declaration; the new function is
excluded and the old one returns to being counted, and nothing refers the change because the diff
adds no line holding the token. What bounds it is that gofmt separates declarations with a blank
line and the shape is unusual. Closing it properly means naming the function inside the token,
which is a grammar change rather than a rule this one can make.

**The reason is required and delimited.** Required for the cause the exemption set requires one:
the declaration is the entire risk record for a finding nobody can clear, and a bare token is not
reviewable. Delimited because that is what lets it wrap — an undelimited reason is capped by
whatever line length a repository's linter enforces, and joining the next comment line instead
needs a rule for when that line is a continuation and when it is prose. The bracket also survives
godoc: `//lydite:…` with no space is a *directive*, and godoc strips a directive line but not its
continuations, so a wrapped reason lost its first line and leaked the rest into the rendered
documentation.

**A declaration covers the function whose doc comment holds it, and nothing else.** That is where a
claim about a function belongs, it is the one comment group a language's own parser already
attaches to a declaration, and it is the only placement that cannot silently widen — a rule that
also read a trailing comment on the `func` line would let a declaration written for one function
acknowledge the next after an edit moved a blank line. `coverage.DeclaredExclusions` is the one
implementation, in `internal/coverage` because `internal/crap` already imports it and the reverse
would be a cycle; `internal/annotation` stays a leaf that answers what a comment *says* rather than
what a language's syntax attaches it to, so `internal/referral` links the token and neither a
parser nor `coverage.DeclaredExclusions`.

**Two numbers keep it honest.** Every `crap` row carries how many functions were excluded, because
a repository can annotate its way to nothing above the threshold and that count is what makes it
visible when one does — and a coverage declaration is counted there too, since the lines it removes
are already gone from the hit map and the function would otherwise drop out of the score in
silence. A declaration that documents no function is named on stderr, the rule a mutation
declaration already follows; the commonest cause is one written inside a body, where it reads
perfectly and does nothing.

**It composes with referral for free.** `internal/referral` reads the shared `[lydite:exclude_from_`
prefix rather than a list of tokens — a list goes stale the first time a gate is added, silently,
in the one place where a missed suppression means a change merges unread. Adding a declaration is
an added line carrying a suppression, so the change is referred and a human reads the claim.

**lydite's own nine are the shell-out boundary**: `provisionGo`, `provisionNode`, `downloadGo`,
`downloadNode`, `ensureExecutable`, `ensureNPMToolchain`, `lintDirBiome`, `rust.ensure` and
`cargotool.Install`. Every one provisions, installs or invokes a foreign toolchain, which is the
code `internal/runner`'s doc already says is asserted as argv rather than executed — "a unit test
that shells out to a foreign toolchain tests the machine it runs on" — and which `ci-end2end`'s
proving ground exercises in a different process that contributes no coverage to any profile. Two of
them say something narrower and worth reading: `downloadGo` and `downloadNode` are reached only on
a machine with no suitable toolchain, which no runner lydite tests on is.

## Its own baseline document

`crap/v1/<tree>.json` on the `lydite` branch, beside and never inside `v4/<tree>.json`. An object
of component name to `{above, worst, producer}`.

- **A wider `v4` entry was rejected, and the trap is the point.** An entry written before the field
  reads back with it at its zero value, so the delta becomes `current − 0` — the absolute count,
  failing every repository on the day it upgrades. That is what the delta exists to prevent,
  arriving through the mechanism meant to prevent it. Widening forces a `v5`, and a `v5` costs
  every consumer a full **coverage** cache miss for a **CRAP** feature.
- **Separate documents, one writer.** `gitstate.Write` stages every metric's document in
  one commit, so the single call site stays the only place a baseline reaches the branch and a
  tree's state cannot half-land.
- **A CRAP miss does not measure the base tree.** Every repository has a coverage baseline and no
  CRAP one the first time it runs a lydite that computes CRAP, and re-measuring the base tree for
  a metric that would gate nothing on that run charges all of them for it. The components report
  `new` and gate nothing for one change, which is the shape a changed producer already has. A
  *coverage* miss does measure, and that measurement answers both metrics at once.
- **The entry carries its producer**, for the reason a coverage entry does: a toolchain that counts
  statements differently moves every function's coverage, so a difference is a change of definition
  and reports `new`.
- **The threshold is not stored and not configurable.** 30 is the value the definition names, and a
  knob added before anyone has asked is a knob whose default is the only value anyone uses. Moving
  it changes what the stored count means, so it is a bump of the metric's directory rather than a
  field beside the number.
- **A score rides on a coverage entry and never travels alone.** It is derived from the coverage
  report, and `missingFromRecord` asks only whether a component has an entry — so a score-only
  entry would satisfy the completeness check for a component whose coverage nobody measured. The
  carry-forward rule is coverage's, asked again: only a component affected selection did not run.
- **No tolerance, deliberately.** Two measurements of one tree differ only if what is measured
  changed, so there is no sub-tenth noise to absorb — and a tolerance over an integer count would
  be a free function above the threshold per merge.

`--gate-coverage` is what turns the comparison on, so the flag named for coverage now gates two
metrics. What it means is "read the recorded state and compare against it", and the recorded state
is one document per metric.

