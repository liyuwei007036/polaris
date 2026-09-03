package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
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
