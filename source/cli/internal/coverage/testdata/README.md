# The captured lcov reports

`rust-lcov.info`, `ts-lcov-v8.info` and `ts-lcov-istanbul.info` are verbatim output of the
tools lydite pins, over the two probe trees beside them. They are the evidence
[ADR 0034](../../../../docs/adr/0034-an-exclusion-declaration-is-scoped-by-a-parser-in-every-language.md)
decides on, and a test that wants an lcov reads one of them rather than writing its own: a
hand-written report matches its author's reading of the format, which is the one thing it must
not be evidence for.

Each probe carries the shapes a scope has to survive — a free function, a method, a generic, a
closure and a nested function item — and two of them carry a real
`[lydite:exclude_from_coverage]` declaration. The declaration is a `//` line comment and not the
language's own doc comment (`///`, `/** */`) because `annotation.body` strips `//` and nothing
else, so those are the only comments a declaration can be written in today.

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
