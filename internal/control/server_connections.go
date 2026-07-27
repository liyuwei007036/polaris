package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// nodeConnections returns the connection list exactly as the agent reported
// it. Fields the agent could not observe stay absent instead of being
// estimated by the master.
func (s *Server) nodeConnections(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	report, updatedAt, err := s.store.NodeMetrics(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	var metrics struct {
		CollectedAt  string          `json:"collected_at"`
		Connections  json.RawMessage `json:"connections"`
		Capabilities json.RawMessage `json:"capabilities"`
	}
	if err := json.Unmarshal(report, &metrics); err != nil {
		writeError(w, err)
		return
	}
	if len(metrics.Connections) == 0 {
		metrics.Connections = json.RawMessage("[]")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"node_id":      r.PathValue("id"),
		"collected_at": metrics.CollectedAt,
		"reported_at":  updatedAt,
		"connections":  metrics.Connections,
		"capabilities": metrics.Capabilities,
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
		case <-ticker.C:
			if !writeEvent("keepalive", map[string]any{}) {
				return
			}
		}
	}
}
