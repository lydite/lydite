# The captured lcov reports

`rust-lcov.info`, `ts-lcov-v8.info`, `ts-lcov-istanbul.info` and `python-lcov.info` are verbatim
output of the tools lydite pins, over the probe trees beside them. They are the evidence
[ADR 0034](../../../../docs/adr/0034-an-exclusion-declaration-is-scoped-by-a-parser-in-every-language.md)
decides on, and a test that wants an lcov reads one of them rather than writing its own: a
hand-written report matches its author's reading of the format, which is the one thing it must
not be evidence for.

Each probe carries the shapes a scope has to survive in its own language — a free function, a
method, a nested function and a closure everywhere, plus a generic in the two that have one —
one function the suite never calls, and a real `[lydite:exclude_from_coverage]` declaration. In
Rust and TypeScript the declaration is a `//` line comment and not the language's own doc
comment (`///`, `/** */`) because `annotation.body` strips `//` and nothing else among their own
comment forms, so those are the only comments a declaration can be written in today.

`pylcovprobe`'s declaration is a `#` comment, which `annotation.body` strips as its own
introducer exactly as it strips `//`. A Python component's measurement takes its function out of
the figure the same way Rust's and TypeScript's do — the deduction's reach is the span
`internal/treesitter`'s Python tables report for the `def` beneath the declaration — which is
what `TestAPythonDeclarationTakesItsFunctionOutOfTheFigure` pins.

Source files carry a `.txt` suffix and are materialised by `internal/fixture`, whose doc comment
says why.

## How they were captured

Rust, with the `cargo-llvm-cov` version `internal/runner/cargo-llvm-cov-pin` pins:

```sh
cargo llvm-cov nextest --remap-path-prefix --lcov --output-path rust-lcov.info
```

`--remap-path-prefix` is the one deviation from the argv `internal/runner` builds, and it changes
one field: `SF:` reads `src/lib.rs` instead of the capturing machine's absolute path. Every `FN`,
`FNDA` and `DA` record is identical with and without it. lydite's own invocation omits the flag,
so the reports it reads in anger carry an absolute `SF:`.

TypeScript, with the vitest and provider versions `source/cloud-services` pins:

```sh
vitest run --coverage --coverage.provider=v8       --coverage.reporter=lcovonly …
vitest run --coverage --coverage.provider=istanbul --coverage.reporter=lcovonly …
```

Both providers are captured because lydite supports both, and they disagree: the `FN` and `DA`
lines are identical, and the *names* in the `FN` records are not. v8 names the two class methods
`bump` and `read`; istanbul names the same two `(anonymous_5)` and `(anonymous_6)`.

Python, with pytest-cov and coverage.py, the argv `internal/runner`'s `buildPytest` derives,
run in the probe directory:

```sh
python3 -m pytest --cov=. --cov-report=lcov:.lydite-reports/coverage/lcov.info \
  --junitxml=.lydite-reports/junit.xml
```

No deviation at all, so `python-lcov.info` is what lydite reads in anger. `SF:` is relative to
the directory the invocation ran in, which is the component's own — unlike cargo-llvm-cov's
absolute paths, and the reason no `--remap-path-prefix` equivalent was needed. Both the module
and the test module are in the report, because `--cov=.` measures the tree the component
declares. coverage.py's `FN` records carry an end line as well as a start (`FN:4,5,add`), which
neither of the other two producers emits; lydite reads a span off a parser regardless, so the
extra field changes nothing about how the trace is read.
