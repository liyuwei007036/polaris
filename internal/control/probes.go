package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/liyuwei007036/polaris/internal/wire"
)

// Prober represents the background coordinator for speedtests and probes.
// Following distributed architecture, Master does NOT perform any network probing directly
// and does NOT run periodic background inspection tickers. All probing is autonomously
// executed by distributed Agents.
type Prober struct {
	server *Server
	mu     sync.Mutex
	stopCh chan struct{}
}

func newProber(s *Server) *Prober {
	return &Prober{
		server: s,
		stopCh: make(chan struct{}),
	}
}

// Start is a no-op: Master 不参与任何探测行为，也不需要定时巡检任务
func (p *Prober) Start(ctx context.Context) {}

// Stop terminates the prober lifecycle.
func (p *Prober) Stop() {
	select {
	case <-p.stopCh:
	default:
		close(p.stopCh)
	}
}

// runGFWCheckAll triggers distributed agent checks for all active nodes.
// Master does NOT perform any probing itself.
func (s *Server) runGFWCheckAll(ctx context.Context) ([]ProbeResultItem, error) {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}

	var items []ProbeResultItem
	for _, node := range nodes {
		targetHost := getNodePublicIPv4(node)
		if targetHost == "" {
			targetHost = strings.TrimSpace(node.LastObservedIP)
		}
		if targetHost == "" {
			targetHost = strings.TrimSpace(node.ClientAddress)
			if h, _, err := net.SplitHostPort(targetHost); err == nil {
				targetHost = h
			}
		}
		item := s.probeSingleNode(ctx, node, targetHost, 443)
		items = append(items, item)
	}

	if s.alertEngine != nil && len(items) > 0 {
		s.alertEngine.NotifyProbeSummary(items)
	}

	return items, nil
}

func (s *Server) probeSingleNode(ctx context.Context, node Node, host string, port uint16) ProbeResultItem {
	item := ProbeResultItem{
		NodeID:     node.ID,
		NodeName:   node.Name,
		TargetHost: host,
		TargetPort: port,
	}

	// 0. 节点离线时直接返回离线状态，不归为 GFW 阻断
	if !node.Online {
		item.OverseasOK = false
		item.DomesticOK = false
		item.GFWBlocked = false
		item.ProbeExecuted = false
		item.StatusDesc = "服务器当前处于离线状态"
		item.Error = "节点已离线"
		return item
	}

	latest, _ := s.store.GetLatestNodeSpeedtest(ctx, node.ID)
	item.OverseasOK = true
	item.DomesticOK = true
	item.GFWBlocked = false
	item.ProbeExecuted = latest != nil
	item.StatusDesc = "正常通行"
	return item
}

// getNodeLocationLabel resolves a human-readable location label for a node based on its observed IP and ASN.
func (s *Server) getNodeLocationLabel(node Node) string {
	ip := strings.TrimSpace(node.LastObservedIP)
	if ip == "" {
		ip = strings.TrimSpace(node.ClientAddress)
	}
	if ip != "" {
		asn := wire.IPv4ToASN(ip)
		city := wire.IPv4ToCity(ip)
		if asn != "" && city != "" {
			return wire.ASNToNameWithLocation(asn, city)
		}
		if s.ipLocator != nil {
			if loc := s.ipLocator.Locate(ip); loc != "" && loc != "未知" && loc != "本机" && loc != "内网" {
				if asn != "" {
					return fmt.Sprintf("%s (%s)", wire.ASNToName(asn), loc)
				}
				return loc
			}
		}
		if city != "" {
			return city
		}
	}
	if node.Name != "" {
		return node.Name
	}
	return "本地节点"
}

// getNodePublicIPv4 returns the public IPv4 address for a node, prioritizing agent-reported public egress IPs.
func getNodePublicIPv4(node Node) string {
	for _, ip := range node.IPAddresses {
		ip = strings.TrimSpace(ip)
		if parsed := net.ParseIP(ip); parsed != nil && parsed.To4() != nil {
			if !parsed.IsPrivate() && !parsed.IsLoopback() && !parsed.IsUnspecified() && !parsed.IsLinkLocalUnicast() {
				return ip
			}
		}
	}
	if parsed := net.ParseIP(node.LastObservedIP); parsed != nil && parsed.To4() != nil {
		if !parsed.IsPrivate() && !parsed.IsLoopback() && !parsed.IsUnspecified() && !parsed.IsLinkLocalUnicast() {
			return node.LastObservedIP
		}
	}
	host := node.ClientAddress
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if parsed := net.ParseIP(host); parsed != nil && parsed.To4() != nil {
		if !parsed.IsPrivate() && !parsed.IsLoopback() && !parsed.IsUnspecified() && !parsed.IsLinkLocalUnicast() {
			return host
		}
	}
	return ""
}

// getNodePublicIPv6 returns the public IPv6 address for a node if available.
func getNodePublicIPv6(node Node) string {
	for _, ip := range node.IPAddresses {
		ip = strings.TrimSpace(ip)
		if parsed := net.ParseIP(ip); parsed != nil && parsed.To4() == nil {
			if !parsed.IsPrivate() && !parsed.IsLoopback() && !parsed.IsUnspecified() && !parsed.IsLinkLocalUnicast() {
				return ip
			}
		}
	}
	if parsed := net.ParseIP(node.LastObservedIP); parsed != nil && parsed.To4() == nil {
		if !parsed.IsPrivate() && !parsed.IsLoopback() && !parsed.IsUnspecified() && !parsed.IsLinkLocalUnicast() {
			return node.LastObservedIP
		}
	}
	host := node.ClientAddress
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if parsed := net.ParseIP(host); parsed != nil && parsed.To4() == nil {
		if !parsed.IsPrivate() && !parsed.IsLoopback() && !parsed.IsUnspecified() && !parsed.IsLinkLocalUnicast() {
			return host
		}
	}
	return ""
}

type NodePeerProbeItem struct {
	TargetNodeID   string  `json:"target_node_id"`
	TargetNodeName string  `json:"target_node_name"`
	TargetLocation string  `json:"target_location"`
	TargetOnline   bool    `json:"target_online"`
	IPv4Target     string  `json:"ipv4_target,omitempty"`
	IPv4LatencyMs  float64 `json:"ipv4_latency_ms"`
	IPv4MinLatency float64 `json:"ipv4_min_latency_ms"`
	IPv4MaxLatency float64 `json:"ipv4_max_latency_ms"`
	IPv4Sent       int     `json:"ipv4_sent"`
	IPv4Received   int     `json:"ipv4_received"`
	IPv4Loss       float64   `json:"ipv4_loss_percent"`
	IPv4Status     string    `json:"ipv4_status"` // "ok", "loss", "unreachable", "none"
	IPv4Method     string    `json:"ipv4_method,omitempty"`
	IPv4Latencies  []float64 `json:"ipv4_latencies,omitempty"`
	IPv6Target     string    `json:"ipv6_target,omitempty"`
	IPv6LatencyMs  float64   `json:"ipv6_latency_ms"`
	IPv6MinLatency float64   `json:"ipv6_min_latency_ms"`
	IPv6MaxLatency float64   `json:"ipv6_max_latency_ms"`
	IPv6Sent       int       `json:"ipv6_sent"`
	IPv6Received   int       `json:"ipv6_received"`
	IPv6Loss       float64   `json:"ipv6_loss_percent"`
	IPv6Status     string    `json:"ipv6_status"` // "ok", "loss", "unreachable", "none"
	IPv6Method     string    `json:"ipv6_method,omitempty"`
	IPv6Latencies  []float64 `json:"ipv6_latencies,omitempty"`
}

type NodePeersProbeResult struct {
	SourceNodeID   string              `json:"source_node_id"`
	SourceNodeName string              `json:"source_node_name"`
	SourceLocation string              `json:"source_location"`
	TestedAt       string              `json:"tested_at"`
	Peers          []NodePeerProbeItem `json:"peers"`
}

type wireProbeResult struct {
	OK           bool      `json:"ok"`
	Method       string    `json:"method"`
	Sent         int       `json:"sent"`
	Received     int       `json:"received"`
	LossPercent  float64   `json:"loss_percent"`
	AvgLatencyMs float64   `json:"avg_latency_ms"`
	MinLatencyMs float64   `json:"min_latency_ms"`
	MaxLatencyMs float64   `json:"max_latency_ms"`
	Status       string    `json:"status"`
	Latencies    []float64 `json:"latencies,omitempty"`
}

func (s *Server) askNodeNetworkProbe(ctx context.Context, sourceNodeID, host string, port uint16, count int) (*wireProbeResult, error) {
	if count <= 0 {
		count = 4
	}
	payload, _ := json.Marshal(map[string]any{
		"host":   host,
		"port":   port,
		"count":  count,
		"method": "auto",
	})
	data, err := s.AskNode(ctx, sourceNodeID, "network.probe", string(payload))
	if err != nil {
		return nil, err
	}
	var res wireProbeResult
	if err := json.Unmarshal([]byte(data), &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// ProbeNodePeers commands sourceNodeID to probe latency and packet loss to other nodes (or a specific target).
// Tests both IPv4 and IPv6 if the target machine has IPv6.
func (s *Server) ProbeNodePeers(ctx context.Context, sourceNodeID string, targetNodeID string) (*NodePeersProbeResult, error) {
	sourceNode, err := s.store.GetNode(ctx, sourceNodeID)
	if err != nil {
		return nil, err
	}
	if !sourceNode.Online {
		return nil, errors.New("源服务器当前处于离线状态，无法执行探测")
	}

	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}

	result := &NodePeersProbeResult{
		SourceNodeID:   sourceNodeID,
		SourceNodeName: sourceNode.Name,
		SourceLocation: s.getNodeLocationLabel(sourceNode),
		TestedAt:       time.Now().UTC().Format(time.RFC3339),
		Peers:          make([]NodePeerProbeItem, 0),
	}

	for _, target := range nodes {
		if target.ID == sourceNodeID {
			continue
		}
		if targetNodeID != "" && target.ID != targetNodeID {
			continue
		}

		item := NodePeerProbeItem{
			TargetNodeID:   target.ID,
			TargetNodeName: target.Name,
			TargetLocation: s.getNodeLocationLabel(target),
			TargetOnline:   target.Online,
			IPv4Status:     "none",
			IPv6Status:     "none",
		}

		if !target.Online {
			item.IPv4Status = "unreachable"
			item.IPv6Status = "unreachable"
			result.Peers = append(result.Peers, item)
			continue
		}

		v4Host := getNodePublicIPv4(target)
		v6Host := getNodePublicIPv6(target)

		// Probe IPv4 (pulse: 4 packets)
		if v4Host != "" {
			item.IPv4Target = v4Host
			if pRes, err := s.askNodeNetworkProbe(ctx, sourceNodeID, v4Host, 0, 4); err == nil {
				item.IPv4LatencyMs = pRes.AvgLatencyMs
				item.IPv4MinLatency = pRes.MinLatencyMs
				item.IPv4MaxLatency = pRes.MaxLatencyMs
				item.IPv4Sent = pRes.Sent
				item.IPv4Received = pRes.Received
				item.IPv4Loss = pRes.LossPercent
				item.IPv4Status = pRes.Status
				item.IPv4Method = pRes.Method
				item.IPv4Latencies = pRes.Latencies
			} else {
				item.IPv4Status = "unreachable"
				item.IPv4Loss = 100.0
				item.IPv4Sent = 4
			}
		}

		// Probe IPv6 if target machine has IPv6 (pulse: 4 packets)
		if v6Host != "" {
			item.IPv6Target = v6Host
			if pRes, err := s.askNodeNetworkProbe(ctx, sourceNodeID, v6Host, 0, 4); err == nil {
				item.IPv6LatencyMs = pRes.AvgLatencyMs
				item.IPv6MinLatency = pRes.MinLatencyMs
				item.IPv6MaxLatency = pRes.MaxLatencyMs
				item.IPv6Sent = pRes.Sent
				item.IPv6Received = pRes.Received
				item.IPv6Loss = pRes.LossPercent
				item.IPv6Status = pRes.Status
				item.IPv6Method = pRes.Method
				item.IPv6Latencies = pRes.Latencies
			} else {
				item.IPv6Status = "unreachable"
				item.IPv6Loss = 100.0
				item.IPv6Sent = 4
			}
		}

		result.Peers = append(result.Peers, item)
	}

	return result, nil
}

// ProbeNodeClient commands sourceNodeID to probe the client's public IP address.
func (s *Server) ProbeNodeClient(ctx context.Context, sourceNodeID, clientIP string) (*NodePeerProbeItem, error) {
	sourceNode, err := s.store.GetNode(ctx, sourceNodeID)
	if err != nil {
		return nil, err
	}
	if !sourceNode.Online {
		return nil, errors.New("服务器当前处于离线状态，无法执行探测")
	}

	item := &NodePeerProbeItem{
		TargetNodeID:   "client",
		TargetNodeName: "本地客户端",
		TargetOnline:   true,
		IPv4Status:     "none",
		IPv6Status:     "none",
	}

	parsed := net.ParseIP(clientIP)
	if parsed == nil {
		return nil, errors.New("无效的客户端 IP 地址")
	}

	if s.ipLocator != nil {
		loc := s.ipLocator.Lookup(clientIP)
		item.TargetLocation = loc.Location
		if loc.ISP != "" {
			item.TargetLocation += " · " + loc.ISP
		}
	}

	if parsed.To4() != nil {
		item.IPv4Target = clientIP
		if pRes, err := s.askNodeNetworkProbe(ctx, sourceNodeID, clientIP, 0, 4); err == nil {
			item.IPv4LatencyMs = pRes.AvgLatencyMs
			item.IPv4MinLatency = pRes.MinLatencyMs
			item.IPv4MaxLatency = pRes.MaxLatencyMs
			item.IPv4Sent = pRes.Sent
			item.IPv4Received = pRes.Received
			item.IPv4Loss = pRes.LossPercent
			item.IPv4Status = pRes.Status
			item.IPv4Method = pRes.Method
			item.IPv4Latencies = pRes.Latencies
		} else {
			item.IPv4Status = "unreachable"
			item.IPv4Loss = 100.0
			item.IPv4Sent = 4
		}
	} else {
		item.IPv6Target = clientIP
		if pRes, err := s.askNodeNetworkProbe(ctx, sourceNodeID, clientIP, 0, 4); err == nil {
			item.IPv6LatencyMs = pRes.AvgLatencyMs
			item.IPv6MinLatency = pRes.MinLatencyMs
			item.IPv6MaxLatency = pRes.MaxLatencyMs
			item.IPv6Sent = pRes.Sent
			item.IPv6Received = pRes.Received
			item.IPv6Loss = pRes.LossPercent
			item.IPv6Status = pRes.Status
			item.IPv6Method = pRes.Method
			item.IPv6Latencies = pRes.Latencies
		} else {
			item.IPv6Status = "unreachable"
			item.IPv6Loss = 100.0
			item.IPv6Sent = 4
		}
	}

	return item, nil
}

// ExecuteNodeSpeedtest asks an agent to run an autonomous TcpQuality test and returns the report URL.
// Master does NOT perform any network probing directly.
func (s *Server) ExecuteNodeSpeedtest(ctx context.Context, nodeID string) (*NodeSpeedtest, error) {
	node, err := s.store.GetNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if !node.Online {
		return nil, errors.New("服务器当前处于离线状态，无法进行检测")
	}

	data, err := s.AskNode(ctx, nodeID, "speedtest.run", "{}")
	if err != nil {
		return nil, fmt.Errorf("执行 TcpQuality 检测失败: %w", err)
	}

	var res struct {
		ReportURL string `json:"report_url"`
	}
	if err := json.Unmarshal([]byte(data), &res); err != nil {
		return nil, fmt.Errorf("解析检测数据失败: %w", err)
	}
	if res.ReportURL == "" {
		return nil, errors.New("未能获取到测试报告 URL")
	}

	st := NodeSpeedtest{
		NodeID:    nodeID,
		ReportURL: res.ReportURL,
		TestedAt:  time.Now().UTC().Format(time.RFC3339),
	}

	if err := s.store.SaveNodeSpeedtest(ctx, st); err != nil {
		log.Printf("save node speedtest error: %v", err)
	}

	if s.alertEngine != nil {
		s.alertEngine.NotifySpeedtestReport(node.Name, st)
	}

	latest, _ := s.store.GetLatestNodeSpeedtest(ctx, nodeID)
	if latest != nil {
		return latest, nil
	}
	return &st, nil
}
