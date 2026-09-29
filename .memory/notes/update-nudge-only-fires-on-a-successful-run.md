---
name: update-nudge-only-fires-on-a-successful-run
kind: gotcha
description: maybeNudgeUpdate runs only after root.Execute() returns nil, because main exits first on any error including a referral's exit 2.
anchors:
  - path: source/cli/cmd/lydite/main.go
    blob: 0985162033d2
  - path: source/cli/cmd/lydite/update.go
    blob: 782d272eccd2
confidence: verified
---

`main()` (`cmd/lydite/main.go`) calls `root.Execute()`, and on any error — including a `ui.ExitError` for an ordinary referral (exit 2) — calls `os.Exit(...)` immediately (`main.go:23` or `:26`), before reaching `maybeNudgeUpdate()` (`:28`). The update nudge is therefore reachable only on the success path, never after a referral, a crashed scan or any non-zero exit — most of the runs a person watching stderr cares about. `update.go`'s own comments describe the TTL/CI/dev-build gating (`nudgeWanted`) but not this exit-path gating. No test exercises it: nothing runs `main()` end to end.
