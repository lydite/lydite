# The complexity probes, and what the oracles said about them

`rustprobe/`, `tsprobe/` and `pyprobe/` carry one function per branching construct
[ADR 0036](../../../../docs/adr/0036-crap-scores-rust-and-typescript-from-a-hand-rolled-walk.md)
names a counting rule for. Each table below gives the number that ADR's rules predict, worked
out by hand from the source and not from the code that computes it — so a test asserting the
walk's output is checking it against something derived independently — and beside it the number
an oracle measured over the same function.

The oracles are `rust-code-analysis-cli`'s cyclomatic metric, ESLint's `complexity` rule and
`radon cc`, each run once, by hand, in a scratch directory outside this repository. None is a
dependency: no manifest names them, no `.github/dependabot.yml` entry watches them, and nothing
lydite ships invokes them. They exist to back the counting rules with a measurement — see the
ADR's "Oracles, not dependencies".

Source files carry a `.txt` suffix and are materialised by `internal/fixture`, whose doc comment
says why. They are not a buildable crate or package: no `Cargo.toml` and no `package.json`
accompany them, because a complexity walk parses a file and never builds one, and both oracles
read a bare file too.

## Rust — `rustprobe/`

`src/lib.rs` is the scored file. Line numbers are its own.

| Line | Function | ADR 0036 predicts | Why | `rust-code-analysis` | |
|---|---|---|---|---|---|
| 7 | `classify` | 4 | 1 + three `if_expression` in an `else if` chain | 4 | agrees |
| 20 | `guarded` | 4 | 1 + `if` + `&&` + `\|\|` | 4 | agrees |
| 30 | `unwrap_or_zero` | 2 | 1 + `if let`, one `if_expression` | 2 | agrees |
| 39 | `drain` | 2 | 1 + `while let`, one `while_expression` | 2 | agrees |
| 48 | `countdown` | 2 | 1 + `while` | 2 | agrees |
| 58 | `first_even` | 3 | 1 + `for` + `if` | 3 | agrees |
| 69 | `poll_until` | 3 | 1 + unconditional `loop` + `if` | 3 | agrees |
| 80 | `describe` | 4 | 1 + three arms; the `_` wildcard does not count | **5** | **diverges** |
| 91 | `label` | 3 | 1 + two arms; `other` is a binding, not the wildcard | 3 | agrees |
| 100 | `parse_pair` | 3 | 1 + two `?` | 3 | agrees |
| 108 | `tally` | 2 | 1 + the closure's `&&`, which folds into this span | **1**, closure scored separately at 2 | **diverges** |
| 115 | `outer` | 3 | 1 + `if` + a flat 1 for the nested `fn`; span is 115–128 less 116–122 | **2**, nested `fn` scored separately at 2 | **diverges** |
| 116 | `inner` | 2 | 1 + `if`, its own unit with its own span | 2 | agrees |

`tests/integration.rs` and `benches/throughput.rs` are whole files lydite skips by path, and
`src/lib.rs`'s `#[cfg(test)] mod tests` is a module it skips by node. The oracle scores all
three — `band` 3 and `bands_are_named` 2, `churn` 3 and `main` 1, `classify_names_each_band` 3 and
`outer_folds_odd_and_even` 1 — because
it has no notion of test code. That is the ADR's "Test code" rule doing its job, not a counting
disagreement; the numbers are recorded so a walk that mistakenly descends into them is
recognisable by what it produces.

The two fixture paths demonstrating the whole-file skip are `rustprobe/tests/integration.rs` and
`rustprobe/benches/throughput.rs`, matching the `tests/` and `benches/` prefixes `rustTestFile`
recognises. The skip is a property of the path, not of the content, so no fixture body can prove
it on its own.

### How it was captured

`rust-code-analysis-cli 0.0.25`, the January 2023 release, installed into a scratch directory:

```sh
cargo install --locked rust-code-analysis-cli --root <scratch>
rust-code-analysis-cli --metrics --output-format json --paths <scratch>/rustprobe/src/lib.rs
```

`--locked` is required. Without it the resolver picks a newer `tree-sitter` than the 2023
release compiles against, and the build fails on a type mismatch between `tree-sitter` 0.20 and
0.27 inside `mk_langs!`. That is the staleness the ADR declines to inherit, met on the first
command.

The JSON nests a `spaces` array per enclosing scope, and `metrics.cyclomatic.sum` at a function
**includes every nested space's value**. The per-function numbers above are therefore read from
`min`/`max` on a function with one nested space, not from `sum`: `tally` reports
`sum 3, min 1, max 2` — 1 for itself and 2 for the closure — and `outer` reports
`sum 4, min 2, max 2`.

### The divergences, and which number lydite keeps

**`describe`: lydite 4, oracle 5 — lydite keeps 4.** The oracle counts one per `match` arm
including the `_` wildcard; a micro-probe pins this exactly, with a two-arm match scoring 3
whether its catch-all is `_` or a binding, and a three-arm match scoring 4. ADR 0036 counts only
arms whose pattern is not the literal `_`, on the same ground Go's `complexity()` skips a
`default` clause: control reaches the wildcard when every other arm has already been decided
against, so it is not a decision of its own. Keeping the oracle's number here would make Rust's
`match` count differently from Go's `switch` in the one respect the two share. This divergence is
not one ADR 0036 anticipated; a note recording it is added to that ADR's match-arm rule.

**`tally`: lydite 2, oracle 1 plus a separate 2 — lydite keeps 2.** The oracle gives a closure
its own scoring space. ADR 0036 folds a closure into the function whose span contains it, which
is Go's existing rule and the one that keeps a span's coverage coherent with the complexity
scored against it. Anticipated by the ADR.

**`outer`: lydite 3 (and `inner` 2), oracle 2 (and `inner` 2) — lydite keeps 3.** Both score the
nested `fn` as its own unit and agree on its value; they differ by the flat +1 ADR 0036 leaves in
the parent for containing one. Anticipated by the ADR, which states the increment explicitly.

**`?` does not diverge.** The ADR flagged the `?` operator as the rule with a real chance of
disagreeing with the oracle, and it does not: a micro-probe shows the oracle adding exactly one
per `?`, so `parse_pair`'s two give 3 from both. Nor does the unconditional `loop`, which the
oracle counts too — a bare `loop { return 1; }` scores 2 against a branchless function's 1.

## TypeScript — `tsprobe/`

`src/probe.ts` is the scored file, `src/badge.tsx` the TSX one. Line numbers are each file's own.

| File | Line | Function | ADR 0036 predicts | Why | ESLint | |
|---|---|---|---|---|---|---|
| `probe.ts` | 8 | `pick` | 2 | 1 + ternary | 2 | agrees |
| `probe.ts` | 13 | `title` | 3 | 1 + `?.` member + `??` | 3 | agrees |
| `probe.ts` | 18 | `notify` | 2 | 1 + `?.()` call | 2 | agrees |
| `probe.ts` | 23 | `settle` | 4 | 1 + `??=` + `\|\|=` + `&&=` | 4 | agrees |
| `probe.ts` | 30 | `readNumber` | 2 | 1 + `catch` | 2 | agrees |
| `probe.ts` | 39 | `describe` | 3 | 1 + two non-default `case`; `default` does not count | 3 | agrees |
| `probe.ts` | 51 | `greet` | 3 | 1 + two default parameters | 3 | agrees |
| `probe.ts` | 56 | `total` | 4 | 1 + `for-of` + `while` + `do-while` | 4 | agrees |
| `probe.ts` | 72 | `evens` | 2 | 1 + the callback arrow's `&&`, which folds into this span | **1**, arrow scored separately at 2 | **diverges** |
| `probe.ts` | 77 | `bucket` | 3 | 1 + `if` + ternary; a top-level arrow is its own unit | 3 | agrees |
| `probe.ts` | 88 | `Gate.allow` | 4 | 1 + `if` + `\|\|` + `&&` | 4 | agrees |
| `badge.tsx` | 7 | `Badge` | 4 | 1 + default parameter + ternary + `??` | 4 | agrees |

`src/probe.test.ts`, `src/gate.spec.ts` and `src/__tests__/helpers.ts` are whole files lydite
skips by path — the `.test.` and `.spec.` infixes and the `__tests__` directory `typeScriptTestFile`
recognises. ESLint scores them (`band` 3 and its callback arrow 2, `gate.spec.ts`'s arrow 2,
`sample` 3) because, like the Rust oracle, it has no notion of test code.

### How it was captured

ESLint `10.10.0` with `@typescript-eslint/parser` `8.70.0`, installed into a scratch directory
outside this repository, with a one-off flat config enabling nothing but the `complexity` rule in
its default (classic) variant:

```js
import tsParser from "@typescript-eslint/parser";

export default [
  {
    files: ["**/*.ts", "**/*.tsx"],
    languageOptions: {
      parser: tsParser,
      parserOptions: { ecmaFeatures: { jsx: true } },
    },
    rules: { complexity: ["error", 0] },
  },
];
```

```sh
eslint --no-config-lookup --config eslint.config.mjs <scratch>/tsprobe/src
```

The threshold is `0` rather than `1` so that a complexity-1 function is reported too — at `1`,
`evens` produces no message at all, which reads identically to a function ESLint never visited.

### The divergence, and which number lydite keeps

**`evens`: lydite 2, ESLint 1 plus a separate 2 — lydite keeps 2.** ESLint gives every arrow its
own scoring unit; ADR 0036 folds an arrow used as a callback into the function whose span
contains it, and scores only an arrow written as a real declaration — `bucket`, where the two
agree. Anticipated by the ADR, which calls the nesting rule the deliberate departure from
ESLint that it is. Every other construct the ADR names agrees with ESLint exactly, including the
two the ADR justifies on desugaring rather than on ESLint's behaviour: default parameters
(`greet`) and optional chaining (`title`, `notify`).

ESLint also reports a complexity-1 "class field initializer" for `Gate`'s `private open = false`.
It is not a function, lydite scores no unit for it, and it is recorded here only so that its
absence from lydite's output is not read as a missing row.

## Python — `pyprobe/`

`src/probe.py` is the scored file. Line numbers are its own.

The counting rule is radon's, for the reason Go's is gocyclo's and TypeScript's is ESLint's: the
number lydite reports and the number a developer gets from the tool already in their editor
should agree. One, plus every `if` and `elif`, every conditional expression, every `for` and
`while`, the `else` on a loop or a `try`, every `except` handler, every `and` and `or`, every
clause of a comprehension, every `assert`, and every `match` case that is not the literal `_`. An
`if`'s own `else`, a `with` and a `finally` count nothing. The three nesting rules are the ones
the other two languages already carry: a `lambda` folds into the function whose span contains it
the way a Rust closure does, a nested `def` is scored as its own unit and leaves a flat +1 behind
in its parent, and a decorated function's span is the `def`'s own — the decorator line belongs to
no unit, exactly as a Rust `#[inline]` line belongs to none.

| Line | Function | The rule predicts | Why | `radon` | |
|---|---|---|---|---|---|
| 10 | `classify` | 4 | 1 + `if` + two `elif`; the `else` does not count | 4 | agrees |
| 22 | `guarded` | 4 | 1 + `if` + `and` + `or` | 4 | agrees |
| 29 | `pick` | 2 | 1 + a conditional expression | 2 | agrees |
| 34 | `countdown` | 2 | 1 + `while` | 2 | agrees |
| 43 | `first_even` | 3 | 1 + `for` + `if` | 3 | agrees |
| 51 | `search` | 4 | 1 + `for` + its `else` + `if` | 4 | agrees |
| 60 | `read_number` | 2 | 1 + one `except` handler | 2 | agrees |
| 68 | `read_pair` | 4 | 1 + two `except` handlers + the `try`'s `else`; the `finally` is unconditional | 4 | agrees |
| 82 | `regroup` | 3 | 1 + two `except*` handlers | **1** | **diverges** |
| 93 | `evens` | 3 | 1 + the comprehension's `for` + its `if` | 3 | agrees |
| 98 | `tally` | 2 | 1 + the lambda's `and`, which folds into this span | 2 | agrees |
| 104 | `require_positive` | 2 | 1 + `assert` | 2 | agrees |
| 110 | `read_file` | 1 | a `with` is not a decision | 1 | agrees |
| 116 | `describe` | 3 | 1 + two cases; the `_` wildcard does not count | 3 | agrees |
| 127 | `label` | 3 | 1 + two cases; `other` is a binding, not the wildcard | **2** | **diverges** |
| 136 | `bracket` | 3 | 1 + two cases; a case's guard belongs to the case | 3 | agrees |
| 147 | `gather` | 3 | 1 + `async for` + `if`, the same nodes as their synchronous forms | 3 | agrees |
| 158 | `cached_band` | 2 | 1 + `if`; a decorator decides nothing | 2 | agrees |
| 165 | `outer` | 3 | 1 + `if` + a flat 1 for the nested `def`; span is 165–175 less 168–171 | **2**, nested `def` scored separately at 2 | **diverges** |
| 168 | `inner` | 2 | 1 + `if`, its own unit with its own span | 2 | agrees |
| 181 | `Gate.__init__` | 1 | no branch | 1 | agrees |
| 184 | `Gate.allow` | 4 | 1 + `if` + `or` + `and` | 4 | agrees |
| 195 | `Marker.kind` | 2 | 1 + a conditional expression | 2 | agrees |

`src/test_probe.py`, `src/gate_test.py` and `src/tests/helpers.py` are whole files lydite skips by
path — pytest's `test_` prefix and `_test` suffix, and a `tests` directory. radon scores them
(`band` 3 and `test_bands_are_named` 4, `test_a_gate_opens_for_an_admin` 2, `sample` 4) because,
like the other two oracles, it has no notion of test code.

radon also reports an aggregate row per class — `Gate` 4 and `Marker` 3, each the sum of its
methods' own values. lydite scores no unit for a class, and the rows are recorded here only so
that their absence from lydite's output is not read as missing ones. `Marker` is decorated,
which is what makes it the probe for the wrapper rule: `decorated_definition` wraps a
`class_definition` as readily as a `function_definition`, so lydite reads the wrapper's
*contents* rather than treating the wrapper itself as a function.

### How it was captured

`radon 6.0.1` under CPython 3.14.6, installed into a scratch virtualenv outside this repository:

```sh
python3 -m venv <scratch>/venv
<scratch>/venv/bin/pip install radon
<scratch>/venv/bin/radon cc -s --show-closures <scratch>/pyprobe/src/probe.py
```

`--show-closures` is required for `inner`: without it radon reports `outer` alone and its nested
`def` is absent from the output entirely, which reads identically to a function radon scored 0
for. With it, the nested one is reported as `outer.inner` — lydite names it `inner`, the same
bare name its own `def` gives it, which is what Rust's nested `fn` is named too.

### The divergences, and which number lydite keeps

**`regroup`: lydite 3, radon 1 — lydite keeps 3.** `except*` is an exception *group* handler, and
radon's `ComplexityVisitor` branches on the AST node names `Try` and `TryExcept`, neither of
which is the `TryStar` CPython parses `try/except*` into. Its handlers are therefore counted by
nothing and the function scores as though it were branchless. tree-sitter spells an `except*`
clause the same `except_clause` it spells an ordinary one, so lydite counts both and reports 3.
This is a gap in the oracle rather than a disagreement about what a decision is.

**`label`: lydite 3, radon 2 — lydite keeps 3.** radon treats any capture pattern as the
wildcard: `case other:` and `case _:` are both `MatchAs` with no sub-pattern, so both are
subtracted from the case count. lydite counts only the literal `_`, which is the rule its own
Rust walk already applies to a `match` arm — and the two languages counting one construct two
ways over a difference neither language has is worth more than agreeing with radon here.
`describe`, whose catch-all *is* `_`, agrees with radon exactly.

**`outer`: lydite 3 (and `inner` 2), radon 2 (and `outer.inner` 2) — lydite keeps 3.** Both score
the nested `def` as its own unit and agree on its value; they differ by the flat +1 ADR 0036
leaves in the parent for containing one. The same divergence, for the same reason, as Rust's
`outer` against `rust-code-analysis`.

**The lambda does not diverge.** radon stopped giving a `lambda` its own scoring unit
([radon#68](https://github.com/rubik/radon/issues/68)), so its branches are counted toward the
function containing it — which is exactly what ADR 0036's nesting rule says, reached from the
other direction. `tally` is 2 from both, where Rust's equivalent `tally` and TypeScript's `evens`
each diverge from their own oracle over the identical rule.
