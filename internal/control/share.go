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
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "分享链接不存在或已过期"})
		return
	}
	tokenHash := sha256.Sum256([]byte(token))
	link, err := s.store.FindShareLink(r.Context(), tokenHash[:])
	if errors.Is(err, ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "分享链接不存在或已过期"})
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
			if len(report) > 0 && json.Unmarshal(report, &stored) == nil && stored.Proxy != nil {
				summary.ReceivedBytes += stored.Proxy["received_bytes"]
				summary.SentBytes += stored.Proxy["sent_bytes"]
				summary.MonthlyReceivedBytes += stored.Proxy["monthly_received_bytes"]
				summary.MonthlySentBytes += stored.Proxy["monthly_sent_bytes"]
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

func shareURL(r *http.Request, token string) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/share/" + token
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
