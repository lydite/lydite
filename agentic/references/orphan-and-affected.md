# The orphan gate, and affected selection

> **The reference for `internal/orphan`, `internal/affected` and `internal/pathmatch`** — what goes untested, and what a change could have broken.
>
> The declaration both gates hold to account is in [`components.md`](components.md).

## The orphan gate: what makes the declaration trustworthy

A declared list fails open. Nothing breaks when it goes stale, so a directory nobody
declared is tested by nobody and the build stays green — the one failure mode a
declared list has and a discovered one does not, and the reason ADR 0016 can choose
declaration at all. `internal/orphan` closes it: **every source file must fall under
some component's `dir` or an explicit exclude**, and one that falls under neither
fails `lydite test`.

It is a **Gate** in CONTEXT.md's sense and not a **Referral** — the author clears it by
declaring the component, or by writing the exclude that says this code is tested by
nobody and somebody decided that. Both leave a line in the file whose history is the
record of what gets tested.

```yaml
components: [...]
excludes: ["scripts/**", "tools/gen.go"]
```

**It is a path question and nothing else.** It reads no manifest, parses no source and
reads no manifest at all, which is what lets it catch a whole undeclared
directory holding none yet — a case nothing that reads manifests can see. A generated
file is not special-cased for the same reason: recognising one means reading it, and a
gate that starts reading files has to be right about every language it meets. The
exclude is where a repository says so.

**A file counts only when it is written in a language lydite has a runner for.** The
extension set lives beside the `Lang` constants in `internal/runner`, so the two cannot
come apart — a language that gains a runner and no extensions is one the gate is blind
to, and `TestEveryLangHasSourceExts` refuses it. A `README.md`, a `LICENSE`, a
`Makefile`, an OpenAPI document and a shell script are not code any component could
claim, so requiring an exclude for one is paperwork for a question lydite cannot act on
either way — and a gate that fires on ordinary work is one that gets switched off. The
proving ground is the calibration: six files there sit under no component and exactly
one, `scripts/seed.ts`, must fire.

**A language disabled in `.lydite/config.yml` is still checked.** `rust.enabled: false`
says which checks run over a repository's Rust; it does not say that no component should
test it. Reading it as both would let a repository drop a whole language out of this gate
by changing what its linter looks at — a silent widening of what may go untested, in a
file whose history is not the record of that. The exclude is how a repository says it, in
one line, where a reviewer is already looking. (`floorReport` filters to enabled
languages and is the only gate that does; the reasons differ, and neither generalises to
the other.)

**The file list comes from git** — tracked, plus untracked ones git is not ignoring.
Both halves matter. Tracked alone misses the file just written and not yet staged,
which is the moment its author can most cheaply act on the answer; including ignored
files reports every compiled artefact and every installed dependency as untested
source. A filesystem walk would need its own list of directories to skip
(`node_modules`, `target`, `dist`, `coverage`), which is a second copy of a judgement
`.gitignore` already holds — and the copy that drifts is the one that starts calling
build output source. Outside a git repository, and equally when git lists no source file
at all, the gate reports `unmeasured` and passes: a scan root that is itself ignored — a
vendored checkout, a `--dir` pointed at build output — sits inside a work tree and lists
nothing while exiting zero, which would otherwise render as a green gate that had
examined the whole repository. A gate that could not run must never read as one that did.

**Excludes live in `components.yml`, not `config.yml`.** An exclude states what goes
untested, which is the one thing that file exists to record; splitting the two would
leave each half readable and neither answering the question. They use the same anchored
syntax as the exemption set, so a subtree is spelled `scripts/**` and a bare `scripts`
covers the path of that name alone. An exclude covering no file is named on stderr
rather than failing the build: it is very likely a directory name written where a
pattern was needed, but it is also what an honest exclude becomes the day its file is
deleted, and failing a build over tidying is how a gate earns a reputation for firing on
ordinary work.

**The gate runs before selection and before any component runs.** Whether the
declaration is complete does not depend on which components an invocation chose, or on
there being any — a repository that declares none is exactly the one whose every source
file is orphaned, and a gate it never saw would be the failure it exists to catch. It
takes no baseline and reads the same on the default branch as on a pull request, for
the reason `coverage.floor` does: a file is under a component or it is not.

**It does not catch a forgotten `depends_on` edge**, because there both components exist
and no file is orphaned. Pushes to the default branch running every component is what
covers that.

`internal/pathmatch` holds the matcher, which `internal/referral` also uses. Both decide
something consequential off a path — what may merge unread, and what may go untested —
and two matchers would agree until one learned about a pattern form the other had not,
in a way neither's tests would show.

## Affected selection: `--affected`

A pull request runs only the components a change could have broken; a push to the default
branch runs all of them, which is what makes this an optimisation with a bounded failure
rather than a correctness mechanism. `internal/affected` decides it, from paths alone —
no manifest read, no source parsed, the same stance `internal/orphan` takes. See
[ADR 0018](../../docs/adr/0018-selection-widens-on-ignorance.md).

A component is affected when the change touches its `dir`, one of its `watch` paths, a
component it `depends_on` **transitively**, or an **invalidator**. `depends_on` is read here
and nowhere else: the scheduler ignores it, so the edge decides what runs at all rather than
in what order.

**A changed path matching nothing selects every component.** The default on ignorance is to
widen, mirroring the way a change matching no exemption is referred. This is the decision the
rest of the feature rests on. Narrowing there would make the invalidator list a *safety*
mechanism — every file family missing from it a change that silently tests nothing — and lists
rot. Widening makes it a *performance* list, where a gap costs a slower run and never a missed
one.

It also yields the invariant the package is built around: **the selected set is empty if and
only if the diff is empty.** Every changed path selects at least one component, so
`0 of N affected` can only mean HEAD has no changes against the merge-base, never a narrowing
that went wrong. `TestSelectedIsEmptyOnlyWhenTheDiffIs` holds it.

**The invalidator set is built in, and its purpose is the inverse of what it looks like.** A
root `go.work`, a top-level `Cargo.lock` and a `rust-toolchain.toml` beside them all match no
component and already select everything, so they need no entry. What the set is for is a file
matching a component's directory *too narrowly*: a repository with one component rooted at `.`
and another at `web/` would otherwise have a change to `.lydite/components.yml` select the root
component alone. It is unexported and has no declarable form — `watch` already says "this
outside file invalidates me" one level down, and a set a repository could empty is not a floor,
the same argument the built-in disqualifiers win. Lockfiles, manifests and toolchain files match
at any depth; `.lydite/**` and the workflow paths stay anchored, because the scan root is the
only place lydite reads configuration from and the only place CI is defined.

`.github/workflows/**` and `.github/actions/**` are on the set for a reason worth stating: **a
component rooted at `.` claims every path, so in that repository nothing ever reaches the
widening rule.** The safety net is switched off by one declaration, and only the invalidator set
protects the other components — a workflow edit would otherwise select the root component alone
and leave every other one unrun. That interaction is a real limit rather than a closed hole:
anything at the root that is not on the set is still absorbed by a `.`-rooted component.

**An exclude does not narrow selection.** `excludes` says no component *tests* a path, not that
nothing depends on it — an excluded file can still be imported into a component, as the proving
ground's own `generated/client.ts` is. Reading one declaration as the answer to two questions is
the mistake the orphan gate refuses to make with `rust.enabled`, and an exclude that narrowed
would let a repository make changes to a path run nothing at all. So an excluded path is
unmatched, and therefore widens.

**Selection is explicit, and never inferred.** `lydite test` with no flags always runs every
component. Every signal for "is this a pull request" is unreliable where lydite runs — a
detached HEAD, a shallow clone, a fork with no upstream fetched, a default branch not called
`main` — and the caller already knows the event, so the workflow passes `--affected` on pull
requests exactly as `lydite/actions` already passes `--diff-base auto`.

**`--affected` and `--component` compose, and a shard passes both.** `--component` says what this
job is responsible for; `--affected` says which of those need running. The rest of the slice is
reported `not affected`, with its baseline entry carried forward, by the one shard that owns it.
The two narrow different things and neither substitutes for the other: `lydite test plan` cannot
narrow by `--affected`, which needs a merge-base, git history and a checkout that is not shallow,
so the matrix it emits covers the whole declaration and the shards select within their slices. See
[ADR 0026](../../docs/adr/0026-a-shard-reports-what-it-owns-and-the-fold-decides-completeness.md).

**An unresolvable merge-base is an error**, not a fallback in either direction. Falling back to
nothing is the failure this exists to avoid; falling back to everything is safe but makes the
optimisation stop happening with no symptom other than a slow job — the same way a `{}` baseline
left wardnet's coverage gate comparing against nothing for months. A shallow checkout is a
fixable misconfiguration, so lydite names the fix.

**The diff is repository-wide, and mapped onto the scan root.** `internal/gitdiff` reads the
paths — the half `internal/referral` also needs, so there is one implementation of the thing
that must not vary. Its `core.quotePath` / `diff.relative` / `diff.mnemonicPrefix` pins are
load-bearing in both packages, and demonstrably: without `diff.relative=false` a diff taken in a
subdirectory drops everything outside it *and* strips the prefix from what survives. referral
keeps its patch-line parsing, which selection never reads and must not pay for — that parse
fails closed on an over-long line, so a minified bundle in the diff would abort a selection with
no interest in its contents. A path outside the scan root is not dropped: it matches no
component, and therefore widens.

**A deselected component is reported, never dropped.** One `unmeasured` / `not affected` row
each, labelled `test(<name>)` exactly as a suite row is — so every component produces exactly one
such row whether it ran or not, and what separates them is the status. A bare name would share a
namespace with the gate rows, and nothing forbids a component called `watch`, `select`,
`orphans` or `schedule`; a consumer keying rows by label would then silently lose the gate. Rows
are in declaration order, plus a `select` row carrying `N of M affected` and the reason each
selected component was chosen. **On the default branch, `--affected` runs everything.** The merge-base of that branch with
itself is its own head, so a computed selection narrows to nothing — and a consumer wiring the
flag into one workflow would get a permanently green `lydite test` that executed no suite at all.
ADR 0016 requires the default-branch run to be complete, since a forgotten `depends_on` edge is
caught at merge or never, so the rule is enforced in the command rather than left to every caller
to remember. `0 of N` remains reachable, by a commit whose tree matches its base.

Selection does not run at all for a repository that declares no components. Nothing to select
from is not the same as a change that selected nothing, and asking selection which it is produced
a `select` row claiming the diff was empty beside a row saying no components are declared — and
paid a `git fetch` to do it, which on a shallow or fork checkout turned a report that renders into
a hard error before a row was written.

`0 of N` makes the `select` row `unmeasured` rather than `pass`:
nothing was gated, and a gate that did not run must never render as one that did. The
distinction travels as a **status**, so a consumer separates "0 of 4 affected" from "4 of 4
passed" without parsing prose — and the reasons are what make a selection that quietly returned
everything visible to a human reading a log. The verdict is still pass and the exit code 0: a
branch with no commits against its base is correct work, and failing it is how a gate gets
switched off.

