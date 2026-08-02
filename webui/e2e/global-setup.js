import { spawn, spawnSync } from 'node:child_process'
import { existsSync, mkdtempSync, openSync, readFileSync, rmSync } from 'node:fs'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const adminEmail = 'browser.e2e@example.test'
const adminPassword = 'Browser-E2E-Password-2026!'

export default async function globalSetup() {
  const projectRoot = resolve(fileURLToPath(new URL('../..', import.meta.url)))
  const work = mkdtempSync(join(tmpdir(), 'sb-control-browser-e2e-'))
  const executable = join(work, process.platform === 'win32' ? 'sb-control.exe' : 'sb-control')
  const go = resolveGo(projectRoot)
  run(go, ['build', '-trimpath', '-o', executable, './cmd/sb-control'], { cwd: projectRoot })

  const dataDir = join(work, 'master-data')
  const init = run(executable, ['master', 'init-admin', '--data-dir', dataDir, '--email', adminEmail, '--password-stdin'], {
    cwd: projectRoot,
    input: `${adminPassword}\n`,
  })
  const secret = init.trim().split(/\s+/).at(-1)
  if (!secret) throw new Error('初始化管理员后没有得到两步验证密钥')

  const agentPort = await freePort()
  const browserPort = await freePort()
  const baseURL = `http://127.0.0.1:${browserPort}`
  const logPath = join(work, 'master.log')
  const log = openSync(logPath, 'a')
  const master = spawn(executable, [
    'master', 'serve', '--data-dir', dataDir,
    '--agent-port', `${agentPort}`,
    '--web-port', `${browserPort}`,
    '--allow-insecure-http',
  ], { cwd: projectRoot, stdio: ['ignore', log, log], windowsHide: true })

  try {
    await waitForHealth(baseURL, master, logPath)
  } catch (error) {
    master.kill('SIGKILL')
    rmSync(work, { recursive: true, force: true })
    throw error
  }

  process.env.SB_CONTROL_E2E_BASE_URL = baseURL
  process.env.SB_CONTROL_E2E_ADMIN_EMAIL = adminEmail
  process.env.SB_CONTROL_E2E_ADMIN_PASSWORD = adminPassword
  process.env.SB_CONTROL_E2E_TOTP_SECRET = secret

  return async () => {
    master.kill('SIGKILL')
    await Promise.race([
      new Promise((resolveExit) => master.once('exit', resolveExit)),
      new Promise((resolveTimeout) => setTimeout(resolveTimeout, 2_000)),
    ])
    rmSync(work, { recursive: true, force: true })
  }
}

function resolveGo(projectRoot) {
  if (process.env.GO_BINARY) return process.env.GO_BINARY
  const local = join(projectRoot, '.tools', 'go', 'go', 'bin', process.platform === 'win32' ? 'go.exe' : 'go')
  return existsSync(local) ? local : 'go'
}

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    encoding: 'utf8',
    env: process.env,
    windowsHide: true,
    ...options,
  })
  if (result.error || result.status !== 0) {
    throw new Error(`${command} ${args.join(' ')} 执行失败\n${result.error || ''}\n${result.stdout || ''}\n${result.stderr || ''}`)
  }
  return result.stdout || ''
}

async function freePort() {
  return await new Promise((resolvePort, reject) => {
    const server = createServer()
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => {
      const address = server.address()
      server.close(() => resolvePort(address.port))
    })
  })
}

async function waitForHealth(baseURL, master, logPath) {
  const deadline = Date.now() + 15_000
  while (Date.now() < deadline) {
    if (master.exitCode !== null) {
      throw new Error(`主控进程提前退出\n${readFileSync(logPath, 'utf8')}`)
    }
    try {
      const response = await fetch(`${baseURL}/api/v1/health`)
      if (response.ok) return
    } catch {
      // The TCP listener may not be ready yet.
    }
    await new Promise((resolveWait) => setTimeout(resolveWait, 100))
  }
  throw new Error(`等待主控健康检查超时\n${readFileSync(logPath, 'utf8')}`)
}
