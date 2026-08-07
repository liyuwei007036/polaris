import { ref } from 'vue'

export const csrfToken = ref(localStorage.getItem('polaris_csrf') || '')

export function setCsrfToken(token) {
  csrfToken.value = token || ''
  if (token) localStorage.setItem('polaris_csrf', token)
  else localStorage.removeItem('polaris_csrf')
}

window.addEventListener('storage', (event) => {
  if (event.key === 'polaris_csrf') csrfToken.value = event.newValue || ''
})

export function friendlyError(message) {
  const messages = {
    'not found': '未找到请求的内容',
    'permission denied': '没有操作权限',
    'authentication failed': '登录信息不正确',
    'conflicting state': '当前设置与已有内容冲突，请检查后重试',
    'invalid request': '提交的内容不完整或格式不正确，请检查后重试',
  }
  if (messages[message]) return messages[message]
  if (typeof message === 'string' && /[\u3400-\u9fff]/.test(message)) return message
  return '操作未完成，请检查填写内容和服务器状态后重试'
}

export async function api(path, options = {}) {
  const { method = 'GET', body, silentUnauthorized = false } = options
  const headers = { 'Content-Type': 'application/json' }
  if (csrfToken.value) headers['X-CSRF-Token'] = csrfToken.value
  const response = await fetch(`/api/v1${path}`, {
    method,
    credentials: 'same-origin',
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (response.status === 401 && !silentUnauthorized) {
    window.dispatchEvent(new CustomEvent('sb:unauthorized'))
    throw new Error('会话已过期，请重新登录')
  }
  if (response.status === 204) return null
  const contentType = response.headers.get('content-type') || ''
  const data = contentType.includes('json')
    ? await response.json().catch(() => null)
    : await response.text().catch(() => null)
  if (!response.ok) {
    const message = data?.error || (typeof data === 'string' ? data : `操作未完成（错误代码 ${response.status}）`)
    const error = new Error(friendlyError(message))
    error.code = message
    throw error
  }
  return data
}

export const post = (path, body) => api(path, { method: 'POST', body })
export const put = (path, body) => api(path, { method: 'PUT', body })
export const del = (path) => api(path, { method: 'DELETE' })
