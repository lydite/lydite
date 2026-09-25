# Referral: `lydite review`

> **The reference for `internal/referral`, `internal/clearance`, `internal/forge` and `.lydite/exemptions.yml`.**

`lydite review` decides whether a change may merge unattended. Almost nothing in the referral
model itself is a failure: exemptions, disqualifiers and the isolation rule below emit pass or
refer, and a malformed exemptions file or an unresolvable merge-base is an error, exit 1. `review`
runs one check, a public-API diff for every component that opts in with `api_surface` —
`internal/apisurface` for Go, `internal/rustapisurface` for Rust, `internal/tsapisurface` for
TypeScript — (see
[ADR 0040](../../docs/adr/0040-an-undeclared-go-api-break-fails-and-a-declared-one-is-referred.md)
and its amendment, and [`components.md`](components.md)), and that check *can* fail: an undeclared break is a gate,
because the author clears it by not breaking the API or by declaring the break, and both are work
they can do. A declared break refers instead, and a surface that could not be compared refers as
well — see [below](#a-declared-api-break-refers-an-undeclared-one-fails). See
[ADR 0013](../../docs/adr/0013-referral-not-approval.md) for the model and
[ADR 0014](../../docs/adr/0014-evidence-only-referral-matching.md) for what it matches on.

The model is an allowlist and the default is to refer. `.lydite/exemptions.yml` at the scan
root declares shapes of change that merge unattended; with no file, or an empty one, every
change is referred, which is the correct day-one state rather than a broken one.

```yaml
exemptions:
  - name: readme-only
    # reason is required: this file is the entire risk model, and a diff of
    # bare globs is not reviewable.
    reason: >
      Prose in the top-level README changes nothing executable and no build
      step reads it.
    paths: ["README.md"]
disqualifiers:
  # Only ever ADDS to the built-in set. A veto list that can be emptied is
  # not a floor.
  paths: ["infra/**"]
```

Five properties are load-bearing and easy to weaken by accident:

- **The file is read from the merge-base, never from the branch.** A change that widens the
  gate gets no benefit from its own widening — otherwise one pull request declares itself
  exempt. `loadExemptionsAt` does this with `git show <base>:<path>`.
- **All-or-nothing, against a single exemption.** Every changed path must be covered by one
  exemption. Matching on *any* path would let an agent staple a README tweak onto a dangerous
  change; matching on the *union* of two exemptions would mean adding a narrow entry silently
  widens every existing one, destroying `git log .lydite/exemptions.yml` as the readable
  record of widenings that #15 exists to protect.
- **Disqualifiers veto any match, and the built-in set cannot be removed.** A net-new
  suppression annotation, a newly skipped or `.only`-focused test, a removed test — deleted,
  gutted, or renamed out of a test path, since `git mv foo_test.go foo_disabled.go` takes it
  out of the runner's view leaving nothing deleted and no hunk to read — an edit to
  any file under `.lydite/`, an edit to `.github/workflows/`, and an edit to
  `.gitattributes`. gitleaks' own `.gitleaks.toml` and `.gitleaksignore` join that list too,
  matched by base name rather than a repository-root path since the scan root referral runs
  over is not always the repository root: neither file is a token any one line can be checked
  against, so the file itself is the veto. A `.gitleaks.toml` that itself extends another file
  with `[extend].path` is a named, accepted gap — that reference is not resolved, so a
  repository using it must name the extended file in its own `extra.Paths` (see ADR 0035). The
  suppression list carries the whole-file and whole-crate forms
  (`#![allow(`, `#[expect(`, `@ts-nocheck`, `//nolint`, `//go:build ignore`) alongside the
  per-line ones — `gitleaks:allow` among them, clearing a secret finding outright — because the
  broad form is strictly more powerful than the narrow one it would
  otherwise be the only one caught. A ShellCheck directive (`# shellcheck disable=...`,
  `source=`, `shell=`, `extended-analysis=`) is vetoed as a class rather than by any one
  spelling, matched by `containsShellcheckDirective` on ShellCheck's own directive-reader
  prefix — `#`, optional spaces or tabs, `shellcheck`, then a space or tab — because the key
  after it is one ShellCheck could add to at any time, and every one of them narrows what gets
  checked the same way `disable=` does. `.gitattributes` is there because a `-diff` attribute
  replaces a hunk body with `Binary files ... differ`, and git reads that attribute from the
  branch — the diff is passed `--text` so the trick does not work, and the edit is referred
  anyway.
- **Everything matched on is evidence off the diff.** Nothing an author asserts about their
  own change may earn an exemption or clear a disqualifier. The `!` conventional-commit marker
  ADR 0013 held back stayed unimplemented for exactly that reason — nothing detected an
  undeclared API break, so a claim-based veto would have worked only for the author who would
  have declared anyway. The rule for any future addition: an author-controlled claim may add a
  referral, never remove one. The declared-API-break disqualifier below is the first thing built
  to that rule rather than merely stated by it — see
  [below](#a-declared-api-break-refers-an-undeclared-one-fails).
- **A change that edits the exemption set may edit nothing else**, and this is the one thing
  `review` reports as a *failure* rather than a referral: the author clears it by splitting the
  change in two, which is work they can do, and that is exactly what separates a gate from a
  referral. Two properties already protect the file — the merge-base read, so a change gets no
  benefit from its own widening, and the disqualifier, so such a change is always referred —
  and neither closes the realistic attack, which is not a forged exemption but an unremarkable
  one riding along in a large change approved for its other contents. What isolation buys is
  that `git log .lydite/exemptions.yml` becomes the complete, reviewable record of every
  widening. `.lydite/config.yml` deliberately carries no such requirement: report paths change
  alongside code for honest reasons, and a rule that fires on ordinary work gets relaxed later.
- **An absent exemptions file and an unreadable one are different questions**, asked
  separately: `git cat-file -e` answers whether it is there, and only then does `git show` read
  it. Collapsing them would make a broken read indistinguishable from an empty allowlist, which
  is safe only while the allowlist *is* empty and the safe answer happens to coincide.
- **The diff fails closed when it cannot be read.** A patch line longer than the scanner's
  buffer — `--text` renders a binary blob or a minified bundle as one — aborts the run rather
  than yielding an empty set of added lines, which would leave every content veto silent while
  the path list stayed complete.
- **Every git setting that decides what a diff says is pinned** (`core.quotePath`,
  `diff.relative`, `diff.mnemonicPrefix`). Each is settable in a global gitconfig lydite does
  not control, and `diff.relative` alone scopes the diff to `--dir` and strips the prefix from
  what survives — dropping `.github/workflows/` out of a monorepo's paths entirely.
- **The diff covers the whole repository, not just `--dir`.** Unlike `internal/coverage`, which
  scopes its diff with git's `--relative`, referral decides whether a *pull request* needs a
  human, and a workflow edit outside a monorepo's scan root is exactly what must not slip past.
  `--dir` only locates the exemptions file.

Path patterns are **repository-root-relative and anchored**: `README.md` matches the README at
the repository root and nothing else, any-depth matching is spelled `**/README.md`, and a
monorepo scanned with `--dir source` writes `source/README.md` — the diff is not scoped to
`--dir`, so the paths carry no prefix stripped. This parts company with gitignore on
purpose — floating patterns are right for a skip list where over-matching is free, and wrong
for a list that decides what merges without a human. The syntax itself is
`internal/pathmatch`, shared with the orphan gate's excludes; which root a pattern is relative
to belongs to each caller, and only referral's is the repository root. Unknown YAML keys are rejected rather
than ignored, for the reason `config.validateLinter` rejects `linter: eslint`.

The verdict is computed from `<merge-base>..HEAD`, so the local answer and the CI answer come
from identical inputs. A dirty working tree gets its own row saying the uncommitted work was
excluded, because silently deciding on HEAD while the developer is looking at edited files is
the one way this command gives a confidently wrong answer.

## A declared API break refers, an undeclared one fails

`review`'s `addAPISurfaceRows` (`cmd/lydite/review_apisurface.go`) is the one check in the
command, and it renders three verdicts for a component that opted in with `api_surface`,
whichever language it compares: the comparison itself is `internal/apisurface`'s
`golang.org/x/exp/apidiff` for Go, `internal/rustapisurface`'s pinned `cargo-semver-checks`
subprocess for Rust, and `internal/tsapisurface`'s pinned `@microsoft/api-extractor` subprocess
for TypeScript — see the ADR's amendments for those two halves — but the verdicts
`addAPISurfaceRows` draws from any of the three results are the same three:

- an undeclared incompatible change is `ui.StatusFail` — the author clears it by restoring the
  API or by declaring the break, and both are work they can do;
- a declared incompatible change is `ui.StatusRefer` (`referral.DisqualificationAPIBreakDeclared`)
  — every breaking change should reach a person, and the disqualification is what makes it one;
- a surface that could not be built or loaded — the base tree fails to build, a Go module path
  moved between the merge-base and this change, a Rust crate has no library target — is also
  `ui.StatusRefer` (`referral.DisqualificationAPISurfaceUncomputable`), because `review` genuinely
  cannot tell a break from no break and neither pass nor fail would be true. See
  [ADR 0040](../../docs/adr/0040-an-undeclared-go-api-break-fails-and-a-declared-one-is-referred.md)
  for why the third verdict exists rather than one of the first two standing in for it, and
  [the rule](../rules/a-gate-that-could-not-run-never-renders-as-one-that-passed.md) it follows.

Both API-surface `Disqualification` kinds are the first ones `internal/referral` carries that are not
derived from the line-level diff evidence `Disqualifications` computes everything else from.
`internal/referral` still imports nothing about git history or webhooks — it never reads a
commit message or a PR title itself. `breakDeclaration`, in `cmd/lydite`, is what reads the
declaration (through `declaration.Declared`, over a title the caller resolves and over every
commit in `base..HEAD` from `internal/gitstate.CommitMessages`) and hands `review` only the
resulting `referral.Disqualification` to append. The title itself is resolved differently by
each caller: `review` reads `pull_request.title` off the webhook payload
(`internal/forge.PullRequestEvent`), while `clearance` resolves it live through
`client.PullRequestTitle` (`GET /pulls/:n`), because an `issue_comment` payload carries no
`pull_request.title` of its own. The fact of the declaration crosses the package boundary
already decided; the evidence-only computation inside `internal/referral` never touches it.

The declaration is a claim, not evidence, so it obeys the rule two paragraphs up: it may only
ever add a referral, never remove one. It can never clear the undeclared-break gate — a
disqualification adds a referral and has no power over a gate at all — and it is checked once
per run, independent of whether any component opted in. A change that declares a break refers
even in a repository with no `api_surface` component and nothing compared, because the claim
needs no corroboration from the surface diff to be worth a person's attention.

This refers rather than gates only because the version is not chosen yet. At tag time the same
declaration is read again, over the commit range a release closes, but as a hard gate rather
than a referral — a version number now exists to check the claim against. See
[ADR 0045](../../docs/adr/0045-a-tag-that-is-not-the-breaking-bump-cannot-carry-a-declared-break.md)
and [`ci.md`](ci.md) for `lydite release check`.

Computing any of this costs `review` three things it otherwise has no reason to pay for:
loading components, resolving a Go toolchain, and materialising the merge-base as a real tree on
disk, since `go/packages` loads a module by running the `go` tool over one and `git show
<base>:<path>` cannot supply that. `baseWorktree` in `review_apisurface.go` does the last of
those with `git worktree add --detach` and a `context.WithoutCancel` cleanup — the same shape
`cmd/lydite/coverage.go`'s `measureBaseTree` already uses, kept as its own separate
implementation rather than shared, since `coverage.go` is not this feature's file to touch. A
repository where no component opts in pays none of it: `addAPISurfaceRows` returns before
loading a toolchain or adding a worktree once it finds no component asked, the same "day-one
state" an absent `.lydite/exemptions.yml` gets above — nothing was asked for, so there is
nothing here that could fail to run.

## An added dependency refers

`review`'s `measureDependencies` (`cmd/lydite/review_depdelta.go`) compares, for every
dependency manifest the change touches, the packages the merge-base pins against the ones HEAD
pins — both sides read with `git show <rev>:<path>`, never off the working tree or a worktree,
since `internal/depdelta`'s readers only need a manifest's bytes. `addDependencyRows` turns
that comparison into rows and disqualifications:

- a package present at HEAD and absent at the base is `referral.DisqualificationDependencyAdded`,
  direct or transitive alike — a version move on a name already present is not an addition, and
  a removal never disqualifies on its own;
- a manifest whose set could not be built at either side — an ecosystem with no reader (yarn,
  pnpm, pip are named and not read) or content that did not parse — is
  `referral.DisqualificationDependencyDeltaUnmeasured`, because "lydite does not know whether
  this added a dependency" and "it did not" must not read as the same verdict;
- a manifest compared and found to add nothing gets its own `dependencies(<path>)` pass row, so a
  manifest the report is silent about is never confused with one nothing was measured over.

Which paths are manifests is sniffed from the diff by `depdelta.Detect`, never from a
component's declared lockfile: the question is "does this path name a manifest, and of what
kind", which the path answers on its own, and reading component declarations would miss a
manifest no component named. This check runs unconditionally on every `review`, with no
per-component opt-in — unlike `api_surface`, every repository with a dependency manifest is
covered from the day this shipped. See
[ADR 0047](../../docs/adr/0047-an-added-dependency-refers-and-a-version-bump-is-conditionally-exempt.md).

`measureDependencies` runs once per `review` invocation and its result, `[]manifestDelta`, feeds
both the rows above and the `versions: patch-and-minor` condition below — one comparison
answering two questions, so a manifest is never read off two trees twice for two different
callers.

## A version bump is exempt only on a condition

An exemption may carry one condition, `versions: patch-and-minor`, and `patch-and-minor` is the
only value it accepts — anything else is rejected by name at parse time, the stance
`dec.KnownFields(true)` already takes toward an unknown key. Unset is no condition at all, which
is the path-only behaviour every exemption written without one relies on. See
[ADR 0047](../../docs/adr/0047-an-added-dependency-refers-and-a-version-bump-is-conditionally-exempt.md).

**The condition is a further test one matched exemption must pass, never a term in a
disjunction across several.** `Decide` asks `Covers` of a single exemption first and only then
its condition, so an exemption requiring nothing and an exemption requiring `patch-and-minor`
cannot be combined into a union that requires neither — the union rule the `Exemption` doc
comment states, preserved by the ordering rather than by a second check. A covered change whose
condition goes unmet lands exactly where an uncovered one lands, referred, and
`Decision.Unsatisfied` names the exemption so the report can say which of the two happened:
"nothing declares this shape" and "the shape is declared, conditionally" have different
remedies.

Two halves have to hold together, and `review.go` computes both:

- **every version pair in every manifest the change touches moved by a patch or a minor**
  (`versionsPatchAndMinor` over the `manifestDelta` values `measureDependencies` already built
  for the added-dependency disqualifier, through `depdelta.Delta.PatchOrMinorEligible`). A
  manifest that could not be measured fails it for the reason it also disqualifies, and a
  `0.x` line is not boring the way a `1.x` one is — the classification lives in
  `internal/depdelta`;
- **the licence gate and the advisory check ran and passed for every component**
  (`dependencyGatesPassed`), read out of the `scan.json` in each `--reports` directory. A clean
  SCA run is evidence about advisories and nothing else, and the bump that introduces a
  copyleft dependency is precisely a lockfile-only change with a clean SCA run, so the licence
  gate must have **run and passed** rather than merely not failed.

A scan document says what ran, not what a component is, so a row's label is taken apart into
gate and component (`licence(cli)`, `govulncheck(cli)`) and the gates a component's rows carry
are what name its language: `gosec` implies `govulncheck`, `cargo clippy` implies `cargo-audit`.
TypeScript runs no advisory check, so an npm component's dependency evidence is its licence row
alone — requiring one of it would make the condition unsatisfiable for every repository that has
a TypeScript component.

**Without `--reports` the condition can never be satisfied.** No directory, no `scan.json` in
the ones given, and a document that will not parse are all the same answer: the flag can only
ever supply evidence, so its absence is the absence of the exemption and the change is referred
exactly as it was before the flag existed. Every local run is that case.

`internal/referral` learns none of this. It takes a `referral.Evidence` — one boolean,
`PatchAndMinor` — beside the change and the file, the same separation the API-break declaration
has: `cmd/lydite` measures, and the package is handed only the verdict. A caller that measured
nothing passes the zero value, under which every condition fails. Evaluating the condition
inside the package would make an exemption's meaning depend on what that package could reach at
the moment it ran, and would put git, report documents and component loading behind a function
whose whole value is that it has none of them.

**The `referral` job needs `scan` and reads its artifact.** `.github/workflows/lydite-pr.yml`'s
`referral` job depends on `scan` and downloads its `lydite-reports-scan` artifact, passing
`--reports` to `lydite review` when `scan.json` is actually there — `scan` skipping on a
title-only edit, or failing outright, still lets `referral` publish a verdict, just without the
licence and SCA evidence a `versions:` condition needs. This is added latency on every pull
request (`referral` no longer answers as soon as `setup` does) and one more artifact dependency
to keep correct, which ADR 0047 records as a cost asked about and approved before it was built.

# Clearance: `/lydite clear`, `explain` and `exempt`

A referral is resolved by a person commenting on the pull request, never by the author
pushing more code. `lydite clearance` is that surface and `internal/clearance` is its
decision; `internal/forge` is the only thing that talks to the platform. See
[ADR 0015](../../docs/adr/0015-clearance-binds-to-a-commit.md).

`review --publish` records the verdict as the **`lydite/referral` commit status** and as
the pull request's standing comment. The status is the whole record of the verdict: it
stands on that context at one commit, and nothing else stores it. A clearance is its own status,
**`lydite/clearance`** (`clearance.ClearanceContext`), written under different authority from the
verdict, and `clearance.Decide` reads the referral status to decide whether there is anything to
clear. **A clearance records both statuses on the head, by either route**, `lydite/clearance`
first so a partial failure never leaves a green referral with no clearance record: a repository
requiring `lydite/referral` unblocks and a second `/lydite clear` answers already-passing. The
direct post (no `--status-out`) posts `lydite/clearance` and then `lydite/referral` = success, and
a failure of either post fails the run. The rendered route writes two documents, each a single
`forge.Status` object: the `lydite/clearance` status at the path `--status-out` names, and the
`lydite/referral` status at the sibling with `.referral` before that path's extension —
`lydite-status.json` beside `lydite-status.referral.json` (`referralDocument` in
`cmd/lydite/status.go`). The split is what the relay's authority model requires: a clearance ref is
admitted to `lydite/clearance` alone, so only the clearance document is ever relayed and the
referral document is posted by the workflow with its own `statuses: write` token. The path is
derived rather than configured, so a caller cannot render a clearance without rendering the
referral it resolves. `--status-out <file>` on `review --publish` renders that command's single
`lydite/referral` document; a document that cannot be written fails the run on either command.

`review --surfaces <path>` reads a comparison `review compare` already made — the raw
per-component findings, and the base they were measured against — instead of running it
again, and decides and publishes from that document through the same `publish`/
`stateFor`/`describe` (`cmd/lydite/status.go`, with `referralStatus` building the document) as a single, ordinary invocation would.
See [ci.md](ci.md)'s `referral`/`referral-publish` split: `review compare` is the only
half of this that runs a component's own code, and it computes no exemption match, no
declaration and no verdict — the decision is made afterward, in a job that never ran
that code and reads only text (exemptions, the diff, a declared breaking change) from
its own separate checkout. `reconcileSurfaces` refuses a document whose claimed base
does not match what that job resolves itself, and requires a result named for every
component the tree says opted in — but a claimed-clean result's own content is not
independently verified, so a process left running past the comparison's own subprocess
call could still forge one. See ci.md for what this leaves open.

Six properties are load-bearing:

- **A clearance names one commit.** Not the tree, and not the shape of the verdict. Any
  push produces a new head carrying no status, so the clearance evaporates with no state
  of its own — including after a rebase that changes nothing. Both alternatives require
  lydite to hold that two revisions are the same change, and a clearance resting on that
  inference is one the inference can be wrong about.
- **A referral publishes `pending`, never `failure`.** A required check blocks on
  anything but `success`, so this softens nothing; it is the accurate word, and a gate
  fails where a referral does not. `stateFor` is the single place that mapping lives.
- **The status is read before it is written.** Re-running `review` after a clearance
  comment cannot change the answer — a referral re-evaluated is still a referral — so the
  only thing recomputation would buy is telling a referral apart from the isolation gate,
  and the standing status already carries that. Reading it means a `failure` is not
  clearable by comment, and that no pull-request content is fetched at comment time.
- **The commenter must have push permission**, read from the platform rather than from
  the comment. The repository is public, so without it any account could clear anything.
  This is a floor and not the whole trust — whoever holds the credentials satisfies it,
  and that gap stands: closing it needs a factor bindable to the revision being cleared,
  which a standard authenticator code cannot supply — see
  [ADR 0052](../../docs/adr/0052-an-authenticator-code-cannot-bind-a-clearance-on-its-own.md).
- **A head that moved after the comment is refused.** A status created after the comment
  cannot be one the person read. Both timestamps are the platform's, so neither is the
  author's to set. `/lydite clear <sha>` names a revision explicitly and overrides the
  ordering, since naming it is the stronger statement.
- **Only the first line of a comment is parsed.** A reply that quotes an earlier comment
  carries its text, and scanning the whole body would let a quoted `/lydite clear` clear
  a change nobody meant to.

`.github/workflows/lydite-clearance.yml` runs on `issue_comment`, so it always executes
the **default branch's** copy and the deciding logic is never the pull request's to edit.
Nothing in it checks out the pull request's code, and nothing in it may start to: the job
holds a writing token. Its job-level `if:` is evaluated before a runner is allocated, so a
comment not addressed to lydite costs nothing rather than costing a short job.

**The status blocks no merge yet.** Making it a required check is one field on a ruleset
`gt` renders and hardcodes to `ci-gate` alone, so it cannot be set from this repository —
see [#34](https://github.com/lydite/lydite/issues/34). Until then the loop is published
and flipped correctly and enforces nothing, which is a temporary state rather than the
design.

## `/lydite exempt <shape>` proposes an entry and lands nothing

The third verb answers with a paste-ready `.lydite/exemptions.yml` entry and writes
nothing: no status, no edit to the standing comment, no commit. `name` is the `<shape>`
the commenter typed; `paths` is the change's own **uncovered** set, answered by a
`clearance.Uncover` callback the caller passes into `Decide` — `Decide` calls it only
from the one branch that has already passed every other gate, so a command refused for
permission, a stale revision, a missing verdict or a moved head never pays for reading
the exemptions file or asking the platform what the pull request touched. The callback
itself runs `referral.Uncovered` over the pull request's changed paths against the
exemptions file in the clearance job's own checkout; `reason` is a placeholder stating
the question a person has to answer. Landing the entry is an ordinary pull request
somebody opens, reviews and merges. See
[ADR 0049](../../docs/adr/0049-exempt-proposes-an-entry-and-lands-nothing.md).

**Which exemptions file that is comes from `--dir`, as it does everywhere else.**
`lydite clearance` takes the same flag `review` does, and `uncoveredPaths` locates the
file at `path.Join(referral.RootRelative(ctx, dir), referral.FileName)` — the scan
root's own path inside the repository, which is the `source/.lydite/exemptions.yml`
shape `referral.IsExemptionsPath` supports. Reading the fixed path relative to the
process's own directory finds nothing there, and an absent file is the day-one state:
every changed path comes back uncovered and the proposal covers the whole change under
a headline calling those the paths no declared exemption covers. The read is off the
working tree rather than out of a commit, unlike `review`'s `loadExemptionsAt`, because
this job's checkout *is* the default branch — there is no branch here whose own
widening it could read.

Nothing a commenter writes may widen what the block says. A glob argument was rejected
for exactly that — it lets an author propose `docs/**` off a change that touched one
README — and it is worse here than in a hand-written entry, because the block reads as
lydite's output and the reviewer who would have interrogated a colleague's glob accepts
the same glob from a tool.

**A derived path is glob-escaped too**, because a `paths:` entry is a `pathmatch` pattern
and a filename is not. `cmd/lydite/clearance.go`'s `escapeGlob` prefixes `\`, `*`, `?`, `[`
and `]` with a backslash in every path before `proposalYAML` encodes it, so an entry matches
exactly the file it was derived from: without it, `app/[slug]/page.tsx` proposes a character
class covering `app/s/page.tsx` and missing its own file, and a file named `**` proposes the
pattern covering the whole repository under a headline saying it covers what the change
touched. `path.Match` — which `pathmatch.Match` calls per segment — reads `\` as escaping the
rune after it, and a `**` segment escapes to `\*\*`, which is not the string `pathmatch`
special-cases as the many-segments wildcard.

**The `reason` is a question, and the marker in it is reserved.**
`referral.ReasonPlaceholderMarker` is the literal `TODO(lydite):`, and
The exemptions-file parser rejects any reason carrying it *anywhere* — a reason that keeps the
question and prefixes a sentence to it has answered nothing. The check is in
`internal/referral` rather than in the generator because copying the block into a pull
request is a route lydite does not control, and `validate` is the one place every route
already passes through. A templated real-sounding reason was rejected outright: prose
that already reads review-quality is prose a skimming reader accepts, which defeats the
requirement that somebody actually thought about it.

**`forge.Client.ChangedPaths` is the one thing the comment surface asks about the pull
request itself**, and it returns names rather than content — nothing is compiled, nothing
beyond the strings is parsed, no tree is materialised. That is what keeps it on the right
side of ADR 0015's "no pull-request content is fetched at comment time", which exists to
protect a job holding a writing token. A rename contributes both of its names, the rule
ADR 0014 sets for the diff and for the same reason. A proposal carries no `versions:`:
the evidence behind it is a full `review` run's, no endpoint holds it, and reading a
manifest off the head is the line falling the other way.

**An empty uncovered set refuses rather than proposing.** Every changed path is already
covered by something and the change is referred regardless, so there is no path list that
would make it exempt — proposing its full set would be an entry duplicating others and
widened by their union. The refusal deliberately does not say *which* cause it is: a
path-only computation cannot tell "covered between several exemptions, by none alone"
from "covered, and a disqualifier vetoed the match", because `exempt` never computes
`Disqualifications` at all. It names the standing comment as where the answer is.

The pull-request comment follows `docs/design/reference/surfaces.dc.html`: verdict badge,
one-sentence headline, a `Check / Head / Base` table, named list sections, and a footer
rule. Which facts fill the columns is lydite's to choose — a referral has no
measurements, so the head column carries what the change contains and the base column
what was read out of the merge-base. The design's footer also claims parity with the
reader's local run; **nothing can establish that yet** (see #27), so it is absent rather
than asserted. `ui.Marker` identifies the standing comment for editing, by a marker in the
body rather than by author, because the author is whoever's token posted it.

## A clearance carries forward across a merge queue

A repository with a merge queue and a required `lydite/referral` deadlocks on every referred
change: a queue entry replays the change onto a fresh `main`, so its commit is by construction a
revision nobody cleared and nobody can — a `gh-readonly-queue/` ref belongs to no pull request,
so there is no comment surface `/lydite clear` could even be typed at. See
[ADR 0053](../../docs/adr/0053-a-clearance-carries-forward-when-the-decision-it-was-given-for-is-unchanged.md)
for the reasoning; this section is the mechanism it lands.

The move is to stop treating a clearance as a statement about a tree and start treating it as a
statement about a decision, one that carries forward exactly when the decision is unchanged.
`internal/referral.Decide` refers for one of two reasons — an uncovered path (`Uncovered`) or a
disqualifier vetoing an otherwise-matching exemption (`Disqualifications`) — and both are pure
functions of the diff and the exemptions file, with no tree identity in either. So the queue-time
question is never "is this the same tree"; it is "does `Decide`, recomputed on the queue's own
tree, refer for the same reasons it referred for when a person cleared it."

**`referral.Fingerprint(uncovered []string, disqualifications []Disqualification) string`**
(`source/cli/internal/referral/decide.go`) answers that question with a short, deterministic hash
over both arguments together — never `Uncovered` alone. `Uncovered` is only populated on the
branch where no single exemption covers every path; a referral caused purely by a disqualifier —
a net-new suppression, a disabled test, an edit to `.lydite/exemptions.yml` itself — leaves
`Uncovered` empty, indistinguishable by that value alone from "fully exempt." Hashing both closes
that: a disqualifier-only referral fingerprints differently from a fully-exempt change, even
though both leave `Uncovered` empty. Neither slice arrives sorted from `Decide` — `Uncovered`
walks `ch.Paths` and `Disqualifications` walks the diff's own order — so `Fingerprint` sorts a
copy of each before hashing, making the result independent of the order either arrived in. A
disqualification's identity for this purpose is `Kind` and `Path` alone, matching
`disqualify.go`'s own doc comment; `Evidence` is the text of what was found, not part of what a
disqualification *is*, and is not hashed. The hash is short and not cryptographically sensitive —
it exists to detect the same reasons recomputed on a different tree, not to resist a deliberate
collision.

**`clearance.WithFingerprint`/`clearance.FingerprintIn`** (`source/cli/internal/clearance/decide.go`)
carry the fingerprint on the one thing that already outlives the commit it was computed for: the
`lydite/clearance` status description, keyed by SHA forever regardless of which branch still
points at that commit, so no new durable store is needed. `WithFingerprint` composes the
description a clearance is recorded under — who cleared what, then the fingerprint of the
decision they cleared — as an `" [fp:<value>]"` suffix, budget-checked against
`clearance.DescriptionLimit`, the platform's 140-character cap on a status description. The human
half gives way when the two do not fit: a cut fingerprint is a value that compares unequal to the
decision it was taken over, so the description's leading text is truncated with an ellipsis
before the fingerprint is appended, never the other way round. An empty fingerprint appends
nothing, which reads back as absent. `FingerprintIn` is the reverse read, and its second return
value is the reason it exists as a pair rather than returning a bare string: it distinguishes a
description that carries no fingerprint field at all — every clearance recorded by a lydite that
wrote none, and any description the platform truncated past its closing marker — from one whose
field is present but empty. Both are unusable for a comparison, and both have to be refusable as
such rather than silently read as a match.

**`lydite clearance queue`** (`source/cli/cmd/lydite/mergequeue.go`, `newQueueCmd`/`runQueue`) is
the merge_group-aware CLI path. It answers a `merge_group` event payload rather than a pull
request: it reads the queue ref's own `QueueEntry` (`internal/forge.MergeGroupEvent.QueueEntry`)
for the originating pull-request number, resolves the base commit the same way `review` does,
recomputes `referral.Decide` against the queue's own tree and — deliberately — the base branch's
*current* exemptions rather than any pinned revision, and derives the fingerprint from the
result. `queueDecision` calls `referral.Decide` alone — never `measureDependencies` and never
`computeAPISurfaces` — and hands it the zero `referral.Evidence{}`, under which every `versions:`
condition fails: no dependency-delta comparison and no API-surface comparison runs here at all.
That is deliberate scope, in the same spirit as why `review`'s own `computeAPISurfaces` gates a
component's comparison behind `guardCredential` at all — that guard exists to keep a component's
untrusted build code (a Rust crate's `build.rs`, a TypeScript package's lifecycle scripts) out of
a process about to publish with a writing credential. This job holds no writing credential to
protect in the first place, but it also has none of the toolchain provisioning or worktree
machinery `review` sets up to run that comparison safely, so rather than reimplement any of it
here, it runs none of it and lets `Evidence{}`'s zero value say so. Passing nothing is the
direction that refers: an entry whose clearance was given against a `versions: patch-and-minor`
condition, or against a declared API break, that held at pull-request time therefore fingerprints
differently at queue time and goes back to a person rather than being silently trusted forward.
Measuring either at queue time is worth doing and is not this path's to do — see the referral
job's own `--reports`, in `queueDecision`'s own doc comment. The command then submits a
`queueRequest` (`QueueRef`, `PullRequest`, `SHA`, `BaseSHA`, `Fingerprint`, `Referred`) to the
relay, minting its own Actions OIDC token for the relay's origin — it holds no writing token by
design, since it is the job that parses diff evidence and the relay is what writes, the same split
every other relay-fronted job keeps. `Referred` is `decision.Referred`: whether the recomputed
decision refers at all, which is what lets the relay answer an unreferred entry without reading
any clearance.

**`pr-relay`'s `POST /merge-group`** (`source/cloud-services/pr-relay/src/index.ts`) is the one
relay route that composes a verdict rather than relaying a caller's own. `queueOutcome` gives
three answers, ordered by what each has to establish first. An entry whose submitted `referred` is
`false` is published `success` immediately and reads no clearance at all: a clearance is only ever
given against a referral, so for a change every exemption covers there is none to compare against
and nothing to wait for, which is the commonest change there is and the deadlock ADR 0053 exists
to remove. That answer speaks for the whole queue commit even when the platform batched it, since
the decision behind it was recomputed over the queue's own tree against the base tip and every
batched change is inside that diff. Absence is not that claim — a payload saying nothing about
`referred` is compared, and a `referred` that is not a boolean is refused outright.

A referred entry is the path that needs a clearance, and the route resolves the *pull request's own
head*, live, through `GET /pulls/:n` with the installation token — never the queue ref's own
embedded SHA. That SHA (`base_sha` in the `merge_group` payload, and the trailing revision in the
ref's `pr-<n>-<sha>` segment) is the base branch's tip the entry was replayed onto, not the pull
request's head; reading it as a head is a common misreading the route is written to avoid entirely
by never touching it for that purpose. With the real head in hand, `queueBatching` establishes
that the queue commit carries this pull request's change and nothing else — a clearance speaks for
one pull request, so a batched entry answers `pending` before any clearance is read, as described
below. Only then does the route read the `lydite/clearance` status standing on that head
(`standingStatus`) and — before trusting it as evidence at all — check that status's own
`creator.login` equals the lydite App's bot identity, `lydite[bot]`
(`APP_STATUS_CREATOR`). This is not the same check as matching the `lydite/clearance` context
name: a context name is not evidence of who wrote it, and a fingerprint embedded in a description
is a hash of the change's own diff that anybody holding `statuses: write` could recompute and
paste into a forged status, clearing their own entry. The creator check is now its own rule, not
just this paragraph's reasoning — see
[`agentic/rules/a-status-read-back-as-authority-must-be-checked-against-its-own-creator.md`](../rules/a-status-read-back-as-authority-must-be-checked-against-its-own-creator.md).
Only once the status is both `success` and App-authored does the route compare the recorded
fingerprint against the submitted one (`queueVerdict`) and publish `lydite/referral` at the queue
revision: a match publishes `success`, carrying the original clearer's own attribution forward
rather than composing lydite's own, because the decision they judged is unchanged and the
judgement is still theirs; a mismatch, an unusable fingerprint, an unauthored clearance, or no
clearance at all publishes `pending` naming why — **never `failure`**, because `clearance.Decide`
refuses to accept a clearing comment against a `failure` status, and publishing `failure` here
would make a mismatched queue entry permanently unclearable and reopen the exact deadlock this
mechanism removes. The entry then drops out of the queue exactly as any required check still
pending would, and the author clears the pull request again to re-enter it.

**`/lydite clear` computes and embeds the fingerprint, at full `review` parity.**
`cmd/lydite/clearance.go`'s `applyAction` (`KindClear` branch) calls `clearedFingerprint`, which
recomputes `clearedDecision` — the same evidence `review` decides on: `referral.Changes` against
the resolved base, the base commit's own `.lydite/exemptions.yml`, the break declaration (title
and commit messages), the API-surface comparison of every opted-in component, and the dependency
delta of every manifest the change touches — and hashes `Uncovered`/`Disqualifications` with the
same `referral.Fingerprint` `review` and `clearance queue` both use.
[ADR 0057](../../docs/adr/0057-a-clearance-computes-its-fingerprint-in-a-job-holding-no-credential.md)
closes [lydite/lydite#254](https://github.com/lydite/lydite/issues/254) this way, having rejected
the narrower "paths and disqualifiers only" fingerprint discussed there as one that would not
describe the decision a person actually cleared.

Three things make this safe to compute inside the job that answers the comment, which holds
`statuses: write` and `pull-requests: write` on every invocation of `clearance` (unlike `review`,
whose in-process fallback is guarded only when the run also publishes):

- **`checkoutIsHead`** refuses before resolving anything else when the checkout is not at the
  revision being cleared — a job answering a comment is free to have checked out the default
  branch, and a decision recomputed there is not the decision anyone was asked about.
- **The in-process API-surface comparison is guarded unconditionally.** `clearedDecision` calls
  `computeAPISurfaces(ctx, cmd, opt.dir, baseSHA, true)` — the `guardCredential` argument is always
  `true` here, never conditioned on `--status-out` the way `review`'s own fallback is, because
  `runClearance` requires `GITHUB_TOKEN` on every invocation regardless of whether that invocation
  also posts. **`--surfaces <file>`** (mirroring `review`'s own flag) is the route around the
  guard: it reads a comparison a separate, credential-free job already made with `review
  compare`, reconciles it against the base resolved here through `reconcileSurfaces` (never
  re-running it), and is the only way a component whose comparison runs the change's own code
  (`untrustedBuild`) gets a full-parity fingerprint. Given neither a readable `--surfaces` document
  nor an opted-out component, `uncomputableSurfaces` records the comparison as a disqualification
  naming why — the same "could not run" shape [the rule](../rules/a-gate-that-could-not-run-never-renders-as-one-that-passed.md)
  above requires — rather than silently treating it as clean. The two-job split this enables (a computing job holding no credential, a
  posting job that never re-runs the comparison) lands in `lydite/actions`' `lydite-clearance.yml`,
  not in this repository's own diff.
- **The pull request's title is resolved live**, through `client.PullRequestTitle` (`GET
  /repos/:o/:r/pulls/:n`), never read off the comment payload: an `issue_comment` event's
  `event.Issue.Title` is the comment thread's own title, which is the pull request's only by
  convention, and `review`'s own `pullRequestTitle(eventPath)` has no equivalent to read at clear
  time. A failure to resolve it is a warning, not a fatal error — under-declaring a break can only
  under-refer, never falsely clear one.

A fingerprint that could not be computed at all (an unreadable base, an unresolvable checkout) is
recorded as empty rather than failing the clear: `clearance.WithFingerprint` appends nothing, so
the referral still resolves on this head, and the clearance simply does not carry forward to a
merge-queue entry — named on stderr every time, so a clearance that quietly stopped travelling is
never indistinguishable from one that legitimately re-refers.

**What still does not carry forward.** `queueDecision` (`cmd/lydite/mergequeue.go`) still calls
`referral.Decide` alone against the zero `referral.Evidence{}` — never `computeAPISurfaces`, never
`measureDependencies` — so however faithfully `/lydite clear` computed the fingerprint, an entry
referred for a declared API break or an added dependency fingerprints differently at queue time
and goes back to a person. That gap is unchanged by ADR 0057 and is the referral job's `--reports`
to close, not `clearedDecision`'s. This does not affect an *unreferred* entry (a fully exempt
change) — `queueOutcome` in the relay publishes `success` for those directly, since a clearance is
only ever given against a referral and there is none to carry forward or wait on.

**The merge-queue batching caveat — closed.** A queue ref that groups more than one pull request
into one entry only names the *last* `pr-N-sha` segment (`QueueEntry` in
`internal/forge/event.go`), so the clearance compared against would be the last pull request's
own while the tree the decision is recomputed over holds every earlier entry's change too —
`referral.Fingerprint` hashes only the sets of uncovered paths and `(Kind, Path)`
disqualifications, so an earlier entry whose referral reasons happen to be a subset of the last
entry's own could leave the group's fingerprint equal to the last entry's clearance. The relay's
`queueBatching` rules this out before the clearance is even read: it compares the queue commit's
changed files, from the ref's own embedded base (never the request body's `base_sha`, which the
caller could choose to defeat the check), against the pull request's own changed files from that
same base — the two comparisons run concurrently, neither depending on the other's answer — and
answers `pending` naming the batch on any difference or on an unusable comparison. Changed files
rather than commits, because a squash or rebase merge method changes which commits survive the
replay but not the tree the entry has to hold. And each file's *blob sha* alongside its path, from
the same `files` array `comparedFiles` reads the names out of: a path list alone does not identify
a change, so an earlier entry editing only files the last pull request also edits produces an
identical path list at different content — and since `Fingerprint` hashes paths and
disqualification kinds rather than what any line says, it agrees there too. A path-only comparison
would therefore carry the clearance forward on content its clearer never saw for precisely the
batch this check exists to catch. Two comparisons agree only when they name the same files at the
same blobs; a file the platform reported with no blob sha makes the comparison unusable, which is
the same `pending` as no comparison at all.

