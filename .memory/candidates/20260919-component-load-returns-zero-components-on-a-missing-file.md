---
name: component-load-returns-zero-components-on-a-missing-file
kind: gotcha
about: source/cli/internal/component/component.go
description: component.Load(root) returns File{}, nil (not an error) when .lydite/components.yml is absent — a completeness check that ranges over file.Components without also checking whether anything was read will vacuously pass on any repository, or any test fixture, with no declaration at all.
anchors:
  - path: source/cli/internal/component/component.go
    blob: b270357cb906bb408d7c237cbf1de1101896d0d5
confidence: verified
---

`load(root, strict)` (backing both `Load` and `LoadHistorical`) treats `os.ErrNotExist` on
`.lydite/components.yml` as the correct day-one state and returns `File{}, nil` — zero
components, no error. This is deliberate and documented ("a repository that declares none
is one lydite runs no tests for").

The gotcha is for anyone writing a check that must hold every *declared* component to a
rule: `for _, c := range file.Components { ... }` iterates zero times and the check
silently passes whenever there is no `components.yml` at all — which is every test
fixture in `cmd/lydite`'s own test suite that does not explicitly declare one (e.g.
`review_test.go`'s `bumpRepo`/`goSum`-based fixtures). This produced a real fail-open bug
in `cmd/lydite/review_depdelta.go`'s `dependencyGatesPassed` during development: the first
fix iterated only `file.Components` and every existing test (none of which declare
`components.yml`) still passed, masking that a *real* repository's declared-but-silent
component (a language switched off in `.lydite/config.yml`, producing zero rows) would
also read as satisfied. The working fix checks the union of declared component names and
names the scan document actually mentions, never `file.Components` alone.
