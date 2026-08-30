# lydite
lydite unifies SAST/SCA/lint/coverage gating for Rust, TypeScript, and Go projects into one CLI, run identically locally and in CI.

## Language

**Aggregate coverage**:
The current whole-tree coverage percentage for one ecosystem, compared against a cached per-main-commit baseline (`lydite` branch). Catches regressions in code the current PR never touches (e.g. a deleted test file).
_Avoid_: "total coverage", "overall coverage".

**Patch coverage**:
The coverage percentage of only the lines added/modified by the current PR (HEAD vs. merge-base), gated against that same ecosystem's aggregate baseline (`patch% >= baseline%`). Catches untested new code even when the codebase is too large for it to move the aggregate percentage. Computed alongside aggregate coverage, not instead of it — they catch disjoint regression classes.
_Avoid_: "diff coverage" (used interchangeably by other tools like Codecov, but this repo standardizes on "patch coverage").

**Baseline**:
The aggregate coverage value cached on the `lydite` branch for a specific main-branch commit SHA, computed once per SHA. Both aggregate and patch coverage compare against this same value — patch coverage has no baseline concept of its own.

**Coverable line**:
A source line that a language's own coverage tool (`go tool cover`, `cargo llvm-cov`, Istanbul) reports an entry for. Comments, blank lines, imports, and braces are never coverable — they simply never appear in a coverage report, so patch coverage's denominator (coverable changed lines) excludes them automatically, without lydite doing any language-aware filtering itself.

**Linter**:
Which engine backs lydite's TypeScript check for a given repo — ESLint (the default) or Biome — selected by `typescript.linter` in `.lydite.yml`. The two are mutually exclusive; there is no "both". A repo's value is a **migration state**, not a per-invocation choice: it is a fact about that repo, the same for every run in it.
_Note_: the two are not interchangeable rule sets. Switching changes which findings lydite gates on, and under Biome the check gates on **correctness** as well as security — so the TypeScript check is "security findings only" under ESLint but not under Biome.
_Avoid_: "linting mode", "the TS linter setting".

**Pin**:
The exact version of a tool lydite installs and runs, recorded in a real package-manager manifest (`package.json`, `Cargo.toml`, `go.mod`, `requirements.txt`) so Dependabot can see and bump it — never only in a Go constant. The distinguishing property of a pin is that something must be able to *age it out*: a pin nothing can bump is indistinguishable from a scanner that has silently stopped being current.
_Avoid_: "vendored version", "bundled version" (lydite vendors no tool source; it installs pinned releases).

**Quality history**:
The record of a project's quality measurements over time, accumulated across runs and rendered as a dashboard. Distinct from **Baseline**, which is a single cached value for one commit: a baseline answers "did this PR regress?", quality history answers "where has this project been trending?".
_Avoid_: "reporting" (already means uploading to Codecov/Semgrep in `action.yml`, and `coverage.source: report` means a pre-existing coverage file), "state" (conflates it with the cache).

**Ledger**:
A measurement record that cannot be recomputed after the fact — the toolchain **Pin**s that produced it may have moved, and the commit may no longer exist after a squash-merge. Ledger entries are append-only and are never deleted, only compacted. Contrast **Cache**.

**Cache**:
Derived data that can be regenerated on demand by re-running the tool that produced it. Losing a cache entry costs time, not information, so cache writes are best-effort and non-fatal. The per-commit **Baseline** is a cache; **Quality history** is a **Ledger**. The distinction is not stylistic: it dictates whether a failed write may be ignored.

**Projection**:
A pre-computed rollup derived from the **Ledger**, existing so the dashboard reads one small file instead of the whole history. Regenerable by definition, so it is a **Cache** in every respect except that its source is the ledger rather than a scanner.

**Gap**:
An interval in the **Quality history** where a run happened but its ledger append did not land (a push race, a token scope, a branch-protection rule). Because ledger writes are non-fatal, gaps are possible by design — so they are recorded explicitly and rendered as a break in the trend line. The governing invariant is not "there are no gaps" but *the ledger never lies about its own completeness*: a chart that shows where data is missing is trustworthy, one that interpolates across a hole is not.

**Finding snapshot**:
The full set of findings from the most recent run — rule, path, severity, message — overwritten on every run. A **Cache**, not a **Ledger**: it is fully regenerable by re-scanning, and only the latest one is ever of interest. It answers "what is wrong right now, and where?"; the ledger answers "is this getting better?". Keeping findings out of the ledger is what lets quality history stay small enough to retain forever.
_Note_: because snapshots are not retained, "when did this finding first appear?" is deliberately unanswerable today. Answering it later means adding stable per-finding fingerprints and promoting findings to a ledger of their own — an additive change, which is why the split is the cheaper starting point.

## Flagged ambiguities
**"Threshold"** — the original patch-coverage feature request proposed a `coverage.patch.threshold` config (an arbitrary fixed percentage per language). This was superseded: patch coverage has no independent threshold — it always gates against the aggregate baseline (see **Patch coverage**). Only an `enabled: bool` (default `true`, opt-out) remains in config.

## Example dialogue
> **Dev:** If a PR adds 9 new lines with 0% patch coverage, does lydite fail it?
> **Domain expert:** Only if the aggregate baseline for that language is above 0% — patch coverage gates against baseline, not a fixed number. If the language has no baseline yet (first time seen), it's reported informationally and doesn't fail, same as aggregate coverage's `[NEW]` case.
> **Dev:** What about a changed comment line — does that count against patch coverage?
> **Domain expert:** No — it's never a coverable line, so it's excluded automatically once we intersect changed lines with what the coverage tool actually reports.
