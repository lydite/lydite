---
about: a fold step locates its --reports directories by finding the document each shard wrote (find <dir> -maxdepth 2 -name <document>), never by globbing a fixed nesting depth; in this repository the pattern lives in ci-end2end.yml's one-shard proving-ground legs, since lydite-pr.yml no longer exists here
saw:
  - .github/workflows/ci-end2end.yml
  - source/cli/cmd/lydite/reports.go
  - agentic/references/ci.md
  - agentic/rules/find-a-folds-inputs-by-document-not-by-directory-depth.md
---

`ci-end2end.yml`'s "lydite test merge (one shard, proving ground)" step (line ~581) finds its
inputs with `find shards -maxdepth 2 \( -name measurements.json -o -name test.json \) -exec
dirname {} \; | sort -u`, and "lydite mutation merge (one shard, proving ground)" (line ~661)
with `find mutation-shards -maxdepth 2 -name mutation.json -exec dirname {} \; | sort -u` — both
print the downloaded layout first, and both refuse to fold when the find returns nothing. The
steps' own comments still describe themselves as mirroring "`lydite-pr.yml`'s `merge` job";
that workflow and `lydite-baseline.yml` are gone from this repository (`agentic/references/ci.md`
says so, lines ~20-22: superseded by the reusable workflow in `lydite/actions`), and the
prescriptive rule (`agentic/rules/find-a-folds-inputs-by-document-not-by-directory-depth.md`)
still names them in its "Applies to".

`actions/download-artifact` only gives a matched artifact its own subdirectory when the
pattern matched two or more; a lone match extracts flat into `path`. A repository declaring
exactly one component plans exactly one shard, so its `measurements.json`/`mutation.json`/etc.
lands one level higher than a `for dir in x/*/` glob looks — the glob then finds only the
shard's per-component log subdirectories and folds nothing, or folds a log directory as if it
were a report. Depth 2 reaches both the flat and the nested shape and stops above the
per-component directories.

`publish`'s document names are `<command>.json` (`source/cli/cmd/lydite/reports.go`'s
`documentName`, line ~59). `readDocuments` (same file, line ~159) skips `measurements.json`
and `mutants.json` by name — both share the directory and the extension but carry no command
and no verdict, so reading them as reports would refuse the file.

`merge-multiple: true` on the `download-artifact` step is not a fix: it collapses every shard's
document onto one path, destroying the per-shard separation completeness is decided from.

See [[find-a-folds-inputs-by-document-not-by-directory-depth]] for the prescriptive rule.
