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

  await page.goto('http://127.0.0.1:19670/', { waitUntil: 'domcontentloaded' })
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
  await page.waitForTimeout(800)

  async function snap(name, hashPath, waitMs = 1200) {
    if (hashPath) {
      await page.goto(`http://127.0.0.1:19670/#/${hashPath}`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(waitMs)
    }
    const filePath = path.join(artifactDir, `${name}.png`)
    await page.screenshot({ path: filePath, fullPage: false })
    console.log(`Saved screenshot: ${name}.png`)
  }

  await snap('light-nodes', 'nodes', 1200)

  // Click London node to see detail in light mode
  const lonCard = page.locator('.openlat-node-card:has-text("伦敦")').first()
  if (await lonCard.isVisible()) {
    await lonCard.click()
    await page.waitForTimeout(1200)
    // Scroll down to fact cards so we capture the fact cards
    await page.evaluate(() => window.scrollBy(0, 500))
    await page.waitForTimeout(500)
    await snap('light-node-detail-facts', null, 500)
    await page.evaluate(() => window.scrollTo(0, 0))
    await snap('light-node-detail-top', null, 500)
  }

  // Add server dialog in light mode
  await page.locator('button:has-text("添加服务器")').first().click()
  await page.waitForTimeout(600)
  await snap('light-dialog-add-server', null, 500)
  await page.locator('.el-dialog__headerbtn, button:has-text("关闭")').first().click()

  // Other pages in light mode
  await snap('light-connections', 'connections', 1200)
  await snap('light-devices', 'devices', 1200)
  await snap('light-connection-history', 'connection-history', 1200)
  await snap('light-inbounds', 'inbounds', 1200)
  await snap('light-proxy-groups', 'proxy-groups', 1200)
  await snap('light-rule-providers', 'rule-providers', 1200)
  await snap('light-subscriptions', 'subscriptions', 1200)
  await snap('light-routes', 'routes', 1200)
  await snap('light-outbounds', 'outbounds', 1200)
  await snap('light-audit', 'audit', 1200)

  await browser.close()
  console.log('Light mode screenshots done!')
}

run().catch((err) => {
  console.error(err)
  process.exit(1)
})
