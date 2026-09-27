---
about: rust.PolicyFor(dir, policy) == PolicyFromLydite is exactly policy.Configured(), so a stated lydite licence policy always wins over a component's own deny.toml and "PolicyFromConsumer under a stated lydite policy" cannot happen
saw:
  - source/cli/internal/rust/deny.go
  - source/cli/internal/stages/scan/licences.go
  - source/cli/internal/stages/record/findings.go
---

`PolicyFor` (`internal/rust/deny.go`) checks `policy.Configured()` first and returns
`PolicyFromLydite` whenever it is true, before it ever looks for a `deny.toml`; only an unconfigured
policy falls through to `PolicyFromConsumer` (a deny config found by `denyConfig(dir)`) or
`PolicyFromNone`. Its doc: "The repository's own policy wins over a component's deny.toml." So
`PolicyFor(dir, p) == PolicyFromLydite` and `p.Configured()` are the same predicate for every `dir`,
and a component carrying its own `deny.toml` is governed by it only in a repository that states no
`licence.policy.allow`.

This is easy to misread as a behaviour change. `rustLicence` (`internal/stages/scan/licences.go`)
guards the merge-base read with `rust.PolicyFor(p.Dir, policy) == rust.PolicyFromLydite`, where the
pre-flow `recordRustLicence` in `cmd/lydite/scan.go` guarded the same read with
`policy.Configured()`. The two are equivalent: no case exists where a configured policy yields
`PolicyFromConsumer`, so no consumer-governed component ever started or stopped getting a base
read. (That consumer policies never take a base is the rule in
`.claude/rules/only-lydites-own-licence-policy-is-delta-gated.md`.) `licenceGated`
(`internal/stages/record/findings.go`, deciding whether `FindingCounts` seeds a component's licence
zero) also calls `PolicyFor`, but compares against `PolicyFromNone`.
