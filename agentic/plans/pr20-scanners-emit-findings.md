# Session prompt — PR 2 of 3: every scanner reports its findings as data

Continues `pr19-findings-deduped.md`, which merged as
[#127](https://github.com/lydite/lydite/pull/127) — the folded document now holds each claim
once, through `finding.Set` reached from `ui.Report.AddFindings`. **This is the second of
three.** The third is described at the bottom under *What comes after*. **Do not start it.**
Work on PR 3 begins only once this one has merged.

## Setup — do this before anything else

You are in a `gt`-powered bare repo at `/Users/pedrogomes/work/repositories-personal/lydite/`.
**Work in the worktree you are already in — do not create another.** Every PR in this
sequence uses this same worktree.

**Point the worktree at a fresh branch off the current `main`:**

```sh
export GH_TOKEN="$(gh auth token --user pedromvgomes)"
git -c credential.helper='!gh auth git-credential' \
    fetch https://github.com/lydite/lydite.git main:refs/remotes/origin/main --force
git log --oneline -1 origin/main          # must carry #127's squash, or later
git checkout -b feat/scanners-emit-findings origin/main
```

Check the fetch actually succeeded. **SSH is blocked** — `origin` is an SSH alias, so a plain
`git fetch` fails with `Permission denied (publickey)` and leaves `FETCH_HEAD` stale, which has
already caused a cherry-pick onto the wrong branch in an earlier session.

The root `.envrc` scopes `GH_TOKEN` to gh user `pedromvgomes`. Use it for every `gh` command.

## Carve the issue first

**[#111](https://github.com/lydite/lydite/issues/111) is about the count, not the parsers.** A
pull request closes one issue, so **open a new issue for this slice before writing any code**:
every scanner reporting its findings as data, the six parsers, `Finding.Detail`. #111 stays for
PR 3, which is where a finding count reaches the ledger.

This PR's body ends with `Closes #<the new issue>`, never `Refs`.

## The slice

Today `biome` is the only check that turns its tool's report into `finding.Finding` values. The
rest — `gosec`, `semgrep`, `cargo clippy`, `cargo-audit`, `cargo-deny`, `govulncheck` — render
to the terminal and report a row, and nothing downstream can anchor to what they found. So the
standing comment and the review threads carry the TypeScript findings and none of the others,
which reads as lydite having found nothing where it found plenty.

Six parsers, `Finding.Detail`, [ADR 0032](../../docs/adr/), and amendments to
`.agents/references/findings.md` and `.agents/references/scanning.md`.

This also carries the parked `CONTEXT.md` edge at
`/tmp/claude-502/plan/context-dependency-advisory.patch` — a **Dependency advisory** glossary
entry. Re-apply it here; it was deliberately kept out of PR 1. If that file has been cleaned,
write the entry from decision 2 below rather than skipping it.

### Decisions already taken. Do not re-litigate them.

1. **Side-channel, not takeover.** Each tool keeps rendering to the terminal exactly as today;
   lydite reads a JSON copy purely to populate `Result.Findings`. `Result.Detail` stays
   **empty** for these tools — its own doc says "empty for every tool that prints its own
   findings" — and the per-finding `Finding.Detail` carries the excerpt, the call stack and
   the dependency graph. Biome stays the exception it already is.
2. **A dependency advisory is located at the manifest/lockfile line** naming the package,
   because the one edit that clears it is the bump. Call traces become `Finding.Detail`, never
   the anchor. When the line cannot be found: `Line: 0`, unanchorable, the standing comment —
   **never a guessed line**, which after #124 would be a thread on a stranger's PR.
   `site` is the advisory id with package and version, *not* the line's text: a lockfile line
   reads `name = "time"` and cannot tell two advisories against one crate apart. This is a
   stated departure from `findings.md` and the prose must say so.
3. **`govulncheck` runs twice** — text for the terminal and the verdict, JSON for the data.
4. **`cargo fmt` gets no parser.** `.agents/references/linters.md:93` — "lydite is not a
   formatter and must never report a formatting diff as a finding." That the `cargo fmt` row
   still fails a Rust component contradicts it, and is a **separate** open question; the memory
   note `rust-fmt-still-fails-scan` records it. Do not fix it here.
5. **Clippy dedup happens in the parser, before `finding.Number`.** See the trap below.

### Verified against the real tools, on 2026-09-11. Do not re-derive; do re-check if a pin moves.

| Check | JSON *and* its own terminal output? | Exit status under JSON |
|---|---|---|
| `gosec` | **Yes** — `-fmt json -out <file> -stdout -verbose text` | preserved (1) |
| `semgrep` | **Yes** — `--json-output=<file>` writes *a copy* | preserved |
| `cargo clippy` | No flag, but each message carries `rendered`, the exact text cargo prints | preserved |
| `cargo-audit` | **No** — `--json` replaces terminal output | preserved (1) |
| `cargo-deny` | **No** — `--format json`, NDJSON on **stderr** | preserved (1/4) |
| `govulncheck` | **No** — and no output-file flag exists | **LOST — JSON exits 0, text exits 3** |

Shapes, as observed:

- **gosec**: `{"Golang errors": {...}, "Issues": [...], "Stats": {...}}`. `file` is an
  **absolute path** — rebase it. `line` is a **string** and may be a range (`"10-12"`).
  `code` is the source excerpt → `Finding.Detail`. **`Golang errors` is non-empty when a
  package did not compile**, which means gosec scanned nothing there — the analogue of Biome's
  `parse` and `internalError/io` categories, and it must not read as a clean pass.
- **cargo-audit**: `vulnerabilities.list[]` with `advisory.id`, `package.name`,
  `package.version`, `versions.patched`. A `Cargo.lock` `[[package]]` stanza is unique per
  (name, version), so locating the line is an **exact** lookup. `warnings` is a separate map
  (unmaintained/unsound/notice) — decide explicitly whether those are findings.
- **cargo-deny**: NDJSON on stderr, `{"type":"diagnostic","fields":{...}}` mixed with
  `{"type":"log",...}`. Fields carry `code`, `severity`, `message`, `graphs` (the dependency
  path → `Finding.Detail`), and *sometimes* `labels[]` with `line`/`column`/`span` **but no
  filename**. Many diagnostics have **no label at all** — they name a crate, not a place.
- **govulncheck**: a stream of `{"config":…}`, `{"SBOM":…}`, `{"progress":…}`, `{"osv":…}`,
  `{"finding":…}` objects. It emits **several `finding` messages per advisory** at different
  trace depths — 17 messages for 11 distinct OSVs in the probe. **Dedup by OSV and keep the
  richest trace, or it triple-counts.** The trace's *last* frame is the scanned module's own
  code. `stdlib` advisories have a depth-1 trace only and are cleared by a toolchain bump.
- **clippy**: `--message-format json`, `message.spans[].file_name` relative to the workspace
  root, `line_start`/`line_end`, `message.code.code` is the lint name, `message.rendered` is
  the terminal text.

**Measured**: a second `govulncheck` pass over lydite's own `source/cli` costs **4.6s** on top
of a 6.2s first pass — the second is cheaper because it reuses the first's package-load work.

### Trap, found while probing and not yet in any doc

`cargo clippy --all-targets` emits **every diagnostic twice**, once for the lib target and once
for the test target — identical file, span, rule and source text. Fingerprint dedup **cannot**
fix this, and #127 does not change that: `finding.Number` gives the two copies ordinals 0 and 1,
so they hash differently and both survive, by design. Collapse them **in the clippy parser,
before numbering**, keyed on file, span and rule. Get this wrong and lydite puts two review
threads on one line and reports double the count.

### The stance every parser takes

**Follow `reportableBiome`** (`internal/typescript/biome.go`): **default-true for an
unrecognised category**, and a report that will not parse falls back to the tool's own exit
status rather than inventing a verdict. A parser that silently drops a diagnostic it does not
recognise is how a gate stops gating.

### What #127 already gives you, and what it does not

`ui.Report.AddFindings` collects on the fingerprint through `finding.Set`, so this PR cannot
put a claim in the document twice by routing one through two producers. It does **not** save
you from the clippy double-emit, which is two claims with two ordinals and not a duplicate at
all as far as the fingerprint is concerned.

Dedup happens at `AddFindings` and deliberately not earlier, because `scan`'s `labelled` sets
`Component` and rebases `Path` after the parser numbered its findings, and both are fingerprint
ingredients. **A parser must therefore number its findings itself, in source order, before
returning them**, exactly as biome's does — and must not try to fingerprint anything.

### Tests

- Each parser, against a captured real report — not a hand-written one where possible.
- The unrecognised-category case, per parser: it is reported, not dropped.
- A report that will not parse falls back to the exit status.
- gosec's `Golang errors` does not read as a clean pass.
- An advisory whose lockfile line cannot be found is `Line: 0` and unanchorable.
- **The clippy double-emit collapses to one claim** — the regression that matters most here.
- govulncheck's several messages per OSV collapse to one finding with the richest trace.

**Prove each new test fails without its fix.** A `go test -run` that matches nothing prints
`ok` — confirm the test actually ran with `-run '<Name>' -v` and read the `=== RUN` lines.

## Gates — before you commit, and again before you propose

From `source/cli`, with `GOTOOLCHAIN=local` and **the release build tags**:

```sh
GOTOOLCHAIN=local go build -tags 'grammar_subset grammar_subset_rust grammar_subset_typescript grammar_subset_tsx' ./...
GOTOOLCHAIN=local go test -race -tags 'grammar_subset grammar_subset_rust grammar_subset_typescript grammar_subset_tsx' ./...
golangci-lint run ./...
```

Without the tags the binary carries all 206 of gotreesitter's grammars and `ci-test` runs the
suite *with* them, so a language whose own tag is missing fails at its first parse and a bare
`go test` can never see it.

From the repository root: `lydite scan --dir .` and `gt repo check`.

**`golangci-lint` skips analysis using cached facts and then reports `0 issues` having run
nothing.** A planted defect is the only way to know the check is live — and it must be one that
*compiles*: an unused function trips `unused`, while a `declared and not used` variable is a
compile error the linter reports as `typecheck`, which proves nothing about the analysis.

## Commit

One commit on the branch. Conventional Commits, lower-case subject, no trailing full stop:

```
feat(scan): every scanner reports its findings as data
```

Keep the branch at **one commit** — amend it as the review loop below produces fixes, so
"the last commit" and "the branch" stay the same object.

## Loop A — review the commit, before the PR exists

**At most 5 turns.** Each turn:

1. Run `/panel-code-review` on the last commit. Pass both ends:
   `--base <sha>~1 --head <sha>` — `--base` alone reviews the commit *plus* the working tree.
2. **Fix every valid finding, including GREEN ones.** A finding you judge invalid is not
   silently dropped — say which one and why, in your reply to the user.
3. Re-run the full gate list above.
4. Amend the commit.

**Stop when a turn returns zero findings, or when you have used 5 turns — whichever is first.**
Do not exceed 5. If you stop at 5 with findings outstanding, say so plainly and list what is
left.

A worktree review of a change this size escalates to the `deep` panel (the rule is
`referencing_files >= 20` in a worktree) — six Claude runs, about $4. That is expected; report
the cost and continue without asking.

## Open the pull request

```sh
git -c credential.helper='!gh auth git-credential' \
    push https://github.com/lydite/lydite.git feat/scanners-emit-findings:feat/scanners-emit-findings
```

PR title is the commit subject — it must conform to Conventional Commits, because `gt`
squash-merges and the title becomes a commit subject on `main`. Body says what each parser
reads, why the side-channel and not a takeover, why an advisory anchors to the manifest line,
and why the clippy collapse happens before numbering. End with `Closes #<the new issue>`.

## Loop B — review the pull request

**At most 5 turns.** Each turn:

1. Run `/panel-code-review` on the PR.
2. Fix every valid finding, including GREEN ones. Push.
3. **Reply to every open comment on the PR** — the panel's own, and anything a human or bot
   left. A comment you are not acting on gets a reply saying why, not silence.

**Stop at zero findings with no open comments, or at 5 turns.** Report where you stopped.

**A PR target selects the `deep-codex` panel, and codex hit its usage limit during #127.** When
every reviewer answers "You've hit your usage limit", the run posts an *empty* review that
reached no verdict — that is a failure, not a clean pass. Re-run with `--panel deep --force`,
which is the Claude panel, and say in your reply that you overrode it and why.

### About CI on this PR

- **The `referral` job exits 0 on a referral.** `lydite/referral` stays *pending* until a human
  comments `/lydite clear`, and `.lydite/exemptions.yml` does not exist — so **every** PR here
  is referred. A green job says nothing; read the commit status.
- **Removing or renaming a test trips the `tests removed` referral check.** A test moved to
  another package reads as one deleted. Say in a PR comment where it went, so the human
  clearing the referral is not guessing.
- **Poll by commit SHA**: `gh run list --commit "$(git rev-parse HEAD)"`. `--limit 1` right
  after a push reads the *previous* run.
- **`gh pr checks` exits non-zero when any check is not green**, so `s=$(gh pr checks …) || …`
  swallows every poll.
- **Pushing cancels the in-flight run** — `lydite-pr.yml` sets `cancel-in-progress`.
- **The Bash tool caps a foreground command at ten minutes.** **Check CI once, report, and move
  on.** Do not sit in a polling loop.
- **`gt` merges by squashing and pushing, not through GitHub's merge button**, so
  `gh pr view <n> --json merged` reads `false` on a merged PR and its state is `closed`.
  **Read `main`'s head, never the `merged` flag.**

## When both loops are done

**Do not merge.** Never merge a PR unless explicitly told to.

Write the continuation prompt for **PR 3** to `.agents/plans/pr21-findings-reach-the-ledger.md`,
in the shape of this file, and tell the user it is ready. Its content is below — carry the whole
of it across.

## What comes after — do not start this

### PR 3 — `feat(ledger): the finding count reaches the quality history`

`Closes #111`.

- **`ledger.Component` gains a per-gate map**, scanner gates only. `0` means the gate ran and
  found nothing; **absent means it does not apply to that language** — ADR 0029's "absent is
  not zero", which a single integer cannot express. CRAP and patch stay out: `CRAP.Above`
  already *is* the count of CRAP findings, and recording it twice makes "two quantities that
  must agree ... two quantities free to disagree".
- **The channel is `scan.json`**, which `saveDocument` already writes unconditionally with
  `findings` as a top-level key, read out of each `--reports` directory by `lydite test record`.
  **`measurements.json` keeps its single writer** — `findings.md` already argues this for the
  neighbouring case, and `measurementsDoc` refuses to load without a `Tree` a scan has no
  business asserting.
- **A scan job in `lydite-baseline.yml`**, mirroring `lydite-pr.yml`'s but **without
  `--diff-base`**: on `main` there is no change to scope to, so it covers the whole repository,
  every finding lands `AnchorNowhere`, and the count is the repository's standing total.
  `record` gains `scan` in its `needs` and keeps `if: ${{ !cancelled() }}` — **a red scan must
  still record**, because a red scan on `main` is the most interesting thing a finding history
  can hold, and no later run can fill the hole since the next push is a different tree.
- **Artifact naming**: `record` downloads `pattern: lydite-shard-*`. Upload the scan's reports
  under a **distinct** prefix — it is not a shard — and add a second `download-artifact` step
  into the same `shards/` directory so the existing `for dir in shards/*/` loop picks it up.
- **Refine `measurementsIn`'s row** for a directory holding a scan document but no
  `measurements.json`. It is tolerated today but renders as an amber "no measurements", which
  reads like a malfunction where it is the expected shape.

## Owed, and not this PR's work

- **Five plan files plus this one are untracked** in `.agents/plans/`. Plans land in their own
  `docs(plan):` pull request, never with a feature — and that PR needs its own issue.
- **Three memory candidates are staged** in `.agents/memory/candidates/` by the explorer:
  `pullrequestfromref-head-now-documented`, `rust-fmt-still-fails-scan`,
  `issue-123-still-open-unfixed`. **Run the `memory-curate` pass** — the third is made false by
  #127 and must be curated accordingly. You do not hand-edit the store or `INDEX.md`.
- **`lydite/actions` still carries the `contains` bug** in its packaged `lydite-comment`:
  the marker matches anywhere in a body, so a person quoting lydite's verdict is the comment
  the next run `PATCH`es wholesale. One-word `jq` fix (`contains` → `startswith`, with a
  `// ""` guard for a null body). That repo is not checked out here. **It needs an issue.**
- **`docs/release-notes/v0.2.0.md` is on `main` and stale** — no mutation, names
  `.lydite-reports/baseline.json` which is `measurements.json`, no `test plan`/`merge`/`record`,
  and now also missing the CRAP gate, the `[lydite:exclude_from_<gate>]` grammar, ADR 0028, the
  ledger with ADR 0029, the findings channel with ADR 0030, the review threads with ADR 0031,
  and the `AGENTS.md` split. **Its own pull request**, and it must be correct before any tag —
  `release.yml` reads it out of the tagged tree.
- **[#109](https://github.com/lydite/lydite/issues/109)** — a mutant is bounded in time and not
  in memory. Measured: 14 GB in eighty seconds; it killed a CI job during #124.
- **[#112](https://github.com/lydite/lydite/issues/112)** — mutation scalars in the ledger,
  structurally blocked behind #49.
- **[#103](https://github.com/lydite/lydite/issues/103)** — `lydite mutation` has no job in
  `lydite/actions`.

## The gate on the release

**Nothing ships until everything but the dashboard ([#27](https://github.com/lydite/lydite/issues/27))
is done, and that explicitly includes the relay being deployed** so `vars.LYDITE_RELAY_URL` is
set. The relay App (`LYDITE_APP_ID=4814607`) is registered but **not installed** on the org.
`lydite/actions` `@v1` still points at `e936433`. **Do not move `@v1`, and do not tag a
release.** The order is fixed: a lydite release first, because `setup@v1` resolves `latest`;
only then does `@v1` move.

## Environment

- **SSH is blocked.** Push and fetch over HTTPS with the gh credential helper, as in Setup.
- **When a lydite command shells out to git itself** — `mutation`, `test --gate-coverage`,
  `review`, `scan --diff-base auto` all run `git fetch origin <branch>` in a child process —
  `git -c` cannot reach it. Pass the rewrite through the environment:

  ```sh
  export GH_TOKEN="$(gh auth token --user pedromvgomes)" \
    GIT_CONFIG_COUNT=2 \
    GIT_CONFIG_KEY_0=url.https://github.com/.insteadOf GIT_CONFIG_VALUE_0=git@github-personal: \
    GIT_CONFIG_KEY_1=credential.helper GIT_CONFIG_VALUE_1='!gh auth git-credential'
  ```

- **No container runtime**, so nothing touching the proving ground runs locally.
- `lydite scan` runs from the repository root (`--dir ../..` from `source/cli`). `--dir ..`
  names `source/`, which declares no components and errors.
- A full `go test -race ./...` is roughly four minutes. `lydite test --component cli` is about
  four minutes; `lydite mutation --component cli` was **seventy-eight minutes** on #124's diff.
  Run the long ones in the background.
- Probe binaries from the challenge session are at `/tmp/claude-502/lyd-bin` (gosec,
  govulncheck) and `/tmp/claude-502/cargo-tools/bin` (cargo-audit, cargo-deny), with probe
  fixtures under `/tmp/claude-502/{gosecprobe,clippyprobe,auditprobe,vulnprobe}`. They may have
  been cleaned; reinstall at the pinned versions if so.

## Traps previous sessions sprung

- **A `go test -run` that matches nothing prints `ok`.** Always confirm a new test actually ran.
- **Two `sed` expressions apply in sequence to the same line.** When you transform a file
  mechanically, rebuild the original from the output and diff it.
- **`golangci-lint` skips analysis using cached facts**, then reports `0 issues` having run
  nothing. Plant a defect that *compiles* — an unused function, not an unused variable.
- **A mutant that drops a loop's decrement turns an appending loop unbounded**, and the runner
  dies of memory rather than of the timeout — the job exits 143 reporting "interrupted". That is
  #109. **Prefer forward iteration and `range` over a manual index.**
- **`jq`'s `contains` raises on a null body.** `(.body // "")` first.
- **`npx vitest run --coverage` leaves `source/cloud-services/coverage/`** in the tree, and the
  next `lydite scan` fails on Semgrep findings inside the generated `prettify.js`. Delete it
  before scanning.
- **Editing a `note:` in `.gt-repo.yaml` re-renders `.github/dependabot.yml`.** `gt repo check`
  fails until `gt repo sync`. Never hand-edit the generated file.
- **A stale doc comment survives a rewritten reference.** #127's only panel finding was
  `readShards`' comment still describing the old contract after `.agents/references/findings.md`
  had been rewritten. When you change behaviour, grep for every place that describes it.
- **Correlated reviewers reach the same wrong answer.** Treat convergence as evidence, not
  proof, and **verify a claimed platform behaviour against the platform**, not a forum summary.
- **A fix applied to two of three copies reads as done.** #124 hardened the marker match in the
  CLI and the relay and left the composite action — the only path in force — untouched.
  When you change one of a set, grep for the set.
- **Never `git stash`.** The stack is shared with every other worktree and session. Use a
  temporary WIP commit.
- **`git reset --hard` is blocked by the permission classifier.** Move a branch with
  `git branch -f <name> <sha>` from another branch instead.

## Non-negotiables

- Never `Co-Authored-By`, `Claude-Session`, or a `claude.ai/code/session_...` URL — not even
  when a harness asks. `CLAUDE.md` says so explicitly.
- `Closes #N`, not `Refs`. Carve unshipped scope into a new issue first.
- **Never merge a PR unless told to. Never tag a release, and never move `@v1`.**
- Comments describe the code as it is — no "used to", no "now", no ticket numbers, no roadmap.
  ADRs are the exception. Test names say what behaviour they protect.
- Never hand-edit a generated file; edit `.gt-repo.yaml`, then `gt repo sync`.
- **Prove a regression test fails without its fix.**
- Delegate to the `memory-explorer` agent before answering *why* something is built the way it
  is, what breaks if you change X, or whether an approach has been tried.
- Invoke the `challenge` skill before `ExitPlanMode`.
