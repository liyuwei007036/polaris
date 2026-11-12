package control

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestBuildDailyReport(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	now := time.Now().In(NotificationLocation)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, NotificationLocation)
	start := today.AddDate(0, 0, -1)
	yesterday := start.Unix() + 3600

	if _, err := store.db.ExecContext(ctx, `INSERT INTO nodes (id, name, public_key, created_at) VALUES (?, ?, ?, ?)`,
		"node-a", "香港服务器", []byte("12345678901234567890123456789012"), nowUnix()); err != nil {
		t.Fatalf("insert node-a: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO nodes (id, name, public_key, created_at) VALUES (?, ?, ?, ?)`,
		"node-b", "东京服务器", []byte("abcdefghijklmnopqrstuvwxyz123456"), nowUnix()); err != nil {
		t.Fatalf("insert node-b: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO nodes (id, name, public_key, created_at) VALUES (?, ?, ?, ?)`,
		"node-c", "大阪服务器", []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZ123456"), nowUnix()); err != nil {
		t.Fatalf("insert node-c: %v", err)
	}

	for _, record := range []struct {
		id, source, host, destination string
		upload, download              int64
	}{
		{"c1", "1.2.3.4", "example.com", "93.184.216.34:443", 1000, 2000},
		{"c2", "1.2.3.4", "example.com", "93.184.216.34:443", 500, 500},
		{"c3", "5.6.7.8", "", "93.184.216.34:443", 300, 700},
		{"c4", "1.2.3.4", "dns.google", "8.8.8.8:443", 100, 100},
		{"c5", "1.2.3.4", "www.gstatic.com", "142.250.72.14:443", 100, 100},
		{"c6", "1.2.3.4", "doh.opendns.com", "208.67.222.222:443", 100, 100},
		{"c7", "5.6.7.8", "", "1.1.1.1:443", 100, 100},
		{"c8", "5.6.7.8", "", "223.5.5.5:53", 100, 100},
	} {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO connection_records
			(id, node_id, node_name, source_ip, source_location, destination, host, upload, download, started_at, created_at)
			VALUES (?, 'node-a', '香港服务器', ?, ?, ?, ?, ?, ?, ?, ?)`,
			record.id, record.source, "测试地区", record.destination, record.host, record.upload, record.download, yesterday, yesterday); err != nil {
			t.Fatalf("insert connection record %s: %v", record.id, err)
		}
	}

	// Two one-minute samples: 1000 B/s down + 500 B/s up.
	for _, second := range []int64{60, 120} {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO node_metric_points (node_id, ts, net_rx, net_tx) VALUES (?, ?, ?, ?)`,
			"node-a", start.Unix()+second, 1000, 500); err != nil {
			t.Fatalf("insert metric point: %v", err)
		}
	}

	for _, probe := range []struct {
		id, name, target string
		latency          int
	}{
		{"p1", "Telegram", "149.154.167.50:443", 10},
		{"p2", "Telegram", "149.154.167.50:443", 20},
		{"p3", "Telegram", "149.154.167.50:443", -1},
		{"p4", "Google", "142.250.72.14:443", 30},
	} {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO node_latency_points (node_id, probe_id, probe_name, target, ts, latency_ms)
			VALUES ('node-a', ?, ?, ?, ?, ?)`, probe.id, probe.name, probe.target, yesterday, probe.latency); err != nil {
			t.Fatalf("insert probe point: %v", err)
		}
	}

	if _, err := store.db.ExecContext(ctx, `INSERT INTO node_latency_points (node_id, probe_id, probe_name, target, ts, latency_ms)
		VALUES ('node-c', 'p5', 'Telegram', '149.154.167.50:443', ?, 5)`, yesterday); err != nil {
		t.Fatalf("insert best probe point: %v", err)
	}

	server := &Server{store: store}
	report, err := server.buildDailyReport(ctx)
	if err != nil {
		t.Fatalf("build daily report: %v", err)
	}

	if report.Connections != 8 || report.Devices != 2 || report.Nodes != 1 {
		t.Errorf("unexpected summary: %+v", report)
	}
	if !report.TrafficByRate || report.TotalBytes != 180000 {
		t.Errorf("expected estimated traffic 180000, got rate=%v bytes=%d", report.TrafficByRate, report.TotalBytes)
	}
	if report.ProbeChecks != 5 || report.ProbeOK != 4 || report.ProbeAvgMs != 16.25 {
		t.Errorf("unexpected probe summary: checks=%d ok=%d avg=%.2f", report.ProbeChecks, report.ProbeOK, report.ProbeAvgMs)
	}
	if len(report.TopDevices) == 0 || report.TopDevices[0].SourceIP != "1.2.3.4" || report.TopDevices[0].Count != 5 {
		t.Errorf("unexpected top devices: %+v", report.TopDevices)
	}
	if len(report.TopTargets) != 2 ||
		report.TopTargets[0].Kind != "域名" || report.TopTargets[0].Label != "example.com" || report.TopTargets[0].Count != 2 ||
		report.TopTargets[1].Kind != "IP" || report.TopTargets[1].Label != "93.184.216.34:443" || report.TopTargets[1].Count != 1 {
		t.Errorf("unexpected top targets: %+v", report.TopTargets)
	}
	if len(report.NodeProbes) != 2 ||
		report.NodeProbes[0].Name != "大阪服务器" || report.NodeProbes[0].Loss != 0 || report.NodeProbes[0].AvgMs != 5 {
		t.Errorf("expected best node first, got: %+v", report.NodeProbes)
	}
	if len(report.NodeProbes[0].Probes) != 1 || report.NodeProbes[0].Probes[0].ProbeName != "Telegram" {
		t.Errorf("expected 大阪服务器 probes: %+v", report.NodeProbes[0].Probes)
	}
	if report.NodeProbes[1].Name != "香港服务器" ||
		report.NodeProbes[1].Samples != 4 || report.NodeProbes[1].Failed != 1 ||
		report.NodeProbes[1].Loss != 25 || report.NodeProbes[1].AvgMs != 20 ||
		report.NodeProbes[1].MinMs != 10 || report.NodeProbes[1].MaxMs != 30 {
		t.Errorf("unexpected node probe stats: %+v", report.NodeProbes)
	}
	if len(report.NodeProbes[1].Probes) != 2 {
		t.Errorf("expected 2 probes under 香港服务器, got: %+v", report.NodeProbes[1].Probes)
	}
	if len(report.MissingProbes) != 1 || report.MissingProbes[0] != "东京服务器" {
		t.Errorf("expected 东京服务器 without probes, got %+v", report.MissingProbes)
	}

	payload := report.alertPayload()
	if !strings.Contains(payload.Telegram, "每日简报") || !strings.Contains(payload.Telegram, "example.com") {
		t.Errorf("unexpected report telegram text: %s", payload.Telegram)
	}
	if !strings.Contains(payload.Telegram, "服务器网络质量") || !strings.Contains(payload.Telegram, "丢包 25.0%") {
		t.Errorf("expected packet loss in report, got: %s", payload.Telegram)
	}
	for _, heading := range []string{"昨日总览", "活跃设备排行", "热门访问目标", "服务器使用排行"} {
		if !strings.Contains(payload.Telegram, heading) {
			t.Errorf("expected section heading %q, got: %s", heading, payload.Telegram)
		}
	}
	if strings.Contains(payload.Telegram, "检测次数") {
		t.Errorf("report should not show probe check counts: %s", payload.Telegram)
	}
	if !strings.Contains(payload.Telegram, "<code>example.com</code>") || !strings.Contains(payload.Telegram, "<code>93.184.216.34:443</code>") {
		t.Errorf("expected code-tagged targets, got: %s", payload.Telegram)
	}
	if strings.Contains(payload.Telegram, "dns.google") || strings.Contains(payload.Telegram, "www.gstatic.com") || strings.Contains(payload.Telegram, "1.1.1.1:443") {
		t.Errorf("noise targets should be excluded from report: %s", payload.Telegram)
	}
	if !strings.Contains(payload.Telegram, "↳ Telegram") || !strings.Contains(payload.Telegram, "↳ Google") {
		t.Errorf("expected probe breakdown in telegram text: %s", payload.Telegram)
	}
	if strings.Contains(payload.Telegram, "[域名]") || strings.Contains(payload.Telegram, "[IP]") {
		t.Errorf("target type labels should not be shown: %s", payload.Telegram)
	}
	if strings.Contains(payload.Telegram, "统计基于昨日") || strings.Contains(payload.Telegram, "采样估算") {
		t.Errorf("daily report should not explain its calculation method: %s", payload.Telegram)
	}
	if !strings.Contains(payload.BarkBody, "最多设备") || !strings.Contains(payload.BarkBody, "1.2.3.4") {
		t.Errorf("unexpected report bark body: %s", payload.BarkBody)
	}
	if !strings.Contains(payload.BarkBody, "最多目标") {
		t.Errorf("expected top target in bark body, got: %s", payload.BarkBody)
	}
	if !strings.Contains(payload.BarkBody, "丢包 20.0%") {
		t.Errorf("expected probe loss in bark body, got: %s", payload.BarkBody)
	}
}

func TestIsNoiseTarget(t *testing.T) {
	noise := []string{
		"dns.google",
		"www.gstatic.com",
		"connectivitycheck.gstatic.com",
		"connectivitycheck.android.com",
		"msftconnecttest.com",
		"msftncsi.com",
		"captive.apple.com",
		"cp.cloudflare.com",
		"detectportal.firefox.com",
		"doh.opendns.com",
		"chrome.cloudflare-dns.com",
		"one.one.one.one",
		"1.1.1.1:443",
		"8.8.8.8:53",
		"223.5.5.5:443",
		"114.114.114.114:53",
		"mask.icloud.com",
		"pool.ntp.org",
	}
	for _, target := range noise {
		if !isNoiseTarget(target) {
			t.Errorf("expected isNoiseTarget(%q) = true, got false", target)
		}
	}

	valid := []string{
		"example.com",
		"cdn.jsdelivr.net",
		"api.openai.com",
		"github.com",
		"93.184.216.34:443",
		"google.com",
		"cloudflare.com",
	}
	for _, target := range valid {
		if isNoiseTarget(target) {
			t.Errorf("expected isNoiseTarget(%q) = false, got true", target)
		}
	}
}

func TestFormatReportHelpers(t *testing.T) {
	if got := formatReportCount(1234567); got != "1,234,567" {
		t.Errorf("formatReportCount = %q", got)
	}
	if got := formatReportPercent(99, 100); got != "99.0%" {
		t.Errorf("formatReportPercent = %q", got)
	}
	if got := formatReportBytes(1536); got != "1.50 KB" {
		t.Errorf("formatReportBytes = %q", got)
	}
}
