# Only lydite's own licence policy is delta-gated; a consumer's own config gates absolutely

A Rust component's licence check runs under one of three policy sources —
`PolicyFromLydite` (generated from `licence.policy.allow`), `PolicyFromConsumer` (the
component's own `deny.toml`), or `PolicyFromNone`. Delta-gating a consumer's own `deny.toml`
against the merge-base would weaken a check that repository opted into deliberately —
cargo-deny already evaluates that file whole on every run, and grandfathering an existing
rejection out of it is not lydite's call to make. Only `PolicyFromLydite` compares against the
merge-base; `PolicyFromConsumer` fails on every rejection cargo-deny reports, every run.

## Applies to

`internal/rust/deny.go`'s `PolicyFor` and anything that reads a Rust component's licence
verdict.

## Example

Wrong: running `rust.LicenceCheck` with a merge-base `licence.Base` whenever a `deny.toml` is
present, so a licence the consumer's own config already rejected passes because it was also
rejected at the merge-base.

Right: `PolicyFromConsumer` never receives a `licence.Base` — its row fails on cargo-deny's
own verdict, unconditionally.
