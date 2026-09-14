# Configuration

> **The reference for `.lydite/config.yml` and `internal/config`.**

Every file that configures lydite lives in `.lydite/` at the scan root: `config.yml` here,
`components.yml` (see [components.md](components.md)), and `exemptions.yml` (see
[referral-and-clearance.md](referral-and-clearance.md)). One directory rather than a
family of dotfiles beside it, because a dotfile next to a dot-directory that both configure the
same tool is an arrangement nobody can predict the shape of from either half.

`.lydite/config.yml` is optional and does one thing, and tuning severity or suppressing individual
findings is not it (that's what a fix-up pass + inline `#nosec`/`nosemgrep` annotations in the
scanned repo are for): it **opts out** of what lydite's default already does (run every check over
every declared component), and carries the numeric gate knobs
(`coverage.tolerance`, `coverage.patch.tolerance`, `coverage.floor`).

Keys that described a pipeline lydite no longer has are **rejected by name** rather than ignored:
`coverage.source` and the `coverage.{go,rust}` report paths, which located a report some other job
produced, and `rust.exclude` / `typescript.exclude` / `go.exclude`, which narrowed a walk for
manifests. lydite writes every coverage report itself and reads its units from
`.lydite/components.yml`, so those keys
have nothing left to say and are **rejected by name** rather than ignored (see [coverage.md](coverage.md)).

See `internal/config/config.go` for the full schema; shape:

```yaml
rust:
  enabled: true          # set false to run no Rust check over any Rust component
typescript:
  enabled: true
  linter: biome          # the only accepted value. The retired "eslint" is rejected with an
                          # error rather than silently run under Biome (see linters.md).
  install: ""            # override the install-command auto-detection a JavaScript component's
                          # runner does from the lockfile, e.g.
                          # "corepack enable && yarn install --immutable"
go:
  enabled: true
semgrep:
  enabled: true
  config: auto           # override to a custom registry ref/path if needed
toolchain:
  enabled: true          # set false to keep the diagnostics but never download/install
                          # (air-gapped runners, or images that preprovision everything)
  # go/rust/node: deliberately unset. The versions come from the repo's own
  # manifests — see toolchains.md. These keys exist only as a local override.
coverage:
  tolerance: 0.1         # pp a coverage figure may dip below its baseline before the gate fails;
                          # absorbs sub-tenth measurement noise ("86.1% vs baseline 86.1%,
                          # regressed 0.0%"). Compared at display precision (tenths); 0 = fail any
                          # dip the report can show. Must be finite and non-negative — Load
                          # rejects anything else.
  floor: 0               # minimum coverage any single measured component must reach. 0 = off,
                          # which is the default: it is opt-in, so upgrading never starts failing
                          # a repo over a gap it has always had. This is the signal line-weighting
                          # removes — see coverage.md.
  patch:
    tolerance: 0.1       # the patch gate's own dip allowance — deliberately independent, so
                          # loosening the aggregate knob never weakens the untested-new-code check
```

Omitting the file, or omitting a section/key within it, keeps that value at its default — see
`internal/config/config_test.go` for the exact merge semantics.

