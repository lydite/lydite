// lydite's design tokens, as docs/design/tokens.md states them. The dashboard is dark-only,
// so only the dark surfaces and the on-dark accent are carried: no product screen is designed
// light, and a light ramp here would be a theme nothing has drawn.

export const brand = {
  /** Primary action, active state, focus ring and links, on a dark background. */
  accent: "#A17AFF",
  /** A link under the pointer, on a dark background. */
  linkHover: "#D2C0FF",
  selection: "rgba(105,48,232,0.4)",
} as const;

export const surface = {
  bg: "#0D0E10",
  surface: "#151619",
  raised: "#1C1E21",
  border: "#24262A",
  strong: "#35383E",
  text: "#D3D6D8",
  muted: "#9A9DA3",
  faint: "#767980",
  track: "#1C1E21",
} as const;

/** Status and severity. A colour never carries a severity alone: a glyph or a label says it too. */
export const status = {
  pass: "#16C79A",
  crit: "#F2426E",
  high: "#FF7A45",
  med: "#F0B429",
  low: "#35C4E8",
  info: "#9A9DA3",
} as const;

export const font = {
  /** Language: headings, prose, controls. */
  sans: '"Geist Variable", ui-sans-serif, system-ui, sans-serif',
  /** Anything the machine produced: paths, commands, hashes, metrics, CLI output. */
  mono: '"JetBrains Mono Variable", ui-monospace, monospace',
} as const;

export const radius = {
  chip: "4px",
  button: "8px",
  panel: "12px",
  card: "14px",
  pill: "999px",
} as const;

export const elevation = {
  raised: "0 6px 20px rgba(0,0,0,0.35)",
  overlay: "0 24px 60px rgba(0,0,0,0.55)",
} as const;

export const motion = {
  micro: "120ms",
  standard: "180ms",
  entrance: "260ms",
  easing: "cubic-bezier(.2,.8,.3,1)",
} as const;

export const focus = {
  width: "2px",
  offset: "2px",
} as const;
