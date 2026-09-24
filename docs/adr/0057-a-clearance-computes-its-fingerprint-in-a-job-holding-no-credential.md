# A clearance computes its fingerprint in a job that holds no credential

[ADR 0053](0053-a-clearance-carries-forward-when-the-decision-it-was-given-for-is-unchanged.md)
makes a clearance a statement about a decision: `/lydite clear` records
`referral.Fingerprint(Uncovered, Disqualifications)` in the `lydite/clearance` description, and
`lydite clearance queue` republishes `lydite/referral` when it recomputes the same value. The
fingerprint is only worth carrying if it names the decision the person actually cleared — the one
`review` reached and published, over the evidence `review` decides on, which includes the
API-surface comparison every opted-in component asked for and the dependency delta of every
manifest the change touches.

A job holding a writing credential cannot compute that evidence.
`cmd/lydite/review_apisurface.go`'s `computeAPISurfaces` refuses, under `guardCredential`, to run
the comparison of any component whose `untrustedBuild` is non-empty — a Rust crate's `build.rs`
and proc-macros, a TypeScript package's lifecycle scripts — in a process that is about to publish
with a token, because `executil.RunQuietIsolatedEnv` keeps that token out of the comparison's own
child environment but not out of this process's, and a same-user descendant reads
`/proc/<pid>/environ` regardless
([`agentic/rules/give-untrusted-build-scripts-no-inherited-environment.md`](../../agentic/rules/give-untrusted-build-scripts-no-inherited-environment.md)).
The `/lydite clear` job holds `statuses: write` and `pull-requests: write`. So the clearance path
has the problem the referral path already solved, and reaches for the same answer.
[#254](https://github.com/lydite/lydite/issues/254) frames it.

## The clearance path splits into a computing job and a posting job

**A job that recomputes the decision holds no writing grant; a job that holds the writing grant
posts the rendered document and computes nothing.** That is what `review` already does:
`lydite.yml`'s `referral` job runs `review compare`, which is the only half that executes a
component's own code and reaches no verdict, and `lydite-referral-publish.yml` decides and
publishes from that document without ever running the comparison again.

The split lands in `lydite/actions`' `lydite-clearance.yml`, modelled on
`lydite-referral-publish.yml` beside it. The computing job checks out the pull request's head,
recomputes the decision at full parity, and renders the `lydite/clearance` and `lydite/referral`
documents `--status-out` writes; the posting job resolves the head again, refuses a document
naming anything else, and writes it. A job inside a called reusable workflow may narrow its own
`permissions`, and a caller's grant is a ceiling rather than an assignment, so this is a change
inside one repository: `lydite/lydite`'s own `lydite-clearance.yml` is rendered by `gt` from
`.gt-repo.yaml` and neither it nor `gt`'s governance has anything to say about how the called
workflow arranges its jobs.

**Within one workflow file the relay cannot tell the two jobs apart.**
[ADR 0051](0051-lydite-is-a-plain-consumer-of-its-own-relay.md) separates the isolated jobs by
*file* precisely because `pr-relay` admits a gated status by the run's verified
`job_workflow_ref`, and two jobs of one file present one ref. The computing job is therefore
denied `id-token: write` as well as the writing grants: a job with no token request URL in its
environment cannot mint a claim at all, which is the only thing that keeps a compromised
`build.rs` from presenting the clearance ref the posting job presents. `CLEARANCE_WORKFLOW_REFS`
keeps naming one file, and the allowlist is untouched. The computing job's checkout carries
`persist-credentials: false` for the same reason the `referral` job's does — `actions/checkout`
otherwise embeds the token into `.git/config`, where no process-level isolation reaches it.

## What this changes about ADR 0051's exception

ADR 0051 built the split for lydite's own publishing path, and ADR 0053 read it as exactly that —
"ADR 0051's stated exception for lydite's own repo, not the shape ordinary relay consumers should
adopt" — when it rejected a direct-token queue-republish job and extended the relay instead. The
clearance workflow is not lydite's own: it is one of the reusable workflows every consumer calls,
so after this decision the split is a shape consumers get, and describing it as lydite's own
exception no longer describes anything.

What was actually exceptional survives, stated narrowly:

- **The relay remains the general mechanism for writing a status from a job that holds no
  credential of its own.** Nothing here gives a consumer a direct-token job it did not already
  have. The clearance job's `statuses: write` predates this decision and is unchanged; the relay
  route beside it is unchanged.
- **The split is the general mechanism for computing evidence that must not be computed beside a
  credential.** That is a property of what a job computes, not of whose repository it runs in. A
  consumer whose components opt into `api_surface` in Rust or TypeScript has exactly lydite's
  problem, and there is no version of it that the relay answers.

So this is not a reason to reopen ADR 0053's queue-time choice. There the question was who writes
the status, and the relay answers it. Here the writing job already exists, legitimately: it
answers a comment from a default-branch checkout with its own ephemeral token
([ADR 0015](0015-clearance-binds-to-a-commit.md)), and what is added beside it is a credential-free
reader. Routing the clearance through the relay a second time would answer a question nobody
asked; no amount of relay makes an untrusted build script safe to run beside a token.

## Considered and rejected

**A narrower fingerprint — paths and disqualifiers only, with API-surface and dependency evidence
treated as unmeasured.** It needs no workflow change at all and unblocks the deadlock #243
describes immediately, which is its whole appeal. It is rejected because it buys that speed with a
clearance whose fingerprint does not describe the decision a human cleared. A person clears the
verdict `review` published, and that verdict is reached over the surface comparison and the
dependency delta; a fingerprint taken over a subset of its reasons records a judgement against
reasons that were never the whole of what referred the change, and carries it forward to a queue
entry on that basis. ADR 0053's own reasoning against fingerprinting `Uncovered` alone is the same
argument one level up: the fingerprint's value is that it is the decision, and a fingerprint over
part of the evidence is a different claim wearing the same name.

It is also not the cheap option it appears to be under the guard. A guarded comparison is not
omitted — `referUncomputable` records each opted-in component as a disqualification naming why it
could not be compared — so a guarded run fingerprints "the comparison could not be made" as a
reason for referring. That value matches neither what `review` published nor what `clearance
queue` recomputes, so on exactly the repositories the guard fires for, the narrow fingerprint
carries nothing forward while reading as though it does.

**Weakening `guardCredential` for the clearance job** — on the grounds that a comment-answering
job checks out the default branch anyway — was not available: the recomputation is only the
decision being cleared if it runs against the pull request's own head (`checkoutIsHead`), so this
job does check out the change's tree, and running its build code beside `statuses: write` is the
thing the rule forbids.

## Consequences

- `clearedDecision` guards its comparison exactly when the invocation also posts — the direct
  route, no `--status-out`. A clearance posted from one job therefore still computes at full
  parity wherever no opted-in component builds untrusted code, and the split is what gives the
  rest a process to compute in. The guard's own test is whether this invocation publishes, not
  what happens to be in its environment; the workflow is what makes that test worth anything, by
  denying the computing job the grants that make a stolen token useful.
- A repository that declares no `api_surface` component, or whose opted-in components are Go only,
  pays nothing for this: `computeAPISurfaces` returns immediately when nothing opted in, and
  `untrustedBuild` names only Rust and TypeScript, so a Go comparison already runs beside a
  credential.
- **The reply comment belongs to the job holding `pull-requests: write`.** `applyAction` posts the
  acknowledgement from the same invocation that records the clearance, and `reply` treats a
  failure as a warning on stderr rather than an error — so a computing job without the grant
  records a clearance and silently answers nobody, on a surface whose entire purpose is answering
  a person.
- Consumers get two jobs where they had one: a second runner, a second checkout, and an artifact
  handed between them. The cost is paid on every `/lydite clear`, including on the repositories
  that gain nothing from it by the point above.
- **A clearance given for an API-surface or dependency disqualification still does not carry
  forward.** `queueDecision` (`cmd/lydite/mergequeue.go`) calls `referral.Decide` alone with the
  zero `referral.Evidence{}` — never `computeAPISurfaces`, never `measureDependencies` — so a
  decision that refers for a declared break or an added dependency fingerprints differently at
  queue time however faithfully clear time computed it, and the entry goes back to a person. That
  is the safe direction and the one `Evidence{}`'s zero value is chosen for; closing it means
  measuring the same evidence at queue time, which is the referral job's `--reports` to supply and
  not this decision's to arrange.
