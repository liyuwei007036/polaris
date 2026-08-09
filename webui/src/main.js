import { createApp, defineAsyncComponent } from 'vue'
import ElementPlus from 'element-plus'
import { ElDialog, ElMessage, ElMessageBox } from 'element-plus'
import zhCn from 'element-plus/es/locale/lang/zh-cn'
import 'element-plus/dist/index.css'
import 'element-plus/theme-chalk/dark/css-vars.css'
import './styles.css'
import { isMobileUI } from './device'

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

// Windows 没有国旗字形，emoji 国旗会退化成 HK、JP 这样的字母对。
// 量一下宽度就能判断：能画成一个字形的说明支持，否则给字母对配一个徽章样式。
function flagEmojiSupported() {
  const context = document.createElement('canvas').getContext('2d')
  if (!context) return false
  context.font = '16px sans-serif'
  return context.measureText('\u{1F1ED}\u{1F1F0}').width < context.measureText('\u{1F1ED}').width * 1.8
}

if (!flagEmojiSupported()) document.documentElement.classList.add('no-flag-emoji')

window.addEventListener('unhandledrejection', (event) => {
  if (event.reason instanceof Error) {
    ElMessage.error(event.reason.message)
    event.preventDefault()
  }
})

// 两套界面各自成块：手机不会下载桌面版的表格与图表代码，反之亦然。
const root = defineAsyncComponent(() => (isMobileUI() ? import('./mobile/MobileApp.vue') : import('./App.vue')))

createApp(root).use(ElementPlus, { locale: zhCn }).mount('#app')
