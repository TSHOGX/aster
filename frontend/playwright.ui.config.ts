import { defineConfig } from "@playwright/test";
import { resolve } from "node:path";
process.env.PREVIEW_FIXTURES ||= resolve(__dirname, "test-results/fixtures");
export default defineConfig({
  globalSetup: "./e2e/ui-setup.ts",
  testDir: "./e2e/ui",
  workers: 4,
  forbidOnly: !!process.env.CI,
  timeout: 45000,
  expect: { timeout: 10000 },
  reporter: [["list"]],
  outputDir: "test-results/ui",
  use: {
    baseURL: "http://127.0.0.1:3107",
    viewport: { width: 1600, height: 1000 },
    trace: "retain-on-failure",
  },
  webServer: {
    command: "node scripts/start-ui-server.mjs",
    url: "http://127.0.0.1:3107",
    reuseExistingServer: false,
    timeout: 120000,
  },
});
