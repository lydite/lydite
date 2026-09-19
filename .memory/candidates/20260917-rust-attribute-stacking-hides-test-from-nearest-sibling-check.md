---
name: rust-attribute-stacking-hides-test-from-nearest-sibling-check
kind: gotcha
about: source/cli/internal/treesitter/testcode.go
description: a Rust function can carry multiple stacked attributes (#[test] followed by #[ignore]) as separate preceding-sibling attribute_item nodes — a check that only reads the immediate preceding sibling misses #[test] whenever another attribute sits between it and the function.
anchors:
  - path: source/cli/internal/treesitter/testcode.go
    blob: a02785482000fd74e013c659b0c370e22819410b
confidence: verified
---

`testWalk.rustAttributes` (`testcode.go`) walks the *whole run* of `attribute_item`
preceding siblings via `n.PrevSibling()` in a loop, stopping only at the first
non-attribute, non-extra (comment) sibling — deliberately not just `n.PrevSibling()`
once, which is the shape `TestModule`'s existing single-sibling check uses for a
different question (a module's own `#[cfg(test)]` marker, which is never stacked with
anything else in practice).

The landmine this guards against: `#[test]\n#[ignore]\nfn ignored_by_attribute() {...}`
(captured in `internal/flaky/testdata/nextestprobe/tests/c.rs.txt`) has `#[ignore]` as the
function's *immediate* preceding sibling, not `#[test]`. A single-sibling check would
read this function as carrying no recognised test attribute at all and silently fail to
enumerate it as a declared test — even though nextest itself recognises it as a test (it
just doesn't run by default, and is absent entirely from nextest's own JUnit report as a
separate, already-documented fact). Any future test-attribute detection in this grammar
must walk the whole preceding run, not the nearest sibling alone.
