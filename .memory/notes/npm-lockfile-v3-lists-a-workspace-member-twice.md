---
name: npm-lockfile-v3-lists-a-workspace-member-twice
kind: gotcha
description: package-lock.json v3 lists a workspace member both under its own directory and as a node_modules link entry, so a licence reader that skips only the link reports the repository's own packages as unknown-licence dependencies.
anchors:
  - path: source/cli/internal/typescript/licence.go
    blob: c1e23b1d206f
  - path: source/cloud-services/package-lock.json
    blob: 9877fa78aeb8
confidence: verified
---

`package-lock.json` v3's flat `packages` map lists a workspace member twice: once keyed by its own directory (e.g. `"pr-relay"`, `"libs/github-app"`) — the source entry, which states no `license` because it is the repository's own code — and once under `node_modules/<name>` (e.g. `"node_modules/@lydite/pr-relay"`) with `"link": true`, the alias other packages resolve through. `source/cloud-services/package-lock.json` shows both forms for its three local packages.

A reader that skips only the `link: true` alias would report the workspace's own packages as dependencies of unknown licence. `internal/typescript/licence.go`'s `packageName` (line 316) sidesteps the source entry for free: only a key containing `node_modules/` names an installed package, so a bare `"pr-relay"` key returns `ok == false`; the `p.Link` checks (lines 185, 303) handle the alias. The two skips look redundant but each covers a different entry.
