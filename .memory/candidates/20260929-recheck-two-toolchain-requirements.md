---
about: Requirements still emits a manager requirement after the Node one for TypeScript units
saw:
  - source/cli/internal/toolchain/require.go
targets: a-typescript-unit-can-carry-two-toolchain-requirements
verdict: still-true
---
require.go:168-178 appends `managerRequirement` output (Manager set, same Unit) right after the Node requirement, only for `u.Lang == runner.TypeScript`. managerRequirement is :201; it emits nothing for a pin lydite refuses (unless provisioning is disabled) or npm. toolchain.go line numbers in the note were not re-derived.
