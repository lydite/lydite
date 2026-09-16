---
about: github.com/github/go-spdx/v2's spdxexp.Satisfies panics rather than erroring on some malformed licence expressions
saw:
  - source/cli/internal/licence/policy.go
---

`spdxexp.Satisfies` (v2.7.0) does not always answer a malformed SPDX expression with an error
return. `Satisfies("MIT OR (", []string{"MIT"})` is a nil-pointer dereference inside the
library's `parseOperator`, confirmed against the pinned version — not a hypothetical.

A licence expression reaches `internal/licence/policy.go`'s `Evaluate` as a dependency author's
free text pulled out of a manifest or licence file lydite does not own (a garbled `Cargo.toml`,
a hand-written `go.mod` licence comment fed through `Expression`, etc.), so it cannot be trusted
to parse cleanly. `policy.go`'s `satisfies` wraps the library call in a `defer recover()` and
treats a panic exactly like a parse error (→ `Unclassifiable`, licence side `unknown` in the
resulting pair). Without this, one malformed expression anywhere in a dependency tree ends the
whole scan rather than producing one `unknown` pair.

Also noted: the older Cargo slash form (`MIT/Apache-2.0`) is unreadable to spdxexp (`expected id
at offset 3`) and classifies as `unknown` rather than as the dual licence it means — older
crates still use this form. Left as `unknown` deliberately (correct per ADR 0038: an
unclassifiable licence grandfathers like any other), but a future licence-expression
normalisation pass (`/` → ` OR `) before handing text to spdxexp would recover this case.
