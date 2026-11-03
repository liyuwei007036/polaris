package control

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"
)

// nodeMetricPoints keeps a coarse host-resource history so the node detail
// page can draw the same CPU / memory / disk / network curves as ref/openlat
// instead of only showing the latest sample.
const (
	metricPointInterval = 60 * time.Second
	metricPointRetain   = 8 * 24 * 3600
)

type metricPoint struct {
	TS        int64   `json:"ts"`
	CPU       float64 `json:"cpu"`
	MemUsed   uint64  `json:"mem_used"`
	MemTotal  uint64  `json:"mem_total"`
	DiskUsed  uint64  `json:"disk_used"`
	DiskTotal uint64  `json:"disk_total"`
	NetRx     uint64  `json:"net_rx"`
	NetTx     uint64  `json:"net_tx"`
}

// recordNodeMetricPoint stores at most one sample per minute per node. A node
// reports every few seconds; keeping every report would multiply the database
// for no visible gain on a chart whose thinnest range is one hour.
func (s *Server) recordNodeMetricPoint(nodeID string, host *HostResources) {
	if host == nil {
		return
	}
	now := time.Now().Unix()
	bucket := now - now%int64(metricPointInterval/time.Second)
	var last int64
	if err := s.store.db.QueryRow(`SELECT COALESCE(MAX(ts), 0) FROM node_metric_points WHERE node_id = ?`, nodeID).Scan(&last); err == nil && last >= bucket {
		return
	}
	_, _ = s.store.db.Exec(`INSERT OR REPLACE INTO node_metric_points
		(node_id, ts, cpu, mem_used, mem_total, disk_used, disk_total, net_rx, net_tx)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nodeID, bucket, host.CPUPercent, host.MemoryUsed, host.MemoryTotal,
		host.DiskUsed, host.DiskTotal, host.NetRxRate, host.NetTxRate)
}

// nodeHistory serves the detail page's charts. Resource curves come from the
// metric points; latency curves come from the raw probe samples so the client
// can derive per-probe statistics exactly like ref/openlat does.
func (s *Server) nodeHistory(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	nodeID := r.PathValue("id")
	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	if hours <= 0 || hours > 24*30 {
		hours = 6
	}
	if r.URL.Query().Get("series") == "ping" {
		s.nodePingHistory(w, r, nodeID, hours)
		return
	}
	cutoff := time.Now().Add(-time.Duration(hours) * time.Hour).Unix()
	rows, err := s.store.db.QueryContext(r.Context(),
		`SELECT ts, cpu, mem_used, mem_total, disk_used, disk_total, net_rx, net_tx
		 FROM node_metric_points WHERE node_id = ? AND ts >= ? ORDER BY ts ASC`, nodeID, cutoff)
	if err != nil {
		writeError(w, err)
		return
	}
	defer rows.Close()
	points := make([]metricPoint, 0, 256)
	for rows.Next() {
		var p metricPoint
		if err := rows.Scan(&p.TS, &p.CPU, &p.MemUsed, &p.MemTotal, &p.DiskUsed, &p.DiskTotal, &p.NetRx, &p.NetTx); err != nil {
			writeError(w, err)
			return
		}
		points = append(points, p)
	}
	if err := rows.Err(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"metrics": points})
}

// pingHistoryPoint is the wire shape ref/openlat consumes: one sample per probe
// per time bucket, with latency null when every packet in the bucket was lost.
type pingHistoryPoint struct {
	TaskID  any      `json:"task_id"`
	TS      int64    `json:"ts"`
	Latency *float64 `json:"latency"`
	Loss    *float64 `json:"loss,omitempty"`
}

// nodePingHistory returns probe samples for the requested window. Samples are
// averaged into at most `points` buckets per probe so a 24 hour window stays a
// few hundred points instead of tens of thousands.
func (s *Server) nodePingHistory(w http.ResponseWriter, r *http.Request, nodeID string, hours int) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("points"))
	if limit <= 0 || limit > 2000 {
		limit = 240
	}
	cutoff := time.Now().Add(-time.Duration(hours) * time.Hour).Unix()
	rows, err := s.store.db.QueryContext(r.Context(),
		`SELECT probe_id, probe_name, target, ts, latency_ms
		 FROM node_latency_points WHERE node_id = ? AND ts >= ? ORDER BY ts ASC`, nodeID, cutoff)
	if err != nil {
		writeError(w, err)
		return
	}
	defer rows.Close()

	type probeSeries struct {
		id     string
		name   string
		points []rawPingPoint
	}
	order := make([]string, 0, 4)
	series := make(map[string]*probeSeries)
	for rows.Next() {
		var p rawPingPoint
		if err := rows.Scan(&p.probeID, &p.probeName, &p.target, &p.ts, &p.latencyMs); err != nil {
			writeError(w, err)
			return
		}
		key := p.probeID
		if key == "" {
			key = p.target
		}
		entry, ok := series[key]
		if !ok {
			entry = &probeSeries{id: key}
			series[key] = entry
			order = append(order, key)
		}
		if p.probeName != "" {
			entry.name = p.probeName
		} else if entry.name == "" {
			entry.name = probeDisplayName(p.target)
		}
		entry.points = append(entry.points, p)
	}
	if err := rows.Err(); err != nil {
		writeError(w, err)
		return
	}

	bucket := int64(hours) * hourSeconds / int64(limit)
	if bucket < 1 {
		bucket = 1
	}

	points := make([]pingHistoryPoint, 0, len(order)*limit)
	probes := make(map[string]string, len(order))
	losses := make(map[string]float64, len(order))
	for _, key := range order {
		entry := series[key]
		probes[key] = entry.name

		type accum struct {
			latSum  float64
			latN    int
			lossN   int
			total   int
		}
		buckets := make(map[int64]*accum)
		lost := 0
		for _, p := range entry.points {
			slot := (p.ts / bucket) * bucket
			acc, ok := buckets[slot]
			if !ok {
				acc = &accum{}
				buckets[slot] = acc
			}
			acc.total++
			if p.latencyMs >= 0 {
				acc.latSum += float64(p.latencyMs)
				acc.latN++
			} else {
				acc.lossN++
				lost++
			}
		}
		if len(entry.points) > 0 {
			losses[key] = round1(float64(lost) / float64(len(entry.points)) * 100)
		}

		slots := make([]int64, 0, len(buckets))
		for slot := range buckets {
			slots = append(slots, slot)
		}
		sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })
		for _, slot := range slots {
			acc := buckets[slot]
			point := pingHistoryPoint{TaskID: taskIDValue(key), TS: slot}
			if acc.latN > 0 {
				lat := round1(acc.latSum / float64(acc.latN))
				point.Latency = &lat
			}
			if acc.total > 0 {
				loss := round1(float64(acc.lossN) / float64(acc.total) * 100)
				point.Loss = &loss
			}
			points = append(points, point)
		}
	}
	sort.SliceStable(points, func(i, j int) bool { return points[i].TS < points[j].TS })

	writeJSON(w, http.StatusOK, map[string]any{
		"metrics": []metricPoint{},
		"ping":    points,
		"probes":  probes,
		"loss":    losses,
	})
}

// taskIDValue keeps numeric probe IDs numeric for clients that key by number,
// while legacy target-based probes remain strings.
func taskIDValue(key string) any {
	if id, err := strconv.Atoi(key); err == nil {
		return id
	}
	return key
}

func round1(v float64) float64 {
	return float64(int64(v*10+0.5)) / 10
}

func (s *Server) pruneNodeMetricPoints(ctx context.Context) {
	_, _ = s.store.db.ExecContext(ctx, `DELETE FROM node_metric_points WHERE ts < ?`, time.Now().Unix()-metricPointRetain)
}
