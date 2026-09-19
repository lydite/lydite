---
about: executil's ordinary Run* functions layer extra env onto the calling process's own, which leaks CI credentials into a subprocess that executes head-tree code
saw:
  - source/cli/internal/executil/executil.go
  - source/cli/internal/rustapisurface/rustapisurface.go
  - source/cli/cmd/lydite/review.go
  - source/cli/cmd/lydite/review_compare.go
  - source/cli/cmd/lydite/review_apisurface.go
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

The actual fix went through two shapes. The first split `referral` into "compute the
verdict" (no credential) and "publish it" (the credential, no untrusted code) — but a
follow-up review caught that this still let the job holding no credential decide the
*whole* verdict, including reading exemptions and rendering the decision, and upload
that decision as the trusted artifact. A background process a malicious `build.rs`
left running past the comparison's own call could still overwrite that artifact with a
forged "pass" before the upload step captured it — the decision computed in the same
job that ran the untrusted code is not safe to trust just because a *different* job
posts it.

The shape that closes this splits along a different boundary: **only the raw
comparison** crosses the job boundary, never a decision. `lydite review compare
--write-surfaces <path>` (`internal/rustapisurface`'s and Go's `internal/apisurface`'s
raw findings, keyed by component, with the base they were measured against) is the
whole of what the credential-less `referral` job produces — it reads no exemptions and
makes no decision, so there is no verdict in the artifact for a leftover process to
tamper with. `referral-publish` holds the credential, checks the change out separately
(`persist-credentials: false` again, and never via a `./`-local action after that
checkout — see below), reads its exemptions and diff as text, and runs `lydite review
--surfaces <path> --publish` to decide and post in one step that never re-executes the
comparison. A credential that was never in a job's environment, its checkout, or its
artifact is the only thing that reliably keeps it out of that job's process tree;
scrubbing a live process's own environment after the fact is not a substitute, and
neither is trusting a decision a job holding no credential made about code it also ran.

**A second, distinct trap the same review found**: a workflow step written as
`uses: ./.github/actions/<name>` resolves from whatever the *job's own checkout*
currently holds, not from the workflow file's own trusted source. `referral-publish`
checks out the pull request's own head (to read its exemptions/diff), so a `./`-local
action referenced *after* that checkout would run whatever `action.yml` the pull
request itself wrote — in the one job that holds `statuses: write`. The fix is a
remote, base-ref-pinned reference instead: `lydite/lydite/.github/actions/<name>@${{
github.event.pull_request.base.sha }}`, which is fetched independently of the job's own
checkout and always resolves the base branch's own version of the action, never the
PR's. Every other job in this workflow also checks out the PR head and then uses a
local `./` action, but none of them hold a write credential, so the same combination
does not carry the same risk there — this is specific to a job that both checks out
untrusted code and can act on its behalf afterward.
