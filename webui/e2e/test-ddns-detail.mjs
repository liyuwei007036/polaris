import { chromium } from '../node_modules/@playwright/test/index.mjs'

const ARTIFACTS_DIR = 'C:/Users/dom/.gemini/antigravity/brain/d3c3836e-81e0-4ba0-a494-54b06d6f62f5'

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
    await page.goto('http://127.0.0.1:19670/#/nodes', { waitUntil: 'domcontentloaded' })
    const isLoginPage = (await page.locator('.login-form').count()) > 0
    if (isLoginPage) {
      await page.locator('.login-form input').first().fill('polaris_admin')
      await page.locator('input[type="password"]').first().fill('123456')
      await page.getByRole('button', { name: '登录', exact: true }).click()
      await page.waitForTimeout(1500)
    }

    // Enable DDNS on demo-node-lon-01 via correct API /api/v1/nodes/demo-node-lon-01
    const updateRes = await page.evaluate(async () => {
      const token = localStorage.getItem('polaris_csrf') || ''
      const res = await fetch('/api/v1/nodes/demo-node-lon-01', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': token },
        body: JSON.stringify({
          name: '🇬🇧 伦敦 OVH VPS',
          client_address: 'lon.polaris-node.net',
          ddns_enabled: true,
          ddns_domain: 'lon.polaris-node.net',
          ddns_domains: ['lon.polaris-node.net', 'uk.node.cloud'],
          ddns_record_ids: ['rec-demo-1', 'rec-demo-2'],
          provider: 'OVHcloud',
          provider_url: 'https://www.ovhcloud.com',
          price: '7.50',
          price_period: 'month',
          currency: 'USD',
          expires_on: '2026-11-30',
          bandwidth: '1 Gbps',
          traffic_plan: '1000 GB',
        }),
      })
      return { status: res.status, data: await res.json().catch(() => null) }
    })
    console.log('Update node result:', updateRes)

    // Set light mode
    await page.evaluate(() => {
      document.documentElement.classList.remove('dark')
      document.documentElement.classList.add('light')
      localStorage.setItem('polaris_theme', 'light')
    })
    await page.waitForTimeout(500)

    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.waitForTimeout(1200)

    // Capture cards showing DDNS badge on London OVH VPS
    await page.screenshot({ path: `${ARTIFACTS_DIR}/node-cards-ddns-light.png` })
    console.log('Saved node-cards-ddns-light.png')

    // Click London OVH VPS card to see DDNS in NodeDetail
    const lonCard = page.locator('.openlat-node-card').filter({ hasText: '伦敦 OVH VPS' }).first()
    await lonCard.click()
    await page.waitForTimeout(1000)

    // Scroll down to fact-grid
    await page.locator('.page-content').evaluate((el) => {
      el.scrollTop = el.scrollHeight
    })
    await page.waitForTimeout(600)

    const factGrid = page.locator('.fact-grid')
    if (await factGrid.count()) {
      await factGrid.screenshot({ path: `${ARTIFACTS_DIR}/node-detail-ddns-light.png` })
      console.log('Saved node-detail-ddns-light.png')
    }

    // Dark mode
    await page.evaluate(() => {
      document.documentElement.classList.remove('light')
      document.documentElement.classList.add('dark')
      localStorage.setItem('polaris_theme', 'dark')
    })
    await page.waitForTimeout(500)

    if (await factGrid.count()) {
      await factGrid.screenshot({ path: `${ARTIFACTS_DIR}/node-detail-ddns-dark.png` })
      console.log('Saved node-detail-ddns-dark.png')
    }

    // Switch to List View in Dark mode to see DDNS tag
    await page.getByRole('button', { name: '返回列表' }).click()
    await page.waitForTimeout(600)

    const listViewBtn = page.locator('button[title="列表视图"]')
    if (await listViewBtn.count()) {
      await listViewBtn.click()
      await page.waitForTimeout(600)
    }
    await page.screenshot({ path: `${ARTIFACTS_DIR}/node-rows-ddns-dark.png` })
    console.log('Saved node-rows-ddns-dark.png')

  } catch (err) {
    console.error('Error:', err)
  } finally {
    await browser.close()
  }
}

main()
