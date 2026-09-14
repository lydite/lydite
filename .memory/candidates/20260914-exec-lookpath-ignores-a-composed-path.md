---
name: exec-lookpath-ignores-a-composed-path
kind: gotcha
about: source/cli/internal/toolchain/ambient.go
description: exec.LookPath resolves argv[0] against the parent process's own PATH, not a composed env slice about to be passed as cmd.Env — a probe that wants to run a just-provisioned toolchain has to resolve the binary itself.
anchors:
  - path: source/cli/internal/toolchain/ambient.go
    blob: 88fa9da42d652c29a78b1ac48897ae622a35e956
confidence: verified
---

`probeUnder` (`ambient.go`, added alongside `lookPathIn`) has to run a toolchain a
provisioning step just unpacked into a directory that is not on lydite's own process
PATH — only on the composed `PATH` inside `cmd.Env`. The first attempt to resolve the
binary with `exec.LookPath(p.bin)` silently found the *ambient* toolchain instead: a
child's `cmd.Env` decides nothing about how `exec.Command`/`exec.LookPath` resolve
`argv[0]`, because that resolution happens in the parent using the parent's own
`os.Getenv("PATH")`.

`lookPathIn` (`ambient.go`) exists because of this — it walks the composed env's own
`PATH=` entries (last one wins, matching how the child sees it) and stats each
candidate directory itself (`Perm()&0o111 != 0`) rather than delegating to
`exec.LookPath`. Any code in this package that wants to run a binary from a `PathDirs`
list a provisioning step produced, rather than from the ambient PATH, needs this same
pattern — `exec.LookPath` cannot be trusted for it.
