---
name: lydite-reports-composite-collides-with-publish-by-default
kind: gotcha
description: A call to the lydite-reports composite that does not override prefix uploads an artifact matching publish's lydite-reports-* glob with no visible upload-artifact step, which assert-no-lydite-reports-collision.py special-cases.
anchors:
  - path: .github/actions/lydite-reports/action.yml
    blob: dc2b8b206fa1
  - path: .github/assert-no-lydite-reports-collision.py
    blob: da7da921de7a
confidence: verified
---

`.github/actions/lydite-reports/action.yml`'s single `actions/upload-artifact` step names itself `${{ inputs.prefix }}-${{ inputs.name }}` (line 58), and `prefix` defaults to `lydite-reports` — deliberately, per its own input description (line ~21), because that is the prefix `publish` reads. A caller invoking `uses: ./.github/actions/lydite-reports` with only a `name:` therefore uploads an artifact matching `publish`'s `lydite-reports-*` glob without any raw `upload-artifact` step in the calling workflow's text — a shape a check grepping for `actions/upload-artifact@` cannot see. `.github/assert-no-lydite-reports-collision.py` special-cases this call shape, resolving the effective name from `prefix` (or the default) plus `name` before checking it against the glob (its header comment, lines ~6-20).
