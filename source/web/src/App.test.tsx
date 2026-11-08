import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { App } from "./App";

afterEach(cleanup);

it("renders the dashboard heading", () => {
  render(<App />);
  expect(screen.getByRole("heading", { level: 1, name: "lydite" })).toBeDefined();
});
