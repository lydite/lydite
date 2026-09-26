# Stages read and write the platform through an `SCMRepository` interface, ahead of a second vendor

Every stage that talks to the hosting platform — resolving a comment, a head, a permission, a
standing status, or posting one back — does it through `forge.SCMRepository`, an interface of
eight methods (`IssueComment`, `HeadSHA`, `PullRequestTitle`, `ChangedPaths`, `CanWrite`,
`ReferralStatus`, `PostStatus`, `CreateComment`), each typed in lydite's own terms rather than the
platform's — a pull request is an `int`, a status is `forge.Status`, a standing verdict is
`clearance.Status`. `GitHubRepository` is its only implementation today, built by
`forge.NewGitHubRepository(t trust.TrustedContext)` from a `TrustedContext` alone: both the
`Client` it wraps and the `Repo` it is bound to come from `t` and nothing else, so the repository
every call is made against is the one the run was started in. lydite talks to exactly one hosting
platform. This records why the interface exists anyway, now, rather than a second vendor forcing
it later.

## Rejected: stages taking `*forge.Client` and `forge.Repo` directly

`forge.Client` is not a neutral transport — `internal/forge/calls.go`'s methods build GitHub REST
paths directly (`/repos/%s/%s/pulls/%d`), because `internal/forge`'s own package doc says as much:
it is "deliberately small and hand-rolled over `net/http`", covering exactly the platform calls
lydite makes. A stage taking `*forge.Client` and a `forge.Repo` as two separate parameters
couples itself to that shape, and makes the repository a free parameter any caller could point
anywhere — nothing stops a future call site from constructing a `Repo` from a flag or a payload
instead of from trust, which is exactly the class of mistake
[the rule on resolving a write's target live](../../agentic/rules/resolve-a-writes-target-live-never-trust-the-claim-or-the-body.md)
exists to catch. Binding `Client` and `Repo` together, behind one interface, constructed once from
`TrustedContext` and threaded through the flow as a single value, removes that parameter rather
than trusting every stage to supply it correctly.

## Rejected: waiting for a second vendor before abstracting

The usual reason to hold off on an interface — YAGNI, since lydite integrates with one platform —
does not apply here, because a second vendor is not what this interface is for today. It is the
test seam every stage needs. A stage's contract is that it is callable from a plain struct literal
in a test with no fake pipeline to stand up; a stage typed against `forge.SCMRepository` is
satisfied in a test by a hand-written `fakeRepository` (`internal/stages/clearance/fake_test.go`,
`internal/stages/scm/scm_test.go`) implementing the same eight methods, each failing the test if
called unexpectedly — no network double, no real GitHub API, no `httptest` server standing in for
one. A stage typed against `*forge.Client` would need one of those instead, for every test of
every stage that talks to the platform. Waiting for a second vendor to justify the interface would
mean paying that cost until one arrives, for a benefit the interface already gives on day one.

## The method set is exactly what the flows use

`SCMRepository` names eight methods, and grepping the stage packages for each one finds a caller
of every single one — `IssueComment` from `LoadComment`, `HeadSHA` from `ResolveHead`,
`PullRequestTitle` from `Fingerprint`'s surface comparison, `ChangedPaths` from `Decide`'s
uncovered-paths derivation, `CanWrite` from `CheckPermission`, `ReferralStatus` from `ReadStatus`,
`PostStatus` from `RecordStatuses`, `CreateComment` from `PostComment`. Nothing on the interface is
speculative, and nothing a flow needs is missing from it; review threads and the standing comment
`lydite publish` writes are not here because no stage reaches for them. Widening the set is a
deliberate change made when a new stage needs a new call, not a surface grown ahead of a caller.

## Consequences

- A second hosting platform, when one arrives, implements `SCMRepository` once and every existing
  stage runs against it unchanged — the interface is already the seam that would have to exist for
  that to be true, so this decision costs nothing extra to have made early.
- `NewGitHubRepository` is the only place a `Repo` and a `Client` are built from a credential; every
  stage downstream reads the interface and never reconstructs either.
- The interface's method set is exactly what the flows use, checked by grepping the stage packages
  for each one rather than by a comment claiming so; adding a call the flows don't yet need is a
  choice to widen the interface ahead of a caller, not a free addition.

See [`agentic/references/architecture.md`](../../agentic/references/architecture.md) for where
`forge.SCMRepository` sits among the four layers, and
[ADR 0061](0061-trust-and-the-repository-come-first-and-a-webhook-payload-only-points.md) for why
it is built from a `TrustedContext` and nothing the payload carries.
