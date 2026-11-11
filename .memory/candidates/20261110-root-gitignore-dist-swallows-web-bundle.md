---
about: "the root .gitignore's unanchored dist/ (goreleaser output) also ignores source/web/dist, so the committed dashboard bundle is tracked only through source/web/.gitignore's !/dist/"
saw:
  - .gitignore
  - source/web/.gitignore
---
`.gitignore` ignores `dist/` with no leading slash, which matches at every depth (it exists for goreleaser's `dist/` at the root). `source/web/dist/` is committed on purpose (ADR 0010, docs/adr/0010-repo-topology.md), and is tracked only because `source/web/.gitignore` carries `!/dist/`. Deleting or moving that file silently stops a rebuilt bundle from being staged, and `bundle-hash.sh --check` then fails in CI against a dist/ git never saw change. Anchoring the root pattern to `/dist/` would be the alternative.
