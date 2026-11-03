import { chromium } from '../node_modules/@playwright/test/index.mjs'

const TARGET_URL = 'http://127.0.0.1:19670/'

async function main() {
  const browser = await chromium.launch({
    headless: true,
    executablePath: 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',
  })
  const page = await browser.newPage()

  page.on('console', (msg) => console.log('PAGE LOG:', msg.text()))
  page.on('pageerror', (err) => console.log('PAGE ERROR:', err.message))
  page.on('response', async (res) => {
    if (res.url().includes('/api/v1/')) {
      console.log(`HTTP ${res.status()} ${res.url()}`)
      if (res.status() >= 400) {
        console.log(`ERROR BODY:`, await res.text().catch(() => ''))
      }
    }
  })

  await page.goto(TARGET_URL, { waitUntil: 'domcontentloaded' })
  await page.waitForTimeout(1000)

  // Login
  const userInput = page.locator('input[autocomplete="username"]')
  if (await userInput.count()) {
    await userInput.fill('polaris_admin')
    await page.locator('input[type="password"]').first().fill('123456')
    await page.getByRole('button', { name: '登录', exact: true }).click()
    await page.waitForTimeout(1500)
  }

  await page.waitForSelector('.app-layout')
  console.log('App layout visible, checking view...')

  // Navigate to #/nodes
  await page.goto(`${TARGET_URL}#/nodes`, { waitUntil: 'domcontentloaded' })
  await page.waitForTimeout(2000)

  const bodyHtml = await page.evaluate(() => document.querySelector('.page-content')?.innerHTML?.slice(0, 500) || document.body.innerHTML.slice(0, 500))
  console.log('Page content preview:', bodyHtml)

  const cardCount = await page.locator('.openlat-node-card').count()
  console.log('Card count:', cardCount)

  const rowCount = await page.locator('.openlat-node-row').count()
  console.log('Row count:', rowCount)

  const emptyCount = await page.locator('.el-empty').count()
  console.log('Empty count:', emptyCount)

  const viewModeVal = await page.evaluate(() => localStorage.getItem('polaris_nodes_view_mode'))
  console.log('localStorage viewMode:', viewModeVal)

  await browser.close()
}

main().catch(console.error)
