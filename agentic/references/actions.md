# `lydite/actions`

> **The reference for `.github/actions/` and the `lydite/actions` repository.**

The actions a consumer runs live in [`lydite/actions`](https://github.com/lydite/actions), not
here (ADR 0010). It ships ten actions — `setup`, `review`, `scan`, `test`, `plan`, `merge`,
`mutation`, `mutation-merge`, `record`, `publish` — plus two reusable workflows. `lydite.yml`
runs the referral and the scan with no `needs`, `plan` groups the declared components into
shards, a test matrix and a mutation matrix each consume that grouping in parallel, `merge` and
`mutation-merge` each fold their matrix's shards, and `publish` (`if: always()`) renders one
comment from every job's report and computes the review-thread delta through `lydite threads`,
opening, answering and closing threads on the reports call for. `lydite-baseline.yml` is the
separate workflow that records a coverage baseline after a change has merged: its own `plan` and
`scan` jobs, a `measure` matrix, and one `record` job that folds every shard and pushes with
`--branch`. Consumers pin the floating major, `@v1`, which a release moves.

**There is no input to decline mutation.** A comment with no mutation section reads as a
repository whose mutants all died rather than one that chose not to run them — the gap
[lydite/actions#9](https://github.com/lydite/actions/issues/9) tracks, and the CLI half of it is
[`surface.md`](surface.md)'s `StatusDeclined`.

**What is here is the dogfood, and the two are deliberately the same shape.**
`.github/workflows/lydite-pr.yml` runs the same concerns through the local composites in
`.github/actions/`. The one difference is that the binary composite here uses the binary the
pull request built, because a dogfood against the last release tests the last release. When the
shape here changes, that repository is where the change has to land as well; nothing enforces
it.

**A fold is a shared script, not a copy per action.** `merge` and `mutation-merge` both reach
`.github/actions/fold/fold.sh` through `github.action_path`, resolved relative to the calling
action rather than duplicated into each. Two folds computing the same completeness question —
every declared component takes exactly one row, and a missing or doubled row fails the job —
share one implementation of it.

**A consumer's comment is rendered by `lydite publish` and nothing else, and its review threads
are computed by `lydite threads` and nothing else.** The posting step takes a file and a marker
and knows nothing about coverage, components or verdicts; `lydite-threads` takes report
directories and an operations file and knows nothing about fingerprints. That is what stops a
refinement to either surface becoming a two-repository release. It is also why a sticky-comment
action earns nothing: the marker upsert is twenty lines of `gh api`, and the relay does the same
thing on the authenticated path.

**It uploads to no third party, and Codecov is not coming back.** A run's coverage and JUnit
results go to its own artifacts and nowhere else. lydite replaced Codecov as the blocking gate,
and a dashboard is not a reason to keep the relationship: it would put a token in CI for
something non-blocking, and the history it offers is what
[#26](https://github.com/lydite/lydite/issues/26)'s ledger is for — owned here, where the
measurements are made, rather than reconstructed by a third party from uploads.

**Only `publish` authenticates**, and the split matters: `id-token: write` for the relay, and
`pull-requests: write` for the fallback paths and for reading the threads already standing —
the delta is computed with the job's own token on both paths, which is a read the job could
already make. Every other job holds a read-only token, which
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

**And it is matched at the start of a body, never anywhere in it** — `startswith`, not
`contains`. The platform's quote-reply copies the raw markdown of the comment it answers, HTML
comment included, so a person quoting the verdict would otherwise be the comment the next run
`PATCH`es wholesale. The rule lives in three places because one upsert does: `forge.FindComment`,
the relay's `findComment`, and this action's `jq`. Fixing two of the three is how it was last got
wrong.

**The pull-request comment carries no logo.** It identified whose verdict it was while the
comment arrived under a consumer's own `github-actions[bot]`; the App is that identity now, so a
mark above the verdict restates the byline and spends a row of a reader's screen doing it. Any
image a comment did carry would have to be an **absolute raw URL** against this repository's
default branch — the comment is posted into the *consuming* repo's pull request, where a relative
`assets/...` resolves against that repo and 404s, and a release tag stops resolving for consumers
pinned to an older version. That constraint is why `assets/lydite-mark-64.png` was treated as a
public API, and it is the constraint to honour if a comment ever embeds an image again.

