---
about: goTestFlags cannot be reused directly to strip coverage flags from a variant that still needs its package pattern
saw:
  - source/cli/internal/runner/runner.go
---

`goTestFlags` (used by `GoRerun`) drops every argument that does not begin with `-`, because
its caller supplies the package pattern itself afterward. Calling it from `buildGoTest` to strip
coverage flags for Plain/BuildOnly would also drop a component's declared package pattern (e.g.
`./internal/...`), narrowing the invocation to the current directory alone — a larger silent
narrowing than the coverage leak it would be fixing, and nothing in the existing test suite
would have caught it (no test at the time fed a package pattern through Plain and asserted it
survives).

The fix (lydite/lydite#212) factored the shared scan into `dropCoverage(args, keepPackages
bool)`, with `goTestFlags` and the new `goTestUninstrumented` as named wrappers. It has to be
one pass, not two: pairing a flag with the value behind it (`-timeout 5m`) requires the same
pass that classifies a bare word, since a later pass over the survivors cannot tell `5m` from a
package pattern.
