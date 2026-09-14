# Session prompt — the pull-request surface, and the identity that posts it

> **Not a step of the component platform.** This slice sits between step 7 and
> step 8: nothing lydite scans, measures or gates reaches a pull request, and
> nothing records the identity that would post it. Every gate built across steps
> 1–7 is invisible to the person whose change it is about. Step 8
> (`pr8-plan-and-merge.md`) is deferred behind it, because a planner and a merge
> step both have to feed a surface that publishes results.

Closes [#62](https://github.com/lydite/lydite/issues/62) and
[#64](https://github.com/lydite/lydite/issues/64). The design is **already
decided** — it came out of a full `challenge` interview, and the decisions are
listed below with the reasoning that produced them. Do not re-litigate them;
they were argued against the alternatives and several reversed the first
proposal.

## Setup

You are in a gt-powered bare repo at
`/Users/pedrogomes/work/repositories-personal/lydite/feature/<worktree>`.
The root `.envrc` scopes `GH_TOKEN` to gh user `pedromvgomes` — read it and use
that user for every `gh` command.

**The Go module moves in this slice.** It is at `cli/` today and becomes
`source/cli/`; run go and golangci-lint commands from wherever it currently is.

**Verify step 7 (#54) is merged first.** It carries scan on components, the
deletion of `internal/detect`, and per-component toolchains.

**Two commits are unpushed on `docs/plan-tracker-crossref`** (`a0dab57`,
`c244c92`) — plan-tracker updates that belong to this slice. Land them with it
rather than separately.

## Read before planning

- `.agents/plans/component-platform.md` — the tracker, and what this slice is
  not a step of.
- [ADR 0009](../../docs/adr/0009-quality-history-storage-and-access.md) — the
  App-over-OAuth argument, `worker/`, and the **deferral of a lydite-operated
  service** that this slice narrowly revises. Read the revision reasoning below
  before touching it.
- [ADR 0010](../../docs/adr/0010-repo-topology.md) — the repo layout this slice
  supersedes, and the reasoning in it that stays true.
- [ADR 0015](../../docs/adr/0015-clearance-binds-to-a-commit.md) — why the
  referral status must stay early.
- `CONTEXT.md` — **Gate**, **Referral**, **Clearance**, **Component**.
- `cli/internal/ui/comment.go`, `cli/cmd/lydite/publish.go`,
  `cli/internal/forge/` — the comment surface that already exists, and the only
  thing that talks to the platform.
- `cli/internal/ui/row.go:80-89` — `Row.Log`'s doc comment says it exists
  *precisely* so a PR comment can link a failing component's output. That is the
  seam this slice was waiting for.

## The decisions, and why

**Identity**

- **Two Apps.** `lydite` — `pull-requests: write`, `statuses: write`; the
  visible bot on every comment. `lydite-dashboard` — `contents: read` only,
  opt-in, for ADR 0009's dashboard. Posting a comment needs no content read, and
  a tool arguing it holds none of your data must not demand repo-read to write
  one. Two Apps means two private keys in two Workers, so a compromised posting
  relay cannot read anyone's source.
- **The vendor operates the Apps** — the Codecov/Snyk model. Consumers install;
  they never hold a private key. An earlier proposal had each consumer register
  their own App from a manifest; it was rejected because that is not how these
  products work and it pushes key custody onto every consumer.
- **CI authenticates with a GitHub Actions OIDC token, never a credential.** The
  relay verifies signature (JWKS), `iss`, `aud`; takes the repository from the
  `repository` claim; cross-checks the submitted PR number against `ref`
  (`refs/pull/N/merge`); confirms the App is installed; mints an installation
  token server-side; posts; discards.
- **This is what dissolves [#49](https://github.com/lydite/lydite/issues/49),
  and it is the real argument for the App.** With no writable token in CI, a
  single drop-in action that runs the branch's suite *and* comments is safe. The
  branch's code can at worst provoke a wrong comment on its own pull request; it
  cannot write anywhere else. #64's own text does not make this argument — make
  it in the ADR.
- **The `github-token` fallback is required, not a stopgap.** No App installed,
  or the relay unreachable → post with `${{ github.token }}` as
  `github-actions[bot]`. A consumer who has not installed the App still gets a
  comment.

**Surface**

- **One standing comment**, Copilot-shaped: a headline line with icon and
  status, then one collapsible section per concern. `❌ Failed` (scan, test or
  coverage failed) · `⏸ Needs attention` (referral) · `✅ Looks good`.
- **The referral folds into that comment, but its commit status stays early.**
  `lydite-referral.yml` keeps publishing `lydite/referral` on push. A status is
  not a comment: a human can start clearing immediately rather than waiting on
  the test matrix, and clearance stays decoupled from whether CI is green.
- **lydite renders; the action posts.** `lydite publish` is pure — no network,
  no token, runnable locally. The posting step is content-agnostic, so refining
  the comment never needs an actions release. That coupling is what #62 warns
  about and what the current `scan/action.yml` already demonstrates.
- **The artifact is the whole `.lydite-reports/` directory**, not just the JSON,
  so the comment can carry collapsed log tails on failure.
- **No new commit status contexts.** `ci-gate` already carries pass/fail and gt
  hardcodes branch protection to it (#34).

**Layout, secrets, language**

- **The monorepo is rooted at `source/`** — see the layout below.
- **GitHub is the single source of truth for every secret; Cloudflare is a
  replica.** The Cloudflare API token is needed in CI for deployment anyway, so
  holding App keys anywhere else means two stores to rotate and one that
  silently drifts.
- **TypeScript for both Workers.** Consistency is the deciding argument: both
  sign an App JWT, and one language means one implementation of the
  security-relevant part rather than two that agree today. On Rust specifically
  — verified against Cloudflare's docs — `workers-rs` compiles to WebAssembly
  (`wasm32-unknown-unknown` via `wasm-bindgen`), and the docs warn that
  unoptimised Wasm binaries "may exceed Worker bundle size limits or experience
  long startup times". For one JWT verify, one sign and two `fetch`es, where
  WebCrypto is native runtime code, Rust adds cold-start and buys no throughput.
  Zero dependencies follows too: WebCrypto covers RS256 verify *and* sign.

## Two pull requests, in order

**PR A — the restructure.** A pure move, no behaviour change. PR B's diff *is*
the design, and mixed with a ~200-file rename it is unreadable — a mechanical
rename is exactly the change where a real edit hides.

**PR B — the feature.** `Closes #62`, `Closes #64`, on top of A.

### The new layout

```
source/
  cli/                          (was cli/) — the Go module, path stays lydite/lydite
  web/                          (was web/) — the React dashboard
  cloud-services/               one npm workspace
    libs/
      github-app/               App JWT signing, installation tokens, JWKS/OIDC verify
    pr-relay/                   the posting relay Worker
    oauth-exchange/             ADR 0009's dashboard exchange (scaffold only)
docs/  assets/  scripts/  .lydite/  .github/       unchanged at the root
```

**`source/cloud-services` is one component, not three.** CONTEXT.md is explicit
that a component is the unit its build tool treats as a whole — a JavaScript
*workspace* — and that eleven crates behind one `cargo --workspace` are one
component. Declaring the libs and each Worker separately would install and build
the workspace three times.

### What the move touches

- `.gt-repo.yaml` — every Dependabot `directory:` (`/cli` plus the six pin
  directories) gains the `/source` prefix, and a new npm entry for
  `/source/cloud-services`. Then `gt repo sync` and `gt repo check`. **Never
  hand-edit `ci-orchestration.yml`.**
- `.github/workflows/*.yml` — `working-directory: cli`, `go-version-file:
  cli/go.mod`, and `ci-test.yml`'s `--dir ..` → `--dir ../..`. That header
  comment explains at length why the flag is not a `working-directory` override;
  the reasoning survives the move and the value must not be got wrong.
- `.goreleaser.yml` — build paths into `source/cli`.
- `.lydite/components.yml` — `dir: source/cli`; the pin exclude becomes
  `source/cli/internal/**/*-pin/**`; plus the new `cloud-services` component.
  `TestLyditesOwnDeclarationLeavesNothingUnscanned` fails if a pin falls outside
  the glob, which is the guard that catches a missed rename.
- `AGENTS.md` — the Layout section and every `cli/`-prefixed path in it.
- ADR 0010 is **superseded by the new layout ADR, not edited**: its reasoning
  (two repos, why the actions repo is split, why the module path is
  non-fetchable) is untouched and still correct.

## The work in PR B

1. **Documentation first — this is where the reasoning lives.** Three ADRs (the
   App and the relay, including the narrow ADR 0009 revision; one standing
   comment rendered by the CLI; the `source/` layout). `CONTEXT.md` gains
   **Surface** and **Relay** as glossary entries only — no implementation
   detail. Update `component-platform.md` and `pr8-plan-and-merge.md`'s deferral
   note.
2. **`scan` learns to write logs.** `test` already writes
   `.lydite-reports/<name>/test.log` and sets `Row.Log`; `scan` does neither, so
   a failing `gosec(cli)` leaves its findings only in the job log, unreachable
   from a publish step. Tee each check's output to
   `.lydite-reports/scan/<check>.log` *and* keep streaming it live —
   `executil.StreamTo` already takes a writer, so this is an extra sink and the
   stated rationale (a scanner's findings *are* the result) holds. Reuse
   `runner.ReportDir` and the `.gitignore`-writing `test.go` already does.
3. **`lydite publish`.** `--reports <dir>... --out <file|->`. Folds each
   directory's JSON plus the logs it references into one `ui.Comment`; extend
   that type with a collapsible `<details>` section carrying the failing row's
   `Detail` (already the last 40 lines) and a link to its `Log`. Headline from
   `ui.Report.ExitCode`'s existing precedence. Marker `<!-- lydite:results -->`.
   `review --publish` keeps its status write and **stops writing its comment**.
   **Missing input is `unmeasured`, never absent** — a silently dropped section
   is exactly "a gate that could not run must not read as one that passed".
4. **`source/cloud-services`.** `libs/github-app` shared between both Workers;
   `pr-relay` with one `POST /comment` route. Repository from the claim, never
   from the body. A 404 on installation lookup is an answer, not a failure.
   Store nothing, log no bodies.
5. **Two workflows here**, outside the gt pipeline as `lydite-referral.yml`
   already is: `workers-deploy.yml` (`on: push: tags: ['v*']`, sync then
   `wrangler deploy`) and `workers-secrets-sync.yml` (`workflow_dispatch`,
   `wrangler secret bulk`). **Each Worker receives only its own App's key** —
   `pr-relay` never sees the dashboard's, which is the whole point of two Apps.
   Secrets reach `run:` steps through `env:`, never `${{ }}` interpolated into a
   script body; Semgrep's `run-shell-injection` rule has caught that here before.
6. **`lydite/actions` rewrite** — `setup/`, `scan/`, `test/`, `publish/` plus a
   reusable `.github/workflows/lydite.yml`. Closes
   [#47](https://github.com/lydite/lydite/issues/47). `publish/` is the only one
   that authenticates: `id-token: write`, plus `pull-requests: write` on the
   fallback path only.
7. **Dogfood.** The gt stages upload `.lydite-reports/`; a publish stage calls
   `lydite/actions/publish@v1`. `.gt-repo.yaml` grants it `id-token: write` via
   `pipeline.ci.stage_permissions` — **verify the schema with `gt repo config`
   first.** If gt cannot express a stage that runs on failure (`if: always()`),
   publish moves to a `workflow_run` workflow, which also executes the default
   branch's copy and is strictly safer.

## Prerequisites — the user's, not yours

Nothing in the code depends on these until the relay is deployed; the fallback
path works without any of them. Do not block on them, and do not attempt them.

1. Register App `lydite` (org `lydite`) — `pull-requests: write`,
   `statuses: write`, webhook off.
2. Register App `lydite-dashboard` — `contents: read`, callback
   `https://app.lydite.org/callback`.
3. DNS for `lydite.org`; Cloudflare routes `pr.lydite.org` and `app.lydite.org`.
   The domain is registered; there is no DNS yet.
4. GitHub repo secrets: `LYDITE_APP_ID`, `LYDITE_APP_PRIVATE_KEY`,
   `LYDITE_DASHBOARD_APP_ID`, `LYDITE_DASHBOARD_CLIENT_SECRET`,
   `CLOUDFLARE_API_TOKEN`, `CLOUDFLARE_ACCOUNT_ID`.
5. Install `lydite` on `lydite/lydite` and `lydite/proving-ground`.

## What step 7 learned, and what transfers

Step 7's lesson was that **the defect that mattered was invisible to every test
that did not launch a process.** `Env.Activate` was deleted correctly, and every
provisioned toolchain silently broke, because `os/exec` resolves a bare program
name against *this* process's `PATH` when the command is constructed. Clean
build, clean lint, whole suite passing, every argv assertion holding.

- **Run it against a real repository, not a described one.** Clone
  `lydite/proving-ground` and render a comment from its actual reports — it
  carries real findings and a genuinely failing component on purpose. A
  hand-written report fixture agrees with whatever the code does.
- **Run it with something missing.** Publish with the scan report absent, and
  with a report naming a log file that is not there. Both must render
  `unmeasured` rows rather than omit sections.
- **Wire new code to a caller early.** A package nothing imports holds a defect
  through a clean build, a clean lint and a passing suite.
- **Delete the test with the thing it tested.** `review --publish`'s comment
  path goes; its tests go with it.
- **A removal needs a message, not an absence.**
- **The `cd x && cmd` trap fired three times across steps 6 and 7** — silently
  skipping the command after a failed `cd` while the shell reported success from
  a later statement. Use absolute paths, and check that an edit landed rather
  than that a command exited 0. This slice moves the whole tree, so expect it.

## Non-negotiable workflow rules

- Before calling `ExitPlanMode`, invoke the `challenge` skill. The design
  decisions above came from one and should not be reopened, but the
  *implementation* plan has not been challenged.
- Commits and PR bodies must NEVER contain `Co-Authored-By`, a `Claude-Session`
  trailer, or any `claude.ai/code/session_...` URL.
- Use `Closes #N`, not `Refs`. If a PR does not deliver an issue's whole scope,
  file a new issue for the remainder first.
- Never merge a PR unless explicitly told to.
- Comments describe the code as it is, never the change that produced it. No
  "used to", "previously", "no longer", no ticket numbers, no roadmap talk. ADRs
  are the only exception. This is the repo's most-violated rule, and a
  tree-wide move is where it is most tempting to break.
- **Never hand-edit a generated file.** `.github/dependabot.yml` and the gt
  workflows are rendered from `.gt-repo.yaml`; edit the source, then
  `gt repo sync` and `gt repo check`.
- Before proposing a PR: `go build ./...`, `go test -race ./...`,
  `golangci-lint run ./...` from the Go module, all clean. Also
  `lydite scan --dir <root>` and `gt repo check`.
- Ask before editing CI. This slice edits CI heavily; the user has approved that
  in the plan, but confirm the shape before writing.

## Verification

- **PR A's bar is that nothing changed.** Full CI green, `lydite test` reporting
  the same coverage figures as before the move, and
  `go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean`
  producing the same `dist/` layout.
- Force each headline: a failing component, a referral, an all-green run.
- Relay via `wrangler dev` against a stub JWKS: no token, expired token, a
  `repository` claim naming a different repo, and a PR number disagreeing with
  `ref` — all four rejected.
- **The new TypeScript component actually runs here**: `lydite test` reports a
  `cloud-services` row with a coverage figure and `lydite scan` a
  `biome(cloud-services)` row. Confirm on a checkout with no `node_modules` —
  `internal/nodedeps` only executes on a bare tree. This is the repository's
  first TypeScript component, so it is the first run of Biome, the `vitest`
  runner and Node toolchain resolution inside `self-scan` and `ci-test` rather
  than only against the proving ground. Expect it to find something.
- End to end on a real pull request here: the comment appears once, is edited in
  place on a second push, and the failing component's `<details>` carries its
  log tail.
- Fallback: uninstall the App from `lydite/proving-ground`, re-run, confirm the
  comment still appears from `github-actions[bot]`.

## Finally

Update `.agents/plans/component-platform.md` to record this slice as done and to
un-defer step 8, noting what changed that `pr8-plan-and-merge.md` has to account
for: `publish` already accepts N report directories, so the shard matrix is
additive rather than a redesign.
