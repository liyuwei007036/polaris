// 从节点名称里认出地区，用来在列表和下拉框里显示国旗。
// 认不出来就返回空字符串，界面上不显示图标，不做任何猜测。
const REGIONS = [
  ['🇭🇰', ['香港', 'hongkong', 'hong kong', 'hk', 'hkg']],
  ['🇲🇴', ['澳门', 'macau', 'mo', 'mfm']],
  ['🇹🇼', ['台湾', '台北', 'taiwan', 'taipei', 'tw', 'tpe']],
  ['🇨🇳', ['中国', '大陆', '国内', '北京', '上海', '广州', '深圳', 'china', 'cn', 'pek', 'sha', 'can', 'szx']],
  ['🇯🇵', ['日本', '东京', '大阪', 'japan', 'tokyo', 'osaka', 'jp', 'nrt', 'hnd', 'kix', 'tyo']],
  ['🇰🇷', ['韩国', '首尔', 'korea', 'seoul', 'kr', 'icn', 'sel']],
  ['🇸🇬', ['新加坡', '狮城', 'singapore', 'sg', 'sin']],
  ['🇺🇸', ['美国', '洛杉矶', '硅谷', '纽约', '西雅图', 'united states', 'america', 'us', 'usa', 'lax', 'sjc', 'sea', 'nyc', 'iad', 'dfw', 'ord']],
  ['🇨🇦', ['加拿大', '多伦多', 'canada', 'ca', 'yyz', 'yvr']],
  ['🇬🇧', ['英国', '伦敦', 'britain', 'england', 'uk', 'gb', 'lhr', 'lon']],
  ['🇩🇪', ['德国', '法兰克福', 'germany', 'frankfurt', 'de', 'fra']],
  ['🇫🇷', ['法国', '巴黎', 'france', 'paris', 'fr', 'cdg']],
  ['🇳🇱', ['荷兰', '阿姆斯特丹', 'netherlands', 'amsterdam', 'nl', 'ams']],
  ['🇷🇺', ['俄罗斯', '莫斯科', 'russia', 'moscow', 'ru', 'svo', 'dme']],
  ['🇦🇺', ['澳大利亚', '澳洲', '悉尼', 'australia', 'sydney', 'au', 'syd']],
  ['🇮🇳', ['印度', '孟买', 'india', 'mumbai', 'in', 'bom', 'del']],
  ['🇧🇷', ['巴西', '圣保罗', 'brazil', 'br', 'gru']],
  ['🇹🇷', ['土耳其', '伊斯坦布尔', 'turkey', 'tr', 'ist']],
  ['🇻🇳', ['越南', '胡志明', 'vietnam', 'vn', 'sgn', 'han']],
  ['🇹🇭', ['泰国', '曼谷', 'thailand', 'bangkok', 'th', 'bkk']],
  ['🇲🇾', ['马来西亚', '吉隆坡', 'malaysia', 'my', 'kul']],
  ['🇵🇭', ['菲律宾', '马尼拉', 'philippines', 'ph', 'mnl']],
  ['🇮🇩', ['印尼', '印度尼西亚', '雅加达', 'indonesia', 'id', 'cgk']],
  ['🇦🇪', ['阿联酋', '迪拜', 'dubai', 'uae', 'ae', 'dxb']],
  ['🇮🇹', ['意大利', '米兰', 'italy', 'milan', 'it', 'mxp']],
  ['🇪🇸', ['西班牙', '马德里', 'spain', 'madrid', 'es', 'mad']],
  ['🇸🇪', ['瑞典', '斯德哥尔摩', 'sweden', 'se', 'arn']],
  ['🇨🇭', ['瑞士', '苏黎世', 'switzerland', 'ch', 'zrh']],
  ['🇵🇱', ['波兰', '华沙', 'poland', 'pl', 'waw']],
  ['🇫🇮', ['芬兰', '赫尔辛基', 'finland', 'fi', 'hel']],
  ['🇳🇴', ['挪威', 'norway', 'no', 'osl']],
  ['🇩🇰', ['丹麦', 'denmark', 'dk', 'cph']],
  ['🇮🇪', ['爱尔兰', 'ireland', 'ie', 'dub']],
  ['🇲🇽', ['墨西哥', 'mexico', 'mx', 'mex']],
  ['🇿🇦', ['南非', 'south africa', 'za', 'jnb']],
  ['🇦🇷', ['阿根廷', 'argentina', 'ar', 'eze']],
]

// 拉丁字母缩写要求前后不是字母数字，避免 "hk" 命中 "checkout" 这类名字。
function matches(haystack, key) {
  if (!/^[a-z ]+$/.test(key)) return haystack.includes(key)
  const index = haystack.indexOf(key)
  if (index < 0) return false
  const before = haystack[index - 1]
  const after = haystack[index + key.length]
  return !(before && /[a-z0-9]/.test(before)) && !(after && /[a-z0-9]/.test(after))
}

export function regionFlag(name) {
  const text = String(name || '').toLowerCase()
  if (!text) return ''
  for (const [flag, keys] of REGIONS) {
    for (const key of keys) if (matches(text, key)) return flag
  }
  return ''
}
