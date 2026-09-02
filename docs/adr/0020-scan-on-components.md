# `lydite scan` reads its units from the declaration, and detection is deleted

[ADR 0016](0016-components-and-lydite-run-tests.md) makes the component the unit
lydite builds, tests, schedules and selects, and
[ADR 0019](0019-coverage-per-component-gated-by-lydite-test.md) makes it the unit
coverage is measured and gated over. `lydite scan` was the last command that
still discovered its own units, by walking the tree for `Cargo.toml`,
`package.json` and `go.mod`. This finishes the move.

**A component names a runner, the runner implies a language, and its `dir` says
where that language's code is. That is what `lydite scan` runs each language's
checks over. `internal/detect` is deleted, the three `exclude` keys with it, and
language toolchains resolve per component rather than once per repository.**

## Deleted, not kept beside the declaration

A discovery mechanism kept next to a declaration is two answers to one question,
and the one that rots is the one nothing exercises. Nothing would have failed as
they drifted: a repository whose declaration and whose tree disagree would be
tested over one set of directories and scanned over another, and every run would
still report a verdict.

The declaration is also the better answer where the two differ. A vendored
`Cargo.toml` no component builds is a crate detection audits and nobody compiles;
a `package.json` in a fixture directory is a package detection lints. Both are
scanned today, and neither is code this repository ships.

What makes deleting it safe is that the declaration cannot quietly go stale: a
source file under no component and no exclude is an **Orphan**, and reporting one
is a gate. That is the property ADR 0016 introduced precisely so a declared list
could replace a discovered one, and this decision is the second half of it.

**That backstop has a hole, and it is named rather than papered over.** A
component rooted at `.` covers every path in the repository, so a Go component at
the root leaves a TypeScript directory beside it orphaning nothing while no
TypeScript check ever runs. The orphan gate also belongs to `lydite test`, and a
consumer can run `scan` without it. So `scan` warns on stderr about source no component's checks reach, reading
git's file list and file extensions and no manifest — the same kind of question
the orphan gate asks, deciding nothing and returning no unit. It is a warning
and not a gate because what a repository should do about it is declare a
component or write the exclude, which is `lydite test`'s demand to make; what
must not happen is the narrowing being silent.

It asks a different question from the orphan gate's, and the difference is the
language: the gate asks whether any component *contains* a file, because a
component tests what is under it whatever it is written in, while a scanner is
per language. **For Go it asks one thing more**, because containment is not
enough there: a nested `go.mod` starts a separate module the enclosing module's
package graph excludes, so `./...` at an ancestor never compiles it and neither
gosec nor govulncheck sees it. A component rooted at `.` in a repository with a
second module at `sdk/` contains every Go file in the tree and scans only its
own. That was verified against the tools rather than reasoned about — the same
G306 in both modules is reported once — and it is exact rather than heuristic,
being a property of the go command. The same property supplies the exception:
a `go.mod` under `testdata/`, or under a `.`- or `_`-prefixed directory, is not
a boundary, because the go command ignores those directories too — so a fixture
module is no more scanned by its parent than it was before, and warning about
one would fire on an ordinary repository layout.

**Rust deliberately gets no equivalent rule.** A `Cargo.toml` between the
component root and a file may be a workspace member cargo already covers or an
unrelated crate it does not, and telling those apart means reading the manifest.
A path-shaped rule would warn about crates that are perfectly well scanned,
which is how a diagnostic earns being ignored. TypeScript needs none: Biome
walks the tree from where it is pointed.

## A repository declaring no components is an error, not a row

`lydite scan` with nothing declared has nothing to scan. Rendering that as an
`unmeasured` row would leave the job green over an entirely unscanned
repository — a security scan that silently stopped, which is the failure this
codebase has already shipped once in another form
(`wardnet/wardnet#957`, where patch coverage never ran and the pull-request
comment read as though it had).

So it is an error: exit 1, naming `.lydite/components.yml`. That is consistent
with `lydite test`, which is already loud in the same situation because every
source file in such a repository is an orphan. It is also work the author can
do — declaring a component is one file — which is what separates a **Gate** from
a **Referral**.

The cost is real and accepted: Semgrep is root-scoped and component-independent,
so it would have run. A partial scan that renders as a scan is worse than a
refusal that names the fix.

**Rejected: falling back to a walk when no components are declared.** That is
keeping detection, with the added property that which mechanism decided a run's
scope depends on a file's absence — so the walk would be exercised only by
repositories nobody had migrated, which is exactly where a stale mechanism does
its damage.

## `rust.exclude`, `typescript.exclude` and `go.exclude` go with it

Each fed exactly one thing: a detection walk. With the walk gone they have
nothing left to narrow, so they are rejected by name rather than ignored — the
stance `coverage.source` and `linter: eslint` already take, and for the same
reason. A dropped key means a repository scanning something other than what its
author wrote while every run still reports a pass.

lydite's own configuration is the worked example. It excluded six `*-pin`
directories by name, and under the declaration every one of them is out of scope
for a reason that needs no configuration: a nested `go.mod` is excluded from its
parent's package graph by Go itself, the pinned `Cargo.toml` files are members of
no workspace and lydite declares no Rust component, and `biome-pin` holds no
TypeScript. The file was left holding nothing but comments, and is deleted.

**Not extended to `components.yml`'s `excludes`.** That key says no component
*tests* a path. Reading it as also saying no scanner *looks* at one would be
answering two questions with one declaration — the mistake refused for
`rust.enabled` in the orphan gate, and for excludes in affected selection. A
repository wanting a subdirectory inside a component left unlinted states that in
that language's own tool configuration, which is where the rest of its lint
policy already lives.

For TypeScript that is narrower than it sounds, and worth saying rather than
leaving to be discovered: lydite always passes `--config-path`, which beats the
scanned project's own root `biome.json`, so ignores written there have no effect
on lydite's run. What does work is a `biome.json` in the subdirectory carrying
`"root": false`, which Biome merges into lydite's config — the same merge that
is a real limitation in the other direction, since such a file can also narrow
what lydite checks.

## Toolchains resolve per component

`engines.node` was read from every detected package and the highest floor won. A
monorepo whose `web/` needs Node 22 and whose `tools/` pins Node 18 therefore got
one runtime, chosen by a rule neither package stated. The component is the unit
that makes the question answerable, and all three languages resolve the same way:
Go reads the component directory's `go.mod`, Rust reads the `rust-toolchain.toml`
beside it — which is also the directory cargo runs in, so lydite and rustup now
read the same file rather than agreeing by luck — and Node reads that
component's `engines.node`, then a `.nvmrc` beside it, then the scan root's.

**Reading the same file is not the same as reaching the same answer**, and for
Rust it does not. The probe asks the ambient `cargo` its version from lydite's
own working directory, so a component pinning a channel *older* than the
default reads as satisfied and is never provisioned — rustup then fetches that
channel lazily in the middle of `cargo clippy`, without the `clippy` and
`rustfmt` components, where a missing component reads as a check failure rather
than a setup step. Resolving per component does not introduce this (taking the
highest channel across every crate left the same hole, for the same reason) and
narrows it, since a component pinning a channel *newer* than the default is now
provisioned where before only the maximum was. Closing it means asking rustup
which toolchains and components are installed rather than asking cargo its
version, which is a different question from the one this decision is about. Tracked as
[#55](https://github.com/lydite/lydite/issues/55).

Components resolving to the same requirement are probed and provisioned once and
share the result, so the common case costs one diagnostic line rather than one
per component.

### The environment is a value, not a change to this process

Resolution per component forces it. `Env.Activate` wrote the result into
lydite's own environment, and components run concurrently in one process — so a
process-wide `PATH` can hold one Node version, which is the whole of the
behaviour being removed.

Two consequences that are not obvious, and neither is visible in argv:

**A child's environment is a flat list where the last occurrence of a key wins.**
Two callers each prepending their own directories produce two `PATH` entries, one
of which is silently discarded. So `runner.Invocation` carries directories rather
than a finished `PATH=`, and one function composes them.

**`os/exec` resolves a bare program name against *this* process's `PATH`**, when
the command is constructed; `cmd.Env` is applied afterwards and has no bearing on
it. Without resolving the program against the environment being handed to the
child, a toolchain lydite had just provisioned and put on the child's `PATH`
would be one the child could use and the lookup could not find. That is not
hypothetical: it reproduced as `npm ci: executable file not found in $PATH`,
printed moments after lydite reported installing the Node that held it.

A third consequence is smaller and worth stating because it is surface a
repository can see: **`PATH` is the one variable a component cannot simply
declare.** A component's `env:` is composed into the child like any other
variable, but the composed `PATH` would always be the later entry and would win,
so a declared one is folded into the composition instead — and it goes **behind
the inherited path**, which is a boundary rather than a preference. lydite now
resolves a program against the environment it hands the child, so a declared
directory ahead of the inherited one would let `.lydite/components.yml` choose
which `go`, `cargo`, `npm` or `sh` lydite itself launches: a repository shipping
`ci-bin/go` and declaring `env: {PATH: ci-bin}` would have `lydite scan` install
gosec with that binary, on a runner whose own toolchain had just been verified.
A component may extend the path its suite runs with; it may not choose the
toolchain lydite runs.

**The same boundary is why a check and an install do not share an
environment.** `go install`, `cargo install` and `npm ci` read `GOPROXY`,
`GOSUMDB`, `CARGO_REGISTRIES_*` and `npm_config_registry`, so a declared
environment reaching them chooses where lydite fetches the scanner it is about
to run — without touching `PATH` at all. Reproduced: a component declaring
`GOPROXY: http://127.0.0.1:1` made `lydite scan` try to fetch gosec from it.
Caching makes it worse rather than better, since the key names the tool's
version and not where it came from, so one poisoned build outlives the run and,
on a runner sharing `~/.cache/lydite`, reaches other repositories. So
`executil.Env` carries the two separately: the checks get the component's
declared environment, and lydite's own provisioning gets only the toolchain
lydite resolved.

This does not make a scan immune to the repository it scans, and it is not
meant to. A repository already influences its own scan through its own
manifests and tool configuration — a nested `biome.json`, a `.semgrepignore`
([#28](https://github.com/lydite/lydite/issues/28)) — and that is a known,
tracked property of scanning a repository at all. What is closed here is
narrower and worse: influence over lydite's *own* binaries, which persists in a
cache and crosses repositories.

## Consequences

- A consumer upgrading must declare its components before `lydite scan` will run,
  and must delete any `*.exclude` key. Both refusals name the fix.
- Scan rows are labelled by component name — `gosec(cli)`, `cargo clippy(api)` —
  rather than by a relative directory applied only when a walk found more than
  one unit. The name is the only one of the two unique by construction, and it is
  the token `lydite test`'s rows already carry.
- A component declaring a raw `command:` implies no language, so nothing scans
  it. It gets an `unmeasured` row saying so rather than being skipped in silence,
  which would read as a component that was scanned and found clean.
- A Go component whose `dir` is not its module root resolves an unpinned
  requirement, so `GOTOOLCHAIN` is not pinned for it. Such a component is broken
  in louder ways first — `govulncheck` exits with `no go.mod file` and
  `internal/coverage` reports it unmeasurable — and the diagnostic names the
  component that declared no version, so the quiet half is at least said out
  loud.
- A language disabled in `.lydite/config.yml` produces no rows at all. That is an
  opt-out the repository stated, not a check that failed to run, and a row per
  opted-out component trains readers to skip the tag that exists to be noticed.
- A Cargo workspace is scanned once, at the component root, rather than once per
  member crate that detection resolved; a JavaScript workspace is linted once
  rather than once per nested `package.json`. Both are what the build tool
  already treats as a whole.
