---
about: "source/web's engines.node is >=24 because @vipengele/ts and the ts-core-* packages declare node >=24; lydite provisions the dashboard's Node from that field"
saw:
  - source/web/package.json
  - source/web/pnpm-lock.yaml
  - agentic/references/toolchains.md
---
`@vipengele/react` depends on `@vipengele/ts`, and `@vipengele/ts`, `@vipengele/ts-core-common` and `@vipengele/ts-core-redaction` all declare `engines: {node: '>=24'}` (visible in source/web/pnpm-lock.yaml). lydite's TypeScript toolchain resolution reads `engines.node` from the component's package.json (toolchains.md table, "TypeScript" row), so `source/web/package.json` declares `>=24`, and `lydite test`/`scan` provision Node 24 when the ambient Node is older. pnpm 12 itself does not enforce the project's `engines`: `pnpm install` under Node 22 succeeds, and the floor only bites through lydite's provisioning.
