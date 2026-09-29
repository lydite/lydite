---
about: the resume fingerprint hashes the composed environment, and that environment includes the inherited PATH, so state is not shared between shells whose PATH differs
saw:
  - source/cli/internal/stages/mutation/run.go
  - source/cli/internal/toolchain
---

`stateFingerprint` folds in `envDigest(Shape.Env(tc, c, t.inv))` and the suite's environment. `toolchain.Compose` adds `os.Getenv("PATH")` to that environment, so two local runs from shells with different PATHs open different fingerprints and each measures everything. `envDigest` keeps the last occurrence of each key, sorts, and hashes each entry with a length prefix; it is never printed or written.
