import { chromium } from 'playwright'
import fs from 'fs'
import path from 'path'

const artifactDir = 'C:/Users/dom/.gemini/antigravity/brain/d3c3836e-81e0-4ba0-a494-54b06d6f62f5'

async function run() {
  const browser = await chromium.launch({
    headless: true,
    executablePath: 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',
  })
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const page = await context.newPage()

  console.log('Navigating to login...')
  await page.goto('http://127.0.0.1:19670/', { waitUntil: 'domcontentloaded' })
  await page.waitForTimeout(1000)

  // Check if login needed
  const isLoginPage = (await page.locator('.login-form').count()) > 0
  if (isLoginPage) {
    console.log('Logging in...')
    await page.locator('.login-form input').first().fill('polaris_admin')
    await page.locator('input[type="password"]').first().fill('123456')
    await page.getByRole('button', { name: '登录', exact: true }).click()
    await page.waitForTimeout(1500)
  }

  // Helper to take screenshot
  async function snap(name, hashPath, waitMs = 1200) {
    if (hashPath) {
      await page.goto(`http://127.0.0.1:19670/#/${hashPath}`, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(waitMs)
    }
    const filePath = path.join(artifactDir, `${name}.png`)
    await page.screenshot({ path: filePath, fullPage: false })
    console.log(`Saved screenshot: ${name}.png`)
  }

  // 1. Nodes (服务器监控) - light mode
  await snap('page-nodes-light', 'nodes', 1500)

  // 2. Click London node to see NodeDetail (6 fact cards with icons, bandwidth, clean egress IP with flag)
  const lonCard = page.locator('.node-card:has-text("伦敦"), .node-row:has-text("伦敦")').first()
  if (await lonCard.isVisible()) {
    await lonCard.click()
    await page.waitForTimeout(1500)
    await snap('page-node-detail-light', null, 500)
  }

  // 3. Inbounds (接入服务)
  await snap('page-inbounds-light', 'inbounds', 1200)

  // 4. Proxy Groups (代理分组)
  await snap('page-proxy-groups-light', 'proxy-groups', 1200)

  // 5. Rule Providers (规则供应商)
  await snap('page-rule-providers-light', 'rule-providers', 1200)

  // 6. Subscriptions (客户端配置)
  await snap('page-subscriptions-light', 'subscriptions', 1200)

  // 7. Route Rules (服务器访问规则)
  await snap('page-routes-light', 'routes', 1200)

  // 8. Outbounds (上网出口)
  await snap('page-outbounds-light', 'outbounds', 1200)

  // 9. Connections (当前连接 - live streaming active connections)
  await snap('page-connections-light', 'connections', 1500)

  // 10. Devices (热门设备)
  await snap('page-devices-light', 'devices', 1200)

  // 11. Connection History (历史连接)
  await snap('page-connection-history-light', 'connection-history', 1200)

  // 12. Audit (操作记录)
  await snap('page-audit-light', 'audit', 1200)

  // 13. Dashboard (运行概览)
  await snap('page-dashboard-light', 'dashboard', 1500)

  // Now switch to dark mode and capture Node Monitoring and Node Detail in dark mode
  console.log('Switching to Dark mode...')
  await page.evaluate(() => {
    document.documentElement.classList.add('dark')
    localStorage.setItem('polaris_theme', 'dark')
  })
  await page.waitForTimeout(500)

  await snap('page-nodes-dark', 'nodes', 1200)

  // London node detail in dark mode
  const lonCardDark = page.locator('.node-card:has-text("伦敦"), .node-row:has-text("伦敦")').first()
  if (await lonCardDark.isVisible()) {
    await lonCardDark.click()
    await page.waitForTimeout(1200)
    await snap('page-node-detail-dark', null, 500)
  }

  await snap('page-connections-dark', 'connections', 1500)
  await snap('page-devices-dark', 'devices', 1200)
  await snap('page-inbounds-dark', 'inbounds', 1200)

  await browser.close()
  console.log('All screenshots captured successfully!')
}

run().catch((err) => {
  console.error('Test run failed:', err)
  process.exit(1)
})
