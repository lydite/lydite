# Derive `Crashed` from the tool's own report, never from `Err`/`Ok` or the exit code

Every scanner wrapped here exits non-zero when it finds something, so a failing exit code or a
set `Err` means "crashed or found something" and cannot tell the two apart — Biome's wrapper
reuses `Err` on purpose to report a finding, which would make every Biome finding read as a
crash if `Crashed` were derived from it. A consumer that diffs one run's findings against
another's (`findingScope` in `cmd/lydite/record.go`) needs to know when the claims it has are
not a complete answer: a scanner that crashed and reported zero findings looks identical to a
clean run unless something else says otherwise, and treating a crashed run as clean writes
every finding open in that bucket as resolved, then reappearing as new the next time the
scanner runs cleanly — permanent churn in a ledger nothing prunes.

Each wrapper must set `executil.Result.Crashed` from what only that tool's own structured
report can say: it did not install, its report would not parse, or the report's own statement
that it did not finish everything it was given (a package that would not compile, a file it
could not read).

## Applies to

`source/cli/internal/executil.Result.Crashed`, and every wrapper that sets it: gosec,
govulncheck, Biome, cargo-audit, cargo-deny, clippy, Semgrep, gitleaks, shellcheck, and the
licence gate. Any new scanner wrapper added beside these must decide `Crashed` the same way.

## Example

```go
// wrong: a failing exit code also means "found something" for every scanner here
result.Crashed = result.Err != nil

// right: the tool's own report is the only source
result.Crashed = report.PackageDidNotCompile() || report.Truncated
```

Reasoning: [`agentic/references/scanning.md`](../references/scanning.md),
[`agentic/references/quality-history.md`](../references/quality-history.md), and
[ADR 0058](../../docs/adr/0058-a-findings-detail-reaches-the-ledger-as-transitions.md).
