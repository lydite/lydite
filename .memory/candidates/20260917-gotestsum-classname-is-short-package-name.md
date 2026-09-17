---
name: gotestsum-classname-is-short-package-name
kind: gotcha
about: source/cli/internal/flaky/testdata/run1-suite.xml
description: gotestsum's pkgname-format JUnit report writes a testcase's classname as the package's short name (e.g. "flakyprobe"), never the full import path or scan-root-relative directory — it cannot disambiguate two Go packages sharing a base name.
anchors:
  - path: source/cli/internal/flaky/testdata/run1-suite.xml
    blob: ee17472553d82a3927a162b879ac1425a5a29bad
confidence: verified
---

Captured gotestsum output for a package at `internal/flaky/testdata/flakyprobe` records
`<testcase classname="flakyprobe" name="TestDeterministicNew">` — `classname` is the bare
package identifier, not `internal/flaky/testdata/flakyprobe` or any import path. This
matters for anything tempted to key Go test outcomes by `classname`+`name` instead of
`name` alone (ADR 0041's decision, `internal/junit.ReadOutcomesByClass`): doing so would
not actually fix Go's disambiguation problem, since two packages with the same base name
in different directories would still collide on `classname`. Go's rerun stays
collision-free because it scopes one `go test` process to one package (compiler forbids
two same-named functions there), not because of anything `classname` carries.
