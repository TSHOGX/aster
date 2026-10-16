import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: "./e2e", testMatch: ["ui.spec.ts", "volume-imports.spec.ts", "evolution.spec.ts", "evolution-output.spec.ts", "evolution-source.spec.ts", "evolution-permissions.spec.ts", "studio-permissions.spec.ts"], workers: 2, timeout: 30000,
  expect: { timeout: 10000 },
  reporter: [["list"], ["json", { outputFile: "test-results/ui-report.json" }]],
  outputDir: "test-results/ui",
  use: { baseURL: "http://127.0.0.1:3108", viewport: { width: 1440, height: 1000 }, trace: "retain-on-failure" },
  webServer: { command: "node scripts/start-ui-server.mjs", url: "http://127.0.0.1:3108/admin", reuseExistingServer: false, timeout: 60000 },
});
