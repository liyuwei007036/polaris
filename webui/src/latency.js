// Port of ref/openlat/src/lib/api.ts's ping summary. The control plane hands
// back raw probe samples and the page derives latency, jitter, loss and the
// hourly series here, so the detail page follows the reference behaviour
// instead of trusting a pre-aggregated payload.

const HOUR_SECONDS = 3600
const HOUR_BUCKETS = 24

function jitterOf(values) {
  const clean = values.filter((v) => v !== null && v !== undefined)
  if (clean.length < 2) return null
  let sum = 0
  for (let i = 1; i < clean.length; i++) sum += Math.abs(clean[i] - clean[i - 1])
  return sum / (clean.length - 1)
}

function hourSlots(count = HOUR_BUCKETS) {
  const nowHour = Math.floor(Date.now() / 1000 / HOUR_SECONDS) * HOUR_SECONDS
  return Array.from({ length: count }, (_, i) => nowHour - (count - 1 - i) * HOUR_SECONDS)
}

function hourlyBuckets(points, fallbackLoss, slots) {
  const byHour = new Map()
  for (const p of points) {
    const hour = Math.floor(p.ts / HOUR_SECONDS) * HOUR_SECONDS
    const row = byHour.get(hour) ?? { lat: 0, latN: 0, loss: 0, lossN: 0 }
    if (p.latency !== null && p.latency !== undefined) {
      row.lat += p.latency
      row.latN++
    }
    row.loss += p.loss ?? fallbackLoss
    row.lossN++
    byHour.set(hour, row)
  }
  return slots.map((ts) => {
    const row = byHour.get(ts)
    return {
      ts,
      latency: row && row.latN > 0 ? row.lat / row.latN : null,
      loss: row && row.lossN > 0 ? row.loss / row.lossN : null,
    }
  })
}

// 把多个探测按小时求均值，合成单条 24 小时序列。
export function mergeHours(probes, slots) {
  return slots.map((ts, i) => {
    let latSum = 0
    let latN = 0
    let lossSum = 0
    let lossN = 0
    for (const probe of probes) {
      const hour = probe.hours[i]
      if (!hour) continue
      if (hour.latency !== null) {
        latSum += hour.latency
        latN++
      }
      if (hour.loss !== null) {
        lossSum += hour.loss
        lossN++
      }
    }
    return {
      ts,
      latency: latN > 0 ? latSum / latN : null,
      loss: lossN > 0 ? lossSum / lossN : null,
    }
  })
}

function mean(values) {
  return values.length > 0 ? values.reduce((s, v) => s + v, 0) / values.length : null
}

export function summarizePing(history) {
  const sorted = [...(history?.ping ?? [])].sort((a, b) => a.ts - b.ts)
  const byProbe = new Map()
  for (const p of sorted) {
    const list = byProbe.get(p.task_id)
    if (list) list.push(p)
    else byProbe.set(p.task_id, [p])
  }

  const slots = hourSlots()
  const probes = []
  for (const [id, points] of byProbe) {
    const values = points.map((p) => p.latency)
    const clean = values.filter((v) => v !== null && v !== undefined)
    const loss = history?.loss?.[String(id)] ?? (points[points.length - 1]?.latency === null ? 100 : 0)
    const hours = hourlyBuckets(points, loss, slots)
    const lats = hours.map((h) => h.latency).filter((v) => v !== null)
    const losses = hours.map((h) => h.loss).filter((v) => v !== null)
    probes.push({
      id,
      name: history?.probes?.[String(id)] ?? `探测 ${id}`,
      points,
      latency: points[points.length - 1]?.latency ?? null,
      jitter: jitterOf(values),
      loss,
      min: clean.length > 0 ? Math.min(...clean) : null,
      max: clean.length > 0 ? Math.max(...clean) : null,
      hours,
      avgLatency: mean(lats),
      avgLoss: mean(losses),
    })
  }

  const hours = mergeHours(probes, slots)
  const avgLatency = mean(hours.map((h) => h.latency).filter((v) => v !== null))
  const avgLoss = mean(hours.map((h) => h.loss).filter((v) => v !== null))

  const alive = probes.filter((p) => p.latency !== null)
  const pick = alive.reduce((best, p) => (best === null || p.latency < best.latency ? p : best), null)

  if (probes.length === 0) {
    return { latency: null, loss: 0, probe: '', jitter: null, probes, hours, avgLatency, avgLoss, none: true }
  }
  if (!pick) {
    const first = probes[0]
    return { latency: null, loss: 100, probe: first.name, jitter: first.jitter, probes, hours, avgLatency, avgLoss }
  }
  return {
    latency: pick.latency,
    loss: pick.loss,
    probe: pick.name,
    jitter: pick.jitter,
    probes,
    hours,
    avgLatency,
    avgLoss,
  }
}
