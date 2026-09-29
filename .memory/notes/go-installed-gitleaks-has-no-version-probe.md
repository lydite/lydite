---
name: go-installed-gitleaks-has-no-version-probe
kind: gotcha
description: unlike Semgrep, gitleaks's installer cannot ask the binary its own version to decide whether to reinstall — a go install'd binary carries no release ldflags, so it relies entirely on gotool's version-keyed cache directory name.
anchors:
  - path: source/cli/internal/secrets/secrets.go
    blob: 816b462ed276
  - path: source/cli/internal/secrets/pins.go
    blob: c083508d0cf2
  - path: source/cli/internal/gotool/gotool.go
    blob: 736ee234b088
  - path: source/cli/internal/semgrep/semgrep.go
    blob: 5abe52c62ec9
confidence: verified
---

`internal/semgrep`'s `ensure` (`semgrep.go:156-165`) checks an installed tool's own
reported version (`semgrep --version`) against the pinned one before reinstalling.
`internal/secrets.Check` (`secrets.go:71-72`) has no equivalent check — it calls
`gotool.Ensure(ctx, nil, "gitleaks", gitleaksVersion, gitleaksPkg, "")` directly with the
pin from `pins.go` (`gitleaksVersion = "v8.30.1"`).

That asymmetry isn't a gap: a `go install`ed gitleaks binary carries no release ldflags,
so a runtime version probe (`gitleaks version`) cannot distinguish which tag was
actually installed the way `semgrep --version` can for a pip-installed tool. Instead,
`gotool.Ensure` (`internal/gotool/gotool.go:58-78`) keys its cache directory by the
version string itself (`BinDir(name, version, key)`, `gotool.go:59`) and skips the
install `if _, err := os.Stat(bin); err == nil` (`gotool.go:64-66`) — the directory name
is the only source of truth for which version is installed, because the binary itself
cannot be asked. Anyone adding a new go-installed scanning tool with no build-time
version stamping needs the same directory-keyed approach, not a `--version` check.
