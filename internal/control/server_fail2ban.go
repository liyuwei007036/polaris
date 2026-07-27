package control

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
)

func (s *Server) listFail2BanJails(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	jails, err := s.store.ListFail2BanJails(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jails": jails})
}

func (s *Server) createFail2BanJail(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var jail Fail2BanJail
	if !decodeJSON(w, r, &jail) {
		return
	}
	jail.NodeID = r.PathValue("id")
	created, err := s.store.CreateFail2BanJail(r.Context(), jail)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "fail2ban_jail.created", "fail2ban_jail", created.ID, "sb-control fail2ban jail created"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateFail2BanJail(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var jail Fail2BanJail
	if !decodeJSON(w, r, &jail) {
		return
	}
	jail.ID = r.PathValue("id")
	updated, err := s.store.UpdateFail2BanJail(r.Context(), jail)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "fail2ban_jail.updated", "fail2ban_jail", updated.ID, "sb-control fail2ban jail updated"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) setFail2BanJailEnabled(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := s.store.SetFail2BanJailEnabled(r.Context(), r.PathValue("id"), input.Enabled); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "fail2ban_jail.state_changed", "fail2ban_jail", r.PathValue("id"), "sb-control fail2ban jail state changed"); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteFail2BanJail(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.DeleteFail2BanJail(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "fail2ban_jail.deleted", "fail2ban_jail", r.PathValue("id"), "sb-control fail2ban jail deleted"); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) publishNodeFail2Ban(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	nodeID := r.PathValue("id")
	jailConfiguration, filters, err := s.store.CompileNodeFail2Ban(r.Context(), nodeID)
	if err != nil {
		writeError(w, err)
		return
	}
	payload, err := json.Marshal(map[string]any{"jail": jailConfiguration, "filters": filters})
	if err != nil {
		writeError(w, err)
		return
	}
	digest := sha256.Sum256(payload)
	hash := hex.EncodeToString(digest[:])
	task, err := s.DispatchTask(r.Context(), Task{NodeID: nodeID, OperatorID: operator.ID, Kind: "fail2ban.apply", IdempotencyKey: "fail2ban-" + hash, Payload: string(payload), ExpectedHash: hash})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "fail2ban.publish_requested", "node", nodeID, "sb-control fail2ban task requested"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, task)
}
