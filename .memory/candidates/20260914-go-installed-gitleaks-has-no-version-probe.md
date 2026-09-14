---
about: a go install'd gitleaks binary cannot answer "is the pinned version already installed"
saw:
  - source/cli/internal/secrets/secrets.go
  - source/cli/internal/secrets/pins.go
---

`internal/semgrep` checks an installed tool's own reported version before reinstalling
it. That probe is impossible for gitleaks: a `go install`ed build has no release
ldflags, so `gitleaks version` prints `version is set by build process` regardless of
which tag was actually installed.

`internal/secrets.Check` relies instead on `internal/gotool`'s version-keyed bin
directory (the same guarantee `internal/golang`'s gosec install uses) rather than a
runtime version check — the directory name is the only source of truth for which
version is installed, because the binary cannot be asked.
