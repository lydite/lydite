# `source/web` — the dashboard

A React 19 + Vite + TypeScript app on pnpm 12 (`packageManager` and `engines.node` in
`package.json`), declared as the `web` component in `.lydite/components.yml` with vitest and
`coverage-v8`. It mounts through `<Vipengele>` from `@vipengele/react` (wrapper
`src/theme/LyditeRoot.tsx`), builds on `@vipengele/react-ui`, and takes its dark-only tokens from
`docs/design/tokens.md` (`src/theme/tokens.ts`, `theme.ts`). Geist and JetBrains Mono are
self-hosted through `@fontsource-variable/*`. `src/ledger/` and `src/auth/` are reserved for the
browser ledger reader.

Read, before changing:

- [`design.md`](../../agentic/references/design.md) — the brand and tokens the UI follows.
- [`components.md`](../../agentic/references/components.md) — how the `web` component is declared,
  installed (through `nodedeps`) and gated.
- [`quality-history.md`](../../agentic/references/quality-history.md) — the ledger the dashboard
  reads.
- [`toolchains.md`](../../agentic/references/toolchains.md) — the Node and pnpm runtimes lydite
  provisions.
- [`ci.md`](../../agentic/references/ci.md) — `web-bundle.yml`, which checks the committed bundle.

The built bundle is committed at `dist/` and guarded by `scripts/bundle-hash.sh` and
`bundle.sha256` ([ADR 0010](../../docs/adr/0010-repo-topology.md)), which records two digests,
`inputs <hex>` and `dist <hex>`. The inputs digest covers `src/**`, `index.html`, `pnpm-lock.yaml`
and `vite.config.ts`, so any edit under `src/` — a test included — needs a rebuild and a
re-record. The dist digest covers every file under `dist/`, so `dist/` changes only by rebuilding:
a hand edit not re-recorded fails `--check`, and CI's `--check-against` also rejects a recorded
dist digest that moved while the inputs digest did not. When the inputs changed too, nothing
verifies that `dist/` is what they produce (that needs a rebuild, which ADR 0010 rules out), so
the reviewer owns the `dist/` diff whenever it moves.

```sh
pnpm install --frozen-lockfile
pnpm test                       # vitest run
pnpm typecheck                  # tsc --noEmit
pnpm build                      # writes dist/
scripts/bundle-hash.sh --write  # re-record both digests in bundle.sha256 after a build
scripts/bundle-hash.sh --check  # fail unless both recorded digests match; CI on main
scripts/bundle-hash.sh --check-against origin/main  # --check, then fail if dist moved alone; CI on a PR
```

Front-end and library code reuses the vipengele platform; see
[the rule](../../agentic/rules/reuse-vipengele-before-building-ui-or-library-code.md). Only a
component specific to lydite's domain lives here.
