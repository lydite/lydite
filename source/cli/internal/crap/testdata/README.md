# The complexity probes, and what the oracles said about them

`rustprobe/`, `tsprobe/` and `pyprobe/` carry one function per branching construct
[ADR 0036](../../../../docs/adr/0036-crap-scores-rust-and-typescript-from-a-hand-rolled-walk.md)
names a counting rule for. Each table below gives the number that ADR's rules predict, worked
out by hand from the source and not from the code that computes it — so a test asserting the
walk's output is checking it against something derived independently — and beside it the number
an oracle measured over the same function.

The oracles are `rust-code-analysis-cli`'s cyclomatic metric, ESLint's `complexity` rule and
radon's `cc`, run once, by hand, in a scratch directory outside this repository. None is a
dependency: no manifest names them, no `.github/dependabot.yml` entry watches them, and nothing
lydite ships invokes them. They exist to back the counting rules with a measurement — see the
ADR's "Oracles, not dependencies".

Source files carry a `.txt` suffix and are materialised by `internal/fixture`, whose doc comment
says why. They are not a buildable crate or package: no `Cargo.toml` and no `package.json`
accompany them, because a complexity walk parses a file and never builds one, and every oracle
reads a bare file too.

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

| Line | Function | ADR 0036 predicts | Why | radon | |
|---|---|---|---|---|---|
| 7 | `classify` | 4 | 1 + `if` + two `elif`; the `else` does not count | 4 | agrees |
| 19 | `guarded` | 4 | 1 + `if` + `or` + `and` | 4 | agrees |
| 26 | `first_even` | 3 | 1 + `for` + `if` | 3 | agrees |
| 34 | `scan` | 4 | 1 + `for` + `if` + the loop's `else`, which runs only when nothing broke | 4 | agrees |
| 44 | `countdown` | 2 | 1 + `while` | 2 | agrees |
| 51 | `read_number` | 2 | 1 + one `except` | 2 | agrees |
| 59 | `settle` | 4 | 1 + two `except` + the `try`'s `else`; the `finally` does not count | 4 | agrees |
| 73 | `open_all` | 2 | 1 + `for`; the `with` does not count | 2 | agrees |
| 82 | `evens` | 3 | 1 + the comprehension's `for` + its `if` filter | 3 | agrees |
| 87 | `pick` | 2 | 1 + a conditional expression | 2 | agrees |
| 92 | `describe` | 3 | 1 + two cases; the bare `_` does not count | 3 | agrees |
| 103 | `label` | 3 | 1 + two cases; `other` is a binding, not the wildcard | **2** | **diverges** |
| 112 | `positive` | 2 | 1 + `assert`, a raise behind a condition | 2 | agrees |
| 118 | `tally` | 2 | 1 + the callback lambda's `and`, which folds into this span | 2 | agrees |
| 123 | `outer` | 3 | 1 + `if` + a flat 1 for the nested `def`; span is 123–133 less 126–129 | **2**, nested `def` scored separately at 2 | **diverges** |
| 126 | `inner` | 2 | 1 + `if`, its own unit with its own span | 2 | agrees |
| 136 | `memoize` | 1 | a decorator is an ordinary function and branches nowhere | 1 | agrees |
| 142 | `cached` | 2 | 1 + `if`; `@memoize` above it counts nothing | 2 | agrees |
| 149 | `scale` | 1 | a lambda bound to a name at the top level is its own unit | — | radon scores no unit |
| 155 | `Gate.__init__` | 1 | no branch | 1 | agrees |
| 158 | `Gate.allow` | 4 | 1 + `if` + `or` + `and` | 4 | agrees |
| 165 | `Gate.build` | 2 | 1 + `if`; a decorated method is still declared on its class | 2 | agrees |

`src/test_probe.py`, `src/probe_test.py` and `tests/helpers.py` are whole files lydite skips by
path — pytest's own two default discovery names and the `tests/` directory `pythonTestFile`
recognises. radon scores them (`band` 3 and `test_classify_names_each_band` 3,
`test_guarded_admits_an_admin` 4, `sample` 3) because, like the other two oracles, it has no
notion of test code. `tests/helpers.py` is named for neither pytest convention on purpose: the
directory alone marks it, so no fixture body can prove that half of the rule.

radon also reports a complexity-3 row for `Gate` itself, the class's own aggregate. It is not a
function, lydite scores no unit for it, and it is recorded here so that its absence from
lydite's output is not read as a missing row. The same goes for `scale`: radon walks module-level
statements without giving a lambda bound there a unit of its own, so it has no number to compare
against lydite's 1.

### How it was captured

`radon 6.0.1`, installed into a scratch virtualenv outside this repository:

```sh
python3 -m venv <scratch>/venv && <scratch>/venv/bin/pip install radon
<scratch>/venv/bin/radon cc -s --show-closures <scratch>/pyprobe/src/probe.py
```

`-s` shows the raw score beside each rank, which is the number the table records. Without
`--show-closures` a nested `def` is not reported at all — `outer.inner` disappears, which reads
identically to a function radon folded into its parent rather than one it declined to print.

Every function is reported whatever its score, so a complexity-1 function produces a row rather
than nothing; radon has no threshold to set the way ESLint's `complexity` rule does.

`assert` counts under radon's default, and `--no-assert` turns it off — `positive` scores 2 and 1
respectively. The default is the number a developer sees, so it is the one recorded.

### Why radon and not mccabe

`mccabe 0.7.0`, which flake8 carries, was run over the same probes (`python -m mccabe --min 1`)
and is not the oracle. It counts no `and`, no `or`, no conditional expression, no comprehension
and no `match` at all: `guarded` scores 2 under it, `evens` and `pick` and `describe` and `label`
all score 1, and `tally` scores 1. Agreeing with it would make Python the one language here whose
short-circuiting operators are free, contradicting the rule Rust, TypeScript and Go all follow.
It also folds a nested `def` into its parent outright — `outer` scores 4 — where every other
oracle and lydite itself score it apart.

### The divergences, and which number lydite keeps

**`label`: lydite 3, radon 2 — lydite keeps 3.** radon reads every irrefutable pattern as the
wildcard, so `case other:` costs nothing there just as `case _:` does. ADR 0036 counts only an
arm whose pattern is the literal `_`, on the ground Go's `complexity()` skips a `default` clause:
control reaches `_` when every other case has already been decided against. A name-binding case
is written as a decision and reads as one, and telling a binding that always matches from a
pattern that sometimes does is exhaustiveness analysis lydite does not do. This is the mirror
image of Rust's `describe`, where the oracle counted the wildcard lydite skips; the rule is the
same one in both languages, and each ecosystem's tool departs from it in a different direction.

**`outer`: lydite 3 (and `inner` 2), radon 2 (and `outer.inner` 2) — lydite keeps 3.** Both score
the nested `def` as its own unit and agree on its value; they differ by the flat +1 ADR 0036
leaves in the parent for containing one. Anticipated by the ADR, which states the increment
explicitly.

**The lambda does not diverge.** A lambda used as a callback was the rule with a real chance of
disagreeing — it is where Rust's closure and TypeScript's arrow both do — and radon folds one
into the enclosing function exactly as lydite does, so `tally` is 2 from both. radon gives a unit
only to a `def`, which is why `scale`, a lambda bound at module level, is a lydite row with no
oracle number beside it rather than a disagreement.

**Nor does `for ... else`.** radon counts a loop's `else` and a `try`'s `else`, and lydite counts
both: `scan` is 4 and `settle` 4 from either. An `if`'s `else` counts in neither.
