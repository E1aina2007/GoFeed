import process from 'node:process'
import { defineConfig, devices } from '@playwright/test'

const baseURL = process.env.GOFEED_TIMELINE_WEB
if (!baseURL || !/^http:\/\/127\.0\.0\.1:\d+$/.test(baseURL)) {
  throw new Error('Run TestTimelineBrowserLive from backend to own test services and cleanup')
}

export default defineConfig({
  testDir: './integration/playwright',
  testMatch: 'timeline-live.spec.ts',
  timeout: 60_000,
  expect: { timeout: 15_000 },
  retries: 0,
  workers: 1,
  reporter: 'line',
  outputDir: './test-results/timeline-live',
  use: {
    baseURL,
    headless: true,
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
  },
  projects: [
    { name: 'chromium', use: devices['Desktop Chrome'] },
    { name: 'Mobile Chrome', use: devices['Pixel 5'] },
  ],
})
