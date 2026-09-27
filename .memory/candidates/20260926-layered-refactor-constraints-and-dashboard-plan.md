---
about: no ADR mandates cmd/lydite's flat command-assembles-gates shape or forbids a layered refactor, but several mechanical constraints make moving code between packages costly, and the dashboard's data path is already decided (ADR 0009) even though no lydite "serve"/daemon exists
saw:
  - source/cli/cmd/lydite/AGENTS.md
  - source/cli/AGENTS.md
  - agentic/references/layout.md
  - agentic/references/quality-history.md
  - agentic/references/coverage.md
  - docs/adr/0009-quality-history-storage-and-access.md
  - docs/adr/0021-source-root-for-the-monorepo.md
  - source/cli/internal/forge/forge.go
  - source/cli/cmd/lydite/root.go
targets: null
verdict: null
---

Search for an architecture ADR governing `cmd/lydite` layering, a CLI-framework decision record,
or a past restructuring attempt turned up none. `source/cli/cmd/lydite/AGENTS.md` states the
current convention plainly ("each command assembles gates that live in `internal/`") but this is
descriptive, not backed by a rationale doc — there is no ADR titled anything like "commands stay
thin" or "package by gate". `docs/adr/` has 58 entries; none is about cmd/internal layering,
cobra vs. another CLI framework, or dependency injection. Cobra (`github.com/spf13/cobra`) is
just used, unremarked, in `source/cli/cmd/lydite/root.go`.

Mechanical constraints a layered refactor has to respect, verified against code:

- No cgo (`CGO_ENABLED=0`, static binary) and module path `lydite/lydite` — already a rule
  (`.claude/rules/no-cgo-and-the-module-path-stays-lydite-lydite.md`), unrelated to layering but
  binds any refactor.
- `var version = "dev"` in `package main`, overridden via `-ldflags` — restated in both
  `source/cli/cmd/lydite/AGENTS.md` and the root `CLAUDE.md`. Renaming the package or variable
  breaks the release build.
- `grammar_subset*` build tags gate every treesitter-based parse (crap, mutation); a bare
  `go test ./...` panics without them (`source/cli/AGENTS.md`).
- Exit code 2 = "refer" is a real contract: `source/cli/cmd/lydite/review_test.go` has ~20
  assertions on it, and `review_apisurface.go:167` notes a CI step matching on exit code
  thresholds. A layered refactor that changes how `review`'s verdict becomes an exit code must
  preserve this exactly.
- **Coverage/mutation baselines are keyed by component name and tree, not by package path**
  (`agentic/references/coverage.md` "A baseline is per-component counts, keyed by tree" — `v4/<tree>.json`
  keyed by component name). Moving Go code between internal packages within one component does
  NOT invalidate a coverage baseline by itself. BUT the **producer string** (ADR 0025) for
  `go-test` folds in `-coverpkg` and the trailing package-pattern scope
  (`.claude/rules/fold-only-denominator-moving-args-into-a-producer.md`) — so if a refactor moves
  code across a `-coverpkg` boundary or changes a component's declared scope args, the producer
  string changes and the baseline compares as incomparable, reading the component as newly
  measured. This is a real, non-obvious cost of moving code between packages if it crosses a
  declared coverage scope.
- `--json` output grammar and the "gate that could not run never renders as passed" invariant
  (`.claude/rules/a-gate-that-could-not-run-never-renders-as-one-that-passed.md`) constrain any
  new layer's error/skip semantics.
- `publish`/`threads` are consumed by `lydite/actions` workflows and the relay
  (`agentic/references/surface.md`, `agentic/references/ci.md`); their CLI surface (flags,
  `--json` shape) is an external contract, not just internal wiring.

Dashboard (ADR 0009, confirmed still current — `agentic/references/quality-history.md` line
197: "The dashboard is not this slice. `source/web/` is still empty; the hosted read path is
ADR 0009's later work."):

- Data lives in git: an orphan branch on the *consumer's* own repo holds four kinds of data —
  baseline (cache), ledger (`history/v1/*.ndjson`, append-only), snapshot (overwritten detail,
  not yet built beyond findings-as-transitions in ADR 0058), and a daily projection
  (`history/v1/daily/<branch>/<year>.ndjson`). Nothing is uploaded to any lydite-run service.
- Serving is **not a lydite daemon**. Grepped `daemon` across docs/adr, agentic/references and
  source/cli: every hit is unrelated (wardnet's own daemon, docker-compose daemon state). The
  word does not appear anywhere in lydite's own design for the dashboard.
- The React dashboard (`source/web/`, still empty) is built with Vite and embedded into the
  lydite binary via `go:embed`, so `lydite report` (no such command exists yet;
  `source/cli/cmd/lydite/reports.go` exists as a different, unrelated file of the same rough
  name) generates either a self-contained offline HTML (`vite-plugin-singlefile`, ~900KB,
  bounded window inlined) or the same bundle served as a static hosted asset that fetches JSON
  live.
  - "Publishing stays out of the CLI": deploy credentials never enter the CLI process; hosting
    happens through a separate flow, not `lydite` itself deploying anything.
- Hosted mode: a public static bundle with **no data baked in**. A viewer authenticates via a
  GitHub App (not OAuth App — deliberate, `contents: read` scoped per-repo, not all-or-nothing)
  through `source/cloud-services/oauth-exchange` (a Cloudflare Worker doing only the OAuth
  code-for-token exchange), then the browser reads the ledger branch directly from GitHub's
  Contents API using the viewer's own token. lydite operates no data store and no multi-tenant
  service (ADR 0009 explicitly rejects that as a deferred business decision, not an architecture
  gap).
- This binds the ledger's file layout: every partition must stay under GitHub's 1MB Contents API
  cap (`quality-history.md`'s month-splitting into `.ndjson`/`.1.ndjson`/etc. exists because of
  this).
- Go code the dashboard would reuse: `internal/ledger` (the NDJSON format, `BranchState` replay,
  the daily projection) and `internal/gitstate` (branch read/write) are the two packages whose
  shapes the dashboard's data-fetching (client-side, in TypeScript, against the same GitHub
  Contents API) has to mirror/agree with — there is no shared Go/TS schema codegen today, so a
  layered refactor introducing typed contracts here would need to keep the NDJSON record schema
  and the TS reader in sync by hand or add generation.

GitHub access today (three paths, not fully reconciled into one abstraction):
- `internal/forge.Client` — hand-rolled `net/http` client (deliberately not a generated SDK;
  package doc explains why: 13 audited methods vs. the whole platform surface), used for status/
  comment/review-thread writes. It's a concrete struct (`*Client`), not an interface — no seam
  for a fake exists at that boundary yet; tests point `BaseURL` at a local `httptest.Server`
  instead.
- The relay (`source/cloud-services/pr-relay`), a Cloudflare Worker using GitHub App identity via
  OIDC, posts PR comments on a consumer's behalf without a long-lived token in the workflow.
- Raw `GITHUB_TOKEN`/`gh` CLI usage in workflows for everything the relay and forge don't cover.
- Invariants confirmed as already-recorded rules (not rediscovered here, just verified they
  apply to any repository-abstraction layer): "resolve a write's target live" rule
  (`.claude/rules/resolve-a-writes-target-live-never-trust-the-claim-or-the-body.md`) and the
  fallback-transport three-way sort rule
  (`.claude/rules/sort-a-fallback-transports-response-into-three-not-two.md`).

Pain points / seams for a layered refactor, read directly (not from memory):
- `source/cli/cmd/lydite/coverage.go` is 2139 lines, `test.go` 2285, `mutation.go` 1285,
  `scan.go` 1168, `record.go` 1023 — these are the largest command files and the obvious
  candidates for a handler/service split.
- `internal/forge` has no interface — a repository abstraction over GitHub would need one
  introduced (or keep using the concrete client with a narrower interface defined at each
  consumer, Go-idiomatic style).
- No `fake`/`mock` types found anywhere under `source/cli/internal/*/*.go` except
  `internal/mutation/executor_test.go` — most tests exercise real code paths or a local HTTP
  test server (`internal/forge`) rather than hand-rolled fakes, suggesting the codebase leans on
  integration-style tests over interface substitution today.
