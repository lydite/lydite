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

The shape that gets closer splits along a different boundary: **only the raw
comparison** crosses the job boundary, never a decision. `lydite review compare
--write-surfaces <path>` (`internal/rustapisurface`'s and Go's `internal/apisurface`'s
raw findings, keyed by component, with the base they were measured against) is the
whole of what the credential-less `referral` job produces — it reads no exemptions and
makes no decision. `referral-publish` holds the credential, checks the change out
separately, reads its exemptions and diff as text, and runs `lydite review --surfaces
<path> --publish` to decide and post in one step that never re-executes the comparison.
`reconcileSurfaces` (`review_apisurface.go`) refuses the document if its claimed base
does not match what `referral-publish` resolves *itself*, and requires a result named
for every component this tree's own `components.yml` says opted in.

**This still does not close the exposure — a fourth review pass caught that too.**
"No verdict in the artifact to tamper with" was the wrong claim: a per-component clean
result (empty findings) *is* the api-surface verdict for that component, and
`reconcileSurfaces` never verifies a result's *content*, only its shape (right base,
one entry per opted-in component). A process a malicious `build.rs` leaves running past
its own subprocess call can still overwrite the artifact with a well-formed document —
correct base (derivable from the full-history checkout `referral` already has), an
entry for every component (derivable from `components.yml`, which the same checkout
holds), each with no findings — before the upload step captures it. `referral-publish`
has no way to tell that apart from a real clean comparison without re-running it, which
would put the credential back in the same process as the untrusted code. **This gap is
open, tracked in ci.md's `referral`/`referral-publish` paragraph, and not something this
branch closes** — closing it needs either a sandbox that can't write the output path
after the comparison exits, or treating every document-supplied clean Rust result as
unverifiable and referring it (which would mean an opted-in Rust component can never
auto-pass through this path, only fail-or-refer).

**A second trap the same review found, syntactic rather than architectural**: a
workflow step written as `uses: ./.github/actions/<name>` resolves from whatever the
*job's own checkout* currently holds, not from the workflow file's own trusted source —
so `referral-publish` (which checks out the pull request's own head to read its
exemptions/diff) cannot use a `./`-local action after that checkout without running
whatever `action.yml` the pull request itself wrote. The first attempted fix,
`uses: owner/repo/path@${{ github.event.pull_request.base.sha }}`, does not work at
all: **Actions does not evaluate `${{ }}` expressions in a step's `uses:` value** —
this is a hard parser limitation, not a permissions or resolution issue, and a fifth
review pass caught that the whole job would fail to run. The fix that actually works:
check out the base commit into its own path (`actions/checkout` with `ref:
${{ github.event.pull_request.base.sha }}` and `path: base`) and reference the local
action from there (`uses: ./base/.github/actions/<name>`) — a literal relative path,
no expression needed, because the *checkout* (not the `uses:` line) is what varies by
ref.

**A third, related trap**: even a correctly-pinned action reference only protects the
composite action's own YAML, not the actual `lydite` binary those actions install —
`setup`'s `lydite-binary` artifact is built from the pull request's own checkout, so a
pull request editing `cmd/lydite/status.go`'s `publish` function to always report
success is exactly as available as editing a `build.rs`. `referral-publish` closes this
by building its own `lydite` from the same `base/` checkout it uses for the action
reference, rather than downloading `setup`'s artifact at all. Every *other* job in this
workflow intentionally dogfoods the pull request's own build (stated in the workflow's
own top comment) — this is the one job where that tradeoff does not hold, because it is
the one job holding a write credential.
