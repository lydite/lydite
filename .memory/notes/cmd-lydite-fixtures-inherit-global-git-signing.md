---
name: cmd-lydite-fixtures-inherit-global-git-signing
kind: gotcha
description: cmd/lydite's git fixtures inherit the developer's global git config, so a global commit.gpgsign=true signs every fixture commit and slows the package while CI runs unsigned.
anchors:
  - path: source/cli/cmd/lydite/affected_test.go
    blob: e0086eff34c8
  - path: source/cli/internal/gitdiff/gitdiff_test.go
    blob: f86134a2df1e
  - path: source/cli/internal/apisurface/probe_test.go
    blob: 4d80c1bbe9c1
confidence: suspect
---

`cmd/lydite`'s fixture helpers shell out to plain `git` in a `t.TempDir()` and set only `user.email`/`user.name`: `affectedRepo` and `commitChange` (`cmd/lydite/affected_test.go`) are typical, and about ten `cmd/lydite/*_test.go` files commit this way (`coverage_test.go` alone has ten `"commit"` sites). Every such commit reads the developer's global and system git config. No isolation exists at package or module level: `grep -rn "TestMain\|gpgsign" cmd/lydite` finds nothing, and `source/cli` has no `GIT_CONFIG_GLOBAL`/`GIT_CONFIG_NOSYSTEM` use. Three other packages opt their fixtures out with a repo-local `git config commit.gpgsign false` — `internal/gitdiff/gitdiff_test.go`, `internal/flaky/newtests_test.go`, `internal/apisurface/probe_test.go` — and `cmd/lydite` has none.

With a global `commit.gpgsign=true`, every fixture commit is GPG-signed: measured 0.48-0.51s per signed commit against 0.15-0.18s unsigned with a warm agent (~2.1s vs ~0.2s under load in an earlier session). Multiplied across the package's fixture commits that can push `cmd/lydite` past `go test`'s 10-minute default locally while CI (no signing) runs it in about 3 minutes; an unanswerable signing prompt would fail every fixture commit outright. Local workarounds: `GIT_CONFIG_GLOBAL=/dev/null`, or `GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=commit.gpgsign GIT_CONFIG_VALUE_0=false`, on the `go test` command line (each checked to leave one fixture-style commit unsigned, not timed against the whole suite). The absence of isolation was re-checked; the timings were not, hence `suspect`. See [[cli-suite-outruns-gos-default-test-timeout]].
