---
about: executil's ordinary Run* functions layer extra env onto the calling process's own, which leaks CI credentials into a subprocess that executes head-tree code
saw:
  - source/cli/internal/executil/executil.go
  - source/cli/internal/rustapisurface/rustapisurface.go
  - source/cli/cmd/lydite/review.go
  - source/cli/cmd/lydite/review_publish.go
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

`executil.RunQuietIsolatedEnv` is the one `Run*` variant whose `cmd.Env` is set to
exactly the given slice with no `os.Environ()` layered underneath (and a nil argument
is coerced to an empty slice rather than falling back to full inheritance, since a nil
`cmd.Env` means "inherit everything" to `os/exec`) — any future subprocess invocation
that builds or executes source from the scanned tree needs the same treatment, see
`agentic/rules/give-untrusted-build-scripts-no-inherited-environment.md`. **This alone
does not close the credential exposure**, though: it only keeps the credential out of
the comparison's own `cmd.Env`. It does nothing about `os.Environ()` of the *calling*
process itself, which a same-user descendant can still read via
`/proc/<pid>/environ` (a snapshot taken at exec time — `os.Unsetenv` never touches it)
regardless of anything the calling process does afterward, and `actions/checkout`
separately embeds `github.token` into `.git/config` by default (`persist-credentials`
defaults to true), which any process with filesystem access to the checked-out tree
can read directly with no `/proc` tricks needed at all.

The actual fix is job separation: `.github/workflows/lydite-pr.yml`'s `referral` job
computes the verdict (running the untrusted comparison) with `persist-credentials:
false` and no `GITHUB_TOKEN`/`statuses:write` in its own environment at all; a second
job, `referral-publish`, holds the credential and runs none of the change's own code —
it downloads `referral`'s verdict artifact and posts via the new `lydite review
publish --verdict <path>` subcommand, which recomputes nothing. A credential that was
never in a job's environment or its checkout is the only thing that reliably keeps it
out of that job's process tree; scrubbing a live process's own environment after the
fact is not a substitute.
