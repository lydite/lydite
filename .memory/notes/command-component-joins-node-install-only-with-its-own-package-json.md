---
name: command-component-joins-node-install-only-with-its-own-package-json
kind: gotcha
description: "A command: component joins the coordinated node install only when its own directory holds a package.json, not merely when a workspace root resolves above it; installsNodeDeps is the one predicate the install and its report row share."
anchors:
  - path: source/cli/internal/test/run/prepare.go
    blob: f03a29baedc8
confidence: verified
---

In `internal/test/run/prepare.go`, a `command:` component (no `runner:`, so `runner.Lookup` fails and there is no `r.Lang`) joins lydite's coordinated `nodedeps.Install` (`prepareCommand`, line 61) only when its own directory holds a `package.json` (`installsNodeDeps`, line 84) — not merely when it resolves a shared JS workspace root via `nodedeps.WorkspaceRoot`. Resolving a root is not sufficient: a Go or Rust `command:` component can sit as a sibling under the same ancestor workspace root without being one of that workspace's packages, and handing it a node install it never asked for is a worse failure than the lockfile race the join prevents (lydite/lydite#199).

`installsNodeDeps(dir, c)` is the single predicate both `prepareCommand` (the install path) and `installNote` (`prepare.go:128`, the "no install ran, no root resolved" row) key off, so a joining `command:` component gets the same failure-row and unmeasured-row visibility a runner-based TypeScript component has. Before, `installNote` gated on `r.Lang != runner.TypeScript`, which hid the row for every `command:` component.
