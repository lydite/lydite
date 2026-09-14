# Toolchains

> **The reference for `internal/toolchain`** — the Go, Rust and Node runtimes lydite provisions.

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

**A toolchain is resolved per component, not per repository.** A component's `dir` is where its
manifest is read, so a workspace declaring `engines.node: >=22` and a tools package pinning 18 keep
their own runtimes — read across every `package.json` with the highest floor winning, they collapse
to one runtime chosen by a rule neither of them stated. Components resolving to the same
requirement are probed and provisioned once and share the result, so the common case costs one
diagnostic line rather than one per component.

It also makes lydite's answer and rustup's the same answer by construction: rustup selects a
toolchain from the directory cargo runs in, and that is now the same directory lydite read the
`rust-toolchain.toml` from.

**The environment is a value handed to each child, never a change to lydite's own process.**
Components run concurrently in one process, so a `PATH` written into that process can hold one Node
version — which is the whole of what "one runtime per repository" was. Two things about that are
easy to get wrong and invisible in argv:

- **A child's environment is a flat list where the last occurrence of a key wins.** Two callers
  each prepending their own directories produce two `PATH` entries and one is silently discarded.
  So `runner.Invocation` carries directories rather than a finished `PATH=` string, and
  `toolchain.Compose` is the one function that turns every caller's directories into the one entry.
- **`os/exec` resolves a bare program name against *this* process's `PATH`**, when the command is
  constructed; `cmd.Env` is applied afterwards and has no bearing on it. `executil` therefore
  resolves the program against the environment being handed to the child. Without it a toolchain
  lydite had just provisioned is one the child can use and the lookup cannot find — reproduced as
  `npm ci: executable file not found in $PATH`, printed immediately after lydite reported
  installing the Node that held it.

**Versions come from the repo's own manifests, never from `.lydite/config.yml`.**

| Language | Version source |
|---|---|
| Go | the `go` and `toolchain` directives in the component's own `go.mod`, higher of the two |
| Rust | `channel` in the component's `rust-toolchain.toml`, else the legacy bare `rust-toolchain` |
| TypeScript | `engines.node` in the component's `package.json`, else `.nvmrc` (component, then scan root) |

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

Each language provisions differently, and only one of the three downloads anything:

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
  failure recorded in [ci.md](ci.md), and it reproduces on a runner whose ambient toolchain is
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
  **The probe does not ask rustup what is installed**, and that is a real limit
  rather than an oversight: it asks the ambient `cargo` its version, from lydite's own
  working directory. A component pinning a channel older than the machine's default
  therefore reads as satisfied and is never materialised, and rustup fetches it lazily
  during `cargo clippy` — without the components, which is the failure this provisioning
  exists to prevent. Per-component resolution narrows it rather than causing it: taking
  the highest channel across every crate left the same hole. Closing it means asking
  `rustup` which toolchains and components are present ([#55](https://github.com/lydite/lydite/issues/55)).

  Installing is not selecting, and the two come apart in exactly one case. rustup picks a toolchain
  by reading `rust-toolchain.toml` from the directory cargo runs in, which covers the normal case
  for free — `internal/rust` runs cargo inside the crate directory and a Rust component's
  instrumented variant runs inside the component's, so the file lydite read is the file rustup
  reads. A version supplied by `toolchain.rust` in
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
already knows which languages a repository declares and where, so it reads each component's own
manifest; and it is the only place that helps wardnet at all, since wardnet calls `wardnet/bulwark@v1`
directly rather than through gt. See [ADR 0004](../../docs/adr/0004-ensure-language-toolchains.md).

Downloads land in the same version-keyed `~/.cache/lydite` layout every other lydite-managed
install already uses, so a consumer caching that one path (as wardnet's workflow already does)
covers language toolchains too with no key change. Installs stage into a sibling temp directory and
are renamed into place, so an interrupted download can never leave a half-populated toolchain that
the next run mistakes for a complete one.

