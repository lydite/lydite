---
about: source/cli/cmd/lydite/test.go
saw: "--gate-flaky flag help text claims Go-only rerun, but the gate covers three languages"
---

`--gate-flaky`'s flag help string at `source/cli/cmd/lydite/test.go:311` reads "rerun each
**Go** component's new tests once, in a process of their own, and fail on a test whose two
outcomes disagree" — but the flaky gate reruns Go, Rust (cargo-nextest) and TypeScript (vitest)
tests, per `flakyRerunner` around `source/cli/cmd/lydite/test.go:1421` and ADR 0041 (#164). The
help text was never updated when the gate grew the other two languages, so `lydite test --help`
undersells what `--gate-flaky` actually covers.

Found while rewriting `docs/release-notes/v0.2.0.md`: the release note correctly describes the
flaky gate as covering three languages, which made the stale `--help` string stand out by
contrast.
