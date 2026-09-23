---
about: a call to the lydite-reports composite action that doesn't override `prefix` uploads an artifact named `lydite-reports-<name>` — colliding with publish's own glob by default, with no upload-artifact step of its own visible in the calling workflow
saw:
  - .github/actions/lydite-reports/action.yml
  - .github/assert-no-lydite-reports-collision.py
---

`lydite-reports/action.yml`'s single `actions/upload-artifact` step names itself
`${{ inputs.prefix }}-${{ inputs.name }}`, and `prefix` defaults to `lydite-reports` — deliberately,
per its own input description, because that is the prefix `publish` reads. A caller that invokes
`uses: ./.github/actions/lydite-reports` with only a `name:` input therefore uploads an artifact
matching `publish`'s `lydite-reports-*` glob without a raw `upload-artifact` step anywhere in the
calling workflow's own text — a shape a check that only greps for `actions/upload-artifact@`
literally cannot see. `.github/assert-no-lydite-reports-collision.py` special-cases this call
shape, resolving the effective name from `prefix` (or the default) plus `name` before checking it
against the glob, rather than only scanning raw `upload-artifact` steps.
