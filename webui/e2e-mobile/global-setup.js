import desktopSetup from '../e2e/global-setup.js'

// 默认和桌面版用例一样：现编译一个主控跑起来。
// 但这台机器上不一定有 Go 工具链（例如主控是在 WSL 里编译运行的），
// 这时把 POLARIS_E2E_BASE_URL 指过去，直接对着那份实例跑。
export default async function globalSetup(config) {
  if (!process.env.POLARIS_E2E_BASE_URL) return desktopSetup(config)
  process.env.POLARIS_E2E_ADMIN_USERNAME ||= 'polaris_admin'
  process.env.POLARIS_E2E_INITIAL_PASSWORD ||= '123456'
  process.env.POLARIS_E2E_CHANGED_PASSWORD ||= 'Mobile-E2E-Password-2026!'
  return async () => {}
}
