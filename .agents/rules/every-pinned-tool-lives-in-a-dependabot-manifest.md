# Every pinned tool version lives in a manifest Dependabot watches

A pin nothing can age out is a scanner that goes stale while still reporting a pass. A new pin
needs its own manifest colocated with the package that uses it, an entry in
`.github/dependabot.yml`, and — for a cargo pin — an `src/lib.rs` and an exclude in
`.lydite/components.yml`, because `cargo metadata` fails on a manifest with no target and the
updater then stops bumping that pin in a job log nobody reads. A version restated outside its
manifest is a mirror `internal/pins` must know about.

Reasoning: [`.agents/references/tool-pins.md`](../references/tool-pins.md).
