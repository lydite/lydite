---
about: "a consumer runs lydite test record via gt's reusable-lydite-baseline.yml@v2 -> lydite/actions record, with CLI version resolved as latest; a new file in the lydite-branch commit belongs in gitstate.stageRecording, not in the records closure"
saw:
  - .github/workflows/ci-orchestration.yml
  - docs/adr/0010-repo-topology.md
  - .claude/skills/bump-version/SKILL.md
  - source/cli/internal/gitstate/gitstate.go
  - source/cli/internal/stages/ledger/compose.go
  - source/cli/internal/finding/finding.go
---
Delivery: ci-orchestration.yml:195-209 `lydite-baseline` (push only) calls
`pedromvgomes/gt/.github/workflows/reusable-lydite-baseline.yml@v2`, which (not visible from this repo) uses lydite/actions `record`.
The CLI version is not pinned: ADR 0010 "The action installs latest" (version input defaults to `latest`); bump-version SKILL says a release reaches every consumer on next CI run.
A new CLI behaviour in `record` therefore needs only a lydite release (tag vX.Y.Z -> release.yml -> goreleaser), unless new inputs/artifacts are needed from lydite/actions or the gt workflow. No dependabot/pinsync of lydite/actions in this repo (pinsync covers tool pins only, tool-pins.md).

Seam: gitstate.stageRecording (gitstate.go:892) stages Snapshot documents, then calls the records closure and ledger.Append; all in one commit via pushState. ComposeRecords (stages/ledger/compose.go) only yields []ledger.Record, so an extra non-record file needs a gitstate.Write/Snapshot (or Records) extension, not just compose.go. Open set known at compose.go:98 (ledger.BranchState); finding.Finding (finding.go:75) has Message/Severity/Detail in `in.Found`, but ledger.FindingEvent (ledger.go:228) keeps only transition/fingerprint/gate/component/path/rule.
Unverified: gt workflow contents (external repo); stale claim in actions.md:22-27 about lydite-pr.yml still exists (see existing gotcha note).
