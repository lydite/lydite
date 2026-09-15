---
about: .github/workflows/lydite-pr.yml
saw: fix/one-shard-folds branch, commits c8b8adc..dd046cb
---

`lydite-pr.yml`'s `merge`, `mutation-merge` and `publish` jobs locate their fold inputs by
finding the report document each shard/job must have written (`find <dir> -maxdepth 2 -name
<document> -exec dirname {} \; | sort -u`), not by globbing a fixed directory nesting depth
(`for dir in x/*/`).

`actions/download-artifact` only gives a matched artifact its own subdirectory when the
pattern matched two or more times; a lone match extracts flat into `path`. A repository
declaring exactly one component (the common case for a single-service repo, per lydite's own
model where a cargo/npm workspace is one component) plans exactly one shard, so its
`measurements.json`/`mutation.json`/etc. lands one level higher than a directory glob looks —
the glob then finds only the shard's per-component log subdirectories and folds nothing, or
folds a log directory as if it were a report.

`publish`'s document names are `review.json`, `scan.json`, `test.json`, `mutation.json`
(`source/cli/cmd/lydite/reports.go`'s `documentName`); `measurements.json` is deliberately
excluded from that set (`readDocuments` skips it by name).

`merge-multiple: true` on the `download-artifact` step is not a fix for this: it collapses
every shard's document onto one path, destroying the per-shard separation completeness is
decided from.

See [[find-a-folds-inputs-by-document-not-by-directory-depth]] for the prescriptive rule, and
`agentic/references/ci.md` / `agentic/references/actions.md` for the full reasoning.
