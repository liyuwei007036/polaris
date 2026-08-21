import { test, expect } from '@playwright/test'

// 用 iPhone 的 UA 和视口访问，控制台应当自己切到手机版界面。
test.use({
  viewport: { width: 390, height: 844 },
  userAgent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1',
  hasTouch: true,
})

// 「更多」页里的九个二级页面，以及点进去之后页面标题应当是什么。
const morePages = [
  '服务器',
  '网络防护',
  '服务器访问规则',
  '上网出口',
  '代理分组',
  '规则供应商',
  '操作记录',
  '域名解析',
  '系统设置',
]

test('手机端页面回归：自动进入手机版，走完登录与全部页面', async ({ page }) => {
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

  // 页面不允许横向滚动：手机上一旦能左右拖，说明有元素比屏幕宽。
  async function expectNoSideways(where) {
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
    expect(overflow, `${where} 出现横向溢出 ${overflow}px`).toBeLessThanOrEqual(0)
  }

  await page.goto(process.env.POLARIS_E2E_BASE_URL)

  // 走的必须是手机版：桌面版的侧边栏不应该存在。
  await expect(page.locator('.m-login')).toBeVisible()
  await expect(page.locator('.app-sidebar')).toHaveCount(0)
  await expectNoSideways('登录页')

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

  // 概览：四张指标卡和五张图都要在。
  for (const label of ['服务器总数', '在线服务器', '活动连接', '节点数量']) {
    await expect(page.locator('.metric').getByText(label, { exact: true })).toBeVisible()
  }
  await expect(page.locator('.chart canvas')).toHaveCount(5)
  await expectNoSideways('运行概览')

  // 底部标签栏：五个入口，且完整落在视口里。
  const tabBar = page.locator('.m-app__tabs')
  await expect(tabBar.locator('.m-app__tab')).toHaveCount(5)
  const tabGeometry = await page.evaluate(() => {
    const bar = document.querySelector('.m-app__tabs')
    return { bottom: Math.round(bar.getBoundingClientRect().bottom), viewport: window.innerHeight }
  })
  expect(tabGeometry.bottom).toBeLessThanOrEqual(tabGeometry.viewport)

  // 底部四个入口按打开频率排：看板、当前连接、客户端配置、接入服务。
  for (const [tab, heading] of [['连接', '当前连接'], ['配置', '客户端配置'], ['接入', '接入服务'], ['更多', '更多']]) {
    await tabBar.getByRole('button', { name: tab, exact: true }).click()
    await expect(page.getByRole('heading', { name: heading, exact: true })).toBeVisible()
    // 一次只挂一个页面：过渡没走完会把两页叠在一起，看着像点了没反应。
    await expect(page.locator('.m-page')).toHaveCount(1)
    await expectNoSideways(heading)
  }

  // 「更多」里的九个二级页面逐个点开。
  for (const heading of morePages) {
    await page.evaluate(() => { location.hash = '#/more' })
    await expect(page.getByRole('heading', { name: '更多', exact: true })).toBeVisible()
    await page.getByRole('button', { name: heading, exact: true }).click()
    await expect(page.getByRole('heading', { name: heading, exact: true })).toBeVisible()
    await expect(page.locator('.m-page')).toHaveCount(1)
    await expectNoSideways(heading)
    // 二级页面停留时底部要高亮「更多」，否则看不出自己在哪一层。
    await expect(tabBar.getByRole('button', { name: '更多', exact: true })).toHaveClass(/is-active/)
  }

  // 接入服务的新建表单是整屏抽屉，里面的选择器点开后仍然不能撑宽页面。
  await page.evaluate(() => { location.hash = '#/inbounds' })
  await expect(page.getByRole('heading', { name: '接入服务', exact: true })).toBeVisible()
  await page.getByRole('button', { name: '新建接入服务', exact: true }).click()
  const form = page.locator('.m-sheet__panel')
  await expect(form.getByText('新建接入服务', { exact: true })).toBeVisible()
  await expect(form.getByText('VLESS + Reality', { exact: true })).toBeVisible()
  await expectNoSideways('新建接入服务')
  await form.getByRole('button', { name: '关闭', exact: true }).click()
  await expect(page.locator('.m-sheet')).toHaveCount(0)

  // 服务器页的接入向导：命令要能显示出来。
  await page.evaluate(() => { location.hash = '#/nodes' })
  await page.getByRole('button', { name: '添加服务器', exact: true }).click()
  const token = page.locator('.m-sheet__panel')
  await expect(token.getByText('服务器接入信息', { exact: true })).toBeVisible()
  await expect(token.locator('.command')).toContainText('install.sh agent')
  await expectNoSideways('服务器接入信息')
  await token.getByRole('button', { name: '完成', exact: true }).click()

  // 客户端配置的规则编辑：桌面版是一张五列表格，手机上应当是可点开的卡片。
  await page.evaluate(() => { location.hash = '#/subscriptions' })
  await page.getByRole('button', { name: '新建客户端配置', exact: true }).click()
  await expect(page.getByRole('heading', { name: '新建客户端配置', exact: true })).toBeVisible()
  await page.getByRole('tab', { name: /访问规则/ }).click()
  await page.getByRole('button', { name: '添加规则', exact: true }).click()
  const ruleSheet = page.locator('.m-sheet__panel')
  await expect(ruleSheet.getByText('编辑规则', { exact: true })).toBeVisible()
  await expectNoSideways('编辑规则')
  await ruleSheet.getByRole('button', { name: '完成', exact: true }).click()
  await expect(page.locator('.rule')).toHaveCount(1)
  await page.getByRole('button', { name: '返回', exact: true }).click()
  await expect(page.getByRole('heading', { name: '客户端配置', exact: true })).toBeVisible()

  // 退出登录回到手机版登录页。
  await page.evaluate(() => { location.hash = '#/more' })
  await page.getByRole('button', { name: '退出登录', exact: true }).click()
  await page.getByRole('button', { name: '退出', exact: true }).click()
  authenticated = false
  await expect(page.locator('.m-login')).toBeVisible()

  expect(consoleErrors, `控制台报错：${consoleErrors.join(' | ')}`).toEqual([])
  expect(httpErrors, `接口报错：${httpErrors.join(' | ')}`).toEqual([])
})

test('桌面视口仍然走桌面版界面', async ({ browser }) => {
  // 这个文件顶部的 test.use 会传染给测试里新建的上下文，UA 和触控都要显式改回桌面。
  const context = await browser.newContext({
    viewport: { width: 1280, height: 800 },
    userAgent: 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/151.0.0.0 Safari/537.36',
    hasTouch: false,
  })
  const page = await context.newPage()
  await page.goto(process.env.POLARIS_E2E_BASE_URL)
  await expect(page.locator('.login-page')).toBeVisible()
  await expect(page.locator('.m-login')).toHaveCount(0)
  await context.close()
})
