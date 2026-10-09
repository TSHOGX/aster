import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: "./e2e/project",
  workers: 1,
  forbidOnly: !!process.env.CI,
  timeout: 120000,
  expect: { timeout: 20000 },
  reporter: [["list"]],
  outputDir: "test-results/project",
  use: {
    baseURL: process.env.PROJECT_BROWSER_URL,
    viewport: { width: 1600, height: 1000 },
    trace: "retain-on-failure",
    launchOptions: {
      executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE,
      args: ["--no-sandbox", "--disable-dev-shm-usage"],
    },
  },
});
