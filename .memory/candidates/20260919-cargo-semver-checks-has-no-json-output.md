---
about: cargo-semver-checks 0.50.0 has no --output-format flag at all, stable or unstable
saw:
  - source/cli/internal/rustapisurface/rustapisurface.go
  - source/cli/internal/rustapisurface/testdata/exit-codes.txt
  - docs/adr/0040-an-undeclared-go-api-break-fails-and-a-declared-one-is-referred.md
---

Before writing `internal/rustapisurface`, the plan assumed `cargo semver-checks
--output-format json` existed, mirroring clippy's and cargo-audit's NDJSON output. It
does not: `-Z help` on the real 0.50.0 binary lists only `witness-hints`,
`consistency-check`, `stability-aware` — no output-format flag under stable or
`-Z unstable-options`. Passing `--output-format json` errors with `unexpected argument`.

The tool's actual contract, measured against a real probe crate: findings on stdout as
`--- failure <lint_id>: <title> ---` blocks with a `Failed in:` witness list, and the
verdict from the exit code alone — `0` nothing to report, `100` a `major` lint failed,
`101` the run could not be made (base doesn't build, no library target). Any future
Rust tool integration should probe the actual binary's `--help`/`-Z help` output before
assuming it has NDJSON, since not every cargo subcommand does.
