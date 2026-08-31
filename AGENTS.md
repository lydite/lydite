# lydite — agent guide

Lydite is a Go CLI that unifies code-quality and security scanning — SAST, SCA, linting, and
coverage gates — for Rust, TypeScript, and Go projects. It is the single entry point a developer
runs locally and CI runs identically, so "green locally" and "green in CI" can never drift apart.
It replaces per-repo ad hoc security workflows (CodeQL, standalone cargo-audit jobs, Codecov as a
blocking gate) across `wardnet`, `wardnet-cloud`, and `inforge` with one consistent pipeline.

## Commands

```sh
go build ./...                 # build the binary
go test -race ./...            # run tests
golangci-lint run ./...        # lint — must be clean before a PR
go run ./cmd/lydite            # run the CLI locally
go run ./cmd/lydite review     # referral verdict for the current branch (exit 2 = refer)
go run ./cmd/lydite test --dir ..  # run each declared component's suite (--dir is the scan root)

# The Go module is rooted at cli/, so every command above runs from there.

# Release build dry-run (produces dist/):
go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean
```

## Layout

```
cli/                           # the Go module (module path lydite/lydite); every go command runs here
cli/cmd/lydite/                # the lydite CLI (scan, test, review, coverage, version, update)
cli/internal/ui/               # the output grammar every command renders through, plus --json
cli/internal/referral/         # exemptions, disqualifiers, the referral decision (see Referral below)
cli/internal/clearance/        # the comment surface and the clearance decision (see Clearance below)
cli/internal/forge/            # the hosting platform: commit statuses, permission, comments
internal/component/             # .lydite/components.yml: what a repo builds and tests (see Components below)
internal/runner/                # a runner name to its plain/instrumented/build-only invocations
internal/nodedeps/              # how a JavaScript workspace's dependencies get installed
internal/cargotool/             # pinned cargo subcommands: version parsing and the install
internal/download/              # fetch, checksum-verify and unpack an archive safely
internal/compose/               # the services a component's suite needs (see Services below)
internal/detect/                # ecosystem + TS-package detection (walks for Cargo.toml/package.json/go.mod)
internal/config/                # .lydite/config.yml loading (opt-outs + pipeline shape — see Configuration below)
internal/toolchain/             # ensures the Go/Rust/Node runtime each detected ecosystem needs (see Toolchains below)
internal/rust/                  # clippy, cargo-audit, cargo-deny
internal/typescript/            # pinned Biome, the only TS linter (see Linters)
internal/golang/                # gosec, govulncheck (installed into a version-keyed GOBIN dir)
internal/semgrep/                # pinned Semgrep, installed via pipx
internal/coverage/               # per-language coverage percentage (see Coverage below)
internal/gitstate/               # lydite branch read/write (see Coverage below)
internal/executil/              # shared external-command runner every scanner package uses
assets/                        # the shipped logo set — the action's PR comment embeds
                                #   lydite-mark-64.png by raw URL (see below)
docs/design/                    # tokens, surface specs, and the reference prototypes (see Design)
docs/release-notes/             # _header.md + one <tag>.md per release that needs one (see Release notes)
.lydite/                       # every file that configures lydite: config.yml, components.yml,
                                #   exemptions.yml
.goreleaser.yml                 # build/release config (v2 schema)
.golangci.yml                   # lint config (v2 schema)
.github/workflows/{ci,release}.yml
.github/dependabot.yml
                                # (the composite action lives in lydite/actions, not here)
scripts/install.sh              # curl|sh installer shipped with every release
```

- Module path: `lydite/lydite` (not `github.com/lydite/lydite` — a deliberate deviation from
  the other repos in this org, to be applied there too later; do not "fix" this back).
- `lydite` ships as a single statically-linked binary (`CGO_ENABLED=0`), built for
  linux/darwin × amd64/arm64.

## Status

All six subcommands (`scan`, `test`, `review`, `coverage`, `version`, `update`) are fully implemented — every check
is a real tool invocation (not a stub). Every scanner pins its own tool version and installs it into
a lydite-managed cache directory rather than trusting whatever's already on the machine (see each
`internal/<lang>` package's doc comment for why). `update` follows the same pattern as `inforge`'s
self-update (checksum-verified binary replacement, refuses on dev builds, passive update nudge on
every other command). `lydite coverage` has been verified end-to-end against this repo's own real
`lydite` branch on GitHub, not just a local fixture. `lydite test` runs each component's suite in
turn, starting the compose services and running the `setup`/`teardown` commands it declares;
running them in parallel under a port-aware scheduler is what remains.

## CI

`.github/workflows/ci.yml` runs three jobs on every push/PR to `main`: `lint` (golangci-lint),
`build & test` (`go build`/`go test -race`), and `self-scan` — lydite builds itself and runs
`lydite scan --dir .` against its own repo. `self-scan` is dogfooding, not a formality: it's the
only job that exercises the actual scan/report path end-to-end against a real repo, and it already
caught a real bug once (see the git history around the `go-version: "1.26.5"` pin below).

**Pin the exact Go patch version in workflows (currently `"1.26.6"`), never a bare minor (`"1.26"`).**
`actions/setup-go`'s `go-version: "1.26"` resolves to whatever `1.26.x` patch it has
cached/available, which is not necessarily the version this repo's `go.mod` `toolchain` directive
pins — and critically, `go install`-ing an *external* tool (gosec, govulncheck) does **not** consult
the current module's `go.mod` toolchain directive the way building the module itself does. This bit
us for real: `self-scan`'s `govulncheck` step passed locally (toolchain directive respected) but
failed in CI (setup-go had installed an older, vulnerable patch) until `go-version` was pinned to the
exact `1.26.5`. If `go.mod`'s `toolchain` line is ever bumped, update every `go-version:` in
`ci.yml`/`release.yml` to match in the same change.

`internal/toolchain` (see Toolchains below) now also sets `GOTOOLCHAIN` from the scanned repo's own
`go.mod` before running any Go tooling, which fixes this class of drift for **consumers** — they no
longer need to pin `go-version` on lydite's account. Keep the pins here regardless: this repo's
`lint` and `build & test` jobs invoke `go` directly rather than through lydite, so nothing in that
mechanism covers them, and `self-scan` benefits from the pin holding independently of the feature
it is dogfooding.


## Components: `.lydite/components.yml` and `lydite test`

A repository declares its **components**, and lydite builds, tests, measures and gates each one.
`internal/component` parses the declaration and `internal/runner` turns a runner name into
invocations; `lydite test` runs the suites. See
[ADR 0016](docs/adr/0016-components-and-lydite-run-tests.md).

```yaml
components:
  - name: cli                      # unique; names the matrix job and every report row
    dir: cli                       # component root, relative to the scan root
    runner: go-test                # implies the language
    args: ["-race", "./..."]
    watch: ["Makefile", "VERSION"] # paths outside dir that invalidate this component
    depends_on: [sdk]              # declared, because the edge is not always derivable
    env:
      FOO: bar
    compose:
      file: ./docker/compose.yaml  # relative to the component root
      up: [db]                     # default: every service in the file
      wait: healthy                # healthy | started | none
    setup: ["make migrate"]
    teardown: ["rm -rf ./data"]
    mutation: false                # opt-out; the default is true
```

**A component is the unit its build tool treats as a whole** — a Cargo workspace, a Go module, a
JavaScript workspace — not a deployable. Nothing enforces it, because it cannot be read off a
manifest; it is stated in the package doc because it is the rule most likely to be got wrong.
Eleven crates behind one `cargo --workspace` invocation are one component: declared as three, the
workspace compiles three times and provisions three copies of everything the suite needs.

**`lang` is derived from `runner`, never declared.** `cargo-nextest` can only be Rust, and a
second statement of the language could only disagree with the first.

**Unknown keys are rejected**, the same stance `referral.Parse` and `config.validateLinter` take. A
dropped key means a component configured differently from what its author wrote — a suite running
without the environment it declared — while every run still reports a result.

`Load` also validates against the tree: unique names, a `dir` that exists and does not escape the
scan root, `depends_on` that resolves to declared components, and no cycles. A dangling edge is
rejected rather than dropped, because the edge exists to make a dependent run on a change to its
dependency and an edge naming nothing silently stops doing that while the dependent keeps passing.

### Runners: three invocations of one suite

`internal/runner` maps a runner name onto three variants of the same suite, because lydite needs
all three and they must not disagree about which tests they run:

| variant | why it exists |
|---|---|
| plain | the fast path — mutation runs the suite once per mutant |
| instrumented | the coverage gate, and mutation's baseline |
| build-only | tells an *unviable* mutant from a *killed* one; both exit non-zero |

Deriving them from one declaration is the point. **Instrumentation is not a flag that can be
spliced into an arbitrary command**: `go-test` appends `-coverprofile`, while `cargo-nextest`
replaces the runner outright with `cargo llvm-cov`. That is why a component either names a runner
or supplies a raw `command:`, which opts out of the derived variants entirely.

- `go-test` — plain `go test`; instrumented adds `-coverprofile` **and `-coverpkg=./...`**;
  build-only is `go build`. Without `-coverpkg` Go instruments only the package under test, so code
  exercised solely through another package's tests reads as uncovered and a pull request whose new
  code is fully exercised from its caller fails the patch gate on correct work
  ([#36](https://github.com/lydite/lydite/issues/36)).
- `cargo-nextest` — instrumented is `cargo llvm-cov nextest`, exporting `--json` (the aggregate
  totals) *and* `--lcov` (the per-line hits the patch gate reads) from one run: the JSON export has
  no per-line data at all, so neither is derivable from the other. Build-only is `cargo build
  --all-targets`, because `cargo build` alone never compiles the test targets and a test-only
  compilation error is exactly what separates an unviable mutant from a killed one.
- `cargo-llvm-cov-nextest` — the same, with the plain variant already instrumented, for a
  repository that has decided to pay for instrumentation once.
- `vitest` and `jest` — instrumented adds `--coverage`; build-only is `tsc --noEmit`, since a
  JavaScript test run has no compile step and a syntactically broken mutant would read as a test
  failure.

**A component's runner is pinned and installed, exactly like a scanner.** A test
runner left to whatever a machine happens to carry decides which tests run and what
a failure looks like, so a verdict would vary by runner — and unlike a stale scanner
an absent one is not a degradation but a component that cannot run at all
(`error: no such command: nextest`). `internal/cargotool` holds the version parsing
and the version-keyed install both `internal/rust` and `internal/runner` use, so the
rule exists once. The invocation stays `cargo nextest run` rather than an absolute
path into the cache, because that is what a reader can re-run from a failure detail;
`Invocation.Env` prepends the pinned binary's directory to PATH, so an older one
already on the machine cannot win.

**The prebuilt release is taken first, and `cargo install` is the fallback.** Building
cargo-nextest from source is around seven minutes; the published archive is three
seconds, and a cold cache end to end is eleven. That is not a speed-up but the
difference between a first run someone waits through and one they abandon. The
digest comes from the release's own `.sha256` and is checked before a byte reaches
a caller — lydite is about to put this binary on PATH and execute it — and the
install stages into a sibling directory and is renamed into place, so an interrupted
download cannot leave a directory the next run reads as complete. A platform nextest
publishes nothing for, a blocked download, or a digest that does not match all fall
back to the source build, saying so on stderr rather than silently making a first run
seven minutes long. Linux takes the **musl** target: a musl-linked static binary runs
on a glibc distribution as well as on Alpine, and the reverse is not true.

`internal/download` holds the fetching, verifying and unpacking that
`internal/toolchain` already had, because the parts that must not vary between the two
callers are the security-relevant ones — a second copy of a path-traversal guard is a
second chance to get it wrong, and only one of the copies would be found. Its
`ExtractTarGz` takes the number of leading components to strip: a toolchain tarball
wraps everything in one directory, and a single-binary release tarball has no wrapper
at all, where stripping would discard the only entry.

Caching `~/.cache/lydite` still matters, and lydite's own `proving ground` job does it,
keyed on the pin manifests so a Dependabot bump misses and installs what it just
pinned. `cargo-llvm-cov` is still **not** installed, which is the gap that poisons a
baseline under `coverage.source: run`.

**A JavaScript component is installed before it is run.** A fresh checkout has
no `node_modules` and every import fails before a single test is collected, so the
`vitest` and `jest` runners carry a `Prepare` step and the others carry none —
`go test` and `cargo` fetch what a build needs on the way past. The rule lives in
`internal/nodedeps` because the coverage gate asks the same question of the same
tree, and two copies would answer it identically until one learned about a package
manager the other had not. The lockfile is the declaration (`package-lock.json` →
npm, `yarn.lock` → yarn, `pnpm-lock.yaml` → pnpm); every detected form is a
*frozen* install, since one that may rewrite the lockfile would have lydite change
what the repository resolves to and then gate the result. A root carrying two
lockfiles resolves to nothing rather than a guessed priority order, and
`typescript.install` is how such a repository says what it means. There is
deliberately **no key naming the package manager**: the lockfile already states it,
and a second statement could only drift.

An install that fails stops the suite, with a row saying so. A JavaScript suite run
without its dependencies fails at import, naming the tests rather than what is
actually missing — the same misattribution a suite run without its database
produces.

Nothing in `internal/runner` executes anything, and its tests assert argv — the same stance
`internal/rust` and `internal/typescript` take, for the same reason: a unit test that shells out to
a foreign toolchain tests the machine it runs on. **Two of the three languages therefore have no
repository here to run against** — `web/` is empty and the only `Cargo.toml` files are pin
manifests, deliberately not components. `ci-end2end.yml`'s `proving ground` job closes that,
running `lydite test` against every component of
[`lydite/proving-ground`](https://github.com/lydite/proving-ground) on a bare checkout — no
`node_modules`, no services started, nothing prepared by the workflow, because a step doing either
would hide the case a consumer actually hits.

Where a runner emits JUnit, the invocation names where it lands: the quality-history ledger
([#26](https://github.com/lydite/lydite/issues/26)) records test counts, which no coverage report
carries.


### Output: captured, not streamed

Every component's output goes to `.lydite-reports/<name>/test.log` under the scan
root — the suite, the install, the setup and teardown commands, and the container
lifecycle. A failing row carries the last 40 lines under it and names the log; a
passing row names the log in `--json` and says nothing in the terminal.

**This is the opposite of what the scanners do, and the difference is real.** A
scanner's findings *are* the result, so `executil.Run` streams them live. A test
suite's output is thousands of lines of passing tests, and a CI log carrying all
of it buries the one component that failed under the container lifecycle of the
ones that did not — which is exactly how a `no such command: nextest` ends up
fifty lines above the verdict that reports it. `executil.RunOutput` is the version
that writes to a caller-chosen writer, and the caller is the component's log.

The tail is under the row deliberately, duplicating what the log holds. A reader
looking at a red row should not have to scroll, and a reader who needs more than
40 lines has the path. `ui.Row.Log` carries that path into `--json` — it is not
rendered in the text grammar, since a failing row already names it where the
reader is looking and a passing one would put a path nobody wants on every line
of a clean run. A consumer cannot parse a path back out of prose, which is what
lets a PR comment link the output of the one component that failed.

`--stream` mirrors everything to stderr as well. It exists for the case a captured
file cannot serve: a suite that hangs prints nothing until it is killed. Stderr and
never stdout, because stdout carries the report and, under `--json`, a document a
suite's output would make unparseable.

### Services: a compose file, and no schema of lydite's own

`internal/compose` starts what a component's suite needs and stops it again.
lydite owns **no service schema**: images, ports, environment and healthchecks are
compose's job already, and a second description here could only agree redundantly
or drift.

It hard-codes **no container runtime** either. Which implementation is present is a
property of the machine, not of the repository — podman on a laptop, docker on a
runner — so `Probe` tries `docker compose`, `podman compose`, then the standalone
`docker-compose`, and the run says which it chose on stderr. It *runs* each
candidate rather than looking it up on PATH: `docker` exists on a machine whose
daemon is stopped and on one with no compose plugin, and neither can start a
service. A component declaring no services is never probed at all, so a repository
without services runs on a machine with no container engine.

**`wait: healthy` requires a declared healthcheck, and is refused without one**
rather than degrading to `started`. A suite racing a database that is not yet
listening is the flakiest thing a pipeline can contain, and the failure is
attributed to the test rather than to the wait. It is also the default, so a
component that says nothing gets the strict answer. The waiting itself is compose's
`--wait`, not lydite polling `ps`: the healthcheck in the file is what defines
ready, and a second implementation here would be lydite deciding what healthy
means. `started` and `none` produce the same invocation, because `up --detach`
already returns only once every container has started — the distinction is the
repository's statement of intent, and `healthy` is the one that changes the command.

**Teardown runs on every path out**, including a `setup` that failed halfway, which
is when a half-applied migration most needs undoing. It is `down --volumes`: a suite
that truncates and reseeds is deterministic only if it starts from nothing. Both
teardowns get a context of their own, because the run's may already be cancelled and
a cancelled teardown is the leak this exists to prevent. A failing `teardown` command
turns a passing component into a failing one — it has left state the next run
inherits — but never masks a failure that already happened, since the earlier one is
what the reader has to act on.

Each component's stack gets its own compose project (`lydite-<name>`), so two
components cannot adopt each other's containers and teardown removes exactly what
this run started. Published **host ports** are read while the file is open, in both
of compose's port syntaxes: the scheduler serialises two components that publish the
same one, and the conflict is physical — two services called `db` on different ports
do not collide, and a `db` and a `postgres` on the same port do.

`--keep-services` leaves them running. It is a flag and never a key in the
declaration, because whether to keep services between runs is a choice about one
invocation rather than a fact about the repository.

Unknown keys in a compose file are **accepted**, unlike everywhere else lydite parses
YAML. The file is compose's, not lydite's, and rejecting a key lydite has no opinion
about would make lydite's version the ceiling on what a repository may write in a
file lydite does not own.

## Configuration

Every file that configures lydite lives in `.lydite/` at the scan root: `config.yml` here,
`components.yml` above, and `exemptions.yml` under Referral below. One directory rather than a
family of dotfiles beside it, because a dotfile next to a dot-directory that both configure the
same tool is an arrangement nobody can predict the shape of from either half.

`.lydite/config.yml` is optional and does two things, and tuning severity or suppressing
individual findings is neither of them (that's what a fix-up pass + inline `#nosec`/`nosemgrep`
annotations in the scanned repo are for):

- **Opt out** of what lydite's zero-config default already does (scan everything detected, every
  check enabled), plus the numeric gate knobs (`coverage.tolerance`, `coverage.patch.tolerance`).
- **Describe the repo's pipeline** — `coverage.source` and the `coverage.{go,rust}` report paths.
  These narrow nothing; they state a fact about how the repo is built. That fact is the same for
  every invocation in that repo, which is exactly why it belongs in a file at the scan root rather
  than in a flag or action input each caller has to remember to repeat identically.

See `internal/config/config.go` for the full schema; shape:

```yaml
rust:
  enabled: true          # set false to skip Rust entirely even if a Cargo.toml is detected
  exclude: []            # extra directory names to skip during ecosystem/package detection
typescript:
  enabled: true
  exclude: ["legacy-app"]
  linter: biome          # the only accepted value. The retired "eslint" is rejected with an
                          # error rather than silently run under Biome (see Linters below).
  install: ""            # override coverage's install-command auto-detection, e.g.
                          # "corepack enable && yarn install --immutable" (see Coverage below)
go:
  enabled: true
  exclude: []
semgrep:
  enabled: true
  config: auto           # override to a custom registry ref/path if needed
toolchain:
  enabled: true          # set false to keep the diagnostics but never download/install
                          # (air-gapped runners, or images that preprovision everything)
  # go/rust/node: deliberately unset. The versions come from the repo's own
  # manifests — see Toolchains below. These keys exist only as a local override.
coverage:
  source: run            # who produces the coverage data. "run" (default) has lydite execute each
                          # ecosystem's test suite itself; "report" has it never execute anything and
                          # only parse what a prior CI job already wrote. Not "run tests or not" —
                          # that describes lydite's behavior; this names which side of the pipeline
                          # owns coverage production. Anything else is rejected, never silently
                          # treated as "run" (see the section below).
  go:
    report: coverage.out # only read under source: report, and usually unnecessary — each discovered
                          # module's own coverage.out/cover.out/c.out is found without it. A bare
                          # path applies when exactly one module is discovered; a repo with several
                          # uses a mapping instead:
                          #   report:
                          #     wctl: coverage/wctl.out
                          #     sdk/wardnet-go: coverage/sdk.out
  rust:
    report: coverage/llvm-cov.json  # the cargo-llvm-cov JSON export — the aggregate percentage
    lcov: coverage/lcov.info        # the lcov export — patch coverage's per-line data. Two paths
                                     # because the JSON export has no per-line data at all. Same
                                     # bare-or-mapping shape as go.report, keyed by crate dir.
                                     # There is deliberately no `typescript:` here: Istanbul/Vitest
                                     # write coverage/{coverage-summary.json,lcov.info} by fixed
                                     # convention, so there has never been anything to override.
  tolerance: 0.1         # pp a language's aggregate coverage may dip below its baseline before
                          # the gate fails; absorbs sub-tenth measurement noise ("86.1% vs
                          # baseline 86.1%, regressed 0.0%"). Compared at display precision
                          # (tenths); 0 = fail any dip the report can show. Must be finite and
                          # non-negative — Load rejects anything else.
  floor: 0               # minimum coverage any single measured unit (Go module, Rust crate/
                          # workspace root, TS package) must reach. 0 = off, which is the default:
                          # it is opt-in, so upgrading never starts failing a repo over a gap it
                          # has always had. This is the signal line-weighting removes — see
                          # Aggregation below.
  patch:
    tolerance: 0.1       # the patch gate's own dip allowance — deliberately independent, so
                          # loosening the aggregate knob never weakens the untested-new-code check
```

Omitting the file, or omitting a section/key within it, keeps that value at its default — see
`internal/config/config_test.go` for the exact merge semantics.

## Linters: Biome, and only Biome

Biome backs the TypeScript check. `typescript.linter` accepts `biome` and nothing else; the
retired `eslint` value is **rejected** by `config.validateLinter` with an error naming the
removal, rather than accepted and quietly run under Biome. A repo that set that key stated
which rule set it gates on, and switching it silently would change what the scan measures
while every run still reported a pass — the same reason an unknown value is rejected rather
than defaulted. `LinterESLint` stays defined for no purpose but to be recognised and refused.

**Biome is not a replacement for `eslint-plugin-security` — Semgrep is, and it already ran.**
Biome's `security` group is six JSX/eval/secret rules (`noBlankTarget`,
`noDangerouslySetInnerHtml`, `noDangerouslySetInnerHtmlWithChildren`, `noGlobalEval`,
`noScriptUrl`, `noSecrets`), of which only `noGlobalEval` coincided with anything the plugin
had; lydite additionally gates on the **`correctness`** group, so the TypeScript check is not
"security findings only" the way the others are. Measured against a fixture carrying all four
classes, Semgrep at `config: auto` covers `detect-child-process` (with taint — it names the
tainted argument) and `detect-non-literal-fs-filename` (as a path-traversal finding), partly
covers `detect-unsafe-regex` (dynamic patterns via `detect-non-literal-regexp`, but no static
ReDoS analysis of a literal), and does not cover `detect-object-injection` at all. Several of
those Semgrep rules are ports of the ESLint ones and are stronger than the originals. The real
gap is `detect-object-injection` — the plugin's most commonly disabled rule — plus
literal-pattern ReDoS. Do not rebuild either as a Biome GritQL plugin: GritQL matches syntax
against the CST with no dataflow or taint, so it could only produce a weaker copy of what
Semgrep already does. See [ADR 0008](docs/adr/0008-biome-as-the-only-typescript-linter.md),
and [ADR 0005](docs/adr/0005-optional-biome-linter.md) for the opt-in it supersedes.

**Why it went.** ESLint's TypeScript support is a *compiler* dependency, not a linter one:
`@typescript-eslint/parser` declares `typescript` as a peer with an
upper bound (`>=4.8.4 <6.1.0`), so lydite's own pin manifest had to carry a `typescript`
inside that window, and the window moves only when upstream ships support for a new compiler.
`internal/typescript`'s package doc had recorded exactly this hazard — and a Dependabot PR
bumped `typescript` across the ceiling anyway and merged, after which `npm ci` on the
committed lockfile failed with `ERESOLVE` and every consumer with TypeScript got an install
error instead of a lint result. CI stayed green throughout, because lydite's own repo has no
TypeScript to scan: its only `package.json` files are the pin manifests, which `.lydite/config.yml`
excludes by name, so `self-scan` cannot reach that code path. Biome parses TypeScript with its
own Rust parser and depends on no compiler package, so `biome-pin` has no peer range to cross
and the failure class does not exist for it.

Four things about the Biome integration were established against Biome directly rather than
from its docs. The pin is now **2.5.10**, and `biome.json`'s `$schema` URL must be bumped with
it — `TestBiomePinMatchesConfigSchema` fails the build otherwise, the same mirror guard
`internal/golang/pins_test.go` provides for the Go constants. Re-verified on 2.5.10 where
noted:

- **`files.includes` negations work from an out-of-tree config** (re-verified on 2.5.10: an
  `eval` inside `dist/` is not reported). Biome resolves config globs
  "relative to the folder the configuration file is in", and lydite stages `biome.json` in its
  cache directory — so this looked like it would silently ignore nothing. It doesn't: `**`-prefixed
  negations are depth-agnostic and match correctly. But they are doing real work — with them
  removed, Biome lints `dist/` and reports findings inside a minified production bundle.
  `TestBiomeConfigIgnoresMatchesDefaultSkipDirs` guards it.
- **`--config-path` beats the scanned project's own root `biome.json`** (re-verified on 2.5.10).
  A project config setting
  `linter.enabled: false` is ignored, so lydite's verdict doesn't vary with what the target repo
  declares — the same stance `internal/typescript`'s doc comment takes generally.
- **A `biome.json` in a *subdirectory* does NOT abort lydite's run**, and the earlier claim that
  it does was a mis-scoped reproduction. Biome errors with "Found a nested root configuration, but
  there's already a root configuration" **only when it resolves configuration from the tree
  itself**. lydite always passes `--config-path`, and under that flag a nested `biome.json` —
  with `"root": true`, or with no `root` key at all — is simply ignored: Biome exits 0, lints
  normally, and the report is clean. Confirmed on 2.5.8 and 2.5.10 with lydite's exact
  invocation, so this was never version drift.
  The consequence is that `lintDirBiome`'s `biomeNestedRootConfig` branch is unreachable as long
  as `--config-path` is passed. It stays, because it stops being unreachable the moment anyone
  drops that flag — and dropping it is a plausible change, since it is what would let a project's
  own Biome config be honoured. What must not survive is the claim that it fires today.
- **A nested config declaring `"root": false` is *merged* into lydite's config.** Its rules then
  fire in our run. `reportableBiome`'s category allowlist contains that — merged `lint/style/*`
  is dropped. The same merge is a real limitation in the other direction that cannot be closed
  from here: a nested config could set `"security": "off"` and silently narrow what lydite
  checks.

**A check whose findings never stream must set `executil.Result.Detail`.** `executil.Run`
streams every tool's stdout/stderr live, so for gosec, clippy, cargo-audit and Semgrep the
findings are on the terminal and in the log `action.yml` captures before anyone reads the
`Result`. Biome is the exception: its report goes to a file via `--reporter-file` so its own
chatter cannot corrupt the JSON, so nothing streams and `Output` holds no findings.
`cmd/lydite/scan.go`'s `report` prints `Detail` under a failing check — without it a
developer sees a bare `✗ biome(.)` row and has to re-run the pinned toolchain by hand to learn why,
and the PR comment carries nothing at all. Detail lines are **indented**, and that is
load-bearing rather than cosmetic: a finding quotes source, which can contain anything the
source contains — including something shaped like a verdict — and indentation is what stops
it from beginning a line the way a status row does.

`recommended: false` in the bundled `biome.json` is not tidiness — without it Biome's default
preset enables `style` and `suspicious` too, and lydite would start failing PRs over opinions it
never agreed to enforce. The formatter and assist are explicitly disabled for the same reason:
lydite is not a formatter and must never report a formatting diff as a finding.

## Tool version pins

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
| cargo-deny | `internal/rust/cargo-deny-pin/Cargo.toml` | parsed by `internal/rust/pins.go` |
| semgrep | `internal/semgrep/requirements.txt` | parsed by `internal/semgrep/pins.go` |
| gosec, govulncheck | `internal/golang/go-pin/go.mod` | **still Go constants** — see below |

The npm toolchains install with `npm ci` from a committed lockfile into a cache directory keyed
by a **hash of that lockfile**. The old key was a hand-maintained string of concatenated version
numbers, so adding a dependency meant remembering to extend the key, and forgetting meant reusing
a cache directory that predated it — precisely how "every .ts file is silently skipped" would have
come back when the TypeScript parser was added. A lockfile hash cannot be forgotten.

**Go is the one exception, deliberately.** `gosecPkg`/`govulncheckPkg` are const expressions that
concatenate the version at compile time, and `go:embed` cannot read files inside a nested module,
so the versions can't be read from `go-pin/go.mod` at runtime. The constants stay and
`internal/golang/pins_test.go` fails CI if a Dependabot bump to `go-pin/go.mod` isn't mirrored into
them. `go-pin` is a **separate module** rather than `tool` directives in lydite's own `go.mod`:
declaring them in the main module works, but drags gosec's entire dependency graph (grpc, protobuf,
`google.golang.org/api`, …) into code lydite never links — measured at go.mod 17→69 lines and
go.sum 17→114 — which would then generate a stream of irrelevant Dependabot PRs.

**Each cargo tool gets its own manifest, and that is not tidiness.** cargo-audit and cargo-deny
cannot resolve in a shared dependency graph — cargo-deny's `krates` pins `petgraph =0.8.1` while
cargo-audit's `cargo-lock` pulls `0.8.2` — and a `[package]` manifest with no `src/lib.rs` doesn't
parse at all. Either way cargo errors, Dependabot's updater fails in a job log nobody reads, and the
pin silently stops being bumped: the exact failure this whole arrangement exists to prevent. `cargo
install` never shares a graph between two tools, so the conflict is invisible in normal use.
`internal/rust`'s `TestPinManifestsAreSeparate` guards against a well-meaning consolidation.

**Adding a new pin means three edits, not one:** the manifest, a `.github/dependabot.yml` entry,
and an exclude in lydite's own `.lydite/config.yml`. The manifests are real `package.json`/`Cargo.toml`/
`go.mod` files, so CI's self-scan would otherwise treat each as a package to lint, a crate to audit
or a module to scan. `internal/config`'s `TestPinDirectoriesAreExcluded` covers only the exclude
half — it never reads `dependabot.yml`, and it discovers pins by a `*-pin` directory-name suffix, so
`internal/semgrep/requirements.txt` falls outside it entirely. Nothing enforces the Dependabot
entry. See [ADR 0006](docs/adr/0006-tool-pins-as-dependabot-manifests.md).

## Toolchains

lydite provisions every tool it *runs* — gosec/govulncheck via `go install` into a version-keyed
cache, cargo-audit/cargo-deny via `cargo install`, Biome via npm, Semgrep via pipx — under the
"pin the exact toolchain, don't reuse ambient installs" principle in `internal/golang`. The one
thing it long did *not* provision was the language toolchain it does that provisioning **with**:
`go`, `cargo` and `node` were simply assumed to be on PATH. `internal/toolchain` closes that.

Nothing was visibly broken before, and that matters for judging changes here: on a GitHub-hosted
runner Go is preinstalled and Go 1.21+ fetches whatever `go.mod` asks for, so the assumption held.
It was a robustness and determinism gap, not an outage — a self-hosted or container runner without
Go fails at `go install`, the version used is whatever the image ships rather than what the repo
declares, and with no shared module cache every run re-downloads.

**Versions come from the repo's own manifests, never from `.lydite/config.yml`.**

| Ecosystem | Version source |
|---|---|
| Go | the `go` and `toolchain` directives in **every** discovered `go.mod`, highest wins |
| Rust | `channel` in `rust-toolchain.toml`, else the legacy bare `rust-toolchain`, per discovered crate |
| TypeScript | `engines.node` in each detected package's `package.json`, else `.nvmrc` (package, then scan root) |

Those files are already authoritative and already enforced by the language's own tooling, so a
second copy in lydite's config could only agree redundantly or drift silently — and a stale
duplicate is worse than none, because it reads as authoritative. `toolchain.{go,rust,node}` in
`.lydite/config.yml` exist purely as a deliberate local **override**, which is a different thing from a
parallel source of truth. `toolchain.enabled: false` keeps the diagnostics and skips the
downloads.

**An ambient toolchain that already satisfies the declared version is used as-is.** Downloading
something already present and correct is pure cost, and on the runners lydite actually runs on
that is the common case — so the common path does no network I/O at all. `internal/toolchain`'s
`satisfied` is the single predicate for that decision; an unpinned requirement (a `stable` rust
channel, an `lts/*` .nvmrc, no declaration at all) is satisfied by anything present, since the repo
named no floor to be below. A toolchain that won't identify itself is treated as too old, because
it cannot be *shown* to satisfy a pin.

Each ecosystem provisions differently, and only one of the three downloads anything:

- **Go** delegates to `GOTOOLCHAIN`. Any Go 1.21+ can fetch another toolchain itself, through the
  module proxy and verified against Go's checksum database — better provenance than lydite could
  hand-roll — so lydite just names the version. Only with no Go at all, or one older than 1.21,
  does it download a tarball from `go.dev`, taking the SHA-256 from the release index.
  **Go is pinned even when the ambient toolchain already satisfies the declaration** — the one
  ecosystem where "satisfied" is not the same as "nothing to do". `GOTOOLCHAIN`'s default (`auto`)
  does not only *upgrade*: `go install <tool>@<version>` run outside a module, which
  `internal/golang` does to fetch gosec and govulncheck, makes the go command consult that
  **tool's** own `go.mod` and switch to whatever minimum it declares. `golang.org/x/vuln@v1.6.0`
  declares `go >= 1.25.0`, so an `auto` runner with Go 1.26 installed builds govulncheck with
  go1.25 — and a govulncheck built by an older Go rejects newer source outright. That is the exact
  failure recorded under CI above, and it reproduces on a runner whose ambient toolchain is
  perfectly correct. `pinAmbientGo` therefore sets `GOTOOLCHAIN=local` in that case.
  `local`, not the declared version: the declaration is a *minimum*, so a `go 1.26` directive
  resolves to `go1.26.0` and pinning it would downgrade a runner already on 1.26.6 — backwards,
  when the newer patch is the one carrying the security fix. lydite's own `ci.yml` sets
  `GOTOOLCHAIN: local` by hand for exactly this reason; consumers now get it automatically.
  The probe reads the ambient version with `GOTOOLCHAIN=local` set for the same reason: `go
  version` inside a module honours that module's `toolchain` directive and reports the version it
  would switch *to*, which is not the one `local` would then select — measure and pin have to come
  from the same place, or lydite concludes "ambient is fine" and lands on an older toolchain than
  it just measured.
- **Rust** delegates to rustup, which already reads the same `rust-toolchain.toml` lydite does.
  This extends rather than contradicts `internal/rust`'s existing stance that the toolchain version
  "is the target repo's responsibility via its own rust-toolchain.toml". What it adds is
  materialising the channel up front with the `clippy` and `rustfmt` components the checks need —
  rustup would otherwise install it lazily in the middle of `cargo clippy`, where a missing
  component reads as a check failure rather than a setup step. With no rustup at all, lydite says
  so and continues rather than installing rustup behind the user's back.
  Installing is not selecting, and the two come apart in exactly one case. rustup picks a toolchain
  by reading `rust-toolchain.toml` from the directory cargo runs in, which covers the normal case
  for free — `internal/rust` and `internal/coverage` both run cargo inside the crate directory, so
  the file lydite read is the file rustup reads. A version supplied by `toolchain.rust` in
  `.lydite/config.yml` has no such file, so rustup cannot see it and would install the requested channel
  and then go on running the old default. `Requirement.Overridden` marks that case and
  `provisionRust` sets `RUSTUP_TOOLCHAIN` **only** then: applying it whenever a channel came from a
  manifest would override rustup's own per-crate selection — the thing `internal/rust` says not to
  second-guess — and would break a monorepo whose crates pin different channels.
- **Node** is the only one lydite downloads and unpacks itself, from `nodejs.org`, with the digest
  read from that release's `SHASUMS256.txt`. There is no assumable equivalent of GOTOOLCHAIN or
  rustup — nvm/fnm/volta are all optional and mutually exclusive. The `.tar.gz` is taken over the
  `.tar.xz` purely because Go's standard library decompresses gzip and not xz.

**Provisioning failures warn; they do not fail the scan.** This step is preparation, not a gate,
and falling through to "whatever is on PATH" is exactly today's behavior — turning a working scan
into a hard failure over a network blip would be a regression, and if the toolchain really is
absent the next step fails loudly and specifically. What must not happen is failing *silently*, so
every reuse, substitution, skip and failure is named on stderr. Stderr, not stdout, deliberately: stdout carries the
report, and one command emits exactly one report — a warning interleaved into it would break
the grammar and, under `--json`, the document.

**Doing this in lydite rather than in each caller's CI is the point.** gt briefly grew a
`setup-go` step in its lydite stage (`19e4b77`, reverted in `a0ed107`) and it was wrong twice
over: it looked for `go.mod` at exactly one path, so wardnet — whose modules live under `wctl/` and
`sdk/wardnet-go/`, not the scan root — would silently have got nothing, and it put knowledge of Go
toolchains into gt, which would then have needed the same for Rust and TypeScript forever. lydite
already knows which ecosystems it detected and where, so it reads every manifest under the scan
root; and it is the only place that helps wardnet at all, since wardnet calls `wardnet/bulwark@v1`
directly rather than through gt. See [ADR 0004](docs/adr/0004-ensure-language-toolchains.md).

Downloads land in the same version-keyed `~/.cache/lydite` layout every other lydite-managed
install already uses, so a consumer caching that one path (as wardnet's workflow already does)
covers language toolchains too with no key change. Installs stage into a sibling temp directory and
are renamed into place, so an interrupted download can never leave a half-populated toolchain that
the next run mistakes for a complete one.

## Coverage

`lydite coverage` diffs the current branch's per-language coverage against a lazily-computed,
per-main-commit-SHA baseline cached on a dedicated `lydite` branch (never `main` — bot-owned
generated cache data, not source, needs no PR/review and never pollutes main's history):

- **A run on main records its own coverage as that commit's baseline.** When `HeadSHA == BaseSHA`
  (lydite is running on main, not ahead of it) there is nothing to gate against — the current commit
  *is* the baseline — so `cmd/lydite/coverage.go` writes what it just measured to `lydite`
  and stops. This is the *primary* way baselines get created, and the only one that works for a repo
  whose coverage is produced by a multi-job CI pipeline rather than by lydite running the tests
  itself (precisely what `coverage.source: report` exists to serve): such a repo can never *re*compute a
  historical baseline, because `computeBaselineAt`'s throwaway worktree is a bare checkout with none
  of the toolchain (`cargo-llvm-cov`, yarn/Node) or staged reports the pipeline provides — it
  measures nothing. wardnet ran that way for months: the numbers it kept failing to reconstruct in a
  worktree were numbers it had *already measured, and thrown away*, when this same command ran on
  main. Recording them costs nothing — no test re-run, no extra tooling, they are already in hand.
  **Consumers must therefore run `lydite coverage` on pushes to main, not only on PRs.**
- **Baseline writes merge; a partial run never shrinks the baseline.** A consumer's CI may
  path-filter its coverage jobs (wardnet skips frontend coverage on a Rust-only change), and the
  cache-miss worktree routinely lacks tooling, so either write path can legitimately measure only
  some of the detected languages. Recording only what was measured would silently drop the
  unmeasured language's entry — every later PR would see it as `new`, compared against nothing,
  permanently, and the "never cache an empty baseline" guard doesn't catch it because the report
  isn't empty, just partial. So *both* writers (record-on-main and `computeBaselineAt`'s cache-miss
  path) run `cmd/lydite/coverage.go`'s `carryForwardBaseline` first: it copies the entry for every
  *detected-but-unmeasured* language from the nearest prior baseline via `gitstate.PriorBaselines`,
  which starts at the recorded SHA **itself** (so a re-run or a concurrent per-language job never
  clobbers a fresher same-commit entry with an ancestor's stale value — and a shallow depth-1
  checkout can still see it) before walking first-parent ancestors, best-effort, skipping poisoned
  `{}` entries. A language that's genuinely gone — source deleted, or `enabled: false` in
  `.lydite/config.yml` (`enabledEcosystems` strips disabled languages from the detected set) — is no
  longer *detected*, so its entry still dies with it; only "the code is there but this run didn't
  measure it" carries forward. The `recorded coverage baseline` line names anything carried, and an
  unmeasured language *no* prior had is named in a stderr warning (shallow history is the usual
  culprit) instead of vanishing silently. This applies even when a main run measures **nothing**
  (a docs-only merge: every producer path-filtered away, no reports for a report-sourced repo to read) —
  the whole baseline is carried rather than skipping the record, because a main commit with no
  baseline forces every PR against it into the recompute-nothing → all-`new` → gate-on-nothing
  hole (wardnet/wardnet#899). "No coverage measured" is only printed when there was truly nothing
  to record: nothing measured *and* no priors to carry.
- `internal/gitstate.BaseSHA` resolves `git merge-base HEAD origin/main`.
- `internal/gitstate.ReadBaseline` fetches `lydite` and reads `v2/<sha>.json` (see
  `gitstate.StatePath`, and Aggregation below for why it is versioned) via `git show`
  (no checkout) — a missing branch or missing file is a cache miss, not an error.
- On a cache miss, `cmd/lydite/coverage.go`'s `computeBaselineAt` checks out `origin/main` at that
  SHA into a throwaway `git worktree` (never disturbing the caller's own working tree/branch),
  computes coverage there, and `internal/gitstate.WriteBaseline` pushes it to `lydite` (via
  another throwaway worktree — creating the branch as an orphan the first time). `lydite` is
  shared and busy — every CI run on the repo may push to it — so `WriteBaseline` fetches the fresh
  remote ref immediately before staging each attempt (the local tracking ref is as stale as the
  job's checkout, minutes old by the time a scan finishes) and retries a rejected push from the
  re-fetched ref, treating "the fetched branch already has this exact content" as success. A push
  that never lands is returned as an error: the PR-side cache-miss caller downgrades it to a
  warning (it already holds the computed baseline), but the record-on-main path must never print
  "recorded" for a baseline that was lost — that exact silent loss (stale ref → non-fast-forward
  rejection → swallowed) is how wardnet's main runs kept recording nothing while every PR
  re-hit a cache miss.
- `internal/coverage.Compute` gets the actual number per detected ecosystem: covered-over-total
  statements read from the profile for Go, `cargo llvm-cov --json`'s
  `data[0].totals.lines.{count,covered}` for Rust, and — for
  TypeScript, best-effort only — a package's own `test:coverage` script plus Vitest/Istanbul's
  `coverage-summary.json`, since unlike a linter there's no single canonical coverage-invocation
  convention to standardize on across arbitrary TS packages. A language whose coverage can't be
  measured is silently omitted from the report, not failed.
- Rust never assumes `--dir` itself is the crate/workspace root — `internal/detect.RustCrateDirs`
  discovers every independent Cargo crate/workspace root under `--dir` (deduping a workspace
  member's own `Cargo.toml` under its ancestor workspace root), and both `internal/rust.Check` and
  `internal/coverage.rustCoverage` iterate every discovered root, summing line counts across them
  the same way TypeScript sums across packages (see Aggregation below). Rust's report overrides are therefore keyed by
  crate directory (relative to `--dir`) rather than a single path — `coverage.rust.report` and
  `coverage.rust.lcov` accept a mapping of crate dir to path, and the `--rust-report`/
  `--rust-lcov-report` flags are repeatable with the same `<crateDir>=<path>` syntax. A bare value
  (a scalar in the file, or no `=` on the flag) is only honored when discovery finds exactly one
  crate, preserving the original single-crate invocation unchanged.
- Go never assumes `--dir` itself is a module root either, for the same reason and by the same
  shape (see [ADR 0002](docs/adr/0002-go-multi-module-coverage.md)) — `internal/detect.GoModuleDirs`
  discovers every module under `--dir` and `internal/coverage.goCoverage` measures each in turn,
  summing across them. `coverage.go.report` and `--go-report` are likewise keyed by module dir
  (`--go-report <moduleDir>=<path>`). This one bit for real: `go test`, `go list -m` and `go tool
  cover -func` are all module-scoped, so running them at a monorepo root measured *nothing* — and
  said so only in a warning, leaving Go absent from wardnet's gate, aggregate and patch both, while
  CI stayed green. Two guards keep that from recurring quietly: the aggregate is computed from the
  profile itself rather than by shelling out to `go tool cover -func` (same number, but it works
  from any directory), and `moduleName` rejects `go list -m`'s `command-line-arguments` answer
  instead of treating it as a module path that strips nothing.
- Generated Go files are excluded from both the aggregate and the patch gate, matched on Go's
  `// Code generated ... DO NOT EDIT.` convention (<https://golang.org/s/generatedcode>) rather than
  a filename pattern — the same signal golangci-lint and Codecov use. Without it the gate measures
  code generation rather than testing: wardnet's regenerated REST client was 983 of one PR's 1007
  changed Go lines and pinned its SDK module's aggregate at 2%. Note `go.exclude` in `.lydite/config.yml`
  cannot do this job — it narrows module *discovery*, not what is inside a module.
- **An empty baseline is never cached, and an empty cached baseline is a miss.** `Compute` silently
  omits any language whose tooling it can't run (deliberate — a repo with no coverage tooling
  shouldn't hard fail). But baseline computation runs in a *bare worktree*: no `node_modules`, no
  CI-staged report, only whatever tooling the runner happens to have. `internal/coverage.rustCoverage`
  under `SourceRun` requires `cargo-llvm-cov` on `PATH` and lydite does **not** install it (unlike
  cargo-audit/cargo-deny/gosec/semgrep, which it pins and installs itself) — so on a runner without
  it, the baseline computes to `{}`. Cached, that `{}` is indistinguishable from a real entry: every
  later PR gets a cache *hit*, every language reports `new`, and the gate enforces **nothing**,
  silently and permanently, with no way to self-heal. wardnet ran this way — all nine baselines on
  its `lydite` branch were `{}`, and its coverage gate had never once compared anything.
  `cmd/lydite/coverage.go` therefore refuses to cache an empty report, `gitstate.ReadBaseline`
  treats a cached `{}` as a miss (which heals already-poisoned branches without a manual purge), and
  `warnUnmeasured` names every detected-but-unmeasured ecosystem on stderr rather than dropping it in
  silence. If a language is missing from the gate, lydite now says so.
- A language with no prior baseline entry (new) is reported but doesn't fail the check on its own;
  a language whose current coverage dipped below its baseline by more than `coverage.tolerance`
  (default 0.1pp, compared at the report's tenth-of-a-point display precision) does. To keep
  tolerated dips from compounding — each merge lowering the baseline the next PR gates against —
  the baseline writers restore any within-tolerance dip to the prior (high-water) value when
  recording; only a beyond-tolerance drop, which was FAIL-visible on the PR that introduced it,
  resets the baseline. A language the baseline has but the
  current run doesn't splits on detection: still detected in the tree means its coverage step just
  didn't run this time (path-filtered CI job, missing tooling) and it's reported as `unmeasured`;
  no longer detected means the source actually left the tree and it's reported as `dropped`
  (wardnet/wardnet#892 showed a Rust-only PR as "typescript: no longer measured" when the TS code
  was untouched — only the frontend coverage job had been skipped). Neither fails on its own.

### Aggregation: line-weighted, plus a per-unit floor

A language's figure is the ratio of its units' **summed line counts** — `Σ covered / Σ total`
across every discovered Go module, Rust crate/workspace root and TypeScript package — never the
mean of the units' percentages. Each per-unit measurement returns an `internal/coverage.LineCount`
(Go counts statements, since that is what a Go profile records; the ratio is the same quantity),
and `Compute` returns the `Unit` list alongside the per-language percentages.

The mean is not a mild approximation, which is why the counts are carried all the way rather than
reduced early. On a nine-package pnpm monorepo, one commit that added a 39-line untested file to
the smallest package (230 lines) while adding ~1,250 well-tested lines elsewhere read as **−2.2**
under the mean and **+0.44** by line count: the gate failed a change that improved coverage, by
five times the true magnitude, in the opposite direction. A 4,494-line library and a 230-line app
had equal votes. Widening `coverage.tolerance` to absorb that is explicitly not the fix — it exists
for sub-tenth instrumentation noise, and a tolerance wide enough to hide a 2.2-point artefact hides
a genuine two-point regression too.

**A unit with no measurable lines is unmeasured, never 0%.** An empty Go profile, a crate llvm-cov
reports zero lines for, an Istanbul summary with `total.lines.total == 0` — each is returned as a
`Unit` with a zero `LineCount`, which `Unit.Measured` reports false for. `aggregate` skips it, so no
0/0 reaches a language's figure, and the floor below never fails it as a unit at 0%.

It is returned rather than dropped because dropping it is invisible. A language reports a percentage
as soon as **one** of its units measures, so `warnUnmeasured` — which works per language — stays
silent while the floor gate covers fewer units than the repo holds: a repo whose CI path-filtered
eight of nine TypeScript packages has a fully measured language and a gate that saw one package.
`floorReport` therefore names each one as `unmeasured` with a stderr warning and prints its pass
line as `N of M unit(s)`, the same "a gate that didn't run must be visibly distinct from a gate that
passed" rule Patch coverage below states.

A TypeScript package that declares no `test:coverage` script is the exception: under `SourceRun` it
has opted out of being measured, which is not the same as a report lydite expected and did not
find, so it is not a discovered unit at all. One `unmeasured` row and one warning per types-only
package on every run trains readers to ignore the tag that exists to be noticed.

**`floorReport` filters to enabled ecosystems, and it is the only gate that does.** `Compute`
measures every language `detect.Ecosystems` finds without consulting `rust/typescript/go.enabled`,
so its units — and `current` itself — can carry a language the repo opted out of. For the aggregate
that has always been one surprising failing row; for a per-unit gate it would be one build failure
per crate. Closing it at the source (having `Compute` skip disabled languages, which is what this
document's Coverage section already claims happens) is the better fix and is not this change.

**`coverage.floor` is what line-weighting removes, put back deliberately.** Weighting by lines is
blind to a small unit nobody tests: a package with 0 of 8 lines covered is 0.1% of an 8,305-line
repo, so the headline barely moves. The two metrics answer different questions — "did this change
leave code untested?" is the aggregate, "is there a unit nobody tests at all?" is the floor — and
neither bounds the other. `cmd/lydite/coverage.go`'s `floorReport` gates every measured unit
against it and adds one failing row per unit below, in the same `internal/ui` grammar
`diffReport`/`patchReport` use. An unmeasured unit is reported as `unmeasured`, never a failure and never
folded into the passing count.

**It is also the one gate that runs on main.** The aggregate and patch gates compare against a
baseline, and on a push to main the current commit *is* the baseline, so they have nothing to
compare and the record-on-main path returns before them. A floor has no baseline: a unit is above
the bar or below it, and that reads the same on main as on a pull request. Skipping it there would
leave main ungated on a unit that arrived through a path no pull request measured. The baseline is
recorded first and unconditionally — it is worth keeping whether or not a unit is below the floor,
and losing it would push every later pull request into a recompute-nothing cache miss over an
unrelated failure. It defaults to `0` (off), because upgrading lydite must not start
failing a repo over a gap it has always had, and it has **no baseline and no
compare-against-last-time**: a floor is an absolute standard, and ratcheting it against a prior
value would make a unit that has never had tests permanently acceptable.

**Cached baselines from before this live at a different path and are simply missed.** Entries
recorded under the mean are a different quantity, so `gitstate.StatePath` writes and reads
`v2/<key>.json` on `lydite` and every consumer takes one clean cache miss and re-records.
The version is the *metric's*, not the file format's — the entries stay a plain
language → percentage object, readable by hand on the branch — and the old entries stay in place
rather than being overwritten. `PriorBaselines`' `git ls-tree` needs `-r` for the same reason, or
it lists the directory instead of the entries in it. Consumers still see a step change on upgrade
(+9 points on the repo above); say so in the release notes. See
[ADR 0007](docs/adr/0007-line-weighted-coverage-aggregation.md).

### `coverage.source`: who produces the coverage

Unlike Codecov or Sonar — which never execute your tests, only ingest a coverage report your build
already produced — `lydite coverage`'s default (`coverage.source: run`) actually runs each
ecosystem's test suite itself (`go test -coverprofile`, `cargo llvm-cov`, a package's
`test:coverage` script). That's the right default for local dev (one command, no separate step to
remember), but it's wrong for CI if a test job already runs with coverage instrumentation on —
running tests again would duplicate work that may already be expensive (wardnet/wardnet-cloud's
existing pipelines already run tests twice: once plain for pass/fail, once instrumented for
coverage; `lydite coverage` piling on a third run would make it worse, not better).

`coverage.source: report` fixes this: lydite never executes anything, only looks for a report file
a prior job already produced — `internal/coverage.findReportForUnit` checks an explicit override
first (keyed by the discovered module/crate directory it applies to, from `coverage.go.report` /
`coverage.rust.report` / `coverage.rust.lcov`, or the matching `--go-report`/`--rust-report`/
`--rust-lcov-report` flag), then a built-in candidate list resolved relative to that directory
(`coverage.out`/`cover.out`/`c.out` for Go;
`coverage/llvm-cov.json`/`llvm-cov.json`/`target/llvm-cov/llvm-cov.json` for Rust — TypeScript has
no override, since `coverage/coverage-summary.json` is already Istanbul's own fixed convention, not
something projects vary). In CI, the intended shape is: the existing test job already produces
coverage as a side effect of running tests once (e.g. `cargo llvm-cov nextest` *as* the test runner,
not a second pass after a plain `cargo test`), and `lydite coverage` runs afterward as a pure
report-consumer.

**The axis is named for who owns production, not for what lydite skips.** The setting used to be
`--tests=run|skip` (and a `tests-mode` action input), which described lydite's own behavior and
left the reader to infer the pipeline shape behind it. `run`/`report` names the decision the repo
is actually making. The never-execute promise is unchanged and still guarded by
`internal/coverage.TestGoCoverageSourceReportDoesNotRunTests`.

**Where it lives is the point** (see [ADR 0003](docs/adr/0003-coverage-source-in-config.md)).
`coverage.source` is a property of how a repo's pipeline is built
— one answer, true for every workflow in that repo — so it belongs in `.lydite/config.yml` at the scan
root, not restated at each call site. The composite action therefore has *no* input for it:
`action.yml` passes only `--dir`, and `--dir` is the one thing that can't move into the file, since
the file lives at the scan root and lydite must know the root before it can read its own config.
The CLI flags (`--source`, and `--go-report`/`--rust-report`/`--rust-lcov-report`) remain as a
local-dev/one-off override that outranks the file; `--tests` survives as a deprecated alias mapping
`skip` to `report`. `cmd/lydite/coverage.go`'s `resolveSource` and `resolveReports` own that
precedence. Both source flags default to `""`, not to `"run"` — with a `"run"` default the flag
would always be populated, always outrank the file, and `coverage.source` would never once be
consulted, a silent failure indistinguishable from the config key not working.

**One exception, and it is a trap.** Computing a **baseline** at a historical main SHA (a cache
miss) always uses `coverage.SourceRun` internally — `cmd/lydite/coverage.go`'s `computeBaselineAt`
hardcodes it and passes an empty `ReportPaths{}` — regardless of what `coverage.source` says. A
historical commit's throwaway checkout has no CI-produced report sitting in it, so there is nothing
to consume. This is why `internal/coverage.Compute` takes the source as a **parameter** and never
reads `cfg.Coverage.Source` itself, even though it is already handed the config: a `Compute` that
consulted the config directly would hand every report-sourced repo an empty baseline forever —
exactly the `{}` poisoning the caller then refuses to cache, leaving the gate comparing against
nothing, silently and permanently. Keeping the axis in the signature forces the one caller that
must override it to say so out loud.
`cmd/lydite/TestComputeBaselineAtRunsTestsEvenWhenSourceIsReport` guards this directly, with a
fixture whose only possible source of a coverage number is a test that actually ran. The real cost
is one test run per main commit (cached afterward on `lydite`), not once per PR, so it
doesn't reintroduce the duplication `coverage.source: report` exists to avoid.

TypeScript's `SourceRun` path also runs a one-time dependency install per workspace root before
executing each package's `test:coverage` script — a fresh checkout (baseline computation's throwaway
worktree, but also any other `SourceRun` invocation) has no `node_modules` a prior step could have
already installed. `internal/coverage.resolvePackageManager` auto-detects npm/yarn/pnpm from the
root's lockfile (`package-lock.json`/`yarn.lock`/`pnpm-lock.yaml`); a root with more than one
recognized lockfile is treated as ambiguous and install is skipped there rather than guessing a
priority order. `typescript.install` in `.lydite/config.yml` overrides auto-detection entirely with an
explicit shell command (e.g. `corepack enable && yarn install --immutable`), for Corepack-pinned or
otherwise nonstandard install flows auto-detection can't infer, or to resolve an ambiguous root.
`internal/coverage.tsWorkspaceRoots` dedupes so a shared root serving multiple nested packages is
only installed once, not once per package.

### Patch coverage

Aggregate coverage and patch coverage catch disjoint regression classes: aggregate catches
coverage lost in code the current PR never touches (e.g. a deleted test file — none of those lines
are in the diff, so aggregate is the only gate that notices); patch coverage catches untested new
code even when the codebase is big enough that it doesn't move the aggregate percentage. Neither
bounds the other, so `lydite coverage` computes and gates on both, not either/or — patch coverage
is a second, independent check alongside `diffReport`'s existing aggregate gate, not a replacement.

Patch coverage has **no baseline or threshold of its own** — it always gates against that same
language's aggregate baseline already read from `lydite` (`patch% >= baseline% -
coverage.patch.tolerance` — its own knob, deliberately independent of `coverage.tolerance`, so
loosening the noisy aggregate gate never silently weakens this one). A language
with no aggregate baseline yet is reported informationally (`new`), not failed, mirroring
aggregate's own handling of a first-time-seen language. It's opt-out, not opt-in, per language, via
`.lydite/config.yml`:

```yaml
coverage:
  patch:
    go:
      enabled: false   # defaults to true
```

Changed lines come from a hand-rolled unified-diff hunk parser (`internal/coverage.ChangedLines`,
`git diff --relative --unified=0 <merge-base>..HEAD`) — deliberately not a diff library, since the
format needed is a small, stable subset (hunk headers + `+` lines). `--relative` matters: the
command runs in `--dir`, and every consumer of the changed-line map works in `--dir`-relative
paths (crate/package prefixes, lcov normalization). Without it, git emits repo-root-relative paths,
so with `--dir` pointing at a subdirectory (wardnet's `--dir source`) every changed file failed the
prefix match and the patch gate silently measured nothing. `mergeBase` is the exact same SHA
`gitstate.BaseSHA` already resolved for the aggregate baseline lookup, reused as-is rather than
recomputed. The parser does no language-aware filtering of comments/blank lines/imports — that
happens later, when changed lines are intersected with a coverage report's line-hit data
(`internal/coverage.PatchPercent` counts only lines the report actually mentions).

**A Go coverage profile is the exception, and it bit us.** lcov (Rust, TypeScript) lists only
executable lines, so "absent from the report" safely means "not executable, don't count it". A Go
profile records *blocks*, not statements — every line between a block's braces is in the report,
comments and blank lines included.

The dividing line is the *format*, not the tooling: lcov simply has no slot for a non-executable
line, whereas a Go profile has no notion of a line at all, only a brace-to-brace span that lydite
itself expands. Don't infer it from how the tool measures — Vitest's default `v8` provider is
range-based exactly like Go's profile, and `llvm-cov`'s own text report does print counts beside
comment lines inside a function. Both still emit clean lcov (v8 maps ranges back onto statements via
`v8-to-istanbul`; `llvm-cov --lcov` only emits `DA:` for lines carrying a coverage segment) —
verified directly against both producers with a comment and a blank line inside an uncovered
function, neither of which appeared in the resulting lcov. So Go needs the filtering below and the
other two genuinely don't. So a comment added inside an uncovered function counted as an
uncovered new line, and a comment-only PR scored 0% patch coverage and failed the gate
(`wardnet/inforge#216`, whose entire diff was `nosemgrep` annotations and workflow YAML).
`internal/coverage.ParseGoProfile` therefore reads each profiled source file and drops blank and
`//`-comment lines before they ever reach `LineHits`. It deliberately does **not** try to track
`/* */` comments (that needs a lexer — `/*` inside a string literal opens nothing) or treat a
leading `*` as a comment continuation (`*p = x` is a pointer assignment): over-counting a rare block
comment merely understates patch coverage, while wrongly dropping a statement would let genuinely
untested code through the gate.

Per-ecosystem line-hit sources, all converging on the same `LineHits` (`map[file]map[line]hits`)
shape:

- **Go**: `internal/coverage.ParseGoProfile` reads the same `coverage.out` profile the aggregate
  percentage is computed from — no separate format, no second `go test` run. `Compute`'s returned
  `PatchSources.GoProfiles` is a `map[string]GoModuleProfile` keyed by module dir (like Rust's
  `RustLCOV`, not a single path), each entry carrying the profile path, the module path, and the
  module's directory relative to `--dir`. Both of the latter are needed to turn a profile's
  package-qualified names into `--dir`-relative paths, and neither generalises across modules —
  wardnet's are `github.com/wardnet/wardnet/source/wctl` under `wctl/` and `wardnet.network/go`
  under `sdk/wardnet-go/`. `goPatchPercent` merges every module's hits; no per-module prefix scoping
  is needed (unlike Rust's), because the keys are already `--dir`-relative and cannot collide.
- **Rust**: `cargo llvm-cov` doesn't emit per-line data in its `--json` export, so patch coverage
  additionally produces an `--lcov` report, per discovered crate/workspace root (see the Coverage
  section above). Under `SourceRun`, this doesn't cost a second test execution: `cargo llvm-cov
  --no-report` runs each crate's suite once and keeps raw profile data on disk, then both `--no-run
  --json` (aggregate, unchanged) and `--no-run --lcov` (patch, new) regenerate their reports from
  that same profile. Under `SourceReport`, the lcov file is another `findReportForCrate` lookup per
  crate — an explicit `coverage.rust.lcov` entry (or `--rust-lcov-report <crateDir>=<path>`), else a
  candidate list
  (`coverage/lcov.info`, `lcov.info`, `target/llvm-cov/lcov.info`) resolved relative to that crate's
  own directory, mirroring `coverage.rust.report` exactly. `Compute`'s returned `PatchSources.RustLCOV` is
  a `map[string]string` keyed by crate dir (like TypeScript's `TSLCOV`, not a single path) —
  `cmd/lydite/coverage.go`'s `rustPatchPercent` resolves each crate's contribution independently,
  mirroring `tsPatchPercent`'s longest-prefix matching so two crates can't clobber each other's hit
  data for a same-named file. A crate with no resolvable lcov file is omitted from patch coverage,
  not a failure — but not silently when it matters (see the `unmeasured` paragraph below).
- **TypeScript**: reads `<pkgDir>/coverage/lcov.info` (Istanbul/Vitest's native lcov output) — fixed
  convention, no override flag, matching the existing no-override precedent for TS aggregate
  coverage. This only works if the consumer's own test config already has an `lcov` reporter
  enabled; otherwise it's omitted, the same best-effort caveat AGENTS.md already documents for TS
  aggregate coverage.

`cmd/lydite/coverage.go`'s `patchReport` adds one row per language in the same `internal/ui`
grammar the aggregate gate uses (e.g. `✗ go patch ... 0.0% (0/9 new lines; baseline 55.68%)`).
Every gate in a run shares one `ui.Report`, so a command emits one row set and one verdict line
however many gates ran.

**A skipped patch gate must say so.** When a *detected* language's patch gate is enabled and the
diff touches that language's files, but no per-line source was resolved (no lcov for the crate, no
`lcov` reporter in a TS package), `patchReport` adds an `unmeasured` row for that language and
a stderr warning naming the missing wiring — it never fails the gate on its own, mirroring
`diffReport`'s `unmeasured` handling. The old behavior was a bare `continue`, and it read as
"patch coverage passed" in the PR comment: wardnet/wardnet#957 shipped a green lydite summary
(aggregate flat, patch never ran for want of an lcov path) while Codecov — fed the very same lcov
export the pipeline had already produced — failed that diff's patch coverage. The two reports can
only stay aligned if a gate that didn't run is visibly distinct from a gate that passed. The
report stays scoped to detected ecosystems, since the patch gates default to enabled for all three
languages regardless of what the repo contains — a stray changed `.rs` file in a pure Go repo must
not produce a rust line.

## Output grammar and `--json`

Every command renders through `internal/ui`, which implements the specification in
`docs/design/tokens.md`: a row is glyph, space, label, leader dots, value with the value
column at 34 characters; the last line is the command, the verdict and the duration.
`--no-color` drops colour and keeps every glyph, and colour is off automatically when
stdout is not a terminal or `NO_COLOR` is set — so a CI log never fills with escape
sequences nobody configured away.

**Glyph and exit code are different axes, deliberately.** A run has exactly one verdict and
that verdict owns the exit code; a row's glyph only says how much attention the row wants.
`refer`, `unmeasured` and `dropped` all render amber `!`, and only `refer` votes. This is
what lets an unmeasured gate be visibly distinct from a passing one — the wardnet#957
failure — without a path-filtered coverage job starting to fail builds. `ui.Report.ExitCode`
is the single place that mapping lives: `✗` anywhere is 1, else a referral is 2, else 0.

**Anything automated reads `--json`, never the text.** The document carries the same rows as
the terminal, so the two cannot disagree, and statuses travel as their own names rather than
as glyphs. `TestJSONKeysArePartOfTheContract` pins the keys; `ui.jsonRow` stays a separate
type from `ui.Row` for that reason, so a field added for rendering cannot quietly become
part of the published document. `lydite/actions` greps for a
`^\[(PASS|FAIL)\] <name>$` shape this grammar does not produce, and therefore matches nothing
— cutting it over to `--json` is a change in that repository. **Do not reintroduce a bracketed
text mode to accommodate it.** A text-scraping consumer forces every refinement to the human
surface through a synchronised release in another repo, which is the coupling `--json` exists
to remove.

**stdout is the report; everything else goes to stderr.** `internal/gitstate`'s git plumbing
runs through `executil.RunQuiet`, which captures without streaming — `git rev-parse` printing
two SHAs into the middle of a report was how this was found. Scanners still stream live
through `executil.Run`, because their findings are the point; under `--json` the commands
call `executil.StreamTo(os.Stderr)` so the document stays parseable and the findings still
reach the terminal and the CI log. Warnings have always gone to stderr and still do.

Errors are not verdicts. A command that cannot reach an answer returns an `error`, and
`main.go` prints `lydite: <err>` and exits 1; a command that reached one returns
`ui.ExitError`, which `main.go` exits on silently because the report already said what
happened. Every subcommand sets `SilenceUsage`/`SilenceErrors` so cobra does not print a
flag list under a failed gate.

## Referral: `lydite review`

`lydite review` decides whether a change may merge unattended. It runs no check — no
scanner, no test suite — so almost nothing in it is a failure: it emits pass or refer, and a malformed
exemptions file or an unresolvable merge-base is an error, exit 1. See
[ADR 0013](docs/adr/0013-referral-not-approval.md) for the model and
[ADR 0014](docs/adr/0014-evidence-only-referral-matching.md) for what it matches on.

The model is an allowlist and the default is to refer. `.lydite/exemptions.yml` at the scan
root declares shapes of change that merge unattended; with no file, or an empty one, every
change is referred, which is the correct day-one state rather than a broken one.

```yaml
exemptions:
  - name: readme-only
    # reason is required: this file is the entire risk model, and a diff of
    # bare globs is not reviewable.
    reason: >
      Prose in the top-level README changes nothing executable and no build
      step reads it.
    paths: ["README.md"]
disqualifiers:
  # Only ever ADDS to the built-in set. A veto list that can be emptied is
  # not a floor.
  paths: ["infra/**"]
```

Five properties are load-bearing and easy to weaken by accident:

- **The file is read from the merge-base, never from the branch.** A change that widens the
  gate gets no benefit from its own widening — otherwise one pull request declares itself
  exempt. `loadExemptionsAt` does this with `git show <base>:<path>`.
- **All-or-nothing, against a single exemption.** Every changed path must be covered by one
  exemption. Matching on *any* path would let an agent staple a README tweak onto a dangerous
  change; matching on the *union* of two exemptions would mean adding a narrow entry silently
  widens every existing one, destroying `git log .lydite/exemptions.yml` as the readable
  record of widenings that #15 exists to protect.
- **Disqualifiers veto any match, and the built-in set cannot be removed.** A net-new
  suppression annotation, a newly skipped or `.only`-focused test, a removed test — deleted,
  gutted, or renamed out of a test path, since `git mv foo_test.go foo_disabled.go` takes it
  out of the runner's view leaving nothing deleted and no hunk to read — an edit to
  any file under `.lydite/`, an edit to `.github/workflows/`, and an edit to
  `.gitattributes`. The suppression list carries the whole-file and whole-crate forms
  (`#![allow(`, `#[expect(`, `@ts-nocheck`, `//nolint`, `//go:build ignore`) alongside the
  per-line ones, because the broad form is strictly more powerful than the narrow one it would
  otherwise be the only one caught. `.gitattributes` is there because a `-diff` attribute
  replaces a hunk body with `Binary files ... differ`, and git reads that attribute from the
  branch — the diff is passed `--text` so the trick does not work, and the edit is referred
  anyway.
- **Everything matched on is evidence off the diff.** Nothing an author asserts about their
  own change may earn an exemption or clear a disqualifier — ADR 0013's `!` conventional-commit
  marker is deliberately *not* implemented, because nothing detects an undeclared API break and
  a claim-based veto works only for the author who would have declared anyway. The rule for any
  future addition: an author-controlled claim may add a referral, never remove one.
- **A change that edits the exemption set may edit nothing else**, and this is the one thing
  `review` reports as a *failure* rather than a referral: the author clears it by splitting the
  change in two, which is work they can do, and that is exactly what separates a gate from a
  referral. Two properties already protect the file — the merge-base read, so a change gets no
  benefit from its own widening, and the disqualifier, so such a change is always referred —
  and neither closes the realistic attack, which is not a forged exemption but an unremarkable
  one riding along in a large change approved for its other contents. What isolation buys is
  that `git log .lydite/exemptions.yml` becomes the complete, reviewable record of every
  widening. `.lydite/config.yml` deliberately carries no such requirement: report paths change
  alongside code for honest reasons, and a rule that fires on ordinary work gets relaxed later.
- **An absent exemptions file and an unreadable one are different questions**, asked
  separately: `git cat-file -e` answers whether it is there, and only then does `git show` read
  it. Collapsing them would make a broken read indistinguishable from an empty allowlist, which
  is safe only while the allowlist *is* empty and the safe answer happens to coincide.
- **The diff fails closed when it cannot be read.** A patch line longer than the scanner's
  buffer — `--text` renders a binary blob or a minified bundle as one — aborts the run rather
  than yielding an empty set of added lines, which would leave every content veto silent while
  the path list stayed complete.
- **Every git setting that decides what a diff says is pinned** (`core.quotePath`,
  `diff.relative`, `diff.mnemonicPrefix`). Each is settable in a global gitconfig lydite does
  not control, and `diff.relative` alone scopes the diff to `--dir` and strips the prefix from
  what survives — dropping `.github/workflows/` out of a monorepo's paths entirely.
- **The diff covers the whole repository, not just `--dir`.** Unlike `internal/coverage`, which
  scopes its diff with git's `--relative`, referral decides whether a *pull request* needs a
  human, and a workflow edit outside a monorepo's scan root is exactly what must not slip past.
  `--dir` only locates the exemptions file.

Path patterns are **repository-root-relative and anchored**: `README.md` matches the README at
the repository root and nothing else, any-depth matching is spelled `**/README.md`, and a
monorepo scanned with `--dir source` writes `source/README.md` — the diff is not scoped to
`--dir`, so the paths carry no prefix stripped. This parts company with gitignore on
purpose — floating patterns are right for a skip list where over-matching is free, and wrong
for a list that decides what merges without a human. Unknown YAML keys are rejected rather
than ignored, for the reason `config.validateLinter` rejects `linter: eslint`.

The verdict is computed from `<merge-base>..HEAD`, so the local answer and the CI answer come
from identical inputs. A dirty working tree gets its own row saying the uncommitted work was
excluded, because silently deciding on HEAD while the developer is looking at edited files is
the one way this command gives a confidently wrong answer.

## Clearance: `/lydite clear`

A referral is resolved by a person commenting on the pull request, never by the author
pushing more code. `lydite clearance` is that surface and `internal/clearance` is its
decision; `internal/forge` is the only thing that talks to the platform. See
[ADR 0015](docs/adr/0015-clearance-binds-to-a-commit.md).

`review --publish` records the verdict as the **`lydite/referral` commit status** and as
the pull request's standing comment. The status is the whole record: a clearance is a
state change on that context at one commit, and nothing else stores it.

Six properties are load-bearing:

- **A clearance names one commit.** Not the tree, and not the shape of the verdict. Any
  push produces a new head carrying no status, so the clearance evaporates with no state
  of its own — including after a rebase that changes nothing. Both alternatives require
  lydite to hold that two revisions are the same change, and a clearance resting on that
  inference is one the inference can be wrong about.
- **A referral publishes `pending`, never `failure`.** A required check blocks on
  anything but `success`, so this softens nothing; it is the accurate word, and a gate
  fails where a referral does not. `stateFor` is the single place that mapping lives.
- **The status is read before it is written.** Re-running `review` after a clearance
  comment cannot change the answer — a referral re-evaluated is still a referral — so the
  only thing recomputation would buy is telling a referral apart from the isolation gate,
  and the standing status already carries that. Reading it means a `failure` is not
  clearable by comment, and that no pull-request content is fetched at comment time.
- **The commenter must have push permission**, read from the platform rather than from
  the comment. The repository is public, so without it any account could clear anything.
  This is a floor and not the whole trust — whoever holds the credentials satisfies it,
  which is what [#25](https://github.com/lydite/lydite/issues/25) closes.
- **A head that moved after the comment is refused.** A status created after the comment
  cannot be one the person read. Both timestamps are the platform's, so neither is the
  author's to set. `/lydite clear <sha>` names a revision explicitly and overrides the
  ordering, since naming it is the stronger statement.
- **Only the first line of a comment is parsed.** A reply that quotes an earlier comment
  carries its text, and scanning the whole body would let a quoted `/lydite clear` clear
  a change nobody meant to.

`.github/workflows/lydite-clearance.yml` runs on `issue_comment`, so it always executes
the **default branch's** copy and the deciding logic is never the pull request's to edit.
Nothing in it checks out the pull request's code, and nothing in it may start to: the job
holds a writing token. Its job-level `if:` is evaluated before a runner is allocated, so a
comment not addressed to lydite costs nothing rather than costing a short job.

**The status blocks no merge yet.** Making it a required check is one field on a ruleset
`gt` renders and hardcodes to `ci-gate` alone, so it cannot be set from this repository —
see [#34](https://github.com/lydite/lydite/issues/34). Until then the loop is published
and flipped correctly and enforces nothing, which is a temporary state rather than the
design. `/lydite exempt` is likewise held back, in
[#33](https://github.com/lydite/lydite/issues/33), because what it should emit is
undecided rather than unbuilt.

The pull-request comment follows `docs/design/reference/surfaces.dc.html`: verdict badge,
one-sentence headline, a `Check / Head / Base` table, named list sections, and a footer
rule. Which facts fill the columns is lydite's to choose — a referral has no
measurements, so the head column carries what the change contains and the base column
what was read out of the merge-base. The design's footer also claims parity with the
reader's local run; **nothing can establish that yet** (see #27), so it is absent rather
than asserted. `ui.Marker` identifies the standing comment for editing, by a marker in the
body rather than by author, because the author is whoever's token posted it.

## Semgrep: token-bearing vs token-less runs

`internal/semgrep.Check` picks its subcommand from whether `SEMGREP_APP_TOKEN` is set: `semgrep ci`
(diff-aware, applies the org's platform policy, uploads to the AppSec Platform) when it is, plain
`semgrep scan --config <ruleset> --error` when it isn't. Those two modes disagree about **scope**,
and that disagreement was a standing CI defect: GitHub deliberately withholds repo secrets from
`dependabot[bot]` events, so every Dependabot PR arrived with an empty token, silently fell back to
a *whole-repo* scan, and blocked on the consuming repo's pre-existing findings — findings no
token-bearing run had ever reported, in code the PR never touched. Whether a PR was green depended
on who opened it.

`lydite scan --diff-base <ref>` closes that gap: in scan mode it passes Semgrep
`--baseline-commit`, so the fallback blocks on the same thing `semgrep ci` would — what the change
introduces — and nothing else. `--diff-base auto` resolves the merge-base with `origin/main` via the
same `internal/gitstate.BaseSHA` the coverage gate already uses, so a PR's scan and its coverage
agree on what "this change" means. `action.yml` passes `auto` on every `pull_request` event.

Two deliberate choices in `cmd/lydite/scan.go`'s `resolveDiffBase`:

- **A token short-circuits it entirely** — `semgrep ci` already scopes itself to the diff, so
  resolving a merge-base would cost a `git fetch` nothing reads, and would newly demand a
  full-history checkout from token-bearing consumers that don't need one today.
- **An unresolvable `auto` is an error, not a silent full scan.** Falling back would reintroduce
  the exact surprise the flag exists to remove: a scan that quietly widens its own scope. A shallow
  checkout is a fixable misconfiguration (`fetch-depth: 0`), so lydite says so and fails.

Default (`--diff-base` empty) is still a full-repo scan — that's what a local `lydite scan` wants,
and it's what a push to `main` gets.

Restoring `semgrep ci` on Dependabot PRs (for the platform dashboard's sake) is a *consumer-side*
option, not a lydite one: the token has to be added to the repo's separate **Dependabot secrets**
store (`gh secret set SEMGREP_APP_TOKEN --app dependabot`), since Actions secrets are not visible to
Dependabot events. It is not required for CI to be green — the diff-aware fallback above is — and it
does hand an upload token to a workflow that executes the bumped dependency's code, so it's a
per-repo judgment call.

## The `action.yml` composite action

**Its output parser does not match lydite's output.** `tool_result()` matches
`^\[(PASS|FAIL)\] <name>$`, a shape `internal/ui` does not produce — see "Output grammar and
`--json`" above. It has to read `--json` instead. Reintroducing a bracketed text mode here is
the wrong direction: it is what couples the human surface to a parser in another repository.

Unlike `inforge`'s action (install-only — its invocations vary too much per call site to bake in),
lydite's usage is uniform enough (`.lydite/config.yml` already carries all the config) that the action
owns the whole install → run → report flow: install lydite, run `scan`/`coverage` (each toggleable
independently via `run-scan`/`run-coverage`), post one sticky PR comment summarizing both (upsert,
not a fresh comment every run — via `marocchino/sticky-pull-request-comment`), and optionally
upload to Codecov (non-blocking, purely for its dashboard/history) and/or switch lydite's own
Semgrep check into `semgrep ci` mode (diff-aware + uploads to the Semgrep AppSec Platform) when a
`SEMGREP_APP_TOKEN`-equivalent input is supplied. The Codecov upload is two `codecov/codecov-action`
invocations sharing the same `codecov-token` gate — one `report_type: coverage`, one
`report_type: test_results` — both relying entirely on that action's own recursive workspace
auto-discovery rather than lydite passing explicit `files:`/`directory:` paths itself. This is
why a consumer's CI only needs to hand lydite a token: lydite owns the whole Codecov
relationship (coverage *and* JUnit test-results), so the calling workflow never has to install a
Codecov CLI or push to Codecov directly itself.

The PR comment's header embeds `assets/lydite-mark-64.png` by **absolute raw URL**
(`raw.githubusercontent.com/lydite/lydite/main/...`), never a repo-relative path — the comment is
posted into the *consuming* repo's PR, where a relative `assets/...` would resolve against that repo
and 404. It's pinned to lydite's default branch, not a release tag, so the image keeps resolving for
consumers pinned to an older lydite version. Renaming or moving that file therefore breaks the logo
in every consumer's PR comment retroactively — treat its path as a public API.

**Never interpolate `${{ inputs.* }}` or `${{ steps.*.outputs.* }}` directly into a `run:` script
body** — pass it via that step's `env:` block instead, and reference the env var name (`"$DIR"`,
not `"${{ inputs.dir }}"`) inside the script. Semgrep's own `yaml.github-actions.security.run-shell-injection`
rule caught this exact mistake once already (see git history) — expression interpolation directly
into a shell script is a real script-injection vector if the interpolated value could ever contain
shell metacharacters, regardless of how trusted the input value looks today. `if:` conditions and
`with:` blocks on a `uses:` step are fine to interpolate directly — only `run:` script bodies are
the risk, since that's the only place text gets spliced into something a shell then executes.

## Design

The brand and design system live here rather than in a `lydite/brand` repository, because
every consumer of them is already in this repository — see
[ADR 0012](docs/adr/0012-design-system-in-the-monorepo.md). The split that matters is
inside the tree, not across repositories:

- `assets/` is **what ships**. Production SVGs, plus `lydite-mark-64.png`, whose path is a
  public API (above).
- `docs/design/tokens.md` is the token set and the surface specifications.
- `docs/design/reference/` is **reference only** — the `.dc.html` prototypes and the logo
  construction proofs. The prototypes use a custom runtime and inline styles, both
  artefacts of the authoring environment: do not port the runtime, and do not copy the
  styles into `web/`.

Two things in `tokens.md` are documented but **not implemented**, and neither should be
mistaken for a description of current behavior:

**The CLI output grammar is implemented**, in `internal/ui`, and every command renders
through it — glyphs, leader dots aligning the value column at 34 characters, `--no-color`,
and a verdict-plus-duration last line. `lydite/actions` parses a
`^\[(PASS|FAIL)\] <name>$` shape this grammar does not produce, and therefore reads nothing;
cutting it over to `--json` is a change in that repository. Do not restore the bracketed text
form to accommodate it — a text-scraping consumer makes every refinement to the human surface
a two-repository release.

**There is no light product theme and no responsive design.** The light token ramp exists
and the PR comment uses it, but no product screen has been drawn light, and nothing below
1240px has been drawn at all. `docs/design/README.md` records both as gaps. Inventing
either from the token table is how a design system starts lying about its own coverage.

## Release notes

`release.yml` assembles the GitHub release body's header from
`docs/release-notes/_header.md` (the invariant install block — it lives there
rather than in `.goreleaser.yml`'s `header:` so the workflow can extend it) plus
`docs/release-notes/<tag>.md` when that file exists, passes it with
`--release-header`, and goreleaser appends its commit-derived changelog beneath.

A missing per-version file is deliberately not an error. Most patch releases are
fully described by their commit subjects, and failing a release over a file it
never needed would make every release depend on remembering a step — the same
failure the major-alias move in that workflow was automated away to prevent.
Write one when the release carries what a commit subject cannot: a change in
what an existing number or verdict *means*, an upgrade step consumers must take,
or a new major. `docs/release-notes/v2.0.0.md` is the worked example, and the
file has to be on `main` before the tag is pushed — the workflow reads it out of
the tagged tree.

## Conventions

- **Version injection:** `cmd/lydite` exposes `var version = "dev"`, overridden at release via
  `-ldflags "-X main.version=<tag>"`. Keep that variable name and package stable.
- **goreleaser & golangci-lint both use the v2 config schema.** In golangci-lint v2, `gosimple` is
  part of `staticcheck` — don't add it as a separate linter (it will error).
- Lint must pass with zero issues; `errcheck` is on, so check returned errors.

## Boundaries

- **Always:** run `go build ./...`, `go test -race ./...`, and `golangci-lint run ./...` before
  proposing a PR.
- **Ask first:** changing the Go version, renaming the binary/`cmd` dir, altering the release
  archive layout, or editing CI.
- **Never:** introduce cgo, commit `dist/` or secrets, or skip the lint/test gates.

## Worktrees

This repo uses a bare-repo + typed-worktree layout managed by the `gt` CLI — one session, one
`gt wt add <type/name>` worktree; never use raw `git worktree` or edit inside `.bare/`.
