# One standing comment, rendered by the CLI and posted by something else

Every gate lydite built — the scanners, the suites, the coverage baseline, the
patch gate, the orphan gate, affected selection — reported to a job log and
stopped there. `review --publish` was the only thing that had ever written a
pull-request comment, and by design it carried the referral verdict alone. So
the person whose change it all describes saw none of it.

**`lydite publish` renders one standing comment from the report documents one or
more runs wrote, and posts nothing. It is pure: no network, no token, and no
knowledge of a hosting platform. Posting the markdown it emits is a separate
step with a separate identity.**

## The CLI renders, because the alternative already failed

The renderer could live in the CLI, in the composite action, or in the workflow.
It is in the CLI, and the evidence is in this repository's own history:
`lydite/actions` greps for a `^\[(PASS|FAIL)\] <name>$` shape that
`internal/ui` does not produce, so it matches nothing and has matched nothing
since the output grammar was written.

That is what a text-scraping consumer costs. Every refinement to the human
surface becomes a synchronised release in another repository, and when the two
drift the consumer silently reports nothing rather than failing. `--json` exists
precisely so nothing has to scrape prose, and a renderer in the action would be
a second thing that has to understand a report — one release behind, forever.

The CLI already holds the vocabulary: seven statuses, the verdict precedence,
the distinction between a gate that passed and one that never ran. Restating any
of it elsewhere is restating the part most expensive to get wrong.

## Rendering and posting are separated because they are not the same job

`publish` writes markdown and stops. Two things follow, and both are the reason.

A developer can run it locally and read exactly what a reviewer will see, with
no token and no pull request. And the posting step is content-agnostic: it
takes a file and a marker and knows nothing about coverage, components or
verdicts — so refining the comment never needs a release of whatever posts it.

This is the same shape ADR 0009 chose for the dashboard, where "`lydite report`
generates a file and stops", and for the same reason: deploy credentials never
enter a process that runs third-party scanners over untrusted code, and the CLI
behaves identically locally and in CI.

## The document had to become readable

`--json` went to stdout and nowhere else, which is enough for a consumer reading
one run and useless for assembling a comment from several. So `scan`, `test` and
`review` each write their document into the report directory on every run —
`scan.json`, `test.json`, `review.json` — whatever stdout is getting.

Unconditionally, and not only under `--json`. A run whose results reach a later
step only when somebody remembered a shell redirection publishes nothing when
they forget, and reintroduces as a workflow's responsibility exactly the
coupling the document exists to remove.

`ui.Document` and `ui.ReadDocument` are that shape read back. They accept keys
they do not know, which is the opposite of lydite's stance everywhere else: a
document is lydite's own output rather than something an author wrote, so there
is nobody to tell that a key is stale — and the reader is routinely an older
binary than the writer, since a workflow pins the version that renders the
comment separately from the one that ran the suite. A missing command or verdict
is still refused, because that is not a newer shape; it is not a report.

## `publish` takes N report directories, and that is the load-bearing shape

A local run produces every command's document at one scan root. A CI run
produces one directory per job. `--reports` is repeatable and each directory is
read the same way, so the two are the same invocation with a different number of
arguments.

It is also what makes the sharded matrix of
[ADR 0017](0017-shards-the-scheduler-and-the-planner.md) additive rather than a
redesign: more `test` jobs is more directories, and nothing about the comment
changes.

## A missing input is a section, never an omission

A named directory that is absent, unreadable, or holds no document renders as an
`unmeasured` section naming what was missing.

This is the rule the whole surface turns on. A section that quietly disappears
is indistinguishable from a concern that passed — which is the failure
wardnet/wardnet#957 shipped, where patch coverage never ran and the
pull-request comment read as though it had. The comment must be able to say "I
do not know" in a way a reader cannot mistake for "fine".

The same rule bounds the other direction. A section is `unmeasured` only when
nothing in it was decided — no row passed, failed or referred. Promoting a
section on any single unmeasured row would mark ordinary runs as ungated:
`--affected` reports every component it did not select as unmeasured, and
`review` reports a dirty working tree the same way. A partly measured section
says so in the counts on its own summary line, which is visible without opening
it, and a concern whose report never arrived has no decided row at all and so
lands as unmeasured anyway.

## The referral folds in, but its commit status stays early

The comment carries the referral as one section. The `lydite/referral` commit
status is still published by `review --publish`, in a job with no `needs`, so it
lands within seconds of a push.

A status is not a comment. A person can start clearing a referral while the test
matrix is still running, and clearance stays decoupled from whether CI is green
— the property [ADR 0015](0015-clearance-binds-to-a-commit.md) rests on. Waiting
for the comment would make every clearance wait for the slowest suite.

`review` therefore has no comment-rendering code of its own any more. Its
verdict reaches the comment by the route every other command's results take: the
document it wrote. Rendering it twice would be two derivations of one answer,
free to disagree.

That folding is why all of lydite's pull-request jobs are one workflow run.
Artifacts belong to a run, so a publish triggered by another workflow's
completion could not see the referral's report — it would have to recompute the
verdict, or look a run up by head SHA and race it.

## One marker, and no new status contexts

One comment per change, upserted by a marker in its body. A pull request
accumulating one comment per command is the surface nobody reads, and matching
on the author rather than the marker would break the moment the identity
changes — which is exactly what [ADR 0022](0022-a-vendor-operated-app-and-an-oidc-relay.md)
makes happen, twice, in both directions.

No new commit status contexts. `ci-gate` already carries pass and fail, and gt
hardcodes branch protection to it alone
([#34](https://github.com/lydite/lydite/issues/34)), so a new context would
enforce nothing and add a row to every pull request's check list for it.

## What the comment shows, and what it deliberately does not

A verdict badge, a headline naming the concern to act on, and one collapsible
section per concern — the shape Copilot's review comment uses, for the reason it
uses it: four concerns at one line each, and the one that failed already open.

Each section carries a two-column table of what was checked and what it
answered. The reference prototype in `docs/design/reference/` specifies three
columns — check, head, base — and that design was drawn for a referral, whose
facts are what the change contains and what was read out of the merge-base. A
report row carries a label and a value, so a third column could only be filled
by inventing a fact or left empty on every row.

A failing row's output is quoted, fenced, under its section. That is what
`Row.Log` was added for, and its doc comment said so before anything read it: a
consumer cannot parse a path back out of prose. `scan` had to learn to write one
— `test` has written a log per component since it had rows at all, while a scan
check that failed left its findings only in a job log.

The quoted output is capped per section, and the count of what was left out is
stated. A hosting platform refuses a comment over a size limit, and a refused
comment is no surface at all — which is the outcome this whole decision exists
to prevent.

The footer carries the version and the base commit. The design's footer also
claims parity with the reader's local run; nothing can establish that yet
([#27](https://github.com/lydite/lydite/issues/27)), so it is absent rather than
asserted.
