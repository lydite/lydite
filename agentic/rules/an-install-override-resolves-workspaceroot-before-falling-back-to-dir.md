# An install override still resolves `WorkspaceRoot` first, and only falls back to `dir` when no root resolves

`typescript.install` replaces *what* runs, never *where*. `nodedeps.Install` must attempt
`WorkspaceRoot` for the override exactly as it does for a detected install, keying the
override on the resolved root so every sibling component resolving that root coalesces into
the same single run — sharing the same `lockRoot` and `installedUnder` guard a detected
install shares. Skipping that resolution and keying the override on the component's own `dir`
instead makes every component carrying the override its own key, so each runs the same frozen
install concurrently over one shared `node_modules`, lockfile and store — the race
[ADR 0055](../../docs/adr/0055-an-install-step-is-a-setup-command-and-the-override-still-coalesces.md)
diagnoses. `dir` is the fallback only for the cases `WorkspaceRoot` itself refuses to resolve:
an ambiguous multi-lockfile root, or no lockfile at all.

## Applies to

`source/cli/internal/nodedeps.Install`, and any future code path that decides where an
overridden install runs.

## Example

```go
// ✗ an override skips WorkspaceRoot and keys on the component's own directory,
//    so every sibling carrying the override installs the same root on its own
if override != "" {
    root = dir
} else if root, ok = WorkspaceRoot(dir, scanRoot); !ok {
    return nil
}

// ✓ an override still resolves WorkspaceRoot first, and coalesces there like
//    every detected install does; dir is only the fallback when no root resolves
if override != "" {
    if root, ok = WorkspaceRoot(dir, scanRoot); !ok {
        root = dir
    }
} else if root, ok = WorkspaceRoot(dir, scanRoot); !ok {
    return nil
}
```

Reasoning: [`agentic/references/components.md`](../references/components.md).
