---
name: config-package-doc-describes-rejected-keys
kind: gotcha
description: internal/config's package doc still advertises coverage.source and the coverage report-path keys, which the same file rejects by name.
anchors:
  - path: source/cli/internal/config/config.go
    blob: 63758512e6d5
confidence: verified
---

The package doc's second bullet (`internal/config/config.go:11-16`) says
`.lydite/config.yml` can "Describe the repo's pipeline: coverage.source says whether
lydite or a prior CI job owns coverage production, and coverage.{go,rust} locate that
job's reports."

The code in the same file does the opposite. `removedKeys` (`config.go:291-299`) lists
`coverage.source`, `coverage.go.report`, `coverage.rust.report` and `coverage.rust.lcov`
for `rejectRemoved`, with the message "lydite measures coverage from each component's
own instrumented run, so there is no longer a pipeline half that owns producing it",
pointing at `docs/adr/0019-coverage-per-component-gated-by-lydite-test.md`.
`TestLoadRejectsRemovedCoverageKeys` (`config_test.go:185`) asserts the rejection.

The package doc is what a reader opens first — godoc, or the top of the file, long
before scrolling to `removedKeys`. Acting on it (writing `coverage.source: report` back
into a config, or describing lydite's config surface from it) is wrong, and the only
correction arrives as a load-time error. What is true now: the file can opt out of what
lydite already does and tune the numeric gate knobs (tolerance, patch tolerance, floor);
it cannot say which job owns producing coverage, because lydite always produces it from
each component's instrumented run. AGENTS.md's Configuration section is correct and
matches the code. Nothing else in the file is stale.
