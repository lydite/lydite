import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { App } from "./App";
import { brand, surface } from "./theme/tokens";

afterEach(cleanup);

it("renders the dashboard heading", () => {
  render(<App />);
  expect(screen.getByRole("heading", { level: 1, name: "lydite" })).toBeDefined();
});

it("renders inside a vipengele root pinned to lydite's dark theme", () => {
  render(<App />);
  const root = screen.getByRole("main").closest(".vpg-root");
  if (!(root instanceof HTMLElement)) {
    throw new Error("the dashboard rendered outside a vipengele theme root");
  }
  expect(root.dataset.vpgMode).toBe("dark");
  expect(root.style.getPropertyValue("--vpg-surface-dark")).toBe(surface.bg);
  expect(root.style.getPropertyValue("--vpg-accent-dark")).toBe(brand.accent);
});
