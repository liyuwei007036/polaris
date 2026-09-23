package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/liyuwei007036/polaris/internal/security"
)

// nodeConnections returns the connection list exactly as the agent last
// pushed it. Connections are real-time state held in memory, never persisted,
// so a node that has stopped pushing reports an empty list rather than a
// stale one.
func (s *Server) nodeConnections(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	nodeID := r.PathValue("id")
	snapshot, ok := s.connHub.node(nodeID)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"node_id": nodeID, "connections": json.RawMessage("[]")})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"node_id":      nodeID,
		"collected_at": snapshot.CollectedAt,
		"connections":  snapshot.Connections,
	})
}

// browserConnectionsStream pushes real-time, all-nodes connection snapshots
// to an authenticated browser over Server-Sent Events. The browser opens this
// once and receives every subsequent update as agents push them; it never
// needs to poll or pick a single node to view.
func (s *Server) browserConnectionsStream(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, errors.New("streaming is unavailable"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	writeEvent := func(event string, payload any) bool {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, encoded); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !writeEvent("snapshot", map[string]any{"nodes": s.connHub.snapshot()}) {
		return
	}
	ch := s.connHub.subscribe()
	defer s.connHub.unsubscribe(ch)
	// The fleet total the master sums once per reporting round. It is its own
	// event because it is a different measurement from any one node's push:
	// browsers chart this series directly instead of adding up whatever had
	// arrived by the time they redrew.
	totalsCh := s.connHub.subscribeTotals()
	defer s.connHub.unsubscribeTotals(totalsCh)
	// One total straight away, so a console that opened mid-round has a reading
	// to show instead of an empty chart until the next one closes.
	now := time.Now()
	if !writeEvent("totals", s.connHub.totals(now, s.connActivity.popular(now, popularNodeLimit))) {
		return
	}
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case snap := <-ch:
			if !writeEvent("node", snap) {
				return
			}
		case totals := <-totalsCh:
			if !writeEvent("totals", totals) {
				return
			}
		case <-ticker.C:
			if !writeEvent("keepalive", map[string]any{}) {
				return
			}
		}
	}
}

// browserLiveStream carries invalidation events for live operational data.
// Pages fetch one initial snapshot, then refresh only when an agent or task
// actually changes state.
func (s *Server) browserLiveStream(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, errors.New("streaming is unavailable"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	writeEvent := func(event string, payload any) bool {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, encoded); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !writeEvent("ready", map[string]any{}) {
		return
	}
	ch := s.liveHub.subscribe()
	defer s.liveHub.unsubscribe(ch)
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-ch:
			if !writeEvent("change", event) {
				return
			}
		case <-ticker.C:
			if !writeEvent("keepalive", map[string]any{}) {
				return
			}
		}
	}
}

// listDeviceConnections returns paginated historical connection records from SQLite.
func (s *Server) listDeviceConnections(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	page, err := security.ParsePositiveInt(r.URL.Query().Get("page"), 1, 1_000_000)
	if err != nil {
		writeError(w, err)
		return
	}
	pageSize, err := security.ParsePositiveInt(r.URL.Query().Get("page_size"), 20, 200)
	if err != nil {
		writeError(w, err)
		return
	}

	filter := ConnectionRecordFilter{
		NodeID:       r.URL.Query().Get("node_id"),
		SourceIP:     r.URL.Query().Get("ip"),
		User:         r.URL.Query().Get("user"),
		Network:      r.URL.Query().Get("network"),
		OutboundName: r.URL.Query().Get("outbound"),
		Keyword:      r.URL.Query().Get("keyword"),
	}
	if startRaw := r.URL.Query().Get("start_time"); startRaw != "" {
		if t, err := time.Parse(time.RFC3339, startRaw); err == nil {
			filter.StartTime = t.Unix()
		} else if sec, err := strconv.ParseInt(startRaw, 10, 64); err == nil {
			filter.StartTime = sec
		}
	}
	if endRaw := r.URL.Query().Get("end_time"); endRaw != "" {
		if t, err := time.Parse(time.RFC3339, endRaw); err == nil {
			filter.EndTime = t.Unix()
		} else if sec, err := strconv.ParseInt(endRaw, 10, 64); err == nil {
			filter.EndTime = sec
		}
	}

	records, total, err := s.store.ListConnectionRecords(r.Context(), filter, page, pageSize)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"records": records,
		"total":   total,
	})
}

// popularDevices returns aggregated metrics and top ranked client devices.
func (s *Server) popularDevices(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	rangeStr := r.URL.Query().Get("range")
	var since time.Time
	now := time.Now()
	switch rangeStr {
	case "24h":
		since = now.Add(-24 * time.Hour)
	case "30d":
		since = now.Add(-30 * 24 * time.Hour)
	default:
		rangeStr = "7d"
		since = now.Add(-7 * 24 * time.Hour)
	}

	limit, _ := security.ParsePositiveInt(r.URL.Query().Get("limit"), 20, 100)

	devices, err := s.store.PopularDevices(r.Context(), since, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	summary, err := s.store.DeviceSummaryStats(r.Context(), since)
	if err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"range":   rangeStr,
		"summary": summary,
		"devices": devices,
	})
}

