# Quality history is stored in the repo and read with the viewer's own GitHub credentials

lydite is growing a dashboard: coverage over time, tests run vs. failed, CRAP, and finding
counts per branch — the reporting Codecov and Semgrep AppSec Platform provide today, brought
in-house so lydite stops depending on two external services for data it already computes
itself. Codecov in particular is already non-blocking and informational; lydite owns every
number it displays.

The data lives on an orphan branch in the consuming repo — the same place per-commit coverage
baselines are cached today. Nothing is uploaded anywhere.

## Ledger, cache, projection, snapshot

The branch holds four kinds of data with three different write policies, and conflating them is
how a trend line becomes untrustworthy.

The per-commit coverage **baseline** is a *cache*: regenerable by re-running the tool, so a
failed write costs time, not information, and stays best-effort as it is today.

**Quality history** is a *ledger*. It cannot be recomputed after the fact — the toolchain pins
that produced a number may have moved, and after a squash-merge the commit it describes no
longer exists. It is append-only NDJSON partitioned by month, never deleted, only compacted.
Writes are still non-fatal, because failing a consumer's build over a push race would erode
trust in a gate that is otherwise about their code. Instead a failed append is recorded as an
explicit **gap** and rendered as a break in the line. The invariant is not "there are no gaps"
but *the ledger never lies about its own completeness*.

Only scalars go in the ledger — coverage, test counts, CRAP, finding counts. Detail (per-file
coverage, the finding list, failing tests) is a *snapshot*: overwritten every run, regenerable
by re-scanning, and interesting only for the latest run. Measured on a 50K-line repo, per-file
coverage including uncovered line numbers is ~100 KB and a 150-finding list is ~24 KB, while
five years of scalar ledger is ~5.5 MB. Keeping detail out of the ledger is what lets history be
retained forever. The cost is that "when did this finding first appear?" is unanswerable; adding
stable per-finding fingerprints later is additive.

A **projection** — a downsampled daily rollup — is derived from the ledger so the dashboard reads
one small file instead of walking every partition.

## The rendered dashboard is never committed

The dashboard is a React app built by Vite and embedded in the lydite binary with `go:embed`,
so consumers run one Go binary and never need Node, and the dashboard cannot drift from the CLI
producing its data.

It renders two ways. Offline, `vite-plugin-singlefile` emits one self-contained HTML with a
bounded window of data inlined — roughly 900 KB regardless of repo age, because the window is
fixed rather than merely expected to stay small. Hosted, the same bundle is a static asset that
fetches JSON.

What is never committed is the rendered HTML. At ~900 KB per run against a 235-byte ledger
append, committing it would add gigabytes of undeltifiable history per year to a branch that
otherwise grows about a megabyte. The branch stores data; HTML is generated on demand.

## Why lydite hosts no data

Three ways to serve the hosted dashboard were considered.

Publishing a replica to the **user's own** Cloudflare account needs no infrastructure from us,
but demands the most setup of any option and delivers less than the alternative below — and
nothing it requires (a publish action, a public/private visibility setting, Access policy docs)
survives into that alternative. It was dropped rather than built as a stepping stone to nothing.

Publishing to a **lydite-operated multi-tenant service** gives the best UX and is the obvious
business, but it makes lydite a central store of unremediated vulnerabilities across other
people's private repositories. That is auth, tenant isolation, token rotation, storage cost,
uptime, and incident response — and for a security tool, one cross-tenant leak is existential.
Deferred as a business decision, not adopted as an architecture.

Instead the dashboard is a **public static bundle containing no data**. A viewer authenticates
with GitHub and the app reads the ledger directly from the private branch using *their own*
credentials, through a small Cloudflare Worker that does nothing but exchange an OAuth code for
a token. lydite stores nothing, so there is no custody, no tenancy, and no per-customer cost;
access control is exactly GitHub's repository permissions, revocation included; and a viewer can
compare every project they can already read, which neither alternative offers without a platform
behind it.

This binds the data layout: every file must be independently fetchable and under the GitHub
Contents API's 1 MB cap, which the monthly ledger partitions already satisfy.

Two costs are accepted. The Worker is real infrastructure, small but ours to keep up. And a
login-free public dashboard — a README badge linking somewhere anyone can open — needs the
Worker to proxy reads, because unauthenticated GitHub API access is capped at 60 requests per
hour per IP.

## Publishing stays out of the CLI

`lydite report` generates a file and stops. Deploy credentials never enter a process that runs
third-party scanners over untrusted code, and the CLI keeps behaving identically locally and in
CI — which a step that can only authenticate in CI would break.

## A GitHub App, not an OAuth App

The viewer authenticates through a GitHub App rather than an OAuth App, and the difference is not
cosmetic. An OAuth App's `repo` scope is all-or-nothing: it grants read *and write* access to every
private repository the user can reach, including ones the dashboard has no interest in. Asking for
that in order to draw a coverage chart is indefensible for a tool whose entire argument is that it
holds none of your data.

A GitHub App requests `contents: read` on repositories the user selects at install time, and reads
the ledger with a user-to-server token, so what the dashboard can see is bounded by an explicit,
revocable, per-repository grant rather than by trust. It also removes the single-callback-URL
limitation, which matters if a preview deployment ever needs its own.

The app and its callback share one origin, `app.lydite.org`, so the Worker's code-for-token
exchange never crosses origins. The domain is `.org` rather than `.io` deliberately: the callback
URL is baked into the app's configuration and every viewer's login flow depends on it, which is not
somewhere to inherit a ccTLD whose long-term governance is an open question. `.org` gives up the
automatic HSTS preloading that the `.dev` TLD would have provided; the preload entry is submitted
manually instead.
