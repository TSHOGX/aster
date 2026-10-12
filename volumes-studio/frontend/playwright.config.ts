import { defineConfig } from '@playwright/test';
export default defineConfig({
  testDir: './e2e', timeout: 60000, workers: 1, fullyParallel: false,
  outputDir: './test-results', reporter: 'list',
  use: { baseURL: 'https://127.0.0.1:18443', ignoreHTTPSErrors: true, trace: 'retain-on-failure' },
  webServer: { command: 'node ../tests/browser/serve.mjs', url: 'https://127.0.0.1:18443/volumes-studio/', ignoreHTTPSErrors: true, timeout: 120000, reuseExistingServer: false, gracefulShutdown: { signal: 'SIGTERM', timeout: 5000 } },
});
