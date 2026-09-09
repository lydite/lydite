# `docs/design` — where the brand is authored

Read [`design.md`](../../.agents/references/design.md) before touching anything here. It
carries the split that matters — `assets/` is what ships, `docs/design/source/` is where the
brand is authored, `docs/design/reference/` is reference only — and the two things the token
set documents but nothing implements.

`lydite-brand.svg` is the only file in the tree carrying live `<text>`. Editing it needs
**Kohinoor Telugu Bold**, which ships with macOS and is absent everywhere else; a machine
without it substitutes silently and the wordmark redraws wrong.

Do not port the `.dc.html` prototypes' runtime or inline styles into `source/web/`.
