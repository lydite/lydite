import { cleanup, render, screen } from "@testing-library/react";
import { useScope } from "@vipengele/react-telemetry";
import { afterEach, expect, it } from "vitest";
import { LyditeRoot, scopeTag } from "./LyditeRoot";
import { brand, surface } from "./tokens";

afterEach(cleanup);

function ScopeTag() {
  return <output>{useScope().tag}</output>;
}

it("renders its subtree inside one vipengele root pinned to lydite's dark theme", () => {
  const { container } = render(
    <LyditeRoot>
      <main />
    </LyditeRoot>,
  );
  const roots = container.querySelectorAll(".vpg-root");
  expect(roots).toHaveLength(1);
  const root = roots[0];
  if (!(root instanceof HTMLElement)) {
    throw new Error("LyditeRoot rendered no vipengele theme root");
  }
  expect(root.contains(screen.getByRole("main"))).toBe(true);
  expect(root.dataset.vpgMode).toBe("dark");
  expect(root.style.getPropertyValue("--vpg-surface-dark")).toBe(surface.bg);
  expect(root.style.getPropertyValue("--vpg-accent-dark")).toBe(brand.accent);
});

it("runs its subtree under the dashboard's telemetry scope", () => {
  render(
    <LyditeRoot>
      <ScopeTag />
    </LyditeRoot>,
  );
  expect(scopeTag).toBe("lydite-dashboard");
  expect(screen.getByRole("status").textContent).toBe(scopeTag);
});
