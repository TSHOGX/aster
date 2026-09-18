import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: "./e2e",
  reporter: [["list"], ["json", { outputFile: "test-results/results.json" }]],
  fullyParallel: false,
  workers: 1,
  use: {
    baseURL: "http://127.0.0.1:3187",
    viewport: { width: 1440, height: 900 },
    trace: "retain-on-failure",
    launchOptions: {
      executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE,
    },
  },
  webServer: {
    command:
      process.env.HARNESS_PERF_PRODUCTION === "1"
        ? "HARNESS_PERF_FIXTURES=1 npm run build && node e2e/start-production.mjs"
        : "HARNESS_DEV_ORIGINS=127.0.0.1 HARNESS_PERF_FIXTURES=1 npm run dev -- --port 3187",
    url: "http://127.0.0.1:3187/perf-fixtures/virtual",
    reuseExistingServer: false,
    timeout: 240000,
  },
});
