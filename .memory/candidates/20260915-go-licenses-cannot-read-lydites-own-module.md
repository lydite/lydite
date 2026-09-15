---
about: google/go-licenses cannot enumerate lydite's own dependencies, which ruled it out as the Go licence classifier in favour of an in-process library
saw:
  - source/cli/internal/golang/licence.go
  - source/cli/internal/licence/testdata/go-licenses-lydite.txt
---

Evaluating candidates for ADR 0038's Go licence source, `go-licenses csv ./...` run inside
`source/cli` exits 1 and emits no CSV row at all: 55 lines of `Package <stdlib pkg> does not
have module info. Non go modules projects are no longer supported`, then a fatal
`some errors occurred when loading direct and transitive dependency packages`. The cause is
that this repository's Go toolchain is provisioned as a module,
`golang.org/toolchain@v0.0.1-go1.26.6` (see `internal/toolchain`), and `go-licenses`' loader
does not recognise that shape.

Over a plain probe module (no toolchain-as-module quirk) `go-licenses` does run, but reports
one licence per dependency module and drops the MIT half of a dual-licensed module
(`gopkg.in/yaml.v2` carries both `LICENSE` (Apache-2.0) and `LICENSE.libyaml` (MIT); only the
Apache-2.0 one is reported) and emits an `Unknown` row for the main module under test. It also
does not need cgo, so this repository's no-cgo rule does not rule it out — the toolchain-read
failure does.

The chosen replacement, `github.com/google/licensecheck` (`internal/golang/licence.go`'s
`classifyModule`), reads licence files in-process off disk and needs no module-graph loader at
all, so it has no equivalent failure mode. Anyone evaluating a Go SCA/licence tool against this
repository should expect a loader that assumes a normal `GOROOT`-based toolchain to choke on
lydite's own `source/cli` tree specifically, independent of that tool's general quality.
