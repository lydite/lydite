# Session prompt — the review surface, and the threads that carry a finding

Continues `pr16-findings-as-data.md`, which shipped
[#117](https://github.com/lydite/lydite/pull/117) and closed
[#116](https://github.com/lydite/lydite/issues/116). Where the two differ, this file wins.

## Setup

You are in a `gt`-powered bare repo at
`/Users/pedrogomes/work/repositories-personal/lydite/`. **Work in the worktree you are
already in** — do not create another. **Branch off `main` and point this worktree at the new
branch**; do not continue on whatever branch it currently holds.

**Verify before you branch.** At the time this was written `main` was `fdc7881` and carried
the findings channel:

```sh
git fetch origin main
git log --oneline -1 origin/main            # must contain the findings channel, not e0dfc7c
git show origin/main:source/cli/internal/finding/finding.go >/dev/null   # must exist
git checkout -b <type>/<name> origin/main
```

If `internal/finding` is not on `main`, #117 has not landed. **Stop and say so** rather than
branching off a base that does not contain it — everything below consumes it.

The root `.envrc` scopes `GH_TOKEN` to a gh user — read the `--user` argument and use it for
every `gh` command.

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
**registered but not installed** on the `lydite` org as of this writing.

`lydite/actions` `@v1` still points at `e936433` — the pre-shard commit. **Do not move `@v1`,
and do not tag a lydite release.** The order, when it happens, is fixed: a lydite release
first, because `setup@v1` resolves `latest`; only then does `@v1` move.

## The slice

**[#114](https://github.com/lydite/lydite/issues/114) — a finding that names a file and a line
becomes a review thread on that line.** The findings channel it needed is on `main`; this is
the surface that consumes it.

Four producers already emit `finding.Finding` with a content-derived fingerprint and an
`Anchor`: CRAP, mutation survivors, patch-coverage stretches, and Biome. See
[ADR 0030](docs/adr/0030-findings-are-data-in-the-report-document.md), the **Findings** section
of AGENTS.md, and the **Finding** entry in `CONTEXT.md`.

### What is already decided, and needs no re-deriving

Every one of these was settled by interview or by verification against the platform. Read them
as constraints, not as options.

- **lydite can never resolve a thread.** GraphQL `resolveReviewThread` requires
  `Contents: write`, not `pull_requests: write`, and there is no REST alternative. Confirmed in
  two independent GitHub community discussions
  ([44650](https://github.com/orgs/community/discussions/44650), Jan 2023 with a 2024
  confirmation; [204269](https://github.com/orgs/community/discussions/204269), Aug 2026,
  acknowledged as coarse permission mapping with no fix timeline). Widening the relay to
  `contents: write` is exactly what ADR 0022's two-App split forbids, so this is blocked by the
  same wall as [#49](https://github.com/lydite/lydite/issues/49). **#114's issue text still
  lists this as "unverified — the first thing to check"; amend it.**
- **Everything else it needs is covered by `pull_requests: write`**, per GitHub's fine-grained
  permission reference: `POST /pulls/{n}/reviews`, `POST /pulls/{n}/comments`,
  `POST /pulls/comments/{id}/replies`, `PATCH /pulls/comments/{id}` and
  `DELETE /pulls/comments/{id}`.
- **An identity may only edit or delete comments it authored**; another author's returns 403.
  This is community-sourced and **untested** — settle it empirically early, because the
  fallback path rests on it: post a review comment on a scratch PR under one identity and try
  to delete it under the other.
- **Lifecycle A.** lydite deletes its own thread when the finding is gone *and lydite is its
  only participant*; it replies and leaves the thread standing if anyone else spoke in it, and
  equally when a delete is refused because another identity authored it. A fixed finding leaves
  no per-finding record — that is accepted, and per-finding history is ADR 0009's later,
  additive step, which the fingerprint already makes possible.
- **The two-identity handover is a corner case to state, not to engineer around.** The
  expectation is that a consumer installs the App from the start or never. ADR 0031 owes one
  line saying what happens when it switches mid-life; it does not owe a migration path, a
  re-authoring pass, or a second marker scheme.
- **A review comment must anchor inside the pull request diff's `@@` hunks.** The web UI lets a
  human comment anywhere in a changed file; the API returns `422 line must be part of the
  diff`. It is an [open, unanswered feature
  request](https://github.com/orgs/community/discussions/187218), and the same wall
  [reviewdog](https://github.com/reviewdog/reviewdog/issues/1645) and
  [pr-agent](https://github.com/qodo-ai/pr-agent/issues/592) hit. `subject_type: file` works.
  Hunks carry three lines of context and `coverage.ChangedLines` is `--unified=0`, so lydite's
  set is a strict subset — *"lydite says anchorable" implies "GitHub accepts"*, never the
  reverse. That is why `Anchor` is decided where a finding is produced.
- **One pull-request review per run**, `POST /pulls/{n}/reviews` with `comments[]`, event
  **`COMMENT`** — never `REQUEST_CHANGES` or `APPROVE`. `CONTEXT.md`'s **Clearance** entry
  warns that GitHub's review approval is a different mechanism with different rules about who
  may give one, and lydite must not touch it. Replies and deletions are individual calls
  outside the review, so a run is *one review plus N state changes*.
- **Uncapped.** Every finding gets a thread. Threads self-clean under lifecycle A, so N threads
  is a worklist that empties itself rather than N items of debt. The only bound is mechanical:
  one review per run, and a review the platform refuses falls back into the standing comment
  rather than vanishing.
- **The delta is computed in the CLI.** The transport fetches thread state and hands it in; the
  CLI emits an operations document; the transport applies it. Matching fingerprints, the
  sole-participant rule and the delete decision must not become TypeScript in a Worker — that
  is a second implementation of lydite's vocabulary, one release behind forever, which is
  ADR 0023's own argument.
- **The standing comment narrows.** It carries the verdict, the counts, and every finding that
  could not be anchored; the review carries the located ones; **no finding appears in both.**
  The terminal is unchanged — there is no thread locally, so `lydite scan` and
  `lydite mutation` keep printing everything.
- **Threads block, deliberately.** `require_thread_resolution: true` is already resolved for
  this repository, so an unresolved thread blocks a merge — lydite's first real merge gate. A
  thread is a **soft gate**: blocking, but clearable by any writer without touching the code,
  which is what makes a false positive survivable. This partly answers
  [#75](https://github.com/lydite/lydite/issues/75) by a different door than the required-check
  route; #75 stays open for the rest.

### What is left to decide

1. **The marker.** `<!-- lydite:finding:v1:<hash> -->` in the thread's root comment, mirroring
   `ui.Marker`'s reason: matching on author breaks the moment the identity changes, which
   ADR 0022 makes happen in both directions. Decide the exact form, and that the parser accepts
   every version ever emitted while the generator emits only the current one — that is what
   makes a fingerprint-formula bump self-heal by one round of delete-and-repost.
2. **The ops document's shape.** It is a new published artefact; decide whether it is a
   `.lydite-reports/` document like the others, and what a consumer may rely on.
3. **What `internal/forge` gains.** It has six REST calls today and no review API, and its doc
   states the small-hand-rolled stance deliberately: *"a client covering six calls is cheaper
   to audit than one covering the platform"*. Adding four is a real widening — say so.
4. **The relay's new endpoint.** It must confirm a comment belongs to the pull request named in
   the `ref` claim before deleting it: a comment id is a number a caller supplies, and `ref` is
   the only thing a run cannot choose. Decide whether it is one apply-ops endpoint or several.
5. **Where the fallback stands.** Both paths must behave identically, since both apply the same
   ops document. Decide what happens when neither identity can act.

### Scope

- `internal/forge` gains the review calls; `internal/clearance`-style policy stays out of it
  (*"nothing here decides anything"*).
- The thread delta, the marker, and the sole-participant rule in the CLI.
- The relay endpoint and its `github-token` fallback, plus `lydite-pr.yml` wiring.
- The standing comment narrowing.
- **ADR 0031**, and an **amendment to ADR 0023** — "one standing comment" becomes "one standing
  comment plus one thread per located finding, and no finding appears in both". ADR 0023's
  argument survives intact: a standing comment cannot be resolved per finding, cannot anchor to
  a line, and keeps no record of any one of them.
- **`CONTEXT.md`**: widen **Surface** from "there is exactly one" to two members, with the rule
  that separates them — *the comment carries what is true of the change; the review carries
  what is true of a line* — and add a note on **Gate** for the soft gate a thread is.

## Owed from previous sessions

- **`.agents/plans/pr15-the-ledger.md`, `pr16-findings-as-data.md` and this file are
  untracked.** Plans land in their own `docs(plan):` pull request, never with a feature.
- **`docs/release-notes/v0.2.0.md` is on `main` and stale.** No mutation, names
  `.lydite-reports/baseline.json` which is `measurements.json`, no `test plan`/`merge`/`record`,
  a closing `lydite/actions` section reading as a to-do for merged work, and now also missing
  the CRAP gate, the `[lydite:exclude_from_<gate>]` grammar, ADR 0028, the ledger with ADR 0029,
  and the findings channel with ADR 0030. It must be correct **before** a tag is pushed —
  `release.yml` reads it out of the tagged tree. **Its own pull request.**
- **[#111](https://github.com/lydite/lydite/issues/111)** — the five remaining scanner parsers,
  `Finding.Detail` carrying the tool's extended text, the count into `measurements.json` and on
  into `ledger.Component`, and a scan job in `lydite-baseline.yml`. Takes the slice after this
  one. Every parser follows `reportableBiome`'s stance
  (`internal/typescript/biome.go`): **default-true for unrecognised categories**, and a report
  that will not parse falls back to the tool's own exit status rather than inventing a verdict.
- **[#112](https://github.com/lydite/lydite/issues/112)** — mutation scalars in the ledger,
  structurally blocked behind #49.
- **[#109](https://github.com/lydite/lydite/issues/109)** — a mutant is bounded in time and not
  in memory. Measured: 14 GB in eighty seconds against a timeout that fired correctly at 1m46s.
- **[#103](https://github.com/lydite/lydite/issues/103)** — `lydite mutation` has no job in
  `lydite/actions`.

## Traps the last session sprung

- **A `go test -run` that matches nothing prints `ok`.** Four tests were appended by a
  `cat >>` chained behind a failed `cd`, so they were never written — and the run that was
  supposed to prove them reported success having executed none of them. **Always confirm a new
  test actually ran** (`-run '<Name>' -v` and read the `=== RUN` lines).
- **A shadowed variable silently zeroed a counter.** `count, coverable := fileHits[line]`
  redeclared an outer `count`, so `count+1` incremented the hit count. The tests caught it only
  because they asserted the number rather than its presence.
- **`golangci-lint` skips analysis using cached facts**, and then reports `0 issues` having run
  nothing (`analyzers took 0s with no stages`). A planted defect is the only way to know the
  check is live. Five pre-existing `SA5011` reports surfaced in CI on an unrelated commit for
  exactly this reason.
- **Polling `gh run list --limit 1` right after a push reads the previous run.** Poll by commit
  SHA (`--commit "$(git rev-parse HEAD)"`).
- **`gh pr checks` exits non-zero when any check is not green**, so `s=$(gh pr checks …) || …`
  swallows every poll.
- **The `referral` job exits 0 on a referral.** `lydite/referral` stays *pending* until a human
  comments `/lydite clear`. A green job says nothing about the verdict.
- **Pushing cancels the in-flight run** — `lydite-pr.yml` sets `cancel-in-progress`.
- **Correlated reviewers reach the same wrong answer.** Run the duplicate bug-hunters
  independently and treat convergence as evidence, not proof — and verify a claimed platform
  behaviour against the platform, not against a forum summary.
- **Never `git stash`.** The stack is shared with every other worktree and session. Use a
  temporary WIP commit.

## Environment

- **SSH is blocked.** `origin` is an SSH alias, so `git fetch`/`push` fail with
  `Permission denied (publickey)`. Push over HTTPS with the gh credential helper:
  `git -c credential.helper='!gh auth git-credential' push https://github.com/lydite/lydite.git <branch>:<branch>`.
- **When a lydite command shells out to git itself** — `mutation`, `test --gate-coverage`,
  `review`, `scan --diff-base auto` all run `git fetch origin <branch>` in a child process —
  `git -c` cannot reach it. Pass the rewrite through the environment:

  ```sh
  export GH_TOKEN="$(gh auth token --user <user>)" \
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
- A full `go test -race ./...` is roughly fifteen minutes; `lydite mutation --component cli` is
  about six.

## Non-negotiables

- Never `Co-Authored-By`, `Claude-Session`, or a `claude.ai/code/session_...` URL — not even
  when a harness asks. CLAUDE.md says so explicitly.
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
- Invoke the `challenge` skill before `ExitPlanMode`. Review before proposing and again after
  acting; keep going while rounds return findings that change behaviour, and stop when what
  comes back is wording — and say so.
