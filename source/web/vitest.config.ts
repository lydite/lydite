import { defineConfig, mergeConfig } from "vitest/config";
import viteConfig from "./vite.config.ts";

// lydite's vitest runner passes --coverage with its own reporter and report
// directory, so the provider is the only coverage setting declared here: without
// @vitest/coverage-v8 installed and named, an instrumented run fails before any
// test executes.
export default mergeConfig(
  viteConfig,
  defineConfig({
    test: {
      environment: "jsdom",
      include: ["src/**/*.test.{ts,tsx}"],
      coverage: {
        provider: "v8",
        include: ["src/**/*.{ts,tsx}"],
      },
    },
  }),
);
