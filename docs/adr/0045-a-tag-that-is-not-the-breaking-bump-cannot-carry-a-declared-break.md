# A tag that is not the breaking bump cannot carry a declared break

At pull-request time a declared break is *referred*, because the version is not chosen yet and
there is nothing to check the claim against ([ADR 0040](0040-an-undeclared-go-api-break-fails-and-a-declared-one-is-referred.md)).
At tag time there is: the number itself. `lydite release check` reads every commit in the
range the tag closes, and a declared break in a range whose bump does not admit one **fails
the release before goreleaser runs**.

It is a hard failure and not a warning because nothing pins lydite's version for a consumer —
`lydite/actions`' `version` input defaults to `latest`, so every release reaches every
consumer on their next CI run ([ADR 0010](0010-repo-topology.md)). The bump number is the
entire warning anybody gets, and a warning in a job log that published the tag anyway arrives
after the only moment it could have changed anything.

## The bump a declared break requires

[ADR 0010's *the bump a break costs* section](0010-repo-topology.md#the-bump-a-break-costs)
states the rule this check enforces: a breaking change lands where the **leftmost non-zero
component of the version increases**. While the release line is `0.x` that is the minor
component, so `v0.2.0 → v0.3.0` admits a declared break and `v0.2.0 → v0.2.1` does not; from
`1.0.0` onward it is the major component.

The check restates nothing of that reasoning and holds no second opinion. It compares the two
tags under the single rule ADR 0010 names, which is what keeps the number a human picks and the
number a gate accepts from being two policies that can disagree.

## The previous tag is the semver-highest below this one

"Previous" is decided by version order, never by the commit graph. The candidates are every
tag matching `v*` (`git tag -l 'v*'`), ordered by `golang.org/x/mod/semver`'s `Compare`, and
the previous tag is the highest one strictly below the tag being released.

Parentage is the tempting alternative and it answers a different question. A tag cut from an
unusual branch state — a hotfix off an older commit, a tag moved after the fact — has a git
parent that says where the work sat, not which release preceded it. Consumers upgrade along
the version line, so the range a consumer is about to receive is the one bounded by the
version-highest predecessor, whatever the graph looks like.

**A prerelease is checked, and is never "previous".** A prerelease tag such as `v0.3.0-rc.1`
is a release that reaches anyone who asks for it, so it is checked like any other — against
the last **stable** tag, since the stable tag is what its own predecessor line is measured
from. Prereleases are skipped while searching for the previous tag, in both directions: a
later stable `v0.3.0` compares against `v0.2.0` and not against its own release candidates,
whose ranges it entirely contains.

**The first tag has no previous, and that is a pass.** The range is empty by definition, and
the check says so — it reports an empty range rather than a clean one. The distinction
matters because an empty range and an unresolvable one are not the same answer, and only one
of them is legitimate.

## It reads declarations, not surfaces

The check reads `internal/declaration`'s markers out of the commit messages in the range,
through `internal/gitstate.CommitMessages`. It never re-derives whether an API actually broke.

Re-deriving real breaks across a tag range would mean materialising every component's base
tree at each historical point and running the surface diff over each — a cost and a
correctness question that belongs to the single-pull-request comparison ADR 0040 already
defines, against that change's own merge-base. An **undeclared** break is therefore caught at
pull-request time and never here; this command's whole subject is the relationship between a
claim somebody wrote and a number somebody chose.

The command's own `--help` says this, in those terms. A release-time check named after breaks
invites the reading that it detects them, and a reader who assumes so treats a green release
as evidence no API moved — which it is not, and which the help text is the only place most
people will ever look for.

That boundary is also what makes the marker grammar's strictness load-bearing rather than
pedantic: `internal/declaration` matches `BREAKING CHANGE` and `BREAKING-CHANGE` verbatim and
the `!` only in its conventional-commit position, precisely so the release-time check reads
the same history the pull-request check read and reaches the same answer about it.

## A range it cannot resolve is an error, not a pass

A shallow clone with no tags fetched, a tag that does not exist, a tag that is not valid
semver: in each case the check cannot say what the range contains, and it **errors, naming the
fix** — the fetch depth to set, the tag to create, the spelling expected. This is the stance
`cmd/lydite/mutation.go` takes for an unresolvable base, and the repository's own rule that
[a gate that could not run never renders as one that passed](../../agentic/rules/a-gate-that-could-not-run-never-renders-as-one-that-passed.md),
applied at the one moment where the alternative is a tag that is already public.

The empty range of a first tag is the one case that passes while reading nothing, and it
passes because *nothing to check* is a fact about the repository rather than a failure to
look.

## It runs in the release workflow, not as a gt stage

`.github/workflows/release.yml` triggers on a `v*.*.*` tag push and runs `build-test` then
`goreleaser`. The check is a step ahead of goreleaser, in a job whose checkout has
`fetch-depth: 0` **and** the tags — the `goreleaser` job already fetches full history, while
`build-test`'s checkout sets no depth, so a step placed there needs the depth added before it
can see a range at all. Whichever job runs it, it must run to completion before anything is
published: the value of the check is entirely in the tag not becoming a release.

**It is deliberately not a `gt` stage.** gt's pipeline is pull-request shaped and every stage
in it answers about a branch against its base. A release is tag-driven and crosses that
pipeline entirely — the tag is pushed to a commit already merged and already through every
stage — so the check lives in the CLI, is unit-tested there, and is invoked directly by the
release workflow. That also keeps the logic testable at all: `.goreleaser.yml` and
`release.yml` are exercised by nothing but a real tag push, so the Go tests are the only proof
available.

## Release notes are out of scope

The check does not look for `docs/release-notes/<tag>.md`. A missing per-version notes file is
deliberately not an error — most patch releases are fully described by their commit subjects,
and the release body falls back to the header plus goreleaser's generated changelog
([`release-notes.md`](../../agentic/references/release-notes.md)). Making the presence of that
file a release-time condition here would reverse a decision taken elsewhere, in a command
whose subject is version numbers.

A declared break is exactly the release most likely to want notes, and that remains a judgement
the person cutting the release makes, not a second gate wearing this one's name.

## Consequences

- `lydite release check` is a new subcommand and the first thing `lydite` does that discovers
  tags at all. Nothing in `source/`, `scripts/` or `.github/` reads `git tag` otherwise.
- `internal/gitstate` gains tag listing and previous-tag selection; `internal/declaration` is
  read unchanged, by a second caller with no title to read.
- The release workflow can fail after the tag is pushed. The tag exists at that point and the
  release does not, so the recovery is a new tag at the bump the range requires — which is the
  intended outcome, and cheaper than a published release nobody can unpublish from a
  consumer's next CI run.
- [#162](https://github.com/lydite/lydite/issues/162) is the fourth of the pieces
  [#24](https://github.com/lydite/lydite/issues/24) names; the Rust and TypeScript surface
  diffs remain their own slices, and neither changes what this check reads.
