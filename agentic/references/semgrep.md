# Semgrep: token-bearing vs token-less runs

> **The reference for `internal/semgrep`** — and for what `--diff-base` exists to fix.

`internal/semgrep.Check` picks its subcommand from whether `SEMGREP_APP_TOKEN` is set: `semgrep ci`
(diff-aware, applies the org's platform policy, uploads to the AppSec Platform) when it is, plain
`semgrep scan --config <ruleset> --error` when it isn't. Those two modes disagree about **scope**,
and that disagreement was a standing CI defect: GitHub deliberately withholds repo secrets from
`dependabot[bot]` events, so every Dependabot PR arrived with an empty token, silently fell back to
a *whole-repo* scan, and blocked on the consuming repo's pre-existing findings — findings no
token-bearing run had ever reported, in code the PR never touched. Whether a PR was green depended
on who opened it.

`lydite scan --diff-base <ref>` closes that gap: in scan mode it passes Semgrep
`--baseline-commit`, so the fallback blocks on the same thing `semgrep ci` would — what the change
introduces — and nothing else. `--diff-base auto` resolves the merge-base with `origin/main` via the
same `internal/gitstate.BaseSHA` the coverage gate already uses, so a PR's scan and its coverage
agree on what "this change" means. `lydite/actions` passes `auto` on every `pull_request`
event, and nothing on a push.

Two deliberate choices in `cmd/lydite/scan.go`'s `resolveDiffBase`:

- **A token short-circuits it entirely** — `semgrep ci` already scopes itself to the diff, so
  resolving a merge-base would cost a `git fetch` nothing reads, and would newly demand a
  full-history checkout from token-bearing consumers that don't need one today.
- **An unresolvable `auto` is an error, not a silent full scan.** Falling back would reintroduce
  the exact surprise the flag exists to remove: a scan that quietly widens its own scope. A shallow
  checkout is a fixable misconfiguration (`fetch-depth: 0`), so lydite says so and fails.

Default (`--diff-base` empty) is still a full-repo scan — that's what a local `lydite scan` wants,
and it's what a push to `main` gets.

## A `.semgrepignore` replaces the defaults, and the scan says so

Semgrep looks for a `.semgrepignore` at the root of the scan, and a file that exists **replaces**
its built-in default ignore list rather than extending it. So a repository that writes one line to
skip a directory also stops ignoring everything the template held back — `vendor/`, `dist/`,
`node_modules/`, `build/`, `test/`, `tests/`, `*_test.go` and the rest — and the findings count
moves with no rule change. In this repository a single-line `.semgrepignore` took the scanned file
count from 82 to 124 and surfaced two pre-existing findings in a test file the change never touched.

`internal/semgrep.Check` takes a writer and, before Semgrep runs, writes one warning line naming the
file and every default pattern that consequently no longer applies. `cmd/lydite/scan.go` passes
`cmd.ErrOrStderr()`, the same stream `warnUnscanned` uses: stdout carries the report, and under
`--json` a sentence there would make the document unparseable.

It is informational and never gates. A repository's own configuration of its own scan is legitimate
influence — ADR 0020 says so of a declared `GOFLAGS` and a nested `biome.json` alike — so lydite
neither merges the file with the defaults nor overrides it. Naming what is lost is the whole of the
remedy; a consumer who wants the narrower scope has done nothing wrong.

Semgrep publishes no command that prints its default list. `defaultIgnorePatterns` is the block
following the marker string `default semgrepignore patterns` in the `semgrep-core` binary inside the
pinned pip package, read with `strings`. Bumping the pin in
`internal/semgrep/requirements.txt` means re-verifying that list the same way: a pattern that has
quietly left Semgrep's template is one the warning names as dropped when it is not.

Restoring `semgrep ci` on Dependabot PRs (for the platform dashboard's sake) is a *consumer-side*
option, not a lydite one: the token has to be added to the repo's separate **Dependabot secrets**
store (`gh secret set SEMGREP_APP_TOKEN --app dependabot`), since Actions secrets are not visible to
Dependabot events. It is not required for CI to be green — the diff-aware fallback above is — and it
does hand an upload token to a workflow that executes the bumped dependency's code, so it's a
per-repo judgment call.

