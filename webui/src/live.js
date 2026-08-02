import { ref } from 'vue'
import { friendlyError } from './api'

export const liveStatus = ref('connecting')

let source
const subscribers = new Set()

function ensureSource() {
  if (source) return
  source = new EventSource('/api/v1/events/live', { withCredentials: true })
  source.addEventListener('open', () => { liveStatus.value = 'connected' })
  source.addEventListener('ready', () => { liveStatus.value = 'connected' })
  source.addEventListener('change', (event) => {
    let payload
    try { payload = JSON.parse(event.data) } catch { return }
    subscribers.forEach((subscriber) => subscriber(payload))
  })
  source.addEventListener('error', () => { liveStatus.value = 'reconnecting' })
}

export function subscribeLive(subscriber) {
  subscribers.add(subscriber)
  ensureSource()
  return () => subscribers.delete(subscriber)
}

export function closeLiveEvents() {
  source?.close()
  source = undefined
  liveStatus.value = 'connecting'
}

async function readTask(taskID) {
  const response = await fetch(`/api/v1/tasks/${taskID}`, { credentials: 'same-origin' })
  if (response.status === 401) {
    window.dispatchEvent(new CustomEvent('sb:unauthorized'))
    throw new Error('会话已过期，请重新登录')
  }
  const task = await response.json().catch(() => null)
  if (!response.ok) throw new Error(task?.error ? friendlyError(task.error) : `无法读取操作结果（错误代码 ${response.status}）`)
  return task
}

export async function waitForTask(taskID, timeoutMs = 30000) {
  const terminal = new Set(['succeeded', 'failed', 'rolled_back'])
  return new Promise((resolve, reject) => {
    let finished = false
    let timeout
    const stop = subscribeLive(async (event) => {
      if (event.kind !== 'task' || event.task_id !== taskID || finished) return
      try {
        const task = await readTask(taskID)
        if (!terminal.has(task.status)) return
        finished = true
        window.clearTimeout(timeout)
        stop()
        resolve(task)
      } catch (error) {
        finished = true
        window.clearTimeout(timeout)
        stop()
        reject(error)
      }
    })
    timeout = window.setTimeout(() => {
      if (finished) return
      finished = true
      stop()
      reject(new Error('操作等待时间过长，请到“操作记录”查看最终结果'))
    }, timeoutMs)
    readTask(taskID).then((task) => {
      if (!finished && terminal.has(task.status)) {
        finished = true
        window.clearTimeout(timeout)
        stop()
        resolve(task)
      }
    }).catch(() => {})
  })
}
