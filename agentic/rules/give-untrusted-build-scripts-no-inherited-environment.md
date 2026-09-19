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
hold the credential at all: see the `referral` / `referral-publish` split in
[`.github/workflows/lydite-pr.yml`](../../.github/workflows/lydite-pr.yml), and
`persist-credentials: false` on that job's own checkout, since `actions/checkout` otherwise
embeds the token into `.git/config` regardless of anything this rule's isolation does.

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
