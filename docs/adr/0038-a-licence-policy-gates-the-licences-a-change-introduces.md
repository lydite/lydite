# A licence policy gates the licences a change introduces

A repository states one licence policy in `.lydite/config.yml`, and a component
whose change *introduces* a dependency licence outside that policy fails its
row, with the claim located at the manifest or lockfile line naming the package.
Licences already present at the merge-base are grandfathered.

Today the repository states no policy at all. The one licence check lydite runs
is `cargo deny check licenses bans` in `internal/rust`, with no `--config`, so
what decides a Rust component's row is whichever `deny.toml` the consumer
happens to have — and a consumer with none gets cargo-deny's default, which
rejects every licence, MIT included. Verified against the pinned cargo-deny
0.20.2: an unconfigured crate logs `unable to find a config path, falling back
to default config` and cannot pass `check licenses` however it is licensed. A
fresh adopter's Rust component is red on day one for a reason nothing in lydite
states. Go and TypeScript have no licence check of any kind.

## The gate is the set of non-conforming pairs a change introduces

The gate compares the set of non-conforming `(package, licence)` pairs against
the same set at the merge-base, per component. A pair absent there is
introduced, and introducing one fails the row.

**A count was rejected.** A change that removes one GPL dependency and adds
another leaves the count where it was and passes, which is the whole of what the
gate exists to catch. The set answers the question the count only approximates.

**The version is not in the key.** Keyed on package *and* version, every bump of
an already-grandfathered dependency reads as a new pair and fails — the gate
would fire on the ordinary maintenance it has no claim about. Keyed on package
and licence, a bump whose licence is unchanged is the pair that was already
there, and a bump that *changes* the licence is a pair nothing grandfathered,
which is exactly the case worth a human. A package removed and re-added in one
change is likewise not new: the change did not introduce it.

**The pairs are the non-conforming ones, never the full inventory.** That is
what makes recomputing the base side cheap: cargo-deny reports only the crates
it rejected, so the set is what the tool already hands back rather than a second
pass over every dependency. It is also what keeps the gate quiet — a policy
edit that widens the allow-list shrinks both sides at once and gates nothing.

**Per component, and no repository-wide gate**, for the reason
[ADR 0028](0028-crap-gates-the-delta-above-the-threshold.md) gives: per
component is strictly the stricter rule, and a change that introduces a licence
in one component and clears one in another nets to zero over the repository.
Per component also carries the ecosystem for free — a component names one
runner, which implies one language — so a `serde` crate and a `serde` package
can never collide in one set.

An unknown or unclassifiable licence is a pair like any other, with `unknown` on
the licence side. It grandfathers the same way, so adopting the gate does not
fail a repository over a dependency whose licence nobody could read before.

## The base set is recomputed at the merge-base

`measureBaseTree` is the precedent: check the merge-base out into a throwaway
worktree, run the same path there, remove the worktree. The licence set is
recomputed that way.

**A stored, tree-keyed baseline written by `test record` was rejected**, and the
deciding argument is the one ADR 0028 makes against widening the `v4` coverage
entry. An entry written by a lydite that did not yet compute licences reads back
with the field at its zero value — the empty set — so the delta on the day of
the upgrade is `current − ∅`, the absolute set, failing every adopting
repository over dependencies nobody in that change chose. That is the exact
failure the delta exists to prevent, arriving through the mechanism meant to
prevent it. It would also need a `lydite-baseline.yml` change, which is a CI
edit and out of this slice.

ADR 0028's own reason for *not* measuring the base tree does not apply here, and
the difference is what makes recompute affordable. A CRAP miss would have to run
every component's suite, start every compose service and build an instrumented
binary. A licence set needs the lockfile, the module cache and one tool
invocation: no suite, no service, no instrumentation, no coverage report. The
base worktree is checked out, the set is read, the worktree is removed.

**A base that cannot be built gates nothing and says so on the row.** The row is
`unmeasured` and names what failed — a worktree that would not check out, a
module download that would not resolve, a cargo-deny that would not run. It is
never `pass`: a gate that could not run must not render as one that ran and
found nothing. A run with no diff base at all — `lydite scan` on `main` — is
`context`, reporting the full non-conforming set and gating nothing, which is
the shape a scan with no `--diff-base` already has.

## The policy is an SPDX allow-list, and absent it the gate gates nothing

```yaml
licence:
  policy:
    allow: [Apache-2.0, BSD-2-Clause, BSD-3-Clause, ISC, MIT, Unicode-3.0]
```

**A deny-list was rejected.** It admits, silently, every licence nobody thought
to name — which over a dependency graph nobody reads is most of them. An
allow-list's failure mode is a row a human has to answer; a deny-list's is a
gate that passed.

**An absent or empty `policy.allow` reports `not configured` on the row and
gates nothing.** It does not invent a default, and the reason is in this
repository's own history with the question: cargo-deny's default is precisely
an invented one, and it rejects every licence. A gate whose unconfigured
behaviour fails every repository is a gate that gets switched off before
anybody configures it.

**There is no separate `enabled` switch.** `Secrets{Enabled}` is the template
for an on/off key, but here the list itself already carries that state: a
non-empty `allow` is configured, an absent or empty one is not, and a second
key that can disagree with the list — `enabled: true` beside an empty `allow`,
or `enabled: false` beside a populated one — is a state nothing in this design
needs and every reader has to resolve anyway. The list is a fact about the
repository that every invocation shares, in the same group as `coverage.source`
and the report paths: which licences this organisation may ship is a property
of the organisation, not of any one run. It suppresses no finding — it decides
what a finding *is*.

**`license:` is rejected by name.** The key is spelled `licence:`, matching this
repository's own prose and the package name, and SPDX and every tool spell it
`license`. A key a consumer misspells is a policy silently absent and a gate
silently off, so the misspelling is an error naming the correct key, the way
`coverage.source` and `linter: eslint` are errors rather than omissions.

**Conformance is evaluated over the licence expression, not over a string.**
`Apache-2.0 OR MIT` conforms when either half is allowed; measured — `libc`
passes under an allow-list carrying MIT and not MPL-2.0. Go states no licence
anywhere a tool can read — there is no `go.mod` field for it — so there is no
expression to consult, and where a module offers multiple licence files with
different ids, they are read the same way an expression's `OR` would be: any
one allowed id conforms. Measured — `gopkg.in/yaml.v2` carries `LICENSE`
(Apache-2.0) and `LICENSE.libyaml` (MIT); an allow-list holding either passes
it.

**The stated cost is a dependency that bundles a second, separately-licensed
component under its own `LICENSE` file at the module root** — a vendored
generator, a copied algorithm, an embedded asset — rather than genuinely
offering a choice between two licences for the same code. Nothing in a flat
set of `LICENSE*` files distinguishes that case from dual-licensing, and this
design accepts the risk: a module carrying one allowed id and one
non-conforming id passes. A future signal that ties a licence file to the
subtree it covers would narrow this; none exists to read today.

## Rust reads the same policy, through a generated config

One stated policy for all three languages. Where the policy is set, lydite
generates cargo-deny's `[licenses]` table from it into a temporary file and runs
`cargo deny check licenses --config <generated>`.

Measured against cargo-deny 0.20.2 over `internal/licence/testdata/denyprobe`:
an allow-list omitting MPL-2.0 rejects `cbindgen` with code `rejected` and exits
4; adding MPL-2.0 to the same list exits 0 with the summary line alone.

**The generated table sets `unused-allowed-license = "allow"`, and that is not
cosmetic.** At its default, every allowed licence the dependency graph does not
happen to use produces a `license-not-encountered` warning diagnostic whose
`graphs` array is empty — three of them for the probe. `denySubject` reads an
empty `graphs` as no crate, so each locates at line 0 and every one of them
shares the site `license-not-encountered␟`, separated only by `finding.Number`'s
ordinal. A repository whose policy is merely broader than its dependency set
would publish a handful of claims naming no package and clearable by no edit.
A repository-wide allow-list is by construction broader than any one component's
graph, so the default is wrong for every generated config.

**`check licenses` moves out of the existing cargo-deny row.** `denyArgv` runs
`check bans` alone under whatever config the consumer has, and the licence check
is its own invocation under lydite's generated config. Pointing `--config` at a
generated file for a combined run would replace the consumer's whole document —
their `[bans]`, `[advisories]` and `[sources]` tables with it — so a repository
that had curated a ban list would silently stop enforcing it. Splitting the two
keeps the consumer's config governing everything it governed and leaves exactly
one thing lydite's policy decides. It costs a second cargo-deny invocation per
Rust component, against a warm crates.io index and no build.

With no lydite policy but a consumer `deny.toml` present, the licence check runs
against that file and the row states where the policy came from — and gates on
it absolutely, the way cargo-deny always has: a consumer's own configuration is
opt-in, evaluated whole on every run, and carries no adoption-shock problem for
a delta to solve. With neither, the row is `context` naming what is missing,
and cargo-deny's reject-everything default never decides a row again.

**Only lydite's own stated policy is delta-gated**, because a policy applied
absolutely would fail every adopting repository on the licences it already ships
— the same argument that chose a delta in the first place, and it does not stop
being true at a language boundary. The non-conforming set is what cargo-deny
already reports, at both ends, so the base side costs one more invocation in the
base worktree. A consumer's `deny.toml` needs no delta: it is the source Rust
has always gated on absolutely, before this ADR and after it.

## Go is classified in-process by `licensecheck`

`github.com/google/licensecheck` v0.3.1, pinned through `source/cli`'s own
`go.mod`, reading the licence files at each dependency's module root.

Measured over `internal/licence/testdata/goprobe`: `github.com/juju/errors`
classifies LGPL-3.0 at 84.5% coverage, `github.com/hashicorp/go-version`
MPL-2.0 at 100%, and `gopkg.in/yaml.v2` yields both of its licence files —
Apache-2.0 from `LICENSE` and MIT from `LICENSE.libyaml`. It builds under
`CGO_ENABLED=0` and pulls in nothing outside the standard library beyond its own
`internal/match`, so it costs no pin, no manifest, no install and no second
binary: it is a Go dependency Dependabot already watches, which is what
[Tool pins](../../agentic/references/tool-pins.md) asks of every pinned version.

**`github.com/google/go-licenses` was rejected on a measurement, not on
documentation.** It does not need cgo, so the repository's absolute no-cgo rule
does not decide it. What decides it is that it cannot read lydite's own module:
run as `go-licenses csv ./...` in `source/cli` it reports `does not have module
info. Non go modules projects are no longer supported` for every standard-library
package — the Go toolchain is provisioned as a module, `golang.org/toolchain@…`,
and its loader does not recognise that shape — and exits 1 having emitted no CSV
row at all. The capture is `go-licenses-lydite.txt`. Over the probe module,
where it does run, it reports one licence per module and drops the MIT half of
`gopkg.in/yaml.v2` entirely, and emits an `Unknown` row for the main module
under test. A second binary to pin, install, keep in a Dependabot manifest and
keep working, which classifies less than the library does and cannot read the
repository it would be shipped from.

**The scope is `go list -deps -json ./...`, not `go list -m all`.** Measured over
`source/cli`, the module graph names 20 modules and 12 of them answer no
directory, because a graph node is not a download. A classifier reading that
graph calls all 12 `unknown`, and an allow-list then rejects every one of them:
a gate that fails a repository over modules its build never compiles.
`-deps` names only the modules contributing compiled packages, and answered a
directory for every one.

Test-only dependencies are excluded, which is what `-deps` without `-test`
already does — `github.com/google/go-cmp` does not appear. A licence obligation
attaches to what is distributed, and lydite's consumers ship a binary. The cost
is named: a copyleft test helper that reaches a shipped artefact by some other
route is not caught here.

A `replace` is followed to its right-hand side, which is the tree the build
compiles. It is the same side `gomod.go` already records a manifest line for, so
the line a claim lands on and the licence that claim is about name one entry.

**A vendored module is resolved through the package's directory, not the
module's.** Measured: with a `vendor/` tree, `go list -deps -json` reports no
`Module.Dir` at all while each package's own `Dir` points into
`vendor/<module path>`, which is where `go mod vendor` copied the licence files
— both of `gopkg.in/yaml.v2`'s among them. Reading `Module.Dir` alone classifies
every dependency of every vendoring repository as `unknown`.

Licence files are matched case-insensitively by prefix — `LICENSE`, `LICENCE`,
`COPYING` — at the module root only. The probe alone produced `LICENSE`,
`License`, `LICENSE.txt` and `LICENSE.libyaml`. Where nothing matches, or
nothing classifies, the licence is `unknown`.

## A lockfile-only bump is not evidence about licences

`internal/referral` is expected to gain an exemption for a lockfile-only bump
whose SCA run is clean, so routine dependency maintenance stops asking for a
human. This ADR implements none of that, and states one constraint on it: a
clean SCA run is evidence about advisories and about nothing else. A bump that
introduces a copyleft dependency is precisely a lockfile-only change with a
clean SCA run, and it is the change that most needs a human — the licence
obligation arrives with the merge and is discovered afterwards.

So the exemption is conditional on the licence gate having **run and passed**,
never on its absence. A component whose licence row is `unmeasured` or `not
configured` is a component the exemption does not apply to, for the same reason
a gate that could not run never renders as one that passed.

## Consequences

- `.lydite/config.yml` grows a `licence` section, and `license` is rejected by
  name.
- `lydite scan` gains a `licence(<component>)` row per component: `pass`,
  `fail` with the introduced pairs, `unmeasured` where the base could not be
  built, or `context` where no policy is stated and where no diff base was
  given.
- `internal/rust`'s cargo-deny row narrows to `check bans`, and a repository
  with no `deny.toml` stops being failed by a default nothing in it chose.
- A claim is located at the `Cargo.lock` stanza or the `go.mod` require naming
  the package, and identified by the package and licence rather than by the text
  of that line — the departure [ADR 0032](0032-every-scanner-reports-its-findings-as-data.md)
  already makes for a dependency advisory, for the same reason: two licences
  against one package would otherwise collapse into one claim.
- `source/cli` takes one new dependency, `github.com/google/licensecheck`, and
  no new pinned binary.
- TypeScript has no licence source here and gets one `context` row naming the
  limit, the shape [ADR 0028](0028-crap-gates-the-delta-above-the-threshold.md)
  gives a language with no complexity source.
  **Closed by [ADR 0042](0042-a-typescript-components-licences-are-read-from-its-lockfile.md)**,
  which reads an npm component's licences from its lockfile directly, with no
  install; yarn and pnpm remain `unmeasured` absent an install this
  repository's `scan` does not perform.
