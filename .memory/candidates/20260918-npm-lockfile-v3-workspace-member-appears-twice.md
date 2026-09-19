---
about: an npm workspace's local package appears as two entries in package-lock.json v3, not one
saw:
  - source/cloud-services/package-lock.json
  - source/cli/internal/typescript/licence.go
---

`package-lock.json` schema version 3's flat `packages` map lists a workspace member twice:
once keyed by its own directory (e.g. `"pr-relay"`, `"libs/github-app"`) — the source entry,
which states no `license`/`licenses` field because it's the repository's own code — and once
keyed under `node_modules/<name>` (e.g. `"node_modules/@lydite/pr-relay"`) with `"link": true`,
which is the alias other packages resolve the dependency through.

A licence-source reader that skips only the `link: true` alias and not the bare-directory source
entry will report the workspace's own packages as dependencies with an unknown licence — a real
bug caught during implementation (confirmed against `source/cloud-services/package-lock.json`'s
actual three local packages: `libs/github-app`, `oauth-exchange`, `pr-relay`, each appearing as
both forms). `internal/typescript/licence.go`'s `packageName` sidesteps this for free: only a
key containing `node_modules/` names an installed package at all (`packageName` returns
`ok == false` for a bare `"pr-relay"` key), so the source entry is already excluded before the
`link: true` check is even reached — the two skips look redundant but only the second one is
doing anything for the alias entry; the first (implicit) one handles the source entry.
