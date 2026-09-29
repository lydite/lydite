---
name: component-setup-runs-after-the-coalesced-node-install
kind: rationale
description: Component.Setup already runs per component after the shared node install with the provisioned PATH, so an extra post-install command needs no new config surface.
anchors:
  - path: source/cli/internal/test/run/prepare.go
    blob: f03a29baedc8
  - path: source/cli/internal/component/component.go
    blob: 78b9519abbc5
confidence: suspect
---

Established while drafting ADR 0055 (lydite/lydite#248): a component's `setup:` commands run after the shared node install, not before it. The order observed was `prepare` (which reaches `nodedeps.Install` — the coalesced, locked `node_modules` install keyed on the resolved workspace root — via a runner's `Prepare` or via `prepareCommand`, `internal/test/run/prepare.go:61`), then `startServices`, then `runCommands` over `c.Setup`. `runCommands` runs each line through `sh -c` under `childEnv(tc, c, ...)`, which includes the toolchain's `PathDirs` — so `npx`/`npm`/`pnpm` resolve to the Node lydite provisioned — plus the component's declared `env:`.

Consequence: `Component.Setup` is already the answer to "run one extra command after this component's install" with no new config surface; e.g. `npx playwright install --with-deps chromium` in `setup:` runs scoped to that one component, after `node_modules` exists, with the right PATH.

This was first written against `runComponent` in `cmd/lydite/test.go`, which has since moved (`Prepare` now lives in `internal/test/run/prepare.go:29`); the current order and line locations were not re-derived here, so re-check against the current test flow before relying on it, hence `suspect`.
