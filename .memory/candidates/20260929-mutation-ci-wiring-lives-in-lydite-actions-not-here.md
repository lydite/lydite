---
about: mutation's CI wiring (timeout, matrix, inputs, lydite version) lives only in lydite/actions, reached from this repo through gt's reusable-lydite.yml; a mutation-state cache must key on the shard and keep the state dir out of ~/.cache/lydite
saw:
  - .github/workflows/ci-orchestration.yml
  - agentic/references/actions.md
  - agentic/plans/pr22-mutation-resumes.md
  - /home/user/actions/mutation/action.yml
  - /home/user/actions/.github/workflows/lydite.yml
  - /home/user/actions/.github/workflows/lydite-baseline.yml
---

Found while planning slice 3 of pr22-mutation-resumes.

1. lydite/lydite has no workflow that runs `lydite mutation`. `ci-orchestration.yml`'s `lydite` job
   (`uses: pedromvgomes/gt/.github/workflows/reusable-lydite.yml@v2`, `with: relay:`) is rendered
   from `.gt-repo.yaml`; gt's reusable calls `lydite/actions`' `lydite.yml@v1`. `grep -n mutation
   .github/workflows/ci-orchestration.yml` -> 0 hits. Only `ci-end2end.yml` (proving-ground legs)
   runs a built binary directly. The mutation job timeout (`timeout-minutes: 60`,
   lydite.yml:312) and the baseline's `mutate` job (lydite-baseline.yml:156, also 60) are in
   lydite/actions. gt's reusable-lydite.yml was NOT read (no network): whether it passes
   `lydite-version` through is unverified.
2. `lydite/actions/.github/workflows/lydite.yml` calls `setup@v1` with
   `version: ${{ inputs.lydite-version }}` (default `latest`), so a new action input passed to
   an older released lydite fails at the CLI (unknown flag) - action changes must follow the lydite release.
3. The mutation action's existing `cache` step caches `~/.cache/lydite` under an immutable
   `lydite-<os>-<hashFiles>` key. lydite's default state root is `os.UserCacheDir()/lydite/mutation/...`
   (plan slice 1), i.e. inside that cache; it would be saved by that step's post-run only on a
   key miss and never refreshed, so state must go in an explicit `state-dir` outside it.
4. The matrix passes `component: ${{ matrix.shard.components }}` - a comma list - so a cache key
   "per component" is really per shard; use `matrix.shard.name` (already the artifact-name).
5. `.github/actions/*` local composites here are unused (see note
   local-lydite-actions-composites-are-dead-in-this-repo); nothing local has to mirror the mutation
   action despite actions.md saying so.
