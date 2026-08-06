// Every timestamp the server sends is UTC. Times are always rendered in the
// visitor's own time zone, so an operator in Shanghai and a server in UTC see
// the same moment written the way each of them reads a clock.
export function parseServerTime(value) {
  if (!value) return null
  if (typeof value === 'number') return new Date(value)
  const text = String(value).trim()
  if (!text) return null
  // A bare "2026-08-06 12:00:00" carries no zone; browsers would read it as
  // local time and the reading would drift by the viewer's own offset.
  const naive = /^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(:\d{2}(\.\d+)?)?$/.test(text)
  const date = new Date(naive ? `${text.replace(' ', 'T')}Z` : text)
  return Number.isNaN(date.getTime()) ? null : date
}

export function formatDateTime(value, fallback = '—') {
  const date = parseServerTime(value)
  if (!date) return fallback
  const pad = (part) => String(part).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`
}

export function formatDate(value, fallback = '—') {
  const date = parseServerTime(value)
  if (!date) return fallback
  const pad = (part) => String(part).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`
}

export function formatTime(value, fallback = '—') {
  const date = parseServerTime(value)
  if (!date) return fallback
  const pad = (part) => String(part).padStart(2, '0')
  return `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`
}

// localTimeZoneLabel names the zone the console is rendering times in, so a
// column heading can say so instead of leaving the reader to guess.
export function localTimeZoneLabel() {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || ''
  } catch {
    return ''
  }
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
