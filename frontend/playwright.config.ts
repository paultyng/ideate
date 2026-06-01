import { defineConfig } from '@playwright/test'

// COLLECT_SCREENSHOTS=1 captures one PNG at the end of every test (not
// just on failure) into test-results/. Used for design audits — the
// screenshot pile across the suite gives a broad view of the app's
// visual state in real test scenarios. Curated README screenshots
// still come from screenshots.spec.ts via task generate:screenshots.
const collectScreenshots = process.env.COLLECT_SCREENSHOTS === '1'

export default defineConfig({
  testDir: './playwright',
  timeout: 30000,
  retries: 0,
  // One worker per run — every test shares the single `wails dev`
  // backend (one filesystem store, one agent coordinator, one MCP
  // server). Multi-worker parallelism caused rotating flakes:
  // concurrent testagents pile up running sessions in the global bar,
  // CPU contention pushes testagent's 5s auto-exit timer past test
  // assertion windows, and the per-idea filesystem races on session
  // record writes. Serializing keeps each test's "this is the only
  // session running" assumption true.
  workers: 1,
  use: {
    baseURL: 'http://localhost:34116',
    headless: true,
    screenshot: collectScreenshots ? 'on' : 'only-on-failure',
    trace: 'on-first-retry',
  },
  projects: [
    { name: 'chromium', use: { browserName: 'chromium' } },
  ],
})
