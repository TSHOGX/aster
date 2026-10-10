import { defineConfig } from "@playwright/test";
const origin = process.env.ADMIN_LIVE_URL;
if (!origin) throw new Error("ADMIN_LIVE_URL is required; run scripts/check.sh frontend-edge");
export default defineConfig({
  testDir: "./e2e", testMatch: "live.spec.ts", workers: 1, timeout: 45000,
  reporter: [["list"], ["json", { outputFile: "test-results/live-report.json" }]],
  outputDir: "test-results/live", use: { baseURL: origin, trace: "retain-on-failure", locale: "en-US" },
});
