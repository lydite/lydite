# Surface: `lydite publish`, `lydite threads`, and the identity that posts them

> **The reference for `lydite publish`, the standing comment, `lydite threads` and the review
> threads, `internal/threads`, and `source/cloud-services/pr-relay`.**

Every gate lydite has reports to a job log; the **Surface** is where the verdict reaches the
person whose change it is about. There are two of them, and one rule separates them: **the
comment carries what is true of the change; the review carries what is true of a line.** The
comment is a standing pull-request comment carrying the referral, the scan, the suites and the
mutants as one collapsible section each; the review is one thread per finding that reaches the
change, on the line or the file it is about. See
[ADR 0023](../../docs/adr/0023-one-standing-comment-rendered-by-the-cli.md) for the comment,
[ADR 0031](../../docs/adr/0031-a-located-finding-is-a-review-thread.md) for the threads, and
[ADR 0022](../../docs/adr/0022-a-vendor-operated-app-and-an-oidc-relay.md) for the identity.

**`lydite publish` renders and posts nothing.** `--reports <dir>` (repeatable), `--out
<file|->`. No network, no token, no knowledge of a hosting platform. A developer runs it locally
and reads exactly what a reviewer will see; and the posting step is content-agnostic, so
refining the comment never needs a release of whatever posts it. `lydite/actions` therefore
parses no output at all: it reads the documents a run wrote, which is the only interface
lydite publishes for a consumer.

**Every command writes its report document into `.lydite-reports/`, on every run.**
`scan.json`, `test.json`, `review.json` — unconditionally, not only under `--json`, because a
run whose results reach a later step only when somebody remembered a shell redirection
publishes nothing when they forget. `ui.Document` and `ui.ReadDocument` are that shape read
back, and they **accept unknown keys**, which is the opposite of lydite's stance everywhere
else: the document is lydite's own output rather than something an author wrote, and the reader
is routinely an older binary than the writer. A missing command or verdict is still refused —
that is not a newer shape, it is not a report.

**`--reports` takes N directories, and that shape is load-bearing.** A local run produces every
command's document at one scan root; a CI run produces one per job. It is also what makes
[ADR 0017](../../docs/adr/0017-shards-the-scheduler-and-the-planner.md)'s shard matrix additive: more
`test` jobs is more directories and nothing about the comment changes.

**A missing input is a section, never an omission.** A named directory that is absent,
unreadable or holds no document renders `unmeasured`, naming what was missing. A section that
quietly disappears is indistinguishable from a concern that passed — the wardnet#957 failure.

**A section is `unmeasured` only when nothing in it was decided.** Promoting on any single
unmeasured row would mark ordinary runs as ungated: `--affected` reports every unselected
component as unmeasured, and `review` reports a dirty working tree the same way. A partly
measured section says so in the counts on its own summary line, which is visible without
opening it.

**`scan` writes a log per check**, under `.lydite-reports/scan/<slug>.log`, and sets `Row.Log`.
`test` has written one per component since it had rows; a scan check that failed left its
findings only in a job log, which is unreachable from a comment. The output is already captured
by `executil.Run` — a scanner's findings *are* the result, so they still stream live — so this
is an extra sink rather than a redirection. A failing row shows its `Detail` if it has one
(Biome's findings reach a reader no other way) and the tail of its log otherwise.

**Quoted output is capped per section.** A platform refuses a comment over a size limit, and a
refused comment is no surface at all — which is what this exists to prevent.

**`review --publish` writes the commit status and no comment.** The status is the record a
clearance acts on and has to land early, so `lydite-pr.yml`'s `referral` job has no `needs` and
publishes within seconds of a push (ADR 0015). Its verdict reaches the comment by the route
every other command's results take — the document it wrote — because rendering it twice would be
two derivations of one answer. `ui.Marker` is `<!-- lydite:results -->`: one comment per change,
upserted by the marker rather than by author, which is what lets the relay and the fallback hand
over instead of leaving two standing verdicts.

## The relay, and the fallback

`source/cloud-services/pr-relay` writes on a consumer's behalf so that **no CI job holds a
credential**. The job presents the GitHub Actions OIDC token; the relay verifies signature (JWKS),
`iss`, a declared `aud` and `exp`, takes the repository from the `repository` claim and never
from the body, reads the pull-request number out of `ref` (`refs/pull/<n>/merge` and
`refs/pull/<n>/head` both), mints an installation token narrowed to that one repository and to
`pull_requests: write`, writes, and discards it. It stores nothing.

Two endpoints. `POST /comment` upserts the standing comment by its marker; `POST /review`
applies the operations document `lydite threads` computed. Before applying anything, `/review`
lists the pull request's own review comments and **refuses the whole request if any `reply` or
`delete` names an id outside that set** — a comment id is a number the caller supplies while
`ref` is the only thing a run cannot choose, and refusing the whole document rather than the one
operation stops the answer being usable to probe which ids exist. It answers with an outcome per
operation, so a posted review and a refused delete are distinguishable.

That takes the comment write out of the job that runs the repository's code: with no writable
token there, the worst a pull request's own suite can provoke through the relay is a wrong comment
on its own pull request. It **narrows [#49](https://github.com/lydite/lydite/issues/49) rather than
closing it.** Recording a coverage baseline is a push to the `lydite` branch and needs
`contents: write`; the relay mints `pull_requests: write` and has no endpoint that commits
anything. `lydite test record` takes the coverage write out of the gating job the same way, and
neither closes the rest: a recording job holds a pushing token whatever command it runs, so it
belongs on a tree that has already merged — which is where `lydite-baseline.yml` runs it.

- **RS256 is fixed, not read from the token.** Honouring a token's own `alg` is how a verifier
  accepts `alg: none`.
- **A rejection carries no detail.** One that says which check failed is an oracle.
- **`ref` is the only claim a run cannot choose**, so a push build can comment on nothing.
- **The private key is PKCS#8.** GitHub issues an App key as PKCS#1 and WebCrypto imports only
  PKCS#8; the conversion happens once out of band
  (`openssl pkcs8 -topk8 -nocrypt`) and a PKCS#1 key is refused with that command rather than
  failing later as an opaque import error.
- **Each Worker holds only its own App's credential**, as a per-Worker secret rather than an
  account-level store. `pr-relay` cannot read the dashboard's, which is the whole reason the
  read half is a separate App.
- **The `github-token` fallback is required, not a stopgap.** No relay configured, or a relay
  answering that the App is not installed, posts as `github-actions[bot]`. A consumer who
  installed nothing still gets the surface. That is why "not installed" is an *answer* and not
  an error.

## The threads

**`lydite threads` owns the fetch, the delta and the apply.** It reads `--reports` (repeatable),
lists the threads already standing on the pull request, computes what reconciles them with this
run's located findings, and **always** writes that to `--ops <file>`. It applies it only under
`--apply`. The relay's `POST /review` applies the identical document, so there is one
implementation of what a fingerprint matches and what may be deleted, and two transports that
decide nothing — the argument that keeps the comment's renderer in the CLI, applied to the
threads.

**The ops document is not a report document.** It goes to `--ops <file>` and never into
`.lydite-reports/`: nothing about it is promised to a consumer, and `lydite test plan` is the
precedent for a command that reaches no verdict writing none. It is versioned, and a reader that
does not recognise the version refuses the whole of it rather than applying the half it
understands.

**Reading the prior state uses the job's own token on both paths.** The delta has to be computed
somewhere that can read the pull request, and the publish job already holds
`pull-requests: write` for the comment's fallback — so this costs no new credential. What the
relay takes over is the *write*, which is the half ADR 0022 is about.

**`lydite publish` stays pure.** Nothing about a hosting platform enters it, which is why the
threads are a separate command rather than a flag on that one.

**The marker is `<!-- lydite:finding:<fingerprint> -->`**, and the token after the prefix *is*
the fingerprint, version prefix included. The parser accepts any token and returns it verbatim,
so a thread written under a formula this binary never emitted is simply one matching no current
finding — which is what makes a formula bump self-heal in one round of delete-and-repost. Every
comment lydite writes into a thread carries the marker, including its replies, which is what
lets the sole-participant rule read the marker rather than an author.

**`lydite is the only participant` governs both branches of a thread's life.** A claim that is
gone deletes its thread where lydite is alone in it, and is replied to and left standing where
anyone else spoke. A thread the change has made outdated is deleted and reopened at the current
line under the same rule — because the platform collapses an outdated thread behind its "show
outdated" toggle, so a thread that blocks the merge becomes one the author cannot see, which is
worse than the notification a repost costs. Outdated is a null `position` read together with
`subject_type`: a comment on a whole file has no position by construction rather than by the
change having moved, and the null alone would churn every file-level thread on every push. A delete the platform refuses takes the
someone-else-spoke path, which is why a delete operation carries the body to post instead.

**A review the platform refuses fails the publish job**, naming how many located findings
reached no surface. The findings still reach the terminal, the job log and the uploaded
`.lydite-reports/` artifact; a run that quietly posted nothing would read as a change with
nothing wrong with it.

**A review comment must anchor inside the diff's own `@@` hunks.** The web UI lets a human
comment anywhere in a changed file and the API answers `422 line must be part of the diff`.
Hunks carry three lines of context and `coverage.ChangedLines` is `--unified=0`, so lydite's set
is a strict subset — *"lydite says anchorable" implies "the platform accepts it"*, never the
reverse.

**One review per run for the line-anchored claims**, `POST /pulls/{n}/reviews` with `comments[]`,
event **COMMENT** — never REQUEST_CHANGES or APPROVE, because review approval is a different
mechanism with different rules about who may give one. The review carries no body of its own: a
summary above the threads would be a second standing verdict beside the comment that already
holds one, and a review with `comments[]`, `event: COMMENT` and no `body` is accepted (verified
against the platform).

**A file-anchored claim cannot travel in that review.** A review's comments are
`DraftPullRequestReviewComment`, which has no `subjectType` field and requires a position, and
the API answers 422 for both — verified. `subject_type: file` is accepted on
`POST /pulls/{n}/comments` instead, with an explicit `commit_id`. So a run is one review plus a
call per file-level thread, plus the replies and deletions.

**A delete the platform will not do answers 403 or 404**, and both take the answering path: 403
where the identity can see the comment it did not author, 404 where it cannot (verified against
a read-scoped identity). A reply that is itself unfound settles which of the two a 404 was — the
comment is simply gone, which is the state the delete was asking for.

**lydite can never resolve a thread.** `resolveReviewThread` needs `Contents: write`, which is
exactly what ADR 0022's two-App split forbids. A thread is therefore a *soft gate*: it blocks
the merge, and any writer clears it without touching the code.

**`lydite threads` dedups by fingerprint on read**, first occurrence wins, the drop named on
stderr. It consumes documents a local run wrote with no fold at all, so it cannot rest on the
fold having deduplicated — the fold's own duplication is
[#123](https://github.com/lydite/lydite/issues/123).
