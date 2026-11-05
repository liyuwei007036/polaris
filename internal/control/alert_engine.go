package control

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"sort"
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

// NowAlertTime returns the current time in the notification timezone, including
// the zone name and UTC offset.
func NowAlertTime() string {
	return FormatAlertTime(time.Now())
}

// FormatAlertTime formats a time in the notification timezone and includes the
// zone name together with its UTC offset so notifications are unambiguous.
func FormatAlertTime(t time.Time) string {
	local := t.In(NotificationLocation)
	zoneName := NotificationLocation.String()
	_, offset := local.Zone()
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	return fmt.Sprintf("%s (%s, UTC%s%02d:%02d)",
		local.Format("2006-01-02 15:04:05"), zoneName, sign, offset/3600, (offset%3600)/60)
}

type AlertEngine struct {
	store         *Store
	bark          *BarkClient
	telegram      *TelegramClient
	locator       *ipLocator
	mu            sync.Mutex
	cooldowns     map[string]time.Time
	highRateAt    map[string]time.Time
	loginFailures map[string]int
	loginFailAt   map[string]time.Time
	offlineNodes  map[string]time.Time
	fail2banMu    sync.Mutex
	fail2banBatch map[string]*fail2banBatch
	fail2banWait  time.Duration
	cached        AlertSettings
	cacheTime     time.Time
}

type fail2banBatch struct {
	key      string
	nodeID   string
	nodeName string
	jail     string
	ips      []string
	seen     map[string]struct{}
	started  time.Time
	timer    *time.Timer
}

const (
	defaultFail2banWindow = 30 * time.Second
	maxFail2banBatchSize  = 50
)

func NewAlertEngine(store *Store, locator *ipLocator) *AlertEngine {
	if locator == nil {
		locator, _ = newIPLocator()
	}
	return &AlertEngine{
		store:         store,
		bark:          NewBarkClient(),
		telegram:      NewTelegramClient(),
		locator:       locator,
		cooldowns:     make(map[string]time.Time),
		highRateAt:    make(map[string]time.Time),
		loginFailures: make(map[string]int),
		loginFailAt:   make(map[string]time.Time),
		offlineNodes:  make(map[string]time.Time),
		fail2banBatch: make(map[string]*fail2banBatch),
		fail2banWait:  defaultFail2banWindow,
	}
}

func (e *AlertEngine) SetIPLocator(locator *ipLocator) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.locator = locator
}

func (e *AlertEngine) formatIP(ipStr string) string {
	clean := cleanIPAddress(ipStr)
	if clean == "" {
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

func cleanIPAddress(ipStr string) string {
	clean := strings.TrimSpace(ipStr)
	if h, _, err := net.SplitHostPort(clean); err == nil {
		clean = h
	}
	if clean == "-" {
		return ""
	}
	return strings.TrimSpace(clean)
}

// topTrafficEndpoints returns the source IP and destination with the largest
// share of the current sampling interval. It aggregates by source IP and by
// host first, rather than picking one connection, so a source that opens many
// small connections is ranked correctly. When none of the connections has a
// measured rate yet, it falls back to cumulative bytes.
func (e *AlertEngine) topTrafficEndpoints(connections []storedConnection) (sourceIP, destination string) {
	sourceRates := make(map[string]float64)
	destRates := make(map[string]float64)
	sourceBytes := make(map[string]int64)
	destBytes := make(map[string]int64)
	hasRates := false

	for _, connection := range connections {
		source := cleanIPAddress(connection.SourceIP)
		dest := strings.TrimSpace(connection.Host)
		if dest == "" {
			dest = strings.TrimSpace(connection.Destination)
		}
		if source != "" {
			sourceBytes[source] += connection.Upload + connection.Download
		}
		if dest != "" {
			destBytes[dest] += connection.Upload + connection.Download
		}
		if !connection.HasRates {
			continue
		}
		hasRates = true
		if source != "" {
			sourceRates[source] += connection.UploadRate + connection.DownloadRate
		}
		if dest != "" {
			destRates[dest] += connection.UploadRate + connection.DownloadRate
		}
	}

	if hasRates {
		return maxRateKey(sourceRates), maxRateKey(destRates)
	}
	return maxBytesKey(sourceBytes), maxBytesKey(destBytes)
}

func maxRateKey(values map[string]float64) string {
	bestKey := ""
	bestValue := 0.0
	for key, value := range values {
		if value > bestValue || bestKey == "" {
			bestKey, bestValue = key, value
		}
	}
	return bestKey
}

func maxBytesKey(values map[string]int64) string {
	bestKey := ""
	bestValue := int64(0)
	for key, value := range values {
		if value > bestValue || bestKey == "" {
			bestKey, bestValue = key, value
		}
	}
	return bestKey
}

type nodeAlertContext struct {
	Name     string
	IP       string
	Location string
	Provider string
}

func (e *AlertEngine) nodeContext(nodeID, fallbackName string) nodeAlertContext {
	info := nodeAlertContext{Name: fallbackName}
	if e.store == nil || nodeID == "" {
		return info
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	node, err := e.store.NodeByID(ctx, nodeID)
	if err != nil {
		return info
	}
	if strings.TrimSpace(node.Name) != "" {
		info.Name = node.Name
	}
	ip := getNodePublicIPv4(node)
	if ip == "" {
		ip = strings.TrimSpace(node.LastObservedIP)
	}
	if ip == "" {
		ip = cleanIPAddress(node.ClientAddress)
	}
	info.IP = ip
	if ip != "" && e.locator != nil {
		location := strings.TrimSpace(e.locator.Locate(ip))
		if location != "" && location != "未知" {
			info.Location = location
		}
	}
	info.Provider = strings.TrimSpace(node.Provider)
	return info
}

func (c nodeAlertContext) TelegramLines() []string {
	lines := make([]string, 0, 5)
	if c.Name != "" {
		lines = append(lines, tgLine("节点名称", c.Name))
	}
	if c.IP != "" {
		if net.ParseIP(c.IP) != nil {
			lines = append(lines, tgCodeLine("公网 IP", c.IP))
		} else {
			lines = append(lines, tgCodeLine("接入地址", c.IP))
		}
	}
	if c.Location != "" {
		lines = append(lines, tgLine("归属地", c.Location))
	}
	if c.Provider != "" {
		lines = append(lines, tgLine("厂商", c.Provider))
	}
	return lines
}

func (c nodeAlertContext) BarkSummary() string {
	parts := make([]string, 0, 4)
	if c.Name != "" {
		parts = append(parts, "节点："+c.Name)
	}
	if c.IP != "" {
		if net.ParseIP(c.IP) != nil {
			parts = append(parts, "IP："+c.IP)
		} else {
			parts = append(parts, "地址："+c.IP)
		}
	}
	if c.Provider != "" {
		parts = append(parts, "厂商："+c.Provider)
	}
	if c.Location != "" {
		parts = append(parts, "归属地："+c.Location)
	}
	return strings.Join(parts, "｜")
}

func (e *AlertEngine) withNodeInfo(ctx nodeAlertContext, lines []string) []string {
	return append(ctx.TelegramLines(), lines...)
}

func (e *AlertEngine) barkWithNode(ctx nodeAlertContext, body string) string {
	summary := ctx.BarkSummary()
	if summary == "" {
		return body
	}
	if strings.TrimSpace(body) == "" {
		return summary
	}
	return summary + "\n" + body
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

// notificationsEnabled reports whether any notification channel is configured.
// Callers that may see many addresses use this once, instead of asking the
// engine and starting a goroutine for every address.
func (e *AlertEngine) notificationsEnabled() bool {
	settings, err := e.getSettings()
	return err == nil && settings.HasNotifier()
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

func (e *AlertEngine) sendAlert(_ context.Context, payload alertPayload) {
	go func() {
		settings, err := e.getSettings()
		if err != nil || !settings.HasNotifier() {
			return
		}

		// Channel 1: Bark (iOS APNs) — compact lock-screen notification.
		if settings.BarkReady() {
			barkSound := payload.BarkSound
			if barkSound == "" {
				if settings.BarkSound != "" {
					barkSound = settings.BarkSound
				} else {
					barkSound = "minuet.caf"
				}
			}
			if !strings.HasSuffix(barkSound, ".caf") {
				barkSound = barkSound + ".caf"
			}
			baseGroup := strings.TrimSpace(settings.BarkGroup)
			eventGroup := strings.TrimSpace(payload.BarkGroup)
			barkGroup := ""
			switch {
			case baseGroup != "" && eventGroup != "":
				barkGroup = baseGroup + "-" + eventGroup
			case eventGroup != "":
				barkGroup = eventGroup
			case baseGroup != "":
				barkGroup = baseGroup
			default:
				barkGroup = "Polaris"
			}
			barkURL := strings.TrimSpace(payload.BarkURL)
			if barkURL == "" {
				barkURL = strings.TrimSpace(settings.BarkURL)
			}
			barkBody := strings.TrimSpace(payload.BarkBody)
			if !strings.Contains(barkBody, "时间：") {
				if barkBody != "" {
					barkBody += "\n"
				}
				barkBody += "时间：" + FormatAlertTime(time.Now())
			}
			barkTitle := payload.BarkTitle
			if payload.Severity != "" {
				barkTitle = "[" + payload.Severity + "] " + barkTitle
			}
			msg := BarkMessage{
				DeviceKey: settings.BarkDeviceKey,
				Title:     barkTitle,
				Body:      barkBody,
				Group:     barkGroup,
				Sound:     barkSound,
				Level:     "active",
				URL:       barkURL,
			}
			if e.bark != nil {
				pushCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				if err := e.bark.Send(pushCtx, settings.BarkServer, settings.BarkDeviceKey, msg); err != nil {
					log.Printf("failed to send bark notification (%s): %v", barkTitle, err)
				}
				cancel()
			}
		}

		// Channel 2: Telegram Bot API — HTML bot message with structured fields.
		if settings.TelegramReady() {
			text := payload.Telegram
			if strings.TrimSpace(text) == "" {
				text = payload.BarkTitle + "\n\n" + payload.BarkBody
			}
			if e.telegram != nil {
				pushCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				if err := e.telegram.SendHTML(pushCtx, settings.TelegramServer, settings.TelegramBotToken, settings.TelegramChatID, text); err != nil {
					log.Printf("failed to send telegram notification (%s): %v", payload.BarkTitle, err)
				}
				cancel()
			}
		}
	}()
}

// CheckConnectionsTelemetry evaluates live connection pushes from nodes against defined thresholds.
func (e *AlertEngine) CheckConnectionsTelemetry(nodeID, nodeName string, downloadRate, uploadRate float64, connections []storedConnection) {
	settings, err := e.getSettings()
	if err != nil || !settings.HasNotifier() {
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
					mainIP, mainDest := e.topTrafficEndpoints(connections)
					if mainDest == "" {
						mainDest = "未指定域名/IP"
					}
					barkBody := fmt.Sprintf("当前 %.1f Mbps，超过阈值 %.1f Mbps\n持续超过 %d 秒\n目标 %s\n来源 %s",
						maxRateMbps, settings.TrafficThresholdMbps, settings.TrafficDurationSec, mainDest, e.formatIP(mainIP))
					overPercent := 0.0
					if settings.TrafficThresholdMbps > 0 {
						overPercent = (maxRateMbps/settings.TrafficThresholdMbps - 1) * 100
					}
					nodeCtx := e.nodeContext(nodeID, nodeName)
					telegramText := buildTelegramAlert(severityWarning, "流量告警", "瞬时流量突增", nodeName, e.withNodeInfo(nodeCtx, []string{
						tgLine("当前速率", fmt.Sprintf("%.1f Mbps", maxRateMbps)),
						tgLine("告警阈值", fmt.Sprintf("%.1f Mbps", settings.TrafficThresholdMbps)),
						tgLine("超出幅度", fmt.Sprintf("%.1f%%", overPercent)),
						tgLine("持续时长", fmt.Sprintf("超过 %d 秒", settings.TrafficDurationSec)),
						tgLine("主要来源", e.formatIP(mainIP)),
						tgLine("目标主机", mainDest),
					}), "先确认主要来源 IP 和目标主机是否属于正常业务。如果不是，建议在连接列表中阻断该来源，并检查该节点是否有异常下载、备份任务或流量盗用。")
					e.sendAlert(context.Background(), alertPayload{
						Severity:  severityWarning,
						BarkTitle: fmt.Sprintf("流量突增：%s", nodeName),
						BarkBody:  e.barkWithNode(nodeCtx, barkBody),
						BarkGroup: "流量监控",
						BarkSound: "bell.caf",
						Telegram:  telegramText,
					})
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
			barkBody := fmt.Sprintf("当前 %d 条，超过上限 %d 条\n建议检查并发来源与代理客户端",
				connCount, settings.ConnThresholdCount)
			overCount := connCount - settings.ConnThresholdCount
			if overCount < 0 {
				overCount = 0
			}
			nodeCtx := e.nodeContext(nodeID, nodeName)
			telegramText := buildTelegramAlert(severityWarning, "连接告警", "活跃连接数激增", nodeName, e.withNodeInfo(nodeCtx, []string{
				tgLine("当前连接", fmt.Sprintf("%d 条", connCount)),
				tgLine("设定上限", fmt.Sprintf("%d 条", settings.ConnThresholdCount)),
				tgLine("超出数量", fmt.Sprintf("%d 条", overCount)),
			}), "先查看连接是否集中在单一 IP。若集中在单一 IP，请结合「单 IP 高并发」告警处理；若分散在大量客户端，请检查是否有异常客户端、容器镜像拉取或外部探测。")
			e.sendAlert(context.Background(), alertPayload{
				Severity:  severityWarning,
				BarkTitle: fmt.Sprintf("连接数激增：%s", nodeName),
				BarkBody:  e.barkWithNode(nodeCtx, barkBody),
				BarkGroup: "连接监控",
				BarkSound: "anticipate.caf",
				Telegram:  telegramText,
			})
		}
	}

	// 3. Single IP Connection Surge Check
	if settings.SingleIPAlertEnabled && settings.SingleIPThresholdCount > 0 {
		ipCounts := make(map[string]int)
		for _, c := range connections {
			if ip := cleanIPAddress(c.SourceIP); ip != "" {
				ipCounts[ip]++
			}
		}
		for ip, count := range ipCounts {
			if count >= settings.SingleIPThresholdCount {
				alertKey := "single_ip:" + nodeID + ":" + ip
				if e.canAlert(alertKey, settings.CooldownMinutes) {
					barkBody := fmt.Sprintf("%s\n%d 条连接，超过单 IP 上限 %d 条\n请确认是否为本人设备",
						e.formatIP(ip), count, settings.SingleIPThresholdCount)
					overCount := count - settings.SingleIPThresholdCount
					if overCount < 0 {
						overCount = 0
					}
					nodeCtx := e.nodeContext(nodeID, nodeName)
					telegramText := buildTelegramAlert(severityCritical, "安全告警", "单 IP 异常高并发", nodeName, e.withNodeInfo(nodeCtx, []string{
						tgLine("来源 IP", e.formatIP(ip)),
						tgLine("活跃连接", fmt.Sprintf("%d 条", count)),
						tgLine("单 IP 上限", fmt.Sprintf("%d 条", settings.SingleIPThresholdCount)),
						tgLine("超出数量", fmt.Sprintf("%d 条", overCount)),
					}), "确认该 IP 是否为客户本人。如果不是，建议在安全防护中阻断该 IP，并检查该客户的订阅链接或账号是否泄露。")
					e.sendAlert(context.Background(), alertPayload{
						Severity:  severityCritical,
						BarkTitle: fmt.Sprintf("单 IP 高并发：%s", nodeName),
						BarkBody:  e.barkWithNode(nodeCtx, barkBody),
						BarkGroup: "安全警报",
						BarkSound: "horn.caf",
						Telegram:  telegramText,
					})
				}
			}
		}
	}

	e.checkProxyRouting(nodeID, nodeName, connections, settings)

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
		targetPorts    map[string]map[int]struct{}
		sensitiveHosts map[string]map[string]struct{}
		rawIPTargets   map[string]struct{}
	}

	clientStats := make(map[string]*clientScanStats)

	for _, c := range connections {
		clientIP := cleanIPAddress(c.SourceIP)
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

		// Check if host is a domain name (FQDN). net.ParseIP excludes literal
		// addresses, including IPv6, so internationalized domains are handled
		// without the old ASCII-letter heuristic.
		host := strings.TrimSpace(c.Host)
		isDomain := host != "" && net.ParseIP(host) == nil && strings.Contains(host, ".")

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
				targetPorts:    make(map[string]map[int]struct{}),
				sensitiveHosts: make(map[string]map[string]struct{}),
				rawIPTargets:   make(map[string]struct{}),
			}
			clientStats[clientIP] = st
		}

		if destHost != "" && destPort > 0 {
			if _, ok := st.targetPorts[destHost]; !ok {
				st.targetPorts[destHost] = make(map[int]struct{})
			}
			st.targetPorts[destHost][destPort] = struct{}{}
		}

		if svc, isSensitive := sensitiveScanPorts[destPort]; isSensitive && destHost != "" {
			if st.sensitiveHosts[destHost] == nil {
				st.sensitiveHosts[destHost] = make(map[string]struct{})
			}
			st.sensitiveHosts[destHost][svc] = struct{}{}
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
				portList := make([]int, 0, len(ports))
				for port := range ports {
					portList = append(portList, port)
				}
				sort.Ints(portList)
				sampleCount := min(6, len(portList))
				portStrs := make([]string, 0, sampleCount)
				for _, port := range portList[:sampleCount] {
					portStrs = append(portStrs, strconv.Itoa(port))
				}
				triggerReason = fmt.Sprintf("单目标多端口嗅探 (尝试连接目标 %s 的 %d 个不同端口)", targetIP, len(ports))
				triggerDetails = fmt.Sprintf("目标地址: %s, 端口样例: %s", targetIP, strings.Join(portStrs, ", "))
				break
			}
		}

		// Condition B: Sensitive Port Sweep (probing high-risk ports across >= 5 distinct targets)
		if triggerReason == "" && len(st.sensitiveHosts) >= 5 {
			hosts := make([]string, 0, len(st.sensitiveHosts))
			for host := range st.sensitiveHosts {
				hosts = append(hosts, host)
			}
			sort.Strings(hosts)
			sampleCount := min(5, len(hosts))
			samples := make([]string, 0, sampleCount)
			for _, target := range hosts[:sampleCount] {
				names := make([]string, 0, len(st.sensitiveHosts[target]))
				for service := range st.sensitiveHosts[target] {
					names = append(names, service)
				}
				sort.Strings(names)
				samples = append(samples, fmt.Sprintf("%s (%s)", target, strings.Join(names, "/")))
			}
			triggerReason = fmt.Sprintf("高危服务端口扫描 (探测 %d 个不同目标的高危端口)", len(st.sensitiveHosts))
			triggerDetails = fmt.Sprintf("样例: %s", strings.Join(samples, ", "))
		}

		// Condition C: Mass Raw IP Sweep (>= 40 distinct raw IPs without domain names on non-web ports)
		if triggerReason == "" && len(st.rawIPTargets) >= 40 {
			rawIPs := make([]string, 0, len(st.rawIPTargets))
			for ip := range st.rawIPTargets {
				rawIPs = append(rawIPs, ip)
			}
			sort.Strings(rawIPs)
			sampleCount := min(5, len(rawIPs))
			triggerReason = fmt.Sprintf("纯 IP 地址段并发漫游扫描 (探测 %d 个不同纯 IP 目标)", len(st.rawIPTargets))
			triggerDetails = fmt.Sprintf("目标样例: %s", strings.Join(rawIPs[:sampleCount], ", "))
		}

		if triggerReason != "" {
			alertKey := "scan:" + nodeID + ":" + clientIP
			cooldown := settings.CooldownMinutes
			if cooldown < 15 {
				cooldown = 15
			}
			if e.canAlert(alertKey, cooldown) {
				barkBody := fmt.Sprintf("%s\n%s\n%s\n建议排查来源或添加阻断规则",
					e.formatIP(clientIP), triggerReason, triggerDetails)
				nodeCtx := e.nodeContext(nodeID, nodeName)
				telegramText := buildTelegramAlert(severityCritical, "安全告警", "检测到异常网络扫描", nodeName, e.withNodeInfo(nodeCtx, []string{
					tgLine("来源 IP", e.formatIP(clientIP)),
					tgLine("扫描特征", triggerReason),
					tgLine("探测详情", triggerDetails),
				}), "确认来源 IP 是否为客户。如果不是，建议阻断；同时检查节点 SSH、RDP、Redis 等高危端口是否需要对公网开放，以及是否存在弱口令。")
				e.sendAlert(context.Background(), alertPayload{
					Severity:  severityCritical,
					BarkTitle: fmt.Sprintf("异常扫描：%s", nodeName),
					BarkBody:  e.barkWithNode(nodeCtx, barkBody),
					BarkGroup: "安全警报",
					BarkSound: "alarm.caf",
					Telegram:  telegramText,
				})
			}
		}
	}
}

type routeIssue struct {
	destIP   string
	location string
	outbound string
	host     string
	count    int
}

// checkProxyRouting reports destinations that belong to China or a private
// network but are currently leaving through a proxy outbound. That is almost
// always a split-routing mistake: domestic and LAN traffic should go direct.
func (e *AlertEngine) checkProxyRouting(nodeID, nodeName string, connections []storedConnection, settings AlertSettings) {
	if len(connections) == 0 {
		return
	}

	issues := map[string]*routeIssue{}
	for _, connection := range connections {
		destination := strings.TrimSpace(connection.Destination)
		host := strings.TrimSpace(connection.Host)
		ipText := destination
		if parsedHost, _, err := net.SplitHostPort(destination); err == nil {
			ipText = parsedHost
		}
		ipText = strings.TrimSpace(ipText)
		ip := net.ParseIP(ipText)
		if ip == nil {
			// A few agents only fill Host, so accept an IP literal there too.
			if parsed := net.ParseIP(host); parsed != nil {
				ip = parsed
				ipText = host
			}
		}
		if ip == nil {
			continue
		}

		if ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		isLocal := ip.IsPrivate() || ip.IsLinkLocalUnicast()
		isDomestic := false
		if !isLocal && e.locator != nil {
			isDomestic = e.locator.IsDomestic(ipText)
		}
		if !isLocal && !isDomestic {
			continue
		}

		outbound := strings.TrimSpace(connection.OutboundName)
		if outbound == "" {
			outbound = strings.TrimSpace(connection.Outbound)
		}
		if outbound == "" || isDirectOutbound(outbound) {
			continue
		}

		key := ipText + "|" + outbound
		issue := issues[key]
		if issue == nil {
			location := "局域网/内网"
			if !isLocal && e.locator != nil {
				if resolved := strings.TrimSpace(e.locator.Locate(ipText)); resolved != "" && resolved != "未知" {
					location = resolved
				}
			}
			issue = &routeIssue{destIP: ipText, location: location, outbound: outbound, host: host}
			issues[key] = issue
		}
		issue.count++
		if issue.host == "" && host != "" {
			issue.host = host
		}
	}
	if len(issues) == 0 {
		return
	}

	list := make([]*routeIssue, 0, len(issues))
	for _, issue := range issues {
		list = append(list, issue)
	}
	sort.Slice(list, func(left, right int) bool {
		if list[left].count != list[right].count {
			return list[left].count > list[right].count
		}
		return list[left].destIP < list[right].destIP
	})

	cooldown := settings.CooldownMinutes
	if cooldown < 30 {
		cooldown = 30
	}
	if !e.canAlert("proxy_route:"+nodeID, cooldown) {
		return
	}

	nodeCtx := e.nodeContext(nodeID, nodeName)
	lines := []string{
		tgLine("检测结果", fmt.Sprintf("%d 个国内或局域网目标正在走代理出口", len(list))),
	}
	barkTargets := make([]string, 0, 3)
	for index, issue := range list {
		label := issue.destIP
		if issue.host != "" {
			label += "（" + issue.host + "）"
		}
		if index < 5 {
			lines = append(lines, tgLine(label, fmt.Sprintf("%s｜出口 %s｜%d 条连接",
				issue.location, issue.outbound, issue.count)))
		}
		if index < 3 {
			barkTargets = append(barkTargets, fmt.Sprintf("%s → %s", label, issue.outbound))
		}
	}
	if len(list) > 5 {
		lines = append(lines, tgLine("其他目标", fmt.Sprintf("另有 %d 个", len(list)-5)))
	}
	if len(list) > 3 {
		barkTargets = append(barkTargets, fmt.Sprintf("另有 %d 个目标", len(list)-3))
	}

	telegramText := buildTelegramAlert(severityWarning, "分流策略", "国内/局域网目标正在走代理", nodeName,
		e.withNodeInfo(nodeCtx, lines),
		"请在分流规则中把上述目标改为直连；如果是域名，请检查 DNS 解析结果和规则优先级。")

	e.sendAlert(context.Background(), alertPayload{
		Severity:  severityWarning,
		BarkTitle: fmt.Sprintf("分流异常：%s", nodeName),
		BarkBody:  e.barkWithNode(nodeCtx, "以下目标应直连但走了代理：\n"+strings.Join(barkTargets, "\n")),
		BarkGroup: "分流策略",
		BarkSound: "horn.caf",
		Telegram:  telegramText,
	})
}

func isDirectOutbound(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "direct", "direct-out", "direct_out", "direct outbound", "direct-connection", "直连":
		return true
	default:
		return false
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
		e.mu.Lock()
		e.offlineNodes[nodeID] = time.Now()
		e.mu.Unlock()
		telegramText := buildTelegramAlert(severityCritical, "节点状态", "节点已离线", nodeName, []string{
			tgLine("当前状态", "控制端已失去该节点心跳"),
			tgLine("影响", "该节点上的代理、监控和任务下发暂时不可用"),
			tgLine("可能原因", "网络中断、机房维护、主机宕机或 Agent 进程异常"),
		}, "先检查节点网络、电源和 Agent 服务。若短时间内自动恢复，可忽略；若持续离线，请登录服务商后台检查主机状态。")
		e.sendAlert(context.Background(), alertPayload{
			Severity:  severityCritical,
			BarkTitle: fmt.Sprintf("节点离线：%s", nodeName),
			BarkBody:  "控制端已失去该节点心跳。\n代理与监控服务可能暂停，请检查网络、电源或机房状态。",
			BarkGroup: "节点状态",
			BarkSound: "alarm.caf",
			Telegram:  telegramText,
		})
	}
}

// NotifyNodeOnline sends a recovery notice only when this node previously had
// an offline notification delivered. A reconnect without a matching offline
// alert (master restart, quick session replacement) must not look like a
// recovery out of nowhere.
func (e *AlertEngine) NotifyNodeOnline(nodeID, nodeName string) {
	e.mu.Lock()
	_, wasOffline := e.offlineNodes[nodeID]
	delete(e.offlineNodes, nodeID)
	e.mu.Unlock()
	if !wasOffline {
		return
	}

	settings, err := e.getSettings()
	if err != nil || !settings.OfflineAlertEnabled {
		return
	}
	alertKey := "online:" + nodeID
	if e.canAlert(alertKey, settings.CooldownMinutes) {
		telegramText := buildTelegramAlert(severityRecovery, "节点状态", "节点已恢复", nodeName, []string{
			tgLine("当前状态", "控制端已重新收到该节点心跳"),
			tgLine("控制通道", "加密控制通道已重建"),
			tgLine("服务状态", "代理、监控与任务下发能力已恢复"),
		}, "")
		e.sendAlert(context.Background(), alertPayload{
			Severity:  severityRecovery,
			BarkTitle: fmt.Sprintf("节点恢复：%s", nodeName),
			BarkBody:  "控制通道已重建，代理与监控服务恢复就绪。",
			BarkGroup: "节点状态",
			BarkSound: "calypso.caf",
			Telegram:  telegramText,
		})
	}
}

func (e *AlertEngine) NotifyServerExpiry(node Node, days int) {
	var barkLines []string
	barkLines = append(barkLines, fmt.Sprintf("到期 %s，剩余 %d 天", node.ExpiresOn, days))
	if node.ProviderURL != "" {
		barkLines = append(barkLines, "续费入口："+node.ProviderURL)
	}

	tgLines := make([]string, 0, 4)
	tgLines = append(tgLines,
		tgLine("到期日期", node.ExpiresOn),
		tgLine("剩余时间", fmt.Sprintf("%d 天", days)),
	)
	if node.ProviderURL != "" {
		tgLines = append(tgLines, tgLinkLine("管理入口", node.ProviderURL, "打开服务商管理后台"))
	}
	nodeCtx := e.nodeContext(node.ID, node.Name)
	telegramText := buildTelegramAlert(severityWarning, "服务器到期", "服务器即将到期", node.Name, e.withNodeInfo(nodeCtx, tgLines), "请在到期前完成续费或迁移。若已经续费，可忽略这条提醒。")

	e.sendAlert(context.Background(), alertPayload{
		Severity:  severityWarning,
		BarkTitle: fmt.Sprintf("服务器即将到期：%s", node.Name),
		BarkBody:  e.barkWithNode(nodeCtx, strings.Join(barkLines, "\n")),
		BarkGroup: "服务器到期",
		BarkURL:   node.ProviderURL,
		Telegram:  telegramText,
	})
}

// NotifyDDNSUpdated sends an alert when a NAT node's public IP changes and DDNS is updated.
func (e *AlertEngine) NotifyDDNSUpdated(nodeID, nodeName, oldIP, currentIP, domain string) {
	alertKey := "ddns:" + nodeID + ":" + currentIP
	if e.canAlert(alertKey, 1) {
		nodeCtx := e.nodeContext(nodeID, nodeName)
		telegramText := buildTelegramAlert(severityInfo, "DDNS", "NAT 节点 IP 变更", nodeName, e.withNodeInfo(nodeCtx, []string{
			tgLine("原公网 IP", e.formatIP(oldIP)),
			tgLine("新公网 IP", e.formatIP(currentIP)),
			tgLine("解析域名", domain),
			tgLine("同步状态", "Cloudflare DNS 已更新，TTL 为 60 秒"),
			tgLine("影响", "依赖该域名的前置节点会刷新 DNS 并重建连接；客户端无需修改配置"),
		}), "如业务异常，请检查 Cloudflare 记录、DNS 缓存以及依赖该域名的前置节点。")
		e.sendAlert(context.Background(), alertPayload{
			Severity:  severityInfo,
			BarkTitle: fmt.Sprintf("节点 IP 变更：%s", nodeName),
			BarkBody: e.barkWithNode(nodeCtx, fmt.Sprintf("%s → %s\n%s 已同步 Cloudflare（TTL 60s）",
				e.formatIP(oldIP), e.formatIP(currentIP), domain)),
			BarkGroup: "DDNS监控",
			BarkSound: "bell.caf",
			Telegram:  telegramText,
		})
	}
}

// NotifyChainedDNSHealing sends an alert when chained nodes are signaled to refresh DNS and heal connections.
func (e *AlertEngine) NotifyChainedDNSHealing(targetNodeName string, chainedCount int, domains []string, targetIP string) {
	alertKey := "dns_healing:" + targetNodeName + ":" + targetIP
	if e.canAlert(alertKey, 1) {
		nodeCtx := nodeAlertContext{Name: targetNodeName, IP: cleanIPAddress(targetIP)}
		if nodeCtx.IP != "" && e.locator != nil {
			if location := strings.TrimSpace(e.locator.Locate(nodeCtx.IP)); location != "" && location != "未知" {
				nodeCtx.Location = location
			}
		}
		telegramText := buildTelegramAlert(severityWarning, "链路自愈", "已触发前置节点 DNS 刷新", targetNodeName, e.withNodeInfo(nodeCtx, []string{
			tgLine("依赖前置节点", fmt.Sprintf("%d 台", chainedCount)),
			tgLine("影响域名", strings.Join(domains, "、")),
			tgLine("目标最新 IP", e.formatIP(targetIP)),
			tgLine("当前动作", "已下发轻量自愈指令，重置旧连接并刷新解析"),
		}), "系统已自动处理，请等待完成通知。若 5 分钟内仍未恢复，请检查前置节点在线状态和 DNS 解析。")
		e.sendAlert(context.Background(), alertPayload{
			Severity:  severityWarning,
			BarkTitle: "链路自愈已触发",
			BarkBody: e.barkWithNode(nodeCtx, fmt.Sprintf("%d 台前置节点正在刷新 DNS\n域名：%s",
				chainedCount, strings.Join(domains, "、"))),
			BarkGroup: "链路自愈",
			BarkSound: "anticipate.caf",
			Telegram:  telegramText,
		})
	}
}

// NotifyChainedDNSCompleted sends a notification when a chained node completes DNS refresh and verification.
func (e *AlertEngine) NotifyChainedDNSCompleted(nodeID, nodeName, summary string) {
	alertKey := "dns_completed:" + nodeID
	if e.canAlert(alertKey, 1) {
		nodeCtx := e.nodeContext(nodeID, nodeName)
		telegramText := buildTelegramAlert(severityRecovery, "链路自愈", "DNS 刷新与验证完成", nodeName, e.withNodeInfo(nodeCtx, []string{
			tgLine("执行结果", summary),
			tgLine("当前状态", "底层卡死连接已切断，DNS 状态已重载恢复"),
			tgLine("客户端影响", "依赖该前置节点的链路会使用最新解析，客户端无需修改配置"),
		}), "")
		e.sendAlert(context.Background(), alertPayload{
			Severity:  severityRecovery,
			BarkTitle: fmt.Sprintf("链路自愈完成：%s", nodeName),
			BarkBody:  e.barkWithNode(nodeCtx, fmt.Sprintf("%s\nDNS 刷新与验证完成。", summary)),
			BarkGroup: "链路自愈",
			BarkSound: "calypso.caf",
			Telegram:  telegramText,
		})
	}
}

// NotifyFail2BanBlock records a blocked attacker. Blocks are merged per node
// and jail for a short window so a burst of SSH brute-force attempts produces
// one summary notification instead of one push per source IP.
func (e *AlertEngine) NotifyFail2BanBlock(nodeID, nodeName, ip, jail string) {
	settings, err := e.getSettings()
	if err != nil || !settings.HasNotifier() {
		return
	}
	cooldown := settings.CooldownMinutes
	if cooldown < 5 {
		cooldown = 5
	}
	if !e.canAlert("f2b:"+nodeID+":"+ip, cooldown) {
		return
	}

	key := nodeID + "|" + jail
	e.fail2banMu.Lock()
	batch := e.fail2banBatch[key]
	if batch == nil {
		batch = &fail2banBatch{
			key:      key,
			nodeID:   nodeID,
			nodeName: nodeName,
			jail:     jail,
			seen:     make(map[string]struct{}),
			started:  time.Now(),
		}
		e.fail2banBatch[key] = batch
		wait := e.fail2banWait
		if wait <= 0 {
			wait = defaultFail2banWindow
		}
		batch.timer = time.AfterFunc(wait, func() { e.flushFail2banBatch(key) })
	}
	if _, exists := batch.seen[ip]; !exists {
		batch.seen[ip] = struct{}{}
		batch.ips = append(batch.ips, ip)
	}
	shouldFlush := len(batch.ips) >= maxFail2banBatchSize
	e.fail2banMu.Unlock()

	if shouldFlush {
		e.flushFail2banBatch(key)
	}
}

func (e *AlertEngine) flushFail2banBatch(key string) {
	e.fail2banMu.Lock()
	batch := e.fail2banBatch[key]
	if batch == nil {
		e.fail2banMu.Unlock()
		return
	}
	delete(e.fail2banBatch, key)
	if batch.timer != nil {
		batch.timer.Stop()
	}
	e.fail2banMu.Unlock()

	if len(batch.ips) == 0 {
		return
	}
	e.sendFail2banSummary(batch)
}

func (e *AlertEngine) sendFail2banSummary(batch *fail2banBatch) {
	settings, err := e.getSettings()
	if err != nil || !settings.HasNotifier() {
		return
	}
	cooldown := settings.CooldownMinutes
	if cooldown < 5 {
		cooldown = 5
	}
	if !e.canAlert("f2b_batch:"+batch.nodeID+":"+batch.jail, cooldown) {
		return
	}

	ips := append([]string(nil), batch.ips...)
	sort.Strings(ips)
	total := len(ips)
	shown := ips
	if len(shown) > 10 {
		shown = shown[:10]
	}
	formatted := make([]string, 0, len(shown))
	for _, ip := range shown {
		formatted = append(formatted, e.formatIP(ip))
	}
	more := total - len(shown)
	ipList := strings.Join(formatted, "、")
	if more > 0 {
		ipList += fmt.Sprintf(" 等，共 %d 个", total)
	}

	start := FormatAlertTime(batch.started)
	end := FormatAlertTime(time.Now())
	windowSeconds := int(time.Since(batch.started).Seconds())
	if windowSeconds < 1 {
		windowSeconds = 1
	}

	nodeCtx := e.nodeContext(batch.nodeID, batch.nodeName)
	if total == 1 {
		telegramText := buildTelegramAlert(severityWarning, "安全防护", "已拦截恶意来源", batch.nodeName, e.withNodeInfo(nodeCtx, []string{
			tgLine("拦截目标", e.formatIP(batch.ips[0])),
			tgLine("触发规则", batch.jail),
			tgLine("处置结果", "已自动加入节点防火墙阻断列表"),
		}), "若该 IP 是业务来源，请在节点 Fail2Ban 白名单中添加；否则可以忽略。")
		e.sendAlert(context.Background(), alertPayload{
			Severity:  severityWarning,
			BarkTitle: fmt.Sprintf("已拦截恶意源：%s", batch.nodeName),
			BarkBody:  e.barkWithNode(nodeCtx, fmt.Sprintf("%s\n规则：%s\n已加入防火墙阻断列表", e.formatIP(batch.ips[0]), batch.jail)),
			BarkGroup: "防御日志",
			BarkSound: "chime.caf",
			Telegram:  telegramText,
		})
		return
	}

	telegramText := buildTelegramAlert(severityWarning, "安全防护", "批量拦截恶意来源", batch.nodeName, e.withNodeInfo(nodeCtx, []string{
		tgLine("拦截数量", fmt.Sprintf("%d 个", total)),
		tgLine("触发规则", batch.jail),
		tgLine("统计窗口", fmt.Sprintf("%s 至 %s，约 %d 秒", start, end, windowSeconds)),
		tgLine("来源列表", ipList),
		tgLine("处置结果", "已自动加入节点防火墙阻断列表"),
	}), "若这些 IP 是业务来源，请在节点 Fail2Ban 白名单中添加；否则可以忽略。")
	e.sendAlert(context.Background(), alertPayload{
		Severity:  severityWarning,
		BarkTitle: fmt.Sprintf("已拦截 %d 个恶意来源：%s", total, batch.nodeName),
		BarkBody: e.barkWithNode(nodeCtx, fmt.Sprintf("规则：%s\n统计窗口：约 %d 秒\n来源：%s",
			batch.jail, windowSeconds, ipList)),
		BarkGroup: "防御日志",
		BarkSound: "chime.caf",
		Telegram:  telegramText,
	})
}

// NotifyConsoleBruteForce alerts on repeated failed login attempts.
func (e *AlertEngine) NotifyConsoleBruteForce(clientIP string, attempts int) {
	alertKey := "bruteforce:" + clientIP
	if e.canAlert(alertKey, 15) {
		telegramText := buildTelegramAlert(severityCritical, "系统安全", "控制台防爆破已触发", clientIP, []string{
			tgLine("攻击来源", e.formatIP(clientIP)),
			tgLine("失败次数", fmt.Sprintf("15 分钟内连续失败 %d 次", attempts)),
			tgLine("触发阈值", "同一 IP 15 分钟内失败 5 次"),
			tgLine("当前处置", "该 IP 已被限制登录控制台"),
		}, "如非本人操作，请立即修改管理密码并检查登录审计；如为本人，请确认密码未泄露。")
		e.sendAlert(context.Background(), alertPayload{
			Severity:  severityCritical,
			BarkTitle: fmt.Sprintf("控制台防爆破：%s", clientIP),
			BarkBody:  fmt.Sprintf("%s\n15 分钟内连续失败 %d 次\n该 IP 已被限制登录", e.formatIP(clientIP), attempts),
			BarkGroup: "系统安全",
			BarkSound: "alarm.caf",
			Telegram:  telegramText,
		})
	}
}

// NotifyLoginFailed notifies on operator login failure.
func (e *AlertEngine) NotifyLoginFailed(username, ip, reason, userAgent string) {
	settings, err := e.getSettings()
	if err != nil || !settings.HasNotifier() {
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
		ua := truncateText(userAgent, 60)
		if ua == "" {
			ua = "未知客户端"
		}
		if username == "" {
			username = "未知用户"
		}
		if reason == "" {
			reason = "用户名或密码错误"
		}
		telegramText := buildTelegramAlert(severityWarning, "系统安全", "控制台登录失败", username, []string{
			tgLine("登录来源", e.formatIP(clean)),
			tgLine("失败原因", reason),
			tgLine("客户端", ua),
		}, "如非本人操作，请检查密码是否泄露；同一 IP 15 分钟内失败 5 次会触发防爆破限制。")
		e.sendAlert(context.Background(), alertPayload{
			Severity:  severityWarning,
			BarkTitle: fmt.Sprintf("控制台登录失败：%s", username),
			BarkBody:  fmt.Sprintf("%s\n%s\n客户端：%s", e.formatIP(clean), reason, ua),
			BarkGroup: "系统安全",
			BarkSound: "horn.caf",
			Telegram:  telegramText,
		})
	}
}

// NotifySubscriptionPullSuccess notifies when a client fetches subscription profile.
func (e *AlertEngine) NotifySubscriptionPullSuccess(configName, ip, userAgent string) {
	settings, err := e.getSettings()
	if err != nil || !settings.HasNotifier() {
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
		ua := truncateText(userAgent, 60)
		if ua == "" {
			ua = "未知客户端"
		}
		telegramText := buildTelegramAlert(severityInfo, "订阅分发", "订阅下载成功", configName, []string{
			tgLine("请求来源", e.formatIP(clean)),
			tgLine("客户端", ua),
			tgLine("获取状态", "200 OK"),
		}, "")
		e.sendAlert(context.Background(), alertPayload{
			Severity:  severityInfo,
			BarkTitle: fmt.Sprintf("订阅下载成功：%s", configName),
			BarkBody:  fmt.Sprintf("%s\n200 OK，%s", e.formatIP(clean), ua),
			BarkGroup: "订阅分发",
			BarkSound: "glass.caf",
			Telegram:  telegramText,
		})
	}
}

// NotifySubscriptionPullFailed notifies on subscription download failure.
func (e *AlertEngine) NotifySubscriptionPullFailed(tokenHint, reason, ip, userAgent string) {
	settings, err := e.getSettings()
	if err != nil || !settings.HasNotifier() {
		return
	}
	clean := strings.TrimSpace(ip)
	if h, _, err := net.SplitHostPort(clean); err == nil {
		clean = h
	}
	alertKey := fmt.Sprintf("sub_err:%s", clean)
	if e.canAlert(alertKey, 3) {
		ua := truncateText(userAgent, 60)
		if ua == "" {
			ua = "未知客户端"
		}
		if len(tokenHint) > 16 {
			tokenHint = tokenHint[:8] + "..." + tokenHint[len(tokenHint)-4:]
		}
		telegramText := buildTelegramAlert(severityWarning, "订阅分发", "订阅下载失败", tokenHint, []string{
			tgLine("请求来源", e.formatIP(clean)),
			tgLine("客户端", ua),
			tgLine("失败原因", reason),
			tgLine("安全提示", "订阅请求未通过验证，可能为过期配置、错误客户端或外部扫描"),
		}, "如果是你的客户端，请检查订阅链接是否过期、客户端 User-Agent 是否匹配访问限制；如果不是，请警惕外部扫描。")
		e.sendAlert(context.Background(), alertPayload{
			Severity:  severityWarning,
			BarkTitle: fmt.Sprintf("订阅下载失败：%s", tokenHint),
			BarkBody:  fmt.Sprintf("%s\n%s", e.formatIP(clean), reason),
			BarkGroup: "订阅分发",
			BarkSound: "horn.caf",
			Telegram:  telegramText,
		})
	}
}

// NotifySubscriptionConfigChanged tells the operator that a resource change
// will be visible in one or more client subscriptions. It is only sent when at
// least one client configuration is affected.
func (e *AlertEngine) NotifySubscriptionConfigChanged(resource, action, name, detail string, affected []string) {
	settings, err := e.getSettings()
	if err != nil || !settings.HasNotifier() {
		return
	}
	affected = deduplicateStrings(affected)
	if len(affected) == 0 {
		return
	}
	sort.Strings(affected)
	if !e.canAlert("subscription_change:"+resource+":"+name+":"+action, 1) {
		return
	}

	shown := affected
	extra := 0
	if len(shown) > 5 {
		extra = len(shown) - 5
		shown = shown[:5]
	}
	list := strings.Join(shown, "、")
	if extra > 0 {
		list += fmt.Sprintf(" 等，共 %d 个", len(affected))
	}

	barkLines := []string{
		fmt.Sprintf("变更：%s%s", resource, action),
		fmt.Sprintf("影响客户端：%d 个", len(affected)),
		list,
		"客户端下次拉取订阅时会自动更新；未开启自动更新时请手动更新。",
	}
	if strings.TrimSpace(detail) != "" {
		barkLines = append(barkLines, detail)
	}

	tgShown := affected
	if len(tgShown) > 15 {
		tgShown = tgShown[:15]
	}
	tgList := strings.Join(tgShown, "、")
	if len(tgShown) < len(affected) {
		tgList += fmt.Sprintf(" 等，共 %d 个", len(affected))
	}
	tgLines := []string{
		tgLine("名称", name),
		tgLine("变更类型", resource+action),
		tgLine("影响客户端", fmt.Sprintf("%d 个", len(affected))),
		tgLine("影响列表", tgList),
	}
	if strings.TrimSpace(detail) != "" {
		tgLines = append(tgLines, tgLine("变更内容", detail))
	}
	tgLines = append(tgLines, tgLine("客户端影响", "客户端下次拉取订阅时会获得新配置；未开启自动更新的客户端需要手动更新订阅"))
	telegramText := buildTelegramAlertFor("订阅通知", severityInfo, "订阅配置", resource+action, name, tgLines,
		"订阅用户：请在客户端更新订阅。管理员：如发现非预期变更，请检查操作审计并回滚。")

	e.sendAlert(context.Background(), alertPayload{
		Severity:  severityInfo,
		BarkTitle: fmt.Sprintf("订阅配置已更新：%s", name),
		BarkBody:  strings.Join(barkLines, "\n"),
		BarkGroup: "订阅配置",
		BarkSound: "glass.caf",
		Telegram:  telegramText,
	})
}
