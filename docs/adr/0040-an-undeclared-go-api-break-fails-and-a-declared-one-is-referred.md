# An undeclared Go public API break fails, and a declared one is referred

A component that opts in has the exported API of its Go module compared against the
merge-base. A break the change did not declare is a **gate**: the author clears it by not
breaking the API, or by declaring the break. A break the change *did* declare is a
**referral**, because every breaking change should reach a person. A declaration with no
detected break is also a referral.

This is the detector [ADR 0014](0014-evidence-only-referral-matching.md) held the
conventional-commit `!` marker back for. That ADR removed the marker because it is an author
claim and "nothing in lydite detects an undeclared API break, so rewriting `feat!:` as `feat:`
removes the disqualifier at no cost"; it said the marker "returns alongside something that can
catch its absence". The rule it set still binds here, unmodified:

> An author-controlled claim may only ever add a referral, never remove one.

The surface diff is evidence, read off two trees. The declaration is a claim, read off text
the author wrote. The two are never mixed: the declaration is consulted only to decide
*which* verdict a detected break gets, and its absence is what the gate fires on. There is no
path by which writing `feat!:` makes anything greener than writing `feat:` would have.

## `lydite review` computes it, not `lydite scan`

`review` is the only command that already emits both verdicts from one diff. Its
`addDecisionRows` draws exactly this line: the exemption-isolation check is `StatusFail`
because the author clears it by splitting the change, and every disqualification is
`StatusRefer` because no change to the branch resolves it. `scan` has no such distinction —
every finding it produces fails — so putting the check there would mean teaching `scan` a
referral concept it has never had, and then teaching `review` to read `scan`'s document to
render the other half.

The cost is that `review` gains three things it does not have today: component loading, so it
knows which components asked; toolchain resolution, so it can build a Go module; and a base
worktree, since `go/packages` needs a real tree on disk and `git show <base>:<path>` cannot
supply one. `cmd/lydite/coverage.go`'s `measureBaseTree` already does the third with
`git worktree add --detach` and cleanup under `context.WithoutCancel`, and that is the shape
to reuse rather than reinvent.

`review` runs no check today, and this is the first thing it runs. That is a real change to
what the command is, accepted because the alternative splits one rule across two commands and
one document.

## The declaration is read from the title and from every commit in the range

**Declared** means either marker, in either place:

- a `!` immediately before the `:` of a conventional-commit type — `feat!:`, `feat(scope)!:`.
  A `!` anywhere else, inside a scope or in the description, is not a declaration;
- a footer line spelled `BREAKING CHANGE:` or `BREAKING-CHANGE:`, the two spellings the
  Conventional Commits specification allows.

Both are looked for in the pull request title, read from the `pull_request` webhook payload
(`internal/forge.PullRequestEvent` gains a `Title`), and in the subject and footers of every
commit in `base..HEAD`, read locally and in CI alike through an additive function in
`internal/gitstate`. Either source declaring is enough.

Both sources are read, rather than one, because the two answer different questions and neither
subsumes the other. **Squash merge makes the title the commit that lands**, so the title is
what a release-time check will read and what `git log` on the default branch will show — a
break declared only in a commit that is about to be squashed away leaves no marker in the
history. But locally there is no pull request and no title, and the commits are all there is.
Reading both keeps the local verdict and the CI verdict computed from the same rule rather
than from whichever input happened to exist.

A declaration with no detected break still refers, and needs no corroboration from the surface
diff. This is the same one-way ratchet as the `[lydite:exclude_from_mutation]` annotation
`internal/referral` already honours: the claim is worthless as a bypass, useful to an honest
author, and never worse than having no marker at all. It is also why the declared-break
disqualifier can never suppress the undeclared-break gate — a disqualifier adds a referral and
has no power over a gate at all.

**The `edited` trigger.** `.github/workflows/lydite-pr.yml` runs on `opened`, `synchronize`
and `reopened`, so a title edited after the last push is never re-read and the run's answer is
about a title that no longer exists. `edited` is added to that trigger list. It is a CI change
and was asked for rather than made silently.

## A component opts in, and nothing else is measured

`.lydite/components.yml` gains one field:

```yaml
components:
  - name: sdk
    dir: sdk
    runner: go
    api_surface: {}
```

```go
// APISurface opts the component into a public-API diff against the
// merge-base. Nil means not measured — most components here are binaries
// and services nobody imports, and an API diff over one is pure noise.
APISurface *APISurfaceConfig `yaml:"api_surface,omitempty"`
```

`APISurfaceConfig` is an empty struct. Presence is the opt-in, which is the `Mutation *bool`
pattern's "a pointer distinguishes omitted from explicit" shape, read the other way round:
mutation is opt-*out* because every component benefits from it, and this is opt-*in* because
most components are binaries and services with no importers at all.

It is an object rather than a boolean because the fields it will eventually carry are
foreseeable — a Rust crate's public root, a TypeScript entry point, a build-tag set — and a
`bool` that has to become an object later is a breaking config change in the tool whose whole
subject is breaking changes. It carries no field *now* because Go needs none: `internal/` is
already the language's own public/private boundary, so nothing has to be named.

**A component that sets `api_surface` and is not Go is a load-time error**, with a message
naming the language and this version — `api_surface is only supported for Go components in
this version`. The alternative, a stated `unmeasured` row, was rejected: a load error is
unmissable and correct on the day Rust support lands, whereas a row saying "asked for, not
measured" is a thing a repository learns to scroll past. Silence was never a candidate — a
gate that could not run must never render as one that passed.

An older lydite binary meets `api_surface:` under strict parsing and rejects the file as an
unknown key. That is the intended failure and not a regression to soften: the alternative is a
binary that silently ignores the opt-in and reports the component green without ever comparing
anything.

## The comparison is `golang.org/x/exp/apidiff`, as a library

`apidiff` is imported and called, so it is a direct dependency of `source/cli/go.mod` and
**not** a `go-pin` entry. The tool-pin ritual —
[`tool-pins.md`](../../agentic/references/tool-pins.md)'s manifest plus Dependabot entry —
exists for tools invoked as subprocesses; a library in `go.mod` is already a manifest
Dependabot watches, and pinning the `apidiff` *command* instead would mean parsing its text
output to recover the structure the library hands over directly.

`gorelease` was considered and answers a different question: it compares a module against its
released **versions** to recommend the next one. What this needs is a comparison against a
merge-base that has no version and never will.

The surface of a module is every package it declares, loaded through `go/packages` from a
tree on disk, minus:

- **`internal/` packages**, because Go's own import rule already makes them unreachable. An
  exported symbol in `internal/` is not part of any API, and reporting a change to one would
  fail exactly the refactoring lydite wants to be cheap;
- **test packages**, which are excluded by not asking for them: the loader runs with
  `Tests: false`, so neither a `_test` external package nor a test-only variant is ever
  constructed. An external test package is importable by nothing, so its surface changing is
  not a change any consumer can see;
- **nothing else.** The comparison is over the **default, untagged build** only. A symbol that
  exists only under a build tag is invisible to it, and a tag-guarded API is a named gap
  rather than a thing the loader guesses at — comparing every tag combination is a
  combinatorial question with no answer a single gate can state.

**A `/v2`-or-later module path is a named gap.** Under Go's import-compatibility rule a major
bump is spelled as a new module path, so base and head declare different modules and every
package reads as removed-and-added. The check is scoped to a module whose path does not change
across the range; a change that moves the path is out of scope in this slice, and saying so is
better than reporting an entire module removed.

### What the probe measured

`source/cli/internal/apisurface/testdata/` holds a probe module built into a real git
repository by `probe_test.go`: one base commit and one head commit per shape, compared through
`apidiff.ModuleChanges` over `go/packages`-loaded worktrees. The reports are recorded
verbatim, so a change to what the tool says is a change to this ADR's evidence and shows up in
review rather than being absorbed.

Against `golang.org/x/exp v0.0.0-20260908205506-85c1c2202aba`:

| Shape | What `apidiff` reported |
|---|---|
| exported function removed | `Incompatible: Removed: removed` |
| exported function signature changed | `Incompatible: Signature: changed from func(int) error to func(int, string) error` |
| method added to an exported interface | `Incompatible: Store.Put: added` |
| exported struct field widened `int` → `int64` | `Incompatible: Config.Timeout: changed from int to int64` |
| exported function added, and a struct field added | `Compatible: Added: added`, `Compatible: Config.Extra: added` |
| an `internal/` symbol removed, and an external test symbol removed | nothing at all |

The interface case is the one worth stating explicitly: adding a method to an exported
interface breaks every implementer outside the module while breaking nobody who only calls it,
and `apidiff` reports it as incompatible. That is the right answer, and it is also the answer
most likely to be argued with when the gate first fires on somebody.

The compatible-addition case is the one that proves the gate does not fire on ordinary growth:
both an added function and an added struct field are reported, and both are reported as
*compatible*. So a gate must read `Change.Compatible` and never the presence of changes.

### What `apidiff` does not carry, and what follows

Two properties of the library shape the package that wraps it.

- **A `Change` is a message and a boolean, and nothing else.** There is no package path and no
  `token.Pos`. `ModuleChanges` prefixes a package path only when a whole package is added or
  removed; a per-symbol message such as `Store.Put: added` names the symbol alone. So locating
  a finding at a declaration line — at the head's line, or at the base's for a removal, stated
  in `Finding.Detail` — is `internal/apisurface`'s own work, resolved from the symbol name
  against the loaded package, and not something the tool hands back.
- **`ModuleChanges` iterates a map**, so the order of changes across packages is not stable
  between runs. A report rendered in that order would differ run to run for no reason a reader
  could act on, so the wrapper compares package by package and orders the result itself.

## No calibration period, because the opt-in is the calibration

[#24](https://github.com/lydite/lydite/issues/24) allows a non-blocking period with an explicit
condition for flipping it on. There is none. A component only has its surface compared because
somebody wrote `api_surface:` into `.lydite/components.yml` for it, in a change that is itself
reviewed — that declaration *is* the deliberate act a calibration period exists to arrange, and
a second one would only mean the gate is off for a while in a repository that already said it
wanted it on.

A non-blocking period also has the failure mode
[`a gate that could not run never renders as one that passed`](../../agentic/rules/a-gate-that-could-not-run-never-renders-as-one-that-passed.md)
is written against, one level up: a check that reports and never blocks is indistinguishable,
in the only place anybody looks, from a check that passed.

## A surface that could not be computed refers

The base tree does not build; `go/packages` returns errors; the module path moved. In each case
`review` genuinely cannot tell a break from no break, so it emits **neither** pass nor fail: the
component gets its own row stating the reason, and the change is **referred**.

Failing would be a gate the author cannot clear — the base tree is not theirs to fix, and it is
the merge-base's content by construction. Passing would be the exact failure the repository's
own rule names: a gate that could not run rendering as one that ran and found nothing. Referring
asserts nothing false and puts the question in front of the only party who can answer it, which
is what a referral is for.

## Consequences

- `source/cli/go.mod` gains `golang.org/x/exp` as a direct dependency, and `golang.org/x/tools`
  moves to the version `x/exp` requires, bringing `golang.org/x/sync` in indirectly.
  `go/packages` is used in this repository for the first time.
- `lydite review` loads components, resolves a toolchain and materialises a base worktree. A
  repository where no component opts in pays none of that: the surface diff is computed only
  for components that asked.
- `internal/referral` gains one disqualification kind for a declared break, beside the kinds
  already there and without restructuring `Disqualifications`.
- **No component in this repository opts in.** lydite is a CLI with no public Go API, so this
  repository's own CI never exercises the gate, and the probe repository built by the test is
  the whole proof.
- Rust (`cargo-semver-checks`) and TypeScript are later slices, and the release-time check —
  a tag that is not a major bump whose range contains a declared break — is its own issue,
  unplanned here.
