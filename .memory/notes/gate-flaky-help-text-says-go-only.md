---
name: gate-flaky-help-text-says-go-only
kind: gotcha
description: --gate-flaky's flag help string undersells its own scope — it reads as Go-only rerun, but the gate reruns Go, Rust (cargo-nextest) and TypeScript (vitest) tests per ADR 0041.
anchors:
  - path: source/cli/cmd/lydite/test.go
    blob: 9be41f98c783
confidence: suspect
---

`--gate-flaky`'s flag help string (`test.go` around line 311) reads "rerun each **Go** component's
new tests once ... and fail on a test whose two outcomes disagree" — but the flaky gate covers three
languages (`flakyRerunner` around `test.go:1421`, ADR 0041/#164). The help text was never updated
when the gate grew the other two languages, so `lydite test --help` undersells what `--gate-flaky`
actually covers. Found while rewriting `docs/release-notes/v0.2.0.md`, whose release note correctly
describes the flaky gate as covering three languages, making the stale `--help` string stand out by
contrast.
</content>
