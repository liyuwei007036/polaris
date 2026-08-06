export function formatDateTime(value, fallback = '—') {
  if (!value) return fallback
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return fallback
  const pad = (part) => String(part).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`
}

export function formatBytes(value, suffix = '') {
  let number = Number(value || 0)
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let index = 0
  while (number >= 1024 && index < units.length - 1) {
    number /= 1024
    index += 1
  }
  // Two decimals everywhere above bytes; raw byte counts are whole numbers
  // and reading "512.00 B" helps nobody.
  return `${number.toFixed(index ? 2 : 0)} ${units[index]}${suffix}`
}

export function includesText(values, query) {
  const keyword = String(query || '').trim().toLocaleLowerCase()
  if (!keyword) return true
  return values.some((value) => String(value ?? '').toLocaleLowerCase().includes(keyword))
}
