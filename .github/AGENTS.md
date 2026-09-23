# `.github` — the workflows and the composite actions

`ci-orchestration.yml`, `lydite-clearance.yml`, `gt-sync.yml` and `dependabot-auto-merge.yml`
are **generated** from `.gt-repo.yaml`; edit that file and run `gt repo sync` — a hand edit to
any of these four is erased by the next sync. The `ci-*` stage workflows
(`ci-preflight.yml`, `ci-build.yml`, `ci-test.yml`, `ci-end2end.yml`) are scaffolded once by
gt and then hand-maintained: gt creates each file the first time a stage is declared and never
touches it again, so this repository owns their content the same way it owns
`dependabot-pins.yml`, `release.yml` and everything under `actions/`. The lydite stage — the
referral, the scan, the gated suites and the standing comment — is `ci-orchestration.yml`'s
`lydite` job, calling `lydite/actions`' reusable workflow through gt, so this repository runs
the same pipeline every consumer runs (ADR 0051).

Read [`ci.md`](../agentic/references/ci.md) before changing a workflow: which stages exist,
why everything lydite says about a pull request is one run rather than a gt stage, how both
lydite workflows shard, and the failure signatures worth recognising — including the exit-143
shape that means the runner went away rather than the job failing.

Read [`actions.md`](../agentic/references/actions.md) before changing anything under
`actions/`: the same shape has to land in `lydite/actions`, and nothing enforces it.

Five rules bind hardest here, and all are in [`agentic/rules/`](../agentic/rules/): never
interpolate an expression into a `run:` body, pin the exact Go patch version, sort a fallback
transport's response into three buckets rather than treating any non-`200` as one
undifferentiated fallback (`lydite-comment` and `lydite-threads`) — for any job invoking
`lydite-reports` from a checkout that isn't the job's own working directory — point its `path`
input at that checkout rather than the composite's `.`-relative default, and never name an
uploaded artifact — a raw `actions/upload-artifact` step or an unoverridden `lydite-reports`
call — inside `publish`'s `lydite-reports-*` glob.
