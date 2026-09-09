# `lydite/actions`

> **The reference for `.github/actions/` and the `lydite/actions` repository.**

The actions a consumer runs live in [`lydite/actions`](https://github.com/lydite/actions), not
here (ADR 0010). It ships `setup`, `review`, `scan`, `test` and `publish`, plus a reusable
`.github/workflows/lydite.yml` that runs the referral, the scan and the gated suites in parallel
and renders one comment from all of them. Consumers pin the floating major, `@v1`, which a
release moves.

**What is here is the dogfood, and the two are deliberately the same shape.**
`.github/workflows/lydite-pr.yml` runs those four concerns through the local composites in
`.github/actions/` — `lydite-binary`, `lydite-reports`, `lydite-comment`. The one difference is
that `lydite-binary` uses the binary the pull request built, because a dogfood against the last
release tests the last release. When the shape here changes, that repository is where the change
has to land as well; nothing enforces it.

**A consumer's comment is rendered by `lydite publish` and nothing else.** The posting step takes
a file and a marker and knows nothing about coverage, components or verdicts, which is what stops
a refinement to the comment becoming a two-repository release. It is also why a sticky-comment
action earns nothing: the marker upsert is twenty lines of `gh api`, and the relay does the same
thing on the authenticated path.

**It uploads to no third party, and Codecov is not coming back.** A run's coverage and JUnit
results go to its own artifacts and nowhere else. lydite replaced Codecov as the blocking gate,
and a dashboard is not a reason to keep the relationship: it would put a token in CI for
something non-blocking, and the history it offers is what
[#26](https://github.com/lydite/lydite/issues/26)'s ledger is for — owned here, where the
measurements are made, rather than reconstructed by a third party from uploads.

**Only `publish` authenticates**, and the split matters: `id-token: write` for the relay, and
`pull-requests: write` for the fallback path alone. Every other job holds a read-only token, which
is what lets `test` run a pull request's own suites and `setup`/`teardown` shell without holding
anything that can write.

**Never interpolate `${{ inputs.* }}` or `${{ steps.*.outputs.* }}` directly into a `run:` script
body** — pass it via that step's `env:` block instead, and reference the env var name (`"$DIR"`,
not `"${{ inputs.dir }}"`) inside the script. Semgrep's own
`yaml.github-actions.security.run-shell-injection` rule caught this exact mistake in a lydite
action once already: expression interpolation into a shell script is a script-injection vector if
the value could ever contain shell metacharacters, however trusted it looks. `if:` conditions and
`with:` blocks on a `uses:` step are fine to interpolate directly — only `run:` bodies splice text
into something a shell then executes.

**The marker is read back from the comment's own first line, never restated in a workflow.** It is
`ui.Marker`, and a second copy is one that can disagree — a run would then post a fresh comment
every time instead of editing the standing one, and the relay and the fallback would orphan each
other's comments rather than handing over.

**The pull-request comment carries no logo.** It identified whose verdict it was while the
comment arrived under a consumer's own `github-actions[bot]`; the App is that identity now, so a
mark above the verdict restates the byline and spends a row of a reader's screen doing it. Any
image a comment did carry would have to be an **absolute raw URL** against this repository's
default branch — the comment is posted into the *consuming* repo's pull request, where a relative
`assets/...` resolves against that repo and 404s, and a release tag stops resolving for consumers
pinned to an older version. That constraint is why `assets/lydite-mark-64.png` was treated as a
public API, and it is the constraint to honour if a comment ever embeds an image again.

