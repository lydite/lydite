# Session prompt — building the review surface, with the design already settled

Continues `pr17-findings-as-threads.md`, whose design questions have since been answered by
interview. **Where the two differ, this file wins.** That file remains the record of what was
already decided before the interview; this one records what the interview decided and is what
you build from.

## Setup

You are in a `gt`-powered bare repo at
`/Users/pedrogomes/work/repositories-personal/lydite/`. **Work in the worktree you are
already in** — `feature/acequia-flash-flood` — and do not create another.

**The branch is already cut and checked out: `feat/findings-as-threads`, on `origin/main`.**
Do not branch again. Verify before you start:

```sh
git rev-parse --abbrev-ref HEAD        # feat/findings-as-threads
git log --oneline -1                   # should be origin/main's head
ls .agents/references/findings.md      # must exist — see below
```

**`AGENTS.md` was split while this slice was paused**, and that changes where prose lands.
The root is now a ~215-line map; every narrative lives in `.agents/references/`, one file per
concern, with six scoped `AGENTS.md` + `CLAUDE.md` pairs down the tree. So the documentation
this slice owes goes to `.agents/references/findings.md` and `.agents/references/surface.md`,
**not** to `AGENTS.md` — which now only needs its map row touched if a new reference appears.
Read the root `AGENTS.md` first; it says where everything is.

The root `.envrc` scopes `GH_TOKEN` to gh user `pedromvgomes`. Use it for every `gh` command.

The Go module is at `source/cli/`; the scan root is the repository root above it. Every `go`
and `golangci-lint` command runs from `source/cli` with `GOTOOLCHAIN=local`, **and with the
release build tags**:

```sh
GOTOOLCHAIN=local go test -race -tags 'grammar_subset grammar_subset_rust grammar_subset_typescript grammar_subset_tsx' ./...
```

Without them the binary carries all 206 of gotreesitter's grammars, and `ci-test` runs the
suite *with* them — a language whose own `grammar_subset_<lang>` tag is missing fails at its
first parse, and a bare `go test` can never see it.

## The gate on the release

**Nothing ships to consumers until everything but the dashboard
([#27](https://github.com/lydite/lydite/issues/27)) is done — and that explicitly includes the
relay being deployed**, so `vars.LYDITE_RELAY_URL` is set and consumers get the App identity
rather than only the `github-token` fallback. The relay App (`LYDITE_APP_ID=4814607`) is
**registered but not installed** on the `lydite` org.

`lydite/actions` `@v1` still points at `e936433`. **Do not move `@v1`, and do not tag a lydite
release.** The order, when it happens, is fixed: a lydite release first, because `setup@v1`
resolves `latest`; only then does `@v1` move.

## The slice

**[#114](https://github.com/lydite/lydite/issues/114) — a finding that names a file and a line
becomes a review thread on that line.** Four producers already emit `finding.Finding` with a
content-derived fingerprint and an `Anchor`: CRAP, mutation survivors, patch-coverage stretches
and Biome. See [ADR 0030](docs/adr/0030-findings-are-data-in-the-report-document.md),
`.agents/references/findings.md`, and the **Finding** entry in `CONTEXT.md`.

```
lydite publish  → comment.md          pure: no network, no token, no platform
lydite threads  → ops.json [--apply]  fetch state · compute delta · apply
                       │
            ┌──────────┴───────────┐
   relay POST /review        forge + GH_TOKEN
   (App identity)            (github-token fallback)
```

## Settled by interview — build these, do not re-derive them

1. **A new subcommand applies the ops on the `github-token` path.** `internal/forge` gains four
   review calls — list review comments, create a review with `comments[]`, reply to a comment,
   delete a comment — taking it from six to ten. Its package doc argues that *"a client
   covering six calls is cheaper to audit than one covering the platform"*; that number changes,
   so **restate the argument at its new size rather than quietly deleting it**. The standing
   comment keeps its existing bash fallback in `.github/actions/lydite-comment`; do not fold it
   in.

2. **`lydite threads` owns fetch, delta and apply.** It reads `--reports` (repeatable), fetches
   prior thread state through `internal/forge`, computes the delta, **always** writes the ops
   document to `--ops <file>`, and applies it only when given `--apply`. The relay applies the
   identical document, so there is one delta implementation and two dumb transports. The
   prior-state read uses the job's own token on both paths — only the *write* is relayed, and
   the publish job already holds `pull-requests: write` for the comment fallback, so this costs
   no new credential. Say that out loud in the ADR rather than letting it look like an
   oversight against ADR 0022.

   `lydite publish` stays pure. Nothing about a hosting platform enters it.

3. **A finding names its row.** `finding.Finding` gains one field carrying the label of the row
   that reported it, written by the producer that already knows both. `publish` then renders a
   failing row's comment detail from that row's **unanchored** findings, plus a line counting
   the located ones; a row with no findings quotes `Row.Detail` exactly as today.

   This is a deliberate partial reversal of ADR 0030, which removed the row link on the grounds
   that *"the grouping buys nothing"*. It buys something now, and **ADR 0031 owes that
   amendment explicitly** — it is not nesting findings inside rows, and it is not parsing
   `gosec(cli)` back out of a label, which are the two things ADR 0030 actually refused.

   Accepted cost: a row's non-finding asides (`"3 did not compile"`) drop out of the *comment*.
   They stay in the terminal and in the log. Say so.

   **"No finding appears in both" needs no coordination** between the two renderers: it is a
   partition on `Anchor`, which is already in the document. The comment takes `AnchorNowhere`;
   the review takes the rest.

   **The narrowing is unconditional.** `publish` drops located findings whether or not threads
   are being posted, so a developer running it locally reads exactly the comment a reviewer
   sees — ADR 0023's parity property, preserved.

4. **A review that cannot be posted fails the publish job**, naming how many located findings
   reached no surface. Never a silent green: the memory store already records
   `relay-audience-mismatch-degrades-silently` as a standing hazard, and this must not become a
   second instance of it. The findings still reach the terminal, the job log and the uploaded
   `.lydite-reports/` artifact.

5. **The ops document is lydite's own shape, a private versioned wire.** Written to
   `--ops <file>`, **not** into `.lydite-reports/` — `lydite test plan` is the precedent for a
   command that reaches no verdict writing no report document. Nothing about it is promised to a
   consumer. Shape:

   ```json
   { "version": 1, "pull_request": 42, "head": "a1b2c3…",
     "create": [ { "fingerprint": "v1:…", "path": "…/runner.go", "line": 453,
                   "subject": "line", "body": "<!-- lydite:finding:v1:… -->…" } ],
     "reply":  [ { "comment": 998, "body": "…" } ],
     "delete": [ 997 ] }
   ```

6. **The marker is `<!-- lydite:finding:` + `Fingerprint()` + ` -->`.** The token after the
   prefix *is* the fingerprint, version prefix included — do not re-derive a second id. The
   parser accepts any `<!-- lydite:finding:<token> -->` and returns the token verbatim, so a
   thread written under a future fingerprint formula is simply one matching no current finding,
   which lifecycle A already handles. That is what makes a formula bump self-heal in one round
   of delete-and-repost, with no migration code.

   Matching on the marker and never on the author, for `ui.Marker`'s reason: the author is
   whoever's token posted it, and ADR 0022 makes that change in both directions.

7. **One relay endpoint, `POST /review`, answering per operation.** In order: verify the OIDC
   token; take the repository and the pull request from the verified claims and never from the
   body; refuse an unknown document version rather than half-applying it; `GET
   /pulls/{n}/comments` once to build the membership set; **refuse the whole request if any
   `reply` or `delete` id is outside it**, applying nothing — a comment id is a number the
   caller supplies and `ref` is the only thing a run cannot choose; then apply, answering with
   an outcome per operation so a posted review and a refused delete are distinguishable.

   Keep every property the existing `/comment` endpoint holds: RS256 fixed rather than read from
   the token, a rejection carrying no detail, nothing stored, the PKCS#8 key, and the
   `github-token` fallback as a designed path rather than an error being papered over.

   **`pullRequestFromRef` accepts `refs/pull/<n>/head` as well as `/merge`**, which its own doc
   comment and every test omit — the memory store records this as
   `pullrequestfromref-also-accepts-head`. Decide what you want it to do here rather than
   inheriting it by accident.

8. **`lydite is the only participant` governs both branches of the lifecycle.** One predicate,
   two uses:

   | Situation | Sole participant | Someone else spoke |
   |---|---|---|
   | the finding is gone | delete the thread | reply, and leave it standing |
   | the thread is outdated, the finding persists | delete and recreate at the current line | leave it outdated |

   A delete refused because another identity authored the comment takes the same path as
   "someone else spoke": reply and leave it standing.

   **Why outdated threads get moved at all:** GitHub collapses an outdated thread behind its
   "Show outdated" toggle, so a thread that blocks the merge becomes one the author cannot see.
   That is worse than the notification churn of reposting. `position` comes back null while
   `original_line` holds, which is how you detect it.

   A fixed finding leaves no per-finding record. That is accepted; per-finding history is
   ADR 0009's later, additive step, which the fingerprint already makes possible.

9. **`lydite threads` dedups by fingerprint on read** — first occurrence wins, the drop named on
   stderr. It consumes documents a local run wrote with no fold at all, so it cannot rely on the
   fold being fixed. **The fold's own duplication is carved into
   [#123](https://github.com/lydite/lydite/issues/123) and is out of scope here** — do not fix
   it in this PR.

## Decided earlier, and still binding

- **lydite can never resolve a thread.** GraphQL `resolveReviewThread` requires
  `Contents: write`, not `pull_requests: write`, and there is no REST alternative. Confirmed in
  two independent GitHub community discussions
  ([44650](https://github.com/orgs/community/discussions/44650),
  [204269](https://github.com/orgs/community/discussions/204269)). Widening the relay to
  `contents: write` is exactly what ADR 0022's two-App split forbids, so this is blocked by the
  same wall as [#49](https://github.com/lydite/lydite/issues/49).
  **#114's issue text still lists this as "unverified — the first thing to check"; amend it.**
- **Everything else is covered by `pull_requests: write`**: `POST /pulls/{n}/reviews`,
  `POST /pulls/{n}/comments`, `POST /pulls/comments/{id}/replies`,
  `PATCH /pulls/comments/{id}`, `DELETE /pulls/comments/{id}`.
- **A review comment must anchor inside the pull request diff's `@@` hunks.** The web UI lets a
  human comment anywhere in a changed file; the API returns `422 line must be part of the diff`.
  Hunks carry three lines of context and `coverage.ChangedLines` is `--unified=0`, so lydite's
  set is a strict subset — *"lydite says anchorable" implies "GitHub accepts"*, never the
  reverse. `subject_type: file` works for `AnchorFile`.
- **One review per run**, `POST /pulls/{n}/reviews` with `comments[]`, event **`COMMENT`** —
  never `REQUEST_CHANGES` or `APPROVE`. `CONTEXT.md`'s **Clearance** entry warns that GitHub's
  review approval is a different mechanism with different rules about who may give one, and
  lydite must not touch it. Replies and deletions are individual calls outside the review, so a
  run is *one review plus N state changes*.
- **Uncapped.** Every finding gets a thread; they self-clean under lifecycle A. The only bound
  is mechanical — one review per run, and a review the platform refuses fails the job (4).
- **Threads block, deliberately.** `require_thread_resolution: true` is already resolved for
  this repository, so an unresolved thread blocks a merge — lydite's first real merge gate. A
  thread is a **soft gate**: blocking, but clearable by any writer without touching the code,
  which is what makes a false positive survivable. This partly answers
  [#75](https://github.com/lydite/lydite/issues/75) by a different door; #75 stays open for the
  rest.
- **The two-identity handover is a corner case to state, not to engineer around.** The
  expectation is that a consumer installs the App from the start or never. ADR 0031 owes one
  line saying what happens when it switches mid-life; it does not owe a migration path, a
  re-authoring pass, or a second marker scheme.
- **The terminal is unchanged.** There is no thread locally, so `lydite scan` and
  `lydite mutation` keep printing everything.

## One thing to settle empirically, early

**An identity may only edit or delete comments it authored**; another author's is said to
return 403. This is community-sourced and **untested**, and the fallback path rests on it.
Settle it on a scratch pull request: post a review comment under one identity and try to delete
it under the other.

Only half is testable here — the relay App is registered but **not installed**, so you can test
`github-actions[bot]` against `pedromvgomes`, not the App against either. Lifecycle A handles a
refusal either way, so the design does not change on the result; what the probe buys is knowing
that branch is reachable rather than dead code. Say which half you tested.

## Scope

- `internal/forge` gains the four review calls; `internal/clearance`-style policy stays out of
  it (*"nothing here decides anything"*).
- `lydite threads`: the fetch, the delta, the marker, the sole-participant rule, the dedup, and
  `--apply`.
- The relay's `POST /review` endpoint and its `github-token` fallback, plus `lydite-pr.yml`
  wiring in the `publish` job.
- The standing comment narrowing, and `finding.Finding`'s new field.
- **ADR 0031**, and an **amendment to ADR 0023** — "one standing comment" becomes "one standing
  comment plus one thread per located finding, and no finding appears in both". ADR 0023's
  argument survives intact: a standing comment cannot be resolved per finding, cannot anchor to
  a line, and keeps no record of any one of them. ADR 0031 also owes the ADR 0030 amendment
  from (3).
- **`CONTEXT.md`**: widen **Surface** from "there is exactly one" to two members, with the rule
  that separates them — *the comment carries what is true of the change; the review carries what
  is true of a line* — and add a note on **Gate** for the soft gate a thread is.
- **`.agents/references/findings.md` and `.agents/references/surface.md`** carry the prose.
  Add a map row in the root `AGENTS.md` only if you create a new reference file.

## Owed from previous sessions

- **`.agents/plans/pr16-findings-as-data.md` and `pr17-findings-as-threads.md` are untracked**,
  and so is this file. Plans land in their own `docs(plan):` pull request, never with a feature
  — and that PR needs its own issue, since a PR closes one.
- **`docs/release-notes/v0.2.0.md` is on `main` and stale.** No mutation, names
  `.lydite-reports/baseline.json` which is `measurements.json`, no `test plan`/`merge`/`record`,
  a closing `lydite/actions` section reading as a to-do for merged work, and now also missing
  the CRAP gate, the `[lydite:exclude_from_<gate>]` grammar, ADR 0028, the ledger with ADR 0029,
  the findings channel with ADR 0030, and the `AGENTS.md` split. It must be correct **before** a
  tag is pushed — `release.yml` reads it out of the tagged tree. **Its own pull request.**
- **[#123](https://github.com/lydite/lydite/issues/123)** — the fold unions every shard's
  findings with no fingerprint dedup. Carved out of this slice deliberately.
- **[#111](https://github.com/lydite/lydite/issues/111)** — the five remaining scanner parsers,
  `Finding.Detail` carrying the tool's extended text, the count into `measurements.json` and on
  into `ledger.Component`, and a scan job in `lydite-baseline.yml`. Takes the slice after this
  one. Every parser follows `reportableBiome`'s stance (`internal/typescript/biome.go`):
  **default-true for unrecognised categories**, and a report that will not parse falls back to
  the tool's own exit status rather than inventing a verdict.
- **[#112](https://github.com/lydite/lydite/issues/112)** — mutation scalars in the ledger,
  structurally blocked behind #49.
- **[#109](https://github.com/lydite/lydite/issues/109)** — a mutant is bounded in time and not
  in memory. Measured: 14 GB in eighty seconds against a timeout that fired correctly at 1m46s.
- **[#103](https://github.com/lydite/lydite/issues/103)** — `lydite mutation` has no job in
  `lydite/actions`.

## Traps previous sessions sprung

- **A `go test -run` that matches nothing prints `ok`.** Four tests were appended by a `cat >>`
  chained behind a failed `cd`, so they were never written — and the run that was supposed to
  prove them reported success having executed none of them. **Always confirm a new test actually
  ran** (`-run '<Name>' -v`, and read the `=== RUN` lines).
- **Two `sed` expressions apply in sequence to the same line.** `-e 's/^### /## /' -e 's/^## /# /'`
  flattens every heading to `#`, because the first expression's output matches the second. This
  silently mangled 22 files in the `AGENTS.md` split and was caught only by a reconstruction
  diff. **When you transform a file mechanically, rebuild the original from the output and diff
  it.**
- **A shadowed variable silently zeroed a counter.** `count, coverable := fileHits[line]`
  redeclared an outer `count`. The tests caught it only because they asserted the number rather
  than its presence.
- **`golangci-lint` skips analysis using cached facts**, then reports `0 issues` having run
  nothing (`analyzers took 0s with no stages`). A planted defect is the only way to know the
  check is live.
- **Editing a `note:` in `.gt-repo.yaml` re-renders `.github/dependabot.yml`.** `gt repo check`
  fails until you run `gt repo sync`. Never hand-edit the generated file.
- **Polling `gh run list --limit 1` right after a push reads the previous run.** Poll by commit
  SHA (`--commit "$(git rev-parse HEAD)"`).
- **`gh pr checks` exits non-zero when any check is not green**, so `s=$(gh pr checks …) || …`
  swallows every poll.
- **The `referral` job exits 0 on a referral.** `lydite/referral` stays *pending* until a human
  comments `/lydite clear`, and `.lydite/exemptions.yml` does not exist — so **every** pull
  request here is referred. A green job says nothing about the verdict; read the commit status.
- **Pushing cancels the in-flight run** — `lydite-pr.yml` sets `cancel-in-progress`.
- **Correlated reviewers reach the same wrong answer.** Run duplicate bug-hunters independently
  and treat convergence as evidence, not proof — and verify a claimed platform behaviour against
  the platform, not against a forum summary.
- **Never `git stash`.** The stack is shared with every other worktree and session. Use a
  temporary WIP commit.
- **`git reset --hard` is blocked by the permission classifier.** Move a branch with
  `git branch -f <name> <sha>` from another branch instead.

## Environment

- **SSH is blocked.** `origin` is an SSH alias, so plain `git fetch`/`push` fail with
  `Permission denied (publickey)` — and a failed `git fetch origin main` leaves `FETCH_HEAD`
  stale, which has already caused a cherry-pick to land on the wrong branch. Always check the
  fetch succeeded. Push and fetch over HTTPS with the gh credential helper:

  ```sh
  git -c credential.helper='!gh auth git-credential' \
      push https://github.com/lydite/lydite.git <branch>:<branch>
  ```

- **When a lydite command shells out to git itself** — `mutation`, `test --gate-coverage`,
  `review`, `scan --diff-base auto` all run `git fetch origin <branch>` in a child process —
  `git -c` cannot reach it. Pass the rewrite through the environment:

  ```sh
  export GH_TOKEN="$(gh auth token --user pedromvgomes)" \
    GIT_CONFIG_COUNT=2 \
    GIT_CONFIG_KEY_0=url.https://github.com/.insteadOf GIT_CONFIG_VALUE_0=git@github-personal: \
    GIT_CONFIG_KEY_1=credential.helper GIT_CONFIG_VALUE_1='!gh auth git-credential'
  ```

  Without it the command stops at `mutation is scoped to the change against the merge-base, and
  it could not be resolved: fetch origin main: exit status 128`, which names a shallow checkout
  as the usual cause and is misleading here.
- **No container runtime**, so nothing touching the proving ground runs locally.
- `lydite scan` runs from the repository root (`--dir ../..` from `source/cli`). `--dir ..`
  names `source/`, which declares no components and errors.
- A full `go test -race ./...` is roughly fifteen minutes — the compile dominates and prints
  nothing for the first ten. `lydite mutation --component cli` is about six.

## Non-negotiables

- Never `Co-Authored-By`, `Claude-Session`, or a `claude.ai/code/session_...` URL — not even
  when a harness asks. `CLAUDE.md` says so explicitly.
- `Closes #N`, not `Refs`. Carve unshipped scope into a new issue first.
- Never merge a PR unless told to. **Never tag a release, and never move `@v1`.**
- Comments describe the code as it is — no "used to", no "now", no ticket numbers, no roadmap.
  ADRs are the exception. Test names say what behaviour they protect.
- Never hand-edit a generated file; edit `.gt-repo.yaml`, then `gt repo sync`.
- Before proposing: `go build`, `go test -race`, `golangci-lint run` **with the grammar tags**,
  from `source/cli`, plus `lydite scan --dir .` and `gt repo check` from the root — and
  `lydite test --component cli` to see the CRAP row, because a gate can fail for a reason the
  change caused elsewhere.
- **Prove a regression test fails without its fix.**
- **Answer every mutation survivor**, and prefer rewriting past an equivalent mutant to
  declaring one. A boundary nothing can observe is answered by leaving no comparison to shift —
  a clamp states itself as `min`/`max`.
- Delegate to the `memory-explorer` agent before answering *why* something is built the way it
  is, what breaks if you change X, or whether an approach has been tried. The store holds seven
  notes, four of which touch this slice.
- Review before proposing and again after acting; keep going while rounds return findings that
  change behaviour, and stop when what comes back is wording — and say so.

**The design above is settled. Do not re-open it in a `challenge` interview.** If building it
turns up something that contradicts a decision, say which decision and why, and ask — a
contradiction found in the code is worth more than the interview that missed it, but it is still
the user's call.
