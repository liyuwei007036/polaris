package control

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
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
	if err := s.store.AppendAudit(r.Context(), operator.ID, "fail2ban_jail.created", "fail2ban_jail", created.ID, "polaris fail2ban jail created"); err != nil {
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
	if err := s.store.AppendAudit(r.Context(), operator.ID, "fail2ban_jail.updated", "fail2ban_jail", updated.ID, "polaris fail2ban jail updated"); err != nil {
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
	if err := s.store.AppendAudit(r.Context(), operator.ID, "fail2ban_jail.state_changed", "fail2ban_jail", r.PathValue("id"), "polaris fail2ban jail state changed"); err != nil {
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
	if err := s.store.AppendAudit(r.Context(), operator.ID, "fail2ban_jail.deleted", "fail2ban_jail", r.PathValue("id"), "polaris fail2ban jail deleted"); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// BannedAddress is one currently banned IP as reported by a node, resolved
// back to the jail — and, where polaris wrote it, the rule name an operator
// configured — that banned it.
type BannedAddress struct {
	NodeID   string `json:"node_id"`
	Jail     string `json:"jail"`
	RuleName string `json:"rule_name,omitempty"`
	Managed  bool   `json:"managed"`
	IP       string `json:"ip"`
	Location string `json:"location,omitempty"`
	BannedAt string `json:"banned_at,omitempty"`
	UnbanAt  string `json:"unban_at,omitempty"`
}

// listBannedAddresses reports every address the nodes currently hold banned.
// The data comes from the last metrics each agent pushed; nothing is stored
// separately, so an address released on the server disappears here as soon as
// that server reports again.
func (s *Server) listBannedAddresses(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	reports, _, err := s.store.AllNodeMetrics(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	jails, err := s.store.ListFail2BanJails(r.Context(), "")
	if err != nil {
		writeError(w, err)
		return
	}
	ruleNames := map[string]string{}
	for _, jail := range jails {
		ruleNames[jail.NodeID+"\x00"+fail2banFilterPrefix+jail.Name] = jail.Name
	}
	banned := []BannedAddress{}
	for nodeID, raw := range reports {
		var metrics storedMetrics
		if err := json.Unmarshal(raw, &metrics); err != nil || metrics.Fail2Ban == nil {
			continue
		}
		for _, jail := range metrics.Fail2Ban.Jails {
			for _, ban := range jail.Banned {
				banned = append(banned, BannedAddress{
					NodeID: nodeID, Jail: jail.Name, RuleName: ruleNames[nodeID+"\x00"+jail.Name],
					Managed: jail.Managed, IP: ban.IP, Location: s.ipLocator.Locate(ban.IP),
					BannedAt: ban.BannedAt, UnbanAt: ban.UnbanAt,
				})
			}
		}
	}
	sort.Slice(banned, func(left, right int) bool {
		if banned[left].BannedAt != banned[right].BannedAt {
			return banned[left].BannedAt > banned[right].BannedAt
		}
		return banned[left].IP < banned[right].IP
	})
	writeJSON(w, http.StatusOK, map[string]any{"banned": banned})
}

// unbanAddress asks one node to release one address from one jail. It is a
// runtime action rather than a configuration change, so it applies to the
// operator's own jails too, not only the ones polaris wrote.
func (s *Server) unbanAddress(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input struct {
		Jail string `json:"jail"`
		IP   string `json:"ip"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Jail = strings.TrimSpace(input.Jail)
	input.IP = strings.TrimSpace(input.IP)
	if input.Jail == "" || net.ParseIP(input.IP) == nil {
		writeError(w, userErrorf("需要提供有效的封禁规则和 IP 地址"))
		return
	}
	nodeID := r.PathValue("id")
	payload, err := json.Marshal(map[string]string{"jail": input.Jail, "ip": input.IP})
	if err != nil {
		writeError(w, err)
		return
	}
	digest := sha256.Sum256(payload)
	hash := hex.EncodeToString(digest[:])
	// The idempotency key carries the clock so repeated unban attempts for the
	// same address are separate tasks: an address can be banned again.
	task, err := s.DispatchTask(r.Context(), Task{
		NodeID: nodeID, OperatorID: operator.ID, Kind: "fail2ban.unban",
		IdempotencyKey: fmt.Sprintf("fail2ban-unban-%s-%d", hash, nowUnix()),
		Payload:        string(payload), ExpectedHash: hash,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "fail2ban.unban_requested", "node", nodeID, "polaris fail2ban unban requested"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, task)
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
	if err := s.store.AppendAudit(r.Context(), operator.ID, "fail2ban.publish_requested", "node", nodeID, "polaris fail2ban task requested"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, task)
}
