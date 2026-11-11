---
about: "vipengele's createTheme derives white text on lydite's #A17AFF accent (under 3:1) and a 45%-alpha focus ring, so lydite overrides --vpg-accent-contrast and --vpg-accent-ring"
saw:
  - source/web/src/theme/theme.ts
  - source/web/src/theme/tokens.ts
  - docs/design/tokens.md
---
`lyditeTheme` in source/web/src/theme/theme.ts passes overrides to `createTheme` (@vipengele/react-tokens). Two of them work around vipengele's own dark derivation rather than carry lydite brand values: `--vpg-accent-contrast` is set to the canvas colour because the derived contrast colour is white on #A17AFF, under 3:1, and `--vpg-accent-ring` is the full-strength accent because the derived ring is the accent at 45% alpha, too faint on the #0D0E10 canvas. Motion durations (`--vpg-duration-*`) are refused by `createTheme`, so `pageStylesheet` sets them on `.vpg-root[data-vpg-mode="dark"]` and restates the reduced-motion query. These are candidates to fix upstream in vipengele/react (agentic/rules/reuse-vipengele-before-building-ui-or-library-code.md). If vipengele fixes them, the overrides can go.
