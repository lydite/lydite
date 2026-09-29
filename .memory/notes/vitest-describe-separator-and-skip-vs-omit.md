---
name: vitest-describe-separator-and-skip-vs-omit
kind: gotcha
description: vitest's JUnit reporter joins a nested describe chain with a literal " > " (space, greater-than, space), and a test excluded by --testNamePattern/-t comes back as <skipped/> in the report rather than being omitted — the opposite of cargo-nextest, which omits a filtered-out test entirely.
anchors:
  - path: source/cli/internal/junit/testdata/vitest-probe-suite.xml
    blob: 635adfadce67
  - path: source/cli/internal/junit/testdata/nextest-suite.xml
    blob: 5e490e6abf66
confidence: suspect
---

A nested `describe("outer", () => describe("inner", () => it("holds a title")))` reports its full
name as `outer &gt; inner &gt; holds a title` (XML-escaped `>`) — load-bearing for
`internal/treesitter.DeclaredTests`'s TypeScript qualification.

More of a landmine: a `vitest run <files> -t '<pattern>'` invocation does NOT omit non-matching
tests from its report — every test in the named files still appears, with the excluded ones marked
`<skipped/>`. `cargo nextest run -E 'test(=...)'`, by contrast, omits a filtered-out test entirely.
This asymmetry means a vitest rerun's `<skipped/>` on a *new* test is ambiguous between "the test
skips itself" and "the filter pattern failed to match it" — which is why `internal/runner.TitlePattern`
escaping every regex metacharacter in a title (`regexp.QuoteMeta`) is not optional: an unescaped
pattern silently produces the second case, reported by the gate as a deterministic test disagreeing
with itself.
</content>
