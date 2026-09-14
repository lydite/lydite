# Linters: Biome, and only Biome

> **The reference for `internal/typescript`** — Biome, and why it is the only TypeScript linter.

Biome backs the TypeScript check. `typescript.linter` accepts `biome` and nothing else; the
retired `eslint` value is **rejected** by `config.validateLinter` with an error naming the
removal, rather than accepted and quietly run under Biome. A repo that set that key stated
which rule set it gates on, and switching it silently would change what the scan measures
while every run still reported a pass — the same reason an unknown value is rejected rather
than defaulted. `LinterESLint` stays defined for no purpose but to be recognised and refused.

**Biome is not a replacement for `eslint-plugin-security` — Semgrep is, and it already ran.**
Biome's `security` group is six JSX/eval/secret rules (`noBlankTarget`,
`noDangerouslySetInnerHtml`, `noDangerouslySetInnerHtmlWithChildren`, `noGlobalEval`,
`noScriptUrl`, `noSecrets`), of which only `noGlobalEval` coincided with anything the plugin
had; lydite additionally gates on the **`correctness`** group, so the TypeScript check is not
"security findings only" the way the others are. Measured against a fixture carrying all four
classes, Semgrep at `config: auto` covers `detect-child-process` (with taint — it names the
tainted argument) and `detect-non-literal-fs-filename` (as a path-traversal finding), partly
covers `detect-unsafe-regex` (dynamic patterns via `detect-non-literal-regexp`, but no static
ReDoS analysis of a literal), and does not cover `detect-object-injection` at all. Several of
those Semgrep rules are ports of the ESLint ones and are stronger than the originals. The real
gap is `detect-object-injection` — the plugin's most commonly disabled rule — plus
literal-pattern ReDoS. Do not rebuild either as a Biome GritQL plugin: GritQL matches syntax
against the CST with no dataflow or taint, so it could only produce a weaker copy of what
Semgrep already does. See [ADR 0008](../../docs/adr/0008-biome-as-the-only-typescript-linter.md),
and [ADR 0005](../../docs/adr/0005-optional-biome-linter.md) for the opt-in it supersedes.

**Why it went.** ESLint's TypeScript support is a *compiler* dependency, not a linter one:
`@typescript-eslint/parser` declares `typescript` as a peer with an
upper bound (`>=4.8.4 <6.1.0`), so lydite's own pin manifest had to carry a `typescript`
inside that window, and the window moves only when upstream ships support for a new compiler.
`internal/typescript`'s package doc had recorded exactly this hazard — and a Dependabot PR
bumped `typescript` across the ceiling anyway and merged, after which `npm ci` on the
committed lockfile failed with `ERESOLVE` and every consumer with TypeScript got an install
error instead of a lint result. CI stayed green throughout, because at the time lydite's own
repository had no TypeScript to scan: its only `package.json` files were the pin manifests, which
are not components, so the self-scan could not reach that code path. `source/cloud-services/`
now can, which is why that workspace declares no `@cloudflare/workers-types` — wrangler puts a
peer range on it and the failure class would be identical. Biome parses TypeScript with its
own Rust parser and depends on no compiler package, so `biome-pin` has no peer range to cross
and the failure class does not exist for it.

Four things about the Biome integration were established against Biome directly rather than
from its docs. The pin is now **2.5.10**, and `biome.json`'s `$schema` URL must be bumped with
it — `TestBiomePinMatchesConfigSchema` fails the build otherwise, the same mirror guard
`internal/golang/pins_test.go` provides for the Go constants. Re-verified on 2.5.10 where
noted:

- **`files.includes` negations work from an out-of-tree config** (re-verified on 2.5.10: an
  `eval` inside `dist/` is not reported). Biome resolves config globs
  "relative to the folder the configuration file is in", and lydite stages `biome.json` in its
  cache directory — so this looked like it would silently ignore nothing. It doesn't: `**`-prefixed
  negations are depth-agnostic and match correctly. But they are doing real work — with them
  removed, Biome lints `dist/` and reports findings inside a minified production bundle.
  `TestBiomeConfigIgnoresMatchesDefaultSkipDirs` guards it.
- **`--config-path` beats the scanned project's own root `biome.json`** (re-verified on 2.5.10).
  A project config setting
  `linter.enabled: false` is ignored, so lydite's verdict doesn't vary with what the target repo
  declares — the same stance `internal/typescript`'s doc comment takes generally.
- **A `biome.json` in a *subdirectory* does NOT abort lydite's run**, and the earlier claim that
  it does was a mis-scoped reproduction. Biome errors with "Found a nested root configuration, but
  there's already a root configuration" **only when it resolves configuration from the tree
  itself**. lydite always passes `--config-path`, and under that flag a nested `biome.json` —
  with `"root": true`, or with no `root` key at all — is simply ignored: Biome exits 0, lints
  normally, and the report is clean. Confirmed on 2.5.8 and 2.5.10 with lydite's exact
  invocation, so this was never version drift.
  The consequence is that `lintDirBiome`'s `biomeNestedRootConfig` branch is unreachable as long
  as `--config-path` is passed. It stays, because it stops being unreachable the moment anyone
  drops that flag — and dropping it is a plausible change, since it is what would let a project's
  own Biome config be honoured. What must not survive is the claim that it fires today.
- **A nested config declaring `"root": false` is *merged* into lydite's config.** Its rules then
  fire in our run. `reportableBiome`'s category allowlist contains that — merged `lint/style/*`
  is dropped. The same merge is a real limitation in the other direction that cannot be closed
  from here: a nested config could set `"security": "off"` and silently narrow what lydite
  checks.

**A check whose findings never stream must set `executil.Result.Detail`.** `executil.Run`
streams every tool's stdout/stderr live, so for gosec, clippy, cargo-audit and Semgrep the
findings are on the terminal and in the job log before anyone reads the
`Result`. Biome is the exception: its report goes to a file via `--reporter-file` so its own
chatter cannot corrupt the JSON, so nothing streams and `Output` holds no findings.
`cmd/lydite/scan.go`'s `report` prints `Detail` under a failing check — without it a
developer sees a bare `✗ biome(.)` row and has to re-run the pinned toolchain by hand to learn why,
and the PR comment carries nothing at all. Detail lines are **indented**, and that is
load-bearing rather than cosmetic: a finding quotes source, which can contain anything the
source contains — including something shaped like a verdict — and indentation is what stops
it from beginning a line the way a status row does.

`recommended: false` in the bundled `biome.json` is not tidiness — without it Biome's default
preset enables `style` and `suspicious` too, and lydite would start failing PRs over opinions it
never agreed to enforce. The formatter and assist are explicitly disabled for the same reason:
lydite is not a formatter and must never report a formatting diff as a finding.

