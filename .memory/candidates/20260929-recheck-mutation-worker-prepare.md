---
about: mutation worker empty-root claim still true; line pointers moved
saw:
  - source/cli/internal/stages/mutation/run.go
  - source/cli/internal/runner/runner.go
  - source/cli/internal/test/run/prepare.go
targets: mutation-worker-prepare-gets-no-scan-root
verdict: still-true
---
Worker Prepare passes "" at stages/mutation/run.go:389 (not :278); baseline passes in.Dir at :451. installNodeDeps is runner.go:929 with the `root == ""` -> declarationRoot fallback at :930-932; prepareCommand (prepare.go:87) still has no fallback. KindRawCommand stop confirmed at run.go:353-354.
