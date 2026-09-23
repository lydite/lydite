# Never name an uploaded artifact inside `publish`'s `lydite-reports-*` glob

`lydite/actions`' `publish` step downloads every artifact in the run matching
`lydite-reports-*`, regardless of which job produced it, and folds it into this repository's own
verdict. An `actions/upload-artifact` step named for an unrelated reason — a proving-ground
fixture, a future job nobody thought to check against this glob — or a call to the local
`lydite-reports` composite action that never overrides its colliding default `prefix`
(`lydite-reports`), folds in silently: exactly what once happened to `ci-end2end.yml`'s
proving-ground legs before they were renamed to `proving-ground-reports-*` and renamed back only
after their own job downloaded them. `.github/assert-no-lydite-reports-collision.py`, run by
`ci-end2end.yml`'s `no-lydite-reports-collision` job, fails whenever an artifact name's fixed
text — the part before any `${{ … }}` expression — matches that prefix, or cannot be ruled out
against it from the text alone.

## Applies to

Any new or changed `actions/upload-artifact` step, or `./.github/actions/lydite-reports` call, in
a workflow under `.github/workflows/`.

## Example

```yaml
# wrong: the composite's default prefix collides with publish's glob
- uses: ./.github/actions/lydite-reports
  with:
    name: fixture

# right: named out of the glob's way
- uses: actions/upload-artifact@...
  with:
    name: proving-ground-reports-fixture
```

Reasoning: [`agentic/references/ci.md`](../references/ci.md).
