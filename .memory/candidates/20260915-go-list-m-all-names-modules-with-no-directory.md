---
about: go list -m all names modules the build never downloads, so a dependency enumerator must scope to `go list -deps -json`, not the module graph
saw:
  - source/cli/internal/golang/licence.go
  - source/cli/internal/licence/testdata/go-list-module-graph.txt
---

Measured over `source/cli` itself: `go list -m all` names 20 modules, and 12 of them answer no
directory at all (`Dir` is empty) — they are graph nodes the module resolver knows about
(reachable through some dependency's `go.mod`) but the build never actually downloads or
compiles, because nothing in `source/cli`'s own compiled packages imports them.

A tool that classifies every module the graph names (rather than every module the build
actually compiles) would call each of those 12 `unknown` for lack of a directory to read
licence files from, and an allow-list-based gate would then reject a repository over
dependencies its own binary never contains.

`go list -deps -json ./...` (without `-test`) answers a `Dir` for every module it names,
because it only walks packages the build graph actually reaches. This is the enumeration
`internal/golang/licence.go`'s `licenceDependencies` uses. `-test` is also deliberately absent:
adding it would pull in test-only dependencies (e.g. `github.com/google/go-cmp`), which are not
part of what a consumer's binary distributes.
