---
name: rust-policyfor-lydite-is-exactly-policy-configured
kind: invariant
description: rust.PolicyFor answers PolicyFromLydite exactly when policy.Configured(), so a stated lydite licence policy always beats a component's own deny.toml.
anchors:
  - path: source/cli/internal/rust/deny.go
    blob: 6ce9d156ef27
  - path: source/cli/internal/stages/scan/licences.go
    blob: b63a436db7d9
confidence: verified
---

`PolicyFor` (`internal/rust/deny.go`, `policy.Configured()` check at line 305) returns `PolicyFromLydite` whenever the policy is configured, before it looks for a `deny.toml`; only an unconfigured policy falls through to `PolicyFromConsumer` (a deny config found by `denyConfig(dir)`) or `PolicyFromNone`. Its doc: "The repository's own policy wins over a component's deny.toml." So `PolicyFor(dir, p) == PolicyFromLydite` and `p.Configured()` are the same predicate for every `dir`, and a component with its own `deny.toml` is governed by it only in a repository stating no `licence.policy.allow`.

Easy to misread as a behaviour change: `rustLicence` (`internal/stages/scan/licences.go`) guards the merge-base read with `rust.PolicyFor(p.Dir, policy) == rust.PolicyFromLydite`, where the pre-flow code guarded it with `policy.Configured()`. They are equivalent — no configured policy yields `PolicyFromConsumer`, so no consumer-governed component started or stopped getting a base read. (Consumer policies never take a base: `.claude/rules/only-lydites-own-licence-policy-is-delta-gated.md`.) `licenceGated` in `internal/stages/record/findings.go` also calls `PolicyFor`, but compares against `PolicyFromNone`.
