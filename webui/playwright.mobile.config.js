import { defineConfig } from '@playwright/test'

// 手机版界面单独一套用例：它要用手机 UA 和窄视口从初始密码走完整个流程，
// 和桌面版用例共用一台主控会互相踩状态（改密、开两步验证），所以分开跑。
export default defineConfig({
  testDir: './e2e-mobile',
  globalSetup: './e2e-mobile/global-setup.js',
  fullyParallel: false,
  workers: 1,
  timeout: 180_000,
  expect: { timeout: 10_000 },
  reporter: [['list']],
  use: {
    baseURL: 'http://127.0.0.1',
    channel: process.env.POLARIS_E2E_BROWSER_CHANNEL || 'chrome',
    headless: true,
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
  },
  outputDir: '../artifacts/e2e/playwright-mobile',
})
