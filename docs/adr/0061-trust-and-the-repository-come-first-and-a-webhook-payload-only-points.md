# Trust and the repository come first; a webhook payload only points

`clearanceflow.New` declares `init-trust` and `init-scm` before every other stage.
`truststages.InitTrust` is the one stage in the flow that reads the process environment —
`trust.FromEnvironment()` reads `GITHUB_REPOSITORY` and whichever of `GITHUB_TOKEN` or `GH_TOKEN`
is set, once, into a sealed `trust.TrustedContext` whose fields no package outside `internal/trust`
can read or forge. `scmstages.InitSCM` builds the run's `forge.SCMRepository` from that
`TrustedContext` alone. Only after both exist does the flow touch the `issue_comment` payload at
all, and even then it reads one thing off it: `forge.CommentRef{ID, Repository}` — a comment id,
and the repository the payload claims that comment is on.
`scmstages.LoadComment` checks `Ref.Repository` against `Trust.Repository()` (case-insensitively,
since GitHub owner/name segments are) and refuses a mismatch outright, before fetching anything;
only once they agree does it call `Repository.IssueComment(ctx, Ref.ID)` to resolve the comment
live. Its body, its author, its timestamp and the pull request number all come back from that
call — `clearancestages.ParseCommand` reads every one of `Login`, `CommentAt` and
`OnPullRequest`/`Number` off `forge.IssueComment`, never off the payload. This records why the
payload is trusted for only that one pointer, and never for the rest.

## Rejected: reading everything from the payload

An `issue_comment` payload already carries a body, an author, a timestamp and a pull request
number — reading them directly would save the live fetch entirely. It is rejected because the
payload is a copy taken at delivery time, and a clearance command is exactly the kind of comment
likely to be edited or deleted before the job that answers it runs: a maintainer who mistypes
`/lydite clear` and corrects it a second later has a payload for the first version and a live
comment holding the second. Answering the payload's body would parse a command version of events
already false by the time anyone reads the verdict. Fetching live means `ParseCommand` always
reads the comment as it currently stands — an edited comment is answered as it reads now, and a
comment deleted before the job reaches it fails `IssueComment` with a 404, which is a resolution
failure and not a parsed command at all, rather than lydite silently acting on text nobody can
still see on the pull request. This is the same reasoning
[the rule on resolving a write's target live](../../agentic/rules/resolve-a-writes-target-live-never-trust-the-claim-or-the-body.md)
states for `pr-relay`'s own endpoints: neither a verified claim nor a caller's own assertion is
trusted by itself for identifying what gets acted on, when the platform itself can be asked and
answers authoritatively. It is a different failure from the one
[ADR 0037](0037-a-deterministic-relay-misconfiguration-fails-the-step-not-the-fallback.md)
classifies — that record sorts a relay's own response to a request already trusted to be about
the right thing; this one is about not trusting the request's own account of what it names.

## Rejected: reading the repository from the payload

The narrower version — trust the payload for its comment id, its body, and *also* for which
repository to act against — is rejected on its own, independent of the first alternative. A run's
repository is what everything else it does is scoped to: which token is even valid, which pull
request numbers mean anything, which status context gets written. `GITHUB_REPOSITORY` is the
platform's own statement of where this job is running, set for every job by the platform itself
and never by anything a comment's author controls; a payload's `repository.full_name` is text the
event delivery mechanism copied, and trusting it for the one fact that decides where a write lands
would let a payload point a properly-credentialed run at a repository the credential does not
actually belong to whenever the two disagree — whether by a genuine platform anomaly or by however
a payload reaches the job. `forge.CommentRef.Repository` exists to be *compared against* the
trusted repository, in `LoadComment`, and refused on mismatch; it is never a source the flow reads
the repository from.

## Trust and the repository are resolved first because reading the comment needs both

The ordering is not incidental. Resolving the comment live requires a `forge.SCMRepository` to
call `IssueComment` on, and refusing a payload naming the wrong repository requires a trusted
repository to compare it against — both of which only exist once `init-trust` and `init-scm` have
already run. Declaring them first, and refusing a mismatch before any request is made, is cheaper
and safer than fetching first against whichever repository the payload named and refusing to act
on what comes back: the former makes an unauthorized read impossible by construction, and the
latter would have to have already made it.

## Consequences

- Every clearance run needs `GITHUB_REPOSITORY` set and a live fetch of the comment happens even
  when the comment turns out to address nothing — `init-trust` and `init-scm` run unconditionally,
  and `LoadComment` runs right after them, before `ParseCommand` has had a chance to say whether the
  comment was ever addressed to lydite at all. A run given no credential fails at `init-scm` with
  `scmstages.ErrNoCredential` before it reads anything, rather than reading a comment it could never
  have acted on regardless.
- An edited comment is answered as it currently reads, not as it read when the webhook fired. A
  deleted comment is a 404 from `IssueComment`, which fails the run — there is no command to parse
  from a comment that no longer exists, and the payload's own copy of a body that used to be there
  is not one.
- `CommentRef.Repository` is read for exactly one purpose: the equality check in `LoadComment`.
  Nothing else in the flow reads it, and nothing should start to.

See [`agentic/references/architecture.md`](../../agentic/references/architecture.md)'s "The payload
only points; trust and SCM come first" section for how this reads at the level of the flow's own
declaration order, and [ADR 0060](0060-stages-read-and-write-the-platform-through-an-scmrepository-interface-ahead-of-a-second-vendor.md)
for how `init-scm` builds the repository this record's live reads go through.
