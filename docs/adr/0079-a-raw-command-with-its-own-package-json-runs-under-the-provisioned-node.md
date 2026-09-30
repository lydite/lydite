# A raw command beside its own package.json runs under the provisioned Node and its pinned package manager

[ADR 0056](0056-a-component-states-its-language-only-where-no-runner-implies-one.md) settled that a
component declaring `command:` and no `runner:` implies no language: `Component.Lang()` is the
runner's and is empty without one, and a `lang:` names what the component is *scanned* as, never
what its suite runs in. That still holds. It left one thing unsaid: `lydite test` installs a
command component's node dependencies when its own directory holds a `package.json`
(`nodedeps.Install`, see `components.md`), and the installer runs the workspace's pinned package
manager — while the toolchain step, which is what provisions that manager, read the component's
language and found none. A component like that reached the install with no Node and no pnpm
provisioned, and failed with `pnpm: not on PATH — lydite could not provision it` on any machine
without an ambient pnpm. A `typescript.install` override was the only way around it.

## Decision

A component declaring `command:` and no `runner:`, whose own directory holds a `package.json`, is
resolved through the workspace's Node toolchain and pinned package manager. It is a
`toolchain.Unit` with `NodeCommand` set, built by `testrun.ComponentUnits`, and
`toolchain.Requirements` resolves it exactly as a TypeScript unit — `engines.node`, `.nvmrc`, the
`packageManager` pin, the pnpm floor, and the `toolchain.node` override all included.

`Unit.Lang` stays empty. The language a unit declares and the toolchain it needs are separate
answers, and `NodeCommand` carries the second without touching the first: a `lang: bash` command
beside a `package.json` is still not TypeScript to anything that decides by language. The criterion
is the one `nodedeps.Install` already joins on — a `package.json` in the component's own directory,
not a resolvable workspace root above it — so the toolchain is provisioned for exactly the
components that install. A command component without a `package.json`, and a component declaring no
suite, provision nothing.

Only `lydite test` builds these units. `lydite review` and `lydite scan` build their unit lists
as before, since neither runs a component's raw command.

## Consequences

- **The command runs under the provisioned Node.** `ChildEnv` places the invocation's own pinned
  directories and then the toolchain's `PathDirs` — Node's, and the pinned package manager's —
  ahead of the inherited `PATH`, and a `PATH` the component declares is appended after it. A
  command that calls `node`, `pnpm` or `npx` therefore finds lydite's, not whatever the machine
  happens to carry, and a declared `PATH` cannot shadow them.
- **The fix depends on the download succeeding.** Provisioning that fails — offline, a registry
  outage, `toolchain.enabled: false` — continues with what is on `PATH`, under a warning. Without an
  ambient pnpm the install still fails with `pnpm: not on PATH`, now with the provisioning warning
  above it saying why.
- **Two packages on one workspace root pinning different `engines.node` still error.** The install
  is coalesced per workspace root ([ADR 0055](0055-an-install-step-is-a-setup-command-and-the-override-still-coalesces.md))
  while Node is resolved per unit, so a command component and a sibling naming different Node
  versions under one root are two answers for one install: their environments differ in the
  Node directory each carries, and `nodedeps.installedUnder` refuses the second rather than
  picking one. Resolving the command component as a TypeScript unit subjects it to the same rule
  its runner-derived neighbours already are.

## Rejected

**Give the command component `Lang: TypeScript`.** It would resolve the toolchain through the
existing path with no new field. `Unit.Lang` would then mean two things — the language declared, and
the language whose toolchain is wanted — and every reader deciding by language would take the
component for TypeScript: a `lang: bash` command beside a `package.json` would read as TypeScript to
the scan, the orphan gate and the coverage readers, against the separation ADR 0056 draws between a
component's declared language and the language its suite runs in.

**A manager-only requirement path.** Provision the package manager the install needs and leave
Node to the ambient one. It is a second resolution mechanism for the `packageManager` pin, the pnpm
floor and the override, which has to be kept identical to the one a TypeScript unit takes and
drifts the first time one of them changes. It also leaves yarn, a node script, running under a
Node lydite did not choose.
