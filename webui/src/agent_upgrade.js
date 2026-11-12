import { ElMessage, ElMessageBox } from 'element-plus'
import { post } from './api'
import { waitForTask } from './live'

/**
 * Prompts for confirmation, dispatches an agent upgrade task, and tracks its
 * completion over the live event stream so failures are surfaced immediately.
 */
export async function performAgentUpgrade(node, { refreshList, refreshNodes } = {}) {
  await ElMessageBox.confirm(
    `将“${node.name}”的 agent 升级到最新版本？升级完成后 agent 会自动重启并重新连接，运行中的代理服务不受影响。`,
    '升级 Agent',
    { type: 'warning', confirmButtonText: '开始升级' },
  )
  const task = await post(`/nodes/${node.id}/agent/upgrade`, {})
  ElMessage.success('升级任务已下发，正在等待服务器更新并重启')
  if (!task?.id) return
  waitForTask(task.id, 180000)
    .then(async (done) => {
      if (done.status === 'succeeded') {
        ElMessage.success(done.result_summary || `“${node.name}”的 agent 已更新`)
        if (refreshList) await refreshList().catch(() => {})
        if (refreshNodes) window.setTimeout(() => { refreshNodes().catch(() => {}) }, 3000)
      } else {
        ElMessage.error(done.result_summary || `“${node.name}”的 agent 升级失败`)
      }
    })
    .catch((err) => {
      ElMessage.warning(err?.message || '暂未收到升级结果，请稍后刷新或在“操作记录”中查看')
    })
}
