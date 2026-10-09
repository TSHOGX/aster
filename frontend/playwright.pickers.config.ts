import { defineConfig } from "@playwright/test";
import base from "./playwright.ui.config";

export default defineConfig({
  ...base,
  globalSetup: undefined,
  testMatch: "schedule-pickers.spec.ts",
  outputDir: "test-results/pickers",
  projects: [
    { name: "chromium", use: { browserName: "chromium" } },
    { name: "firefox", use: { browserName: "firefox" } },
    { name: "webkit", use: { browserName: "webkit" } },
  ],
});
