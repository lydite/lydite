---
about: the cmd/lydite test package takes 6-23 minutes under -race depending on machine load, past go test's default 10-minute panic timeout on a loaded machine, so a bare `go test` there can die with a timeout panic and no failing test
saw:
  - source/cli/cmd/lydite/mutation_test.go
  - source/cli/AGENTS.md
  - .github/workflows/ci-test.yml
---

Running `go test -race -tags 'grammar_subset grammar_subset_rust grammar_subset_typescript
grammar_subset_tsx grammar_subset_python' ./cmd/lydite/...` measured 353s-588s wall-clock across
several runs on one machine, and one run tripped go test's default 10m limit: a timeout panic with
no `--- FAIL` line anywhere. A later observation on a loaded machine measured 15-23 minutes for the
same package — well past 10m every time. The package holds 618 top-level `func Test` functions
(counted across `cmd/lydite/*_test.go`), and the time is concentrated in a serial tail of
end-to-end tests of 10-28s each that drive real runs (mutation, fold, record).

Those local measurements were taken on a machine whose global git config sets
`commit.gpgsign=true`, and the package's git fixtures inherit it (see
`20260928-cmd-lydite-fixtures-inherit-global-git-signing.md`), so part of the local wall-clock is
GPG signing rather than the tests' own work.

CI's `build & test` job (`.github/workflows/ci-test.yml`) runs `go test -race -tags
"$LYDITE_BUILD_TAGS" ./...` with no `-timeout`. On `main`'s `gt CI` run the `lydite / test (cli)`
job, which runs the cli component's suite through `lydite test`, took about 3 minutes, so the
default limit only bites on a slower, contended or signing local machine.

Verification commands run against this package locally should pass `-timeout 30m` (or more on a
loaded machine). A timeout panic with no failing test is the package being slow, not a regression
to bisect.
