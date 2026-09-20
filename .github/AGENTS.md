# `.github` — the workflows and the composite actions

`ci-orchestration.yml` and the `ci-*` stage workflows are **generated** from `.gt-repo.yaml`;
edit that file and run `gt repo sync`. `lydite-pr.yml`, `lydite-baseline.yml`,
`lydite-clearance.yml`, `dependabot-pins.yml`, `release.yml` and everything under
`actions/` are hand-written.

Read [`ci.md`](../agentic/references/ci.md) before changing a workflow: which stages exist,
why everything lydite says about a pull request is one run rather than a gt stage, how both
lydite workflows shard, and the failure signatures worth recognising — including the exit-143
shape that means the runner went away rather than the job failing.

Read [`actions.md`](../agentic/references/actions.md) before changing anything under
`actions/`: the same shape has to land in `lydite/actions`, and nothing enforces it.

Four rules bind hardest here, and all are in [`agentic/rules/`](../agentic/rules/): never
interpolate an expression into a `run:` body, pin the exact Go patch version, sort a fallback
transport's response into three buckets rather than treating any non-`200` as one
undifferentiated fallback (`lydite-comment` and `lydite-threads`), and — for any job invoking
`lydite-reports` from a checkout that isn't the job's own working directory — point its `path`
input at that checkout, not the composite's `.`-relative default.
