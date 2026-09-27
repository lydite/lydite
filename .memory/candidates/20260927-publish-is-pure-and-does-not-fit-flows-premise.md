---
about: publish.go is explicitly pure (no platform, no token, no webhook) while Flow's stated reason to exist is a command that reads a platform live and writes back to it
saw:
  - source/cli/cmd/lydite/publish.go
  - source/cli/cmd/lydite/reports.go
  - agentic/references/architecture.md
  - agentic/references/surface.md
---

Checked while scoping a migration of `publish` onto the Flow architecture piloted by
`clearance` (PR #282 / 4b8e9bc).

`architecture.md`'s opening paragraph states the shape Flow exists for: "a command that answers
a webhook by reading a platform live, deciding something, and writing back to the platform,"
and lists `publish` among the commands that "stay as they are until each is migrated on its own
terms, which is a decision made command by command rather than one this file makes for them" —
i.e. the doc does not itself assume publish needs this shape, only that it's a candidate.

`publish.go`'s own doc comment (`newPublishCmd`, source/cli/cmd/lydite/publish.go:39-45) states
the opposite of the Flow premise as a design point: "It is pure: no network, no token, and
nothing about a hosting platform. What it emits is markdown on stdout or in a file, and posting
that is a separate step with a separate identity." Confirmed by control flow: `buildComment`
reads `--reports <dir>` directories off local disk via `readDocuments`/`readDocument`
(source/cli/cmd/lydite/reports.go:159-200, plain `os.ReadDir`/`os.Open`, no forge, no trust, no
credential), folds `ui.Document`s into `ui.Comment` sections (grouping/rendering logic entirely
in publish.go: `buildComment`, `section.render`, `worst`, `counts`, `verdictOf`, `headline`),
and `writeComment` writes to stdout or a local file (`os.WriteFile`) — never posts anything
itself. Posting the resulting markdown is a separate step/identity (surface.md, around line 16
and 307: "`lydite publish` stays pure").

So publish is not "read the platform live, decide, write back" — it is "read N local JSON
documents, decide nothing (no gate), render markdown, write it locally." A Flow built for it
would have no `init-trust`/`init-scm` stages and no `forge.SCMRepository` at all; its only
candidate "stages" are `readDocuments` (per --reports dir) and `buildComment`/`writeComment`,
which do not need trust, credentials, or live platform reads the way clearance's stages do.
Migrating it onto Flow buys the four-layer separation and reflection-checked wiring, but not
the trust/SCM-ordering concerns ADR 0061 and the "payload only points" section of
architecture.md are about — those don't apply here since there is no payload and no platform
call in this command at all.
