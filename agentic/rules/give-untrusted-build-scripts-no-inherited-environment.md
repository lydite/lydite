# Give a subprocess that builds or runs the scanned tree's own code no inherited environment

`executil`'s ordinary `Run*` functions layer a declared or toolchain environment onto this
process's own, because the tools they invoke are lydite's own, running where the scanned
repository does not get to execute anything. A subprocess that instead builds and executes code
the tree under scan controls — a Rust crate's `build.rs`, a proc-macro, anything
`cargo-semver-checks` compiles to derive rustdoc JSON — must use `executil.RunQuietIsolatedEnv`
instead, whose child environment is exactly the slice passed in, never this process's own. `review`
runs with credentials a `scan` job may not have (the CI `referral` job's `GITHUB_TOKEN`), so
inheriting there hands an untrusted build script a secret it has no reason to reach.

This closes one path and not the whole exposure: `os.Unsetenv` changes only the calling
process's own live copy of its environment, never `/proc/<pid>/environ`, which is a snapshot
taken at exec time — a same-user descendant can still read a credential the calling process
held, however briefly, at its own start. A job that runs untrusted code like this must not
hold the credential at all — see `persist-credentials: false` on the `referral` job's own
checkout in [`.github/workflows/lydite-pr.yml`](../../.github/workflows/lydite-pr.yml), since
`actions/checkout` otherwise embeds the token into `.git/config` regardless of anything this
rule's isolation does. `lydite review --publish` also refuses to run a Rust comparison in its
own process at all when `--surfaces` is absent, for any caller of the single-invocation form,
not only this workflow.

The credential is only half of what a job running untrusted code must not be trusted with.
`referral` writes the raw comparison to an artifact (`lydite review compare
--write-surfaces`) rather than a decision, and `referral-publish` (never re-running the
comparison) checks the document's base and the set of components it names against what it
resolves and reads itself — but a **claimed-clean result's own content is not independently
verified**. A process a malicious build script leaves running past its own subprocess call
could still overwrite the artifact with a well-formed document before the upload step
captures it, naming the right base and every opted-in component with no findings. Splitting
by credential and by artifact ownership narrows this from "any status, described however the
untrusted code likes" to "a clean api-surface result specifically", but does not close it —
see ci.md's `referral`/`referral-publish` paragraph for what remains open.

The credential and the binary are two separate things to keep out of untrusted hands, and
closing one does not close the other: `referral-publish` also builds its own `lydite` from a
checkout of the base commit rather than running `setup`'s artifact, which is built from the
pull request's own tree — a modified `publish` function reporting success regardless of the
verdict it was given is exactly as available to the pull request as a modified `build.rs`.
The same reasoning rules out a `uses: owner/repo/path@${{ expression }}` reference to fetch a
trusted action version at all: Actions does not evaluate expressions in `uses:`, so the only
way to reach the base commit's own copy of a local action is to check it out (as its own,
separately pathed checkout) and reference it from there.

## Applies to

Any new subprocess invocation, in `internal/rustapisurface` or elsewhere, that compiles or
executes source from the merge-base or head tree rather than invoking a pinned lydite-owned
tool against it.

## Example

```go
// wrong: inherits this process's own environment, credentials included
executil.RunQuiet(ctx, dir, "cargo", "semver-checks", ...)

// right: the child gets exactly this slice, nothing this process also holds
executil.RunQuietIsolatedEnv(ctx, dir, []string{"PATH=" + path}, "cargo", "semver-checks", ...)
```

Reasoning: [`agentic/references/referral-and-clearance.md`](../references/referral-and-clearance.md).
