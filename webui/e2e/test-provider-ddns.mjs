import { chromium } from '../node_modules/@playwright/test/index.mjs'

const ARTIFACTS_DIR = 'C:/Users/dom/.gemini/antigravity/brain/d3c3836e-81e0-4ba0-a494-54b06d6f62f5'
const TARGET_URL = 'http://127.0.0.1:19670/'

async function main() {
  const browser = await chromium.launch({
    headless: true,
    executablePath: 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',
  })
  const context = await browser.newContext({
    viewport: { width: 1440, height: 1000 },
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

    // ==========================================
    // 1. LIGHT THEME
    // ==========================================
    await page.evaluate(() => {
      document.documentElement.classList.remove('dark')
      document.documentElement.classList.add('light')
      localStorage.setItem('polaris_theme', 'light')
    })
    await page.waitForTimeout(500)

    // A. Node Cards (Light)
    await page.goto('http://127.0.0.1:19670/#/nodes', { waitUntil: 'domcontentloaded' })
    await page.waitForTimeout(1200)
    await page.screenshot({ path: `${ARTIFACTS_DIR}/node-cards-provider-light.png` })
    console.log('Saved node-cards-provider-light.png')

    // B. Open NodeDetail (Click Hetzner card)
    const card = page.locator('.openlat-node-card').first()
    await card.click()
    await page.waitForTimeout(1000)

    // Scroll down to show fact-grid completely
    await page.locator('.page-content').evaluate((el) => {
      el.scrollTop = el.scrollHeight
    })
    await page.waitForTimeout(600)
    await page.screenshot({ path: `${ARTIFACTS_DIR}/node-detail-facts-light.png` })
    console.log('Saved node-detail-facts-light.png')

    // Element screenshot of fact-grid (Light)
    const factGrid = page.locator('.fact-grid')
    if (await factGrid.count()) {
      await factGrid.screenshot({ path: `${ARTIFACTS_DIR}/node-detail-factgrid-light.png` })
      console.log('Saved node-detail-factgrid-light.png')
    }

    // Return to list
    await page.getByRole('button', { name: '返回列表' }).click()
    await page.waitForTimeout(600)

    // C. List View (Light)
    const listViewBtn = page.locator('button[title="列表视图"]')
    if (await listViewBtn.count()) {
      await listViewBtn.click()
      await page.waitForTimeout(600)
    }
    await page.screenshot({ path: `${ARTIFACTS_DIR}/node-rows-provider-light.png` })
    console.log('Saved node-rows-provider-light.png')

    // ==========================================
    // 2. DARK THEME
    // ==========================================
    await page.evaluate(() => {
      document.documentElement.classList.remove('light')
      document.documentElement.classList.add('dark')
      localStorage.setItem('polaris_theme', 'dark')
    })
    await page.waitForTimeout(500)

    // D. List View (Dark)
    await page.screenshot({ path: `${ARTIFACTS_DIR}/node-rows-provider-dark.png` })
    console.log('Saved node-rows-provider-dark.png')

    // E. Switch back to Card View (Dark)
    const cardViewBtn = page.locator('button[title="卡片视图"]')
    if (await cardViewBtn.count()) {
      await cardViewBtn.click()
      await page.waitForTimeout(600)
    }
    await page.screenshot({ path: `${ARTIFACTS_DIR}/node-cards-provider-dark.png` })
    console.log('Saved node-cards-provider-dark.png')

    // F. Open NodeDetail (Dark)
    const darkCard = page.locator('.openlat-node-card').first()
    await darkCard.click()
    await page.waitForTimeout(1000)

    // Scroll down to show fact-grid completely
    await page.locator('.page-content').evaluate((el) => {
      el.scrollTop = el.scrollHeight
    })
    await page.waitForTimeout(600)
    await page.screenshot({ path: `${ARTIFACTS_DIR}/node-detail-facts-dark.png` })
    console.log('Saved node-detail-facts-dark.png')

    // Element screenshot of fact-grid (Dark)
    const darkFactGrid = page.locator('.fact-grid')
    if (await darkFactGrid.count()) {
      await darkFactGrid.screenshot({ path: `${ARTIFACTS_DIR}/node-detail-factgrid-dark.png` })
      console.log('Saved node-detail-factgrid-dark.png')
    }

  } catch (err) {
    console.error('Test execution failed:', err)
  } finally {
    await browser.close()
  }
}

main()
