# Business logic runs as a Flow, and the CLI keeps its own concerns

[#280](https://github.com/lydite/lydite/issues/280) asks, per command, whether Flow earns its
keep, rather than treating adoption as a schedule every command eventually reaches.
`architecture.md`'s own scope sentence states a narrower test than the question the issue asks:
it names "a command that answers a webhook by reading a platform live, deciding something, and
writing back to the platform" as the shape Flow exists for. `queue`, `publish` and `release check`
do none of that — no webhook, no platform read for `publish`, no writing credential for `queue` —
and each belongs on the scaffold nonetheless, for reasons ADR 0074 and ADR 0075 state and this
record does not repeat. The scope sentence answers the wrong question.

## Decision

**Business logic runs as a Flow. The CLI layer holds only the CLI's own concerns.**

Business logic is what lydite does to, or concludes about, a repository: reading its state,
deciding a verdict, writing a result back to it or to disk. The CLI's own concerns are the tool
managing itself — telling a caller its version, replacing its own binary, nudging a caller that
a newer one exists. Nothing about the second kind touches a repository or a verdict, and nothing
about it needs the layering Flow buys: a typed binding checked at build time, a stage callable
from a struct literal with no fake pipeline stood up around it, an engine that can be handed to a
future caller other than `cobra`.

This settles the platform-reading premise `architecture.md`'s scope sentence states as the test:
it is one example of business logic, not the definition of it. `publish` reads report documents
rather than a platform (ADR 0074); `queue` writes through a relay while holding no credential
(ADR 0075); `release check` reads only git tags and commit messages off a local checkout — no
platform, no credential, no webhook — and is exactly business logic by the definition above: it
concludes a verdict about a repository's own tags and refuses one of them. The principle names
why all three belong on the same scaffold as `clearance`, without needing a fourth ADR to state
the same reasoning again for the next command shaped like none of the first three.

## The verdicts, per command

| Command | Verdict |
|---|---|
| `scan` | runs as a flow |
| `test` | runs as a flow |
| `test record` | runs as a flow |
| `test plan` | runs as a flow |
| `test merge` | runs as a flow |
| `mutation` | runs as a flow |
| `mutation merge` | runs as a flow |
| `review` | runs as a flow |
| `review compare` | runs as a flow |
| `publish` | runs as a flow |
| `threads` | runs as a flow |
| `clearance` | runs as a flow |
| `clearance queue` | runs as a flow |
| `release check` | runs as a flow |
| `version` | stays — the CLI's own concern |
| `update` | stays — the CLI's own concern |
| the update nudge (`maybeNudgeUpdate`) | stays — the CLI's own concern |

`release check` reads the tag being released, resolves the release before it, reads the commits
in the range, and judges whether a declared break lands on a bump that admits one — a verdict
about the repository's own tags, computed the same way any other gate's is. `internal/stages/release`
(package `releasestages`) holds `ResolveTag`, `PreviousTag`, `ReadCommits` and `Judge`;
`internal/flows/release` (package `releaseflow`) wires them in that order. See
`architecture.md`'s "The release flow" section for the stage order, the `Unless` condition a
first release's empty range needs, and how the CLI picks a row from the flow's result.

`ResolveTag` answers `ErrNoTag` — a sentinel, not a worded message — when neither the caller, the
ref a run was triggered by, nor the checkout itself names a tag. No stage names an environment
variable or a CLI flag: `ErrNoTag` is a sentinel because the only way to say where a tag could
come from names `--tag` and `GITHUB_REF_NAME`, which belong to the CLI, not the stage. A stage's
error about the repository's own state — `PreviousTag`'s shallow-checkout error, `ReadCommits`'
unreadable-range error — stays the stage's own text, since neither names a flag or an environment
variable to reach for.

## Alternatives rejected

**Leave `architecture.md`'s scope sentence as the test.** The sentence describes one command's
shape accurately and states nothing that admits any other: `publish`, `queue`, and `release
check` fail the test it states, each for its own documented reason, while all three belong on
the same scaffold. Keeping the sentence and treating each exception as its own ADR's problem to
explain away would leave the actual rule unwritten, spread across however many ADRs happen to
have needed to say it.

**Move every command, including `version` and `update`.** These are not business logic under the
definition above: they tell a caller which binary it is running, or replace it, and neither reads
nor concludes anything about a repository. Putting them on a scaffold built for typed bindings
between stages that read and write a repository's own state would wire nothing meaningful — there
is nothing for a later stage to read off an earlier one's output, because there is no repository
in the loop at all. The nudge these two support (`maybeNudgeUpdate`) is the same case: it checks
lydite's own version against a release feed, which is the tool managing itself, not a comment on
the repository it is running against.

## Consequences

- A new command's business logic starts as a flow; the CLI's own concerns (a self-check, a
  self-update, a version string) start, and stay, in `cmd/lydite`.
- `test plan` and `test merge` are pure folds over documents already on disk, sharing `fold.go`'s
  conflict predicate with `mutation merge`: both run as a flow for the same reason `mutation
  merge` does, over `teststages`' own `LoadPlanComponents`/`GroupShards` and
  `LoadMergeComponents`/`ReadShardMeasurements` — the fold's own reasoning did not change, only
  its layering. See `architecture.md`'s "Test" section for both flows' stages.
- `architecture.md`'s "The release flow" section describes `release check`'s four stages among
  the four layers.

See [`agentic/references/architecture.md`](../../agentic/references/architecture.md) for the
layering this decision governs, [ADR 0059](0059-a-flow-is-a-hand-rolled-engine-of-typed-bindings-not-a-pipeline-library-or-a-shared-context.md)
for the engine's own scope, and [ADR 0074](0074-publish-runs-as-a-flow-of-ordinary-stages.md) and
[ADR 0075](0075-the-merge-queue-submission-is-a-flow-over-a-relay-client.md) for the two commands
whose own reasoning states this record's principle in full for the shape each of them is.
