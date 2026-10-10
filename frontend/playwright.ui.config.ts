import { defineConfig } from "@playwright/test";
import { resolve } from "node:path";
process.env.PREVIEW_FIXTURES ||= resolve(__dirname, "test-results/fixtures");
// Evidence runs measure the application without competing browser workers.
const profiling = [
  "ACTIVITY_PROFILE_OUTPUT",
  "HISTORY_PROFILE_OUTPUT",
  "HISTORY_WINDOW_OUTPUT",
  "MARKDOWN_PROFILE_OUTPUT",
  "MARKDOWN_LATENCY_OUTPUT",
  "OFFICE_LATENCY_OUTPUT",
  "PREVIEW_PROFILE_OUTPUT",
  "READ_LATENCY_OUTPUT",
  "STARTUP_PROFILE_OUTPUT",
].some((name) => process.env[name]);
const officeTests = ["office-visual.spec.ts", "preview-accessibility.spec.ts"];
export default defineConfig({
  globalSetup: "./e2e/ui-setup.ts",
  testDir: "./e2e/ui",
  workers: profiling ? 1 : 4,
  forbidOnly: !!process.env.CI,
  timeout: 45000,
  expect: { timeout: 10000 },
  reporter: [["list"], ["json", { outputFile: "test-results/ui-report.json" }]],
  outputDir: "test-results/ui",
  // Start the real 120-second observation before the shorter functional files
  // so its waiting time overlaps their work. Every test still runs once.
  projects: [
    { name: "idle", testMatch: "realtime-idle.spec.ts" },
    // Large corpus rendering and CPU-throttled accessibility cases compete
    // heavily on a two-core runner. Run one alongside the lighter UI files.
    { name: "office", testMatch: officeTests, workers: 1 },
    {
      name: "ui",
      testIgnore: ["realtime-idle.spec.ts", ...officeTests],
      fullyParallel: !profiling,
    },
  ],
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
