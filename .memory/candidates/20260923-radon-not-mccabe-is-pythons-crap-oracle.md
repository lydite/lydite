---
name: radon-not-mccabe-is-pythons-crap-oracle
kind: rationale
about: source/cli/internal/crap/treesitter.go
description: mccabe (what flake8 carries) was rejected as CRAP's Python complexity oracle because it counts no and/or, no ternary, no comprehension and no match — radon's cc is the oracle instead, and the choice plus both tools' actual measured numbers are recorded in internal/crap/testdata/README.md.
anchors:
  - path: source/cli/internal/crap/treesitter.go
    blob: 7f5d9c10fe7e6524b57a6da6b2de1d5d22d88c69
  - path: source/cli/internal/crap/testdata/README.md
    blob: ed4d458a450e6c549180511a8387ddaf5863d402
confidence: verified
---

ADR 0036 requires a per-language complexity counting rule validated against that
ecosystem's own tool as an independent oracle. For Python the obvious first candidate is
`mccabe` (0.7.0), since it's what `flake8` ships — but `mccabe --min 1` run over the
`pyprobe/` fixtures scores every `and`/`or` as free, has no notion of a ternary
(`conditional_expression`) or a comprehension's implicit loop, and doesn't recognize
`match`/`case` at all. Agreeing with it would make Python the one language in this gate
whose short-circuiting operators cost nothing, breaking the parity Go/Rust/TypeScript
already hold with their own oracles (gocyclo/cyclop, rust-code-analysis, ESLint's
`complexity`).

`radon` (6.0.1) is the oracle instead — its `cc -s --show-closures` agrees with lydite's
hand-derived predictions on every construct except two, both anticipated by ADR 0036's own
reasoning and both resolved in lydite's favor: a `case other:` name-binding pattern (radon
reads every irrefutable pattern as the wildcard; lydite counts only the literal `_`,
mirroring Rust's `match`-arm rule), and the flat +1 a parent gets for containing a nested
`def` (radon gives the nested function its own scope and doesn't add the parent increment;
ADR 0036's amendment states the increment is deliberate). Both tools' exact numbers, over
the same probe functions, are recorded side by side in
`source/cli/internal/crap/testdata/README.md` — worth reading before assuming either
tool's number is simply correct.
