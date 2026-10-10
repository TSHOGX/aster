import { defineConfig } from '@playwright/test';
import base from './playwright.ui.config';
export default defineConfig({
  ...base,
  testMatch: ['office-visual.spec.ts', 'preview-slides.spec.ts'],
  outputDir: 'test-results/office-browsers',
  reporter: [['list'], ['json', { outputFile: 'test-results/office-report.json' }]],
  projects: [
    { name: 'chromium', use: { browserName: 'chromium' } },
    { name: 'firefox', use: { browserName: 'firefox' } },
    { name: 'webkit', use: { browserName: 'webkit' } },
  ],
});
