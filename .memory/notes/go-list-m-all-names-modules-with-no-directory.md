---
name: go-list-m-all-names-modules-with-no-directory
kind: gotcha
description: "`go list -m all` names modules the build never downloads or compiles (12 of 20 for source/cli, each with an empty Dir) — a Go dependency enumerator must scope to `go list -deps -json`, never the module graph, or it misclassifies uncompiled modules as unreadable and rejects the repo over deps its binary never contains."
anchors:
  - path: source/cli/internal/golang/licence.go
    blob: 0ed5217c557a
  - path: source/cli/internal/licence/testdata/go-list-module-graph.txt
    blob: dd2b28030c06
confidence: verified
---

Measured over `source/cli` itself: `go list -m all` names 20 modules, and 12 of them
answer no directory at all (`Dir` is empty) — they are graph nodes the module resolver
knows about (reachable through some dependency's `go.mod`) but the build never actually
downloads or compiles, because nothing in `source/cli`'s own compiled packages imports
them.

A tool that classifies every module the graph names (rather than every module the build
actually compiles) would call each of those 12 `unknown` for lack of a directory to read
license files from, and an allow-list-based gate would then reject a repository over
dependencies its own binary never contains.

`go list -deps -json ./...` (without `-test`) answers a `Dir` for every module it names,
because it only walks packages the build graph actually reaches. This is the
enumeration `internal/golang/licence.go`'s `licenceDependencies` (line 160) uses, fed by
`goListArgv` (line 61: `["list", "-deps", "-json", "./..."]`) — its own comment
(line 33) states the scope choice explicitly. `-test` is also deliberately absent:
adding it would pull in test-only dependencies (e.g. `github.com/google/go-cmp`), which
aren't part of what a consumer's binary distributes.
