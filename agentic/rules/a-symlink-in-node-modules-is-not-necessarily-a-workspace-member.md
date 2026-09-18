# A symlink in `node_modules` is not necessarily a workspace member

Yarn and pnpm both link a workspace member back into the repository, the way npm marks one
`link: true` in its lockfile — but pnpm's default layout also symlinks every ordinary registry
package into its own `.pnpm` store, so a check that treats every symlink as local reads almost
nothing out of a real pnpm install and passes a component nothing measured, the fail-open
[ADR 0038](../../docs/adr/0038-a-licence-policy-gates-the-licences-a-change-introduces.md)
forbids. Tell the two apart by resolving where the link actually points: inside `node_modules`
(itself resolved first, so a symlinked ancestor like darwin's `/var` can't read every entry as
escaping it) is an installed package; outside it and back into the repository is the workspace
member to skip.

## Applies to

`internal/typescript/licence.go`'s `installedDependencies`/`workspaceLocal`, and any future code
that classifies a `node_modules` entry as local versus installed.

Reasoning: [`agentic/references/scanning.md`](../references/scanning.md).
