# Tool version pins

> **The reference for every pinned tool version, `internal/pins` and `tools/pinsync`.**

lydite pins every tool it runs, which is the whole premise — and for a long time every one of
those pins was a Go string constant, invisible to Dependabot. A pinned security toolchain that
nothing ever ages out is a scanner that quietly goes stale while still reporting a pass, which
is the worst available failure. Every pin now lives in a real package-manager manifest that
Dependabot watches, colocated with the package that uses it:

| Tool(s) | Manifest | Runtime source |
|---|---|---|
| @biomejs/biome | `internal/typescript/biome-pin/package.json` + lock | the manifest itself (`npm ci`) |
| cargo-audit | `internal/rust/cargo-audit-pin/Cargo.toml` | parsed by `internal/rust/pins.go` |
| cargo-nextest | `internal/runner/cargo-nextest-pin/Cargo.toml` | parsed by `internal/runner/pins.go` |
| gotestsum | `internal/runner/gotestsum-pin/go.mod` | **a Go constant** in `internal/runner/pins.go` — see below |
| cargo-deny | `internal/rust/cargo-deny-pin/Cargo.toml` | parsed by `internal/rust/pins.go` |
| semgrep | `internal/semgrep/requirements.txt` | parsed by `internal/semgrep/pins.go` |
| gosec, govulncheck | `internal/golang/go-pin/go.mod` | **still Go constants** — see below |

The npm toolchains install with `npm ci` from a committed lockfile into a cache directory keyed
by a **hash of that lockfile**. The old key was a hand-maintained string of concatenated version
numbers, so adding a dependency meant remembering to extend the key, and forgetting meant reusing
a cache directory that predated it — precisely how "every .ts file is silently skipped" would have
come back when the TypeScript parser was added. A lockfile hash cannot be forgotten.

**Go tool pins are the exception, deliberately.** gotestsum's version is a constant in
`internal/runner/pins.go` for exactly the reason gosec's and govulncheck's are constants in
`internal/golang`, and its manifest is a module of its own rather than an entry in `go-pin`
because a pin is colocated with the package that uses it — and because the two are installed
under different rules: `go-pin`'s tools analyse source, so they are keyed by the Go toolchain
that will build them, and a wrapper that reads another command's output is not.

**Go is the one exception, deliberately.** `gosecPkg`/`govulncheckPkg` are const expressions that
concatenate the version at compile time, and `go:embed` cannot read files inside a nested module,
so the versions can't be read from `go-pin/go.mod` at runtime. The constants stay, and
`internal/pins` is what keeps them true. `go-pin` is a **separate module** rather than `tool` directives in lydite's own `go.mod`:
declaring them in the main module works, but drags gosec's entire dependency graph (grpc, protobuf,
`google.golang.org/api`, …) into code lydite never links — measured at go.mod 17→69 lines and
go.sum 17→114 — which would then generate a stream of irrelevant Dependabot PRs.

## A version stated twice: `internal/pins` and `go run ./tools/pinsync`

Two pins are stated a second time somewhere Dependabot cannot reach: `golang.go`'s constants,
for the reason above, and `biome.json`'s `$schema` URL, which Biome never fetches and an editor
validates that file against — a stale one has every local edit checked against the wrong schema.
Dependabot edits a manifest and nothing else, so a bump arrives with the mirror still stating the
old version.

`internal/pins` is the whole of that rule: which manifests hold a version, which files restate
it, and how to read and write both. `Check` reports drift and `Write` resolves it, and
**nothing there ever edits a manifest** — Dependabot owns those, and a tool that wrote one would
be choosing a version rather than propagating one. `TestNoDrift` is the guard, in `go test`,
which is what `ci-gate` blocks on; `go run ./tools/pinsync` is the fix, and the failure names it.

**One package rather than a guard per pin.** The two it replaced each parsed their own manifest,
so `go.mod` require-line parsing existed twice and would have existed three times the moment
anything else needed it — and the copy that drifts is the one nobody is looking at. A new pin read
from its own manifest at run time has no mirror and belongs to no entry.

`.github/workflows/dependabot-pins.yml` runs `Write` on a Dependabot pull request, so the bump
carries its own mirror instead of a human pushing the fix. Two things about it are load-bearing.
It is `pull_request_target`, because a `pull_request` run from Dependabot holds a read-only token
and no secrets — so, as with `lydite-clearance.yml`, the rule executed is always the default
branch's. And **it never compiles the pull request's source**: pinsync is built from the base
checkout and reads the head checkout as data. Collapsing the two checkouts, or running `go run`
inside the head tree, would compile a branch's code in a job that can push.

**It pushes as `lydite-cicd-bot`, a GitHub App**, from `vars.LYDITE_CICD_APP_ID` and
`secrets.LYDITE_CICD_APP_PRIVATE_KEY`; with the variable unset the job says what to run and
stops. The App ID is a variable and not a secret because it is not one — it is printed on the
App's own page, and a secret nobody can check a workflow against is a secret that hides a typo.
An identity is needed at all because **a push made with `GITHUB_TOKEN` starts no workflow run** —
the pull request would carry the fix and keep the red checks that fix answers.

An App rather than a personal access token, for what it is not: a PAT carries one person's whole
account, outlives their involvement, and expires on a schedule nobody is watching. An installation
token is minted per run, lives an hour, belongs to nobody, and is bounded by what the App declares,
on the repositories it is installed on. `lydite-cicd-bot` is the identity lydite's own automation
writes under generally — commits a job produces, tags it cuts, state it records between runs — so
what it may do is stated on the App and not restated here, where a second copy would go stale the
first time a different job needs something. Writing a pin mirror needs `contents: write` and
nothing else.

It is deliberately **not** the relay's App. That one holds `pull_requests: write` and has no
endpoint that commits; widening it to write contents would undo the separation
[ADR 0022](../../docs/adr/0022-a-vendor-operated-app-and-an-oidc-relay.md) exists to keep. Two Apps for
two audiences is the ADR's own shape: one writes to lydite's repositories, the other comments on
somebody else's.

The App's key is still a secret in this repository, so this is a credential in CI. What makes that
acceptable is the same thing that makes `lydite-clearance.yml` acceptable: the job holding it runs
no repository code. It must never become the identity that records a coverage baseline — that job
does run the repository's code, and handing it this key is exactly
[#49](https://github.com/lydite/lydite/issues/49) with a nicer name.

**Each cargo tool gets its own manifest, and that is not tidiness.** cargo-audit and cargo-deny
cannot resolve in a shared dependency graph — cargo-deny's `krates` pins `petgraph =0.8.1` while
cargo-audit's `cargo-lock` pulls `0.8.2` — and a `[package]` manifest with no `src/lib.rs` doesn't
parse at all. Either way cargo errors, Dependabot's updater fails in a job log nobody reads, and the
pin silently stops being bumped: the exact failure this whole arrangement exists to prevent. `cargo
install` never shares a graph between two tools, so the conflict is invisible in normal use.
`internal/rust`'s `TestPinManifestsAreSeparate` guards against a well-meaning consolidation.

**Adding a new pin means two edits, and a cargo pin three:** the manifest, a
`.github/dependabot.yml` entry, and — for cargo only — an exclude in `.lydite/components.yml`.
A pin directory is deliberately not a component, so nothing scans or tests it; but cargo refuses
a `[package]` manifest with no target, so every cargo pin has to carry an (empty) `src/lib.rs` —
real Rust under the `cli` component that nothing builds — and `lydite scan` says so unless the
declaration excludes it. The current glob is `source/cli/internal/**/*-pin/**`.

**A cargo pin with no `src/lib.rs` is not merely untidy: Dependabot cannot read it.**
`cargo metadata` fails with `no targets specified in the manifest`, the updater errors in a job
log nobody reads, and that pin silently stops being bumped — the "pin nothing ages out" failure
ADR 0006 exists to prevent, arriving through the mechanism meant to prevent it. Both
`internal/runner` pins were in that state until this was noticed.
`cmd/lydite`'s `TestLyditesOwnDeclarationLeavesNothingUnscanned` fails when a pin falls outside
it — the replacement for `internal/config`'s deleted `TestPinDirectoriesAreExcluded`, asserting
the property rather than the pin case. Nothing enforces the Dependabot entry, and a pin no bot
bumps is a scanner that quietly goes stale. See
[ADR 0006](../../docs/adr/0006-tool-pins-as-dependabot-manifests.md).

