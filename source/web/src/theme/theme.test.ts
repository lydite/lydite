import { expect, it } from "vitest";
import { colorMode, lyditeTheme, pageStylesheet } from "./theme";
import { brand, font, status, surface } from "./tokens";

it("pins the dashboard to the dark mode", () => {
  expect(colorMode).toBe("dark");
});

it("paints every dark colour vipengele derives with lydite's own value", () => {
  expect(lyditeTheme).toMatchObject({
    "--vpg-surface-dark": surface.bg,
    "--vpg-surface-raised": surface.surface,
    "--vpg-ink-dark": surface.text,
    "--vpg-ink-muted": surface.muted,
    "--vpg-ink-subtle": surface.faint,
    "--vpg-border": surface.border,
    "--vpg-border-strong": surface.strong,
    "--vpg-accent-dark": brand.accent,
    "--vpg-accent-ring": brand.accent,
    "--vpg-success-dark": status.pass,
    "--vpg-danger-dark": status.crit,
    "--vpg-warning-dark": status.med,
    "--vpg-info-dark": status.info,
  });
});

it("carries every severity level under its own name", () => {
  for (const [level, value] of Object.entries(status)) {
    expect(lyditeTheme[`--vpg-status-${level}`]).toBe(value);
  }
});

it("sets Geist for language and JetBrains Mono for machine output", () => {
  expect(lyditeTheme["--vpg-font-sans"]).toBe(font.sans);
  expect(lyditeTheme["--vpg-font-mono"]).toBe(font.mono);
});

it("declares no light scheme and no colour-scheme query on the page", () => {
  expect(pageStylesheet).toContain("color-scheme: dark;");
  expect(pageStylesheet).not.toContain("prefers-color-scheme");
  expect(pageStylesheet).not.toMatch(/color-scheme: light/);
});
