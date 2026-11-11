---
about: "web-bundle.yml's guard proves dist/ matches bundle.sha256 and catches a dist change with no input change, but never that dist/ is what the inputs produce"
saw:
  - source/web/scripts/bundle-hash.sh
  - .github/workflows/web-bundle.yml
  - docs/adr/0010-repo-topology.md
---
`bundle.sha256` holds two digests (`inputs`, `dist`). `--check` fails when either disagrees with the tree. `--check-against <merge-base>` (pull requests) also fails when the recorded dist digest moved while the inputs digest did not. A change that touches any input (a comment under src/**, a lockfile bump) and also hand-edits dist/, then re-records with `--write`, passes both: proving dist/ is what the inputs build would need a rebuild in CI, which ADR 0010 rules out ("No Node in the check, and no dependence on Vite being byte-reproducible"). The reviewer owns the dist/ diff whenever it moves. The workflow is also outside ci-gate's needs, so a red result does not block a merge. Locally, Vite 8 rebuilt identical asset names from identical inputs, but nothing relies on that.
