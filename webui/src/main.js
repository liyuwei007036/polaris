import { createApp, defineAsyncComponent } from 'vue'
import ElementPlus from 'element-plus'
import { ElDialog, ElMessage, ElMessageBox } from 'element-plus'
import zhCn from 'element-plus/es/locale/lang/zh-cn'
import 'element-plus/dist/index.css'
import 'element-plus/theme-chalk/dark/css-vars.css'
import './styles.css'
import { polyfillCountryFlagEmojis } from 'country-flag-emoji-polyfill'
import flagFontURL from 'country-flag-emoji-polyfill/dist/TwemojiCountryFlags.woff2?url'
import { isMobileUI } from './device'
import { FLAG_FONT_FAMILY } from './flags'

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

// Windows 和多数 Linux 桌面没有国旗字形，名称里写的 🇭🇰 会退化成 HK 这样的字母
// 对。补一份只含国旗字形的子集字体（Twemoji，CC-BY 4.0，78 KB，随包发布不依赖
// 外网），页面和图表画布就都能画出旗帜。polyfill 自己探测平台，macOS 与 iOS 上
// 既不注入也不下载。
//
// 画布上的字不走 CSS 的懒加载，所以这里显式取一次字体，图表才有得可画。
if (polyfillCountryFlagEmojis(FLAG_FONT_FAMILY, flagFontURL)) {
  document.fonts.load(`16px "${FLAG_FONT_FAMILY}"`).catch(() => {})
}

window.addEventListener('unhandledrejection', (event) => {
  if (event.reason instanceof Error) {
    ElMessage.error(event.reason.message)
    event.preventDefault()
  }
})

// 两套界面各自成块：手机不会下载桌面版的表格与图表代码，反之亦然。
const root = defineAsyncComponent(() => (isMobileUI() ? import('./mobile/MobileApp.vue') : import('./App.vue')))

createApp(root).use(ElementPlus, { locale: zhCn }).mount('#app')
