// Mirrors internal/control.IsNewerVersion: an optional leading "v" is dropped,
// both sides must be dotted-numeric (so "dev" or a commit hash is never
// "outdated"), and only a strictly newer latest edition counts. Kept in sync
// with the master's own update check so the console never offers an upgrade
// that the backend would reject.
export function isNewerVersion(latest, current) {
  const latestParts = versionParts(latest)
  if (!latestParts) return false
  const currentParts = versionParts(current)
  if (!currentParts) return false
  const length = Math.max(latestParts.length, currentParts.length)
  for (let i = 0; i < length; i += 1) {
    const l = latestParts[i] ?? 0
    const c = currentParts[i] ?? 0
    if (l !== c) return l > c
  }
  return false
}

function versionParts(value) {
  const text = String(value ?? '').trim().replace(/^v/, '')
  if (!text) return null
  const parts = []
  for (const segment of text.split('.')) {
    if (!/^\d+$/.test(segment)) return null
    parts.push(Number(segment))
  }
  return parts
}
