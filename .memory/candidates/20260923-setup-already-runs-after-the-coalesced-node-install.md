about: Component.Setup already runs per-component, after the coalesced node install, with the right PATH
saw:
  - source/cli/cmd/lydite/test.go
  - source/cli/internal/component/component.go
---

In `runComponent` (cmd/lydite/test.go), the order is: `prepare` (~test.go:726, which
reaches `nodedeps.Install` via a runner's `Prepare` or via `prepareCommand` — the
coalesced, locked node_modules install keyed on the resolved workspace root), then
`startServices` (~test.go:733), then `runCommands` over `c.Setup` (~test.go:751). So a
`setup:` line runs after the shared install has already completed for that root.

It also gets a usable environment: `runCommands` (test.go:1171-1179) runs each line
through `sh -c` under `childEnv(tc, c, runner.Invocation{})` (test.go:1836), which
includes the toolchain's `PathDirs` — so `npx`/`npm`/`pnpm` resolve to the Node lydite
provisioned — plus the component's own declared `env:`.

Consequence: `Component.Setup` is already the answer for "run one extra command after
this component's install," with no new config surface needed — a component needing
e.g. `npx playwright install --with-deps chromium` can declare it in `setup:` and it
runs scoped to that one component, after node_modules exists, with the right PATH.
Established while drafting docs/adr/0055-an-install-step-is-a-setup-command-and-the-override-still-coalesces.md,
in response to https://github.com/lydite/lydite/issues/248.
