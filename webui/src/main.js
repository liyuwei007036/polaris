import { createApp } from 'vue'
import ElementPlus from 'element-plus'
import { ElMessage } from 'element-plus'
import zhCn from 'element-plus/es/locale/lang/zh-cn'
import 'element-plus/dist/index.css'
import './styles.css'
import App from './App.vue'

window.addEventListener('unhandledrejection', (event) => {
  if (event.reason instanceof Error) {
    ElMessage.error(event.reason.message)
    event.preventDefault()
  }
})

createApp(App).use(ElementPlus, { locale: zhCn }).mount('#app')
