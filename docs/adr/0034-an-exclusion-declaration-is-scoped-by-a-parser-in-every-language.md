# An exclusion declaration is scoped by a parser in every language, never by the coverage report

[ADR 0028](0028-crap-gates-the-delta-above-the-threshold.md) introduced
`[lydite:exclude_from_<gate>][<reason>]`, and `crap.md` states its scope rule: a declaration covers
the function whose doc comment holds it and nothing else. That rule is implemented once, in
`coverage.DeclaredExclusions`, over `go/ast` — so it holds in Go and nowhere else. Rust and
TypeScript coverage arrives as lcov, which has line hits and function records but no syntax tree,
and `ParseLCOV` ignores declarations entirely. Outside Go a declaration excludes nothing, is not
reported as unused, and still refers the change, because `internal/referral` matches the shared
prefix in the diff. The author pays for a suppression and gets none of it.

**A declaration's scope is resolved by a parser in all three languages: `go/ast` in Go and
tree-sitter in Rust, TypeScript and TSX. The coverage report is never asked which function a
declaration is about, and never asked where that function ends.** The report supplies the lines; the
parser supplies the span they are intersected with.

## What the real reports contain

The two rejected models both rest on reading a function's extent out of the report, so the reports
were captured rather than reasoned about — [ADR 0032](0032-every-scanner-reports-its-findings-as-data.md)
records what a plausible number from work that never happened costs. They are committed under
`internal/coverage/testdata`, verbatim, with the probe trees that produced them: `cargo-llvm-cov` at
the pinned version over a Rust crate, and vitest over a TypeScript package under **both** coverage
providers lydite supports.

Every function record in all three is `FN:<line>,<name>` — **a start line and no end.** The format
permits `FN:<start>,<end>,<name>` and neither producer lydite runs emits it. So an extent has to be
derived, and each available derivation is wrong on code that is not unusual:

- **The next function record's start line.** A closure and a nested item each get their own record
  *inside* the function that declares them. Rust's `tally` is `FN:36` and the closure in its body is
  `FN:37`; `outer` is `FN:45` and its nested `fn inner` is `FN:46`. TypeScript's `tally` is `FN:21`
  and its two arrow functions are `FN:22` and `FN:25`. A declaration on any of those four excludes
  one line and leaves the rest of the function counted — silently, which is the failure this is
  meant to end rather than relocate. `crap.md` already says the opposite is the rule: a closure's
  lines belong to the function that declares it.
- **The contiguous run of line records after it.** They are not contiguous. TypeScript's
  `provision` runs from line 9 to line 18 and its `DA` records are 10, 11, 12, 14, 16 and 17 —
  `} else {` and `}` carry no record, because lcov lists only executable lines. The run stops at 12
  and three of the function's six measured lines stay in the figure.
- **The function's own name.** Rust's names are v0-mangled symbols carrying a crate-metadata
  disambiguator (`_RNvCsbs719SBsOl0_18lcov_scope_fixture9provision`), so they are neither source
  identifiers nor stable across builds. A generic emits one record per monomorphisation at the same
  line: `widest` is `FN:24` twice, once for `i64` and once for `f64`. And in TypeScript the name
  depends on the provider — over the identical file, v8 names the two class methods `bump` and
  `read` where istanbul names them `(anonymous_5)` and `(anonymous_6)`.

What the reports *do* answer reliably is the anchor: in both languages the record's line is the
`fn`/`function` line, immediately below the declaration. That is the half a parser makes redundant,
and the delimiting half is the half that matters.

## Rejected: scope the declaration to lcov's `FN`/`FNDA` records

The leanest option, and the evidence above is the whole of the case against it. It resolves *which*
function and cannot resolve *how far*, and every fallback for the second question fails on a
closure, a nested item, a generic or a blank `}` — each of which is ordinary code rather than an
edge case anyone would notice writing. The result is a declaration that excludes part of a function.
That is worse than one that excludes nothing: the author sees the token, has already paid the
referral it costs, and the figure is wrong in a direction nothing reports.

`FNDA` is a hit count, and a hit count is not what an exclusion needs. Dropping a function from both
sides of a figure needs its lines.

## Rejected: a line-range scope, diverging by language

It needs no parser, and it was rejected on what the divergence would cost rather than on effort. Two
forms were considered:

- **Implicit**, where the declaration runs to some end the report implies. That is the same
  unanswerable question the previous model failed on, asked without even the anchor.
- **Explicit**, a start/stop pair after `LCOV_EXCL_START`. It is a second grammar for one concern,
  and a stop token an author forgets excludes to the end of the file. `crap.md` keeps the
  declaration on the doc comment because it is "the only placement that cannot silently widen", and
  this is precisely a placement that can.

Both leave `[lydite:exclude_from_coverage]` meaning one thing in Go and another in Rust and
TypeScript, in the two gates where an author's own statement is the entire risk record. A reviewer
reading the token in a diff would have to know the file's language to know what was claimed.

## What it costs, and where the walk lives

`internal/mutation` already parses Rust, TypeScript and TSX, its `grammar` table already names each
language's comment node, and `tsGen.comments` already feeds `annotation.Declarations` — so the
declaration side exists for these languages and only the scope side is new: a set of function-like
node types per grammar, and a walk to the innermost one containing a line.

That walk cannot simply be called from `internal/coverage`, because **`internal/mutation` imports
`internal/coverage`** — one edge, `coverage.IsGeneratedGoSource`. Coverage is the package CRAP and
mutation are both built on, and inverting that to reach a parser would make the mutation engine a
dependency of every coverage figure. So the grammar tables and the walk move to a package both
import, and neither depends on the other. `sites.resolve` is not that walk and cannot become it: it
answers which mutant span is innermost, which is span containment rather than function resolution.

Three consequences follow and are accepted:

- **A build missing a `grammar_subset_<lang>` tag now reaches coverage.** It already panics at
  mutation's first parse, and the release tags and the module's test invocation already carry all
  four. The failure is loud, which is the direction a gate's failures must break.
- **Coverage gains a parser it did not have**, and a file tree-sitter cannot read has to mean
  something. It means no exclusion, the stance `excludedGoLines` already takes for a Go file that
  will not parse: the tree compiled to produce the report being read, so failing a coverage figure
  over a parse would turn it into a syntax check.
- **The declaration is written in a `//` line comment, not the language's doc comment.**
  `annotation.body` strips `//` and nothing else, so Rust's `///` and TypeScript's `/** */` carry
  no declaration today — which is already true of the mutation declarations in those languages.
  This ADR does not settle whether that should change; it decides scope, and scope is the same
  function's span either way.
