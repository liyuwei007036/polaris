import { createApp } from 'vue'
import ElementPlus from 'element-plus'
import { ElDialog, ElMessage, ElMessageBox } from 'element-plus'
import zhCn from 'element-plus/es/locale/lang/zh-cn'
import 'element-plus/dist/index.css'
import './styles.css'
import App from './App.vue'

ElDialog.props.closeOnClickModal.default = false
ElDialog.props.closeOnPressEscape.default = false
for (const method of ['alert', 'confirm', 'prompt']) {
  const open = ElMessageBox[method]
  ElMessageBox[method] = (message, title, options = {}, appContext) => open(message, title, {
    ...options,
    closeOnClickModal: false,
    closeOnPressEscape: false,
  }, appContext)
}

window.addEventListener('unhandledrejection', (event) => {
  if (event.reason instanceof Error) {
    ElMessage.error(event.reason.message)
    event.preventDefault()
  }
})

createApp(App).use(ElementPlus, { locale: zhCn }).mount('#app')
