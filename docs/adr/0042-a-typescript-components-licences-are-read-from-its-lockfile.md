# A TypeScript component's licences are read from its lockfile, and `scan` never installs

[ADR 0038](0038-a-licence-policy-gates-the-licences-a-change-introduces.md) deferred
TypeScript: "TypeScript has no licence source here and gets one `context` row naming the
limit." This closes that deferral. A TypeScript component's dependency licences are read
from its own lockfile rather than from an installed `node_modules` tree, and `lydite scan`
never runs a package-manager install to measure them — for npm the lockfile already answers
the question completely; for yarn and pnpm no install-free answer exists, and this slice
states that as its limit rather than paying for one.

## The measurement that decided it

Go and Rust both read what the build actually resolved: `go list -deps` and `cargo-deny`
against `Cargo.lock`, neither needing a separate install step because their toolchains fetch
dependencies on the way past. `internal/nodedeps.PackageVersion` reads `node_modules` rather
than a lockfile for the opposite reason — "what ran, not what should have resolved" — because
an ordinary install can silently be stale or skipped, and a lockfile only ever states intent.

That reasoning does not carry over to a *frozen* install. `nodedeps.Commands` only ever runs
`npm ci`, `yarn install --immutable` or `pnpm install --frozen-lockfile` — every one of them
fails outright if the tree it would produce does not exactly match the lockfile. Under a
frozen install, the lockfile is not "what should resolve"; it is what resolving is
contractually required to produce. Measured over this repository's own
`source/cloud-services` workspace (`package-lock.json`, lockfile version 3, after `npm ci`):
of 186 non-local entries, 87 actually installed a `node_modules/<pkg>/package.json` on this
machine (darwin/arm64) and 99 did not, every one of them an optional, platform-gated
dependency (`@cloudflare/workerd-linux-64`, `@img/sharp-darwin-x64`, and siblings) correctly
absent on this platform. For every one of the 87 that did install, the `license` field in
`node_modules/<pkg>/package.json` is byte-identical to the same package's `license` field in
`package-lock.json`. Zero disagreements.

Given that, requiring an install to answer a question the lockfile already answers is a cost
with no accuracy behind it. It is also a cost `scan` does not pay today: `nodedeps.Install` is
invoked only from `internal/runner`, on the `lydite test` path. `cmd/lydite/scan.go`'s
TypeScript branch runs Biome, which needs no install at all. Wiring the licence gate to
`node_modules` would have made the licence-recording function itself responsible for a fresh
install — once for the current tree, and again inside the merge-base's throwaway worktree,
since [ADR 0038](0038-a-licence-policy-gates-the-licences-a-change-introduces.md)'s delta
compares the same measurement on both sides. Doubling an `npm ci` per component, per scan, to
re-derive data the lockfile already states exactly, is not a cost this ADR accepts.

## npm: the lockfile is the whole answer

`package-lock.json` (schema version 3, the only version measured) is read directly, both for
the current tree and inside `licenceBaseTree`'s merge-base worktree — a plain file read, the
same shape of cost as Go's `go.mod` read and Rust's `Cargo.lock` read, and no install on
either side.

- Each `packages` entry names a `license` string or, for older-style packages, a `licenses`
  array of `{type, url}` objects. A `licenses` array composes through `licence.Expression`
  the same way Go composes a module offering more than one licence file: each `type` string is
  one term of an OR, so a package satisfying any one allowed identifier conforms.
- A `license` field that is not itself a well-formed SPDX identifier or expression — measured
  as free text in the wild, e.g. `"SEE LICENSE IN LICENSE.md"` — is passed through the same
  SPDX-expression parse Go and Rust already wrap in `recover()` (`policy.go`'s `satisfies`),
  and whatever does not parse becomes `Unknown` explicitly. `Unknown` is a pair like any other:
  the policy rejects it and the base grandfathers it, exactly as ADR 0038 already decided.
  This slice adds no LICENSE-file text scan for npm — there was nothing in this repository's
  own dependency graph that needed one, and licensecheck reads files on disk that an
  install-free design has no reason to have.
- A workspace's own local package is a `link: true` entry — measured as exactly the three
  local packages `source/cloud-services` declares (`libs/github-app`, `oauth-exchange`,
  `pr-relay`), each carrying no `license` field of its own, the same absence Go's main module
  has. `link: true` is read in the same pass over `packages` and skipped, mirroring
  `p.Module.Main` in `internal/golang/licence.go`.
- A finding locates at `package.json`'s own `dependencies`/`devDependencies` line when the
  package is a declared direct dependency — parsed the same simple way `gomod.go` parses
  `go.mod`'s `require` lines — and at `Line: 0` for anything reached only transitively. No
  locator is read out of `package-lock.json` itself: its v3 keys are `node_modules/<name>`
  paths, nested for duplicates, and while a locator is possible there, it answers a question
  `package.json` already answers for the one case that matters (a direct dependency), and
  buys nothing for a transitive one, which stays `Line: 0` under every language alike. Never
  guessed.

## yarn and pnpm: no install-free source exists, and none is added

Neither `yarn.lock` nor `pnpm-lock.yaml` carries a package's licence in any format measured.
Unlike npm, there is no lockfile field to fall back to, and this slice does not add an
install to get one: `scan` triggers no package-manager install for any of the three managers,
uniformly.

Where `node_modules` already exists — left by an earlier `lydite test` run in the same job, or
by a developer's own local install — it is read opportunistically, the same shape
`nodedeps.PackageVersion` already reads, and a local workspace package is recognised by
resolving its `node_modules/<name>` entry as a symlink into the component's own workspace
root, since neither lockfile format marks this the way npm's `link: true` does. Where
`node_modules` does not exist — the common case for a bare `scan` invocation, and always the
case under Yarn PnP, which has no `node_modules` at all — the row is `unmeasured`, naming
that no licence source exists without an install this scan does not perform. This is this
slice's stated limit, not a defect discovered later: `nodedeps.Manager` already distinguishes
all three managers, and a future slice that decides `scan` should install for some other
reason can extend the same opportunistic read; nothing here forecloses that, and nothing here
pays for it before it is asked for.

## Consequences

- `internal/typescript` gains a licence source with the same exported shape
  `internal/golang/licence.go` has — `LicenceSet` and `LicenceFindings` — so
  `cmd/lydite/scan.go` treats the three languages alike at the call site.
- `scan`'s cost is unchanged by this ADR: no new install, on any path, for any manager. The
  cost this ADR does add is a `package-lock.json` parse per npm component, current and base,
  the same order of cost as the `go.mod`/`Cargo.lock` reads the other two languages already
  pay.
- A yarn or pnpm component's licence row stays `unmeasured` on an ordinary `scan`, indefinitely,
  unless something else in the same job already installed it. That component's licence gate
  never reaches `pass`, and per [ADR 0038](0038-a-licence-policy-gates-the-licences-a-change-introduces.md)'s
  own rule, the lockfile-bump exemption `internal/referral` is expected to gain therefore never
  applies to it either — the exemption is conditional on the gate having run and passed, never
  on its absence, and an `unmeasured` row is exactly that absence.
- No new pinned tool, no new manifest, no new Dependabot entry. `license-checker`, `licensee`
  and `npm ls` remain unused in this repository.
