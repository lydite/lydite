---
about: .lydite/components.yml's cli component declares args ["-race", "./..."] with no -timeout, so lydite's own invocation of its own suite (test, and mutation's baseline run) is exposed to go test's default 10-minute limit, not just a verification command someone typed by hand
saw:
  - .lydite/components.yml
  - source/cli/cmd/lydite/mutation_test.go
targets: cmd-lydite-suite-sits-near-gos-default-timeout
verdict: still-true
---

Found running `lydite mutation --dir . --component cli --base-branch main` from a built binary
as part of landing #270: the baseline suite reported "not measured — the baseline suite did not
pass" with 42 failures, all `(unknown)` outcome — the signature of a killed-by-timeout process
rather than real test failures. A bare `go test ./cmd/lydite/...` (no `-race`, no tags) on the
same machine under the same load independently timed out at the same 10-minute mark, confirming
the cause is wall-clock, not a regression in the tested code.

This sharpens the existing `cmd-lydite-suite-sits-near-gos-default-timeout` note, which frames
the risk as "a verification command someone runs by hand should pass `-timeout 30m`." The
declared args in `.lydite/components.yml` are the same risk from the other direction: lydite's
own `test`/`mutation` commands build the invocation from those args, and the args carry no
`-timeout` at all, so the exposure is not limited to a person's own terminal — it is baked into
every run this repository does of its own `cli` component's suite. The mutation gate handled it
correctly (an honest "not measured" rather than a false result), so nothing here is broken, but
a component whose own declared args this narrow is one where a busier machine — a shared runner,
several CI jobs concurrent, a session running other test suites alongside — reaches the default
limit not as an edge case but as ordinary variance.
