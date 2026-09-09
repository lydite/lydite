# Pin the exact Go patch version in workflows

`go-version: "1.26.6"`, never a bare `"1.26"`. `actions/setup-go` resolves a bare minor to
whatever patch it has, and `go install` of an external tool does not consult the current
module's `toolchain` directive — which is how a govulncheck built by an older Go once passed
locally and failed in CI. When `go.mod`'s `toolchain` line moves, every `go-version-file:`
reference and every literal `go-version:` moves in the same change.

Reasoning: [`.agents/references/ci.md`](../references/ci.md) and
[`.agents/references/toolchains.md`](../references/toolchains.md).
