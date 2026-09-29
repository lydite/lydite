---
name: referral-job-credential-reaches-untrusted-subprocesses
kind: gotcha
description: executil's ordinary Run* functions layer extra env onto the calling process's own environment, which would leak a CI credential into a subprocess that executes head-tree code (cargo semver-checks' build.rs, npm lifecycle scripts) unless the comparison is either isolated or refused outright in a credentialed process.
anchors:
  - path: source/cli/internal/executil/executil.go
    blob: 1f8345f1508d
  - path: source/cli/internal/rustapisurface/rustapisurface.go
    blob: debe4b008887
  - path: source/cli/internal/tsapisurface/tsapisurface.go
    blob: 97cf980e6f5d
  - path: source/cli/internal/reviewdecision/surface.go
    blob: 4f3bc50e32be
  - path: source/cli/internal/stages/clearance/fingerprint.go
    blob: 516836b3bf3a
  - path: agentic/rules/give-untrusted-build-scripts-no-inherited-environment.md
    blob: c0bcf298586e
confidence: verified
---

`internal/executil`'s `runTo` and `RunQuietEnv` build the child environment as
`append(os.Environ(), extraEnv...)` — safe for lydite's own pinned tools, unsafe for `cargo
semver-checks` (builds rustdoc for the head tree, running the crate's own `build.rs` and
proc-macros) and `internal/tsapisurface`'s npm installs (running both trees' lifecycle scripts). A
credentialed job running those would hand a malicious build script a token that can forge the
verdict.

**What closes the child's env.** `executil.RunQuietIsolatedEnv` sets `cmd.Env` to exactly the given
slice (confirmed at `executil.go:249`) — its callers are `internal/rustapisurface/rustapisurface.go`
and `internal/tsapisurface/tsapisurface.go`. This alone does **not** close the exposure:
`/proc/<pid>/environ` of the calling process is a snapshot taken at exec time, so a same-user
descendant can still read the credential, and `actions/checkout` embeds `github.token` into
`.git/config` unless `persist-credentials: false`.

**So the comparison is refused in-process whenever a credential is held.**
`reviewdecision.CompareSurfaces(..., guardCredential, ...)` (confirmed at `surface.go:145`) reports
every opted-in component whose comparison runs the tree's own code (`untrustedBuild`) as
`Uncomputable` when `guardCredential` is true. The clearance Fingerprint stage
(`clearedDecision`, `fingerprint.go`) passes it unconditionally, because every clearance run holds a
credential regardless of route. Only raw comparison data crosses the job boundary, never a decision
— `lydite review compare --write-surfaces <path>` writes the document, and a credentialed job reads
it back through `reviewdecision.Surfaces` → `reconcileSurfaces`, which checks shape (base commit,
which opted-in components are present) but not content.

**Open gap.** A per-component clean result *is* the api-surface verdict for that component; a
process a malicious build script leaves running past its own subprocess call can overwrite the
output artifact with a well-formed all-clean document before upload. Closing it needs a sandbox that
cannot write the output path after the comparison exits, or treating every document-supplied clean
result for an untrusted-build component as unverifiable. See `agentic/rules/give-untrusted-build-scripts-no-inherited-environment.md`
and [[guardcredential-blocks-full-decide-parity-outside-a-two-job-split]].

**Two workflow traps found alongside it**, still true of any job of this shape: `uses:
./.github/actions/<name>` resolves from whatever the job's checkout holds (Actions does not
evaluate `${{ }}` in `uses:`, so a base-commit action reference has to be a checked-out path, not
`owner/repo/path@${{ sha }}`); and a pinned action does not pin the binary it runs — a `lydite`
built from the pull request's own tree can have its `publish` rewritten to always succeed, so a
credentialed job must build `lydite` from the base checkout, never reuse a PR-built artifact.
</content>
