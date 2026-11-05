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

  // 1. Click London node in Light mode
  await page.goto('http://127.0.0.1:19670/#/nodes', { waitUntil: 'domcontentloaded' })
  await page.waitForTimeout(1500)

  const lonCard = page.locator('.openlat-node-card:has-text("伦敦")').first()
  if (await lonCard.isVisible()) {
    await lonCard.click()
    await page.waitForTimeout(1500)
    await page.screenshot({ path: path.join(artifactDir, 'page-node-detail-light.png') })
    console.log('Saved page-node-detail-light.png')
  }

  // Back to nodes
  const backBtn = page.locator('button:has-text("返回")').first()
  if (await backBtn.isVisible()) {
    await backBtn.click()
    await page.waitForTimeout(800)
  }

  // Open 添加服务器 dialog
  const addServerBtn = page.locator('button:has-text("添加服务器")').first()
  if (await addServerBtn.isVisible()) {
    await addServerBtn.click()
    await page.waitForTimeout(800)
    await page.screenshot({ path: path.join(artifactDir, 'dialog-add-server-light.png') })
    console.log('Saved dialog-add-server-light.png')

    // Close dialog
    await page.locator('.el-dialog__headerbtn, button:has-text("关闭")').first().click()
    await page.waitForTimeout(500)
  }

  // Switch to Dark Mode
  await page.evaluate(() => {
    document.documentElement.classList.remove('light')
    document.documentElement.classList.add('dark')
    localStorage.setItem('polaris_theme', 'dark')
  })
  await page.waitForTimeout(800)

  // London node in Dark mode
  const lonCardDark = page.locator('.openlat-node-card:has-text("伦敦")').first()
  if (await lonCardDark.isVisible()) {
    await lonCardDark.click()
    await page.waitForTimeout(1500)
    await page.screenshot({ path: path.join(artifactDir, 'page-node-detail-dark.png') })
    console.log('Saved page-node-detail-dark.png')
  }

  await browser.close()
  console.log('Finished capturing detail and dialog screenshots!')
}

run().catch((err) => {
  console.error(err)
  process.exit(1)
})
