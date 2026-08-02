import { createHmac } from 'node:crypto'
import { test, expect } from '@playwright/test'

test('管理员通过真实页面完成登录并检查全部核心工作区', async ({ page }) => {
  const consoleErrors = []
  const httpErrors = []
  let authenticated = false
  page.on('console', (message) => {
    if (message.type() === 'error' && !message.text().startsWith('Failed to load resource:')) consoleErrors.push(message.text())
  })
  page.on('pageerror', (error) => consoleErrors.push(error.message))
  page.on('response', (response) => {
    const expectedBootstrap401 = !authenticated && response.status() === 401 && response.url().endsWith('/api/v1/auth/me')
    if (response.status() >= 400 && !expectedBootstrap401) httpErrors.push(`${response.status()} ${response.url()}`)
  })

  await page.goto(process.env.SB_CONTROL_E2E_BASE_URL)
  await page.getByPlaceholder('admin@example.com').fill(process.env.SB_CONTROL_E2E_ADMIN_EMAIL)
  await page.getByPlaceholder('请输入密码').fill(process.env.SB_CONTROL_E2E_ADMIN_PASSWORD)
  await page.getByRole('button', { name: '继续', exact: true }).click()
  await expect(page.getByRole('heading', { name: '两步验证', exact: true })).toBeVisible()
  await page.getByPlaceholder('000000').fill(totp(process.env.SB_CONTROL_E2E_TOTP_SECRET))
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByRole('heading', { name: '运行概览', exact: true })).toBeVisible()
  authenticated = true

  const pages = [
    ['服务器', '服务器'],
    ['接入服务', '接入服务'],
    ['端口共享', '端口共享'],
    ['客户端节点组', '客户端节点组'],
    ['客户端访问规则', '客户端访问规则'],
    ['客户端配置', '客户端配置'],
    ['服务器访问规则', '服务器访问规则'],
    ['上网出口', '上网出口'],
    ['当前连接', '当前连接'],
    ['操作记录', '操作记录'],
    ['网络防护', '网络防护'],
    ['域名解析', '域名解析'],
    ['系统设置', '系统设置'],
  ]
  for (const [navigation, heading] of pages) {
    await page.getByRole('button', { name: navigation, exact: true }).click()
    await expect(page.getByRole('heading', { name: heading, exact: true })).toBeVisible()
  }

  await page.getByRole('button', { name: '当前连接', exact: true }).click()
  await expect(page.getByText('全部服务器', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '网络防护', exact: true }).click()
  await expect(page.getByText('全部服务器', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '操作记录', exact: true }).click()
  await expect(page.getByRole('tabpanel', { name: '系统操作', exact: true }).getByText('共 0 条', { exact: true })).toBeVisible()

  await page.getByRole('button', { name: '接入服务', exact: true }).click()
  await page.getByRole('button', { name: '新建接入服务', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: '新建接入服务', exact: true })
  await expect(dialog).toBeVisible()
  await expect(dialog.getByText('每个用户都会获得独立连接信息，并可使用不同的上网出口。', { exact: true })).toBeVisible()
  await dialog.getByRole('button', { name: '添加用户', exact: true }).click()
  await expect(dialog.getByRole('textbox', { name: '用户名称' })).toHaveCount(2)
  await dialog.getByRole('button', { name: '取消', exact: true }).click()
  await expect(dialog).toBeHidden()

  await page.getByRole('button', { name: '客户端配置', exact: true }).click()
  await expect(page.getByText('3 步完成', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '生成并下载配置', exact: true })).toBeDisabled()
  await page.setViewportSize({ width: 390, height: 844 })
  await expect.poll(async () => await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)

  expect(consoleErrors).toEqual([])
  expect(httpErrors).toEqual([])
})

function totp(secret) {
  const key = decodeBase32(secret)
  const counter = Buffer.alloc(8)
  counter.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 30_000)))
  const digest = createHmac('sha1', key).update(counter).digest()
  const offset = digest[digest.length - 1] & 0x0f
  const value = (digest.readUInt32BE(offset) & 0x7fffffff) % 1_000_000
  return String(value).padStart(6, '0')
}

function decodeBase32(value) {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'
  let bits = ''
  for (const character of value.trim().toUpperCase().replace(/=+$/, '')) {
    const index = alphabet.indexOf(character)
    if (index < 0) throw new Error('无效的两步验证密钥')
    bits += index.toString(2).padStart(5, '0')
  }
  const bytes = []
  for (let offset = 0; offset + 8 <= bits.length; offset += 8) {
    bytes.push(Number.parseInt(bits.slice(offset, offset + 8), 2))
  }
  return Buffer.from(bytes)
}
