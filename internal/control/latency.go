package control

import (
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/liyuwei007036/polaris/internal/wire"
)

const (
	hourBuckets = 24
	hourSeconds = 3600
)

type storedLatency struct {
	Latency    *float64            `json:"latency"`
	Loss       float64             `json:"loss"`
	Probe      string              `json:"probe"`
	AvgLatency *float64            `json:"avgLatency"`
	AvgLoss    *float64            `json:"avgLoss"`
	Jitter     *float64            `json:"jitter"`
	Min        *float64            `json:"min"`
	Max        *float64            `json:"max"`
	Probes     []storedProbeStat   `json:"probes"`
	Hours      []storedLatencyHour `json:"hours"`
	None       bool                `json:"none,omitempty"`
	Failed     bool                `json:"failed,omitempty"`
}

type storedProbeStat struct {
	ID         int                 `json:"id"`
	Name       string              `json:"name"`
	Latency    *float64            `json:"latency"`
	Jitter     *float64            `json:"jitter"`
	Loss       float64             `json:"loss"`
	Min        *float64            `json:"min"`
	Max        *float64            `json:"max"`
	Hours      []storedLatencyHour `json:"hours"`
	AvgLatency *float64            `json:"avgLatency"`
	AvgLoss    *float64            `json:"avgLoss"`
}

type storedLatencyHour struct {
	TS      int64    `json:"ts"`
	Latency *float64 `json:"latency"`
	Loss    *float64 `json:"loss"`
}

type probeHistory struct {
	points []rawPingPoint
}

type rawPingPoint struct {
	probeID   string
	probeName string
	target    string
	ts        int64
	latencyMs int
}

var (
	latencyMu      sync.Mutex
	nodeRawHistory = make(map[string]*probeHistory)
)

func hourSlots(count int) []int64 {
	nowHour := (time.Now().Unix() / hourSeconds) * hourSeconds
	slots := make([]int64, count)
	for i := 0; i < count; i++ {
		slots[i] = nowHour - int64(count-1-i)*hourSeconds
	}
	return slots
}

func probeDisplayName(target string) string {
	clean := strings.TrimSuffix(target, ":53")
	clean = strings.TrimSuffix(clean, ":80")
	clean = strings.TrimSuffix(clean, ":443")
	switch clean {
	case "1.1.1.1", "1.0.0.1":
		return "Cloudflare (" + clean + ")"
	case "8.8.8.8", "8.8.4.4":
		return "Google (" + clean + ")"
	default:
		return target
	}
}

func (s *Server) recordNodePingResults(nodeID string, results []wire.PingResult, prev *storedLatency) *storedLatency {
	latencyMu.Lock()
	defer latencyMu.Unlock()

	hist, ok := nodeRawHistory[nodeID]
	if !ok {
		hist = &probeHistory{}
		if rows, err := s.store.db.Query(`SELECT probe_id, probe_name, target, ts, latency_ms FROM node_latency_points WHERE node_id = ? AND ts >= ? ORDER BY ts ASC`, nodeID, time.Now().Unix()-28*hourSeconds); err == nil {
			for rows.Next() {
				var p rawPingPoint
				if rows.Scan(&p.probeID, &p.probeName, &p.target, &p.ts, &p.latencyMs) == nil {
					hist.points = append(hist.points, p)
				}
			}
			_ = rows.Close()
		}
		nodeRawHistory[nodeID] = hist
	}

	now := time.Now().Unix()

	// If results provided from agent, append them
	for _, res := range results {
		ts := res.Timestamp
		if ts == 0 {
			ts = now
		}
		probeID := res.ProbeID
		if probeID == "" {
			probeID = res.Target
		}
		_, insertErr := s.store.db.Exec(`INSERT OR IGNORE INTO node_latency_points (node_id, probe_id, probe_name, target, ts, latency_ms) VALUES (?, ?, ?, ?, ?, ?)`, nodeID, probeID, res.ProbeName, res.Target, ts, res.LatencyMs)
		if insertErr != nil {
			continue
		}
		var inserted int
		_ = s.store.db.QueryRow(`SELECT COUNT(*) FROM node_latency_points WHERE node_id = ? AND probe_id = ? AND target = ? AND ts = ?`, nodeID, probeID, res.Target, ts).Scan(&inserted)
		// INSERT OR IGNORE is the heartbeat dedupe boundary. The point may have
		// existed already, so only append when it is not already in memory.
		duplicate := false
		for _, old := range hist.points {
			if old.probeID == probeID && old.target == res.Target && old.ts == ts {
				duplicate = true
				break
			}
		}
		if duplicate || inserted == 0 {
			continue
		}
		hist.points = append(hist.points, rawPingPoint{
			probeID:   probeID,
			probeName: res.ProbeName,
			target:    res.Target,
			ts:        ts,
			latencyMs: res.LatencyMs,
		})
	}

	// Keep up to 28 hours of points
	cutoff := now - 28*hourSeconds
	keepIdx := 0
	for i, p := range hist.points {
		if p.ts >= cutoff {
			keepIdx = i
			break
		}
	}
	if keepIdx > 0 {
		hist.points = hist.points[keepIdx:]
	}

	// If points are empty (e.g. initial agent connect without ping results yet),
	// return empty 24 slots with none: true
	if len(hist.points) == 0 {
		if prev != nil {
			return prev
		}
		slots := hourSlots(hourBuckets)
		hours := make([]storedLatencyHour, len(slots))
		for i, s := range slots {
			hours[i] = storedLatencyHour{TS: s, Latency: nil, Loss: nil}
		}
		return &storedLatency{
			Latency: nil,
			Loss:    0,
			Probe:   "",
			Probes:  []storedProbeStat{},
			Hours:   hours,
			None:    true,
		}
	}

	return computeLatencySummary(hist.points)
}

func computeLatencySummary(points []rawPingPoint) *storedLatency {
	slots := hourSlots(hourBuckets)

	// Group by configured probe ID, matching ref's task_id semantics. Older
	// agents have no ID, so their target remains the compatibility key.
	byTarget := make(map[string][]rawPingPoint)
	probeNames := make(map[string]string)
	probeTargets := make(map[string]string)
	for _, p := range points {
		key := p.probeID
		if key == "" {
			key = p.target
		}
		byTarget[key] = append(byTarget[key], p)
		if p.probeName != "" {
			probeNames[key] = p.probeName
		}
		probeTargets[key] = p.target
	}

	var probes []storedProbeStat
	probeID := 1

	for target, pts := range byTarget {
		var validLats []float64
		lossCount := 0

		type slotAccum struct {
			latSum    float64
			latCount  int
			lossCount int
		}
		accums := make(map[int64]*slotAccum)
		for _, s := range slots {
			accums[s] = &slotAccum{}
		}

		for _, p := range pts {
			bucket := (p.ts / hourSeconds) * hourSeconds
			acc, found := accums[bucket]
			if !found {
				acc = &slotAccum{}
				accums[bucket] = acc
			}

			if p.latencyMs >= 0 {
				lat := float64(p.latencyMs)
				validLats = append(validLats, lat)
				acc.latSum += lat
				acc.latCount++
			} else {
				lossCount++
				acc.lossCount++
			}
		}

		var minVal, maxVal, lastLat, jitterVal *float64
		if len(validLats) > 0 {
			minL := validLats[0]
			maxL := validLats[0]
			for _, v := range validLats[1:] {
				if v < minL {
					minL = v
				}
				if v > maxL {
					maxL = v
				}
			}
			minVal = &minL
			maxVal = &maxL
			last := validLats[len(validLats)-1]
			lastLat = &last

			if len(validLats) >= 2 {
				var diffSum float64
				for i := 1; i < len(validLats); i++ {
					diffSum += math.Abs(validLats[i] - validLats[i-1])
				}
				j := math.Round((diffSum/float64(len(validLats)-1))*10) / 10
				jitterVal = &j
			}
		}

		lossPct := 0.0
		if len(pts) > 0 {
			lossPct = math.Round((float64(lossCount)/float64(len(pts)))*1000) / 10
		}

		hours := make([]storedLatencyHour, len(slots))
		var hourLats []float64
		var hourLosses []float64

		for i, s := range slots {
			acc := accums[s]
			var latPtr *float64
			var lossPtr *float64

			totalInSlot := acc.latCount + acc.lossCount
			if totalInSlot > 0 {
				if acc.latCount > 0 {
					l := math.Round((acc.latSum/float64(acc.latCount))*10) / 10
					latPtr = &l
					hourLats = append(hourLats, l)
				}
				lp := math.Round((float64(acc.lossCount)/float64(totalInSlot))*1000) / 10
				lossPtr = &lp
				hourLosses = append(hourLosses, lp)
			}

			hours[i] = storedLatencyHour{
				TS:      s,
				Latency: latPtr,
				Loss:    lossPtr,
			}
		}

		var avgLat, avgLoss *float64
		if len(hourLats) > 0 {
			var sum float64
			for _, v := range hourLats {
				sum += v
			}
			v := math.Round((sum/float64(len(hourLats)))*10) / 10
			avgLat = &v
		}
		if len(hourLosses) > 0 {
			var sum float64
			for _, v := range hourLosses {
				sum += v
			}
			v := math.Round((sum/float64(len(hourLosses)))*10) / 10
			avgLoss = &v
		}

		name := probeNames[target]
		if name == "" {
			name = probeDisplayName(probeTargets[target])
		}
		stableID := probeID
		if parsed, err := strconv.Atoi(target); err == nil && parsed > 0 {
			stableID = parsed
		}
		probes = append(probes, storedProbeStat{
			ID:         stableID,
			Name:       name,
			Latency:    lastLat,
			Jitter:     jitterVal,
			Loss:       lossPct,
			Min:        minVal,
			Max:        maxVal,
			Hours:      hours,
			AvgLatency: avgLat,
			AvgLoss:    avgLoss,
		})
		probeID++
	}

	// Merge hours across all probes for composite 24h track
	compositeHours := make([]storedLatencyHour, len(slots))
	for i, s := range slots {
		var latSum, lossSum float64
		var latN, lossN int
		for _, p := range probes {
			h := p.Hours[i]
			if h.Latency != nil {
				latSum += *h.Latency
				latN++
			}
			if h.Loss != nil {
				lossSum += *h.Loss
				lossN++
			}
		}

		var latPtr, lossPtr *float64
		if latN > 0 {
			l := math.Round((latSum/float64(latN))*10) / 10
			latPtr = &l
		}
		if lossN > 0 {
			lp := math.Round((lossSum/float64(lossN))*10) / 10
			lossPtr = &lp
		}

		compositeHours[i] = storedLatencyHour{
			TS:      s,
			Latency: latPtr,
			Loss:    lossPtr,
		}
	}

	var overallAvgLat, overallAvgLoss, overallJitter, overallMin, overallMax *float64
	var validCompLats []float64
	var validCompLosses []float64

	for _, h := range compositeHours {
		if h.Latency != nil {
			validCompLats = append(validCompLats, *h.Latency)
		}
		if h.Loss != nil {
			validCompLosses = append(validCompLosses, *h.Loss)
		}
	}

	if len(validCompLats) > 0 {
		var sum float64
		minL := validCompLats[0]
		maxL := validCompLats[0]
		for _, v := range validCompLats {
			sum += v
			if v < minL {
				minL = v
			}
			if v > maxL {
				maxL = v
			}
		}
		al := math.Round((sum/float64(len(validCompLats)))*10) / 10
		overallAvgLat = &al
		overallMin = &minL
		overallMax = &maxL

		if len(validCompLats) >= 2 {
			var diffSum float64
			for i := 1; i < len(validCompLats); i++ {
				diffSum += math.Abs(validCompLats[i] - validCompLats[i-1])
			}
			j := math.Round((diffSum/float64(len(validCompLats)-1))*10) / 10
			overallJitter = &j
		}
	}

	if len(validCompLosses) > 0 {
		var sum float64
		for _, v := range validCompLosses {
			sum += v
		}
		al := math.Round((sum/float64(len(validCompLosses)))*10) / 10
		overallAvgLoss = &al
	}

	var bestProbe *storedProbeStat
	for i := range probes {
		p := &probes[i]
		if p.Latency != nil {
			if bestProbe == nil || *p.Latency < *bestProbe.Latency {
				bestProbe = p
			}
		}
	}

	var curLatency *float64
	var curLoss float64
	var curProbe string
	if len(probes) == 0 {
		return &storedLatency{
			Latency:    nil,
			Loss:       0,
			Probe:      "",
			AvgLatency: overallAvgLat,
			AvgLoss:    overallAvgLoss,
			Jitter:     overallJitter,
			Min:        overallMin,
			Max:        overallMax,
			Probes:     probes,
			Hours:      compositeHours,
			None:       true,
		}
	}
	if bestProbe == nil {
		first := probes[0]
		curLoss = 100
		curProbe = first.Name
	} else {
		curLatency = bestProbe.Latency
		curLoss = bestProbe.Loss
		curProbe = bestProbe.Name
		if bestProbe.Jitter != nil && overallJitter == nil {
			overallJitter = bestProbe.Jitter
		}
	}

	return &storedLatency{
		Latency:    curLatency,
		Loss:       curLoss,
		Probe:      curProbe,
		AvgLatency: overallAvgLat,
		AvgLoss:    overallAvgLoss,
		Jitter:     overallJitter,
		Min:        overallMin,
		Max:        overallMax,
		Probes:     probes,
		Hours:      compositeHours,
	}
}
