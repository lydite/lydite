---
about: the cmd/lydite test suite's git fixtures inherit the developer's global git config — nothing in source/cli sets GIT_CONFIG_GLOBAL or a TestMain, and cmd/lydite's fixture helpers never set commit.gpgsign=false — so a global commit.gpgsign=true signs every fixture commit, several times slower each, which can push the package past go test's default 10m locally while CI (no signing) finishes in about 3 minutes
saw:
  - source/cli/cmd/lydite/affected_test.go
  - source/cli/cmd/lydite/coverage_test.go
  - source/cli/internal/gitdiff/gitdiff_test.go
  - source/cli/internal/flaky/newtests_test.go
  - source/cli/internal/apisurface/probe_test.go
---

`cmd/lydite`'s fixture helpers shell out to plain `git` in a `t.TempDir()` and set only
`user.email`/`user.name` on the fixture repository: `affectedRepo`
(`cmd/lydite/affected_test.go` line ~17, which commits "base" at line ~50) and `commitChange`
(same file, line ~64, `git commit --quiet --allow-empty -m change`) are typical; ten
`cmd/lydite/*_test.go` files commit this way (`coverage_test.go` alone has ten `"commit"` call
sites, e.g. line ~734). Every such commit reads the developer's global and system git config.

No isolation exists at the package or module level: a search of `source/cli` finds no
`func TestMain`, no `GIT_CONFIG_GLOBAL`, `GIT_CONFIG_NOSYSTEM` or `GIT_CONFIG_COUNT`, and no
`--no-gpg-sign`. Three other packages opt their own fixtures out with a repo-local
`git config commit.gpgsign false` — `internal/gitdiff/gitdiff_test.go` line ~22,
`internal/flaky/newtests_test.go` line ~461, `internal/apisurface/probe_test.go` line ~91 — and
`cmd/lydite` has none. (Two `review_test.go` tests redirect `HOME`, for their own reasons, and
only for themselves.)

On a machine whose global config sets `commit.gpgsign=true`, every fixture commit is GPG-signed.
A spot check measured 0.48-0.51s per signed commit against 0.15-0.18s unsigned with a warm
agent; an earlier session measured ~2.1s signed against ~0.2s unsigned under load. Multiplied
across the package's fixture commits, that is enough to take `cmd/lydite` past `go test`'s
default 10-minute timeout locally, while CI's runner (no signing configured) runs the same
suite in about 3 minutes (see `20260921-cmd-lydite-suite-sits-near-gos-default-timeout.md`). A
signing prompt that cannot be answered — no agent, a locked key — would instead fail every
fixture commit outright.

Local workarounds that leave the repository untouched: `GIT_CONFIG_GLOBAL=/dev/null` or
`GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=commit.gpgsign GIT_CONFIG_VALUE_0=false` on the `go test`
command line — each checked to leave a single fixture-style commit unsigned (`%G?` = `N`), not
timed against the whole suite.
