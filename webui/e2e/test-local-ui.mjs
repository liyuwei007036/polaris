import { join } from 'node:path'
import { chromium } from '../node_modules/@playwright/test/index.mjs'

const ARTIFACTS_DIR = 'C:/Users/dom/.gemini/antigravity/brain/d3c3836e-81e0-4ba0-a494-54b06d6f62f5'
const TARGET_URL = 'http://127.0.0.1:19670/'

async function main() {
  console.log(`Connecting to ${TARGET_URL}...`)
  const browser = await chromium.launch({
    headless: true,
    executablePath: 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',
  })
  const context = await browser.newContext({
    viewport: { width: 1440, height: 960 },
    deviceScaleFactor: 2, // HiDPI crisp screenshots
  })
  const page = await context.newPage()

  page.on('console', (msg) => {
    if (msg.type() === 'error' || msg.text().includes('error')) {
      console.log('BROWSER CONSOLE:', msg.text())
    }
  })
  page.on('pageerror', (err) => console.log('BROWSER ERROR:', err.message))

  try {
    await page.goto(TARGET_URL, { waitUntil: 'domcontentloaded' })
    console.log('Initial page loaded:', page.url())

    // If on login page, authenticate
    const isLoginPage = (await page.locator('.login-form').count()) > 0
    if (isLoginPage) {
      console.log('Logging in as polaris_admin...')
      const userInput = page.locator('input[autocomplete="username"]')
      if (await userInput.count()) {
        await userInput.fill('polaris_admin')
      } else {
        await page.locator('.login-form input').first().fill('polaris_admin')
      }
      await page.locator('input[type="password"]').first().fill('123456')
      await page.getByRole('button', { name: '登录', exact: true }).click()
      await page.waitForTimeout(2000)

      // Check if password change is required
      const isPwdChange = (await page.locator('.password-card').count()) > 0
      if (isPwdChange) {
        console.log('Password change card encountered, updating...')
        const pwds = page.locator('.password-card input[type="password"]')
        await pwds.nth(0).fill('123456')
        await pwds.nth(1).fill('Polaris2026!Pass')
        await pwds.nth(2).fill('Polaris2026!Pass')
        await page.getByRole('button', { name: '保存', exact: true }).click()
        await page.waitForTimeout(2000)
      }
    }

    // Wait for the app layout
    await page.waitForSelector('.app-layout')
    console.log('Logged in successfully. App layout active.')

    // Ensure we are in Dark theme and Card view
    await page.evaluate(() => {
      document.documentElement.classList.remove('light')
      document.documentElement.classList.add('dark')
      localStorage.setItem('polaris_theme', 'dark')
      localStorage.setItem('polaris_nodes_view_mode', 'card')
      location.hash = '#/nodes'
    })
    await page.waitForTimeout(1500)

    console.log('Checking Nodes View in Dark Theme...')
    await page.waitForSelector('.summary-grid')
    await page.waitForSelector('.openlat-node-card')

    const nodeCount = await page.locator('.openlat-node-card').count()
    console.log(`Found ${nodeCount} uniform server cards in Dark Card View!`)

    // Capture Dark Card View
    await page.screenshot({ path: join(ARTIFACTS_DIR, 'card-view-dark.png'), fullPage: true })
    await page.screenshot({ path: join(ARTIFACTS_DIR, 'card-view.png'), fullPage: true })
    console.log('Saved card-view-dark.png')

    // Click on the first card to open NodeDetail
    console.log('Clicking the first server card to open NodeDetail...')
    await page.locator('.openlat-node-card').first().click()
    await page.waitForTimeout(1500)

    await page.waitForSelector('.node-detail')
    console.log('NodeDetail view loaded!')

    // Capture NodeDetail Resources tab in Dark Theme
    await page.screenshot({ path: join(ARTIFACTS_DIR, 'detail-resources-dark.png'), fullPage: true })
    await page.screenshot({ path: join(ARTIFACTS_DIR, 'detail-view-dark.png'), fullPage: true })
    console.log('Saved detail-resources-dark.png')

    // Switch to '网络延迟' tab in NodeDetail
    const latencyTabBtn = page.getByRole('button', { name: '网络延迟' })
    if (await latencyTabBtn.count()) {
      console.log('Switching to Latency tab in NodeDetail...')
      await latencyTabBtn.click()
      await page.waitForTimeout(1200)
      await page.screenshot({ path: join(ARTIFACTS_DIR, 'detail-latency-dark.png'), fullPage: true })
      console.log('Saved detail-latency-dark.png')
    }

    // Click '返回列表'
    console.log('Clicking back button to return to Nodes list...')
    await page.getByRole('button', { name: '返回列表' }).click()
    await page.waitForTimeout(1000)
    await page.waitForSelector('.openlat-node-card')

    // Switch to List View in Dark Theme
    console.log('Switching to List View...')
    const listBtn = page.locator('.view-btn[title="列表视图"]')
    if (await listBtn.count()) {
      await listBtn.click()
      await page.waitForTimeout(1000)
      await page.waitForSelector('.openlat-node-row')
      const rowCount = await page.locator('.openlat-node-row').count()
      console.log(`Found ${rowCount} server rows in List View!`)

      await page.screenshot({ path: join(ARTIFACTS_DIR, 'list-view-dark.png'), fullPage: true })
      await page.screenshot({ path: join(ARTIFACTS_DIR, 'list-view.png'), fullPage: true })
      console.log('Saved list-view-dark.png')
    }

    // Switch back to Card View before switching themes
    const cardBtn = page.locator('.view-btn[title="卡片视图"]')
    if (await cardBtn.count()) {
      await cardBtn.click()
      await page.waitForTimeout(800)
    }

    // ─────────────────────────────────────────────────────────────
    // Switch to Light Theme
    // ─────────────────────────────────────────────────────────────
    console.log('Switching to Light Theme...')
    await page.evaluate(() => {
      document.documentElement.classList.remove('dark')
      document.documentElement.classList.add('light')
      localStorage.setItem('polaris_theme', 'light')
    })
    await page.waitForTimeout(1000)

    // Capture Light Card View
    await page.screenshot({ path: join(ARTIFACTS_DIR, 'card-view-light.png'), fullPage: true })
    console.log('Saved card-view-light.png')

    // Click into NodeDetail in Light Theme
    console.log('Clicking into NodeDetail in Light Theme...')
    await page.locator('.openlat-node-card').first().click()
    await page.waitForTimeout(1500)
    await page.waitForSelector('.node-detail')
    await page.screenshot({ path: join(ARTIFACTS_DIR, 'detail-resources-light.png'), fullPage: true })
    await page.screenshot({ path: join(ARTIFACTS_DIR, 'detail-view-light.png'), fullPage: true })
    console.log('Saved detail-resources-light.png')

    // Click back button
    await page.getByRole('button', { name: '返回列表' }).click()
    await page.waitForTimeout(1000)

    // Switch to List View in Light Theme
    if (await listBtn.count()) {
      await listBtn.click()
      await page.waitForTimeout(1000)
      await page.waitForSelector('.openlat-node-row')
      await page.screenshot({ path: join(ARTIFACTS_DIR, 'list-view-light.png'), fullPage: true })
      console.log('Saved list-view-light.png')
    }

    // Check Dashboard in Light Theme
    console.log('Navigating to Dashboard in Light Theme...')
    await page.evaluate(() => {
      location.hash = '#/dashboard'
    })
    await page.waitForTimeout(1500)
    await page.screenshot({ path: join(ARTIFACTS_DIR, 'dashboard-light.png'), fullPage: true })
    console.log('Saved dashboard-light.png')

    console.log('All tests passed and screenshots captured successfully!')
  } finally {
    await browser.close()
  }
}

main().catch((err) => {
  console.error('Test failed:', err)
  process.exit(1)
})
