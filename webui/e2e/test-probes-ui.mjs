import { chromium } from '../node_modules/@playwright/test/index.mjs'

const ARTIFACTS_DIR = 'C:/Users/dom/.gemini/antigravity/brain/d3c3836e-81e0-4ba0-a494-54b06d6f62f5'
const TARGET_URL = 'http://127.0.0.1:19670/'

async function main() {
  const browser = await chromium.launch({
    headless: true,
    executablePath: 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',
  })
  const context = await browser.newContext({
    viewport: { width: 1440, height: 960 },
    deviceScaleFactor: 2,
  })
  const page = await context.newPage()

  try {
    await page.goto(TARGET_URL, { waitUntil: 'domcontentloaded' })
    const isLoginPage = (await page.locator('.login-form').count()) > 0
    if (isLoginPage) {
      await page.locator('.login-form input').first().fill('polaris_admin')
      await page.locator('input[type="password"]').first().fill('123456')
      await page.getByRole('button', { name: '登录', exact: true }).click()
      await page.waitForTimeout(1500)
    }

    // 1. Go to System Settings -> 网络测速探针 Tab
    await page.goto('http://127.0.0.1:19670/#/settings', { waitUntil: 'domcontentloaded' })
    await page.waitForTimeout(1500)
    await page.screenshot({ path: `${ARTIFACTS_DIR}/settings-probes-tab.png` })
    console.log('Saved settings-probes-tab.png')

    // 2. Open Node Detail in Nodes view
    await page.goto('http://127.0.0.1:19670/#/nodes', { waitUntil: 'domcontentloaded' })
    await page.waitForTimeout(1500)
    await page.locator('.node-card').first().click()
    await page.waitForTimeout(1000)
    await page.getByRole('tab', { name: '网络延迟' }).click()
    await page.waitForTimeout(1000)
    await page.screenshot({ path: `${ARTIFACTS_DIR}/node-detail-guangxi-probes.png` })
    console.log('Saved node-detail-guangxi-probes.png')

    console.log('All probe UI verification tests passed successfully!')
  } finally {
    await browser.close()
  }
}

main().catch(console.error)
