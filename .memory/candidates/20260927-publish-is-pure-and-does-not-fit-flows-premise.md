---
about: publish runs as a flow but is pure — no platform, token, trust or init stages — so its flow shares none of the ordering concerns clearance's does
saw:
  - source/cli/cmd/lydite/publish.go
  - source/cli/cmd/lydite/reports.go
  - source/cli/internal/flows/publish/publish.go
  - source/cli/internal/stages/publish/gather.go
  - source/cli/internal/stages/publish/build.go
  - source/cli/internal/stages/publish/write.go
  - docs/adr/0074-publish-runs-as-a-flow-of-ordinary-stages.md
  - agentic/references/architecture.md
---

`architecture.md`'s opening paragraph states the shape Flow exists for: "a command that answers
a webhook by reading a platform live, deciding something, and writing back to the platform."
`publish` is not that shape, and runs as a flow anyway (ADR 0074), for structural consistency.

`newPublishCmd`'s doc comment (`source/cli/cmd/lydite/publish.go:20`) states the property:
"It is pure: no network, no token, and nothing about a hosting platform." Its flow
(`publishflow.New`, `source/cli/internal/flows/publish/publish.go`) is three stages —
`gather-reports` → `build-comment` → `write-comment` — with no `init-trust`/`init-scm` stage, no
`forge.SCMRepository`, and no `When`/`Unless`. `GatherReports` (`stages/publish/gather.go:56`)
reads local report directories through an injected `ReadDocuments`, which the CLI fills with
`reports.go`'s `readDocuments` (plain `os.ReadDir`/`os.Open`); `BuildComment`
(`stages/publish/build.go:74`) reads failing rows' logs through an injected `ReadLog`;
`WriteComment` (`stages/publish/write.go:27`) writes to stdout or a local file and posts nothing.

So the trust/SCM-ordering concerns ADR 0061 and architecture.md's "payload only points" section
describe do not apply to `publish`: there is no payload and no platform call. What the flow buys
it is the four-layer separation and `flow.Build`'s reflection-checked wiring, nothing more — a
change that adds a platform call to `publish` is adding the first such concern to it, not joining
one already handled.
