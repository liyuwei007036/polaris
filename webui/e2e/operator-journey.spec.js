import { createHmac } from 'node:crypto'
import { test, expect } from '@playwright/test'

test('浏览器页面回归：真实登录，核心工作区 API 使用路由替身', async ({ page }) => {
  const consoleErrors = []
  const httpErrors = []
  let authenticated = false
  await page.addInitScript(() => {
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined })
    document.execCommand = (command) => {
      if (command !== 'copy') return false
      window.__copiedText = document.activeElement?.value || ''
      return true
    }
  })
  page.on('console', (message) => {
    if (message.type() === 'error' && !message.text().startsWith('Failed to load resource:')) consoleErrors.push(message.text())
  })
  page.on('pageerror', (error) => consoleErrors.push(error.message))
  page.on('response', (response) => {
    const expectedBootstrap401 = !authenticated && response.status() === 401 && response.url().endsWith('/api/v1/auth/me')
    if (response.status() >= 400 && !expectedBootstrap401) httpErrors.push(`${response.status()} ${response.url()}`)
  })

  await page.goto(process.env.POLARIS_E2E_BASE_URL)
  await expect(page.getByText('首次使用默认用户名 polaris_admin，默认密码 123456', { exact: true })).toHaveCount(0)
  await expect(page.getByPlaceholder('请输入用户名')).toHaveCount(0)
  await expect(page.getByPlaceholder('请输入密码')).toHaveCount(0)
  await page.getByLabel('用户名').fill(process.env.POLARIS_E2E_ADMIN_USERNAME)
  await page.getByLabel('密码').fill(process.env.POLARIS_E2E_INITIAL_PASSWORD)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByRole('heading', { name: '首次登录需要修改密码', exact: true })).toBeVisible()
  await page.getByPlaceholder('请输入当前密码').fill(process.env.POLARIS_E2E_INITIAL_PASSWORD)
  await page.getByPlaceholder('至少 12 位，不能与当前密码相同').fill(process.env.POLARIS_E2E_CHANGED_PASSWORD)
  await page.getByPlaceholder('请再次输入新密码').fill(process.env.POLARIS_E2E_CHANGED_PASSWORD)
  await page.getByRole('button', { name: '保存', exact: true }).click()
  await expect(page.getByRole('heading', { name: '运行概览', exact: true })).toBeVisible()
  authenticated = true

  await page.getByRole('button', { name: '系统设置', exact: true }).click()
  await page.getByRole('button', { name: '启用', exact: true }).click()
  const setupDialog = page.getByRole('dialog', { name: '启用两步验证', exact: true })
  await expect(setupDialog.getByTestId('totp-qr')).toBeVisible()
  const secret = await setupDialog.getByRole('textbox', { name: '无法扫码时，可手动输入此密钥' }).inputValue()
  await setupDialog.getByPlaceholder('000000').fill(totp(secret, -1))
  await setupDialog.getByRole('button', { name: '启用', exact: true }).click()
  await expect(page.getByText('两步验证已启用，后续登录需要输入动态验证码', { exact: true })).toBeVisible()

  await page.locator('.operator-panel .el-button').click()
  authenticated = false
  await page.getByLabel('用户名').fill(process.env.POLARIS_E2E_ADMIN_USERNAME)
  await page.getByLabel('密码').fill(process.env.POLARIS_E2E_CHANGED_PASSWORD)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByRole('heading', { name: '两步验证', exact: true })).toBeVisible()
  await page.getByPlaceholder('000000').fill(totp(secret))
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await page.evaluate(() => { location.hash = '#/dashboard' })
  await expect(page.getByRole('heading', { name: '运行概览', exact: true })).toBeVisible()
  for (const label of ['服务器总数', '在线服务器', '活动连接', '节点数量']) await expect(page.getByText(label, { exact: true })).toBeVisible()
  await expect(page.locator('.chart-panel canvas')).toHaveCount(5)
  authenticated = true

  await expect(page.getByRole('button', { name: '端口共享', exact: true })).toHaveCount(0)
  await page.evaluate(() => { location.hash = '#/ingress-routes' })
  await expect(page.getByRole('heading', { name: '接入服务', exact: true })).toBeVisible()

  const pages = [
    ['服务器', '服务器'],
    ['接入服务', '接入服务'],
    ['代理分组', '代理分组'],
    ['规则供应商', '规则供应商'],
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
    // The page we navigated away from has to actually go. A transition that
    // never finishes leaves the old page stacked on top of the new one, which
    // looks like navigation silently doing nothing.
    await expect(page.locator('.page-shell')).toHaveCount(1)
    // Lists paginate, so the pager must be on screen rather than pushed off
    // the bottom by a tall page of rows.
    const pager = page.locator('.pagination-bar').first()
    if (await pager.count()) {
      const geometry = await page.evaluate(() => {
        const bar = document.querySelector('.pagination-bar')
        const content = document.querySelector('.page-content')
        return {
          pagerBottom: Math.round(bar.getBoundingClientRect().bottom),
          viewport: window.innerHeight,
          contentOverflow: content ? content.scrollHeight - content.clientHeight : null,
        }
      })
      expect(geometry, `${heading}: ${JSON.stringify(geometry)}`).toMatchObject({ contentOverflow: 0 })
      expect(geometry.pagerBottom, `${heading} pager off screen: ${JSON.stringify(geometry)}`).toBeLessThanOrEqual(geometry.viewport)
    }
  }

  // Scope these to the page body: Element Plus teleports every select's
  // dropdown to <body>, so a bare text match also finds panels belonging to
  // pages that are no longer on screen.
  await page.getByRole('button', { name: '当前连接', exact: true }).click()
  await expect(page.locator('.page-shell')).toHaveCount(1)
  await expect(page.locator('.page-shell').getByText('全部服务器', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '网络防护', exact: true }).click()
  await expect(page.locator('.page-shell')).toHaveCount(1)
  await expect(page.locator('.page-shell').getByText('全部服务器', { exact: true })).toBeVisible()
  await expect(page.getByText('来源地址范围 / IP 归属地', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '操作记录', exact: true }).click()
  await expect(page.getByRole('tabpanel', { name: '系统操作', exact: true }).getByText('共 0 条', { exact: true })).toBeVisible()

  await page.getByRole('button', { name: '接入服务', exact: true }).click()
  await expect(page.getByRole('heading', { name: '接入服务', exact: true })).toBeVisible()
  await page.getByRole('button', { name: '新建', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: '新建接入服务', exact: true })
  await expect(dialog).toBeVisible()
  await expect(dialog.getByText('端口共享', { exact: true })).toHaveCount(0)
  await expect(dialog.getByText('Reality 密钥和 Short ID 会在创建接入服务时自动生成，无需手动配置。', { exact: true })).toBeVisible()
  await expect(dialog.getByRole('button', { name: '添加', exact: true })).toBeVisible()
  await expect(dialog.getByRole('textbox', { name: '用户名称' })).toHaveCount(1)
  await expect(dialog.getByRole('textbox', { name: '用户名称' })).toHaveValue(/^user_[0-9a-f]{8}$/)
  await expect(dialog.getByRole('textbox', { name: '客户端节点别名' })).toHaveCount(1)
  await page.locator('.el-overlay').filter({ has: dialog }).click({ position: { x: 5, y: 5 } })
  await expect(dialog).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(dialog).toBeVisible()
  await dialog.locator('.protocol-select .el-select').click()
  await page.locator('.el-select-dropdown__item').filter({ hasText: 'VLESS + WebSocket' }).click()
  await expect(dialog.getByRole('textbox', { name: '请求路径' })).toHaveValue(/^\/[0-9a-f]{24}$/)
  await expect(dialog.getByText('TLS 加密（自动证书）', { exact: true })).toBeVisible()
  await dialog.getByRole('button', { name: '取消', exact: true }).click()
  await expect(dialog).toBeHidden()

  let savedListener
  let updatedAccount
  let createdAccount
  let quickListenerPayload
  let savedClientConfig
  let savedDNSRecord
  let adoptedDNSRecord
  // The DNS page must tell "saved" apart from "reachable", so the connection
  // state is switched between the two assertions below.
  let cloudflareConnected = false
  const managedDNSRecord = {
    id: 'record-1', name: 'ws.example.com', type: 'A', content: '203.0.113.10', ttl: 1,
    proxied: false, node_id: 'node-1', status: 'synced', node_name: '测试服务器',
  }
  const remoteDNSRecords = [
    { id: 'remote-1', name: 'cdn.example.com', type: 'CNAME', content: 'ws.example.com', ttl: 1, proxied: true },
    { id: 'remote-2', name: 'other.example.com', type: 'A', content: '198.51.100.7', ttl: 300, proxied: false },
  ]
  let proxyGroupSequence = 0
  let rejectFirstProxyGroup = true
  const savedProxyGroups = []
  const savedRuleProviders = []
  const listener = {
    id: 'listener-1', node_id: 'node-1', name: 'WebSocket 接入', connection_domain: 'ws.example.com', listen_address: '0.0.0.0',
    port: 18444, backend_port: 18444, enabled: true, outbound_id: '',
    spec: {
      protocol: 'vless', network: 'tcp', tls: { enabled: true, alpn: ['http/1.1'] }, reality: { enabled: false },
      transport: { type: 'ws', path: '', host: '', service_name: '' },
    },
  }
  // This section verifies browser interaction contracts, not a live Agent.
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (request.method() === 'GET' && path === '/api/v1/nodes') return route.fulfill({ json: { nodes: [{ id: 'node-1', name: '测试服务器', client_address: '203.0.113.10' }] } })
    if (request.method() === 'GET' && path === '/api/v1/nodes/node-1/metrics') return route.fulfill({ json: { report: null } })
    if (request.method() === 'GET' && path === '/api/v1/listeners') return route.fulfill({ json: { listeners: [listener] } })
    if (request.method() === 'GET' && path === '/api/v1/outbounds') return route.fulfill({ json: { outbounds: [] } })
    if (request.method() === 'GET' && path === '/api/v1/cloudflare/settings') {
      return route.fulfill({ json: cloudflareConnected
        ? { configured: true, connected: true, zone_id: 'zone1', zone_name: 'example.com', token_masked: 'test…7890' }
        : { configured: true, connected: false, error: 'Cloudflare 接口返回错误：Invalid API Token', zone_id: 'zone1', zone_name: 'example.com' } })
    }
    if (request.method() === 'GET' && path === '/api/v1/cloudflare/records') return route.fulfill({ json: { records: cloudflareConnected ? [managedDNSRecord] : [] } })
    if (request.method() === 'GET' && path === '/api/v1/cloudflare/remote-records') return route.fulfill({ json: { records: cloudflareConnected ? remoteDNSRecords : [] } })
    if (request.method() === 'GET' && path === '/api/v1/cloudflare/origin-certificates') return route.fulfill({ json: { certificates: [] } })
    if (request.method() === 'PUT' && path === '/api/v1/cloudflare/settings') {
      return route.fulfill({ status: 400, json: { error: 'Cloudflare 接口返回错误：Invalid API Token' } })
    }
    if (request.method() === 'PUT' && path === '/api/v1/cloudflare/records/record-1') {
      savedDNSRecord = request.postDataJSON()
      return route.fulfill({ json: { ...managedDNSRecord, ...savedDNSRecord } })
    }
    if (request.method() === 'POST' && path === '/api/v1/cloudflare/records') {
      adoptedDNSRecord = request.postDataJSON()
      return route.fulfill({ status: 201, json: { id: 'record-2', status: 'pending', ...adoptedDNSRecord } })
    }
    if (request.method() === 'GET' && path === '/api/v1/mihomo/proxy-groups') return route.fulfill({ json: { proxy_groups: savedProxyGroups } })
    if (request.method() === 'GET' && path === '/api/v1/mihomo/rule-providers') return route.fulfill({ json: { rule_providers: savedRuleProviders } })
    if (request.method() === 'POST' && path === '/api/v1/mihomo/rule-providers') {
      const provider = { id: `provider-${savedRuleProviders.length + 1}`, ...request.postDataJSON() }
      savedRuleProviders.push(provider)
      return route.fulfill({ status: 201, json: provider })
    }
    if (request.method() === 'GET' && path === '/api/v1/mihomo/client-configs') return route.fulfill({ json: { client_configs: savedClientConfig ? [savedClientConfig] : [] } })
    if (request.method() === 'GET' && (path === '/api/v1/listeners/listener-1/endpoints' || path === '/api/v1/endpoints')) {
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
    if (request.method() === 'POST' && path === '/api/v1/listeners/quick') {
      quickListenerPayload = request.postDataJSON()
      return route.fulfill({ status: 400, json: { error: '测试校验错误：请检查接入服务参数' } })
    }
    if (request.method() === 'POST' && path === '/api/v1/listeners/listener-1/endpoints/quick') {
      createdAccount = request.postDataJSON()
      return route.fulfill({ status: 201, json: { id: 'endpoint-2', listener_id: 'listener-1', enabled: true, ...createdAccount } })
    }
    if (request.method() === 'POST' && path === '/api/v1/mihomo/proxy-groups') {
      const payload = request.postDataJSON()
      if (rejectFirstProxyGroup) {
        rejectFirstProxyGroup = false
        return route.fulfill({ status: 400, json: { error: '测试代理分组校验错误' } })
      }
      proxyGroupSequence += 1
      const group = { id: `group-${proxyGroupSequence}`, ...payload }
      savedProxyGroups.push(group)
      return route.fulfill({ status: 201, json: group })
    }
    if (request.method() === 'POST' && path === '/api/v1/mihomo/client-configs') {
      savedClientConfig = { id: 'client-1', subscription_path: '/api/v1/mihomo/subscriptions/test-token', ...request.postDataJSON(), enabled: true }
      return route.fulfill({ status: 201, json: savedClientConfig })
    }
    if (request.method() === 'POST' && path === '/api/v1/mihomo/client-configs/client-1/enabled') {
      savedClientConfig.enabled = request.postDataJSON().enabled
      return route.fulfill({ json: { enabled: savedClientConfig.enabled } })
    }
    return route.continue()
  })
  await page.getByRole('button', { name: '服务器', exact: true }).click()
  await page.getByRole('button', { name: '接入服务', exact: true }).click()
  await expect(page.getByRole('heading', { name: '接入服务', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '添加', exact: true })).toHaveCount(0)

  await page.getByRole('button', { name: '新建', exact: true }).click()
  const createDialog = page.getByRole('dialog', { name: '新建接入服务', exact: true })
  await createDialog.getByRole('textbox', { name: '连接域名' }).fill('reality.example.com')
  await createDialog.getByRole('textbox', { name: '客户端节点别名' }).fill('测试节点 01')
  await createDialog.getByRole('button', { name: '创建', exact: true }).click()
  await expect(page.getByText('测试校验错误：请检查接入服务参数', { exact: true })).toBeVisible()
  await expect(createDialog).toBeVisible()
  expect(quickListenerPayload.accounts).toHaveLength(1)
  expect(quickListenerPayload.listener.connection_domain).toBe('reality.example.com')
  expect(quickListenerPayload.accounts[0].name).toMatch(/^user_[0-9a-f]{8}$/)
  expect(quickListenerPayload.accounts[0]).toMatchObject({ alias: '测试节点 01', enabled: true, outbound_id: 'direct' })
  await createDialog.getByRole('button', { name: '取消', exact: true }).click()

  await page.getByRole('button', { name: '编辑', exact: true }).click()
  const editDialog = page.getByRole('dialog', { name: '修改接入服务', exact: true })
  const accountNames = editDialog.getByRole('textbox', { name: '用户名称' })
  const accountAliases = editDialog.getByRole('textbox', { name: '客户端节点别名' })
  await expect(accountNames).toHaveValue('默认账号')
  await expect(accountAliases).toHaveValue('测试节点 01')
  await expect(editDialog.getByRole('textbox', { name: '连接域名' })).toHaveValue('ws.example.com')
  await accountNames.fill('修改后的用户')
  await expect(editDialog.getByRole('button', { name: '添加', exact: true })).toBeVisible()
  await editDialog.getByRole('button', { name: '添加', exact: true }).click()
  await expect(accountNames).toHaveCount(2)
  await accountNames.nth(1).fill('新增用户')
  await accountAliases.nth(1).fill('测试节点 02')
  await expect(editDialog.getByRole('textbox', { name: '请求路径' })).toHaveValue('')
  await editDialog.getByRole('button', { name: '保存', exact: true }).click()
  await expect(editDialog).toBeHidden()
  expect(savedListener.spec.transport).toEqual({ type: 'ws', path: '', host: '', service_name: '' })
  expect(savedListener.spec.tls).toMatchObject({ enabled: true, alpn: ['http/1.1'] })
  expect(updatedAccount).toMatchObject({ name: '修改后的用户', alias: '测试节点 01', enabled: true, outbound_id: 'direct' })
  expect(createdAccount).toEqual({ name: '新增用户', alias: '测试节点 02', outbound_id: 'direct' })

  // Copying an access service only fills in the create form: the operator
  // still chooses the target server and saves.
  await page.getByRole('button', { name: '复制', exact: true }).click()
  const copyDialog = page.getByRole('dialog', { name: '复制接入服务', exact: true })
  await expect(copyDialog.getByRole('textbox', { name: '服务名称' })).toHaveValue('WebSocket 接入 副本')
  await expect(copyDialog.getByRole('textbox', { name: '连接域名' })).toHaveValue('ws.example.com')
  await expect(copyDialog.getByRole('textbox', { name: '用户名称' })).toHaveValue('默认账号')
  await expect(copyDialog.getByRole('textbox', { name: '客户端节点别名' })).toHaveValue('测试节点 01 副本')
  await copyDialog.getByRole('button', { name: '创建', exact: true }).click()
  await expect(page.getByText('测试校验错误：请检查接入服务参数', { exact: true })).toBeVisible()
  expect(quickListenerPayload.listener).toMatchObject({ name: 'WebSocket 接入 副本', connection_domain: 'ws.example.com', port: 18444 })
  expect(quickListenerPayload.listener.spec.transport).toMatchObject({ type: 'ws' })
  expect(quickListenerPayload.accounts[0]).toMatchObject({ name: '默认账号', alias: '测试节点 01 副本' })
  await copyDialog.getByRole('button', { name: '取消', exact: true }).click()

  await page.getByRole('button', { name: '代理分组', exact: true }).click()
  // Wait for the page itself before pressing a button that exists on several
  // pages, otherwise the click can still land on the one being left behind.
  await expect(page.getByRole('heading', { name: '代理分组', exact: true })).toBeVisible()
  await page.getByRole('button', { name: '新建', exact: true }).click()
  let groupDialog = page.getByRole('dialog', { name: '新建代理分组', exact: true })
  await groupDialog.getByRole('textbox', { name: '分组名称' }).fill('基础节点')
  await groupDialog.locator('.el-form-item').last().locator('.el-select').click()
  await page.locator('.el-select-dropdown:visible .el-select-dropdown__item').filter({ hasText: '测试节点 01' }).click()
  await page.keyboard.press('Escape')
  await groupDialog.getByRole('button', { name: '保存', exact: true }).click()
  await expect(page.getByText('测试代理分组校验错误', { exact: true })).toBeVisible()
  await expect(groupDialog).toBeVisible()
  await groupDialog.getByRole('button', { name: '保存', exact: true }).click()
  await expect(groupDialog).toBeHidden()
  expect(savedProxyGroups[0].members).toEqual([{ kind: 'endpoint', id: 'endpoint-1' }])

  await page.getByRole('button', { name: '新建', exact: true }).click()
  groupDialog = page.getByRole('dialog', { name: '新建代理分组', exact: true })
  await groupDialog.getByRole('textbox', { name: '分组名称' }).fill('组合策略')
  await groupDialog.locator('.el-form-item').last().locator('.el-select').click()
  await page.getByRole('option', { name: '基础节点', exact: true }).click()
  await page.keyboard.press('Escape')
  await groupDialog.getByRole('button', { name: '保存', exact: true }).click()
  await expect(groupDialog).toBeHidden()
  expect(savedProxyGroups[1].members).toEqual([{ kind: 'group', id: savedProxyGroups[0].id }])

  const baseGroupRow = page.locator('.el-table__body-wrapper tbody tr').first()
  await baseGroupRow.getByRole('button', { name: '编辑', exact: true }).click()
  const editGroupDialog = page.getByRole('dialog', { name: '编辑代理分组', exact: true })
  await expect(editGroupDialog).toBeVisible()
  await expect(editGroupDialog.getByRole('textbox', { name: '分组名称' })).toHaveValue('基础节点')
  await expect(editGroupDialog.locator('.el-select__selected-item').filter({ hasText: '测试节点 01' })).toBeVisible()
  await editGroupDialog.getByRole('button', { name: '取消', exact: true }).click()

  // Rule providers are maintained on their own page and referenced from a
  // client configuration, rather than being retyped inside every one of them.
  await page.getByRole('button', { name: '规则供应商', exact: true }).click()
  await expect(page.getByRole('heading', { name: '规则供应商', exact: true })).toBeVisible()
  await page.getByRole('button', { name: '新建', exact: true }).click()
  const providerDialog = page.getByRole('dialog', { name: '新建规则供应商', exact: true })
  await providerDialog.getByRole('textbox', { name: '供应商名称' }).fill('远程代理规则')
  await providerDialog.getByRole('textbox', { name: '供应商规则地址' }).fill('https://rules.example.com/proxy.mrs')
  await providerDialog.getByRole('textbox', { name: '供应商保存路径' }).fill('./ruleset/proxy.mrs')
  await providerDialog.getByRole('button', { name: '保存', exact: true }).click()
  await expect(providerDialog).toBeHidden()
  expect(savedRuleProviders[0]).toMatchObject({
    name: '远程代理规则', behavior: 'domain', format: 'mrs',
    url: 'https://rules.example.com/proxy.mrs', path: './ruleset/proxy.mrs',
    interval: 86400, proxy: 'DIRECT',
  })

  await page.getByRole('button', { name: '客户端配置', exact: true }).click()
  await expect(page.getByRole('heading', { name: '客户端配置', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '新建', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '管理代理分组', exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: '新建', exact: true }).click()
  // 配置改为整页编辑，按左侧导航分节；一次只渲染当前一节。
  await expect(page.getByRole('heading', { name: '新建客户端配置', exact: true })).toBeVisible()
  const clientForm = page.locator('.form-page')
  const clientSection = (label) => clientForm.locator('.form-nav__item').filter({ hasText: label })
  await expect(clientForm.getByRole('button', { name: '添加代理分组', exact: true })).toHaveCount(0)
  await expect(clientForm.getByText('国内直连，其余代理', { exact: true })).toHaveCount(0)
  await expect(clientForm.getByRole('button', { name: '创建并下载', exact: true })).toHaveCount(0)
  await clientForm.getByRole('textbox', { name: '配置名称' }).fill('组合分组配置')
  await clientForm.locator('.el-form-item').nth(1).locator('.el-select').click()
  await page.getByRole('option', { name: '组合策略 · 手动选择', exact: true }).click()
  await page.keyboard.press('Escape')
  await clientForm.locator('.el-form-item').nth(2).locator('.el-select').click()
  await page.getByRole('option', { name: '远程代理规则 · https://rules.example.com/proxy.mrs', exact: true }).click()
  await page.keyboard.press('Escape')
  await clientSection('访问规则').click()
  await expect(clientForm.getByLabel('规则配置模式')).toBeVisible()
  await clientForm.getByRole('button', { name: '添加', exact: true }).click()
  await clientForm.locator('.rule-table tbody tr').nth(0).locator('td').nth(0).locator('.el-select').click()
  await page.getByRole('option', { name: 'RULE-SET', exact: true }).click()
  await expect(clientForm.getByLabel('规则供应商', { exact: true })).toBeVisible()
  await clientForm.locator('.rule-table tbody tr').nth(0).locator('td').nth(2).locator('.el-select').click()
  await page.getByRole('option', { name: '组合策略', exact: true }).click()
  await clientForm.getByRole('button', { name: '添加', exact: true }).click()
  await clientForm.locator('.rule-table tbody tr').nth(1).locator('td').nth(1).getByRole('textbox').fill('youtube.com')
  await clientForm.locator('.rule-table tbody tr').nth(1).locator('td').nth(2).locator('.el-select').click()
  await page.keyboard.press('ArrowDown')
  await page.keyboard.press('Enter')
  await clientForm.getByRole('button', { name: '添加', exact: true }).click()
  await clientForm.locator('.rule-table tbody tr').nth(2).locator('td').nth(0).locator('.el-select').click()
  await page.getByRole('option', { name: 'MATCH', exact: true }).click()
  await clientForm.locator('.rule-table tbody tr').nth(2).locator('td').nth(2).locator('.el-select').click()
  await page.getByRole('option', { name: 'DIRECT', exact: true }).click()
  await expect(clientForm.getByRole('radio', { name: '表格配置', exact: true })).toBeChecked()
  await clientForm.locator('.el-radio-button').filter({ hasText: '高级纯文本' }).click()
  await expect(clientForm.getByLabel('高级规则文本')).toBeVisible()
  await clientForm.locator('.el-radio-button').filter({ hasText: '表格配置' }).click()
  // 未填完的分节在导航上标出来，不用逐节翻找。
  await expect(clientSection('DNS')).not.toContainText('待填')
  await page.getByRole('button', { name: '保存', exact: true }).click()
  await expect(page.getByRole('heading', { name: '客户端配置', exact: true })).toBeVisible()
  expect(savedClientConfig).toMatchObject({
    name: '组合分组配置',
    proxy_group_ids: [savedProxyGroups[1].id],
    rule_provider_ids: [savedRuleProviders[0].id],
    rule_mode: 'table',
    rules: [
      { type: 'RULE-SET', value: '远程代理规则', action: '组合策略', no_resolve: false },
      { type: 'DOMAIN-SUFFIX', value: 'youtube.com', action: '测试节点 01', no_resolve: false },
      { type: 'MATCH', value: '', action: 'DIRECT', no_resolve: false },
    ],
  })
  expect(savedClientConfig).not.toHaveProperty('groups')
  expect(savedClientConfig).not.toHaveProperty('rule_preset')

  const clientConfigRow = page.getByRole('row').filter({ hasText: '组合分组配置' })
  await expect(clientConfigRow.getByRole('button', { name: '下载', exact: true })).toHaveCount(0)
  const clientConfigSwitch = clientConfigRow.getByRole('switch')
  const clientConfigSwitchControl = clientConfigRow.locator('.el-switch')
  await expect(clientConfigSwitch).toBeChecked()
  await clientConfigSwitchControl.click()
  await expect(page.getByText('客户端配置已停用', { exact: true })).toBeVisible()
  expect(savedClientConfig.enabled).toBe(false)
  await clientConfigSwitchControl.click()
  await expect(page.getByText('客户端配置已启用', { exact: true })).toBeVisible()
  expect(savedClientConfig.enabled).toBe(true)
  await clientConfigRow.getByRole('button', { name: '复制', exact: true }).click()
  await expect(page.getByText('更新地址已复制', { exact: true })).toBeVisible()
  await expect.poll(() => page.evaluate(() => window.__copiedText)).toBe(`${process.env.POLARIS_E2E_BASE_URL}/api/v1/mihomo/subscriptions/test-token`)
  await clientConfigRow.getByRole('button', { name: '编辑', exact: true }).click()
  await expect(page.getByRole('heading', { name: '编辑客户端配置', exact: true })).toBeVisible()
  const editClientForm = page.locator('.form-page')
  await expect(editClientForm.getByRole('textbox', { name: '配置名称' })).toHaveValue('组合分组配置')
  await expect(editClientForm.locator('.el-form-item').nth(2).locator('.el-select__selected-item').first()).toContainText('远程代理规则')
  await editClientForm.locator('.form-nav__item').filter({ hasText: '访问规则' }).click()
  await expect(editClientForm.getByLabel('规则供应商', { exact: true })).toBeVisible()
  await expect(editClientForm.locator('.rule-table tbody tr')).toHaveCount(3)
  await page.setViewportSize({ width: 390, height: 844 })
  await expect.poll(() => editClientForm.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
  await expect.poll(() => editClientForm.locator('.form-body').evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
  await page.getByRole('button', { name: '返回', exact: true }).click()
  await page.getByRole('tab', { name: '访问记录', exact: true }).click()
  await expect(page.getByPlaceholder('IP', { exact: true })).toBeVisible()
  await expect(page.getByPlaceholder('归属地', { exact: true })).toBeVisible()
  await expect(page.getByPlaceholder('User-Agent', { exact: true })).toBeVisible()
  await expect.poll(async () => await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await expect.poll(async () => await page.evaluate(() => document.documentElement.scrollHeight <= innerHeight)).toBe(true)
  await expect.poll(async () => await page.evaluate(() => getComputedStyle(document.querySelector('.page-content')).scrollbarWidth)).toBe('none')

  await page.setViewportSize({ width: 1280, height: 800 })
  // A stored zone whose token no longer works must say so instead of showing an
  // empty list under a green "已连接" label.
  await page.getByRole('button', { name: '域名解析', exact: true }).click()
  await expect(page.getByRole('heading', { name: '域名解析', exact: true })).toBeVisible()
  await expect(page.getByText('连接异常', { exact: true })).toBeVisible()
  await expect(page.getByText('读取 Cloudflare 失败：Cloudflare 接口返回错误：Invalid API Token')).toBeVisible()
  await expect(page.getByRole('button', { name: '新建', exact: true })).toBeDisabled()

  // A zone and token that Cloudflare refuses must not be stored silently.
  await page.getByRole('button', { name: '设置', exact: true }).click()
  const settingsDialog = page.getByRole('dialog', { name: '连接 Cloudflare', exact: true })
  await settingsDialog.getByRole('textbox', { name: '访问令牌（API Token）' }).fill('bogus-token')
  await settingsDialog.getByRole('button', { name: '保存', exact: true }).click()
  await expect(page.getByText('Cloudflare 接口返回错误：Invalid API Token', { exact: true })).toBeVisible()
  await expect(settingsDialog).toBeVisible()
  await settingsDialog.getByRole('button', { name: '取消', exact: true }).click()

  cloudflareConnected = true
  await page.getByRole('button', { name: '刷新', exact: true }).click()
  await expect(page.getByText('已连接 example.com', { exact: true })).toBeVisible()
  await page.getByRole('row', { name: /^A ws\.example\.com/ }).getByRole('button', { name: '修改' }).click()
  const dnsDialog = page.getByRole('dialog', { name: '修改域名记录', exact: true })
  await expect(dnsDialog).toBeVisible()
  expect(await dnsDialog.locator('input').evaluateAll((inputs) => inputs.map((input) => input.value)))
    .toEqual(expect.arrayContaining(['ws.example.com', '203.0.113.10']))
  await dnsDialog.locator('.el-form-item', { hasText: '指向地址或内容' }).getByRole('textbox').fill('203.0.113.20')
  await dnsDialog.getByRole('button', { name: '保存', exact: true }).click()
  await expect(page.getByText('域名记录已保存，请点击“发布”写入 Cloudflare', { exact: true })).toBeVisible()
  expect(savedDNSRecord).toMatchObject({ name: 'ws.example.com', type: 'A', content: '203.0.113.20' })
  // A record that only exists upstream is edited by declaring it here first.
  // cdn.example.com has the CDN switched on and serves no access service, and
  // that combination still has to be saveable.
  await page.getByRole('row', { name: /^CNAME cdn\.example\.com/ }).getByRole('button', { name: '修改' }).click()
  const adoptDialog = page.getByRole('dialog', { name: '修改线上记录', exact: true })
  await expect(adoptDialog.getByText('该记录目前只存在于 Cloudflare')).toBeVisible()
  await adoptDialog.locator('.el-form-item', { hasText: '指向地址或内容' }).getByRole('textbox').fill('ws.example.com')
  await adoptDialog.getByRole('button', { name: '保存', exact: true }).click()
  await expect(page.getByText('域名记录已保存，请点击“发布”写入 Cloudflare', { exact: true })).toBeVisible()
  expect(adoptedDNSRecord).toMatchObject({ name: 'cdn.example.com', type: 'CNAME', content: 'ws.example.com', proxied: true, listener_id: '' })

  // The connection domain offers what actually resolves to the selected server.
  await page.getByRole('button', { name: '接入服务', exact: true }).click()
  await expect(page.getByRole('heading', { name: '接入服务', exact: true })).toBeVisible()
  await page.getByRole('button', { name: '新建', exact: true }).click()
  const domainDialog = page.getByRole('dialog', { name: '新建接入服务', exact: true })
  await expect(domainDialog.getByText('点击输入框可选择解析到 203.0.113.10 的域名，也可直接输入其它域名。')).toBeVisible()
  const domainInput = domainDialog.getByRole('textbox', { name: '连接域名' })
  await domainInput.click()
  const suggestions = page.locator('.el-autocomplete-suggestion__list li')
  // ws.example.com carries the server's address; cdn.example.com is a CNAME
  // that leads to it, so both belong to this server.
  await expect(suggestions).toHaveText([
    'ws.example.com解析到本服务器',
    'cdn.example.com解析到本服务器',
    'other.example.com未解析到本服务器',
  ])
  await suggestions.filter({ hasText: 'cdn.example.com' }).click()
  await expect(domainInput).toHaveValue('cdn.example.com')
  await domainDialog.getByRole('button', { name: '取消', exact: true }).click()

  expect(consoleErrors).toEqual([])
  // Two of these are the deliberately rejected create and copy attempts.
  expect(httpErrors).toHaveLength(4)
  expect(httpErrors.some((entry) => /400 .*\/api\/v1\/cloudflare\/settings$/.test(entry))).toBe(true)
  expect(httpErrors.filter((entry) => /400 .*\/api\/v1\/listeners\/quick$/.test(entry))).toHaveLength(2)
  expect(httpErrors.some((entry) => /400 .*\/api\/v1\/mihomo\/proxy-groups$/.test(entry))).toBe(true)
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
