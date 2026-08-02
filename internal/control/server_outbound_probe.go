package control

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
)

func (s *Server) testOutbound(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input struct {
		NodeID string `json:"node_id"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	node, err := s.store.GetNode(r.Context(), input.NodeID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !node.Online {
		writeError(w, ErrConflict)
		return
	}
	outboundID := r.PathValue("id")
	if _, err := s.store.outboundForTest(r.Context(), outboundID); err != nil {
		writeError(w, err)
		return
	}
	payload, _ := json.Marshal(map[string]string{"outbound_id": outboundID})
	nonce, err := newID()
	if err != nil {
		writeError(w, err)
		return
	}
	digest := sha256.Sum256([]byte(outboundID + ":" + nonce))
	task, err := s.DispatchTask(r.Context(), Task{
		NodeID: input.NodeID, OperatorID: operator.ID, Kind: "outbound.test",
		IdempotencyKey: "outbound-test-" + nonce, Payload: string(payload), ExpectedHash: hex.EncodeToString(digest[:]),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	_ = s.store.AppendAudit(r.Context(), operator.ID, "outbound.test_requested", "outbound", outboundID, "outbound connectivity test requested")
	writeJSON(w, http.StatusAccepted, task)
}
