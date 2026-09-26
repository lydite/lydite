---
about: executil's ordinary Run* functions layer extra env onto the calling process's own, which leaks CI credentials into a subprocess that executes head-tree code; the credential-free-compare / credentialed-decide split narrows but does not close the tamper gap
saw:
  - source/cli/internal/executil/executil.go
  - source/cli/internal/rustapisurface/rustapisurface.go
  - source/cli/internal/tsapisurface/tsapisurface.go
  - source/cli/internal/reviewdecision/surface.go
  - source/cli/internal/stages/clearance/fingerprint.go
  - source/cli/cmd/lydite/review.go
  - source/cli/cmd/lydite/review_compare.go
  - agentic/rules/give-untrusted-build-scripts-no-inherited-environment.md
---

Re-checked on `refactor/flow-architecture-clearance-pilot`: the API-surface comparison moved out
of `cmd/lydite/review_apisurface.go` into `internal/reviewdecision`. The claim holds; only the
pointers moved (`computeAPISurfaces` is now `reviewdecision.Surfaces`/`CompareSurfaces`,
`reconcileSurfaces` is now unexported in `internal/reviewdecision/surface.go`).

**CI shape.** The `referral`/`referral-publish` job split this note grew out of lived in
`.github/workflows/lydite-pr.yml`, deleted 2026-09-22 (ADR 0051's amendment). Everything below
about the Go-level exposure still governs any job with that shape — a consumer's paired
comparison job feeding `lydite/actions`' `lydite-referral-publish.yml`, or whatever gt renders.

**The leak.** `internal/executil`'s `runTo` and `RunQuietEnv` build the child environment as
`append(os.Environ(), extraEnv...)` (`executil.go`, `RunQuietEnv` and `runTo`). That is safe for
lydite's own pinned tools, and unsafe for `cargo semver-checks` (builds rustdoc for the head
tree, running the crate's own `build.rs` and proc-macros) and for `internal/tsapisurface`'s npm
installs (running both trees' lifecycle scripts). A credentialed job running those would hand a
malicious build script a token that can forge the verdict.

**What closes the child's env.** `executil.RunQuietIsolatedEnv` sets `cmd.Env` to exactly the
given slice (a nil slice is coerced to empty, since a nil `cmd.Env` means "inherit everything").
Its callers are `internal/rustapisurface/rustapisurface.go` and `internal/tsapisurface/tsapisurface.go`.
This alone does **not** close the exposure: `/proc/<pid>/environ` of the calling process is a
snapshot taken at exec time, so a same-user descendant can still read the credential, and
`actions/checkout` embeds `github.token` into `.git/config` unless `persist-credentials: false`.

**So the comparison is refused in-process whenever a credential is held.**
`reviewdecision.CompareSurfaces(..., guardCredential, ...)` reports every opted-in component
whose comparison runs the tree's own code (`untrustedBuild`) as `Uncomputable` when
`guardCredential` is true. `review` passes `doPublish` (`cmd/lydite/review.go`); `review compare`
passes `false` because it never publishes (`cmd/lydite/review_compare.go`); the clearance
Fingerprint stage passes `true` unconditionally (`internal/stages/clearance/fingerprint.go`,
`clearedDecision`), because a clearance run always holds a token.

**Only raw comparison data crosses the job boundary, never a decision.** `lydite review compare
--write-surfaces <path>` writes `reviewdecision.WriteSurfaces`'s document (results keyed by
component, plus the base). The credentialed job runs `review --surfaces <path>` (or `clearance
--surfaces <path>`), which reads it through `reviewdecision.Surfaces` → `reconcileSurfaces`:
the document's base must equal the base this run resolved itself, every component this tree's
own `components.yml` opts in must have a result, and `Component`/`Dir` are overwritten from this
tree, never taken from the document.

**Open gap.** `reconcileSurfaces` checks shape, not content. A per-component clean result *is*
the api-surface verdict for that component; a process a malicious build script leaves running
past its own subprocess call can overwrite the artifact with a well-formed all-clean document
(right base, every opted-in component) before upload. Closing it needs a sandbox that cannot
write the output path after the comparison exits, or treating every document-supplied clean
result for an untrusted-build component as unverifiable. Tracked in `agentic/references/ci.md`
and the rule file above.

**Two workflow traps found alongside it** (still true of any job of this shape):
- `uses: ./.github/actions/<name>` resolves from whatever the job's checkout holds — the pull
  request's own `action.yml` after a head checkout. Actions does **not** evaluate `${{ }}` in
  `uses:`, so `owner/repo/path@${{ ...base.sha }}` fails to parse. The working shape checks the
  base commit out at its own path and references `./base/.github/actions/<name>`.
- A pinned action does not pin the binary it runs: a `lydite` built from the pull request's own
  tree can have its `publish` rewritten to always succeed. The credentialed job must build
  `lydite` from the base checkout, not reuse the PR-built artifact.
