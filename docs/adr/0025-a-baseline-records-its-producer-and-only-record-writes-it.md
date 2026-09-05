# A baseline records what produced it, and only `lydite test record` writes it

Two changes to the coverage baseline, taken together because both alter the
stored document and two format bumps would cost every consumer two clean cache
misses instead of one.

**A baseline entry records the instrument that measured it**, and a component
whose recorded producer differs from this run's is reported `new` rather than
compared. **`lydite test` no longer writes to the `lydite` branch at all**; the
write is a second command, `lydite test record`, which executes nothing from the
repository.

## A coverage figure is only comparable to one the same instrument produced

The gate's whole claim is *"is this worse than it was"*, and it means that only
if both sides measure the same quantity. Nothing recorded what measured an
entry, so a runner or coverage-provider bump changed the quantity on one side and
the gate reported the difference as a regression by the author of the bump.

Measured directly, bumping vitest 3.2.7 → 4.1.11 and its provider with it, over
an identical tree whose suite passes 36/36 either way: 345 coverable lines
became 152, and **185 of the 193 lines that stopped being counted were covered
ones**. `index.ts`, a barrel of re-exports, went from 5 lines to 0 — v4's
provider counts executable statements where the v8 range mapping had been
counting declarations and imports that always execute. The new number is the
better one, and the gate called it a 1.4-point regression.

`coverage.tolerance` is not the answer. It exists for sub-tenth instrumentation
noise, and a tolerance wide enough to absorb a 1.4-point provider change absorbs
a genuine 1.4-point regression too — the argument
[ADR 0007](0007-line-weighted-coverage-aggregation.md) already makes against the
mean.

### The producer is what wrote the report, not what lydite believes it invoked

The obvious fix — record the runner lydite pinned — works for two languages and
fails in the one where this happened.

| Language | Producer | Where it comes from |
|---|---|---|
| Go | the Go toolchain | it writes the profile outright |
| Rust | cargo-llvm-cov **and** the Rust toolchain | llvm-cov writes the lcov; its line records follow the toolchain's LLVM |
| TypeScript | the workspace's own runner **and** coverage provider | read back from `node_modules` after the install |

`internal/runner` pins cargo-nextest and cargo-llvm-cov itself, and
`internal/toolchain` resolves the Go and Rust toolchains, so for those lydite
knows what measured. For a JavaScript component it knows neither: `vitest` and
`@vitest/coverage-v8` are the *repository's own* dependencies, and lydite
deliberately will not install one into a tree it is about to gate — that would
have lydite change what the repository resolves to and then measure the result.

So the one language whose measuring instrument lydite does not pin is the one
where a bump silently changed what a coverage number means, and it cannot be
closed the way the others were. It is read back instead.

**From `node_modules`, not the lockfile.** The question is what *ran*, not what
should have resolved: a tree installed before the lockfile changed, or one lydite
skipped installing, answers wrongly in the direction that matters. It is also one
code path where the lockfile is three formats with their own schemas.

**An unidentifiable producer is empty, and empty compares equal to empty.** A
Yarn PnP workspace has no `node_modules`, and a workspace lydite cannot
introspect gates exactly as it did before this field existed. Refusing to compare
there would silently stop gating a whole repository over lydite's inability to
look inside it. **Half an instrument is no answer at all**: a producer naming the
runner but not the provider compares equal to itself across a provider bump,
which is the comparison this exists to prevent — and the provider is the half
that changed.

### Exact versions, compared verbatim

Any difference reports the component `new`. lydite cannot know which bumps change
counting, and a version is the only signal available, so a rule that absorbed
patch releases would assert something it cannot support — and when a patch did
change counting, the gate would report it as a regression, which is the failure
being fixed.

The cost is real and is accepted: **one ungated change per bump of anything
named**, including a Go patch bump that certainly changed nothing, and it takes
the `coverage(repo)` row with it, because a composed figure refuses to compare
unless its baseline covers every component in it. One change, visible in the row,
against a number that is wrong in a direction nobody can act on.

**A carried-forward entry keeps its own producer.** Under `--affected` an entry
rides forward across many trees without being re-measured. For JavaScript a
provider bump edits the lockfile, lockfiles are invalidators at any depth, and
everything is re-measured — but for Go and Rust the producer is *lydite's own*
pinned tool, so a consumer upgrading lydite changes it with nothing in the
repository's diff to signal it. The producer therefore travels with the entry
rather than being recomputed at recording time.

**Two residuals, stated rather than hidden.** A provider that changes its
counting without changing its version is undetectable, and nothing can detect
it. And the toolchain half is the version lydite *resolved*, which is the probed
version where the ambient toolchain already satisfies the declaration and the
manifest's own text where lydite provisioned one — so a repository pinning a
channel rather than a version (`stable`) can record `rust stable` on one runner
and `rust 1.91.0` on another, and the two do not compare. That costs one ungated
change per runner, in the direction that reports `new` rather than a false
regression; closing it means probing again after provisioning
([#89](https://github.com/lydite/lydite/issues/89)).

## `lydite test` writes nothing to the `lydite` branch

Recording the baseline is a push. Measuring runs each component's suite and any
`setup`/`teardown` shell the declaration carries, which on a pull request is the
pull request's own code. One command doing both puts a token that can push into
the job that runs arbitrary code ([#49](https://github.com/lydite/lydite/issues/49)).

`lydite test --gate-coverage` now reads the baseline, gates, and leaves what it
would record in `.lydite-reports/baseline.json`. `lydite test record` lands it,
executing no suite, no setup or teardown command and no compose service.

**Both writes go, not just one.** The base-tree cache fill went with the
this-tree recording, so the invariant is one sentence — *`lydite test` never
writes the `lydite` branch* — and is checkable by grepping for one call site.
Keeping the cache fill would have left the rule as "writes only entries for trees
that already merged", which is a sentence nobody can verify at a glance, in a job
that still holds a token.

### What this does not do

**It does not make a pull request's measurement safe to record.** A branch can
edit its own workflow and its own component declaration, so the measuring job can
be made to emit a fabricated measurement, and a command that executes nothing
verifies nothing. Recording a poisoned entry is worse than one bad verdict: it
persists, and every later change gates against it.

So this **narrows** #49 rather than closing it. It removes the write from the job
that runs the repository's code; the answer to the rest is unchanged and is where
it was already — record after merge, in a job measuring code that has passed
review and every gate, which is what `.github/workflows/lydite-baseline.yml`
does.

Saying so plainly is the point. [ADR 0022](0022-a-vendor-operated-app-and-an-oidc-relay.md)
claimed more than it delivered about this same issue and had to be corrected.

### What it buys that the workflow split does not

The command is not a second route to something the workflow already achieved.
Under a shard matrix ([ADR 0017](0017-shards-the-scheduler-and-the-planner.md))
**no single process sees every component**, so nothing can record a complete
baseline: each shard measures part of a tree and cannot honestly claim the tree.
A command that folds N candidates is the only thing that can, and it is what
`lydite test merge` will build on
([#61](https://github.com/lydite/lydite/issues/61)).

Folding needs one fact no report carries: **which entries a run measured and
which it carried forward.** A component can appear in several documents and only
one copy came from a suite that ran, so without that flag a carried entry can
overwrite a measured one, recording the base tree's number for a component the
change rewrote.

The flag is not yet enough to fold a shard, and saying so is the point. Only
`--affected` marks a component carryable, and it is refused alongside the
`--component` a shard narrows with, so a shard establishes no candidate at all
today — honest, because it measured part of a tree and cannot claim the tree.
What this change lands is the document, the fold and the write; what closes the
shard case is `lydite test merge`.

### The candidate names the tree it measured

`lydite test record` refuses a candidate whose tree is not the one checked out.
It is the one integrity property available to a command that executes nothing:
without it a mis-wired workflow lands one tree's numbers under another tree's
key, silently, and that entry gates every later change whose merge-base is that
tree.

It also re-applies the completeness rule against the declaration read from the
tree being recorded — never from the candidate, which would let the document
answer the question its own completeness is in doubt about.

### A separate document, not `ui.Document`

The candidate is data with a schema its consumer depends on; a report is rendered
and read by a person. Putting the counts on `ui.Document` would add a
coverage-shaped field to the type every command renders through, which `publish`
and `review` would carry and never read — and `ui.Row`'s JSON is the published
consumer contract, which exists to keep rendering concerns out.

## Consequences

- `gitstate` bumps its state directory to `v4`. A `v3` entry read as `v4` would
  carry an empty producer, match nothing, and gate nothing while `ReadBaseline`
  reported a hit — worse than a clean miss, which measures the base tree and
  records a complete entry.
- A consumer takes one cache miss, and one only, for both changes.
- **A consumer wiring `--gate-coverage` and no `record` step stops recording.**
  Every run is then a cache miss: correct, slower, and the `record` row says so.
  `lydite/actions` needs the step, which is a cross-repository cutover.
- The first change after any instrument bump is ungated for the components that
  instrument measures, and for the repository row.
