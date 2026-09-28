---
about: the cli component's -race suite takes about as long as go test's 10-minute default timeout, and its declared args set none, so mutation's baseline can time out and read "not measured"
saw:
  - .lydite/components.yml
  - source/cli/cmd/lydite
---

- `.lydite/components.yml` declares `cli` with `args: ["-race", "./..."]` and no `-timeout`, so every run lydite makes of that suite — `lydite test`, and `lydite mutation`'s baseline and per-mutant runs — gets `go test`'s 10-minute default per package.
- `./cmd/lydite` alone under `-race` measured 602s and 796s on a developer laptop (2026-09-28), with other suites running beside it. At that length the baseline panics with `test timed out after 10m0s`, and `lydite mutation` then reports its row as not measured because the baseline suite did not pass.
- Running the suite by hand needs `-timeout 30m` to be reliable on such a machine. Adding a `-timeout` to the declared `args` is not a producer change for coverage (it does not move the denominator; see `.claude/rules/fold-only-denominator-moving-args-into-a-producer.md`).
