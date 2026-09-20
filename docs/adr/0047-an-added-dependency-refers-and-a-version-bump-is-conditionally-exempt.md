# An added dependency refers, and a version bump is exempt only on a condition

A change that introduces a package the merge-base did not depend on — direct or transitive —
is **disqualified**, so no exemption can carry it and a person reads it. A change that only
moves versions of packages already present may match a **version-bump exemption**, and only
when the versions all move by a patch or a minor *and* the licence and SCA rows for every
component ran and passed.

The two halves answer two issues.
[#23](https://github.com/lydite/lydite/issues/23) is the gap SCA leaves: an advisory database
says a package has a known vulnerability, and says nothing at all about an agent adding forty
transitive dependencies, or about a package first published last week. Adding a dependency is
not wrong, which is why this is a disqualifier and not a gate — there is no more work the
author could do to clear it, and the author should not be asked to.
[#82](https://github.com/lydite/lydite/issues/82) is the other side: with the disqualifier and
nothing else, every Dependabot patch bump is referred, and a referral nobody can act on
usefully is the friction that teaches people to stop reading referrals.

This is also the exemption [ADR 0013](0013-referral-not-approval.md) wrote down as its worked
example — "a dependency bump with a clean SCA run" — and which
[ADR 0014](0014-evidence-only-referral-matching.md) left unbuilt because it "needs a condition
an exemption of `{name, reason, paths}` cannot express". The condition is what this ADR adds,
and it is built to the rule ADR 0014 set for exactly this moment:

> An author-controlled claim may only ever add a referral, never remove one.

Every input here is read off two trees. The delta is the set difference between the manifests
at the merge-base and the manifests at HEAD; the version classification is arithmetic on the
strings those manifests contain; the licence and SCA evidence is a report document a scan
produced. Nothing an author writes — not a commit message, not a PR title, not a comment in a
lockfile — is consulted anywhere in this decision. [ADR 0040](0040-an-undeclared-go-api-break-fails-and-a-declared-one-is-referred.md)
is the precedent for the shape; this is the same shape with no claim in it at all.

## The base side is `git show`, not a worktree

Each manifest the diff touches is read at the merge-base with `git show <merge-base>:<path>`,
the pattern `review` already uses for the exemptions file itself
(`cmd/lydite/review.go`'s `loadExemptionsAt`), including its separation of "is it there"
from "can it be read".

No base worktree is materialised, and the contrast with ADR 0040 is the reason. An API-surface
diff needs a real tree on disk because `go/packages` builds it; a dependency set needs only the
text of a manifest, and `go.mod`, `go.sum`, `Cargo.lock` and `package-lock.json` are all read
by parsers that take bytes. `review` gaining a second `git worktree add --detach` for something
that parses line-oriented files would buy nothing and cost a checkout on every pull request.

**A manifest that cannot be read or parsed at either side makes the delta unmeasured, and an
unmeasured delta disqualifies.** It does not exempt, and it is not skipped. This is
[`a gate that could not run never renders as one that passed`](../../agentic/rules/a-gate-that-could-not-run-never-renders-as-one-that-passed.md)
applied to a disqualifier: the state "lydite does not know whether this change added a
dependency" and the state "it did not" must not produce the same verdict, because the first is
where a malformed or novel lockfile lands and the second is where routine maintenance lands.
Failing closed here costs a referral on a manifest lydite cannot read; failing open costs the
whole check on precisely the manifest that was unusual.

## Three ecosystems are read, and the rest are referred by name

Dependency-set extraction is implemented for `go.mod`/`go.sum`, `Cargo.lock` and
`package-lock.json`. Two of the three readers exist:
`internal/typescript/licence.go` already recovers a name and a version per package from
`package-lock.json` ([ADR 0042](0042-a-typescript-components-licences-are-read-from-its-lockfile.md)),
and `internal/rust/lockfile.go` already keeps each `[[package]]`'s name and version. Go needs
additive work — `internal/golang/gomod.go` keeps a line map and not a dependency set — and
`go.sum` is the better source of the two, because it enumerates every module and version the
build actually pins, transitives included, where `go.mod` alone does not.

**Yarn, pnpm and pip are a named gap, not a silent one.** `yarn.lock` has two incompatible
formats, pnpm's lockfile encodes its own store layout, and pip spreads the same question across
`requirements.txt`, `poetry.lock` and `Pipfile.lock` — this repository watches two pip manifests
of its own. There is no reader for any of them, and a reader written by guessing at the format
fails in the one direction that matters: it reports no additions from a file it half-understood.
So a change touching one of those manifests is **referred**, through the same unmeasured path as
an unparseable manifest. Naming the gap is the point; an ecosystem quietly outside the check is
indistinguishable, in the report, from an ecosystem that had nothing to say.

**Applicability is sniffed from the diff's paths, never from a component declaration.**
`internal/referral.Disqualifications` takes a `Change` and knows nothing about
`component.File`, and this feature does not make it know: the question it asks is "does this
diff touch a manifest, and of what kind", which the changed paths answer completely. Reading a
component's declared lockfile through `internal/nodedeps` would make referral depend on
component loading to answer a question about paths, and would go wrong in the direction of
missing a manifest no component declared.

## Added means a new name, and removal never disqualifies

A package is **added** when its name is in the head-side set and not in the base-side set. No
distinction is drawn between a direct and a transitive dependency: #23's forty new packages
arrive transitively, and a rule that only counted direct additions would miss the case that
prompted it. A version change on a name present at the base is never an addition — that is the
exemption's business, below, and not the disqualifier's.

A **removed** package never disqualifies on its own. #23's complaint is new untrusted code
entering the tree; a removal is the opposite of that, and it carries none of the supply-chain
risk the disqualifier exists for. Disqualifying on removal would send every dependency cleanup
to a human, which is the friction #82 exists to remove, in exchange for nothing.

## The exemption's condition is a `versions:` key with exactly one value

```yaml
exemptions:
  - name: dependency-bump
    reason: routine version maintenance, no new dependency
    paths:
      - go.mod
      - go.sum
    versions: patch-and-minor
```

`patch-and-minor` is the only accepted value, and anything else is rejected by name at parse
time — the stance `exemptions.go`'s `dec.KnownFields(true)` already takes toward an unknown key,
and `config.validateLinter` toward a retired `linter: eslint`. An unrecognised value silently
ignored is an exemption that widens to its unconditional form without anybody editing the file.

**Unset means no version condition**, which is today's path-only behaviour, unchanged. This
matters for ADR 0014's union rule, and the ordering preserves it: `Decide` still matches a
change against one exemption's `Covers` before any condition is consulted, so an exemption
requiring nothing and an exemption requiring `versions: patch-and-minor` still cannot be
combined into a union that requires neither. The condition is a further test a single matched
exemption must pass, never a term in a disjunction across several.

**Every version pair in the delta must classify patch-or-minor.** A change where one dependency
is patched and another is majored is not boring as a whole, and the major bump is not made
boring by the patch sitting beside it in the same lockfile.

**A pair that is not comparable fails the condition.** A git-ref pin, a non-semver version
string, a wildcard: lydite cannot say how far the version moved, so it does not say it moved a
little. Same direction of failure as an unreadable manifest, for the same reason.

A cargo pin-manifest requirement edit — `=0.9.0` becoming `=0.9.1` in a `*-pin/Cargo.toml` —
classifies through the `Cargo.lock` stanza that edit moves, like any other dependency edit.
There is no special case for it, and no reader for `Cargo.toml` itself: the tool-pin manifests
are ordinary manifests ([ADR 0006](0006-tool-pins-as-dependabot-manifests.md) exists so that
Dependabot treats them as such), and a rule that treated lydite's own pins differently from a
consumer's would be a rule tested nowhere but here. **A `*-pin/Cargo.toml` edit with no
accompanying `Cargo.lock` change is invisible to this delta** — the same accepted gap as yarn,
pnpm and pip, just for a manifest this ADR otherwise covers: every `*-pin/` directory in this
repository commits a lockfile beside its pin, so a Dependabot bump always moves both, but a
consumer whose pin manifest carries no lockfile is not measured at all rather than referred.

### A `0.x` version is not boring the way a `1.x` one is

SemVer states that the `0.y.z` line is for initial development and that its API should not be
considered stable, and that in the `0.0.*` line "anything MAY change at any time". Cargo's
caret default encodes the first half of that by treating `0.y.z` as compatible only within `y`.
A rule doing plain arithmetic on the version positions would classify `0.4.0` → `0.5.0` as a
minor bump and exempt a change the ecosystem itself considers breaking.

So the classification is stated in terms of positions and not of the raw arithmetic:

- major is `0` and the minor position changes (`0.y.z` → `0.(y+1).0`): **not exempt**;
- major and minor are both `0` and any position changes (anywhere in the `0.0.*` line): **not
  exempt**;
- major is `0`, minor is greater than `0`, and only the patch position changes: **exempt**, an
  ordinary patch bump inside a `0.x` minor that the ecosystem does treat as a compatibility
  boundary.

## The licence and SCA evidence comes from `--reports`, and CI pays for it

`lydite review` gains an optional `--reports <dir>` flag, the same shape `lydite publish` and
`lydite mutation merge` already take: a directory holding report documents, read through the
existing `readDocuments`/`ui.Document` machinery in `cmd/lydite/reports.go`.

When it is given, the version-bump exemption's condition additionally requires that, for every
component, the `licence(<component>)` row and the SCA row are present in the scan document and
are `StatusPass`. A component missing from the document, or whose row is unmeasured, not
configured, or failed, means the condition is not met.

This is [ADR 0038](0038-a-licence-policy-gates-the-licences-a-change-introduces.md)'s constraint
implemented rather than restated. A clean SCA run is evidence about advisories and about nothing
else, and the bump that introduces a copyleft dependency is precisely a lockfile-only change with
a clean SCA run. So the condition is on the licence gate having **run and passed**, never on its
absence.

**Without `--reports` the condition can never be satisfied.** Every local run, and any CI
invocation that does not pass the flag, refers a version bump exactly as it does today. The
new flag can only ever make a change greener by supplying evidence, so a repository that never
adopts it loses nothing and misreads nothing.

**The CI cost is a new edge in the workflow graph.** `.github/workflows/lydite-pr.yml` runs the
scan job and the referral job without a dependency between them; for `review` to read
`scan.json` the referral job must now need the scan job and download its artifact. That is
added latency on every pull request and one more artifact dependency to keep correct — a real
topology change, of the kind this repository requires be asked about rather than made, and it
was asked about and approved before being recorded here.

## Consequences

- `internal/referral` gains one disqualification kind for an added dependency and one for an
  unmeasured delta, beside the kinds already there and without restructuring
  `Disqualifications`.
- `internal/referral.Exemption` gains `versions`, the first condition any exemption has carried.
  An older lydite binary meets it under strict parsing and rejects the file, which is the
  intended failure: the alternative is a binary that ignores the condition and applies the
  exemption unconditionally.
- `internal/golang/gomod.go` grows a dependency-set reader; `internal/rust` and
  `internal/typescript` expose the sets their existing readers already recover.
- A yarn, pnpm or pip manifest in a change is referred, and stays referred until somebody writes
  the reader. This is the accepted gap, and the report says which manifest caused it rather than
  saying nothing.
- **This ADR implements none of it.** The plan it records builds `internal/depdelta` and the
  manifest readers, then the disqualifier (closing #23), then the exemption condition (closing
  #82). Declaring a starter `.lydite/exemptions.yml` that uses the exemption is a separate
  change again, because an exemption set is the artefact `git log` on that file exists to make
  readable and it should not arrive inside the change that invents the mechanism.
