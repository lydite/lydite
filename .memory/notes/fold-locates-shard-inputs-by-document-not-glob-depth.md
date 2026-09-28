---
name: fold-locates-shard-inputs-by-document-not-glob-depth
kind: invariant
description: A fold step locates its --reports directories by finding the document each shard wrote (find <dir> -maxdepth 2 -name <document>), never by globbing a fixed nesting depth, because a lone-match download-artifact extracts flat while a multi-match one nests per artifact.
anchors:
  - path: .github/workflows/ci-end2end.yml
    blob: 684d5c2b9fc1
  - path: source/cli/cmd/lydite/reports.go
    blob: 43cb0c5f23e8
  - path: agentic/rules/find-a-folds-inputs-by-document-not-by-directory-depth.md
    blob: 447a3241eb17
confidence: verified
---

`ci-end2end.yml`'s "lydite test merge (one shard, proving ground)" and "lydite mutation merge"
steps find their inputs with `find shards -maxdepth 2 \( -name measurements.json -o -name
test.json \) -exec dirname {} \;` (confirmed at lines ~587-598, ~667-673) rather than a fixed
`for dir in x/*/` glob. `actions/download-artifact` only gives a matched artifact its own
subdirectory when the pattern matched two or more; a lone match extracts flat into `path`. A
repository declaring exactly one component plans exactly one shard, so its report document lands
one level higher than a fixed-depth glob looks — depth 2 reaches both the flat and the nested
shape. `publish`'s document names are `<command>.json` (`reports.go`'s `documentName`); `readDocuments`
skips `measurements.json`/`mutants.json` by name since they carry no command or verdict.
`merge-multiple: true` is not a fix: it collapses every shard's document onto one path, destroying
the per-shard separation completeness is decided from.

The `ci-end2end.yml` "one component" proving-ground leg (`matrix.component != ''`) is a hand-written
mirror of this pattern, not shared code with the general fold — its own comments still describe
itself as mirroring `lydite-pr.yml`'s `merge` job, which (along with `lydite-baseline.yml`) no
longer exists in this repository (superseded by `lydite/actions`, per ADR 0051). A future change to
the general fold's document-discovery pattern has to be repeated in this leg by hand or it silently
stops proving what it exists to prove.

See [[find-a-folds-inputs-by-document-not-by-directory-depth]] for the prescriptive rule.
</content>
