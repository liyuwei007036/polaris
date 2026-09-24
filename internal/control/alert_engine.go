package control

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

type AlertEngine struct {
	store      *Store
	bark       *BarkClient
	mu         sync.Mutex
	cooldowns  map[string]time.Time
	highRateAt map[string]time.Time
	cached     AlertSettings
	cacheTime  time.Time
}

func NewAlertEngine(store *Store) *AlertEngine {
	return &AlertEngine{
		store:      store,
		bark:       NewBarkClient(),
		cooldowns:  make(map[string]time.Time),
		highRateAt: make(map[string]time.Time),
	}
}

func (e *AlertEngine) getSettings() (AlertSettings, error) {
	e.mu.Lock()
	if !e.cacheTime.IsZero() && time.Since(e.cacheTime) < 30*time.Second {
		s := e.cached
		e.mu.Unlock()
		return s, nil
	}
	e.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s, err := e.store.GetAlertSettings(ctx)
	if err != nil {
		e.mu.Lock()
		defer e.mu.Unlock()
		if !e.cacheTime.IsZero() {
			return e.cached, nil
		}
		return AlertSettings{}, err
	}
	e.mu.Lock()
	e.cached = s
	e.cacheTime = time.Now()
	e.mu.Unlock()
	return s, nil
}

func (e *AlertEngine) InvalidateCache() {
	e.mu.Lock()
	e.cacheTime = time.Time{}
	e.mu.Unlock()
}

// canAlert checks if the alert key is outside of cooldown. If so, updates the cooldown time and returns true.
func (e *AlertEngine) canAlert(key string, cooldownMinutes int) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	last, exists := e.cooldowns[key]
	if exists && time.Since(last) < time.Duration(cooldownMinutes)*time.Minute {
		return false
	}
	e.cooldowns[key] = time.Now()
	return true
}

func (e *AlertEngine) sendAlert(ctx context.Context, title, body, group, level, sound, url string) {
	go func() {
		settings, err := e.getSettings()
		if err != nil || settings.BarkDeviceKey == "" {
			return
		}
		if settings.BarkSound != "" {
			sound = settings.BarkSound
		} else if sound == "" {
			sound = "minuet"
		}
		if group == "" {
			group = settings.BarkGroup
		}
		msg := BarkMessage{
			Title: title,
			Body:  body,
			Group: group,
			Sound: sound,
			Level: level,
			URL:   url,
		}
		pushCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := e.bark.Send(pushCtx, settings.BarkServer, settings.BarkDeviceKey, msg); err != nil {
			log.Printf("failed to send bark notification (%s): %v", title, err)
		}
	}()
}

// CheckConnectionsTelemetry evaluates live connection pushes from nodes against defined thresholds.
func (e *AlertEngine) CheckConnectionsTelemetry(nodeID, nodeName string, downloadRate, uploadRate float64, connections []storedConnection) {
	settings, err := e.getSettings()
	if err != nil || settings.BarkDeviceKey == "" {
		return
	}

	now := time.Now()
	totalRateBytes := downloadRate + uploadRate
	maxRateMbps := (totalRateBytes * 8) / (1000 * 1000)

	// 1. Instantaneous Bandwidth Spike Check
	if settings.TrafficAlertEnabled && settings.TrafficThresholdMbps > 0 {
		rateThresholdBytes := (settings.TrafficThresholdMbps * 1000 * 1000) / 8
		if totalRateBytes >= rateThresholdBytes {
			e.mu.Lock()
			start, exists := e.highRateAt[nodeID]
			if !exists {
				e.highRateAt[nodeID] = now
				start = now
			}
			durationExceeded := now.Sub(start) >= time.Duration(settings.TrafficDurationSec)*time.Second
			e.mu.Unlock()

			if durationExceeded {
				alertKey := "traffic:" + nodeID
				if e.canAlert(alertKey, settings.CooldownMinutes) {
					// Identify main destination and source IP
					var mainDest, mainIP string
					var maxConnBytes int64
					for _, c := range connections {
						b := c.Upload + c.Download
						if b > maxConnBytes {
							maxConnBytes = b
							mainDest = c.Host
							if mainDest == "" {
								mainDest = c.Destination
							}
							mainIP = c.SourceIP
						}
					}
					body := fmt.Sprintf("服务器 [%s] 当前速率 %.1f Mbps，已持续超过 %d 秒。\n主消耗目标: %s\n主要来源: %s",
						nodeName, maxRateMbps, settings.TrafficDurationSec, mainDest, mainIP)
					e.sendAlert(context.Background(), "📈 [Polaris] 瞬时流量突发预警", body, "Polaris-流量监控", "active", "minuet", "")
				}
			}
		} else {
			e.mu.Lock()
			delete(e.highRateAt, nodeID)
			e.mu.Unlock()
		}
	}

	// 2. Instantaneous Active Connection Surge Check
	connCount := len(connections)
	if settings.ConnAlertEnabled && settings.ConnThresholdCount > 0 && connCount >= settings.ConnThresholdCount {
		alertKey := "conn:" + nodeID
		if e.canAlert(alertKey, settings.CooldownMinutes) {
			body := fmt.Sprintf("服务器 [%s] 活跃连接数异常飙升至 %d 条 (设定上限: %d 条)。\n请注意排查多线程并发或异常探测。",
				nodeName, connCount, settings.ConnThresholdCount)
			e.sendAlert(context.Background(), "⚡ [Polaris] 活跃连接数激增预警", body, "Polaris-连接监控", "active", "bell", "")
		}
	}

	// 3. Single IP Connection Surge Check
	if settings.SingleIPAlertEnabled && settings.SingleIPThresholdCount > 0 {
		ipCounts := make(map[string]int)
		for _, c := range connections {
			ip := c.SourceIP
			if h, _, err := net.SplitHostPort(ip); err == nil {
				ip = h
			}
			ip = strings.TrimSpace(ip)
			if ip != "" {
				ipCounts[ip]++
			}
		}
		for ip, count := range ipCounts {
			if count >= settings.SingleIPThresholdCount {
				alertKey := "single_ip:" + nodeID + ":" + ip
				if e.canAlert(alertKey, settings.CooldownMinutes) {
					body := fmt.Sprintf("服务器 [%s] 发现单 IP 突发高并发连接！\n来源 IP: %s\n当前连接: %d 条 (上限: %d 条)\n请注意核实是否为本人设备，警惕凭据泄露。",
						nodeName, ip, count, settings.SingleIPThresholdCount)
					e.sendAlert(context.Background(), "🚨 [Polaris] 单 IP 异常高并发预警", body, "Polaris-安全警报", "active", "alarm", "")
				}
			}
		}
	}
}

// NotifyNodeOffline sends an alert when a node loses contact with master.
func (e *AlertEngine) NotifyNodeOffline(nodeID, nodeName string) {
	settings, err := e.getSettings()
	if err != nil || !settings.OfflineAlertEnabled {
		return
	}
	alertKey := "offline:" + nodeID
	if e.canAlert(alertKey, settings.CooldownMinutes) {
		body := fmt.Sprintf("服务器 [%s] 已失去心跳连接，可能处于关机、死机或网络故障状态。", nodeName)
		e.sendAlert(context.Background(), "⚠️ [Polaris] 节点已离线", body, "Polaris-节点状态", "critical", "alarm", "")
	}
}

// NotifyNodeOnline sends a recovery notice when a node connects.
func (e *AlertEngine) NotifyNodeOnline(nodeID, nodeName string) {
	settings, err := e.getSettings()
	if err != nil || !settings.OfflineAlertEnabled {
		return
	}
	alertKey := "online:" + nodeID
	if e.canAlert(alertKey, settings.CooldownMinutes) {
		body := fmt.Sprintf("服务器 [%s] 重新建立加密连接，已恢复正常在线服务。", nodeName)
		e.sendAlert(context.Background(), "✅ [Polaris] 节点已恢复正常", body, "Polaris-节点状态", "active", "calypso", "")
	}
}

// NotifyFail2BanBlock notifies when an attacker or scanner is blocked.
func (e *AlertEngine) NotifyFail2BanBlock(nodeID, nodeName, ip, jail string) {
	alertKey := "f2b:" + nodeID + ":" + ip
	settings, err := e.getSettings()
	if err != nil {
		return
	}
	if e.canAlert(alertKey, settings.CooldownMinutes) {
		body := fmt.Sprintf("服务器 [%s] 防护系统已拦截恶意扫描源。\n封禁 IP: %s\n触发规则: %s", nodeName, ip, jail)
		e.sendAlert(context.Background(), "🛡️ [Polaris] 拦截恶意扫描源", body, "Polaris-防御日志", "passive", "", "")
	}
}

// NotifyConsoleBruteForce alerts on repeated failed login attempts.
func (e *AlertEngine) NotifyConsoleBruteForce(clientIP string, attempts int) {
	alertKey := "bruteforce:" + clientIP
	if e.canAlert(alertKey, 15) {
		body := fmt.Sprintf("控制台检测到来源 IP %s 连续密码错误 %d 次，已自动触发安全封禁。", clientIP, attempts)
		e.sendAlert(context.Background(), "🔒 [Polaris] 控制台防爆破触发", body, "Polaris-系统安全", "active", "silence", "")
	}
}

// ProbeResultItem holds one node's probe outcome.
type ProbeResultItem struct {
	NodeID      string `json:"node_id"`
	NodeName    string `json:"node_name"`
	TargetHost  string `json:"target_host,omitempty"`
	TargetPort  uint16 `json:"target_port,omitempty"`
	OverseasOK  bool   `json:"overseas_ok"`
	DomesticOK  bool   `json:"domestic_ok"`
	ProbeSource string `json:"probe_source,omitempty"`
	PacketLoss  int    `json:"packet_loss"`
	LatencyMs   int    `json:"latency_ms"`
	StatusDesc  string `json:"status_desc,omitempty"`
	Error       string `json:"error,omitempty"`
}

// NotifyProbeSummary dispatches a Bark report after a scheduled GFW/connectivity probe.
func (e *AlertEngine) NotifyProbeSummary(items []ProbeResultItem) {
	settings, err := e.getSettings()
	if err != nil || settings.BarkDeviceKey == "" {
		return
	}

	var blockedList []string
	var normalList []string

	for _, item := range items {
		port := item.TargetPort
		if port == 0 {
			port = 443
		}
		// Overseas reachable but Domestic unreachable -> GFW Blocked!
		if item.OverseasOK && !item.DomesticOK {
			src := item.ProbeSource
			if src == "" {
				src = "国内真实探针"
			}
			blockedList = append(blockedList, fmt.Sprintf("• [%s] 端口 %d 国内全超时 (疑似被 GFW 阻断, 来源: %s)", item.NodeName, port, src))
		} else if item.DomesticOK {
			src := item.ProbeSource
			if src == "" {
				src = "国内真实探针"
			}
			normalList = append(normalList, fmt.Sprintf("• [%s] 端口 %d 正常 (国内时延: %d ms, 来源: %s)", item.NodeName, port, item.LatencyMs, src))
		} else {
			normalList = append(normalList, fmt.Sprintf("• [%s] 离线/境外未监听", item.NodeName))
		}
	}

	nowStr := time.Now().Format("15:04")

	if len(blockedList) > 0 {
		if settings.GFWAlertEnabled || settings.ProbeNotifyAlways {
			body := strings.Join(blockedList, "\n")
			if len(normalList) > 0 {
				body += "\n" + strings.Join(normalList, "\n")
			}
			body += fmt.Sprintf("\n🕒 探测时间: %s", nowStr)
			e.sendAlert(context.Background(), "🚨 [Polaris] 发现节点被阻断", body, "Polaris-巡检报告", "critical", "critical", "")
			return
		}
	}

	// If no block and user requested always notify on probe
	if settings.ProbeNotifyAlways && len(items) > 0 {
		body := strings.Join(normalList, "\n") + fmt.Sprintf("\n🕒 巡检时间: %s (国内探针核验)", nowStr)
		e.sendAlert(context.Background(), "✅ [Polaris 巡检] 全部节点连接通畅", body, "Polaris-巡检报告", "active", "telegraph", "")
	}
}

// NotifySpeedtestReport sends a Bark summary after a speedtest run.
func (e *AlertEngine) NotifySpeedtestReport(nodeName string, st NodeSpeedtest) {
	settings, err := e.getSettings()
	if err != nil || settings.BarkDeviceKey == "" {
		return
	}

	title := fmt.Sprintf("⚡ [测速与线路] %s 跑分结果", nodeName)
	tcStr := fmt.Sprintf("电信: %d ms (%.1f Mbps)", st.TelecomLatencyMs, st.TelecomSpeedMbps)
	if st.TelecomRoute != "" {
		tcStr = fmt.Sprintf("电信: %d ms (%s / %.1f Mbps)", st.TelecomLatencyMs, st.TelecomRoute, st.TelecomSpeedMbps)
	}
	ucStr := fmt.Sprintf("联通: %d ms (%.1f Mbps)", st.UnicomLatencyMs, st.UnicomSpeedMbps)
	if st.UnicomRoute != "" {
		ucStr = fmt.Sprintf("联通: %d ms (%s / %.1f Mbps)", st.UnicomLatencyMs, st.UnicomRoute, st.UnicomSpeedMbps)
	}
	mbStr := fmt.Sprintf("移动: %d ms (%.1f Mbps)", st.MobileLatencyMs, st.MobileSpeedMbps)
	if st.MobileRoute != "" {
		mbStr = fmt.Sprintf("移动: %d ms (%s / %.1f Mbps)", st.MobileLatencyMs, st.MobileRoute, st.MobileSpeedMbps)
	}

	body := fmt.Sprintf("%s\n%s\n%s\n🕒 测试时间: %s",
		tcStr, ucStr, mbStr,
		time.Now().Format("15:04"))

	e.sendAlert(context.Background(), title, body, "Polaris-巡检报告", "active", "telegraph", "")
}
