# Probe trees for the TypeScript licence source

Every file here is committed with a `.txt` suffix and materialised through `internal/fixture`;
see that package for why the suffix exists. A `package.json` committed under its own name would
be a workspace root lydite's own component walk finds, and a `package-lock.json` beside it an
install it would then be asked to run.

## TypeScript

`npmprobe` is an npm workspace whose `package-lock.json` (schema version 3, the only version
read) covers every case the lockfile source has to get right. Its entries are the real ones
`source/cloud-services/package-lock.json` resolved, at the versions and under the licences
stated there.

| Entry | What it covers |
|---|---|
| `node_modules/typescript` | Apache-2.0 as a plain `license` string, declared direct in `devDependencies` on line 14 |
| `node_modules/lightningcss` | MPL-2.0, the weak-copyleft case a permissive allow-list rejects, declared direct in `dependencies` on line 10 |
| `node_modules/wrangler` | `MIT OR Apache-2.0`, the dual-licence expression an allow-list satisfies through either half |
| `node_modules/@babel/helper-string-parser` | MIT, reached only transitively, so its claim locates at no line |
| `node_modules/@img/sharp-libvips-darwin-arm64` | LGPL-3.0-or-later, strong copyleft, scoped and transitive |
| `node_modules/wrangler/node_modules/@img/sharp-libvips-linux-x64` | a nested duplicate, named by the segment after the last `node_modules/` |
| `node_modules/pause-stream` | the old-style `licenses` array of `{type, url}` objects, composing to `Apache-2.0 OR MIT` |
| `node_modules/unlicensed-probe` | neither field, which is `unknown` — no non-workspace entry of this repository's own lockfile states no licence, so this one carries the shape |
| `node_modules/@lydite/pr-relay` | the `link: true` alias of a workspace member, skipped |
| `pr-relay` | that member's own source entry, which states no licence because it is the repository's own code, skipped |

`yarnprobe` is the other half of the limit ADR 0042 states: a `yarn.lock` naming a dependency's
resolution and no licence anywhere in it, and no `node_modules` to read one out of.
