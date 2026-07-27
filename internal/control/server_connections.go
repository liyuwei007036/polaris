package control

import (
	"encoding/json"
	"net/http"
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
