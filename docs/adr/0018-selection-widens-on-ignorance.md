# Affected selection widens on ignorance

[ADR 0016](0016-components-and-lydite-run-tests.md) settles *which* components a
pull request runs: those whose directory, `watch` paths, transitive
`depends_on` closure or a global invalidator the change touches. It leaves open
what happens to a changed path that matches none of those, and that gap decides
whether the whole feature is safe.

**A changed path matching nothing selects every component.** The default on
ignorance is to widen, never to narrow — the same shape as the exemption set,
where a change matching nothing is referred.

## Why the direction is the whole decision

Selection narrows what runs. That is its purpose, so a narrowing *bug* and
correct behaviour are indistinguishable from the outside: both produce a green
run in less time, which is the outcome everybody wants to see. Nothing downstream
catches it, because the components that did not run reported nothing to catch.

Narrowing on ignorance would make the global-invalidator list a safety
mechanism — every file family missing from it a change that silently tests
nothing. Lists rot. Widening on ignorance demotes that list to a *performance*
concern, where a gap costs a slower run and never a missed one.

It also inverts what the invalidator list is for. Under this rule a root
`go.work`, a top-level `Cargo.lock` and a `rust-toolchain.toml` beside them all
match no component and already select everything, so they need no entry. What
survives is the opposite case: a file matching a component's directory *too
narrowly*. A `package-lock.json` inside the `web` component is claimed by that
component and by nothing else, so it would select `web` alone — when what a
lockfile changes is what every component in the tree resolves to.

## Consequences

- **The selected set is empty if and only if the diff is empty.** Every changed
  path selects at least one component, so `0 of N affected` can only mean HEAD
  has no changes against the merge-base. The scariest case in the feature
  becomes an invariant with a test rather than a judgement call.
- **The invalidator set is built in and cannot be removed or added to.**
  Lockfiles, manifests and toolchain files match at any depth, since one
  matters wherever it sits; `.lydite/` stays anchored, since the scan root is
  the only place lydite reads configuration from. Not removable for the reason
  the built-in disqualifier set is not: a repository able to drop `.lydite/**`
  could make a change to its own component declaration run nothing. Not
  extensible because `watch` already says "this outside file invalidates me"
  one level down.
  The set earns its place only where a path *is* specifically claimed by some
  component and still affects every other one, since a path no component claims
  already widens.
- **A component rooted at `.` does not suppress widening.** `dir: .` states
  where a component is rooted, not that it tests every file in the tree — a Go
  module at the repository root has no other way to spell its location. So a
  path counts as matched only when a component with a non-`.` directory
  contains it, or a `watch` pattern names it; a `.`-rooted component is still
  selected by containment, but it is not evidence that the path was understood.
  Without this the rule is switched off entirely by one declaration: such a
  component claims every path, nothing is ever unmatched, and a root-level
  `Makefile` or `tsconfig.base.json` selects it alone while every other
  component silently does not run.
  The cost is accepted and it is large: in a repository with components at `.`
  and `web/`, nearly every path is matched only by the root component, so
  nearly every change widens and selection degrades to running everything. That
  is the same bluntness accepted below, on the same grounds — it costs time and
  it is visible in the `select` row, where the precise alternative is a
  conditional rule whose mistakes are silent.
- **Bluntness is accepted twice.** A `web/package-lock.json` change selects
  every component, not just `web`; and in a monorepo scanned with `--dir source`,
  any change outside that subtree selects everything. Both cost time and fail
  visibly, where the precise alternatives are conditional rules whose mistakes
  are silent.
- **Selection is explicit.** `lydite test` with no flags always runs every
  component; `--affected` opts in, and the workflow passes it on pull requests
  because the caller knows the event and lydite would have to infer it from
  detached HEADs and shallow clones. An unresolvable merge-base is an error, not
  a fall back to running everything: that fallback is safe but makes the
  optimisation stop happening with no symptom but a slow job.

## An exclude does not narrow selection

`excludes` in `.lydite/components.yml` states that no component *tests* a path.
That is not the same as nothing depending on it: a file under no component's
directory can still be imported into one — the proving ground's own excluded
`generated/client.ts` is derived from a spec and consumed by a component — so
reading the exclude as an invalidation statement would narrow on a path that
can genuinely break a suite.

It is also the mistake the orphan gate already refuses to make in the other
direction, where a language disabled in `.lydite/config.yml` is still checked:
one declaration answering two questions lets a repository change what goes
untested by editing a file written for a different purpose. An exclude that
narrowed would let a change to that path run nothing at all.

The cost is accepted and on the safe side: a change to an excluded path selects
every component.

## A `watch` pattern that matches no file fails the run

Deliberately asymmetric with the orphan gate's excludes, which only warn. An
exclude covering nothing is fail-safe — it excludes nothing, so the gate is if
anything stricter, and failing a build over tidying is how a gate earns a
reputation for firing on ordinary work. A `watch` covering nothing is
fail-dangerous: the component stops being invalidated by its input, silently and
permanently, while every run stays green. Same syntax, opposite consequence, so
the same treatment would be a false symmetry.

It is reported as a row rather than raised from `Load`, because the question
needs the git file list and must degrade to `unmeasured` outside a repository —
the shape the orphan gate already has, for the same reason.

## Considered and rejected

**Narrowing on ignorance, with the orphan gate widened to every tracked file.**
This makes "matches nothing" impossible by construction rather than safe by
default. It requires an exclude for every `README`, `LICENSE` and workflow file,
which is paperwork for a question lydite cannot act on either way — and a gate
that fires on ordinary work is one that gets switched off.

**Matching invalidators only outside every component's directory.** Gets
`web/package-lock.json` right and still catches the root case, but it is a
conditional rule, and conditional rules are where narrowing bugs live.

**Inferring selection from the branch.** Every signal for "is this a pull
request" is unreliable in the environments lydite runs in, and guessing wrong in
the narrowing direction is the failure this ADR exists to prevent. The caller
already knows.
