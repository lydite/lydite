---
about: in a diff-scoped lydite mutation run, every [lydite:exclude_from_mutation] declaration in a file with at least one changed-and-executed line is checked, and one on a line the change did not touch matches no mutant by construction — so an untouched, still-valid exclusion is named "covers no mutant" on stderr for any change touching its file, not only when its own line is in scope
saw:
  - source/cli/internal/mutation/sites.go
  - source/cli/internal/mutation/golang.go
  - source/cli/internal/mutation/treesitter.go
  - source/cli/internal/mutation/mutation.go
  - source/cli/internal/stages/mutation/generate.go
  - source/cli/cmd/lydite/mutation.go
  - agentic/references/mutation.md
---

`internal/stages/mutation/generate.go`'s `generate` (line ~29) calls `mutation.Generate` once per
file that has at least one line both in the diff scope and reported executed (`lines`, lines
~31-44), and writes every returned `UnmatchedDeclaration` to the run's diagnostics as
`lydite: <component>: <path>:<line>: [lydite:exclude_from_mutation] covers no mutant` (lines
~74-76; the text is `UnmatchedDeclaration.String()`, `internal/mutation/mutation.go` line ~343).
`cmd/lydite/mutation.go` wires diagnostics to `os.Stderr` (`Params.Diagnostics`, line ~139).

The generators read *every* declaration in the file — `GenerateGo` (`golang.go` line ~84) and
`GenerateTreeSitter` (`treesitter.go` line ~350) collect `annotation.Declarations` over all the
file's comments — but `sites.add` (`sites.go` line ~49) keeps a mutant only if every line it
spans is in `lines`. `sites.resolve` (line ~90) then checks each declaration against those kept
spans only, and a declaration no span contains is returned unmatched. A declaration on a line the
change did not touch (or did not execute) therefore has no mutant to match, however correct it
is, and is reported every time its file is otherwise in scope.

Concrete case: `cmd/lydite/mutation.go` line ~98, `stop() // [lydite:exclude_from_mutation][...]`
inside `RunE`'s interrupt goroutine. Any pull request that changes and executes some other line of
`mutation.go` gets `lydite: cli: cmd/lydite/mutation.go:98: ... covers no mutant` on stderr,
though the declaration still answers the mutant it was written for whenever line 98 itself is in
scope. The same holds for `internal/stages/mutation/generate.go` line ~91's declaration.

The message is a diagnostic only — no row, no finding, no effect on the verdict — but read at face
value it tells a reviewer to remove a declaration that is still needed. `agentic/references/mutation.md`
(line ~91) and ADR 0027 (line ~114) say a declaration covering no mutant is reported, and neither
says the check runs over declarations outside the change.
