# pnpm is provisioned as its native binary, and a pin below 12 is refused

`internal/toolchain` provisions yarn as a node script, run through a wrapper that execs the
package's own entry point under lydite's provisioned Node — the same shape Node itself takes,
and the shape `agentic/references/toolchains.md` describes for "pnpm and yarn" together. pnpm's
own registry package does not take that shape from major 12 on: `bin/pnpm` there is a
shebang-less shell placeholder, meant to be overwritten by the package's `preinstall` lifecycle
script with a native binary pulled from one of its `@pnpm/exe.<os>-<arch>` optional
dependencies. lydite runs no lifecycle scripts — a `preinstall` is arbitrary code the registry
package chose to ship, not something a toolchain provisioner executes on a scanned repository's
behalf — so the placeholder itself was what got installed, and it does nothing when run. Every
component pinning pnpm ≥ 12 failed at install time, unconditionally, regardless of what its own
suite needed.

## Decision

Provision pnpm as its native binary directly, verified along a chain that starts at the
repository's own declaration and, from the exe package onward, is anchored in the registry's own
live digest rather than in anything the repository committed to in advance:

1. `packageManager` in the workspace root's `package.json` names the pnpm version and,
   optionally, its integrity hash (`pnpm@<version>+<algo>.<hash>`, the field Corepack itself
   reads).
2. The pnpm package's own tarball is fetched from the npm registry and verified against the
   registry's digest for that version, and — when the repository declared one — against the
   declared hash as well. Neither check stands in for the other.
3. That verified tarball's own `optionalDependencies` names the exact version of
   `@pnpm/exe.<os>-<arch>` it depends on for this host. Trusting the exe package's version at
   all depends on having first verified the pnpm tarball that names it — an unverified read
   would let a mismatched or malicious registry response substitute a different exe release with
   nothing to catch it.
4. The exe package's tarball is fetched and verified against the SHA-512 the registry publishes
   for that version — SHA-512 only, never the SHA-1 `shasum` fallback `verifyDist` accepts for
   the pnpm package proper. A registry publishing no SHA-512 for the exe release is refused
   rather than trusted on SHA-1: this tarball is a binary about to be placed on PATH and
   executed directly, not a script tree a wrapper execs under a runtime lydite already trusts,
   and the older digest is not strength enough for that.
5. The exe tarball's `pnpm` entry is installed as `bin/pnpm`, with no wrapper. No node
   invocation is involved in running it: the binary is native.

`pinnedExe` additionally refuses an `optionalDependencies` entry that names anything but an
exact version equal to the pnpm release itself — a range, or a mismatched version, breaks the
chain at the one point a malformed or tampered manifest could otherwise substitute an
unverified exe release for a verified one.

### What the declared hash does and does not cover

The repository's own `packageManager` hash, when it declares one, is checked against the pnpm
tarball only — step 2. It does not reach the exe tarball that step 4 installs and runs: nothing
the repository writes down names, in advance, the bytes of the platform binary that ends up on
PATH. Those bytes are trusted on the registry's own SHA-512 digest, fetched in the same run that
fetches the tarball it describes — the same trust a plain `npm install` places in the registry
for any package with no lockfile-recorded integrity, and no stronger. Steps 3 and 5 chain the exe
package's *identity* (which package, which version) back to the verified pnpm tarball, so a
tampered manifest cannot substitute a different release; they do not chain its *content* back to
anything the repository committed to ahead of time. A registry response able to serve a
different exe tarball whose `dist.integrity` matches its own bytes passes every check here,
exactly as it would defeat the trust model of an unpinned `npm install` of any other package.
Closing that would mean the repository (or lydite, on its behalf) recording a digest for each
platform's exe binary somewhere Dependabot can bump it — not attempted here, since it adds a
maintenance surface with no analogue elsewhere in `internal/toolchain`'s package-manager
provisioning.

### glibc only

The exe packages pnpm publishes per platform are glibc builds; there is no musl variant to
detect or fall back to. This matches lydite's own Node provisioning, which downloads only the
glibc `linux-x64`/`linux-arm64` tarballs from nodejs.org — a component running under lydite's
provisioned Node was already on a glibc host, so pnpm needing the same is not a new constraint
this decision introduces. `pnpmExePackage` refuses outright, before any fetch, on any
`GOOS`/`GOARCH` pair outside linux/darwin × amd64/arm64.

### The cache key changes shape, and nothing evicts the old one

The pnpm cache key becomes `pnpm-exe-<version>-<os>-<arch>[+<algo>.<hash>]` — the version, the
platform the binary was built for, and the declared hash when there is one. It no longer
collides with the plain `<manager>-<version>[+<hash>]` key a node-wrapped install would have
used, and it now varies by platform, where a script-tree install did not need to.

Nothing prunes the pre-existing `~/.cache/lydite/pnpm-<version>/` directories the old key
shape produced, holding the placeholder that never ran. That is deliberate rather than an
oversight: the new key names a directory the old code never wrote to, so a run under this
decision never reads the poisoned entry — there is nothing for eviction code to protect against
that the key shape does not already prevent. Deleting the orphaned directories is a matter of
reclaiming disk space, not of correctness, and is exactly the kind of stale cache entry a
`~/.cache/lydite` consumer already prunes on its own terms.

## A pin below pnpm 12 is refused per component, and the run does not fail

Nothing below major 12 carries the exe-package split this decision provisions against, so
`toolchain.Requirements` makes no requirement for such a pin at all: `managerRequirement`
returns none, `Ensure` provisions nothing for it, and reports no error. The component pinning
it fails on its own, at install time, through `nodedeps.Refusal` — naming the manifest and the
pin — while every other component in the run carries on. `toolchain.enabled: false` lifts the
refusal: with provisioning switched off, the install runs whatever pnpm is already on PATH,
which is the same escape hatch that section already gives an air-gapped or fully-preprovisioned
runner for every other toolchain.

### Consistent with ADR 0004's "provisioning warns, never fails"

ADR 0004 established that toolchain provisioning is preparation, not a gate: a network blip or
an absent toolchain must not turn a working scan into a hard failure, because falling through to
"whatever is on PATH" was already today's behavior and the next step fails loudly and
specifically if the toolchain really is absent. That principle is about *provisioning itself*
failing softly — a download that could not complete, a toolchain that could not be reached —
and it still holds here without exception: `Ensure` returns no error for an unsupported pnpm
pin, exactly as it returns no error for any other toolchain it could not provision.

What differs is not whether provisioning fails, but what happens next. For Go, Rust and Node,
"nothing provisioned" falls through to whatever is ambient, because any ambient toolchain of an
unknown version is still worth trying — the manifest declared a floor, not an exact release, and
an older toolchain may still work. A pnpm pin is not that kind of declaration: `packageManager`
is an exact version Corepack enforces by refusing to run anything else, and a release below 12
is not "close enough to try", it is a shape lydite has no binary-placement scheme for at all —
provisioning it would mean reintroducing the placeholder failure this decision exists to close.
Refusing it is therefore not the run failing on a provisioning gap; it is the one component that
pinned an unsupported release failing its own install, the same way a component fails its own
install today for any other reason particular to it — a missing lockfile, a broken `command:`, a
dependency that will not resolve. The run as a whole does not fail, and no other component's
result is affected.

## Rejected alternatives

**A GitHub-releases standalone binary.** pnpm also publishes a standalone binary through GitHub
Releases, outside npm entirely. Using it would mean a second host with its own checksum
publication and its own rate limits, alongside the npm registry lydite already fetches every
other package manager release from — a second trust chain to maintain, and a second point of
failure with no shared verification path with the rest of `internal/toolchain`. The
`@pnpm/exe.*` packages are published to the same registry, under the same version, with the same
kind of `dist.integrity` field lydite already reads for yarn's own tarball — one host, one
verification shape.

**Pointing the wrapper at `bin/pnpm.mjs`.** pnpm's package ships a Node-runnable
`bin/pnpm.mjs` entry point alongside the shell placeholder, and running it through the existing
node-wrapper mechanism was considered as a way to reuse yarn's shape unchanged. `pnpm.mjs`
itself fetches a native binary for the host at run time — the same download the registry
tarball's own `preinstall` script performs, just deferred to first invocation instead of install
time — outside any digest lydite checks. Wrapping it would mean lydite calling a script whose
job is to perform exactly the unverified fetch this decision exists to replace with a verified
one; it does not remove the problem, it moves it one layer down and hides it behind a working
first run.

**Running the pnpm package's lifecycle script, or delegating to Corepack.** Both would work
mechanically — `preinstall` does exactly what `downloadPnpm` does by hand, and Corepack's own
shim resolution is the same registry-tarball-then-exe-package chain. Neither is acceptable:
running a registry package's lifecycle script means executing arbitrary code the scanned
repository's dependency tree supplied, on lydite's own account, for every component that pins
pnpm — the same reasoning `agentic/references/toolchains.md` already gives for why lydite fetches
package manager tarballs directly rather than enabling Corepack. Corepack's shims are written
into Node's own install directory, which on a runner is not lydite's to write into, and asking it
to fetch on lydite's behalf reintroduces exactly the "trust the tool to have verified it"
indirection the rest of this provisioning chain exists to avoid.

Reasoning: [`agentic/references/toolchains.md`](../../agentic/references/toolchains.md) and
[ADR 0004](0004-ensure-language-toolchains.md).
