# Surface: `lydite publish`, and the identity that posts it

> **The reference for `lydite publish`, the standing comment, and `source/cloud-services/pr-relay`.**

Every gate lydite has reports to a job log; the **Surface** is where the verdict reaches the
person whose change it is about. There is exactly one: a standing pull-request comment carrying
the referral, the scan, the suites and the mutants as one collapsible section
each. See
[ADR 0023](../../docs/adr/0023-one-standing-comment-rendered-by-the-cli.md) for the surface and
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

`source/cloud-services/pr-relay` posts on a consumer's behalf so that **no CI job holds a
credential**. The job presents the GitHub Actions OIDC token; the relay verifies signature (JWKS),
`iss`, a declared `aud` and `exp`, takes the repository from the `repository` claim and never
from the body, reads the pull-request number out of `ref`, mints an installation token narrowed
to that one repository and to `pull_requests: write`, posts, and discards it. It stores nothing.

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

