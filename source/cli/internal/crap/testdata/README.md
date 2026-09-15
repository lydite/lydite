# The complexity probes, and what the oracles said about them

`rustprobe/` and `tsprobe/` carry one function per branching construct
[ADR 0036](../../../../docs/adr/0036-crap-scores-rust-and-typescript-from-a-hand-rolled-walk.md)
names a counting rule for. Each table below gives the number that ADR's rules predict, worked
out by hand from the source and not from the code that computes it — so a test asserting the
walk's output is checking it against something derived independently — and beside it the number
an oracle measured over the same function.

The oracles are `rust-code-analysis-cli`'s cyclomatic metric and ESLint's `complexity` rule, run
once, by hand, in a scratch directory outside this repository. Neither is a dependency: no
manifest names them, no `.github/dependabot.yml` entry watches them, and nothing lydite ships
invokes them. They exist to back the counting rules with a measurement — see the ADR's
"Oracles, not dependencies".

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
