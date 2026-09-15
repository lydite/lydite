# Layout

> **The annotated directory tree** — every package and top-level path, and which reference
> or rule governs it.

```
source/cli/                       # the Go module (module path lydite/lydite); every go command runs here
source/cli/cmd/lydite/            # the lydite CLI (scan, test, review, version, update)
source/cli/internal/ui/           # the output grammar every command renders through, plus --json
                                  #   and the standing PR comment (see surface.md)
source/cli/internal/referral/     # exemptions, disqualifiers, the referral decision (see referral-and-clearance.md)
source/cli/internal/clearance/    # the comment surface and the clearance decision (see referral-and-clearance.md)
source/cli/internal/threads/      # the review threads a located finding becomes: the marker, the
                                  #   delta, and the operations document two transports apply
                                  #   (see surface.md)
source/cli/internal/forge/        # the hosting platform: commit statuses, permission, comments,
                                  #   and the review calls a thread is made of
source/cli/internal/component/    # .lydite/components.yml: what a repo builds and tests (see components.md)
source/cli/internal/orphan/       # source files under no component and no exclude (see orphan-and-affected.md)
source/cli/internal/pathmatch/    # the anchored path-pattern syntax both declarations are written in
source/cli/internal/pins/         # a tool version stated twice: the mirrors, and the drift guard
source/cli/tools/pinsync/         # writes those mirrors from the manifests Dependabot edits
source/cli/internal/runner/       # a runner name to its plain/instrumented/build-only invocations
source/cli/internal/nodedeps/     # how a JavaScript workspace's dependencies get installed
source/cli/internal/cargotool/    # pinned cargo subcommands: version parsing and the install
source/cli/internal/download/     # fetch, checksum-verify and unpack an archive safely
source/cli/internal/compose/      # the services a component's suite needs (see services-and-scheduling.md)
source/cli/internal/scheduler/    # port locks and the concurrency bound (see services-and-scheduling.md),
                                  #   and the conflict predicate the planner groups shards with
source/cli/internal/affected/     # which components a change could have broken (see orphan-and-affected.md)
source/cli/internal/gitdiff/      # the changed paths, and the tracked-file listing both gates read
source/cli/internal/config/       # .lydite/config.yml loading (opt-outs + pipeline shape — see configuration.md)
source/cli/internal/toolchain/    # ensures the Go/Rust/Node runtime each component needs (see toolchains.md)
source/cli/internal/rust/         # clippy, cargo-audit, cargo-deny
source/cli/internal/typescript/   # pinned Biome, the only TS linter (see linters.md)
source/cli/internal/golang/       # gosec, govulncheck (installed into a version-keyed GOBIN dir)
source/cli/internal/semgrep/      # pinned Semgrep, installed via pipx
source/cli/internal/coverage/     # reads a component's coverage report (see coverage.md)
source/cli/internal/crap/         # the CRAP index per function, Go, Rust and TypeScript alike (see crap.md)
source/cli/internal/mutation/     # the mutants, the isolation strategies, and what became of each
                                  #   (see mutation.md)
source/cli/internal/finding/      # the located claims a gate makes, as data: the fingerprint
                                  #   that identifies one and how precisely it reaches the
                                  #   change (see findings.md). A leaf, so the report
                                  #   document and every producer can both import it
source/cli/internal/annotation/   # the exclusion declaration: the [lydite:exclude_from_<gate>]
                                  #   token for mutation, crap and coverage, and how a comment
                                  #   carrying one is read. A leaf, so referral can link it
source/cli/internal/gitstate/     # the base branch, and lydite branch read/write (see coverage.md)
source/cli/internal/ledger/       # the quality history: append-only NDJSON and the daily
                                  #   projection the dashboard reads (see quality-history.md)
source/cli/internal/junit/        # the test counts every runner's JUnit report holds
source/cli/internal/gotool/       # the version-keyed `go install` internal/golang and
                                  #   internal/runner both provision through
source/cli/internal/executil/     # shared external-command runner every scanner package uses
source/cli/.golangci.yml          # lint config (v2 schema)
source/web/                       # the React dashboard (see ADR 0021 for the source/ root)
source/cloud-services/            # the Workers lydite operates, as one npm workspace and so one
                                  #   component: pr-relay posts the PR comment on a consumer's
                                  #   behalf, oauth-exchange is the dashboard's (see surface.md)
assets/                           # the shipped logo set, plus the org avatar (see design.md)
docs/design/source/               # where the brand is authored; the only live <text> in the tree
docs/design/                      # tokens, surface specs, and the reference prototypes (see design.md)
docs/release-notes/               # _header.md + one <tag>.md per release that needs one (see release-notes.md)
docs/adr/                         # the decision record
.lydite/                          # every file that configures lydite: config.yml, components.yml,
                                  #   exemptions.yml
.goreleaser.yml                   # build/release config (v2 schema)
.gt-repo.yaml                     # repo governance; renders .github/dependabot.yml and the gt workflows
.github/workflows/                # CI stages, the release, the Workers' deploy, lydite-pr.yml —
                                  #   everything lydite says about a PR, as one run (see surface.md) —
                                  #   and lydite-baseline.yml, which holds the one job that writes
                                  #   the coverage baseline, after a change has merged (see coverage.md)
.github/actions/                  # the local composites both lydite workflows use, which are the
                                  #   shapes lydite/actions packages (that repository is not here)
scripts/install.sh                # curl|sh installer shipped with every release
```
