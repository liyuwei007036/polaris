// Sparklines are read as a trend, not as a list of samples. A plain polyline
// turns every sample into a hard corner, so at any decent sample rate the line
// looks jagged instead of flowing. Fit a monotone cubic curve through the
// points instead: it is smooth, but it never overshoots the samples it joins.
// (Catmull-Rom overshoots slightly, which on a flat 0 B/s run draws phantom
// traffic below the baseline.)

const round = (n) => Math.round(n * 100) / 100

function monotonePath(points) {
  const n = points.length
  if (n === 2) {
    return `M ${round(points[0].x)} ${round(points[0].y)} L ${round(points[1].x)} ${round(points[1].y)}`
  }

  // Secant slope of every segment; x is always increasing.
  const dx = []
  const dy = []
  const slope = []
  for (let i = 0; i < n - 1; i++) {
    dx[i] = points[i + 1].x - points[i].x
    dy[i] = points[i + 1].y - points[i].y
    slope[i] = dy[i] / dx[i]
  }

  // Tangent at each point: the average of the neighbouring secants, flattened
  // to zero at local extrema so the curve does not loop past a peak.
  const tangent = new Array(n)
  tangent[0] = slope[0]
  tangent[n - 1] = slope[n - 2]
  for (let i = 1; i < n - 1; i++) {
    tangent[i] = slope[i - 1] * slope[i] <= 0 ? 0 : (slope[i - 1] + slope[i]) / 2
  }

  // Fritsch-Carlson limiter: keep the curve inside each segment's value range.
  for (let i = 0; i < n - 1; i++) {
    if (slope[i] === 0) {
      tangent[i] = 0
      tangent[i + 1] = 0
      continue
    }
    const a = tangent[i] / slope[i]
    const b = tangent[i + 1] / slope[i]
    const magnitude = a * a + b * b
    if (magnitude > 9) {
      const scale = 3 / Math.sqrt(magnitude)
      tangent[i] = scale * a * slope[i]
      tangent[i + 1] = scale * b * slope[i]
    }
  }

  let d = `M ${round(points[0].x)} ${round(points[0].y)}`
  for (let i = 0; i < n - 1; i++) {
    const third = dx[i] / 3
    const c1x = points[i].x + third
    const c1y = points[i].y + tangent[i] * third
    const c2x = points[i + 1].x - third
    const c2y = points[i + 1].y - tangent[i + 1] * third
    d += ` C ${round(c1x)} ${round(c1y)}, ${round(c2x)} ${round(c2y)}, ${round(points[i + 1].x)} ${round(points[i + 1].y)}`
  }
  return d
}

// Builds a smoothed line in the sparkline's viewBox units. `max` lets two
// series share one scale so their heights stay comparable; it defaults to the
// series' own peak.
export function sparklinePath(values, { width = 100, top = 2, bottom = 22, max } = {}) {
  const data = (values || []).map((v) => (Number.isFinite(v) && v > 0 ? v : 0))
  if (data.length < 2) return ''

  const peak = Number.isFinite(max) && max > 0 ? max : Math.max(...data, 1)
  const span = bottom - top
  const last = data.length - 1
  const points = data.map((value, index) => ({
    x: (index / last) * width,
    y: bottom - Math.min(value / peak, 1) * span,
  }))

  return monotonePath(points)
}
