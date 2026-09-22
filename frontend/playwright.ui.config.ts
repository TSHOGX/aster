import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: "./e2e/ui",
  workers: 1,
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
    command:
      "npm run build && npm run start -- --hostname 127.0.0.1 --port 3107",
    url: "http://127.0.0.1:3107",
    reuseExistingServer: false,
    timeout: 120000,
  },
});
