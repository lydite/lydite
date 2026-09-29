---
name: orphan-claims-by-containment-except-a-no-suite-component
kind: invariant
description: "A component with a runner or command claims every file under its dir by containment, but a lang-only component claims only files of its declared language, so lang: shell at dir . cannot clear the orphan gate repo-wide."
anchors:
  - path: source/cli/internal/orphan/orphan.go
    blob: 35f44afe37d8
confidence: verified
---

`internal/orphan`'s `claims` (`orphan.go:148`, called from `coveredByComponent`, `:129`, which backs `Find`) has two claiming rules. A component with a runner or a `command:` — anything that runs a suite — claims every file under its `dir` by containment, whatever the language, because the suite runs over that directory. A component declaring `lang:` alone (no runner, no command — ADR 0056's no-suite shape) claims only files of its declared language under its directory, matched via `runner.LangForExt`.

The asymmetry is load-bearing: a no-suite component runs nothing, so claiming by containment would let `lang: shell` at `dir: .` silently clear the orphan gate for every file in the repository while testing none. `coveredByLanguage` (`:301`, behind `orphan.Unscanned` and `lydite scan`'s "unscanned" warning) has the matching asymmetry: it reads `Component.ScanLang()` rather than `runner.Lookup(c.Runner)`, so a raw `command:` with no `lang:` covers by containment while a `lang:`-only or `command:`+`lang:` component covers exactly its declared language.
