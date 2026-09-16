---
about: Component.Lang() returns "" both for an unknown/unset runner and for a command-invoked component, so a validation checking "this component declares no language" must call Lang() rather than assume Runner is always set
saw:
  - source/cli/internal/component/component.go
---

`Component.Lang()` (`component.go`) looks up `c.Runner` via `runner.Lookup` and returns `""` on a
miss. A component invoked with `command:` instead of `runner:` never sets `Runner` at all
(`validateInvocation` requires exactly one of the two), so `Lang()` answers `""` for it too — the
same empty answer as an unrecognised runner name, even though the two are different shapes
(one has no runner because it opted out of the derived variants entirely; the other is a typo
or an unsupported runner).

`validateAPISurface` (added alongside the `api_surface` opt-in field) checks `c.Lang() != runner.Go`
defensively, deliberately not relying on `validateInvocation` having already run first in the
same validation loop — a `command:`-invoked component setting `api_surface` is correctly
rejected by the same check, with the same error, as a `cargo-nextest` component would be,
because both report `Lang() == ""` and neither is Go.

Any future per-language opt-in field on `Component` should check `Lang()` the same way rather
than checking `Runner != ""` or assuming a language is always derivable — a `command:` component
is a real, supported shape that carries no language at all.
