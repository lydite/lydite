---
name: vitest-describe-separator-and-skip-vs-omit
kind: gotcha
about: source/cli/internal/junit/testdata/vitest-probe-suite.xml
description: vitest's JUnit reporter joins a nested describe chain with a literal " > " (space, greater-than, space), and a test excluded by --testNamePattern/-t comes back as <skipped/> in the report rather than being omitted — the opposite of cargo-nextest, which omits a filtered-out test entirely.
anchors:
  - path: source/cli/internal/junit/testdata/vitest-probe-suite.xml
    blob: 635adfadce67024e2db7057537336f63981bcac9
  - path: source/cli/internal/junit/testdata/nextest-suite.xml
    blob: 5e490e6abf66b4a43fd1f4398d8ee73d7982c789
confidence: verified
---

Captured over a real nested `describe("outer", () => describe("inner", () => it("holds a
title two describes deep")))`: vitest's report records the full name as
`outer &gt; inner &gt; holds a title two describes deep` (XML-escaped `>`). This was
previously unverified in this codebase and is load-bearing for
`internal/treesitter.DeclaredTests`'s TypeScript qualification.

Separately, and more of a landmine: a `vitest run <files> -t '<pattern>'` invocation does
NOT omit tests the pattern doesn't match from its report — every test in the named files
still appears, with the excluded ones marked `<skipped/>`. `cargo nextest run -E
'test(=...)'`, by contrast, omits a filtered-out test entirely (confirmed:
`nextest-rerun.xml` holds only the filtered-in test cases). This asymmetry means a vitest
rerun's `<skipped/>` on a *new* test is ambiguous between "the test skips itself" and "the
filter pattern failed to match it" — which is why `internal/runner.TitlePattern` escaping
every regex metacharacter in a title (via `regexp.QuoteMeta`) is not optional: an
unescaped pattern silently produces the second case, reported by the gate as a
deterministic test disagreeing with itself.
