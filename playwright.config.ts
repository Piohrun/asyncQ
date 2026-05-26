import { defineConfig, devices } from '@playwright/test';

const baseURL = process.env.ASYNCQ_E2E_BASE_URL || 'http://localhost:3000';
const startDemo = process.env.ASYNCQ_E2E_START_DEMO === '1';

export default defineConfig({
  testDir: './tests/e2e',
  timeout: 90_000,
  globalSetup: startDemo ? './tests/e2e/global-setup.ts' : undefined,
  globalTeardown: startDemo ? './tests/e2e/global-teardown.ts' : undefined,
  expect: {
    timeout: 30_000,
  },
  use: {
    baseURL,
    trace: 'retain-on-failure',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
});
