# Escape a real path before writing it as a `pathmatch` pattern

`.lydite/exemptions.yml`'s `paths:` entries are `internal/pathmatch` patterns, and a real
filename is not one. A changed file whose name carries `*`, `?`, `[` or `]` becomes a pattern
the moment it is copied verbatim: `app/[slug]/page.tsx` — an ordinary Next.js route — turns
into a character class covering `app/s/page.tsx` and missing the file it names, and a file git
permits to be named `**` turns into the pattern covering every path in the repository. Escape
every one of those characters, and the backslash itself, with a leading backslash before
encoding — `path.Match`, which `pathmatch.Match` calls per segment, reads `\` as escaping the
rune after it, so a `**` segment escapes to `\*\*` and stays two literal stars rather than the
many-segments wildcard.

## Applies to

Any code that turns a filename or path lydite did not choose (a changed file from
`forge.Client.ChangedPaths`, a path read off a diff, anything not already hand-written into a
`paths:` list by a person) into a `pathmatch` pattern. `cmd/lydite/clearance.go`'s `escapeGlob`
is the one implementation today, called from `proposalYAML` before a `/lydite exempt` draft is
encoded.

## Example

```go
// wrong: a filename stands in for a pattern
patterns[i] = path // app/[slug]/page.tsx matches the wrong file

// right: escape what pathmatch would otherwise read as syntax
patterns[i] = escapeGlob(path)
```

Reasoning: [`docs/adr/0049-exempt-proposes-an-entry-and-lands-nothing.md`](../../docs/adr/0049-exempt-proposes-an-entry-and-lands-nothing.md)
and [`agentic/references/referral-and-clearance.md`](../references/referral-and-clearance.md).
