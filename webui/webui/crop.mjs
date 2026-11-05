import { chromium } from 'playwright'
const browser = await chromium.launch({ channel: 'msedge' })
const page = await browser.newPage({ viewport: { width: 1000, height: 700 }, deviceScaleFactor: 2 })
await page.goto('http://127.0.0.1:5199/maptest.html')
await page.waitForSelector('.place-pin-group')
await page.waitForTimeout(500)
await page.locator('.map-canvas-wrap').screenshot({ path: 'C:/Users/dom/AppData/Local/Temp/opencode/nodemap-map-crop.png' })
await browser.close()
