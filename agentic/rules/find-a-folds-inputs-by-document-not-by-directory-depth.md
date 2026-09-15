# Find a fold's inputs by the document each shard wrote, not by directory depth

`actions/download-artifact` gives a matched artifact its own subdirectory only when the pattern
matched two or more; a lone match is extracted straight into `path`. A `for dir in x/*/` glob
assumes the nested shape and, over a single shard or a single job, walks into that shard's own
per-component log directories instead — which a repository declaring exactly one component hits
on every run, not as an edge case. Locate the report document each shard must have written
(`measurements.json`, `mutation.json`, `scan.json`, `review.json`, `test.json`) with
`find <dir> -maxdepth 2 -name <document> -exec dirname {} \;`, and fold whatever directories that
returns — never the glob.

## Applies to

Any step in `.github/workflows/lydite-pr.yml` or `lydite-baseline.yml` that folds shard or job
artifacts after a `download-artifact` step, and the mirrored fold in `lydite/actions`'s reusable
workflow (tracked there as lydite/actions#4 — not fixed in this repository).

## Example

```bash
# ✗ assumes download-artifact always nests
for dir in shards/*/; do args+=(--reports "${dir%/}"); done

# ✓ finds the document a shard must have written, at either depth
while IFS= read -r dir; do args+=(--reports "$dir"); done \
  < <(find shards -maxdepth 2 -name measurements.json -exec dirname {} \; | sort -u)
```

Reasoning: [`agentic/references/ci.md`](../references/ci.md).
