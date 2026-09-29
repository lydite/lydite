---
name: rust-attribute-stacking-hides-test-from-nearest-sibling-check
kind: gotcha
description: "A Rust function can carry multiple stacked attributes (#[test] followed by"
anchors:
  - path: source/cli/internal/treesitter/testcode.go
    blob: 23ccdf01e3cc
confidence: suspect
---

`testWalk.rustAttributes` (`testcode.go`) walks the *whole run* of `attribute_item` preceding
siblings via `n.PrevSibling()` in a loop, stopping only at the first non-attribute, non-extra
(comment) sibling — deliberately not just one `PrevSibling()` call, which is the shape `TestModule`'s
existing single-sibling check uses for a different question (a module's own `#[cfg(test)]` marker,
never stacked with anything else in practice).

The landmine this guards against: `#[test]\n#[ignore]\nfn ignored_by_attribute() {...}` has
`#[ignore]` as the function's *immediate* preceding sibling, not `#[test]`. A single-sibling check
would read this function as carrying no recognised test attribute and silently fail to enumerate it
as a declared test — even though nextest itself recognises it as a test (it just doesn't run by
default, and is absent entirely from nextest's own JUnit report). Any future test-attribute
detection in this grammar must walk the whole preceding run, not the nearest sibling alone.
</content>
