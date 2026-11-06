package control

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The daily report runs at 09:00 in the notification timezone. It summarises
// yesterday's probe health and proxy usage. Keeping the query and rendering
// together makes it easier to change what the report means without touching
// the scheduler.
const (
	dailyReportHour     = 9
	dailyRankingLimit   = 5
	dailyTargetLimit    = 10
	dailyProbeNodeLimit = 10
)

type dailyProbeItem struct {
	ProbeName string
	Samples   int
	Failed    int
	Loss      float64
	AvgMs     float64
	MinMs     float64
	MaxMs     float64
}

type dailyNodeProbeStat struct {
	NodeID  string
	Name    string
	Samples int
	Failed  int
	Loss    float64
	AvgMs   float64
	MinMs   float64
	MaxMs   float64
	Probes  []dailyProbeItem
}

type dailyDeviceStat struct {
	SourceIP   string
	Location   string
	Count      int
	TotalBytes int64
}

type dailyTargetStat struct {
	Kind       string
	Label      string
	Location   string
	Count      int
	TotalBytes int64
}

type dailyNodeStat struct {
	NodeID      string
	Name        string
	Connections int
	TotalBytes  int64
}

type dailyReportData struct {
	Date          string
	Connections   int
	Devices       int
	Nodes         int
	Upload        int64
	Download      int64
	TotalBytes    int64
	TrafficByRate bool
	ProbeChecks   int
	ProbeOK       int
	ProbeAvgMs    float64
	NodeProbes    []dailyNodeProbeStat
	MissingProbes []string
	TopDevices    []dailyDeviceStat
	TopTargets    []dailyTargetStat
	TopNodes      []dailyNodeStat
}

func (s *Store) DailyReportLastDate(ctx context.Context) (string, error) {
	var date string
	err := s.db.QueryRowContext(ctx, `SELECT last_sent_date FROM daily_report_state WHERE id = 1`).Scan(&date)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read daily report state: %w", err)
	}
	return date, nil
}

func (s *Store) SetDailyReportLastDate(ctx context.Context, date string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO daily_report_state (id, last_sent_date) VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET last_sent_date = excluded.last_sent_date`, date)
	if err != nil {
		return fmt.Errorf("write daily report state: %w", err)
	}
	return nil
}

func (s *Server) StartDailyReport(ctx context.Context) {
	go s.runDailyReportLoop(ctx)
}

func (s *Server) runDailyReportLoop(ctx context.Context) {
	for {
		now := time.Now().In(NotificationLocation)
		next := time.Date(now.Year(), now.Month(), now.Day(), dailyReportHour, 0, 0, 0, NotificationLocation)

		if !now.Before(next) {
			sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			last, err := s.store.DailyReportLastDate(sendCtx)
			if err == nil && last != now.Format("2006-01-02") {
				if reportErr := s.sendDailyReport(sendCtx); reportErr != nil {
					log.Printf("daily report failed: %v", reportErr)
				}
			}
			cancel()
			next = next.AddDate(0, 0, 1)
		}

		wait := time.Until(next)
		if wait <= 0 {
			wait = time.Minute
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Server) sendDailyReport(ctx context.Context) error {
	settings, err := s.store.GetAlertSettings(ctx)
	if err != nil || !settings.HasNotifier() {
		return nil
	}
	report, err := s.buildDailyReport(ctx)
	if err != nil {
		return err
	}

	payload := report.alertPayload()
	if s.alertEngine != nil {
		s.alertEngine.sendAlert(ctx, payload)
	}

	today := time.Now().In(NotificationLocation).Format("2006-01-02")
	if err := s.store.SetDailyReportLastDate(ctx, today); err != nil {
		log.Printf("daily report state update failed: %v", err)
	}
	return nil
}

func (s *Server) buildDailyReport(ctx context.Context) (dailyReportData, error) {
	now := time.Now().In(NotificationLocation)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, NotificationLocation)
	start := today.AddDate(0, 0, -1)
	end := today
	startUnix, endUnix := start.Unix(), end.Unix()

	report := dailyReportData{Date: start.Format("2006-01-02")}

	if err := s.store.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COUNT(DISTINCT source_ip), COUNT(DISTINCT node_id),
		       COALESCE(SUM(upload), 0), COALESCE(SUM(download), 0)
		FROM connection_records
		WHERE started_at >= ? AND started_at < ?`,
		startUnix, endUnix,
	).Scan(&report.Connections, &report.Devices, &report.Nodes, &report.Upload, &report.Download); err != nil {
		return report, fmt.Errorf("daily report connection summary: %w", err)
	}
	report.TotalBytes = report.Upload + report.Download

	devices, err := s.dailyTopDevices(ctx, startUnix, endUnix)
	if err != nil {
		return report, err
	}
	report.TopDevices = devices

	targets, err := s.dailyTopTargets(ctx, startUnix, endUnix)
	if err != nil {
		return report, err
	}
	report.TopTargets = targets

	nodeTraffic, trafficByRate, err := s.dailyNodeTraffic(ctx, startUnix, endUnix)
	if err != nil {
		return report, err
	}
	report.TrafficByRate = trafficByRate
	if trafficByRate {
		var total int64
		for _, bytes := range nodeTraffic {
			total += bytes
		}
		if total > 0 {
			report.TotalBytes = total
		} else {
			report.TrafficByRate = false
		}
	}

	nodes, err := s.dailyTopNodes(ctx, startUnix, endUnix, nodeTraffic)
	if err != nil {
		return report, err
	}
	report.TopNodes = nodes

	if err := s.store.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN latency_ms >= 0 THEN 1 ELSE 0 END), 0),
		       COALESCE(AVG(CASE WHEN latency_ms >= 0 THEN latency_ms END), 0)
		FROM node_latency_points
		WHERE ts >= ? AND ts < ?`,
		startUnix, endUnix,
	).Scan(&report.ProbeChecks, &report.ProbeOK, &report.ProbeAvgMs); err != nil {
		return report, fmt.Errorf("daily report probe summary: %w", err)
	}

	nodeProbes, err := s.dailyNodeProbes(ctx, startUnix, endUnix)
	if err != nil {
		return report, err
	}
	report.NodeProbes = nodeProbes

	missing, err := s.dailyNodesWithoutProbes(ctx, startUnix, endUnix)
	if err != nil {
		return report, err
	}
	report.MissingProbes = missing

	return report, nil
}

func (s *Server) dailyTopDevices(ctx context.Context, start, end int64) ([]dailyDeviceStat, error) {
	rows, err := s.store.db.QueryContext(ctx, `
		SELECT source_ip, COALESCE(MAX(source_location), ''), COUNT(*),
		       COALESCE(SUM(upload + download), 0)
		FROM connection_records
		WHERE started_at >= ? AND started_at < ? AND source_ip != ''
		GROUP BY source_ip
		ORDER BY COUNT(*) DESC, SUM(upload + download) DESC
		LIMIT ?`, start, end, dailyRankingLimit)
	if err != nil {
		return nil, fmt.Errorf("daily report top devices: %w", err)
	}
	defer rows.Close()
	items := []dailyDeviceStat{}
	for rows.Next() {
		var item dailyDeviceStat
		if err := rows.Scan(&item.SourceIP, &item.Location, &item.Count, &item.TotalBytes); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func isNoiseTarget(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}

	host := strings.ToLower(raw)
	var port string
	if h, p, err := net.SplitHostPort(host); err == nil {
		host = h
		port = p
	} else if idx := strings.LastIndex(host, ":"); idx != -1 && !strings.Contains(host, "]") {
		host = host[:idx]
		port = host[idx+1:]
	}
	host = strings.Trim(host, "[]")

	// Standard DNS/DoT/NTP ports
	if port == "53" || port == "853" || port == "123" {
		return true
	}

	// Direct DNS server IPs
	switch host {
	case "8.8.8.8", "8.8.4.4",
		"1.1.1.1", "1.0.0.1",
		"9.9.9.9", "149.112.112.112",
		"223.5.5.5", "223.6.6.6",
		"119.29.29.29", "182.254.116.116",
		"180.76.76.76",
		"114.114.114.114", "114.114.115.115",
		"208.67.222.222", "208.67.220.220",
		"208.67.222.123", "208.67.220.123",
		"76.76.2.0", "76.76.10.0",
		"94.140.14.14", "94.140.15.15":
		return true
	}

	// Connectivity check / probe / captive portal domains
	if strings.Contains(host, "connectivitycheck") ||
		strings.Contains(host, "msftconnecttest") ||
		strings.Contains(host, "msftncsi") ||
		strings.Contains(host, "detectportal") ||
		strings.Contains(host, "network-test") ||
		strings.Contains(host, "captive") ||
		host == "gstatic.com" || strings.HasSuffix(host, ".gstatic.com") ||
		host == "clients3.google.com" ||
		host == "cp.cloudflare.com" ||
		host == "connectivity.apple.com" ||
		host == "nmcheck.gnome.org" ||
		host == "networkcheck.kde.org" ||
		host == "ping.archlinux.org" ||
		host == "mask.icloud.com" ||
		host == "mask-h2.icloud.com" ||
		strings.HasSuffix(host, ".ntp.org") || host == "pool.ntp.org" {
		return true
	}

	// DNS / DoH / DoT resolver domains
	if host == "dns.google" || strings.HasSuffix(host, ".dns.google") ||
		host == "one.one.one.one" ||
		host == "dns.sb" || host == "doh.sb" || host == "doh.pub" ||
		strings.HasSuffix(host, "cloudflare-dns.com") ||
		strings.HasSuffix(host, "opendns.com") ||
		strings.HasSuffix(host, "quad9.net") ||
		strings.HasSuffix(host, "nextdns.io") ||
		strings.HasSuffix(host, "adguard-dns.com") ||
		strings.HasSuffix(host, "alidns.com") ||
		strings.HasSuffix(host, "dnspod.cn") ||
		strings.HasPrefix(host, "dns.") || strings.HasPrefix(host, "doh.") || strings.HasPrefix(host, "dot.") ||
		strings.Contains(host, ".dns.") || strings.Contains(host, "-dns.") ||
		strings.Contains(host, ".doh.") || strings.Contains(host, "-doh.") {
		return true
	}

	return false
}

func (s *Server) dailyTopTargets(ctx context.Context, start, end int64) ([]dailyTargetStat, error) {
	candidateLimit := dailyTargetLimit * 20
	if candidateLimit < 100 {
		candidateLimit = 100
	}
	rows, err := s.store.db.QueryContext(ctx, `
		SELECT
			CASE WHEN host != '' THEN '域名' ELSE 'IP' END,
			CASE WHEN host != '' THEN host ELSE destination END,
			COALESCE(MAX(CASE WHEN host = '' THEN destination_location ELSE '' END), ''),
			COUNT(*),
			COALESCE(SUM(upload + download), 0)
		FROM connection_records
		WHERE started_at >= ? AND started_at < ? AND (host != '' OR destination != '')
		GROUP BY
			CASE WHEN host != '' THEN '域名' ELSE 'IP' END,
			CASE WHEN host != '' THEN host ELSE destination END
		ORDER BY COUNT(*) DESC, SUM(upload + download) DESC
		LIMIT ?`, start, end, candidateLimit)
	if err != nil {
		return nil, fmt.Errorf("daily report top targets: %w", err)
	}
	defer rows.Close()
	items := make([]dailyTargetStat, 0, dailyTargetLimit)
	for rows.Next() {
		var item dailyTargetStat
		if err := rows.Scan(&item.Kind, &item.Label, &item.Location, &item.Count, &item.TotalBytes); err != nil {
			return nil, err
		}
		if isNoiseTarget(item.Label) {
			continue
		}
		items = append(items, item)
		if len(items) >= dailyTargetLimit {
			break
		}
	}
	return items, rows.Err()
}

// dailyNodeTraffic estimates yesterday's traffic from the one-minute node rate
// samples. If no rate samples exist, the caller falls back to connection-record
// totals.
func (s *Server) dailyNodeTraffic(ctx context.Context, start, end int64) (map[string]int64, bool, error) {
	seconds := int64(metricPointInterval / time.Second)
	rows, err := s.store.db.QueryContext(ctx, `
		SELECT node_id, COALESCE(SUM(net_rx), 0) * ?, COALESCE(SUM(net_tx), 0) * ?
		FROM node_metric_points
		WHERE ts >= ? AND ts < ?
		GROUP BY node_id`, seconds, seconds, start, end)
	if err != nil {
		return nil, false, fmt.Errorf("daily report node traffic: %w", err)
	}
	defer rows.Close()
	traffic := map[string]int64{}
	for rows.Next() {
		var nodeID string
		var rx, tx int64
		if err := rows.Scan(&nodeID, &rx, &tx); err != nil {
			return nil, false, err
		}
		traffic[nodeID] = rx + tx
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return traffic, len(traffic) > 0, nil
}

func (s *Server) dailyTopNodes(ctx context.Context, start, end int64, traffic map[string]int64) ([]dailyNodeStat, error) {
	rows, err := s.store.db.QueryContext(ctx, `
		SELECT node_id, COALESCE(MAX(node_name), ''), COUNT(*),
		       COALESCE(SUM(upload + download), 0)
		FROM connection_records
		WHERE started_at >= ? AND started_at < ?
		GROUP BY node_id
		ORDER BY COUNT(*) DESC, SUM(upload + download) DESC
		LIMIT ?`, start, end, dailyRankingLimit)
	if err != nil {
		return nil, fmt.Errorf("daily report top nodes: %w", err)
	}
	defer rows.Close()
	items := []dailyNodeStat{}
	for rows.Next() {
		var item dailyNodeStat
		var fallbackBytes int64
		if err := rows.Scan(&item.NodeID, &item.Name, &item.Connections, &fallbackBytes); err != nil {
			return nil, err
		}
		item.TotalBytes = traffic[item.NodeID]
		if item.TotalBytes == 0 {
			item.TotalBytes = fallbackBytes
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Server) dailyNodeProbes(ctx context.Context, start, end int64) ([]dailyNodeProbeStat, error) {
	rows, err := s.store.db.QueryContext(ctx, `
		SELECT p.node_id,
		       n.name,
		       COALESCE(NULLIF(p.probe_name, ''), p.probe_id) AS probe_display,
		       COUNT(*),
		       COALESCE(SUM(CASE WHEN p.latency_ms < 0 THEN 1 ELSE 0 END), 0),
		       COALESCE(AVG(CASE WHEN p.latency_ms >= 0 THEN p.latency_ms END), 0),
		       COALESCE(MIN(CASE WHEN p.latency_ms >= 0 THEN p.latency_ms END), 0),
		       COALESCE(MAX(CASE WHEN p.latency_ms >= 0 THEN p.latency_ms END), 0)
		FROM node_latency_points p
		JOIN nodes n ON n.id = p.node_id
		WHERE p.ts >= ? AND p.ts < ? AND n.revoked_at IS NULL
		GROUP BY p.node_id, probe_display
		ORDER BY n.name ASC, probe_display ASC`, start, end)
	if err != nil {
		return nil, fmt.Errorf("daily report node probes: %w", err)
	}
	defer rows.Close()

	type nodeAccumulator struct {
		nodeID     string
		name       string
		samples    int
		failed     int
		validCount int
		validSum   float64
		minMs      float64
		maxMs      float64
		probes     []dailyProbeItem
	}

	nodeMap := make(map[string]*nodeAccumulator)
	nodeOrder := make([]string, 0)

	for rows.Next() {
		var (
			nodeID       string
			name         string
			probeDisplay string
			samples      int
			failed       int
			avgMs        float64
			minMs        float64
			maxMs        float64
		)
		if err := rows.Scan(&nodeID, &name, &probeDisplay, &samples, &failed, &avgMs, &minMs, &maxMs); err != nil {
			return nil, err
		}

		acc, ok := nodeMap[nodeID]
		if !ok {
			acc = &nodeAccumulator{
				nodeID: nodeID,
				name:   name,
				minMs:  -1,
				maxMs:  -1,
			}
			nodeMap[nodeID] = acc
			nodeOrder = append(nodeOrder, nodeID)
		}

		probeLoss := 0.0
		if samples > 0 {
			probeLoss = float64(failed) * 100 / float64(samples)
		}

		acc.probes = append(acc.probes, dailyProbeItem{
			ProbeName: probeDisplay,
			Samples:   samples,
			Failed:    failed,
			Loss:      probeLoss,
			AvgMs:     avgMs,
			MinMs:     minMs,
			MaxMs:     maxMs,
		})

		acc.samples += samples
		acc.failed += failed
		validSamples := samples - failed
		if validSamples > 0 {
			acc.validCount += validSamples
			acc.validSum += avgMs * float64(validSamples)
			if acc.minMs < 0 || minMs < acc.minMs {
				acc.minMs = minMs
			}
			if acc.maxMs < 0 || maxMs > acc.maxMs {
				acc.maxMs = maxMs
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	results := make([]dailyNodeProbeStat, 0, len(nodeOrder))
	for _, nodeID := range nodeOrder {
		acc := nodeMap[nodeID]
		overallLoss := 0.0
		if acc.samples > 0 {
			overallLoss = float64(acc.failed) * 100 / float64(acc.samples)
		}
		overallAvg := 0.0
		if acc.validCount > 0 {
			overallAvg = acc.validSum / float64(acc.validCount)
		}
		minVal := acc.minMs
		if minVal < 0 {
			minVal = 0
		}
		maxVal := acc.maxMs
		if maxVal < 0 {
			maxVal = 0
		}
		results = append(results, dailyNodeProbeStat{
			NodeID:  acc.nodeID,
			Name:    acc.name,
			Samples: acc.samples,
			Failed:  acc.failed,
			Loss:    overallLoss,
			AvgMs:   overallAvg,
			MinMs:   minVal,
			MaxMs:   maxVal,
			Probes:  acc.probes,
		})
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Loss != results[j].Loss {
			return results[i].Loss < results[j].Loss
		}
		return results[i].AvgMs < results[j].AvgMs
	})

	if len(results) > dailyProbeNodeLimit {
		results = results[:dailyProbeNodeLimit]
	}

	return results, nil
}

func (s *Server) dailyNodesWithoutProbes(ctx context.Context, start, end int64) ([]string, error) {
	rows, err := s.store.db.QueryContext(ctx, `
		SELECT n.name
		FROM nodes n
		WHERE n.revoked_at IS NULL
		  AND NOT EXISTS (
			SELECT 1 FROM node_latency_points p
			WHERE p.node_id = n.id AND p.ts >= ? AND p.ts < ?
		  )
		ORDER BY n.name
		LIMIT 10`, start, end)
	if err != nil {
		return nil, fmt.Errorf("daily report missing probe nodes: %w", err)
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func (r dailyReportData) alertPayload() alertPayload {
	dateRange := fmt.Sprintf("%s 00:00 - 23:59 (%s)", r.Date, NotificationLocation.String())

	var b strings.Builder
	b.WriteString("<b>Polaris 管理通知 · 每日简报</b>\n")
	b.WriteString(tgEscape(dateRange))
	b.WriteString("\n\n")

	// 1. 昨日总览
	b.WriteString("<b>【昨日总览】</b>\n")
	b.WriteString(fmt.Sprintf("• <b>日期</b>：%s\n", tgEscape(r.Date)))
	b.WriteString(fmt.Sprintf("• <b>连接记录</b>：%s 次\n", formatReportCount(r.Connections)))
	b.WriteString(fmt.Sprintf("• <b>活跃设备</b>：%s 个\n", formatReportCount(r.Devices)))
	b.WriteString(fmt.Sprintf("• <b>活跃节点</b>：%s 个\n", formatReportCount(r.Nodes)))
	if r.TrafficByRate {
		b.WriteString(fmt.Sprintf("• <b>总流量</b>：%s\n", formatReportBytes(r.TotalBytes)))
	} else {
		b.WriteString(fmt.Sprintf("• <b>总流量</b>：↓ %s / ↑ %s（共 %s）\n",
			formatReportBytes(r.Upload), formatReportBytes(r.Download), formatReportBytes(r.TotalBytes)))
	}
	if r.ProbeChecks > 0 && r.ProbeOK == 0 {
		b.WriteString("• <b>探针</b>：丢包 100%（全部超时）\n")
	} else if r.ProbeChecks > 0 {
		b.WriteString(fmt.Sprintf("• <b>探针</b>：丢包 %s｜平均延迟 %.0f ms\n",
			formatReportPercent(r.ProbeChecks-r.ProbeOK, r.ProbeChecks), r.ProbeAvgMs))
	} else {
		b.WriteString("• <b>探针</b>：昨日没有探针数据\n")
	}

	// 2. 节点网络质量
	b.WriteString("\n<b>【节点网络质量】</b>\n")
	if len(r.NodeProbes) == 0 {
		b.WriteString("昨日没有探针数据。\n")
	} else {
		for _, node := range r.NodeProbes {
			nodeName := node.Name
			if nodeName == "" {
				nodeName = node.NodeID
			}
			if node.Samples == node.Failed {
				b.WriteString(fmt.Sprintf("• <b>%s</b>：丢包 100%%（全部超时）\n", tgEscape(nodeName)))
			} else {
				b.WriteString(fmt.Sprintf("• <b>%s</b>：综合丢包 %.1f%%｜平均延迟 %.0f ms（最低 %.0f / 最高 %.0f）\n",
					tgEscape(nodeName), node.Loss, node.AvgMs, node.MinMs, node.MaxMs))
			}
			for _, probe := range node.Probes {
				probeName := probe.ProbeName
				if probeName == "" {
					probeName = "未知探针"
				}
				if probe.Samples == probe.Failed {
					b.WriteString(fmt.Sprintf("  ↳ %s：丢包 100%%（超时）\n", tgEscape(probeName)))
				} else {
					b.WriteString(fmt.Sprintf("  ↳ %s：丢包 %.1f%% · 平均 %.0f ms\n",
						tgEscape(probeName), probe.Loss, probe.AvgMs))
				}
			}
		}
		if len(r.MissingProbes) > 0 {
			b.WriteString(fmt.Sprintf("• <b>无探针数据节点</b>：%s\n", tgEscape(strings.Join(r.MissingProbes, "、"))))
		}
	}

	// 3. 活跃设备排行
	b.WriteString("\n<b>【活跃设备排行】</b>\n")
	if len(r.TopDevices) == 0 {
		b.WriteString("昨日没有连接记录。\n")
	} else {
		for index, device := range r.TopDevices {
			loc := ""
			if device.Location != "" {
				loc = tgEscape(device.Location) + "｜"
			}
			b.WriteString(fmt.Sprintf("%d. <code>%s</code>：%s%s 次｜%s\n",
				index+1, tgEscape(device.SourceIP), loc, formatReportCount(device.Count), formatReportBytes(device.TotalBytes)))
		}
	}

	// 4. 热门访问目标
	b.WriteString("\n<b>【热门访问目标】</b>\n")
	if len(r.TopTargets) == 0 {
		b.WriteString("昨日没有可统计的访问目标。\n")
	} else {
		for index, target := range r.TopTargets {
			loc := ""
			if target.Location != "" {
				loc = tgEscape(target.Location) + "｜"
			}
			b.WriteString(fmt.Sprintf("%d. <code>%s</code>：%s%s 次｜%s\n",
				index+1, tgEscape(target.Label), loc, formatReportCount(target.Count), formatReportBytes(target.TotalBytes)))
		}
	}

	// 5. 节点使用排行
	b.WriteString("\n<b>【节点使用排行】</b>\n")
	if len(r.TopNodes) == 0 {
		b.WriteString("昨日没有节点访问记录。\n")
	} else {
		for index, node := range r.TopNodes {
			name := node.Name
			if name == "" {
				name = node.NodeID
			}
			b.WriteString(fmt.Sprintf("%d. <b>%s</b>：%s 次连接｜%s\n",
				index+1, tgEscape(name), formatReportCount(node.Connections), formatReportBytes(node.TotalBytes)))
		}
	}

	b.WriteString("\n时间：")
	b.WriteString(tgEscape(NowAlertTime()))

	telegramText := b.String()

	barkLines := []string{
		fmt.Sprintf("连接 %s 次｜设备 %s 个｜节点 %s 个", formatReportCount(r.Connections), formatReportCount(r.Devices), formatReportCount(r.Nodes)),
	}
	if r.TrafficByRate {
		barkLines = append(barkLines, fmt.Sprintf("流量 %s", formatReportBytes(r.TotalBytes)))
	} else {
		barkLines = append(barkLines, fmt.Sprintf("流量 ↓ %s / ↑ %s", formatReportBytes(r.Upload), formatReportBytes(r.Download)))
	}
	if r.ProbeChecks > 0 && r.ProbeOK == 0 {
		barkLines = append(barkLines, "探针：丢包 100%（全部超时）")
	} else if r.ProbeChecks > 0 {
		barkLines = append(barkLines, fmt.Sprintf("探针：丢包 %s｜平均延迟 %.0f ms",
			formatReportPercent(r.ProbeChecks-r.ProbeOK, r.ProbeChecks), r.ProbeAvgMs))
	} else {
		barkLines = append(barkLines, "探针：昨日没有数据")
	}
	if len(r.TopDevices) > 0 {
		barkLines = append(barkLines, fmt.Sprintf("最多设备：%s（%d 次）", r.TopDevices[0].SourceIP, r.TopDevices[0].Count))
	}
	if len(r.TopTargets) > 0 {
		barkLines = append(barkLines, fmt.Sprintf("最多目标：%s（%d 次）",
			r.TopTargets[0].Label, r.TopTargets[0].Count))
	}
	if len(r.TopNodes) > 0 {
		name := r.TopNodes[0].Name
		if name == "" {
			name = r.TopNodes[0].NodeID
		}
		barkLines = append(barkLines, fmt.Sprintf("最多节点：%s（%d 次）", name, r.TopNodes[0].Connections))
	}

	return alertPayload{
		Severity:  severityInfo,
		BarkTitle: fmt.Sprintf("每日简报：%s", r.Date),
		BarkBody:  strings.Join(barkLines, "\n"),
		BarkGroup: "每日简报",
		BarkSound: "telegraph.caf",
		Telegram:  telegramText,
	}
}

func formatReportCount(value int) string {
	text := strconv.Itoa(value)
	negative := strings.HasPrefix(text, "-")
	if negative {
		text = text[1:]
	}
	for index := len(text) - 3; index > 0; index -= 3 {
		text = text[:index] + "," + text[index:]
	}
	if negative {
		return "-" + text
	}
	return text
}

func formatReportPercent(part, total int) string {
	if total <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", float64(part)*100/float64(total))
}

func formatReportBytes(value int64) string {
	if value <= 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	number := float64(value)
	index := 0
	for number >= 1024 && index < len(units)-1 {
		number /= 1024
		index++
	}
	if index == 0 {
		return fmt.Sprintf("%d %s", value, units[index])
	}
	return fmt.Sprintf("%.2f %s", number, units[index])
}
