<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)"  srcset="assets/lydite-logo-horizontal-tagline-dark.svg">
    <source media="(prefers-color-scheme: light)" srcset="assets/lydite-logo-horizontal-tagline.svg">
    <img alt="lydite" src="assets/lydite-logo-horizontal-tagline.svg" width="480">
  </picture>
</p>

<p align="center">
  Unified code-quality and security scanning for Rust, TypeScript, and Go — one CLI, run identically
  locally and in CI, so "green locally" and "green in CI" can never drift apart.
</p>

`lydite` replaces ad hoc, per-repo security workflows (CodeQL, standalone `cargo-audit` jobs,
Codecov as a blocking gate) with one consistent pipeline: it auto-detects which ecosystems a repo
uses, runs each one's checks with a pinned, self-installed toolchain, and diffs test coverage
against a lazily-computed baseline — no manual setup, no "works on my machine."

## What it checks

| Ecosystem | Checks |
|---|---|
| Rust | `cargo fmt --check`, `cargo clippy` (pedantic/restriction groups come from the target repo's own `Cargo.toml`), `cargo-audit` (CVEs), `cargo-deny` (licenses + bans) |
| TypeScript | [Biome](https://biomejs.dev)'s `security` and `correctness` rules, using a toolchain `lydite` bundles and pins itself — independent of whatever (if anything) the target package declares in its own `devDependencies`. Biome parses TypeScript with its own parser, so no compiler version can constrain it |
| Go | `gosec`, `govulncheck` |
| All of the above | [Semgrep](https://semgrep.dev) |

Every tool is pinned to an exact version and installed into a `lydite`-managed cache directory the
first time it's needed — nothing is ever silently run at whatever version happens to already be on
`PATH`. Those pins live in real package manifests that Dependabot watches, so a pinned security
toolchain can't quietly go stale while still reporting `[PASS]`.

## Install

```sh
curl -fsSL https://github.com/lydite/lydite/releases/latest/download/install.sh | sh
```

This installs to `~/.local/bin` by default (override with `LYDITE_INSTALL_DIR`); pin a specific
version with `LYDITE_VERSION=1.2.3 curl ... | sh`. Update in place any time with `lydite update`.

> **Don't** `go install lydite/lydite/cmd/lydite@latest` — the module path is deliberately not a
> resolvable `github.com/...` import path (see [AGENTS.md](AGENTS.md)), so `go install` won't find
> it. Use the installer above.

## Usage

```sh
lydite scan --dir .          # run every check over every declared component (default ".")
lydite test --dir .          # run each declared component's test suite, services and all
lydite version
lydite update                 # self-update to the latest release
```

`lydite scan` exits non-zero if any check fails, printing one row per check.

`lydite test` runs each component's suite and measures its coverage from the instrumented variant
that component's runner derives — no second test run, and no report to place beforehand.
`--gate-coverage` adds the comparison against a baseline cached on the `lydite` branch:

```sh
lydite test --dir .                     # run the suites and report coverage
lydite test --dir . --gate-coverage     # ...and gate it against the baseline
lydite test --dir . --no-coverage       # the fast path: no instrumentation, no coverage rows
```

Gating is explicit because measuring is local and gating pushes to a shared branch — a developer's
`lydite test` must never write to it. A run that measured without gating says so: its coverage rows
carry a status that is not a pass, so a workflow that forgot the flag reports no green it did not
earn.

Coverage is reported and gated at three altitudes — per component, per language, and for the
repository — all summed from one measurement per component, so they cannot disagree. A JavaScript
component needs `@vitest/coverage-v8` (or `-istanbul`) in its own dependencies; lydite will not add
a dependency to the repository it is about to gate.

See [AGENTS.md](AGENTS.md#coverage) for how the baseline is computed, cached and carried forward.

## Components

`.lydite/components.yml` declares what a repository builds and tests. A component is the unit that
language's own build tool treats as a whole — a Cargo workspace, a Go module, a JavaScript
workspace — and `lydite test` runs each one's suite:

```yaml
components:
  - name: cli
    dir: cli
    runner: go-test
    args: ["-race", "./..."]
```

The runner names the test command and thereby the language: `go-test`, `cargo-nextest`,
`cargo-llvm-cov-nextest`, `vitest`, `jest`, or a raw `command:` for anything else. lydite
orchestrates around that command and never learns to run anyone's tests.

A component whose suite needs services points at a compose file, and lydite brings them up before
the suite and takes them down after it — including when the run fails:

```yaml
components:
  - name: api
    dir: go/api
    runner: go-test
    compose:
      file: ./compose.yaml
      up: [db]
      wait: healthy      # healthy | started | none
    setup: ["make migrate"]
```

lydite owns no service schema and hard-codes no container runtime: it probes for docker or podman
and names the one it found. `wait: healthy` needs the compose service to declare a healthcheck, and
is refused rather than quietly downgraded when it does not.

Components run concurrently. `--concurrency` bounds how many at once (4 by default, or `max` for
one slot per selected component), and two components that publish the same host port — or that are
rooted at the same directory — run one after the other, because those conflicts are physical.

Components are declared rather than discovered, because the declaration is the reviewable
statement of what gets tested and its history is the record of every change to that. See
[ADR 0016](docs/adr/0016-components-and-lydite-run-tests.md).

**The declaration is what `lydite scan` reads too.** A component names a runner, the runner
implies a language, and its `dir` is where that language's checks run — nothing walks the tree
for manifests. A repository that declares no components is one `lydite scan` refuses to run over
rather than one it reports as clean; source no component's checks reach is named on stderr. See
[ADR 0020](docs/adr/0020-scan-on-components.md).

## Configuration

`.lydite/config.yml` at the repo root is optional — the default (no file) is to run every check
over every declared component. Use it to disable a language entirely, point Semgrep at a custom
ruleset, or set the coverage gates' knobs:

```yaml
rust:
  enabled: true       # false runs no Rust check over any Rust component
typescript:
  enabled: true
go:
  enabled: true
semgrep:
  enabled: true
  config: auto
toolchain:
  enabled: true   # set false to skip toolchain provisioning (air-gapped runners)
coverage:
  tolerance: 0.1  # pp the aggregate gate tolerates below baseline (patch gate: coverage.patch.tolerance)
  floor: 0        # minimum coverage any single component must reach; 0 = off
```

Every coverage figure is summed line counts (`Σ covered / Σ total`) over the components it covers,
so a big component and a small one contribute in proportion to their size. That is the honest
headline, and it is deliberately blind to a small component with no tests at all — `coverage.floor`
is the opt-in gate for that second question.

See [AGENTS.md](AGENTS.md#configuration) for the full schema and merge semantics.

Note there are no toolchain *versions* in there. lydite makes sure the Go, Rust and Node runtimes
its checks need are present at the version each ecosystem requires, and it reads that version from
the files your repo already has — the `go`/`toolchain` directives in every `go.mod` it discovers,
`rust-toolchain.toml`, `engines.node` or `.nvmrc`. A toolchain already on PATH that satisfies the
declared version is used as-is, so on a normal CI runner this costs nothing. See
[AGENTS.md](AGENTS.md#toolchains) for what gets provisioned and how.

## GitHub Actions

The action installs lydite, runs the checks and the suites, posts a single sticky PR comment
summarizing both, and optionally reports to the Semgrep AppSec Platform and/or Codecov:

```yaml
permissions:
  contents: write       # lydite test --gate-coverage caches baselines on the lydite branch
  pull-requests: write  # for the PR summary comment
steps:
  - uses: actions/checkout@v7
    with:
      fetch-depth: 0    # the coverage gate needs full history to resolve the base commit
  - uses: lydite/actions/scan@v1
    with:
      semgrep-app-token: ${{ secrets.SEMGREP_APP_TOKEN }}  # optional — omit to keep Semgrep local-only
      codecov-token: ${{ secrets.CODECOV_TOKEN }}          # optional — omit to skip the Codecov upload
```

Each half can be turned off independently if a repo only wants one of them, or isn't ready to grant
`contents: write` yet. See `action.yml`'s own input descriptions for the full list (`dir`,
`version`, `github-token`, and the two optional tokens).

Note there is no input for anything about how coverage is produced: lydite runs each component's
own instrumented variant and writes the report itself. `dir` is the one thing that cannot move into
`.lydite/config.yml`, since the file lives *at* the scan root and lydite has to be told the root
before it can read its own config.

`@v1` is a floating major alias, moved onto each new `v1.x.y` release rather than pinned — so
consumers pick up scanner fixes without a bump PR every time. Pin an exact `@v1.x.y` instead if you
need a release to stay put. See the `bump-version` skill for how a release is cut and the alias
moved.

## Contributing

See [AGENTS.md](AGENTS.md) for the development commands, package layout, and conventions.

## License

MIT — see [LICENSE](LICENSE).
