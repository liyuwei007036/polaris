import { chromium } from 'playwright'
import path from 'path'

const artifactDir = 'C:/Users/dom/.gemini/antigravity/brain/d3c3836e-81e0-4ba0-a494-54b06d6f62f5'

async function run() {
  const browser = await chromium.launch({
    headless: true,
    executablePath: 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',
  })
  const context = await browser.newContext({ viewport: { width: 1440, height: 960 }, deviceScaleFactor: 2 })
  const page = await context.newPage()

  await page.goto('http://127.0.0.1:19670/#/nodes', { waitUntil: 'domcontentloaded' })
  await page.waitForTimeout(1000)

  const isLoginPage = (await page.locator('.login-form').count()) > 0
  if (isLoginPage) {
    await page.locator('.login-form input').first().fill('polaris_admin')
    await page.locator('input[type="password"]').first().fill('123456')
    await page.getByRole('button', { name: '登录', exact: true }).click()
    await page.waitForTimeout(1500)
  }

  // Force LIGHT mode
  await page.evaluate(() => {
    document.documentElement.classList.remove('dark')
    document.documentElement.classList.add('light')
    localStorage.setItem('polaris_theme', 'light')
  })
  await page.waitForTimeout(500)

  // Click London node
  const lonCard = page.locator('.openlat-node-card:has-text("伦敦")').first()
  if (await lonCard.isVisible()) {
    await lonCard.click()
    await page.waitForTimeout(1500)
    // Scroll fact cards into view
    const factGrid = page.locator('.fact-grid').first()
    await factGrid.scrollIntoViewIfNeeded()
    await page.waitForTimeout(800)
    await page.screenshot({ path: path.join(artifactDir, 'light-node-detail-all-facts.png') })
    console.log('Saved light-node-detail-all-facts.png')
  }

  // Also capture in DARK mode
  await page.evaluate(() => {
    document.documentElement.classList.remove('light')
    document.documentElement.classList.add('dark')
    localStorage.setItem('polaris_theme', 'dark')
  })
  await page.waitForTimeout(800)
  const factGridDark = page.locator('.fact-grid').first()
  await factGridDark.scrollIntoViewIfNeeded()
  await page.waitForTimeout(800)
  await page.screenshot({ path: path.join(artifactDir, 'dark-node-detail-all-facts.png') })
  console.log('Saved dark-node-detail-all-facts.png')

  await browser.close()
}

run().catch(console.error)
