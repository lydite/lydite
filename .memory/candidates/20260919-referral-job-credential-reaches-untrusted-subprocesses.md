---
about: executil's ordinary Run* functions layer extra env onto the calling process's own, which leaks CI credentials into a subprocess that executes head-tree code
saw:
  - source/cli/internal/executil/executil.go
  - source/cli/internal/rustapisurface/rustapisurface.go
  - .github/workflows/lydite-pr.yml
---

`internal/executil`'s `runTo`/`RunQuietEnv` always build the child's environment as
`append(os.Environ(), extraEnv...)` — they extend the calling process's own
environment rather than replacing it. That is safe for every tool `lydite scan`/`lydite
review` ran before this branch, because those subprocesses are lydite's own pinned
tools (clippy, cargo-audit, gosec) or a language's own compiler parsing, not code
under review executing arbitrary logic.

`cargo semver-checks` broke that assumption: comparing a Rust crate's public API means
building rustdoc for the head tree, which compiles and runs that crate's own `build.rs`
and proc-macros — code the pull request controls. `.github/workflows/lydite-pr.yml`'s
`referral` job runs `lydite review --publish` with `GITHUB_TOKEN` in its step env and
`statuses: write` permission (to post `lydite/referral`), while the `scan` job that
already ran Rust code before this branch carries neither. A Rust component opting into
`api_surface` would have handed a malicious `build.rs` a token that can forge the
referral verdict on its own head SHA.

The fix, `executil.RunQuietIsolatedEnv`, is the one `Run*` variant whose `cmd.Env` is
set to exactly the given slice with no `os.Environ()` layered underneath (and a nil
argument is coerced to an empty slice rather than falling back to full inheritance,
since a nil `cmd.Env` means "inherit everything" to `os/exec`). Any future subprocess
invocation that builds or executes source from the scanned tree (not a lydite-owned
pinned tool) needs the same treatment — see
`agentic/rules/give-untrusted-build-scripts-no-inherited-environment.md`.
