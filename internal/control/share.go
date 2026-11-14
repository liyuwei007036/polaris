package control

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	shareMinimumTTL = 5 * time.Minute
	shareMaximumTTL = 90 * 24 * time.Hour
)

type shareSummary struct {
	NodesTotal           int     `json:"nodes_total"`
	Online               int     `json:"online"`
	Offline              int     `json:"offline"`
	ActiveConnections    int     `json:"active_connections"`
	DownloadRate         float64 `json:"download_rate"`
	UploadRate           float64 `json:"upload_rate"`
	HasRates             bool    `json:"has_rates"`
	ReceivedBytes        uint64  `json:"received_bytes"`
	SentBytes            uint64  `json:"sent_bytes"`
	MonthlyReceivedBytes uint64  `json:"monthly_received_bytes"`
	MonthlySentBytes     uint64  `json:"monthly_sent_bytes"`
}

// shareNode is the masked, read-only projection of a node. Anything that
// would let a viewer reach the machine or infer commercial terms (addresses,
// domains, provider, price, expiry, traffic plan) is either masked or dropped.
type shareNode struct {
	Name            string            `json:"name"`
	Online          bool              `json:"online"`
	OS              string            `json:"os,omitempty"`
	OSName          string            `json:"os_name,omitempty"`
	Architecture    string            `json:"architecture,omitempty"`
	AgentVersion    string            `json:"agent_version,omitempty"`
	SingBoxVersion  string            `json:"sing_box_version,omitempty"`
	LastSeenAt      string            `json:"last_seen_at,omitempty"`
	Location        string            `json:"location,omitempty"`
	City            string            `json:"city,omitempty"`
	CountryCode     string            `json:"country_code,omitempty"`
	Latitude        float64           `json:"latitude,omitempty"`
	Longitude       float64           `json:"longitude,omitempty"`
	HasCoords       bool              `json:"has_coords,omitempty"`
	ClientAddress   string            `json:"client_address,omitempty"`
	LastObservedIP  string            `json:"last_observed_ip,omitempty"`
	IPAddresses     []string          `json:"ip_addresses,omitempty"`
	DDNSDomains     []string          `json:"ddns_domains,omitempty"`
	Host            *HostResources    `json:"host,omitempty"`
	Node            map[string]uint64 `json:"node,omitempty"`
	Proxy           map[string]uint64 `json:"proxy,omitempty"`
	ConnectionCount int               `json:"connection_count"`
	ReceivedRate    float64           `json:"received_rate"`
	SentRate        float64           `json:"sent_rate"`
	HasRates        bool              `json:"has_rates"`
}

type sharePayload struct {
	Page                 string         `json:"page"`
	ExpiresAt            string         `json:"expires_at"`
	GeneratedAt          string         `json:"generated_at"`
	Masked               bool           `json:"masked"`
	Summary              shareSummary   `json:"summary"`
	Nodes                []shareNode    `json:"nodes,omitempty"`
	Protocols            map[string]int `json:"protocols,omitempty"`
	PopularNodes         []popularNode  `json:"popular_nodes,omitempty"`
	PopularWindowMinutes int            `json:"popular_window_minutes,omitempty"`
}

func (s *Server) createShareLink(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input struct {
		Page      string `json:"page"`
		ExpiresIn int64  `json:"expires_in"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Page != sharePageNodes && input.Page != sharePageDashboard {
		writeError(w, errors.New("share page must be nodes or dashboard"))
		return
	}
	minimumSeconds := int64(shareMinimumTTL / time.Second)
	maximumSeconds := int64(shareMaximumTTL / time.Second)
	if input.ExpiresIn < minimumSeconds || input.ExpiresIn > maximumSeconds {
		writeError(w, fmt.Errorf("share expiry must be between %d and %d seconds", minimumSeconds, maximumSeconds))
		return
	}
	ttl := time.Duration(input.ExpiresIn) * time.Second

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		writeError(w, err)
		return
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	tokenHash := sha256.Sum256([]byte(token))
	expiresAt := time.Now().UTC().Add(ttl)
	link, err := s.store.CreateShareLink(r.Context(), operator.ID, input.Page, tokenHash[:], expiresAt)
	if err != nil {
		writeError(w, err)
		return
	}
	_ = s.store.AppendAudit(r.Context(), operator.ID, "share.created", "share", link.ID, "read-only share link created for "+input.Page)
	writeJSON(w, http.StatusCreated, map[string]any{
		"share": link,
		"token": token,
		"url":   shareURL(r, token),
	})
}

func (s *Server) listShareLinks(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	links, err := s.store.ListShareLinks(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": links})
}

func (s *Server) revokeShareLink(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	id := r.PathValue("id")
	if err := s.store.RevokeShareLink(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	_ = s.store.AppendAudit(r.Context(), operator.ID, "share.revoked", "share", id, "read-only share link revoked")
	w.WriteHeader(http.StatusNoContent)
}

// publicShareLink is intentionally unauthenticated: possession of the token is
// the credential. It only ever returns the masked projection, never a node or
// listener record directly.
func (s *Server) publicShareLink(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		writeShareNotFound(w)
		return
	}
	tokenHash := sha256.Sum256([]byte(token))
	link, err := s.store.FindShareLink(r.Context(), tokenHash[:])
	if errors.Is(err, ErrNotFound) {
		writeShareNotFound(w)
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	payload, err := s.buildSharePayload(r.Context(), link.Page)
	if err != nil {
		writeError(w, err)
		return
	}
	payload.ExpiresAt = link.ExpiresAt
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) buildSharePayload(ctx context.Context, page string) (sharePayload, error) {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return sharePayload{}, err
	}
	reports, _, err := s.store.AllNodeMetrics(ctx)
	if err != nil {
		return sharePayload{}, err
	}
	now := time.Now()
	totals := s.connHub.totals(now, s.connActivity.popular(now, popularNodeLimit))
	live := map[string]nodeConnectionsSnapshot{}
	for _, snapshot := range s.connHub.snapshot() {
		live[snapshot.NodeID] = snapshot
	}

	summary := shareSummary{
		NodesTotal:        len(nodes),
		ActiveConnections: totals.ConnectionCount,
		DownloadRate:      totals.DownloadRate,
		UploadRate:        totals.UploadRate,
		HasRates:          totals.HasRates,
	}
	payload := sharePayload{
		Page:        page,
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Masked:      true,
		Protocols:   map[string]int{},
	}
	for _, node := range nodes {
		if node.Online {
			summary.Online++
		}
		if report, ok := reports[node.ID]; ok {
			var stored storedMetrics
			if len(report) > 0 && json.Unmarshal(report, &stored) == nil {
				trafficSource := stored.Node
				if trafficSource == nil {
					trafficSource = stored.Proxy
				}
				if trafficSource != nil {
					summary.ReceivedBytes += trafficSource["received_bytes"]
					summary.SentBytes += trafficSource["sent_bytes"]
					summary.MonthlyReceivedBytes += trafficSource["monthly_received_bytes"]
					summary.MonthlySentBytes += trafficSource["monthly_sent_bytes"]
				}
			}
		}
	}
	summary.Offline = summary.NodesTotal - summary.Online
	payload.Summary = summary

	if page == sharePageDashboard {
		payload.Protocols = totals.Protocols
		payload.PopularNodes = totals.PopularNodes
		payload.PopularWindowMinutes = totals.PopularWindowMinutes
		return payload, nil
	}

	payload.Nodes = make([]shareNode, 0, len(nodes))
	for i := range nodes {
		node := &nodes[i]
		s.enrichNodeLocation(node)
		item := shareNode{
			Name:           node.Name,
			Online:         node.Online,
			OS:             node.OS,
			OSName:         node.OSName,
			Architecture:   node.Architecture,
			AgentVersion:   node.AgentVersion,
			SingBoxVersion: node.SingBox,
			LastSeenAt:     node.LastSeenAt,
			Location:       node.Location,
			City:           node.City,
			CountryCode:    node.CountryCode,
			Latitude:       node.Latitude,
			Longitude:      node.Longitude,
			HasCoords:      node.HasCoords,
			ClientAddress:  maskShareAddress(node.ClientAddress),
			LastObservedIP: maskShareAddress(node.LastObservedIP),
		}
		for _, ip := range node.IPAddresses {
			item.IPAddresses = append(item.IPAddresses, maskShareAddress(ip))
		}
		for _, domain := range node.DDNSDomains {
			item.DDNSDomains = append(item.DDNSDomains, maskShareAddress(domain))
		}
		if report, ok := reports[node.ID]; ok {
			var stored storedMetrics
			if len(report) > 0 && json.Unmarshal(report, &stored) == nil {
				if node.Online {
					item.Host = stored.Host
				}
				item.Node = stored.Node
				item.Proxy = stored.Proxy
			}
		}
		if snapshot, ok := live[node.ID]; ok {
			item.ConnectionCount = snapshot.ConnectionCount
			item.ReceivedRate = snapshot.ReceivedRate
			item.SentRate = snapshot.SentRate
			item.HasRates = snapshot.HasRates
			if node.Online && item.Host == nil {
				item.Host = snapshot.Host
			}
		}
		payload.Nodes = append(payload.Nodes, item)
	}
	return payload, nil
}

// requireShare resolves the token in the URL and, when pages are supplied,
// checks that the link was created for one of them. A link for the dashboard
// must not be usable to enumerate the server list, and vice versa.
func (s *Server) requireShare(w http.ResponseWriter, r *http.Request, pages ...string) (ShareLink, bool) {
	token := r.PathValue("token")
	if token == "" {
		writeShareNotFound(w)
		return ShareLink{}, false
	}
	tokenHash := sha256.Sum256([]byte(token))
	link, err := s.store.FindShareLink(r.Context(), tokenHash[:])
	if errors.Is(err, ErrNotFound) {
		writeShareNotFound(w)
		return ShareLink{}, false
	}
	if err != nil {
		writeError(w, err)
		return ShareLink{}, false
	}
	if len(pages) > 0 {
		allowed := false
		for _, page := range pages {
			if link.Page == page {
				allowed = true
				break
			}
		}
		if !allowed {
			writeShareNotFound(w)
			return ShareLink{}, false
		}
	}
	return link, true
}

func writeShareNotFound(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "分享链接不存在或已过期"})
}

func writeShareJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	writeJSON(w, status, value)
}

// shareNodes serves the same /nodes shape the real server page consumes, but
// every stored record passes through maskShareNode first.
func (s *Server) shareNodes(w http.ResponseWriter, r *http.Request) {
	// The dashboard counts nodes and online state, so it needs the same masked
	// list; the server page is the one that renders it in full.
	if _, ok := s.requireShare(w, r, sharePageNodes, sharePageDashboard); !ok {
		return
	}
	nodes, err := s.store.ListNodes(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	s.controlMu.Lock()
	for i := range nodes {
		if session := s.controls[nodes[i].ID]; session != nil && session.remoteAddr != "" {
			if nodes[i].LastObservedIP == "" {
				nodes[i].LastObservedIP = session.remoteAddr
			}
		}
	}
	s.controlMu.Unlock()
	out := make([]Node, 0, len(nodes))
	for i := range nodes {
		s.enrichNodeLocation(&nodes[i])
		out = append(out, maskShareNode(nodes[i]))
	}
	writeShareJSON(w, http.StatusOK, map[string]any{"nodes": out})
}

func maskShareNode(node Node) Node {
	node.ClientAddress = maskShareAddress(node.ClientAddress)
	node.LastObservedIP = maskShareAddress(node.LastObservedIP)
	if len(node.IPAddresses) > 0 {
		masked := make([]string, 0, len(node.IPAddresses))
		for _, address := range node.IPAddresses {
			masked = append(masked, maskShareAddress(address))
		}
		node.IPAddresses = masked
	}
	if len(node.DDNSDomains) > 0 {
		masked := make([]string, 0, len(node.DDNSDomains))
		for _, domain := range node.DDNSDomains {
			masked = append(masked, maskShareAddress(domain))
		}
		node.DDNSDomains = masked
	}
	node.DDNSDomain = maskShareAddress(node.DDNSDomain)
	node.DDNSRecordID = ""
	node.DDNSRecordIDs = nil
	node.IPAliases = nil
	node.IPMappings = nil
	// Commercial terms are not part of a read-only operations share.
	node.ExpiresOn = ""
	node.Price = ""
	node.PricePeriod = ""
	node.Currency = ""
	node.Provider = ""
	node.ProviderURL = ""
	node.TrafficPlan = ""
	node.Bandwidth = ""
	return node
}

func (s *Server) shareNodeMetrics(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireShare(w, r, sharePageNodes, sharePageDashboard); !ok {
		return
	}
	reports, updated, err := s.store.AllNodeMetrics(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	live := map[string]nodeConnectionsSnapshot{}
	for _, snapshot := range s.connHub.snapshot() {
		live[snapshot.NodeID] = maskShareSnapshot(snapshot)
	}
	nodes := make([]map[string]any, 0, len(reports))
	for nodeID, report := range reports {
		entry := map[string]any{
			"node_id":    nodeID,
			"updated_at": updated[nodeID],
			"report":     maskShareReport(report),
		}
		if snapshot, ok := live[nodeID]; ok {
			entry["live"] = snapshot
		}
		nodes = append(nodes, entry)
	}
	writeShareJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
}

// maskShareReport drops the per-connection list (client addresses, hosts and
// rules) and masks probe labels that are really addresses. The counters and
// host resource figures the real page draws are kept.
func maskShareReport(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var stored storedMetrics
	if err := json.Unmarshal(raw, &stored); err != nil {
		return raw
	}
	stored.Connections = nil
	if stored.Latency != nil {
		stored.Latency.Probe = maskShareLabel(stored.Latency.Probe)
		for i := range stored.Latency.Probes {
			stored.Latency.Probes[i].Name = maskShareLabel(stored.Latency.Probes[i].Name)
		}
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		return raw
	}
	return encoded
}

type shareMapConnection struct {
	ID              string  `json:"id,omitempty"`
	SourceLat       float64 `json:"source_lat,omitempty"`
	SourceLon       float64 `json:"source_lon,omitempty"`
	HasSourceCoords bool    `json:"has_source_coords,omitempty"`
	IsChainRelay    bool    `json:"is_chain_relay,omitempty"`
}

func maskShareSnapshot(snapshot nodeConnectionsSnapshot) nodeConnectionsSnapshot {
	var rawConns []storedConnection
	if len(snapshot.RawConns) > 0 {
		rawConns = snapshot.RawConns
	} else if len(snapshot.Connections) > 0 {
		_ = json.Unmarshal(snapshot.Connections, &rawConns)
	}

	if len(rawConns) > 0 {
		mapConns := make([]shareMapConnection, 0, len(rawConns))
		for _, c := range rawConns {
			mapConns = append(mapConns, shareMapConnection{
				ID:              c.ID,
				SourceLat:       c.SourceLat,
				SourceLon:       c.SourceLon,
				HasSourceCoords: c.HasSourceCoords,
				IsChainRelay:    c.IsChainRelay,
			})
		}
		if encoded, err := json.Marshal(mapConns); err == nil {
			snapshot.Connections = encoded
		} else {
			snapshot.Connections = nil
		}
	} else {
		snapshot.Connections = nil
	}
	snapshot.RawConns = nil
	if snapshot.Latency != nil {
		latency := *snapshot.Latency
		latency.Probe = maskShareLabel(latency.Probe)
		latency.Probes = append([]storedProbeStat(nil), snapshot.Latency.Probes...)
		for i := range latency.Probes {
			latency.Probes[i].Name = maskShareLabel(latency.Probes[i].Name)
		}
		snapshot.Latency = &latency
	}
	return snapshot
}

func (s *Server) shareListeners(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireShare(w, r, sharePageNodes, sharePageDashboard); !ok {
		return
	}
	listeners, err := s.store.ListListeners(r.Context(), "")
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(listeners))
	for _, listener := range listeners {
		// The dashboard only needs the per-service endpoint count. Protocol
		// specs, credentials and listen addresses stay private.
		out = append(out, map[string]any{
			"id":             listener.ID,
			"node_id":        listener.NodeID,
			"enabled":        listener.Enabled,
			"endpoint_count": listener.EndpointCount,
		})
	}
	writeShareJSON(w, http.StatusOK, map[string]any{"listeners": out})
}

func (s *Server) shareFleet(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireShare(w, r, sharePageNodes, sharePageDashboard); !ok {
		return
	}
	now := time.Now()
	writeShareJSON(w, http.StatusOK, s.connHub.totals(now, s.connActivity.popular(now, popularNodeLimit)))
}

func (s *Server) shareNodeHistory(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireShare(w, r, sharePageNodes); !ok {
		return
	}
	s.serveNodeHistory(w, r, r.PathValue("id"), true)
}

func (s *Server) shareConnectionsStream(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireShare(w, r, sharePageNodes, sharePageDashboard); !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, errors.New("streaming is unavailable"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
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
	snapshots := make([]nodeConnectionsSnapshot, 0)
	for _, snapshot := range s.connHub.snapshot() {
		snapshots = append(snapshots, maskShareSnapshot(snapshot))
	}
	if !writeEvent("snapshot", map[string]any{"nodes": snapshots}) {
		return
	}
	ch := s.connHub.subscribe()
	defer s.connHub.unsubscribe(ch)
	totalsCh := s.connHub.subscribeTotals()
	defer s.connHub.unsubscribeTotals(totalsCh)
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
		case snapshot := <-ch:
			if !writeEvent("node", maskShareSnapshot(snapshot)) {
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

func (s *Server) shareLiveStream(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireShare(w, r, sharePageNodes, sharePageDashboard); !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, errors.New("streaming is unavailable"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
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
			if event.Kind == "node" {
				event.ClientAddress = maskShareAddress(event.ClientAddress)
				event.LastObservedIP = maskShareAddress(event.LastObservedIP)
			}
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

// shareFallback answers the optional console calls that are not meaningful to
// a viewer (pending registrations, update metadata, client probing) with empty
// data instead of leaking the authenticated endpoint or breaking the page.
func (s *Server) shareFallback(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireShare(w, r); !ok {
		return
	}
	switch r.PathValue("path") {
	case "registrations":
		writeShareJSON(w, http.StatusOK, map[string]any{"registrations": []any{}})
	case "cloudflare/records":
		writeShareJSON(w, http.StatusOK, map[string]any{"records": []any{}})
	default:
		writeShareJSON(w, http.StatusOK, map[string]any{})
	}
}

func shareURL(r *http.Request, token string) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/share/" + token
}

var shareIPv4Pattern = regexp.MustCompile(`(?:\d{1,3}\.){3}\d{1,3}`)

// maskShareLabel masks addresses embedded in a label while leaving operator
// naming readable: "香港 01" stays, "Cloudflare (1.1.1.1)" becomes
// "Cloudflare (1.1.*.*)", and a bare hostname loses its name but keeps its TLD.
func maskShareLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	trimmed := strings.Trim(value, "[]")
	if ip := net.ParseIP(trimmed); ip != nil {
		return maskShareHost(trimmed)
	}
	if !strings.ContainsAny(value, " \t") && strings.Contains(value, ".") {
		return maskShareHost(value)
	}
	return shareIPv4Pattern.ReplaceAllStringFunc(value, func(match string) string {
		if ip := net.ParseIP(match); ip != nil {
			return maskShareHost(match)
		}
		return match
	})
}

// maskShareAddress removes the part of an address that identifies the actual
// machine while keeping enough shape for the shared page to stay readable.
func maskShareAddress(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if host, port, err := net.SplitHostPort(value); err == nil {
		return net.JoinHostPort(maskShareHost(host), port)
	}
	return maskShareHost(value)
}

func maskShareHost(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if ip := net.ParseIP(value); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return fmt.Sprintf("%d.%d.*.*", v4[0], v4[1])
		}
		return fmt.Sprintf("%x:%x:****", []byte(ip[0:2]), []byte(ip[2:4]))
	}
	host := strings.TrimSuffix(value, ".")
	parts := strings.Split(host, ".")
	if len(parts) >= 2 {
		return "***." + parts[len(parts)-1]
	}
	return "***"
}
