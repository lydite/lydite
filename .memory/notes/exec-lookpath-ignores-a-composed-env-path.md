---
name: exec-lookpath-ignores-a-composed-env-path
kind: gotcha
description: exec.LookPath resolves argv[0] against the parent process's own PATH, not a composed env slice about to be passed as cmd.Env — a probe that wants to run a just-provisioned toolchain has to resolve the binary itself.
anchors:
  - path: source/cli/internal/toolchain/ambient.go
    blob: 5cd4894d0e7d
confidence: verified
---

`probeUnder` (`ambient.go:153`) has to run a toolchain a provisioning step just unpacked into a
directory that is not on lydite's own process PATH — only on the composed `PATH` inside `cmd.Env`.
Resolving the binary with a plain `exec.LookPath(p.bin)` silently finds the *ambient* toolchain
instead: `cmd.Env` decides nothing about how `exec.Command`/`exec.LookPath` resolve `argv[0]`,
because that resolution happens in the parent using the parent's own `os.Getenv("PATH")`.

`lookPathIn` (`ambient.go:186`) exists because of this — it walks the composed env's own `PATH=`
entries (last one wins, matching how the child sees it) and stats each candidate directory itself
rather than delegating to `exec.LookPath`. Any code in this package that wants to run a binary from
a `PathDirs` list a provisioning step produced, rather than from the ambient PATH, needs this same
pattern.
</content>
