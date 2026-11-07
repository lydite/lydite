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

Seam: gitstate.stageRecording (gitstate.go:919) stages Snapshot documents, then calls the records closure (`gitstate.Records func(worktree) (Recording, error)`, gitstate.go:737) and ledger.Append, then stages the open-findings snapshot parts (stageOpenFindings) only when Append landed a record; all in one commit via pushState. A further file in that commit is a new field on `gitstate.Recording` (gitstate.go:746), built in ComposeRecords' per-attempt closure (stages/ledger/compose.go:95) and staged in stageRecording after Append. `Write` returns `gitstate.Written{Landed, OpenFindingsParts}`; WriteStateOut carries the count to cmd/lydite/record.go's report.
Unverified: gt workflow contents (external repo); stale claim in actions.md:22-27 about lydite-pr.yml still exists (see existing gotcha note).
