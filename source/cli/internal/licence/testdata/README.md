# Captured licence evidence

Every file here is the real output of a real tool over a real project, captured verbatim. The
probe trees the captures name are committed beside them, each file suffixed `.txt` and
materialised through `internal/fixture`; see that package for why the suffix exists.

A capture alone cannot say whether the gate passed, for the reason `internal/rust/testdata`
states: cargo-deny exits with a bitmask of which checks failed — licenses 4, bans 2, 6 for both
— so each `<name>` fixture that stands in for a run has a sibling `<name>.exit` holding the
integer the tool exited with, and a verdict read off it tests for non-zero.

Absolute paths are rewritten to `$GOMODCACHE` and `$REPO`, and klog's timestamp-and-pid prefix
is reduced to its severity letter, so a capture is comparable between machines. Nothing else is
edited.

## The probes

`goprobe` is a Go module whose dependency set covers the three cases a classifier has to get
right: `github.com/juju/errors` is LGPL-3.0, the strong-copyleft case a permissive allow-list
must reject; `github.com/hashicorp/go-version` is MPL-2.0, weak copyleft; and
`gopkg.in/yaml.v2` carries two licence files, `LICENSE` (Apache-2.0) and `LICENSE.libyaml`
(MIT).

`denyprobe` is a crate whose dependency set covers the same ground for cargo-deny:
`cbindgen` is MPL-2.0 and `libc` is `Apache-2.0 OR MIT`, the dual-licence case an allow-list
satisfies through either half. `cbindgen` is named on line 12 of `Cargo.lock.txt`, which is
where a claim against it is located.

## Go

| Fixture | Exit | What it captures |
|---|---|---|
| `licensecheck-goprobe.txt` | — | `github.com/google/licensecheck` v0.3.1 classifying every licence file at each module root of `goprobe`. The `[Unknown]` beside each id is `licensecheck.Match.Type`, which v0.3.1 leaves unset; the id and the coverage percentage are what carry the answer |
| `go-licenses-goprobe.txt` | 0 | `go-licenses csv ./...` over the same module: three rows, one licence per module — `gopkg.in/yaml.v2` reports Apache-2.0 alone and the MIT half is gone — plus an `Unknown` row for the main module |
| `go-licenses-lydite.txt` | 1 | the same command over `source/cli`. It resolves no standard-library package, because the Go toolchain is provisioned as a module, and exits fatally having emitted no CSV row at all |
| `go-list-module-graph.txt` | — | `go list -m -json all` over `source/cli`: 12 of the 20 modules answer no directory, because the module graph names more than the build downloads |
| `go-list-vendored.txt` | — | `go list -deps -json ./...` over `goprobe` with a `vendor/` directory: `Module.Dir` is absent for every vendored module while the package's own `Dir` points into `vendor/`, which is where `go mod vendor` copied the licence files |

## Rust

Each capture is `cargo-deny 0.20.2` run as `deny --format json --config <generated> check
licenses bans` in `denyprobe`, and the NDJSON is its **stderr**: cargo-deny writes nothing to
stdout under `--format json`.

| Fixture | Exit | Config | What it captures |
|---|---|---|---|
| `deny-licenses-rejected.ndjson` | 4 | `deny-policy-strict.toml.txt` | `cbindgen`'s MPL-2.0 rejected under an allow-list that omits it, code `rejected`, with `notes` naming the licence Copyleft. `libc` passes: `Apache-2.0 OR MIT` is satisfied by either half |
| `deny-licenses-allowed.ndjson` | 0 | `deny-policy-permits-mpl.toml.txt` | the same graph under an allow-list that includes MPL-2.0: the summary line alone |
| `deny-licenses-unused-allowance.ndjson` | 4 | `deny-policy-no-unused-key.toml.txt` | the same run with `unused-allowed-license` left at its default. Every allowed licence the graph does not use becomes a `license-not-encountered` warning whose `graphs` array is empty — so it names no crate, locates at no line, and shares one site with every other one |

The `labels` a diagnostic carries point into the generated config (`license-not-encountered`)
or into a manifest cargo-deny synthesised for the crate (`rejected`), never into a file in the
tree, which is why a claim's line is read out of `Cargo.lock` instead.
