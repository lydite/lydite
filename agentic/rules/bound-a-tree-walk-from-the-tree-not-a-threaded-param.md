# Bound an upward directory walk by reading the tree, not a threaded parameter

A walk that climbs from a component's directory looking for something above it — a workspace
root, a declaring config — needs an upper bound past which lydite was never asked to look.
That bound has to be read off the tree itself, at the moment of the walk, because `Prepare` for
a mutation worker runs against a copy of the scan root rather than the scan root: a bound
threaded through `Prepare`'s signature from the outer run still names the original tree, and a
walk climbing under that bound would either stop short inside the copy or climb straight out of
it into the repository it was copied from.

## Applies to

`internal/runner.declarationRoot`, `internal/nodedeps.WorkspaceRoot`'s callers, and any future
code that resolves an upward bound for a directory a component's `Prepare` runs against.

## Example

```go
// ✗ correct for the outer run, wrong inside a mutation worker's copy
func installNodeDeps(ctx context.Context, inv Invocation, dir, override string, ...) error {
	return nodedeps.Install(ctx, dir, inv.ScanRoot, override, env.Check, out)
}

// ✓ derived from the tree dir already sits in, so it resolves the copy's own root
func installNodeDeps(ctx context.Context, _ Invocation, dir, override string, ...) error {
	return nodedeps.Install(ctx, dir, declarationRoot(dir), override, env.Check, out)
}
```

Reasoning: [`agentic/references/components.md`](../references/components.md) and
[`agentic/references/mutation.md`](../references/mutation.md).
