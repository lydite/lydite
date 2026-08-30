# lydite lives in its own org, as a monorepo plus a thin actions repo

lydite was created inside `wardnet` because wardnet was its only consumer. It is now the quality
gate for unrelated projects, and it is growing a dashboard, so the org it lives in should name the
tool rather than one of its users. It moves to a `lydite` org.

## Two repos, not three

`lydite/lydite` is the monorepo: `cli/` (Go), `web/` (the React dashboard), and later `worker/`
(the OAuth token exchange described in ADR 0009). These ship as one artifact from one tag — the
dashboard is embedded in the binary — so they release together and cannot drift apart.

`lydite/actions` holds the composite action, consumed as `uses: lydite/actions/scan@v1`. It is
split out for two reasons. GitHub checks out the entire repository backing a `uses:` on every
consumer job, and the monorepo now carries a Vite app and its build tree; an actions repo should
be small. And consumers pin `@v1` for the *action*, while the release workflow moves `v1` on every
CLI release — so a patch to a coverage parser churns a ref people pinned for a file that did not
change. The name is `actions`, not `gh`: `gh` reads as the GitHub CLI and does not say what the
repo holds.

A third repo — a `lydite/go` mirror — was considered and rejected. There is no lydite library to
import, so a published module path would buy nothing while costing sync automation and a second
tag namespace for `v1` to go stale in.

## The module path stays non-fetchable

`go.mod` is renamed `lydite/lydite` -> `lydite/lydite` and remains deliberately not a URL.
lydite is a binary, installed by `install.sh` with auto-update, and nothing should import it. A
published module path is a compatibility promise, and the ledger schema, the report command, and
the embedded dashboard are all still moving. Publishing later is additive; retracting is not.

## The dashboard bundle is committed, and guarded

`web` builds the Vite bundle and does nothing else. Copying its output into the embedding package
is a separate, explicit step, and the built bundle is committed — so `go build` never needs Node,
goreleaser is unchanged, and contributors without a Node toolchain can build.

The risk this creates is drift: edit `web/`, forget to rebuild, and the binary embeds the previous
dashboard. Nothing fails — it builds, tests pass, and a fix silently does not take effect. That is
the failure `release.yml` already describes for the unmoved major tag: *"the delivery channel
depended on someone remembering after every release. Forgetting is silent and total."* That one was
fixed by verifying rather than remembering, and so is this: a hash over `web/src/**`, the lockfile,
and the Vite config is committed beside the bundle, and CI fails when the recomputed hash differs.
No Node in the check, and no dependence on Vite being byte-reproducible.

## The action installs `latest`

The action's `version` input keeps defaulting to `latest`, so the action's tag and the scanner's
version stay decoupled and no consumer is ever silently running a stale scanner — the failure
ADR 0006 exists to prevent.

The accepted cost is blast radius: a bad release reaches every consuming repo on its next CI run.
That is answered by gating the release rather than pinning the consumers — `release.yml` already
runs build and test before goreleaser, and lydite is run against a real repository before the tag
is published. Per-consumer pinning remains available via the `version` input and
`LYDITE_VERSION`. The alternative — baking a default CLI version into each action release —
trades a loud, immediate failure for a silent, permanent one, which is the wrong direction for a
security tool.

## A fresh repository, starting at 3.0.0

The move is a fresh repository rather than a GitHub transfer. A transfer would preserve history,
releases, and a redirect that keeps `uses: wardnet/bulwark@v1` resolving — but it would resolve to
the monorepo, whose root `action.yml` has moved to `lydite/actions`, so a deprecating shim would
be needed anyway. With a single consumer, that machinery buys nothing: the old repo stays up until
the new one is stable, its consumers are then repointed by hand, and it is dropped.

The first release is 3.0.0. lydite is at 2.0.0, and this is genuinely breaking in three ways at
once — the module path changes, the state branch is renamed, and the action moves to another
repository — so a major bump is what the change already is, not a marketing gesture.
