---
about: Requirements still emits a manager requirement after the Node one for TypeScript units, and now for NodeCommand units
saw:
  - source/cli/internal/toolchain/require.go
targets: a-typescript-unit-can-carry-two-toolchain-requirements
verdict: still-true
---
`Requirements` appends `managerRequirement` output (Manager set, same Unit) right after the Node requirement when the unit's resolved language (`Unit.lang()`) is TypeScript. That is a TypeScript-runner unit, or a `NodeCommand` unit, which resolves as TypeScript while its `Unit.Lang` stays empty. `managerRequirement` emits nothing for a pin lydite refuses (unless provisioning is disabled) or npm. toolchain.go line numbers in the note were not re-derived.
