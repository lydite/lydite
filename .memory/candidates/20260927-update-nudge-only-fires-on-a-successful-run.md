---
about: maybeNudgeUpdate() never runs on a failing or referred command, only on a clean exit
saw:
  - source/cli/cmd/lydite/main.go
  - source/cli/cmd/lydite/update.go
---

`main()` (`source/cli/cmd/lydite/main.go:14-29`) calls `root.Execute()`, and on any error —
including a `ui.ExitError` for an ordinary referral (exit 2) — calls `os.Exit(...)` immediately
(`main.go:23` or `main.go:26`), which terminates the process before it ever reaches
`maybeNudgeUpdate()` at `main.go:28`. The update nudge is therefore reachable only on the
success path (`Execute()` returns nil), never after a referral, a crashed scan, or any other
non-zero exit — which is most of the runs a person watching stderr actually cares about (a
referral is exactly the kind of run where "there's a newer lydite" might matter). This isn't
mentioned in `update.go`'s own comments (`update.go:249-274`), which describe the TTL/CI/dev-build
gating (`nudgeWanted`, `update.go:283-289`) but not this exit-path gating in `main.go`.

Confirmed by reading `main.go:14-29` directly — no test exercises this interaction (there is no
integration test that runs `main()` end to end).
