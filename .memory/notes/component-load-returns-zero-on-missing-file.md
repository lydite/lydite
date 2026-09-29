---
name: component-load-returns-zero-on-missing-file
kind: gotcha
description: component.Load(root) returns File{}, nil (not an error) when .lydite/components.yml is absent — a completeness check that ranges over file.Components without also checking whether anything was read will vacuously pass on any repository, or any test fixture, with no declaration at all.
anchors:
  - path: source/cli/internal/component/component.go
    blob: 78b9519abbc5
confidence: suspect
---

`load(root, strict)` (backing both `Load` and `LoadHistorical`) treats `os.ErrNotExist` on
`.lydite/components.yml` as the correct day-one state and returns `File{}, nil` — zero components,
no error. Deliberate and documented ("a repository that declares none is one lydite runs no tests
for").

The gotcha: a check that must hold every *declared* component to a rule iterates zero times and
silently passes whenever there is no `components.yml` at all — which is every test fixture that
doesn't explicitly declare one. This produced a real fail-open bug in the dependency-evidence check
(`internal/reviewdecision/evidence.go`'s `ScanEvidence`) during development: the first fix iterated
only `file.Components`, and every existing test (none of which declare `components.yml`) still
passed, masking that a *real* repository's declared-but-silent component (a language switched off
in `.lydite/config.yml`, producing zero rows) would also read as satisfied. The working fix checks
the union of declared component names and names the scan document actually mentions, never
`file.Components` alone.
</content>
