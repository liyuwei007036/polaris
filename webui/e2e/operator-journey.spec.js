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
  await expect(page.getByText('首次使用默认用户名 sb_admin，默认密码 123456', { exact: true })).toHaveCount(0)
  await expect(page.getByPlaceholder('请输入用户名')).toHaveCount(0)
  await expect(page.getByPlaceholder('请输入密码')).toHaveCount(0)
  await page.getByLabel('用户名').fill(process.env.SB_CONTROL_E2E_ADMIN_USERNAME)
  await page.getByLabel('密码').fill(process.env.SB_CONTROL_E2E_INITIAL_PASSWORD)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByRole('heading', { name: '首次登录需要修改密码', exact: true })).toBeVisible()
  await page.getByPlaceholder('请输入当前密码').fill(process.env.SB_CONTROL_E2E_INITIAL_PASSWORD)
  await page.getByPlaceholder('至少 12 位，不能与当前密码相同').fill(process.env.SB_CONTROL_E2E_CHANGED_PASSWORD)
  await page.getByPlaceholder('请再次输入新密码').fill(process.env.SB_CONTROL_E2E_CHANGED_PASSWORD)
  await page.getByRole('button', { name: '保存新密码并进入平台', exact: true }).click()
  await expect(page.getByRole('heading', { name: '运行概览', exact: true })).toBeVisible()
  authenticated = true

  await page.getByRole('button', { name: '系统设置', exact: true }).click()
  await page.getByRole('button', { name: '启用两步验证', exact: true }).click()
  const setupDialog = page.getByRole('dialog', { name: '启用两步验证', exact: true })
  await expect(setupDialog.getByTestId('totp-qr')).toBeVisible()
  const secret = await setupDialog.getByRole('textbox', { name: '无法扫码时，可手动输入此密钥' }).inputValue()
  await setupDialog.getByPlaceholder('000000').fill(totp(secret, -1))
  await setupDialog.getByRole('button', { name: '确认启用', exact: true }).click()
  await expect(page.getByText('两步验证已启用，后续登录需要输入动态验证码', { exact: true })).toBeVisible()

  await page.locator('.operator-panel .el-button').click()
  authenticated = false
  await page.getByLabel('用户名').fill(process.env.SB_CONTROL_E2E_ADMIN_USERNAME)
  await page.getByLabel('密码').fill(process.env.SB_CONTROL_E2E_CHANGED_PASSWORD)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByRole('heading', { name: '两步验证', exact: true })).toBeVisible()
  await page.getByPlaceholder('000000').fill(totp(secret))
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await page.evaluate(() => { location.hash = '#/dashboard' })
  await expect(page.getByRole('heading', { name: '运行概览', exact: true })).toBeVisible()
  authenticated = true

  await expect(page.getByRole('button', { name: '端口共享', exact: true })).toHaveCount(0)
  await page.evaluate(() => { location.hash = '#/ingress-routes' })
  await expect(page.getByRole('heading', { name: '接入服务', exact: true })).toBeVisible()

  const pages = [
    ['服务器', '服务器'],
    ['接入服务', '接入服务'],
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
  await expect(dialog.getByText('端口共享', { exact: true })).toHaveCount(0)
  await expect(dialog.getByText('Reality 密钥和 Short ID 会在创建接入服务时自动生成，无需手动配置。', { exact: true })).toHaveCount(0)
  await expect(dialog.getByRole('button', { name: '添加用户', exact: true })).toBeVisible()
  await expect(dialog.getByRole('textbox', { name: '用户名称' })).toHaveCount(1)
  await expect(dialog.getByRole('textbox', { name: '客户端节点别名' })).toHaveCount(1)
  await dialog.getByRole('button', { name: '取消', exact: true }).click()
  await expect(dialog).toBeHidden()

  let savedListener
  let updatedAccount
  let createdAccount
  const listener = {
    id: 'listener-1', node_id: 'node-1', name: 'WebSocket 接入', listen_address: '0.0.0.0',
    port: 18444, backend_port: 18444, enabled: true, outbound_id: '',
    spec: {
      protocol: 'vless', network: 'tcp', tls: { enabled: false }, reality: { enabled: false },
      transport: { type: 'ws', path: '', host: '', service_name: '' },
    },
  }
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (request.method() === 'GET' && path === '/api/v1/nodes') return route.fulfill({ json: { nodes: [{ id: 'node-1', name: '测试服务器', client_address: 'test.example.com' }] } })
    if (request.method() === 'GET' && path === '/api/v1/nodes/node-1/metrics') return route.fulfill({ json: { report: null } })
    if (request.method() === 'GET' && path === '/api/v1/listeners') return route.fulfill({ json: { listeners: [listener] } })
    if (request.method() === 'GET' && path === '/api/v1/outbounds') return route.fulfill({ json: { outbounds: [] } })
    if (request.method() === 'GET' && path === '/api/v1/certificates') return route.fulfill({ json: { certificates: [] } })
    if (request.method() === 'GET' && path === '/api/v1/mihomo/client-configs') return route.fulfill({ json: { client_configs: [] } })
    if (request.method() === 'GET' && path === '/api/v1/listeners/listener-1/endpoints') {
      return route.fulfill({ json: { endpoints: [{ id: 'endpoint-1', listener_id: 'listener-1', name: '默认账号', alias: '测试节点 01', enabled: true, outbound_id: 'direct' }] } })
    }
    if (request.method() === 'PUT' && path === '/api/v1/listeners/listener-1') {
      savedListener = request.postDataJSON()
      return route.fulfill({ json: { ...listener, ...savedListener } })
    }
    if (request.method() === 'PUT' && path === '/api/v1/endpoints/endpoint-1') {
      updatedAccount = request.postDataJSON()
      return route.fulfill({ json: { id: 'endpoint-1', ...updatedAccount } })
    }
    if (request.method() === 'POST' && path === '/api/v1/listeners/listener-1/endpoints/quick') {
      createdAccount = request.postDataJSON()
      return route.fulfill({ status: 201, json: { id: 'endpoint-2', listener_id: 'listener-1', enabled: true, ...createdAccount } })
    }
    return route.continue()
  })
  await page.getByRole('button', { name: '服务器', exact: true }).click()
  await page.getByRole('button', { name: '接入服务', exact: true }).click()
  await expect(page.getByRole('button', { name: '添加用户', exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: '修改', exact: true }).click()
  const editDialog = page.getByRole('dialog', { name: '修改接入服务', exact: true })
  const accountNames = editDialog.getByRole('textbox', { name: '用户名称' })
  const accountAliases = editDialog.getByRole('textbox', { name: '客户端节点别名' })
  await expect(accountNames).toHaveValue('默认账号')
  await expect(accountAliases).toHaveValue('测试节点 01')
  await accountNames.fill('修改后的用户')
  await expect(editDialog.getByRole('button', { name: '添加用户', exact: true })).toBeVisible()
  await editDialog.getByRole('button', { name: '添加用户', exact: true }).click()
  await expect(accountNames).toHaveCount(2)
  await accountNames.nth(1).fill('新增用户')
  await accountAliases.nth(1).fill('测试节点 02')
  await editDialog.getByRole('button', { name: '高级设置：连接传输方式' }).click()
  await expect(editDialog.getByRole('textbox', { name: '请求路径' })).toHaveValue('')
  await editDialog.getByRole('button', { name: '保存并应用', exact: true }).click()
  await expect(editDialog).toBeHidden()
  expect(savedListener.spec.transport).toEqual({ type: 'ws', path: '', host: '', service_name: '' })
  expect(updatedAccount).toMatchObject({ name: '修改后的用户', alias: '测试节点 01', enabled: true, outbound_id: 'direct' })
  expect(createdAccount).toEqual({ name: '新增用户', alias: '测试节点 02', outbound_id: 'direct' })

  await page.getByRole('button', { name: '客户端配置', exact: true }).click()
  await expect(page.getByRole('button', { name: '新建客户端配置', exact: true })).toBeVisible()
  await page.setViewportSize({ width: 390, height: 844 })
  await expect.poll(async () => await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)

  expect(consoleErrors).toEqual([])
  expect(httpErrors).toEqual([])
})

function totp(secret, stepOffset = 0) {
  const key = decodeBase32(secret)
  const counter = Buffer.alloc(8)
  counter.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 30_000) + stepOffset))
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
