package control

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// NotificationLocation returns the location used for alert notification timestamps: Asia/Shanghai (CST, UTC+8).
// If the TZ environment variable is set, it respects TZ.
var NotificationLocation = func() *time.Location {
	if tz := os.Getenv("TZ"); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			return loc
		}
	}
	if loc, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		return loc
	}
	return time.FixedZone("CST", 8*3600)
}()

// NowAlertTime returns current time formatted in Beijing Time (Asia/Shanghai, UTC+8).
func NowAlertTime() string {
	return FormatAlertTime(time.Now())
}

// FormatAlertTime formats a time in the notification timezone (Asia/Shanghai, UTC+8).
func FormatAlertTime(t time.Time) string {
	return t.In(NotificationLocation).Format("2006-01-02 15:04:05")
}

type AlertEngine struct {
	store         *Store
	bark          *BarkClient
	locator       *ipLocator
	mu            sync.Mutex
	cooldowns     map[string]time.Time
	highRateAt    map[string]time.Time
	loginFailures map[string]int
	loginFailAt   map[string]time.Time
	cached        AlertSettings
	cacheTime     time.Time
}

func NewAlertEngine(store *Store, locator *ipLocator) *AlertEngine {
	if locator == nil {
		locator, _ = newIPLocator()
	}
	return &AlertEngine{
		store:         store,
		bark:          NewBarkClient(),
		locator:       locator,
		cooldowns:     make(map[string]time.Time),
		highRateAt:    make(map[string]time.Time),
		loginFailures: make(map[string]int),
		loginFailAt:   make(map[string]time.Time),
	}
}

func (e *AlertEngine) SetIPLocator(locator *ipLocator) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.locator = locator
}

func (e *AlertEngine) formatIP(ipStr string) string {
	clean := strings.TrimSpace(ipStr)
	if h, _, err := net.SplitHostPort(clean); err == nil {
		clean = h
	}
	if clean == "" || clean == "-" {
		return "-"
	}
	loc := "未知归属地"
	if e.locator != nil {
		l := e.locator.Locate(clean)
		if l != "" && l != "未知" {
			loc = l
		}
	}
	return fmt.Sprintf("%s (%s)", clean, loc)
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
		if sound == "" {
			if settings.BarkSound != "" {
				sound = settings.BarkSound
			} else {
				sound = "minuet.caf"
			}
		}
		if !strings.HasSuffix(sound, ".caf") {
			sound = sound + ".caf"
		}
		if group == "" {
			group = settings.BarkGroup
		}
		level = "active"
		msg := BarkMessage{
			DeviceKey: settings.BarkDeviceKey,
			Title:     title,
			Body:      body,
			Group:     group,
			Sound:     sound,
			Level:     level,
			URL:       url,
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

	nonRelay := make([]storedConnection, 0, len(connections))
	for _, c := range connections {
		if !c.IsChainRelay {
			nonRelay = append(nonRelay, c)
		}
	}
	connections = nonRelay

	now := time.Now()
	timeStr := FormatAlertTime(now)
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
					if mainDest == "" {
						mainDest = "未指定域名/IP"
					}
					body := fmt.Sprintf("• 服务器: %s\n• 触发速率: %.1f Mbps (阈值: %.1f Mbps)\n• 持续时长: 超过 %d 秒\n• 主要来源: %s\n• 目标主机: %s\n🕒 记录时间: %s",
						nodeName, maxRateMbps, settings.TrafficThresholdMbps, settings.TrafficDurationSec, e.formatIP(mainIP), mainDest, timeStr)
					e.sendAlert(context.Background(), "📈 [Polaris] 瞬时流量突发预警", body, "Polaris-流量监控", "active", "bell.caf", "")
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
			body := fmt.Sprintf("• 服务器: %s\n• 当前连接: %d 条 (设定上限: %d 条)\n• 状态提示: 活跃连接数异常飙升，请排查多线程并发或外部探测\n🕒 记录时间: %s",
				nodeName, connCount, settings.ConnThresholdCount, timeStr)
			e.sendAlert(context.Background(), "⚡ [Polaris] 活跃连接数激增预警", body, "Polaris-连接监控", "active", "anticipate.caf", "")
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
					body := fmt.Sprintf("• 服务器: %s\n• 来源 IP: %s\n• 活跃连接: %d 条 (设定上限: %d 条)\n• 安全提示: 发现单 IP 突发高并发，请核实是否为本人设备或凭据泄露\n🕒 记录时间: %s",
						nodeName, e.formatIP(ip), count, settings.SingleIPThresholdCount, timeStr)
					e.sendAlert(context.Background(), "🚨 [Polaris] 单 IP 异常高并发预警", body, "Polaris-安全警报", "active", "horn.caf", "")
				}
			}
		}
	}

	// 4. Abnormal Port/Host Scanning Detection (strictly exempts standard domain-name web browsing)
	if !settings.ScanAlertEnabled {
		return
	}

	sensitiveScanPorts := map[int]string{
		21:    "FTP",
		22:    "SSH",
		23:    "Telnet",
		25:    "SMTP",
		135:   "RPC",
		137:   "NetBIOS",
		139:   "NetBIOS",
		445:   "SMB",
		1433:  "MSSQL",
		1521:  "Oracle",
		3306:  "MySQL",
		3389:  "RDP",
		5432:  "PostgreSQL",
		5900:  "VNC",
		6379:  "Redis",
		9200:  "Elasticsearch",
		11211: "Memcached",
		27017: "MongoDB",
	}

	type clientScanStats struct {
		targetPorts      map[string]map[int]struct{}
		sensitiveTargets map[string]string
		rawIPTargets     map[string]struct{}
	}

	clientStats := make(map[string]*clientScanStats)

	for _, c := range connections {
		clientIP := strings.TrimSpace(c.SourceIP)
		if h, _, err := net.SplitHostPort(clientIP); err == nil {
			clientIP = h
		}
		if clientIP == "" {
			continue
		}

		dest := strings.TrimSpace(c.Destination)
		destHost := dest
		destPort := 0
		if h, pStr, err := net.SplitHostPort(dest); err == nil {
			destHost = h
			if p, err := strconv.Atoi(pStr); err == nil {
				destPort = p
			}
		}

		// Check if host is a valid domain name (FQDN)
		host := strings.TrimSpace(c.Host)
		isDomain := false
		if host != "" && net.ParseIP(host) == nil && strings.Contains(host, ".") {
			for _, r := range host {
				if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
					isDomain = true
					break
				}
			}
		}

		// Standard web browsing: domain name on common web ports (80, 443, 8080, 8443) or port 0
		// MUST be completely exempted! Browsing modern websites naturally opens dozens of domain connections.
		if isDomain && (destPort == 80 || destPort == 443 || destPort == 8080 || destPort == 8443 || destPort == 0) {
			continue
		}

		// Also exempt domain-based connections to other common application services unless targeting sensitive ports
		if isDomain {
			if _, isSensitive := sensitiveScanPorts[destPort]; !isSensitive {
				continue
			}
		}

		st, exists := clientStats[clientIP]
		if !exists {
			st = &clientScanStats{
				targetPorts:      make(map[string]map[int]struct{}),
				sensitiveTargets: make(map[string]string),
				rawIPTargets:     make(map[string]struct{}),
			}
			clientStats[clientIP] = st
		}

		if destHost != "" && destPort > 0 {
			if _, ok := st.targetPorts[destHost]; !ok {
				st.targetPorts[destHost] = make(map[int]struct{})
			}
			st.targetPorts[destHost][destPort] = struct{}{}
		}

		if svc, isSensitive := sensitiveScanPorts[destPort]; isSensitive {
			st.sensitiveTargets[dest] = svc
		}

		if !isDomain && destHost != "" && net.ParseIP(destHost) != nil {
			st.rawIPTargets[destHost] = struct{}{}
		}
	}

	for clientIP, st := range clientStats {
		var triggerReason, triggerDetails string

		// Condition A: Vertical Port Scan (single destination IP with >= 10 distinct probed ports)
		for targetIP, ports := range st.targetPorts {
			if len(ports) >= 10 {
				var portStrs []string
				for p := range ports {
					portStrs = append(portStrs, strconv.Itoa(p))
					if len(portStrs) >= 6 {
						break
					}
				}
				triggerReason = fmt.Sprintf("单目标多端口嗅探 (尝试连接目标 %s 的 %d 个不同端口)", targetIP, len(ports))
				triggerDetails = fmt.Sprintf("目标地址: %s, 端口样例: %s", targetIP, strings.Join(portStrs, ", "))
				break
			}
		}

		// Condition B: Sensitive Port Sweep (probing high-risk ports across >= 5 distinct targets)
		if triggerReason == "" && len(st.sensitiveTargets) >= 5 {
			var samples []string
			for target, svc := range st.sensitiveTargets {
				samples = append(samples, fmt.Sprintf("%s (%s)", target, svc))
				if len(samples) >= 5 {
					break
				}
			}
			triggerReason = fmt.Sprintf("高危服务端口扫描 (并发探测 %d 个敏感服务端口)", len(st.sensitiveTargets))
			triggerDetails = fmt.Sprintf("样例: %s", strings.Join(samples, ", "))
		}

		// Condition C: Mass Raw IP Sweep (>= 40 distinct raw IPs without domain names on non-web ports)
		if triggerReason == "" && len(st.rawIPTargets) >= 40 {
			var samples []string
			for ip := range st.rawIPTargets {
				samples = append(samples, ip)
				if len(samples) >= 5 {
					break
				}
			}
			triggerReason = fmt.Sprintf("纯 IP 地址段并发漫游扫描 (并发连接 %d 个纯 IP 端点)", len(st.rawIPTargets))
			triggerDetails = fmt.Sprintf("目标样例: %s", strings.Join(samples, ", "))
		}

		if triggerReason != "" {
			alertKey := "scan:" + nodeID + ":" + clientIP
			cooldown := settings.CooldownMinutes
			if cooldown < 15 {
				cooldown = 15
			}
			if e.canAlert(alertKey, cooldown) {
				body := fmt.Sprintf("• 服务器: %s\n• 来源 IP: %s\n• 扫描特征: %s\n• 探测详情: %s\n• 安全建议: 疑似端口扫描或异常嗅探行为，请排查该来源 IP 或前往安全防护添加阻断规则\n🕒 发现时间: %s",
					nodeName, e.formatIP(clientIP), triggerReason, triggerDetails, timeStr)
				e.sendAlert(context.Background(), "🚨 [Polaris] 检测到异常网络扫描", body, "Polaris-安全警报", "active", "alarm.caf", "")
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
		body := fmt.Sprintf("• 服务器: %s\n• 节点状态: 已失去心跳连接 (离线)\n• 影响说明: 节点代理服务暂停，可能由于网络中断、机房维护或宕机\n🕒 离线时间: %s",
			nodeName, NowAlertTime())
		e.sendAlert(context.Background(), "⚠️ [Polaris] 节点已离线", body, "Polaris-节点状态", "active", "alarm.caf", "")
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
		body := fmt.Sprintf("• 服务器: %s\n• 节点状态: 重新建立加密控制通道 (在线)\n• 服务说明: 代理与监控服务已全部恢复就绪\n🕒 恢复时间: %s",
			nodeName, NowAlertTime())
		e.sendAlert(context.Background(), "✅ [Polaris] 节点已恢复正常", body, "Polaris-节点状态", "active", "calypso.caf", "")
	}
}

func (e *AlertEngine) NotifyServerExpiry(node Node, days int) {
	title := "⏳ [Polaris] 服务器即将到期"
	body := fmt.Sprintf("• 服务器: %s\n• 厂商: %s\n• 到期日期: %s\n• 剩余: %d 天", node.Name, node.Provider, node.ExpiresOn, days)
	if node.ProviderURL != "" {
		body += "\n• 管理入口: " + node.ProviderURL
	}
	e.sendAlert(context.Background(), title, body, "Polaris-服务器到期", "active", "", node.ProviderURL)
}

// NotifyDDNSUpdated sends an alert when a NAT node's public IP changes and DDNS is updated.
func (e *AlertEngine) NotifyDDNSUpdated(nodeID, nodeName, oldIP, currentIP, domain string) {
	alertKey := "ddns:" + nodeID + ":" + currentIP
	if e.canAlert(alertKey, 1) {
		body := fmt.Sprintf("• NAT 节点: %s\n• 原公网 IP: %s\n• 新公网 IP: %s\n• 解析域名: %s\n• 状态说明: Cloudflare DNS 解析已自动同步 (TTL: 60s)\n🕒 变更时间: %s",
			nodeName, e.formatIP(oldIP), e.formatIP(currentIP), domain, NowAlertTime())
		e.sendAlert(context.Background(), "🌐 [Polaris] NAT 节点 IP 变更与 DDNS 自动更新", body, "Polaris-DDNS监控", "active", "bell.caf", "")
	}
}

// NotifyChainedDNSHealing sends an alert when chained nodes are signaled to refresh DNS and heal connections.
func (e *AlertEngine) NotifyChainedDNSHealing(targetNodeName string, chainedCount int, domains []string, targetIP string) {
	alertKey := "dns_healing:" + targetNodeName + ":" + targetIP
	if e.canAlert(alertKey, 1) {
		body := fmt.Sprintf("• 出口 NAT 节点: %s\n• 依赖前置节点数: %d 台\n• 影响域名: %s\n• 目标最新 IP: %s\n• 联动状态: 已下发轻量自愈指令，重置旧连接并刷新解析\n🕒 触发时间: %s",
			targetNodeName, chainedCount, strings.Join(domains, ", "), e.formatIP(targetIP), NowAlertTime())
		e.sendAlert(context.Background(), "🔄 [Polaris] 链路自愈：已触发前置节点 DNS 刷新", body, "Polaris-链路自愈", "active", "anticipate.caf", "")
	}
}

// NotifyChainedDNSCompleted sends a notification when a chained node completes DNS refresh and verification.
func (e *AlertEngine) NotifyChainedDNSCompleted(nodeID, nodeName, summary string) {
	alertKey := "dns_completed:" + nodeID
	if e.canAlert(alertKey, 1) {
		body := fmt.Sprintf("• 前置中转节点: %s\n• 自愈执行结果: %s\n• 状态说明: 底层卡死连接已切断，DNS 状态已重载恢复\n🕒 完成时间: %s",
			nodeName, summary, NowAlertTime())
		e.sendAlert(context.Background(), "✅ [Polaris] 链路自愈成功：DNS 刷新与验证完成", body, "Polaris-链路自愈", "active", "calypso.caf", "")
	}
}

// NotifyFail2BanBlock notifies when an attacker or scanner is blocked.
func (e *AlertEngine) NotifyFail2BanBlock(nodeID, nodeName, ip, jail string) {
	alertKey := "f2b:" + nodeID + ":" + ip
	settings, err := e.getSettings()
	if err != nil {
		return
	}
	cooldown := settings.CooldownMinutes
	if cooldown < 5 {
		cooldown = 5
	}
	if e.canAlert(alertKey, cooldown) {
		body := fmt.Sprintf("• 服务器: %s\n• 拦截目标: %s\n• 触发规则: %s\n• 防护状态: 已自动加入节点防火墙阻断列表\n🕒 拦截时间: %s",
			nodeName, e.formatIP(ip), jail, NowAlertTime())
		e.sendAlert(context.Background(), "🛡️ [Polaris] 拦截恶意扫描源", body, "Polaris-防御日志", "active", "chime.caf", "")
	}
}

// NotifyConsoleBruteForce alerts on repeated failed login attempts.
func (e *AlertEngine) NotifyConsoleBruteForce(clientIP string, attempts int) {
	alertKey := "bruteforce:" + clientIP
	if e.canAlert(alertKey, 15) {
		body := fmt.Sprintf("• 攻击来源: %s\n• 失败次数: 连续密码错误 %d 次\n• 安全策略: 已触发控制台防爆破拦截，该 IP 已被限制登录\n🕒 触发时间: %s",
			e.formatIP(clientIP), attempts, NowAlertTime())
		e.sendAlert(context.Background(), "🔒 [Polaris] 控制台防爆破触发", body, "Polaris-系统安全", "active", "alarm.caf", "")
	}
}

// NotifyLoginFailed notifies on operator login failure.
func (e *AlertEngine) NotifyLoginFailed(username, ip, reason, userAgent string) {
	settings, err := e.getSettings()
	if err != nil || settings.BarkDeviceKey == "" {
		return
	}
	clean := strings.TrimSpace(ip)
	if h, _, err := net.SplitHostPort(clean); err == nil {
		clean = h
	}

	e.mu.Lock()
	now := time.Now()
	for k, t := range e.loginFailAt {
		if now.Sub(t) > 15*time.Minute {
			delete(e.loginFailAt, k)
			delete(e.loginFailures, k)
		}
	}
	e.loginFailures[clean]++
	e.loginFailAt[clean] = now
	failCount := e.loginFailures[clean]
	e.mu.Unlock()

	if failCount >= 5 {
		e.NotifyConsoleBruteForce(clean, failCount)
		return
	}

	alertKey := "login_fail:" + clean
	if e.canAlert(alertKey, 2) {
		ua := strings.TrimSpace(userAgent)
		if len(ua) > 60 {
			ua = ua[:60] + "..."
		}
		if ua == "" {
			ua = "未知客户端"
		}
		if username == "" {
			username = "未知用户"
		}
		if reason == "" {
			reason = "用户名或密码错误"
		}
		body := fmt.Sprintf("• 尝试账号: %s\n• 登录来源: %s\n• 客户端: %s\n• 失败原因: %s\n• 风险提示: 若非本人操作，请确认登录凭据是否泄露\n🕒 尝试时间: %s",
			username, e.formatIP(clean), ua, reason, NowAlertTime())
		e.sendAlert(context.Background(), "⚠️ [Polaris] 控制台登录失败", body, "Polaris-系统安全", "active", "horn.caf", "")
	}
}

// NotifySubscriptionPullSuccess notifies when a client fetches subscription profile.
func (e *AlertEngine) NotifySubscriptionPullSuccess(configName, ip, userAgent string) {
	settings, err := e.getSettings()
	if err != nil || settings.BarkDeviceKey == "" {
		return
	}
	clean := strings.TrimSpace(ip)
	if h, _, err := net.SplitHostPort(clean); err == nil {
		clean = h
	}
	cooldown := settings.CooldownMinutes
	if cooldown < 5 {
		cooldown = 5
	}
	alertKey := fmt.Sprintf("sub_ok:%s:%s", configName, clean)
	if e.canAlert(alertKey, cooldown) {
		ua := strings.TrimSpace(userAgent)
		if len(ua) > 60 {
			ua = ua[:60] + "..."
		}
		if ua == "" {
			ua = "未知客户端"
		}
		body := fmt.Sprintf("• 订阅配置: %s\n• 请求来源: %s\n• 客户端: %s\n• 获取状态: 正常获取配置 (200 OK)\n🕒 下载时间: %s",
			configName, e.formatIP(clean), ua, NowAlertTime())
		e.sendAlert(context.Background(), "📥 [Polaris] 订阅下载成功", body, "Polaris-订阅分发", "active", "glass.caf", "")
	}
}

// NotifySubscriptionPullFailed notifies on subscription download failure.
func (e *AlertEngine) NotifySubscriptionPullFailed(tokenHint, reason, ip, userAgent string) {
	settings, err := e.getSettings()
	if err != nil || settings.BarkDeviceKey == "" {
		return
	}
	clean := strings.TrimSpace(ip)
	if h, _, err := net.SplitHostPort(clean); err == nil {
		clean = h
	}
	alertKey := fmt.Sprintf("sub_err:%s", clean)
	if e.canAlert(alertKey, 3) {
		ua := strings.TrimSpace(userAgent)
		if len(ua) > 60 {
			ua = ua[:60] + "..."
		}
		if ua == "" {
			ua = "未知客户端"
		}
		if len(tokenHint) > 16 {
			tokenHint = tokenHint[:8] + "..." + tokenHint[len(tokenHint)-4:]
		}
		body := fmt.Sprintf("• 请求凭据: %s\n• 请求来源: %s\n• 客户端: %s\n• 失败原因: %s\n• 安全提示: 订阅请求未通过验证，可能为过期配置或外部扫描\n🕒 尝试时间: %s",
			tokenHint, e.formatIP(clean), ua, reason, NowAlertTime())
		e.sendAlert(context.Background(), "❌ [Polaris] 订阅下载失败", body, "Polaris-订阅分发", "active", "horn.caf", "")
	}
}

// ProbeResultItem holds one node's probe outcome.
type ProbeResultItem struct {
	NodeID                      string `json:"node_id"`
	NodeName                    string `json:"node_name"`
	TargetHost                  string `json:"target_host,omitempty"`
	TargetPort                  uint16 `json:"target_port,omitempty"`
	OverseasOK                  bool   `json:"overseas_ok"`
	DomesticOK                  bool   `json:"domestic_ok"`
	GFWBlocked                  bool   `json:"gfw_blocked"`
	ProbeExecuted               bool   `json:"probe_executed"`
	ProbeSource                 string `json:"probe_source,omitempty"`
	PacketLoss                  int    `json:"packet_loss"`
	LatencyMs                   int    `json:"latency_ms"`
	StatusDesc                  string `json:"status_desc,omitempty"`
	Error                       string `json:"error,omitempty"`
	InboundTelecomRoute         string `json:"inbound_telecom_route,omitempty"`
	InboundTelecomLatencyMs     int    `json:"inbound_telecom_latency_ms,omitempty"`
	InboundTelecomNodeLatencies string `json:"inbound_telecom_node_latencies,omitempty"`
	InboundTelecomTracePath     string `json:"inbound_telecom_trace_path,omitempty"`
	InboundUnicomRoute          string `json:"inbound_unicom_route,omitempty"`
	InboundUnicomLatencyMs      int    `json:"inbound_unicom_latency_ms,omitempty"`
	InboundUnicomNodeLatencies  string `json:"inbound_unicom_node_latencies,omitempty"`
	InboundUnicomTracePath      string `json:"inbound_unicom_trace_path,omitempty"`
	InboundMobileRoute          string `json:"inbound_mobile_route,omitempty"`
	InboundMobileLatencyMs      int    `json:"inbound_mobile_latency_ms,omitempty"`
	InboundMobileNodeLatencies  string `json:"inbound_mobile_node_latencies,omitempty"`
	InboundMobileTracePath      string `json:"inbound_mobile_trace_path,omitempty"`
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
		// Only alert on GFW blocked if genuinely blocked (overseas reachable + domestic timed out + not a probe error)
		isBlocked := item.GFWBlocked || (item.OverseasOK && !item.DomesticOK && item.Error == "")
		if isBlocked {
			src := item.ProbeSource
			if src == "" {
				src = "分布式探针"
			}
			blockedList = append(blockedList, fmt.Sprintf("• [%s] 端口 %d 全超时 (阻断未响应, 来源: %s)", item.NodeName, port, src))
		} else if item.DomesticOK {
			src := item.ProbeSource
			if src == "" {
				src = "分布式探针"
			}
			normalList = append(normalList, fmt.Sprintf("• [%s] 端口 %d 正常 (时延: %d ms, 来源: %s)", item.NodeName, port, item.LatencyMs, src))
		} else if !item.OverseasOK {
			normalList = append(normalList, fmt.Sprintf("• [%s] 离线/未监听", item.NodeName))
		} else {
			normalList = append(normalList, fmt.Sprintf("• [%s] 探针检测异常 (%s)", item.NodeName, item.StatusDesc))
		}
	}

	nowStr := NowAlertTime()

	if len(blockedList) > 0 {
		if settings.GFWAlertEnabled || settings.ProbeNotifyAlways {
			body := strings.Join(blockedList, "\n")
			if len(normalList) > 0 {
				body += "\n" + strings.Join(normalList, "\n")
			}
			body += fmt.Sprintf("\n🕒 探测时间: %s", nowStr)
			e.sendAlert(context.Background(), "🚨 [Polaris] 发现节点被阻断", body, "Polaris-巡检报告", "active", "alarm.caf", "")
			return
		}
	}

	// If no block and user requested always notify on probe
	if settings.ProbeNotifyAlways && len(items) > 0 {
		body := strings.Join(normalList, "\n") + fmt.Sprintf("\n🕒 巡检时间: %s (分布式探针核验)", nowStr)
		e.sendAlert(context.Background(), "✅ [Polaris 巡检] 全部节点连接通畅", body, "Polaris-巡检报告", "active", "telegraph.caf", "")
	}
}

// NotifySpeedtestReport sends a Bark summary after a route and latency probe run.
func (e *AlertEngine) NotifySpeedtestReport(nodeName string, st NodeSpeedtest) {
	settings, err := e.getSettings()
	if err != nil || settings.BarkDeviceKey == "" {
		return
	}

	title := fmt.Sprintf("⚡ [三网延迟与线路] %s 巡检报告", nodeName)
	sound := "telegraph.caf"
	var lines []string
	if st.GFWBlocked {
		title = fmt.Sprintf("🚨 [节点阻断警报] %s 端口疑似被阻断！", nodeName)
		sound = "alarm.caf"
		lines = append(lines, fmt.Sprintf("⚠️ 该节点公网端口探针全部超时 (阻断未响应, 来源: %s)", st.DomesticSource))
	}
	if st.TelecomRoute != "" {
		lines = append(lines, fmt.Sprintf("• 电信线路: %d ms (%s)", st.TelecomLatencyMs, st.TelecomRoute))
	} else if st.TelecomLatencyMs > 0 {
		lines = append(lines, fmt.Sprintf("• 电信线路: %d ms", st.TelecomLatencyMs))
	}
	if st.UnicomRoute != "" {
		lines = append(lines, fmt.Sprintf("• 联通线路: %d ms (%s)", st.UnicomLatencyMs, st.UnicomRoute))
	} else if st.UnicomLatencyMs > 0 {
		lines = append(lines, fmt.Sprintf("• 联通线路: %d ms", st.UnicomLatencyMs))
	}
	if st.MobileRoute != "" {
		lines = append(lines, fmt.Sprintf("• 移动线路: %d ms (%s)", st.MobileLatencyMs, st.MobileRoute))
	} else if st.MobileLatencyMs > 0 {
		lines = append(lines, fmt.Sprintf("• 移动线路: %d ms", st.MobileLatencyMs))
	}
	lines = append(lines, fmt.Sprintf("🕒 检测时间: %s", NowAlertTime()))

	body := strings.Join(lines, "\n")
	e.sendAlert(context.Background(), title, body, "Polaris-巡检报告", "active", sound, "")
}
