// /share/<token> is a read-only view of one real console page. The token is
// the credential: every API call in this mode goes to the public share
// endpoints, which return the same shapes as the console API but masked.
const match = typeof window !== 'undefined'
  ? window.location.pathname.match(/^\/share\/([^/?#]+)\/?$/)
  : null

export const shareToken = match ? decodeURIComponent(match[1]) : ''
export const isShareMode = Boolean(shareToken)

export const shareAPIBase = shareToken
  ? `/api/v1/shares/access/${encodeURIComponent(shareToken)}`
  : ''

// The real pages call paths such as /nodes and /nodes/metrics. Prefixing them
// with the share base lets those components stay completely unaware that they
// are running in a share.
export function shareAPIPath(path = '') {
  return `${shareAPIBase}${path}`
}

export function shareStreamPath(path) {
  return shareAPIPath(path)
}
