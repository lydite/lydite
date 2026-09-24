---
about: how internal/orphan decides which files a component claims
saw: source/cli/internal/orphan/orphan.go
---

`internal/orphan`'s `claims` (called from `coveredByComponent`, which backs `Find`) has two
different claiming rules depending on the component's shape, not one. A component with a runner
or a `command:` — anything that runs a suite — claims every file under its `dir` by containment,
whatever language it's written in, because the suite runs over that directory and reaches
whatever it reaches. A component declaring `lang:` alone (no runner, no command — ADR 0056's
no-suite shape) claims only the files of its declared language under its directory, matched via
`runner.LangForExt` on the file's extension — never by containment.

This asymmetry is deliberate and load-bearing: a no-suite component runs nothing, so claiming by
containment would let `lang: shell` at `dir: .` silently clear the orphan gate for every file in
the whole repository while testing none of them. `coveredByLanguage` (the separate function
backing `orphan.Unscanned`, used by `lydite scan`'s "unscanned" warning) has the matching
asymmetry: it reads `Component.ScanLang()` and matches on scan language rather than on
`runner.Lookup(c.Runner)`, so a raw `command:` with no `lang:` still covers by containment (its
`ScanLang()` is empty, hits the "declared but unmeasurable" branch), while a `lang:`-only or
`command:`+`lang:` component covers exactly its declared language.
