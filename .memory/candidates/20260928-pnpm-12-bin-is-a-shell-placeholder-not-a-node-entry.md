---
about: pnpm's npm package only started shipping a bin-through-node placeholder needing a native binary at pnpm 12; provisionPackageManager's confirm step detects the resulting breakage but still leaves the broken wrapper on PATH
saw:
  - source/cli/internal/toolchain/provision.go
  - source/cli/internal/toolchain/toolchain.go
  - source/cli/internal/toolchain/ambient.go
  - source/cli/internal/toolchain/provision_test.go
  - agentic/references/toolchains.md
  - docs/adr/0004-ensure-language-toolchains.md
---

Explored while planning a fix for: lydite's `unpackManager` (provision.go:598-631) always
writes a wrapper that does `exec node "$here"/../package/<bin entry>`, on the assumption every
package manager's `bin` entry is a `#!/usr/bin/env node` script. True for npm-registry `pnpm`
through 10.x and for `@yarnpkg/cli-dist`/`yarn` at every version I checked (1.22.22, 4.6.0) —
verified by fetching each package's registry document and tarball:

- pnpm ≤10.x (checked 8.15.4, 9.15.0, 10.5.0): `bin: {"pnpm": "bin/pnpm.cjs"}`, a real `.cjs`
  file, no `optionalDependencies`, no postinstall. lydite's wrapper works, no native binary
  involved at all.
- pnpm 11.x (checked 11.0.0, 11.5.0): `bin: {"pnpm": "bin/pnpm.mjs"}`, still a real
  `#!/usr/bin/env node` file lydite's wrapper executes correctly — but `bin/pnpm.mjs` itself
  (this is the Rust rewrite) downloads a native `pnpm-native` binary from `get.pnpm.io` on first
  run if none is found via `resolveInstalledBinary()` (checked inside `../native-binary.mjs`,
  reading `optionalDependencies` node_modules lydite never installs). So even 11.x is not fully
  offline/deterministic under lydite's raw-unpack approach; it just doesn't crash.
- pnpm ≥12.0.0 (checked 12.0.0, 12.1.0, 12.4.1): `bin: {"pnpm": "pnpm"}` — a bare filename at
  package root. Fetched and inspected `package/pnpm` from the 12.4.1 tarball directly: it is a
  shebang-less POSIX shell script (its own header comment: "pnpm's native binary replaces this
  file during installation (see ./install.js). Until then this hands over to ./bin/pnpm.mjs").
  Feeding this file to lydite's `node <entry>` wrapper is exactly what produces the reported
  SyntaxError — `node` tries to parse shell syntax as JavaScript. The package's own fallback
  path (`sh` execing this file, which then `exec node .../bin/pnpm.mjs`) never gets a chance to
  run, because lydite's wrapper never invokes it as a shell script.
  `bin/pnpm.mjs` itself is the real fallback entry Corepack also uses; its own header says
  "Corepack installs no dependencies and runs no lifecycle scripts... The binary is therefore
  downloaded on first use" from `get.pnpm.io` via the `get-pnpm` package, verified by
  `COREPACK_INTEGRITY_KEYS`-configured npm signing keys (own signature scheme, not lydite's
  registry-`dist`-digest verification) — so even pointing the wrapper at `bin/pnpm.mjs` instead
  of the placeholder does not make provisioning self-contained; it adds an unpinned runtime
  network fetch outside lydite's own hash-pinning path unless something supplies the native
  binary first.
  pnpm 12.x also publishes per-platform `@pnpm/exe.<os>-<arch>[-musl]` packages as
  `optionalDependencies` (8 of them on 12.0.0/12.1.0) carrying the actual native binary —
  confirmed via the registry document. These did not exist at all on pnpm 11.x (0
  `optionalDependencies`), so a provisioning strategy built around `@pnpm/exe.*` only covers
  pnpm ≥12, and must fall back to the current node-wrapper approach for older majors, which
  needs no such thing.
- yarn (`yarn` 1.22.22 and `@yarnpkg/cli-dist` 4.6.0, both checked): `bin/yarn.js` in both
  cases, a real node-runnable file with no native binary, no `optionalDependencies`. yarn 1.x's
  `preinstall` script (never run by lydite) is wrapped `:; (node ./preinstall.js || true)` —
  deliberately non-fatal, and irrelevant to whether `bin/yarn.js` runs. Yarn/npm are not exposed
  to this class of bug at all; it is a pnpm-specific consequence of pnpm's Rust rewrite (the
  package.json even lists "rust" as a keyword from pnpm 12 on).

Separately: `resolveOne` (toolchain.go:445-490 area) already runs a real post-install smoke
check — `confirm` (toolchain.go, called after `provision`) re-probes the just-installed
manager with `probeUnder`, which execs the wrapper with `--version` under the composed
environment. Against a real pnpm≥12 tarball this exec fails (the SyntaxError), `confirm`
returns an error, and `resolveOne` does NOT fail the provisioning step: it falls back to
`resolved = displayRaw(req)`, appends `"; warning: could not confirm ...'s installed version
(...) — recording %q"` to the diagnostic note, and still returns `r.env = &Env{PathDirs:
st.pathDirs, ...}` — i.e. the broken wrapper's directory is still put on PATH and the step is
reported (`resolutionProvisioned`) as if it succeeded, just with a stderr warning attached. This
matches `agentic/references/toolchains.md`'s stated doctrine "provisioning failures warn; they
do not fail the scan" (deliberate, for network blips) but this is a different case — the
download succeeded and was hash-verified; it's the *shape of the installed artifact* that
confirm's own probe already proved is broken, and the run proceeds to hand a component this
wrapper anyway. The actual failure then surfaces later and far away, inside
`internal/nodedeps.Install`'s `pnpm install` invocation, as a raw Node SyntaxError with no
connection to the warning line that already diagnosed it.

`provision_test.go`'s `pnpmTarball` fixture (provision_test.go:53-61) models exactly the ≤10.x
shape (`bin: {"pnpm": "bin/pnpm.cjs"}`, a real node-runnable `.cjs`) — no test in this package
exercises the bare-filename-shell-script shape pnpm ships at 12.x, so `confirm`'s real-world
catch of this breakage has no test coverage; the suite's model of "a pnpm release" predates the
bug entirely.
