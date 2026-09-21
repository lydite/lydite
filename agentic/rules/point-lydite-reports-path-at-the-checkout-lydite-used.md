# Point `lydite-reports`'s `path` input at the checkout `lydite` actually used

The composite's `path` input defaults to `.`, the job's own working directory — right only when
that job ran lydite with `--dir .` against a checkout at its own root. A job that instead invokes
lydite against a checkout held at a subdirectory (`--dir head`, a base-commit checkout at `base/`)
writes `.lydite-reports` under that subdirectory, not the job's own working directory. The
composite's existence check (`[ -d "$REPORTS" ]`) then finds nothing, uploads nothing, and the
concern is silently absent from the published comment — not rendered as unmeasured, simply never
there, because a directory that was never uploaded is a directory `lydite publish` was never told
to look for. lydite/lydite#194 shipped exactly this and went unnoticed across four merged pull
requests, because a missing artifact and a step that legitimately produced nothing look identical
from the uploading job's own log.

## Applies to

Any new or changed `.github/actions/lydite-reports` invocation (or `lydite/actions`'s mirror)
in a job whose `lydite` command runs against a checkout that is not the job's own working
directory.

## Example

```yaml
# wrong: lydite wrote head/.lydite-reports, but the default path is "."
- uses: ./.github/actions/lydite-reports
  with:
    name: referral

# right: name the checkout lydite actually ran against
- uses: ./.github/actions/lydite-reports
  with:
    name: referral
    path: head
```

Reasoning: [`agentic/references/ci.md`](../references/ci.md).
