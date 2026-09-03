// 只含国旗字形的补充字体（见 main.js 的注入逻辑）。名称里的国旗一律由使用者
// 自己写进去，界面不做地区猜测，这里只负责让写进去的旗帜画得出来。
//
// 页面文字靠 styles.css 里的 font-family 用上它；图表画在 canvas 上，CSS 管不到，
// 得在 ECharts 的 textStyle 里点名。
export const FLAG_FONT_FAMILY = 'Twemoji Country Flags'
