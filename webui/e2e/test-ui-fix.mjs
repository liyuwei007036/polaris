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

    // Ensure Light mode
    await page.evaluate(() => {
      document.documentElement.classList.remove('dark')
      document.documentElement.classList.add('light')
      localStorage.setItem('polaris_theme', 'light')
    })
    await page.waitForTimeout(500)

    // 1. Subscriptions form
    await page.goto('http://127.0.0.1:19670/#/subscriptions', { waitUntil: 'domcontentloaded' })
    await page.waitForTimeout(1000)
    const newSubBtn = page.getByRole('button', { name: '新建' }).first()
    if (await newSubBtn.count()) {
      await newSubBtn.click()
      await page.waitForTimeout(800)
    }
    await page.screenshot({ path: `${ARTIFACTS_DIR}/fixed-subscription-form-light.png` })
    console.log('Saved fixed-subscription-form-light.png')

    // 2. Connection History
    await page.goto('http://127.0.0.1:19670/#/connection-history', { waitUntil: 'domcontentloaded' })
    await page.waitForTimeout(1000)
    await page.screenshot({ path: `${ARTIFACTS_DIR}/fixed-connection-history-light.png` })
    console.log('Saved fixed-connection-history-light.png')

    // 3. Nodes -> 接入服务器
    await page.goto('http://127.0.0.1:19670/#/nodes', { waitUntil: 'domcontentloaded' })
    await page.waitForTimeout(1000)
    const addNodeBtn = page.getByRole('button', { name: '接入服务器' })
    if (await addNodeBtn.count()) {
      await addNodeBtn.click()
      await page.waitForTimeout(800)
    }
    await page.screenshot({ path: `${ARTIFACTS_DIR}/fixed-token-dialog-light.png` })
    console.log('Saved fixed-token-dialog-light.png')

    // Close dialog
    const closeBtn = page.getByRole('button', { name: '关闭', exact: true })
    if (await closeBtn.count()) {
      await closeBtn.click()
      await page.waitForTimeout(500)
    }

    const configBtn = page.getByRole('button', { name: '网络测速配置' })
    if (await configBtn.count()) {
      await configBtn.click()
      await page.waitForTimeout(800)
    }
    await page.screenshot({ path: `${ARTIFACTS_DIR}/fixed-probe-manager-light.png` })
    console.log('Saved fixed-probe-manager-light.png')

    // Close config dialog
    await page.keyboard.press('Escape')
    await page.waitForTimeout(500)

    // Now test Dark Mode
    await page.evaluate(() => {
      document.documentElement.classList.remove('light')
      document.documentElement.classList.add('dark')
      localStorage.setItem('polaris_theme', 'dark')
    })
    await page.waitForTimeout(500)

    // Nodes in dark mode -> 接入服务器
    const addNodeBtnDark = page.getByRole('button', { name: '接入服务器' })
    if (await addNodeBtnDark.count()) {
      await addNodeBtnDark.click()
      await page.waitForTimeout(800)
    }
    await page.screenshot({ path: `${ARTIFACTS_DIR}/fixed-token-dialog-dark.png` })
    console.log('Saved fixed-token-dialog-dark.png')

    await page.goto('http://127.0.0.1:19670/#/connection-history', { waitUntil: 'domcontentloaded' })
    await page.waitForTimeout(1000)
    await page.screenshot({ path: `${ARTIFACTS_DIR}/fixed-connection-history-dark.png` })
    console.log('Saved fixed-connection-history-dark.png')

    await page.goto('http://127.0.0.1:19670/#/subscriptions', { waitUntil: 'domcontentloaded' })
    await page.waitForTimeout(1000)
    const newSubBtnDark = page.getByRole('button', { name: '新建' }).first()
    if (await newSubBtnDark.count()) {
      await newSubBtnDark.click()
      await page.waitForTimeout(800)
    }
    await page.screenshot({ path: `${ARTIFACTS_DIR}/fixed-subscription-form-dark.png` })
    console.log('Saved fixed-subscription-form-dark.png')

  } catch (err) {
    console.error('Test error:', err)
  } finally {
    await browser.close()
  }
}

main()
