package control

import (
	"encoding/json"
	"net"
	"net/http"
	"sort"
	"strings"
)

// Network protection holds nothing in this database. Every screen here asks the
// servers themselves what they are enforcing, and every change is applied by
// the server and confirmed before the operator is told it worked. A rule that
// exists in a record but not in a kernel protects nobody, which is exactly what
// showing stored rules used to hide.

// LiveFirewallRule is one rule a node reports as being in force. Raw is the
// server's own wording; the rest is what could be made out of it, and stays
// empty for a rule shaped in a way the node's parser does not recognize.
type LiveFirewallRule struct {
	NodeID   string `json:"node_id"`
	Family   string `json:"family,omitempty"`
	Table    string `json:"table,omitempty"`
	Chain    string `json:"chain,omitempty"`
	Handle   string `json:"handle,omitempty"`
	Action   string `json:"action,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Port     uint16 `json:"port,omitempty"`
	CIDR     string `json:"cidr,omitempty"`
	Location string `json:"location,omitempty"`
	Raw      string `json:"raw"`
}

// nodeFirewall is one node's answer, including the reason there is none.
type nodeFirewall struct {
	NodeID    string             `json:"node_id"`
	Available bool               `json:"available"`
	Tool      string             `json:"tool,omitempty"`
	Rules     []LiveFirewallRule `json:"rules"`
	Truncated bool               `json:"truncated,omitempty"`
	Error     string             `json:"error,omitempty"`
}

// LiveFail2BanJail is one automatic-banning rule a node reports as configured,
// together with whether Fail2Ban is really running it.
type LiveFail2BanJail struct {
	NodeID          string `json:"node_id"`
	Name            string `json:"name"`
	Managed         bool   `json:"managed"`
	LogPath         string `json:"log_path,omitempty"`
	FilterName      string `json:"filter_name,omitempty"`
	FailRegex       string `json:"fail_regex,omitempty"`
	MaxRetry        uint16 `json:"max_retry,omitempty"`
	FindTimeSeconds uint32 `json:"find_time_seconds,omitempty"`
	BanTimeSeconds  uint32 `json:"ban_time_seconds,omitempty"`
	Ports           string `json:"ports,omitempty"`
	Running         bool   `json:"running"`
	CurrentlyBanned string `json:"currently_banned,omitempty"`
	TotalBanned     string `json:"total_banned,omitempty"`
	Error           string `json:"error,omitempty"`
}

type nodeFail2Ban struct {
	NodeID    string             `json:"node_id"`
	Available bool               `json:"available"`
	Jails     []LiveFail2BanJail `json:"jails"`
	Error     string             `json:"error,omitempty"`
}

// agentFirewall mirrors what the agent encodes. It is decoded here rather than
// imported so the master never links the agent's host-command code.
type agentFirewall struct {
	Available bool `json:"available"`
	Tool      string `json:"tool"`
	Rules     []struct {
		Family   string `json:"family"`
		Table    string `json:"table"`
		Chain    string `json:"chain"`
		Handle   string `json:"handle"`
		Action   string `json:"action"`
		Protocol string `json:"protocol"`
		Port     uint16 `json:"port"`
		CIDR     string `json:"cidr"`
		Raw      string `json:"raw"`
	} `json:"rules"`
	Truncated bool   `json:"truncated"`
	Error     string `json:"error"`
}

type agentFail2Ban struct {
	Available bool `json:"available"`
	Jails     []struct {
		Name            string `json:"name"`
		Managed         bool   `json:"managed"`
		LogPath         string `json:"log_path"`
		FilterName      string `json:"filter_name"`
		FailRegex       string `json:"fail_regex"`
		MaxRetry        uint16 `json:"max_retry"`
		FindTimeSeconds uint32 `json:"find_time_seconds"`
		BanTimeSeconds  uint32 `json:"ban_time_seconds"`
		Ports           string `json:"ports"`
		Running         bool   `json:"running"`
		CurrentlyBanned string `json:"currently_banned"`
		TotalBanned     string `json:"total_banned"`
		Banned          []struct {
			IP       string `json:"ip"`
			BannedAt string `json:"banned_at"`
			UnbanAt  string `json:"unban_at"`
		} `json:"banned"`
		Error string `json:"error"`
	} `json:"jails"`
	Error string `json:"error"`
}

// listFirewall asks every node — or one, when the path names it — what its
// firewall currently enforces.
func (s *Server) listFirewall(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	nodeIDs, err := s.securityNodeIDs(r)
	if err != nil {
		writeError(w, err)
		return
	}
	answers := s.askNodes(r.Context(), nodeIDs, "firewall.query", "{}")
	nodes := make([]nodeFirewall, 0, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		nodes = append(nodes, s.decodeNodeFirewall(nodeID, answers[nodeID]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
}

// changeFirewallRule adds or removes one rule on one server's firewall and
// answers with what that server enforces afterwards.
func (s *Server) changeFirewallRule(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input struct {
		Operation string `json:"operation"`
		Action    string `json:"action"`
		Protocol  string `json:"protocol"`
		Port      uint16 `json:"port"`
		CIDR      string `json:"cidr"`
		// A deletion names the rule's place in the ruleset rather than its
		// contents: two rules can read identically, and the server has to
		// remove the one the operator is looking at.
		Family string `json:"family"`
		Table  string `json:"table"`
		Chain  string `json:"chain"`
		Handle string `json:"handle"`
		Raw    string `json:"raw"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Operation != "add" && input.Operation != "delete" {
		writeError(w, userErrorf("不支持的访问限制操作"))
		return
	}
	input.CIDR = strings.TrimSpace(input.CIDR)
	if input.Operation == "add" && input.CIDR != "" {
		if _, _, err := net.ParseCIDR(input.CIDR); err != nil {
			writeError(w, userErrorf("来源地址范围无效，请使用类似 192.168.1.0/24 的写法"))
			return
		}
	}
	if input.Operation == "delete" && input.Handle == "" && input.Raw == "" {
		writeError(w, userErrorf("这条规则缺少位置信息，无法删除"))
		return
	}
	payload, err := json.Marshal(map[string]any{
		"operation": input.Operation,
		"rule": map[string]any{
			"action": input.Action, "protocol": input.Protocol, "port": input.Port, "cidr": input.CIDR,
			"family": input.Family, "table": input.Table, "chain": input.Chain,
			"handle": input.Handle, "raw": input.Raw,
		},
	})
	if err != nil {
		writeError(w, err)
		return
	}
	nodeID := r.PathValue("id")
	data, err := s.AskNode(r.Context(), nodeID, "firewall.mutate", string(payload))
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "firewall.rule_"+input.Operation, "node", nodeID, "server firewall rule changed"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.decodeNodeFirewall(nodeID, liveAnswer{data: data}))
}

func (s *Server) listFail2Ban(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	nodeIDs, err := s.securityNodeIDs(r)
	if err != nil {
		writeError(w, err)
		return
	}
	answers := s.askNodes(r.Context(), nodeIDs, "fail2ban.query", "{}")
	nodes := make([]nodeFail2Ban, 0, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		node, _ := s.decodeNodeFail2Ban(nodeID, answers[nodeID])
		nodes = append(nodes, node)
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
}

// changeFail2BanJail saves or removes one automatic-banning rule on one server.
func (s *Server) changeFail2BanJail(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input struct {
		Operation       string `json:"operation"`
		Name            string `json:"name"`
		LogPath         string `json:"log_path"`
		FilterName      string `json:"filter_name"`
		FailRegex       string `json:"fail_regex"`
		MaxRetry        uint16 `json:"max_retry"`
		FindTimeSeconds uint32 `json:"find_time_seconds"`
		BanTimeSeconds  uint32 `json:"ban_time_seconds"`
		Ports           string `json:"ports"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Operation != "save" && input.Operation != "delete" {
		writeError(w, userErrorf("不支持的自动封禁操作"))
		return
	}
	payload, err := json.Marshal(map[string]any{
		"operation": input.Operation,
		"jail": map[string]any{
			"name": input.Name, "log_path": input.LogPath, "filter_name": input.FilterName,
			"fail_regex": input.FailRegex, "max_retry": input.MaxRetry,
			"find_time_seconds": input.FindTimeSeconds, "ban_time_seconds": input.BanTimeSeconds,
			"ports": input.Ports,
		},
	})
	if err != nil {
		writeError(w, err)
		return
	}
	nodeID := r.PathValue("id")
	data, err := s.AskNode(r.Context(), nodeID, "fail2ban.mutate", string(payload))
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "fail2ban.jail_"+input.Operation, "node", nodeID, "server fail2ban jail changed"); err != nil {
		writeError(w, err)
		return
	}
	node, _ := s.decodeNodeFail2Ban(nodeID, liveAnswer{data: data})
	writeJSON(w, http.StatusOK, node)
}

// BannedAddress is one address a server currently holds banned.
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

// listBannedAddresses reports every address the servers are holding banned
// right now, read from Fail2Ban itself rather than from any record here.
func (s *Server) listBannedAddresses(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	nodeIDs, err := s.securityNodeIDs(r)
	if err != nil {
		writeError(w, err)
		return
	}
	answers := s.askNodes(r.Context(), nodeIDs, "fail2ban.query", "{}")
	banned := []BannedAddress{}
	for _, nodeID := range nodeIDs {
		_, addresses := s.decodeNodeFail2Ban(nodeID, answers[nodeID])
		banned = append(banned, addresses...)
	}
	sort.Slice(banned, func(left, right int) bool {
		if banned[left].BannedAt != banned[right].BannedAt {
			return banned[left].BannedAt > banned[right].BannedAt
		}
		return banned[left].IP < banned[right].IP
	})
	writeJSON(w, http.StatusOK, map[string]any{"banned": banned})
}

// unbanAddress asks one node to release one address. It applies to the
// operator's own jails too, not only the ones this platform wrote.
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
	payload, err := json.Marshal(map[string]string{"jail": input.Jail, "ip": input.IP})
	if err != nil {
		writeError(w, err)
		return
	}
	nodeID := r.PathValue("id")
	if _, err := s.AskNode(r.Context(), nodeID, "fail2ban.unban", string(payload)); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "fail2ban.unbanned", "node", nodeID, "banned address released"); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// liveAnswer is one node's reply, or the reason it could not give one.
type liveAnswer struct {
	data string
	err  error
}
// securityNodeIDs is the node the path names, or every node that has not been
// revoked.
func (s *Server) securityNodeIDs(r *http.Request) ([]string, error) {
	if nodeID := r.PathValue("id"); nodeID != "" {
		return []string{nodeID}, nil
	}
	nodes, err := s.store.ListNodes(r.Context())
	if err != nil {
		return nil, err
	}
	nodeIDs := make([]string, 0, len(nodes))
	for _, node := range nodes {
		nodeIDs = append(nodeIDs, node.ID)
	}
	return nodeIDs, nil
}

func (s *Server) decodeNodeFirewall(nodeID string, answer liveAnswer) nodeFirewall {
	node := nodeFirewall{NodeID: nodeID, Rules: []LiveFirewallRule{}}
	if answer.err != nil {
		node.Error = answer.err.Error()
		return node
	}
	var reported agentFirewall
	if err := json.Unmarshal([]byte(answer.data), &reported); err != nil {
		node.Error = "服务器返回的防火墙规则无法解析"
		return node
	}
	node.Available, node.Tool, node.Truncated, node.Error = reported.Available, reported.Tool, reported.Truncated, reported.Error
	for _, rule := range reported.Rules {
		node.Rules = append(node.Rules, LiveFirewallRule{
			NodeID: nodeID, Family: rule.Family, Table: rule.Table, Chain: rule.Chain, Handle: rule.Handle,
			Action: rule.Action, Protocol: rule.Protocol, Port: rule.Port, CIDR: rule.CIDR,
			Location: s.ipLocator.LocateCIDR(rule.CIDR), Raw: rule.Raw,
		})
	}
	return node
}

func (s *Server) decodeNodeFail2Ban(nodeID string, answer liveAnswer) (nodeFail2Ban, []BannedAddress) {
	node := nodeFail2Ban{NodeID: nodeID, Jails: []LiveFail2BanJail{}}
	if answer.err != nil {
		node.Error = answer.err.Error()
		return node, nil
	}
	var reported agentFail2Ban
	if err := json.Unmarshal([]byte(answer.data), &reported); err != nil {
		node.Error = "服务器返回的自动封禁规则无法解析"
		return node, nil
	}
	node.Available, node.Error = reported.Available, reported.Error
	var banned []BannedAddress
	for _, jail := range reported.Jails {
		node.Jails = append(node.Jails, LiveFail2BanJail{
			NodeID: nodeID, Name: jail.Name, Managed: jail.Managed, LogPath: jail.LogPath,
			FilterName: jail.FilterName, FailRegex: jail.FailRegex, MaxRetry: jail.MaxRetry,
			FindTimeSeconds: jail.FindTimeSeconds, BanTimeSeconds: jail.BanTimeSeconds, Ports: jail.Ports,
			Running: jail.Running, CurrentlyBanned: jail.CurrentlyBanned, TotalBanned: jail.TotalBanned,
			Error: jail.Error,
		})
		// The jail an address has to be released from is the name Fail2Ban
		// knows, which for a managed rule carries the platform's prefix.
		fail2banName := jail.Name
		if jail.Managed {
			fail2banName = managedJailPrefix + jail.Name
		}
		for _, ban := range jail.Banned {
			banned = append(banned, BannedAddress{
				NodeID: nodeID, Jail: fail2banName, RuleName: jail.Name, Managed: jail.Managed,
				IP: ban.IP, Location: s.ipLocator.Locate(ban.IP), BannedAt: ban.BannedAt, UnbanAt: ban.UnbanAt,
			})
		}
	}
	return node, banned
}

// managedJailPrefix is the namespace the agent writes its jails into; the
// console shows names without it.
const managedJailPrefix = "polaris-"
