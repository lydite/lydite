---
name: cargo-semver-checks-has-no-json-output
kind: gotcha
description: "cargo-semver-checks 0.50.0 has no --output-format flag at all, stable or unstable — its contract is findings on stdout as \"--- failure <lint_id>: <title> ---\" blocks, and the verdict is exit code alone (0 nothing, 100 major lint failed, 101 could not run)."
anchors:
  - path: source/cli/internal/rustapisurface/rustapisurface.go
    blob: debe4b008887
  - path: source/cli/internal/rustapisurface/testdata/exit-codes.txt
    blob: 90fb0f4aff89
confidence: verified
---

Before writing `internal/rustapisurface`, the plan assumed `cargo semver-checks --output-format
json` existed, mirroring clippy's and cargo-audit's NDJSON output. It does not: `-Z help` on the
real 0.50.0 binary lists only `witness-hints`, `consistency-check`, `stability-aware` — no
output-format flag under stable or `-Z unstable-options`. Passing `--output-format json` errors with
`unexpected argument`.

Any future Rust tool integration should probe the actual binary's `--help`/`-Z help` output before
assuming it has NDJSON, since not every cargo subcommand does. (`--release-type minor` is passed
unconditionally in the same file — see [[release-type-minor-is-load-bearing]].)
</content>
