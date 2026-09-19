# Referral: `lydite review`

> **The reference for `internal/referral`, `internal/clearance`, `internal/forge` and `.lydite/exemptions.yml`.**

`lydite review` decides whether a change may merge unattended. Almost nothing in the referral
model itself is a failure: exemptions, disqualifiers and the isolation rule below emit pass or
refer, and a malformed exemptions file or an unresolvable merge-base is an error, exit 1. `review`
runs one check, a public-API diff for every component that opts in with `api_surface` (see
[ADR 0040](../../docs/adr/0040-an-undeclared-go-api-break-fails-and-a-declared-one-is-referred.md)
and [`components.md`](components.md)), and that check *can* fail: an undeclared break is a gate,
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
  otherwise be the only one caught. `.gitattributes` is there because a `-diff` attribute
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
command, and it renders three verdicts for a component that opted in with `api_surface`:

- an undeclared incompatible change is `ui.StatusFail` — the author clears it by restoring the
  API or by declaring the break, and both are work they can do;
- a declared incompatible change is `ui.StatusRefer` (`referral.DisqualificationAPIBreakDeclared`)
  — every breaking change should reach a person, and the disqualification is what makes it one;
- a surface `apisurface.Compare` could not build or load — the base tree fails to build, the
  module path moved between the merge-base and this change — is also `ui.StatusRefer`
  (`referral.DisqualificationAPISurfaceUncomputable`), because `review` genuinely cannot tell a
  break from no break and neither pass nor fail would be true. See
  [ADR 0040](../../docs/adr/0040-an-undeclared-go-api-break-fails-and-a-declared-one-is-referred.md)
  for why the third verdict exists rather than one of the first two standing in for it, and
  [the rule](../rules/a-gate-that-could-not-run-never-renders-as-one-that-passed.md) it follows.

Both API-surface `Disqualification` kinds are the first ones `internal/referral` carries that are not
derived from the line-level diff evidence `Disqualifications` computes everything else from.
`internal/referral` still imports nothing about git history or webhooks — it never reads a
commit message or a PR title itself. `breakDeclaration`, in `cmd/lydite`, is what reads the
declaration (through `declaration.Declared`, over the pull request title from
`internal/forge.PullRequestEvent` and over every commit in `base..HEAD` from
`internal/gitstate.CommitMessages`) and hands `review` only the resulting
`referral.Disqualification` to append. The fact of the declaration crosses the package boundary
already decided; the evidence-only computation inside `internal/referral` never touches it.

The declaration is a claim, not evidence, so it obeys the rule two paragraphs up: it may only
ever add a referral, never remove one. It can never clear the undeclared-break gate — a
disqualification adds a referral and has no power over a gate at all — and it is checked once
per run, independent of whether any component opted in. A change that declares a break refers
even in a repository with no `api_surface` component and nothing compared, because the claim
needs no corroboration from the surface diff to be worth a person's attention.

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

**The CI edge this needs is designed and not wired.** `.github/workflows/lydite-pr.yml` runs
the scan job and the referral job with no dependency between them; for `review` to read
`scan.json` the referral job has to need the scan job and download its artifact, which is added
latency on every pull request and one more artifact dependency to keep correct. ADR 0047 records
that change as asked about and approved; until it lands, this repository's own referral job
passes no `--reports` and every version bump in it is referred.

# Clearance: `/lydite clear`

A referral is resolved by a person commenting on the pull request, never by the author
pushing more code. `lydite clearance` is that surface and `internal/clearance` is its
decision; `internal/forge` is the only thing that talks to the platform. See
[ADR 0015](../../docs/adr/0015-clearance-binds-to-a-commit.md).

`review --publish` records the verdict as the **`lydite/referral` commit status** and as
the pull request's standing comment. The status is the whole record: a clearance is a
state change on that context at one commit, and nothing else stores it.

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
  which is what [#25](https://github.com/lydite/lydite/issues/25) closes.
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
design. `/lydite exempt` is likewise held back, in
[#33](https://github.com/lydite/lydite/issues/33), because what it should emit is
undecided rather than unbuilt.

The pull-request comment follows `docs/design/reference/surfaces.dc.html`: verdict badge,
one-sentence headline, a `Check / Head / Base` table, named list sections, and a footer
rule. Which facts fill the columns is lydite's to choose — a referral has no
measurements, so the head column carries what the change contains and the base column
what was read out of the merge-base. The design's footer also claims parity with the
reader's local run; **nothing can establish that yet** (see #27), so it is absent rather
than asserted. `ui.Marker` identifies the standing comment for editing, by a marker in the
body rather than by author, because the author is whoever's token posted it.

