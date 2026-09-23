about: the scheduler compares Occupies paths as opaque strings, so an absolute path outside the scan root still serialises two components that declare the identical string
saw:
  - source/cli/internal/scheduler/scheduler.go
  - source/cli/cmd/lydite/test.go
---

`Component.Occupies`'s doc comment (internal/component/component.go) describes the
field as "further directories, relative to the scan root" — but nothing downstream
enforces that. `itemFor` (cmd/lydite/test.go:560-561) passes `p.c.Occupies` straight
into `scheduler.Item.Occupies`, unjoined with the scan root or the component's `Dir`.
The scheduler's own conflict predicate, `dirsOverlap`/`contains` (internal/scheduler/scheduler.go:86-107),
compares two path strings for exact equality or a `/`-prefix relationship — it never
checks that either string is actually under the scan root.

Consequence: two components can declare the *same absolute path outside the repository
tree* (e.g. `~/.cache/ms-playwright`, expanded to an absolute path) as an `Occupies`
entry, and the scheduler will still serialise them via the equality branch of
`contains` — the mechanism doesn't require the convention its doc comment states, only
that both components spell the shared resource identically. Relied on by
docs/adr/0055-an-install-step-is-a-setup-command-and-the-override-still-coalesces.md's
claim that `occupies:` handles two components racing on a shared install-time cache
directory outside the repo.
