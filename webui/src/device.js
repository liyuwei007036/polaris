// 手机版和桌面版是两套独立界面，进入控制台时判断一次挂载哪一套。
//
// 判断依据只有两条，都不做猜测：
//   1. UA 明确是手机（Android 手机、iPhone 等）；
//   2. 视口窄到桌面版的表格必然横向溢出（桌面表格最窄的一张也要 900px 以上）。
// iPad 这类平板宽度足够放下桌面表格，仍然走桌面版。
export function isMobileUI() {
  const agent = navigator.userAgent || ''
  if (/\biPad\b/.test(agent)) return false
  if (/Android.*Mobile|iPhone|iPod|Windows Phone|IEMobile|BlackBerry|Opera Mini/i.test(agent)) return true
  return window.matchMedia('(max-width: 767px)').matches
}
