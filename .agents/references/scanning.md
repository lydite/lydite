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
`Result.Findings` — so `Result.Detail` stays empty for every tool that prints its own findings,
and what a reader needs beyond the one-line claim travels per finding in `Finding.Detail`.
Biome is the exception it already was: its report goes to a file so its own chatter cannot
corrupt the JSON, so nothing streams and `Detail` is the only place its findings exist. See
[ADR 0032](../../docs/adr/0032-every-scanner-reports-its-findings-as-data.md).

**Three checks run their tool twice**, because it cannot write a report and print for a human in
one invocation. For `cargo clippy`, `cargo-audit` and `cargo-deny` the second pass buys back the
terminal output, and it is cheap — clippy's is 0.24s against a first pass of 1.76s, because cargo
replays cached diagnostics rather than recompiling. For `govulncheck` it buys back the **verdict**:
under `-format json` it exits 0 whether or not it found anything, while the text run exits 3, so a
single JSON run would report every advisory and pass the check. `gosec` and Semgrep need one pass
each — `-fmt json -out <file> -stdout -verbose text` and `--json-output=<file>` both write a copy
rather than a replacement.

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

**`scan` has no `--component` or `--affected`.** Selection is `lydite test`'s surface today; a scan
that narrowed itself would need the same widening-on-ignorance argument made again, and nothing
asks for it yet.

