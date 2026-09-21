# Thread the real root down; read it off the tree only where there is no real root to thread

A walk that climbs from a component's directory looking for something above it — a workspace
root, a declaring config — needs an upper bound past which lydite was never asked to look. The
caller's own scan root is that bound, and passing it down is correct: a directory between a
component and the true scan root can hold a `.lydite` of its own — a vendored subtree that is
itself a lydite target — and deriving the bound by walking up to the *nearest* such directory
stops there instead of at the real root, silently narrowing what the walk is allowed to find.

The one caller with no real root to thread is `Prepare` for a mutation worker: its `dir` sits
inside a *copy* of the scan root, not the scan root itself, so the outer run's own root would
bound the walk against a tree the worker's `dir` is not under — stopping short inside the copy,
or climbing straight out of it into the repository it was copied from. The copy carries its own
`.lydite`, because it is a copy of everything git tracks, so reading the bound off the tree the
worker's `dir` actually sits in is what resolves the copy's own root correctly there — and only
there. An empty root is the signal a caller passes to ask for that fallback.

## Applies to

`internal/runner.installNodeDeps` and `declarationRoot`, `internal/nodedeps.WorkspaceRoot`'s
callers, and any future code that resolves an upward bound for a directory a component's
`Prepare` runs against.

## Example

```go
// ✗ always tree-derived: a nested .lydite between dir and the real scan root
// stops the walk there, missing the workspace lockfile above it
func installNodeDeps(ctx context.Context, _ Invocation, dir, override string, ...) error {
	return nodedeps.Install(ctx, dir, declarationRoot(dir), override, env.Check, out)
}

// ✓ the caller's real root, threaded down; tree-derived only when there is none to give
func installNodeDeps(ctx context.Context, _ Invocation, dir, root, override string, ...) error {
	if root == "" {
		root = declarationRoot(dir)
	}
	return nodedeps.Install(ctx, dir, root, override, env.Check, out)
}
```

Reasoning: [`agentic/references/components.md`](../references/components.md) and
[`agentic/references/mutation.md`](../references/mutation.md).
