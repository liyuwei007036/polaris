import { chromium } from '../node_modules/@playwright/test/index.mjs'

const ARTIFACTS_DIR = 'C:/Users/dom/.gemini/antigravity/brain/d3c3836e-81e0-4ba0-a494-54b06d6f62f5'

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
    await page.goto('http://127.0.0.1:19670/', { waitUntil: 'domcontentloaded' })
    const isLoginPage = (await page.locator('.login-form').count()) > 0
    if (isLoginPage) {
      await page.locator('.login-form input').first().fill('polaris_admin')
      await page.locator('input[type="password"]').first().fill('123456')
      await page.getByRole('button', { name: '登录', exact: true }).click()
      await page.waitForTimeout(1500)
    }

    // Set Dark Mode
    await page.evaluate(() => {
      document.documentElement.classList.remove('light')
      document.documentElement.classList.add('dark')
      localStorage.setItem('polaris_theme', 'dark')
    })
    await page.waitForTimeout(500)

    // 1. Connection History (Dark)
    await page.goto('http://127.0.0.1:19670/#/connection-history', { waitUntil: 'domcontentloaded' })
    await page.waitForTimeout(1000)
    await page.screenshot({ path: `${ARTIFACTS_DIR}/fixed-connection-history-dark.png` })
    console.log('Saved fixed-connection-history-dark.png')

    // 2. Subscriptions Form (Dark)
    await page.goto('http://127.0.0.1:19670/#/subscriptions', { waitUntil: 'domcontentloaded' })
    await page.waitForTimeout(1000)
    const newSubBtn = page.getByRole('button', { name: '新建' }).first()
    if (await newSubBtn.count()) {
      await newSubBtn.click()
      await page.waitForTimeout(800)
    }
    await page.screenshot({ path: `${ARTIFACTS_DIR}/fixed-subscription-form-dark.png` })
    console.log('Saved fixed-subscription-form-dark.png')

    // 3. Nodes -> Token Dialog (Dark)
    await page.goto('http://127.0.0.1:19670/#/nodes', { waitUntil: 'domcontentloaded' })
    await page.waitForTimeout(1000)
    const addNodeBtn = page.getByRole('button', { name: '接入服务器' })
    if (await addNodeBtn.count()) {
      await addNodeBtn.click()
      await page.waitForTimeout(800)
    }
    await page.screenshot({ path: `${ARTIFACTS_DIR}/fixed-token-dialog-dark.png` })
    console.log('Saved fixed-token-dialog-dark.png')

  } catch (err) {
    console.error('Test error:', err)
  } finally {
    await browser.close()
  }
}

main()
