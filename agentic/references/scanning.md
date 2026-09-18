# Scanning: the declaration names the units too

> **The reference for `lydite scan`** — which units each language's checks run over.

`lydite scan` runs each language's checks over each declared component, and nothing walks the tree
for manifests. A component names a runner, the runner implies a language, and its `dir` is where
that language's checks run. See [ADR 0020](../../docs/adr/0020-scan-on-components.md).

**A repository that declares no components is an error, not a row.** `lydite: no components
declared in .lydite/components.yml`, exit 1. An `unmeasured` row would leave the job green over an
entirely unscanned repository — a security scan that silently stopped, which is the wardnet#957
failure applied to the scanner. `lydite test` is already loud in the same situation, because every
source file in such a repository is an orphan. The cost is accepted and real: Semgrep is
root-scoped and would have run.

**A Cargo workspace is scanned once, at the component root**, and a JavaScript workspace is linted
once rather than once per nested `package.json`. That is what the build tool treats as a whole,
which is what a component is. The workspace-member resolution detection needed — a nested
`Cargo.toml` whose ancestor declares `[workspace]` is not a second unit — has no equivalent here,
because the declaration already states which directories are units.

**Rows are labelled by component name**, unconditionally: `gosec(cli)`, `cargo clippy(api)`,
`biome(web)`. `component.validate` enforces unique names and not unique directories, so the name is
the only one of the two unique by construction — and it is the token `lydite test`'s rows already
carry, so a scan row and a test row about one component are greppable together. The labelling lives
at the command and not in each language package, so one rule covers all three rather than three
that agree until one is changed.

**A component declaring a raw `command:` gets an `unmeasured` row** saying its language cannot be
derived. Skipping it silently would read as a component that was scanned and found clean. This is
deliberately not the treatment a **disabled** language gets: `rust.enabled: false` produces no rows
at all, because that is an opt-out the repository stated rather than a check that could not run,
and a row per opted-out component trains readers to ignore the tag that exists to be noticed.

**Source no component's checks reach is named on stderr**, by `internal/orphan`'s `Unscanned`.
The orphan gate is what normally makes a declared list safe to rely on and it cannot answer this:
it asks whether any component *contains* a file, because a component tests what is under it
whatever it is written in, while a scanner is per language. So a component rooted at `.` covers
every path and leaves a TypeScript directory beside it orphaning nothing while no TypeScript check
runs — and the gate belongs to `lydite test`, which a consumer can run scan without.

**For Go it asks one thing more**, and the exception is exact rather than heuristic: a nested
`go.mod` starts a separate module the enclosing module's package graph excludes, so `./...` at an
ancestor never compiles it and neither gosec nor govulncheck sees it. Verified against the tools —
the same G306 in a root module and in a nested one is reported once. A `go.mod` under
`testdata/` or a `.`/`_`-prefixed directory is not a boundary, for the same reason: the go
command ignores those directories, so a fixture module is not scanned by its parent either. **Rust gets no equivalent
rule**, because a `Cargo.toml` between the component root and a file may be a workspace member
cargo already covers or an unrelated crate it does not, and telling those apart means reading the
manifest; a path-shaped rule would warn about crates that are perfectly well scanned. TypeScript
needs none, since Biome walks the tree from where it is pointed.

**A `.js`, `.jsx`, `.mjs` or `.cjs` file is never a gap on its own.** That family is the extension
of build output, configuration and tooling glue in every ecosystem, so a Go repository with a
`docs/theme.js` is an ordinary Go repository rather than one carrying unscanned TypeScript. The
orphan gate keeps the full extension set because it asks whether a component *claims* a file,
which a component rooted at `.` does; this asks whether a body of source is checked by nothing,
and one `.js` cannot answer it. The cost is a JavaScript-only package going unmentioned, which is
the direction that keeps the diagnostic worth reading.

It reads git's file list, file extensions and the presence of a `go.mod`, and opens no manifest —
the orphan gate's own kind of question rather than detection under a new name: it decides nothing
about what runs, and its answer is a sentence. Excludes narrow it, because an exclude is already
the reviewable statement that a path is claimed by no component; a language switched off in
`.lydite/config.yml` is silent, because that is an answer rather than an oversight.

**A check and an install do not share an environment.** `executil.Env` carries both: `Check` is the
component's resolved toolchain plus the environment its declaration asks for, because a
repository's own build needs `SQLX_OFFLINE` or `CGO_ENABLED` to compile at all; `Install` is the
toolchain alone. `go install`, `cargo install` and `npm ci` read `GOPROXY`, `GOSUMDB`,
`CARGO_REGISTRIES_*` and `npm_config_registry`, so a declaration reaching them would choose where
lydite fetches the scanner it is about to run — without touching `PATH` — and the cache key names
the tool's version, not where it came from, so one poisoned build would outlive the run and cross
repositories on a shared `~/.cache/lydite`. A repository may say how its own code builds; it may
not say where lydite's scanners come from.

**The same split runs through `lydite test`'s `Prepare`,** where the distinction is whose software
is being fetched. `installNodeDeps` installs the *repository's* dependencies and gets the declared
environment — a workspace whose install needs a registry or a token said so in its own
declaration. `installCargoTools` installs *lydite's* pinned runners and gets the toolchain alone,
because `cargo install` reads `CARGO_HOME`, `CARGO_REGISTRIES_*`, `CARGO_NET_*` and
`RUSTC_WRAPPER`.

**Every check reports its findings as data, through a side channel.** The tool keeps printing
exactly what it printed before and lydite reads a structured copy purely to populate
`Result.Findings` — so `Result.Detail` stays empty for gosec's, Semgrep's, gitleaks's and
govulncheck's own findings, and what a reader needs beyond the one-line claim travels per finding
in `Finding.Detail`. Biome, clippy, cargo-audit and cargo-deny are exceptions, for two different
reasons: Biome's report goes to a file so its own chatter cannot corrupt the JSON, so nothing
streams and `Detail` is the only place its findings exist; clippy, cargo-audit and cargo-deny run
once, in JSON mode, with no second, richer terminal rendering left to duplicate, so `Detail`
renders the claim line and each finding's own `Finding.Detail` instead of sitting empty. See
[ADR 0032](../../docs/adr/0032-every-scanner-reports-its-findings-as-data.md).

**Only `govulncheck` still runs its tool twice**, because it has no output-file flag and needs a
second invocation to buy back the **verdict**: under `-format json` it exits 0 whether or not it
found anything, while the text run exits 3, so a single JSON run would report every advisory and
pass the check. `cargo clippy`, `cargo-audit` and `cargo-deny` run once, under `--message-format
json`, `--json` and `--format json`, and render `Result.Detail` from their own `Findings` instead
of buying back a second terminal rendering — clippy's diagnostic already carries `rendered`,
cargo's exact text, and audit's and deny's `Finding.Detail` was already the text beyond the
one-line claim, so there was nothing left to lose. `gosec` and Semgrep need one pass each —
`-fmt json -out <file> -stdout -verbose text` and `--json-output=<file>` both write a copy rather
than a replacement.

**The tool's own exit status decides the row**, with two exceptions where it under-reports: gosec
states a package that did not compile in the report rather than in its status, and Semgrep exits
zero for a run whose rules would not load. A scan that read nothing must not render as a clean
pass, which is the failure `reportableBiome`'s `parse` and `internalError/io` categories exist to
catch. Both carry their reason in `Result.Detail`, which is the only place a verdict lydite
invented can explain itself. Every parser keeps that same stance: an unrecognised category, level
or code is reported rather than dropped, and a report that will not parse falls back to the exit
status — a parser that silently drops what it does not recognise is how a gate stops gating.

**A scan anchors what it found, against the lines the change touched.**
`coverage.ChangedLines` is asked once and `record` anchors each check's claims —
after `labelled` has rebased them onto the scan root, because the map is keyed
from there. Without it every claim keeps the zero anchor and the review surface
takes none of them, which is what `scan` did before: `threads.Located` reads the
anchor, and a scan that set none gave it nothing. A scan with no `--diff-base`
anchors nothing, which is correct rather than missing — it reaches no change.

**The diff base is resolved whenever `--diff-base` is given**, including when
`SEMGREP_APP_TOKEN` is set and when Semgrep is switched off. Skipping it was
right while Semgrep was its only reader; the anchor is a second one, and a
token says nothing about where gosec's claims belong. A token-bearing consumer
passing `--diff-base auto` therefore now needs `fetch-depth: 0`.

**`cargo fmt` gets no parser.** lydite is not a formatter and must never report a formatting diff
as a finding (see [Linters](linters.md)). That its row still fails a Rust component contradicts
that, and is a separate open question.

**Semgrep is unchanged** in what it runs over: it is root-scoped and component-independent, so it
runs once over the scan root whatever the declaration says. Its findings therefore carry no
component, which is what `Finding.Component` being empty already means.

**gitleaks is the second root-scoped gate**, for the same reason Semgrep is one: a secret scanner
reads bytes, not a build graph, and the files most likely to carry a credential —
`.github/workflows/`, a `docker-compose.yml`, an `.env` — belong to no component at all. It runs
once over the scan root's working tree, never over history, and its findings carry no component,
so `findingCounts` puts the count in `root_findings` beside Semgrep's. It is not diff-scoped: the
row fails on every secret in the tree, as gosec's does, and the anchor is what decides which claims
reach the diff and which land in the standing comment as pre-existing debt. See
[ADR 0035](../../docs/adr/0035-secret-scanning-is-root-scoped-over-the-working-tree.md).

**`scan` has no `--component` or `--affected`.** Selection is `lydite test`'s surface today; a scan
that narrowed itself would need the same widening-on-ignorance argument made again, and nothing
asks for it yet.

## The licence gate compares a non-conforming set against the merge-base

`internal/licence` is the language-neutral core — `licence.Dependency`, `licence.Set`,
`licence.Policy`, `licence.Compare` — and it runs no tool and reads no manifest itself. What
checks a dependency's licence against `licence.policy.allow` (see
[Configuration](configuration.md)) is each language's own scanner, and the gate is the set of
`(package, licence)` pairs the current tree carries that fail the policy and the merge-base did
not: a pair present at the merge-base is grandfathered, and only an introduced one fails the row.
See [ADR 0038](../../docs/adr/0038-a-licence-policy-gates-the-licences-a-change-introduces.md) for
why a count, a version-keyed pair, and a stored `test record` baseline were all rejected in favour
of this shape.

**All three languages run it.** `recordGoLicence`, `recordRustLicence` and
`recordTypeScriptLicence` in `cmd/lydite/scan.go` each add a `licence(<component>)` row, gating
pass or fail against the merge-base. TypeScript's own source is `internal/typescript/licence.go`'s
`LicenceSet` (see [ADR 0042](../../docs/adr/0042-a-typescript-components-licences-are-read-from-its-lockfile.md)):
npm's `package-lock.json` states every dependency's licence outright and is read directly, no
install ever run to produce it; yarn and pnpm state no licence in their lockfile at all, so their
only source is a `node_modules` an earlier step already installed, read opportunistically, and a
component whose tree is not there answers `unmeasured` naming that no licence source exists
without an install this scan does not perform. All three render `not configured`, `pass`, `fail`,
`unmeasured` and `context` the same way — TypeScript is a full participant in the gate, not a
permanently-`context` row the way `crapRow` renders `context` for a language it has no complexity
source for at all
([ADR 0028](../../docs/adr/0028-crap-gates-the-delta-above-the-threshold.md)).

**A claim is located at the manifest line naming the package** — `go.mod`'s require line, read by
`readGoMod`; the `Cargo.lock` stanza, read by `readCargoLock`; or `package.json`'s own
`dependencies`/`devDependencies` line, read by `internal/typescript/licence.go`'s own
`readPackageJSON`, a raw-text scan mirroring `readGoMod`'s — and identified by the package and
licence rather than by that line's text, `Pair.Site()`'s `licence␟<package> <licence>`. The
version takes no part in the key: a bump whose licence is unchanged is the pair that was already
there, and a bump that changes it is a pair nothing grandfathered. A package reached only
transitively is named by no line of the manifest and gets `Line: 0` — never guessed, for any of
the three languages — because a guessed line is a review thread on code that has nothing to do
with the claim. This is the same departure ADR 0032 makes for a dependency advisory, and for the
same reason — see [Findings](findings.md).

**Go classifies in-process, over `go list -deps -json ./...`.** `internal/golang/licence.go`'s
`classifyModule` reads each dependency module's `LICENSE*`/`LICENCE*`/`COPYING*` files at its root
through `github.com/google/licensecheck`, and `Expression` folds several classified files into one
SPDX `OR` expression — `gopkg.in/yaml.v2` carries Apache-2.0 in `LICENSE` and MIT in
`LICENSE.libyaml`, and either allowed conforms. `-deps` rather than `-m all` scopes it to modules
the build actually compiles; a `replace` is followed to its right side, and a vendored module is
read through the package's own `Dir` rather than `Module.Dir`, which `go list` leaves empty under
`vendor/`. Nothing classified is `licence.Unknown` — a pair like any other, which grandfathers the
same way a known licence does, so adopting the gate never fails a repository over a dependency
whose licence nobody could already read.

**Rust reads cargo-deny's own rejections, under one of three policy sources.**
`internal/rust/deny.go`'s `PolicyFor` decides which document gates a component: `PolicyFromLydite`
generates cargo-deny's `[licenses]` table from `licence.policy.allow` into a temporary file and
runs `cargo deny check licenses --config <generated>` as its own invocation, split out of the
`check bans` row `denyArgv` still runs under the consumer's own config; `PolicyFromConsumer` is a
component's own `deny.toml` when lydite states no policy, and it gates **absolutely** rather than
by delta — cargo-deny already evaluates that file whole on every run, and grandfathering it would
weaken a check a consumer opted into deliberately; `PolicyFromNone` is neither, and the row is
`context` naming that nothing ran, because cargo-deny's unconfigured default rejects every licence
and inventing an implicit gate out of that default is the failure this design exists not to repeat.
**Only `PolicyFromLydite` is delta-gated** — a policy this repository states and applies absolutely
would fail every adopting repository on the licences it already ships, the same argument that
chose the delta everywhere else.

**TypeScript's source depends on the package manager, and neither one runs an install.**
`nodedeps.Manager` picks the one lockfile present. Under npm, `lockfileDependencies` reads
`package-lock.json` as schema version 3's flat `packages` map, keyed by the path an entry was
installed at — `node_modules/wrangler/node_modules/esbuild` names `esbuild`, the segment after the
last `node_modules/`, because that is how a duplicate is nested. Each entry's licence is
`license`, a bare string in the current format, or the older `licenses` array of `{type, url}`
objects composed through `licence.Expression` the way Go's `classifyModule` composes several
classified files into one SPDX `OR` — a package offering either of two licences conforms if either
is allowed. An entry marked `link: true` is a workspace's own local package pointing back into the
repository rather than at a downloaded tarball, and is skipped the way Go skips its own main
module: a repository's own licence is not a dependency's, and is not this gate's to judge. Under
yarn or pnpm, neither lockfile format states a licence at all, so `installedDependencies` reads
whatever `node_modules` an earlier step already installed — a symlinked workspace member is
skipped for the same `link` reason, and a package whose own manifest cannot be read or parsed
still yields the dependency under `licence.Unknown` rather than being dropped, because a dependency
dropped over an unreadable manifest is one the gate silently allowed. A `node_modules` that is not
there is the caller's error, never an empty answer: `LicenceSet` reports it as a read that failed,
not a set that came back empty.

**The base set is recomputed at the merge-base, not read from a stored baseline.** The same
throwaway-worktree shape `measureBaseTree` uses for CRAP: check the merge-base out, run the same
licence read there, remove the worktree — cheap here because a licence set costs one manifest read
and one tool invocation, no suite, no compose service, no instrumented build. Unlike
`measureBaseTree`, the checkout is **one worktree for the whole scan**, not one per component:
`newLicenceBaseTree` in `cmd/lydite/scan.go` opens it lazily, the first time any component's
licence gate asks for a set, and every Go, Rust and TypeScript component's base read shares it — a
repository with N components pays one checkout of the merge-base commit, not N of the identical
commit. A worktree that will not check out, a module download that will not resolve, or a
cargo-deny that will not run reports `licence.Unmeasured` on the row, naming what failed; it is
never folded into a passing verdict with an empty base set — a gate that could not run must never
render as one that ran and found nothing. TypeScript's own base, `typescriptLicenceBase`, gates the
checkout on `package.json`'s presence rather than on a lockfile: a component declares one manifest
whichever package manager it uses, but the lockfile that states a licence is npm's alone, and
gating on it would make every yarn or pnpm component's base read as "not there yet" on a merge-base
that plainly has one, reporting its whole existing dependency set as newly introduced. A component
with no `package.json` at the base is one this change adds, so `licenceBaseTree.set` answers it a
measured empty base — every pair it carries at the current tree is one the change introduces, and
that is a measurement that found nothing there rather than a failure — while a base with the
manifest but no readable lockfile is the opposite: a set that could not be read, `unmeasured`,
never folded into the empty base that would grandfather nothing.
A run with no diff base at all — `lydite scan` on `main` — is `context`, reporting the full
non-conforming set and gating nothing, the shape every other diffless scan already has.

