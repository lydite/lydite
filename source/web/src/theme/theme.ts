import { type ColorMode, createTheme, type Theme } from "@vipengele/react-tokens";
import { brand, elevation, focus, font, motion, radius, status, surface } from "./tokens";

/**
 * The one mode the dashboard paints in. Pinning it on the provider is what keeps the OS's
 * `prefers-color-scheme` and a host page's `data-theme` from switching a screen to a light
 * ramp nobody designed.
 */
export const colorMode: ColorMode = "dark";

/**
 * vipengele's theme carrying lydite's values. vipengele seeds the light appearance and
 * derives the dark one; lydite's palette is authored dark, so every colour the dark mode
 * paints is overridden with docs/design/tokens.md's value rather than derived. The `-light`
 * variants keep vipengele's defaults and never paint, because `colorMode` is pinned.
 */
export const lyditeTheme: Theme = createTheme(
  {
    radius: radius.button,
    fontSans: font.sans,
    fontMono: font.mono,
  },
  {
    "--vpg-surface-dark": surface.bg,
    "--vpg-ink-dark": surface.text,
    "--vpg-accent-dark": brand.accent,
    "--vpg-success-dark": status.pass,
    "--vpg-danger-dark": status.crit,
    "--vpg-warning-dark": status.med,
    "--vpg-info-dark": status.info,

    // Cards, panels and popovers sit on vipengele's raised surface, which is lydite's
    // `--surface`; derived from the canvas it would not land on #151619.
    "--vpg-surface-raised": surface.surface,
    "--vpg-surface-hover": surface.raised,
    "--vpg-ink-muted": surface.muted,
    "--vpg-ink-subtle": surface.faint,
    "--vpg-border": surface.border,
    "--vpg-border-strong": surface.strong,

    // The focus ring is the accent at full strength. vipengele's own ring is the accent at
    // 45% alpha, which on the dark canvas falls under the contrast a keyboard user needs.
    "--vpg-accent-ring": brand.accent,
    "--vpg-focus-ring-width": focus.width,
    "--vpg-focus-ring-offset": focus.offset,
    // Text on a filled accent is the canvas colour: the contrast expression vipengele derives
    // picks white for #A17AFF, which reads at under 3:1.
    "--vpg-accent-contrast": surface.bg,

    "--vpg-shadow-med": elevation.raised,
    "--vpg-shadow-high": elevation.overlay,

    "--vpg-ease-standard": motion.easing,
    "--vpg-ease-entrance": motion.easing,
    "--vpg-ease-exit": motion.easing,

    // Severity has six levels and vipengele's status roles have four, so each level is
    // carried under its own name as well.
    "--vpg-status-pass": status.pass,
    "--vpg-status-crit": status.crit,
    "--vpg-status-high": status.high,
    "--vpg-status-med": status.med,
    "--vpg-status-low": status.low,
    "--vpg-status-info": status.info,
  },
);

/**
 * The page-level rules a `Theme` cannot carry. The canvas behind the provider's root and
 * the text selection are outside `.vpg-root`. The motion durations are stylesheet-owned in
 * vipengele, so `createTheme` refuses them; a rule on the dark root outranks the base
 * stylesheet's `.vpg-root` assignment, and the reduced-motion query is restated at the same
 * specificity so it still wins.
 */
export const pageStylesheet = `
html {
  color-scheme: dark;
  background-color: ${surface.bg};
  color: ${surface.text};
  font-family: ${font.sans};
}

body {
  margin: 0;
}

::selection {
  background-color: ${brand.selection};
}

.vpg-root[data-vpg-mode="dark"] {
  min-height: 100vh;
  --vpg-duration-fast: ${motion.micro};
  --vpg-duration-normal: ${motion.standard};
  --vpg-duration-slow: ${motion.entrance};
}

@media (prefers-reduced-motion: reduce) {
  .vpg-root[data-vpg-mode="dark"] {
    --vpg-duration-fast: 0.01ms;
    --vpg-duration-normal: 0.01ms;
    --vpg-duration-slow: 0.01ms;
  }
}
`;
