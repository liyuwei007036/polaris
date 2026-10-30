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

	// 在线节点：委托 Agent 自治检测，Master 不参与网络探测
	st, err := s.ExecuteNodeSpeedtest(ctx, node.ID)
	if err != nil {
		latest, _ := s.store.GetLatestNodeSpeedtest(ctx, node.ID)
		if latest != nil {
			st = latest
		} else {
			item.OverseasOK = true
			item.DomesticOK = false
			item.GFWBlocked = false
			item.ProbeExecuted = false
			item.Error = err.Error()
			item.StatusDesc = "执行 Agent 检测失败: " + err.Error()
			return item
		}
	}

	item.OverseasOK = true
	item.DomesticOK = !st.GFWBlocked && (st.DomesticLatency > 0 || st.GFWStatus == "normal")
	item.GFWBlocked = st.GFWBlocked
	item.ProbeExecuted = true
	item.LatencyMs = st.DomesticLatency
	item.ProbeSource = st.DomesticSource
	item.InboundTelecomRoute = st.InboundTelecomRoute
	item.InboundTelecomLatencyMs = st.InboundTelecomLatencyMs
	item.InboundTelecomNodeLatencies = st.InboundTelecomNodeLatencies
	item.InboundTelecomTracePath = st.InboundTelecomTracePath
	item.InboundUnicomRoute = st.InboundUnicomRoute
	item.InboundUnicomLatencyMs = st.InboundUnicomLatencyMs
	item.InboundUnicomNodeLatencies = st.InboundUnicomNodeLatencies
	item.InboundUnicomTracePath = st.InboundUnicomTracePath
	item.InboundMobileRoute = st.InboundMobileRoute
	item.InboundMobileLatencyMs = st.InboundMobileLatencyMs
	item.InboundMobileNodeLatencies = st.InboundMobileNodeLatencies
	item.InboundMobileTracePath = st.InboundMobileTracePath

	if st.GFWBlocked {
		item.StatusDesc = "端口阻断 (探针全部超时)"
	} else if item.DomesticOK {
		item.StatusDesc = "正常通行"
	} else if st.DomesticSource != "" {
		item.StatusDesc = st.DomesticSource
	} else {
		item.StatusDesc = "正常通行"
	}

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

// ExecuteNodeSpeedtest asks an agent to run an autonomous speedtest and reachability check.
// Master does NOT perform any network probing directly.
func (s *Server) ExecuteNodeSpeedtest(ctx context.Context, nodeID string) (*NodeSpeedtest, error) {
	node, err := s.store.GetNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if !node.Online {
		return nil, errors.New("服务器当前处于离线状态，无法进行检测")
	}

	nodeLoc := s.getNodeLocationLabel(node)

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
	targetPort := uint16(443)
	if targetHost != "" {
		listeners, err := s.store.ListListeners(ctx, node.ID)
		if err == nil && len(listeners) > 0 {
			for _, l := range listeners {
				if l.Enabled && l.Port > 0 {
					targetPort = l.Port
					break
				}
			}
		}
	}

	endpoints := s.GetSpeedtestEndpoints(ctx, nodeID)
	payload := struct {
		TargetHost       string              `json:"target_host,omitempty"`
		TargetPort       uint16              `json:"target_port,omitempty"`
		TelecomEndpoints []SpeedtestEndpoint `json:"telecom_endpoints,omitempty"`
		UnicomEndpoints  []SpeedtestEndpoint `json:"unicom_endpoints,omitempty"`
		MobileEndpoints  []SpeedtestEndpoint `json:"mobile_endpoints,omitempty"`
	}{
		TargetHost:       targetHost,
		TargetPort:       targetPort,
		TelecomEndpoints: endpoints.TelecomEndpoints,
		UnicomEndpoints:  endpoints.UnicomEndpoints,
		MobileEndpoints:  endpoints.MobileEndpoints,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("构造测速载荷失败: %w", err)
	}

	data, err := s.AskNode(ctx, nodeID, "speedtest.run", string(payloadBytes))
	if err != nil {
		return nil, fmt.Errorf("执行三网检测任务失败: %w", err)
	}

	var res struct {
		TelecomLatencyMs            int     `json:"telecom_latency_ms"`
		TelecomSpeedMbps            float64 `json:"telecom_speed_mbps"`
		TelecomRoute                string  `json:"telecom_route"`
		TelecomNodeLatencies        string  `json:"telecom_node_latencies"`
		TelecomTracePath            string  `json:"telecom_trace_path"`
		UnicomLatencyMs             int     `json:"unicom_latency_ms"`
		UnicomSpeedMbps             float64 `json:"unicom_speed_mbps"`
		UnicomRoute                 string  `json:"unicom_route"`
		UnicomNodeLatencies         string  `json:"unicom_node_latencies"`
		UnicomTracePath             string  `json:"unicom_trace_path"`
		MobileLatencyMs             int     `json:"mobile_latency_ms"`
		MobileSpeedMbps             float64 `json:"mobile_speed_mbps"`
		MobileRoute                 string  `json:"mobile_route"`
		MobileNodeLatencies         string  `json:"mobile_node_latencies"`
		MobileTracePath             string  `json:"mobile_trace_path"`
		InboundTelecomRoute         string  `json:"inbound_telecom_route"`
		InboundTelecomLatencyMs     int     `json:"inbound_telecom_latency_ms"`
		InboundTelecomNodeLatencies string  `json:"inbound_telecom_node_latencies"`
		InboundTelecomTracePath     string  `json:"inbound_telecom_trace_path"`
		InboundUnicomRoute          string  `json:"inbound_unicom_route"`
		InboundUnicomLatencyMs      int     `json:"inbound_unicom_latency_ms"`
		InboundUnicomNodeLatencies  string  `json:"inbound_unicom_node_latencies"`
		InboundUnicomTracePath      string  `json:"inbound_unicom_trace_path"`
		InboundMobileRoute          string  `json:"inbound_mobile_route"`
		InboundMobileLatencyMs      int     `json:"inbound_mobile_latency_ms"`
		InboundMobileNodeLatencies  string  `json:"inbound_mobile_node_latencies"`
		InboundMobileTracePath      string  `json:"inbound_mobile_trace_path"`
		GFWBlocked                  bool    `json:"gfw_blocked"`
		GFWStatus                   string  `json:"gfw_status"`
		DomesticLatency             int     `json:"domestic_latency_ms"`
		DomesticSource              string  `json:"domestic_source"`
	}
	if err := json.Unmarshal([]byte(data), &res); err != nil {
		return nil, fmt.Errorf("解析检测数据失败: %w", err)
	}

	enrichOutboundTrace := func(trace, defaultTarget string, latency int) string {
		if trace == "" {
			if latency <= 0 {
				return ""
			}
			return fmt.Sprintf("%s ➔ %s (骨干跃点未回显)", nodeLoc, defaultTarget)
		}
		t := strings.Replace(trace, "节点 ➔", nodeLoc+" ➔", 1)
		t = strings.Replace(t, "本地节点 ➔", nodeLoc+" ➔", 1)
		t = strings.Replace(t, "服务器节点 ➔", nodeLoc+" ➔", 1)
		t = strings.Replace(t, "➔ 目标", "➔ "+defaultTarget, 1)
		if !strings.HasPrefix(t, nodeLoc) && !strings.Contains(t, nodeLoc) {
			t = fmt.Sprintf("%s ➔ %s", nodeLoc, t)
		}
		return t
	}

	st := NodeSpeedtest{
		NodeID:                      nodeID,
		TelecomLatencyMs:            res.TelecomLatencyMs,
		TelecomSpeedMbps:            res.TelecomSpeedMbps,
		TelecomRoute:                res.TelecomRoute,
		TelecomNodeLatencies:        res.TelecomNodeLatencies,
		TelecomTracePath:            enrichOutboundTrace(res.TelecomTracePath, "电信网络", res.TelecomLatencyMs),
		UnicomLatencyMs:             res.UnicomLatencyMs,
		UnicomSpeedMbps:             res.UnicomSpeedMbps,
		UnicomRoute:                 res.UnicomRoute,
		UnicomNodeLatencies:         res.UnicomNodeLatencies,
		UnicomTracePath:             enrichOutboundTrace(res.UnicomTracePath, "联通网络", res.UnicomLatencyMs),
		MobileLatencyMs:             res.MobileLatencyMs,
		MobileSpeedMbps:             res.MobileSpeedMbps,
		MobileRoute:                 res.MobileRoute,
		MobileNodeLatencies:         res.MobileNodeLatencies,
		MobileTracePath:             enrichOutboundTrace(res.MobileTracePath, "移动网络", res.MobileLatencyMs),
		InboundTelecomRoute:         res.InboundTelecomRoute,
		InboundTelecomLatencyMs:     res.InboundTelecomLatencyMs,
		InboundTelecomNodeLatencies: res.InboundTelecomNodeLatencies,
		InboundTelecomTracePath:     res.InboundTelecomTracePath,
		InboundUnicomRoute:          res.InboundUnicomRoute,
		InboundUnicomLatencyMs:      res.InboundUnicomLatencyMs,
		InboundUnicomNodeLatencies:  res.InboundUnicomNodeLatencies,
		InboundUnicomTracePath:      res.InboundUnicomTracePath,
		InboundMobileRoute:          res.InboundMobileRoute,
		InboundMobileLatencyMs:      res.InboundMobileLatencyMs,
		InboundMobileNodeLatencies:  res.InboundMobileNodeLatencies,
		InboundMobileTracePath:      res.InboundMobileTracePath,
		GFWBlocked:                  res.GFWBlocked,
		GFWStatus:                   res.GFWStatus,
		DomesticLatency:             res.DomesticLatency,
		DomesticSource:              res.DomesticSource,
	}

	if st.GFWStatus == "" {
		if st.GFWBlocked {
			st.GFWStatus = "blocked"
		} else {
			st.GFWStatus = "normal"
		}
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

type SpeedtestEndpoint struct {
	City string `json:"city"`
	Host string `json:"host"`
}

type SpeedtestEndpointsPayload struct {
	TelecomEndpoints []SpeedtestEndpoint `json:"telecom_endpoints"`
	UnicomEndpoints  []SpeedtestEndpoint `json:"unicom_endpoints"`
	MobileEndpoints  []SpeedtestEndpoint `json:"mobile_endpoints"`
}

// GetSpeedtestEndpoints returns authoritative China carrier backbone targets for return route (返程) testing.
// Zero hardcoded IPs: domain names are resolved dynamically via DNS, and geographic city / carrier metadata
// are resolved dynamically via ip2region and BGP ASN.
func (s *Server) GetSpeedtestEndpoints(ctx context.Context, currentNodeID string) SpeedtestEndpointsPayload {
	resolveEndpoint := func(domain string) SpeedtestEndpoint {
		label := domain
		if ips, err := net.LookupHost(domain); err == nil && len(ips) > 0 {
			ip := ips[0]
			city := ""
			if s.ipLocator != nil {
				city = s.ipLocator.LocateCity(ip)
			}
			if city == "" {
				city = wire.IPv4ToCity(ip)
			}
			asn := wire.IPv4ToASN(ip)
			carrier := wire.ASNToName(asn)
			if city != "" && carrier != "" {
				label = fmt.Sprintf("%s%s", city, carrier)
			} else if city != "" {
				label = city
			} else if carrier != "" {
				label = carrier
			}
		}
		return SpeedtestEndpoint{
			City: label,
			Host: domain,
		}
	}

	return SpeedtestEndpointsPayload{
		TelecomEndpoints: []SpeedtestEndpoint{
			resolveEndpoint("ipv4.sha-4134.endpoint.nxtrace.org"),
			resolveEndpoint("ipv4.pek-4134.endpoint.nxtrace.org"),
			resolveEndpoint("ipv4.can-4134.endpoint.nxtrace.org"),
		},
		UnicomEndpoints: []SpeedtestEndpoint{
			resolveEndpoint("ipv4.sha-4837.endpoint.nxtrace.org"),
			resolveEndpoint("ipv4.pek-4837.endpoint.nxtrace.org"),
			resolveEndpoint("ipv4.can-4837.endpoint.nxtrace.org"),
		},
		MobileEndpoints: []SpeedtestEndpoint{
			resolveEndpoint("ipv4.sha-9808.endpoint.nxtrace.org"),
			resolveEndpoint("ipv4.pek-9808.endpoint.nxtrace.org"),
			resolveEndpoint("ipv4.can-9808.endpoint.nxtrace.org"),
		},
	}
}
