# An exemption's `paths` list only the manifests `depdelta.Detect` actually reads

`versionsPatchAndMinor` and the added-dependency disqualifier both read a path's comparison from
`internal/reviewdecision`'s `measureDependencies`'s `[]ManifestDelta`, which `depdelta.Detect` builds by sniffing the diff for
paths it recognizes as a manifest — `go.mod`/`go.sum`, `Cargo.lock`, `package-lock.json` — never a
component's declared lockfile and never the manifest a lockfile sits beside. `Detect` does not
recognize `Cargo.toml` or `package.json` as manifests at all, so a path it does not name is not
reported as unmeasured — it is skipped outright. Listing a manifest like that in a `paths` entry
would let an edit to it alone clear the exemption with the lockfile beside it untouched and never
compared.

## Applies to

`.lydite/exemptions.yml`'s `dependency-version-bump` entry (or any future entry with a
`versions:` condition), and `internal/depdelta.Detect`.

## Example

```yaml
# wrong: Cargo.toml isn't a path Detect recognizes, so an edit to it alone
# clears unmeasured, with the Cargo.lock beside it never compared
paths:
  - source/cli/internal/rust/cargo-audit-pin/Cargo.toml

# right: name the lockfile Detect actually reads a comparison from
paths:
  - source/cli/internal/rust/cargo-audit-pin/Cargo.lock
```

Reasoning: [`agentic/references/referral-and-clearance.md`](../references/referral-and-clearance.md#an-added-dependency-refers)
and [ADR 0047](../../docs/adr/0047-an-added-dependency-refers-and-a-version-bump-is-conditionally-exempt.md).
