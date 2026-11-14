package control

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/liyuwei007036/polaris/internal/security"
)

type demoNodeDef struct {
	ID             string
	Name           string
	ClientAddress  string
	IP             string
	OS             string
	Arch           string
	Provider       string
	ProviderURL    string
	Price          string
	PricePeriod    string
	Currency       string
	TrafficPlan    string
	Bandwidth      string
	ExpiresOn      string
	CPUName        string
	CPUCores       uint32
	BaseCPU        float64
	MemTotal       uint64
	MemUsed        uint64
	SwapTotal      uint64
	SwapUsed       uint64
	DiskTotal      uint64
	DiskUsed       uint64
	BaseRxRate     uint64
	BaseTxRate     uint64
	UptimeSec      uint64
	AvgLatency     float64
	LatencyJitter  float64
	AvgLoss        float64
	TelecomRoute   string
	TelecomLatency int
	TelecomSpeed   float64
	UnicomRoute    string
	UnicomLatency  int
	UnicomSpeed    float64
	MobileRoute    string
	MobileLatency  int
	MobileSpeed    float64
}

var demoNodes = []demoNodeDef{
	{
		ID:             "demo-node-tyo-01",
		Name:           "🇯🇵 东京 Softbank 01",
		ClientAddress:  "139.162.65.1:19994",
		IP:             "139.162.65.1",
		OS:             "Ubuntu 22.04.4 LTS",
		Arch:           "amd64",
		Provider:       "Linode",
		ProviderURL:    "https://www.linode.com",
		Price:          "10.00",
		PricePeriod:    "month",
		Currency:       "USD",
		TrafficPlan:    "2000",
		Bandwidth:      "1 Gbps",
		ExpiresOn:      "2027-05-15",
		CPUName:        "AMD EPYC 7763 64-Core Processor",
		CPUCores:       4,
		BaseCPU:        14.5,
		MemTotal:       8589934592,   // 8 GB
		MemUsed:        2791728742,   // ~32.5%
		SwapTotal:      2147483648,   // 2 GB
		SwapUsed:       104857600,    // 100 MB
		DiskTotal:      107374182400, // 100 GB
		DiskUsed:       23622320128,  // 22 GB
		BaseRxRate:     14500000,     // 14.5 MB/s
		BaseTxRate:     2300000,      // 2.3 MB/s
		UptimeSec:      1245600,      // 14.4 days
		AvgLatency:     32.4,
		LatencyJitter:  2.8,
		AvgLoss:        0.0,
		TelecomRoute:   "软银 BBTEC 直连",
		TelecomLatency: 35,
		TelecomSpeed:   850.5,
		UnicomRoute:    "联通 4837 直连",
		UnicomLatency:  42,
		UnicomSpeed:    720.0,
		MobileRoute:    "移动 CMI 直连",
		MobileLatency:  38,
		MobileSpeed:    680.0,
	},
	{
		ID:             "demo-node-osa-01",
		Name:           "🇯🇵 大阪 Oracle Cloud",
		ClientAddress:  "150.230.198.1:19994",
		IP:             "150.230.198.1",
		OS:             "Oracle Linux 9.3",
		Arch:           "arm64",
		Provider:       "Oracle Cloud",
		ProviderURL:    "https://cloud.oracle.com",
		Price:          "0.00",
		PricePeriod:    "month",
		Currency:       "USD",
		TrafficPlan:    "10000",
		Bandwidth:      "4 Gbps",
		ExpiresOn:      "2035-01-01",
		CPUName:        "Ampere Altra 80-Core",
		CPUCores:       4,
		BaseCPU:        8.2,
		MemTotal:       25769803776, // 24 GB
		MemUsed:        6442450944,  // 25%
		SwapTotal:      4294967296,  // 4 GB
		SwapUsed:       0,
		DiskTotal:      214748364800, // 200 GB
		DiskUsed:       32212254720,  // 30 GB
		BaseRxRate:     8500000,      // 8.5 MB/s
		BaseTxRate:     1200000,      // 1.2 MB/s
		UptimeSec:      5200000,      // 60 days
		AvgLatency:     36.8,
		LatencyJitter:  3.5,
		AvgLoss:        0.0,
		TelecomRoute:   "NTT 骨干网",
		TelecomLatency: 45,
		TelecomSpeed:   580.0,
		UnicomRoute:    "联通 169 骨干网",
		UnicomLatency:  48,
		UnicomSpeed:    620.0,
		MobileRoute:    "移动 CMI",
		MobileLatency:  42,
		MobileSpeed:    540.0,
	},
	{
		ID:             "demo-node-hk-01",
		Name:           "🇭🇰 香港 CN2+BGP 01",
		ClientAddress:  "103.218.240.1:19994",
		IP:             "103.218.240.1",
		OS:             "Debian 12 (bookworm)",
		Arch:           "amd64",
		Provider:       "DMIT",
		ProviderURL:    "https://www.dmit.io",
		Price:          "28.88",
		PricePeriod:    "quarter",
		Currency:       "USD",
		TrafficPlan:    "1500",
		Bandwidth:      "1 Gbps",
		ExpiresOn:      "2027-02-28",
		CPUName:        "AMD EPYC 7443P 24-Core",
		CPUCores:       2,
		BaseCPU:        22.8,
		MemTotal:       4294967296,  // 4 GB
		MemUsed:        2147483648,  // 50%
		SwapTotal:      2147483648,  // 2 GB
		SwapUsed:       52428800,    // 50 MB
		DiskTotal:      42949672960, // 40 GB
		DiskUsed:       17179869184, // 16 GB
		BaseRxRate:     18200000,    // 18.2 MB/s
		BaseTxRate:     3800000,     // 3.8 MB/s
		UptimeSec:      2100000,     // 24.3 days
		AvgLatency:     18.2,
		LatencyJitter:  1.2,
		AvgLoss:        0.0,
		TelecomRoute:   "电信 CN2 GIA 双程",
		TelecomLatency: 15,
		TelecomSpeed:   920.0,
		UnicomRoute:    "联通 9929 优质网",
		UnicomLatency:  18,
		UnicomSpeed:    880.0,
		MobileRoute:    "移动 CMI 直连",
		MobileLatency:  16,
		MobileSpeed:    860.0,
	},
	{
		ID:             "demo-node-lax-01",
		Name:           "🇺🇸 洛杉矶 CN2 GIA",
		ClientAddress:  "23.95.220.1:19994",
		IP:             "23.95.220.1",
		OS:             "Debian 12 (bookworm)",
		Arch:           "amd64",
		Provider:       "BandwagonHost",
		ProviderURL:    "https://bandwagonhost.com",
		Price:          "99.99",
		PricePeriod:    "year",
		Currency:       "USD",
		TrafficPlan:    "1000",
		Bandwidth:      "2.5 Gbps",
		ExpiresOn:      "2027-11-11",
		CPUName:        "Intel Xeon Platinum 8375C",
		CPUCores:       2,
		BaseCPU:        16.5,
		MemTotal:       2147483648,  // 2 GB
		MemUsed:        1395864371,  // 65%
		SwapTotal:      2147483648,  // 2 GB
		SwapUsed:       157286400,   // 150 MB
		DiskTotal:      42949672960, // 40 GB
		DiskUsed:       21474836480, // 20 GB
		BaseRxRate:     6400000,     // 6.4 MB/s
		BaseTxRate:     950000,      // 950 KB/s
		UptimeSec:      8500000,     // 98.3 days
		AvgLatency:     132.5,
		LatencyJitter:  4.5,
		AvgLoss:        0.0,
		TelecomRoute:   "电信 CN2 GIA (AS4809)",
		TelecomLatency: 128,
		TelecomSpeed:   760.0,
		UnicomRoute:    "联通 AS9929 优质网",
		UnicomLatency:  135,
		UnicomSpeed:    710.0,
		MobileRoute:    "移动 CMI 转 NTT",
		MobileLatency:  142,
		MobileSpeed:    650.0,
	},
	{
		ID:             "demo-node-nyc-01",
		Name:           "🇺🇸 纽约 AWS us-east-1",
		ClientAddress:  "198.51.100.1:19994",
		IP:             "198.51.100.1",
		OS:             "Amazon Linux 2023",
		Arch:           "amd64",
		Provider:       "Amazon AWS",
		ProviderURL:    "https://aws.amazon.com",
		Price:          "18.50",
		PricePeriod:    "month",
		Currency:       "USD",
		TrafficPlan:    "5000",
		Bandwidth:      "5 Gbps",
		ExpiresOn:      "2027-08-01",
		CPUName:        "Intel Xeon Platinum 8259CL",
		CPUCores:       4,
		BaseCPU:        11.2,
		MemTotal:       17179869184, // 16 GB
		MemUsed:        4724464025,  // 27.5%
		SwapTotal:      0,
		SwapUsed:       0,
		DiskTotal:      85899345920, // 80 GB
		DiskUsed:       25769803776, // 24 GB
		BaseRxRate:     3200000,     // 3.2 MB/s
		BaseTxRate:     580000,      // 580 KB/s
		UptimeSec:      3600000,     // 41.6 days
		AvgLatency:     178.4,
		LatencyJitter:  7.2,
		AvgLoss:        0.1,
		TelecomRoute:   "163 骨干网直连",
		TelecomLatency: 185,
		TelecomSpeed:   450.0,
		UnicomRoute:    "联通 4837 骨干网",
		UnicomLatency:  175,
		UnicomSpeed:    510.0,
		MobileRoute:    "移动普通国际线路",
		MobileLatency:  188,
		MobileSpeed:    420.0,
	},
	{
		ID:             "demo-node-fra-01",
		Name:           "🇩🇪 法兰克福 Hetzner AX41",
		ClientAddress:  "88.198.1.1:19994",
		IP:             "88.198.1.1",
		OS:             "Ubuntu 24.04 LTS",
		Arch:           "amd64",
		Provider:       "Hetzner",
		ProviderURL:    "https://www.hetzner.com",
		Price:          "42.00",
		PricePeriod:    "month",
		Currency:       "EUR",
		TrafficPlan:    "20000",
		Bandwidth:      "1 Gbps",
		ExpiresOn:      "2027-12-31",
		CPUName:        "AMD Ryzen 5 3600 6-Core",
		CPUCores:       12,
		BaseCPU:        28.4,
		MemTotal:       68719476736, // 64 GB
		MemUsed:        22011703623, // 32%
		SwapTotal:      34359738368, // 32 GB
		SwapUsed:       0,
		DiskTotal:      536870912000, // 500 GB
		DiskUsed:       161061273600, // 150 GB
		BaseRxRate:     24500000,     // 24.5 MB/s
		BaseTxRate:     6800000,      // 6.8 MB/s
		UptimeSec:      15400000,     // 178 days
		AvgLatency:     142.0,
		LatencyJitter:  5.6,
		AvgLoss:        0.0,
		TelecomRoute:   "电信 CN2 欧洲直达",
		TelecomLatency: 138,
		TelecomSpeed:   790.0,
		UnicomRoute:    "联通 9929 德法骨干",
		UnicomLatency:  140,
		UnicomSpeed:    820.0,
		MobileRoute:    "移动 CMI 欧洲节点",
		MobileLatency:  148,
		MobileSpeed:    710.0,
	},
	{
		ID:             "demo-node-sin-01",
		Name:           "🇸🇬 新加坡 GCP Southeast",
		ClientAddress:  "128.199.200.1:19994",
		IP:             "128.199.200.1",
		OS:             "Debian 11 (bullseye)",
		Arch:           "amd64",
		Provider:       "Google Cloud",
		ProviderURL:    "https://cloud.google.com",
		Price:          "15.00",
		PricePeriod:    "month",
		Currency:       "USD",
		TrafficPlan:    "2000",
		Bandwidth:      "2 Gbps",
		ExpiresOn:      "2027-06-30",
		CPUName:        "Intel Xeon 2.20GHz",
		CPUCores:       2,
		BaseCPU:        15.6,
		MemTotal:       4294967296,  // 4 GB
		MemUsed:        1932735283,  // 45%
		SwapTotal:      2147483648,  // 2 GB
		SwapUsed:       20971520,    // 20 MB
		DiskTotal:      53687091200, // 50 GB
		DiskUsed:       18790481920, // 17.5 GB
		BaseRxRate:     9800000,     // 9.8 MB/s
		BaseTxRate:     1900000,     // 1.9 MB/s
		UptimeSec:      4200000,     // 48.6 days
		AvgLatency:     54.2,
		LatencyJitter:  2.5,
		AvgLoss:        0.0,
		TelecomRoute:   "CN2 GIA 亚太环网",
		TelecomLatency: 52,
		TelecomSpeed:   810.0,
		UnicomRoute:    "联通 9929 新加坡节点",
		UnicomLatency:  56,
		UnicomSpeed:    760.0,
		MobileRoute:    "移动 CMI 直连",
		MobileLatency:  51,
		MobileSpeed:    840.0,
	},
	{
		ID:             "demo-node-lon-01",
		Name:           "🇬🇧 伦敦 OVH VPS",
		ClientAddress:  "51.15.1.1:19994",
		IP:             "51.15.1.1",
		OS:             "Alpine Linux 3.19",
		Arch:           "amd64",
		Provider:       "OVHcloud",
		ProviderURL:    "https://www.ovhcloud.com",
		Price:          "7.50",
		PricePeriod:    "month",
		Currency:       "USD",
		TrafficPlan:    "1000",
		Bandwidth:      "1 Gbps",
		ExpiresOn:      "2026-12-01",
		CPUName:        "Intel Xeon E5-2687W v3",
		CPUCores:       2,
		BaseCPU:        9.8,
		MemTotal:       4294967296, // 4 GB
		MemUsed:        858993459,  // 20%
		SwapTotal:      1073741824, // 1 GB
		SwapUsed:       0,
		DiskTotal:      42949672960, // 40 GB
		DiskUsed:       6442450944,  // 6 GB
		BaseRxRate:     4200000,     // 4.2 MB/s
		BaseTxRate:     850000,      // 850 KB/s
		UptimeSec:      980000,      // 11.3 days
		AvgLatency:     168.5,
		LatencyJitter:  6.8,
		AvgLoss:        0.1,
		TelecomRoute:   "AS4837 伦敦接入",
		TelecomLatency: 165,
		TelecomSpeed:   580.0,
		UnicomRoute:    "联通 4837 直连",
		UnicomLatency:  168,
		UnicomSpeed:    540.0,
		MobileRoute:    "移动 CMI 伦敦机房",
		MobileLatency:  172,
		MobileSpeed:    490.0,
	},
}

func (s *Server) EnsurePingTasks(ctx context.Context) error {
	_ = s.store.initPingTasksTable(ctx)
	var taskCount int
	_ = s.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ping_tasks`).Scan(&taskCount)
	if taskCount == 0 {
		now := time.Now().Unix()
		defaultTasks := []struct {
			name   string
			target string
		}{
			{name: "江苏电信", target: "218.2.135.1:53"},
			{name: "江苏联通", target: "221.6.4.66:53"},
			{name: "江苏移动", target: "221.131.143.69:53"},
		}
		for idx, dt := range defaultTasks {
			_, _ = s.store.db.ExecContext(ctx, `INSERT INTO ping_tasks (name, target, interval, auto_join, node_ids, sort, created_at) VALUES (?, ?, 60, 1, '[]', ?, ?)`, dt.name, dt.target, idx, now)
		}
	}
	return nil
}

// EnsureDemoNodes seeds high-fidelity demo nodes into the store if no nodes exist.
// It only adds sample content: operator credentials and two-factor enrollment
// belong to the account itself and must survive a restart untouched.
func (s *Server) EnsureDemoNodes(ctx context.Context) error {
	_ = s.EnsurePingTasks(ctx)
	_ = s.EnsureDemoFleetData(ctx)
	existing, err := s.store.ListNodes(ctx)
	if err != nil {
		return fmt.Errorf("check existing nodes: %w", err)
	}
	var metricCount int
	_ = s.store.db.QueryRowContext(ctx, "SELECT count(*) FROM node_metric_points").Scan(&metricCount)
	var gxCount int
	_ = s.store.db.QueryRowContext(ctx, "SELECT count(*) FROM node_latency_points WHERE probe_id = 'gx-telecom'").Scan(&gxCount)
	if len(existing) > 0 && metricCount > 0 && gxCount > 0 {
		return nil
	}
	log.Printf("[polaris] 初始化/同步 8 个真实规格监控演示服务器及历史时序数据...")

	now := time.Now().Unix()
	hourSec := int64(3600)

	for _, d := range demoNodes {
		// 1. Generate unique deterministic public key
		h := sha256.Sum256([]byte("polaris-demo-node-key-" + d.ID))
		pubKey := h[:]

		ipAddressesJSON, _ := json.Marshal([]string{d.IP})
		aliasesJSON, _ := json.Marshal(map[string]string{d.IP: d.Name + " 出口"})

		ddnsEnabled := 0
		ddnsDomain := ""
		ddnsDomains := "[]"
		clientAddr := d.ClientAddress
		if d.ID == "demo-node-lon-01" {
			ddnsEnabled = 1
			ddnsDomain = "lon.polaris-node.net"
			ddnsDomains = `["lon.polaris-node.net"]`
			clientAddr = "lon.polaris-node.net"
		}

		// Insert into nodes
		_, err := s.store.db.ExecContext(ctx, `INSERT OR REPLACE INTO nodes (
			id, name, public_key, client_address, agent_version, os, os_name, architecture, sing_box_version,
			capabilities, last_seen_at, ddns_enabled, ddns_domain, ddns_domains, last_observed_ip, ip_addresses, ip_aliases,
			expires_on, price, price_period, currency, provider, provider_url, traffic_plan, bandwidth, created_at
		) VALUES (?, ?, ?, ?, '1.2.0', ?, ?, ?, '1.10.0', '基础代理,TCP/UDP探测', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			d.ID, d.Name, pubKey, clientAddr, d.OS, d.OS, d.Arch, now, ddnsEnabled, ddnsDomain, ddnsDomains, d.IP,
			string(ipAddressesJSON), string(aliasesJSON), d.ExpiresOn, d.Price, d.PricePeriod,
			d.Currency, d.Provider, d.ProviderURL, d.TrafficPlan, d.Bandwidth, now-86400*30,
		)
		if err != nil {
			return fmt.Errorf("insert demo node %s: %w", d.ID, err)
		}

		// 2. Generate 24-hour latency track
		hours := make([]storedLatencyHour, 24)
		for i := 0; i < 24; i++ {
			ts := now - int64(23-i)*hourSec
			// Add natural jitter around average
			offset := (rand.Float64() - 0.5) * d.LatencyJitter * 2
			lat := d.AvgLatency + offset
			if lat < 5 {
				lat = 5
			}
			loss := d.AvgLoss
			if rand.Float64() < 0.05 {
				loss = 0.5
			}
			hours[i] = storedLatencyHour{
				TS:      ts,
				Latency: &lat,
				Loss:    &loss,
			}
		}

		avgLat := d.AvgLatency
		avgLoss := d.AvgLoss
		jitter := d.LatencyJitter
		minLat := d.AvgLatency - d.LatencyJitter
		maxLat := d.AvgLatency + d.LatencyJitter*2

		latencyReport := storedLatency{
			AvgLatency: &avgLat,
			AvgLoss:    &avgLoss,
			Jitter:     &jitter,
			Min:        &minLat,
			Max:        &maxLat,
			Hours:      hours,
			Probes: []storedProbeStat{
				{
					ID:         1,
					Name:       "广西电信",
					Latency:    &avgLat,
					Jitter:     &jitter,
					Loss:       avgLoss,
					AvgLatency: &avgLat,
					AvgLoss:    &avgLoss,
					Hours:      hours,
				},
				{
					ID:         2,
					Name:       "广西联通",
					Latency:    &avgLat,
					Jitter:     &jitter,
					Loss:       avgLoss,
					AvgLatency: &avgLat,
					AvgLoss:    &avgLoss,
					Hours:      hours,
				},
				{
					ID:         3,
					Name:       "广西移动",
					Latency:    &avgLat,
					Jitter:     &jitter,
					Loss:       avgLoss,
					AvgLatency: &avgLat,
					AvgLoss:    &avgLoss,
					Hours:      hours,
				},
			},
		}

		// 3. Stored metrics report
		metrics := storedMetrics{
			CollectedAt: time.Now().UTC().Format(time.RFC3339),
			Host: &HostResources{
				CPUPercent:         d.BaseCPU,
				LoadAvg:            [3]float64{d.BaseCPU / 100.0 * float64(d.CPUCores), (d.BaseCPU + 2) / 100.0 * float64(d.CPUCores), (d.BaseCPU - 1) / 100.0 * float64(d.CPUCores)},
				MemoryTotal:        d.MemTotal,
				MemoryUsed:         d.MemUsed,
				SwapTotal:          d.SwapTotal,
				SwapUsed:           d.SwapUsed,
				DiskTotal:          d.DiskTotal,
				DiskUsed:           d.DiskUsed,
				UptimeSeconds:      d.UptimeSec,
				AgentUptimeSeconds: d.UptimeSec,
				TCP:                uint32(120 + rand.Intn(280)),
				UDP:                uint32(25 + rand.Intn(60)),
				Procs:              uint32(95 + rand.Intn(40)),
				NetRxRate:          d.BaseRxRate,
				NetTxRate:          d.BaseTxRate,
				OSName:             d.OS,
				Kernel:             "Linux 6.8.0-generic",
				Virt:               "KVM",
				CPUName:            d.CPUName,
				CPUCores:           d.CPUCores,
			},
			Latency:      &latencyReport,
			CurrentMonth: time.Now().UTC().Format("2006-01"),
			Proxy: map[string]uint64{
				"received_bytes":         uint64(rand.Int63n(800000000000) + 200000000000),  // 200GB - 1TB
				"sent_bytes":             uint64(rand.Int63n(3000000000000) + 800000000000), // 800GB - 3.8TB
				"monthly_received_bytes": uint64(rand.Int63n(30000000000) + 3000000000),     // 3GB - 33GB
				"monthly_sent_bytes":     uint64(rand.Int63n(90000000000) + 10000000000),    // 10GB - 100GB
			},
			Health: &storedHealth{
				Status:            "healthy",
				SingBoxService:    "active",
				ClashAPIAvailable: true,
				TrafficAvailable:  true,
			},
		}

		reportBytes, _ := json.Marshal(metrics)
		_, err = s.store.db.ExecContext(ctx, `INSERT OR REPLACE INTO node_metrics (node_id, report, updated_at) VALUES (?, ?, ?)`,
			d.ID, string(reportBytes), now,
		)
		if err != nil {
			return fmt.Errorf("insert node_metrics for %s: %w", d.ID, err)
		}

		// 4. Seed historical metric points for resource charts & ping history
		for i := 0; i < 24; i++ {
			pointTS := now - int64(23-i)*hourSec
			cpuVal := d.BaseCPU + (rand.Float64()-0.5)*3.0
			rxVal := uint64(float64(d.BaseRxRate) * (0.8 + rand.Float64()*0.4))
			txVal := uint64(float64(d.BaseTxRate) * (0.8 + rand.Float64()*0.4))
			_, _ = s.store.db.ExecContext(ctx, `INSERT OR REPLACE INTO node_metric_points (
				node_id, ts, cpu, mem_used, mem_total, disk_used, disk_total, net_rx, net_tx
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				d.ID, pointTS, cpuVal, d.MemUsed, d.MemTotal, d.DiskUsed, d.DiskTotal, rxVal, txVal,
			)

			// Seed latency points for 3 probes
			for _, probe := range []struct {
				id   string
				name string
				tgt  string
				base float64
			}{
				{id: "gx-telecom", name: "广西电信", tgt: "202.103.224.68:53", base: d.AvgLatency},
				{id: "gx-unicom", name: "广西联通", tgt: "221.7.128.68:53", base: d.AvgLatency + 3},
				{id: "gx-mobile", name: "广西移动", tgt: "211.138.242.18:53", base: d.AvgLatency + 6},
			} {
				latVal := int(probe.base + (rand.Float64()-0.5)*d.LatencyJitter*2)
				if latVal < 5 {
					latVal = 5
				}
				_, _ = s.store.db.ExecContext(ctx, `INSERT OR REPLACE INTO node_latency_points (
					node_id, probe_id, probe_name, target, ts, latency_ms
				) VALUES (?, ?, ?, ?, ?, ?)`,
					d.ID, probe.id, probe.name, probe.tgt, pointTS, latVal,
				)
			}
		}
	}

	// 6. Seed default ping tasks so administrators can immediately see and manage presets
	_ = s.store.initPingTasksTable(ctx)
	var taskCount int
	_ = s.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ping_tasks`).Scan(&taskCount)
	if taskCount == 0 {
		defaultTasks := []struct {
			name   string
			target string
		}{
			{name: "江苏电信", target: "218.2.135.1:53"},
			{name: "江苏联通", target: "221.6.4.66:53"},
			{name: "江苏移动", target: "221.131.143.69:53"},
		}
		for idx, dt := range defaultTasks {
			_, _ = s.store.db.ExecContext(ctx, `INSERT INTO ping_tasks (name, target, interval, auto_join, node_ids, sort, created_at) VALUES (?, ?, 60, 1, '[]', ?, ?)`, dt.name, dt.target, idx, now)
		}
	}

	log.Printf("[polaris] 8 个演示服务器及其 24 小时延迟热力图与网速数据初始化完成！")
	_ = s.EnsureDemoFleetData(ctx)
	return nil
}

// EnsureDemoFleetData seeds comprehensive, realistic test data for all management views:
// Outbounds, Listeners & Endpoints, Rule Providers, Proxy Groups, Client Configs, Subscriptions,
// Route Rules, Historical Connections & Popular Devices, Audit Events & Tasks.
func (s *Server) EnsureDemoFleetData(ctx context.Context) error {
	now := time.Now().Unix()

	// 1. Get or create operator ID
	var opID string
	_ = s.store.db.QueryRowContext(ctx, `SELECT id FROM operators ORDER BY created_at ASC LIMIT 1`).Scan(&opID)
	if opID == "" {
		opID = "op-default-admin"
		_, _ = s.store.db.ExecContext(ctx, `INSERT OR IGNORE INTO operators (id, username, password_hash, role, must_change_password, created_at, updated_at) VALUES (?, 'admin', 'pbkdf2:dummy', 'admin', 0, ?, ?)`, opID, now, now)
	}

	// 2. Outbounds (上网出口)
	var outboundCount int
	_ = s.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbounds`).Scan(&outboundCount)
	if outboundCount == 0 {
		demoOutbounds := []struct {
			id, name, obType, server string
			port                     uint16
			username, password       string
			expiresAt                int64
		}{
			{id: "ob-cf-warp-v4", name: "Cloudflare WARP (IPv4 官方出口)", obType: "socks", server: "162.159.192.1", port: 2408, expiresAt: now + 86400*365},
			{id: "ob-cf-warp-v6", name: "Cloudflare WARP (IPv6 官方出口)", obType: "socks", server: "2606:4700:d0::a29f:c001", port: 2408, expiresAt: now + 86400*365},
			{id: "ob-hk-socks5", name: "🇭🇰 香港落地备用 Socks5", obType: "socks", server: "45.125.10.88", port: 10808, username: "proxy_user", expiresAt: now + 86400*180},
			{id: "ob-tyo-http", name: "🇯🇵 东京备用 HTTP 代理", obType: "http", server: "139.162.65.1", port: 8080, username: "tokyo_gw", expiresAt: now + 86400*90},
		}
		for _, ob := range demoOutbounds {
			encCreds, _ := s.store.encryptOutboundSecret(ob.password)
			_, _ = s.store.db.ExecContext(ctx, `INSERT INTO outbounds (id, name, type, server, server_port, username, credentials, enabled, expires_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)`,
				ob.id, ob.name, ob.obType, ob.server, ob.port, ob.username, encCreds, ob.expiresAt, now, now)
		}
	}

	// 3. Listeners & Endpoints (接入服务)
	var rkCount int
	_ = s.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_reality_keys`).Scan(&rkCount)
	if rkCount == 0 {
		_, _, _ = s.store.CreateRealityKey(ctx, "默认 Reality 密钥")
	}

	var listenerCount int
	_ = s.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM listeners`).Scan(&listenerCount)
	if listenerCount == 0 {
		demoListeners := []struct {
			id, nodeID, name, domain, protocol, network string
			port, backendPort                           uint16
			spec                                        string
			outboundID                                  string
			endpoints                                   []struct {
				id, name, alias string
				creds           EndpointCredentials
			}
		}{
			{
				id: "lis-tyo-vless", nodeID: "demo-node-tyo-01", name: "东京 VLESS Reality 高速节点", domain: "tyo.polaris-node.net",
				protocol: "vless", network: "tcp", port: 443, backendPort: 443, outboundID: "",
				spec: `{"protocol":"vless","network":"tcp","tls":{"enabled":true},"reality":{"enabled":true,"handshake_server":"gateway.icloud.com","handshake_port":443,"short_ids":["16a8b29c"]}}`,
				endpoints: []struct {
					id, name, alias string
					creds           EndpointCredentials
				}{
					{id: "ep-tyo-mac", name: "MacBook Pro M3 Max", alias: "东京 · 主力办公", creds: EndpointCredentials{UUID: "a1b2c3d4-e5f6-7890-abcd-ef1234567890", Flow: "xtls-rprx-vision"}},
					{id: "ep-tyo-iphone", name: "iPhone 16 Pro", alias: "东京 · 移动随身", creds: EndpointCredentials{UUID: "b2c3d4e5-f6a7-8901-bcde-f12345678901", Flow: "xtls-rprx-vision"}},
				},
			},
			{
				id: "lis-hk-hy2", nodeID: "demo-node-hk-01", name: "香港 Hysteria 2 双程低延迟", domain: "hk.polaris-node.net",
				protocol: "hysteria2", network: "udp", port: 8443, backendPort: 8443, outboundID: "",
				spec: `{"protocol":"hysteria2","network":"udp","tls":{"enabled":true,"alpn":["h3"]}}`,
				endpoints: []struct {
					id, name, alias string
					creds           EndpointCredentials
				}{
					{id: "ep-hk-win", name: "Windows 11 工作站", alias: "香港 · 游戏与流媒体", creds: EndpointCredentials{Password: "polaris_hk_gaming_sec_8899"}},
					{id: "ep-hk-ipad", name: "iPad Pro 13", alias: "香港 · 平板外勤", creds: EndpointCredentials{Password: "polaris_hk_ipad_sec_6677"}},
				},
			},
			{
				id: "lis-lax-vless", nodeID: "demo-node-lax-01", name: "洛杉矶 CN2 GIA 美区专用", domain: "lax.polaris-node.net",
				protocol: "vless", network: "tcp", port: 443, backendPort: 443, outboundID: "ob-cf-warp-v4",
				spec: `{"protocol":"vless","network":"tcp","tls":{"enabled":true},"reality":{"enabled":true,"handshake_server":"www.apple.com","handshake_port":443,"short_ids":["3a4f5b6c"]}}`,
				endpoints: []struct {
					id, name, alias string
					creds           EndpointCredentials
				}{
					{id: "ep-lax-router", name: "Homelab 软路由", alias: "美区 · 常用服务", creds: EndpointCredentials{UUID: "c3d4e5f6-a7b8-9012-cdef-123456789012", Flow: "xtls-rprx-vision"}},
					{id: "ep-lax-appletv", name: "Apple TV 4K", alias: "美区 · 影音流媒体", creds: EndpointCredentials{UUID: "d4e5f6a7-b8c9-0123-defa-234567890123", Flow: "xtls-rprx-vision"}},
				},
			},
			{
				id: "lis-fra-hy2", nodeID: "demo-node-fra-01", name: "法兰克福 Hetzner 欧洲专线", domain: "fra.polaris-node.net",
				protocol: "hysteria2", network: "udp", port: 9443, backendPort: 9443, outboundID: "",
				spec: `{"protocol":"hysteria2","network":"udp","tls":{"enabled":true,"alpn":["h3"]}}`,
				endpoints: []struct {
					id, name, alias string
					creds           EndpointCredentials
				}{
					{id: "ep-fra-backup", name: "European VPS Client", alias: "欧洲 · 备份与同步", creds: EndpointCredentials{Password: "polaris_fra_sec_3344"}},
				},
			},
		}

		for _, l := range demoListeners {
			_, _ = s.store.db.ExecContext(ctx, `INSERT INTO listeners (id, node_id, name, connection_domain, protocol, network, listen_address, port, backend_port, enabled, spec, outbound_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, '0.0.0.0', ?, ?, 1, ?, ?, ?, ?)`,
				l.id, l.nodeID, l.name, l.domain, l.protocol, l.network, l.port, l.backendPort, l.spec, l.outboundID, now, now)

			for _, ep := range l.endpoints {
				credBytes, _ := json.Marshal(ep.creds)
				encCred, _ := security.Encrypt(s.store.masterKey, credBytes)
				_, _ = s.store.db.ExecContext(ctx, `INSERT INTO endpoints (id, listener_id, name, alias, credentials, enabled, outbound_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 1, '', ?, ?)`,
					ep.id, l.id, ep.name, ep.alias, encCred, now, now)
			}
		}
	}

	// 4. Mihomo Rule Providers (规则供应商)
	var rpCount int
	_ = s.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mihomo_rule_providers`).Scan(&rpCount)
	if rpCount == 0 {
		demoProviders := []struct {
			id, name, behavior, format, url, path string
			interval                              int
			proxy                                 string
		}{
			{id: "rp-cn-domain", name: "Geosite-CN", behavior: "domain", format: "mrs", url: "https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/meta/geo/geosite/cn.mrs", path: "./ruleset/cn-domain.mrs", interval: 86400, proxy: "DIRECT"},
			{id: "rp-cn-ip", name: "GeoIP-CN", behavior: "ipcidr", format: "mrs", url: "https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/meta/geo/geoip/cn.mrs", path: "./ruleset/cn-ip.mrs", interval: 86400, proxy: "DIRECT"},
			{id: "rp-openai", name: "Geosite-OpenAI", behavior: "classical", format: "yaml", url: "https://raw.githubusercontent.com/Loyalsoldier/clash-rules/release/openai.txt", path: "./ruleset/openai.yaml", interval: 86400, proxy: "DIRECT"},
			{id: "rp-youtube", name: "Geosite-YouTube", behavior: "classical", format: "yaml", url: "https://raw.githubusercontent.com/Loyalsoldier/clash-rules/release/youtube.txt", path: "./ruleset/youtube.yaml", interval: 86400, proxy: "DIRECT"},
			{id: "rp-github", name: "Geosite-GitHub", behavior: "classical", format: "yaml", url: "https://raw.githubusercontent.com/Loyalsoldier/clash-rules/release/github.txt", path: "./ruleset/github.yaml", interval: 86400, proxy: "DIRECT"},
		}
		for _, rp := range demoProviders {
			_, _ = s.store.db.ExecContext(ctx, `INSERT INTO mihomo_rule_providers (id, name, behavior, format, url, path, interval, proxy, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				rp.id, rp.name, rp.behavior, rp.format, rp.url, rp.path, rp.interval, rp.proxy, now, now)
		}
	}

	// 5. Mihomo Proxy Groups (代理分组)
	var pgCount int
	_ = s.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mihomo_proxy_groups`).Scan(&pgCount)
	if pgCount == 0 {
		demoGroups := []struct {
			id, name, strategy string
			members            []MihomoGroupMember
		}{
			{
				id: "pg-select", name: "🚀 节点选择", strategy: "select",
				members: []MihomoGroupMember{
					{Kind: "group", ID: "pg-auto"},
					{Kind: "group", ID: "pg-hk"},
					{Kind: "group", ID: "pg-jp"},
					{Kind: "group", ID: "pg-us"},
					{Kind: "endpoint", ID: "ep-hk-win"},
					{Kind: "endpoint", ID: "ep-tyo-mac"},
					{Kind: "endpoint", ID: "ep-lax-router"},
				},
			},
			{
				id: "pg-auto", name: "♻️ 自动选择", strategy: "url-test",
				members: []MihomoGroupMember{
					{Kind: "endpoint", ID: "ep-hk-win"},
					{Kind: "endpoint", ID: "ep-tyo-mac"},
					{Kind: "endpoint", ID: "ep-lax-router"},
					{Kind: "endpoint", ID: "ep-fra-backup"},
				},
			},
			{
				id: "pg-hk", name: "🇭🇰 香港专线", strategy: "fallback",
				members: []MihomoGroupMember{
					{Kind: "endpoint", ID: "ep-hk-win"},
					{Kind: "endpoint", ID: "ep-hk-ipad"},
				},
			},
			{
				id: "pg-jp", name: "🇯🇵 日本低延迟", strategy: "url-test",
				members: []MihomoGroupMember{
					{Kind: "endpoint", ID: "ep-tyo-mac"},
					{Kind: "endpoint", ID: "ep-tyo-iphone"},
				},
			},
			{
				id: "pg-us", name: "🇺🇸 美区流媒体", strategy: "select",
				members: []MihomoGroupMember{
					{Kind: "endpoint", ID: "ep-lax-router"},
					{Kind: "endpoint", ID: "ep-lax-appletv"},
				},
			},
		}

		for _, g := range demoGroups {
			membersJSON, _ := json.Marshal(g.members)
			endpointIDs := []string{}
			for _, m := range g.members {
				if m.Kind == "endpoint" {
					endpointIDs = append(endpointIDs, m.ID)
				}
			}
			endpointsJSON, _ := json.Marshal(endpointIDs)
			_, _ = s.store.db.ExecContext(ctx, `INSERT INTO mihomo_proxy_groups (id, name, strategy, endpoint_ids, aliases_json, members_json, created_at, updated_at) VALUES (?, ?, ?, ?, '{}', ?, ?, ?)`,
				g.id, g.name, g.strategy, string(endpointsJSON), string(membersJSON), now, now)
		}
	}

	// 6. Mihomo Client Configs (客户端配置 / 订阅)
	var cfgCount int
	_ = s.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mihomo_client_configs_v3`).Scan(&cfgCount)
	if cfgCount == 0 {
		demoConfigs := []struct {
			id, name        string
			proxyGroupIDs   []string
			ruleProviderIDs []string
			rules           []MihomoRule
		}{
			{
				id: "cfg-general", name: "全平台通用标准配置 (Clash / Mihomo)",
				proxyGroupIDs:   []string{"pg-select", "pg-auto", "pg-hk", "pg-jp", "pg-us"},
				ruleProviderIDs: []string{"rp-cn-domain", "rp-cn-ip", "rp-openai", "rp-youtube", "rp-github"},
				rules: []MihomoRule{
					{Type: "RULE-SET", Value: "Geosite-OpenAI", Action: "🚀 节点选择"},
					{Type: "RULE-SET", Value: "Geosite-YouTube", Action: "🇺🇸 美区流媒体"},
					{Type: "RULE-SET", Value: "Geosite-GitHub", Action: "🚀 节点选择"},
					{Type: "RULE-SET", Value: "Geosite-CN", Action: "DIRECT"},
					{Type: "RULE-SET", Value: "GeoIP-CN", Action: "DIRECT"},
					{Type: "MATCH", Action: "🚀 节点选择"},
				},
			},
			{
				id: "cfg-vip-game", name: "VIP 游戏低延迟加速专线",
				proxyGroupIDs:   []string{"pg-hk", "pg-jp", "pg-auto"},
				ruleProviderIDs: []string{"rp-cn-domain"},
				rules: []MihomoRule{
					{Type: "DOMAIN-SUFFIX", Value: "steampowered.com", Action: "🇭🇰 香港专线"},
					{Type: "DOMAIN-SUFFIX", Value: "steamcommunity.com", Action: "🇭🇰 香港专线"},
					{Type: "DOMAIN-SUFFIX", Value: "epicgames.com", Action: "🇯🇵 日本低延迟"},
					{Type: "RULE-SET", Value: "Geosite-CN", Action: "DIRECT"},
					{Type: "MATCH", Action: "🇭🇰 香港专线"},
				},
			},
			{
				id: "cfg-mobile", name: "移动外勤办公轻量订阅",
				proxyGroupIDs:   []string{"pg-select", "pg-auto"},
				ruleProviderIDs: []string{"rp-cn-domain", "rp-openai"},
				rules: []MihomoRule{
					{Type: "RULE-SET", Value: "Geosite-OpenAI", Action: "🚀 节点选择"},
					{Type: "RULE-SET", Value: "Geosite-CN", Action: "DIRECT"},
					{Type: "MATCH", Action: "🚀 节点选择"},
				},
			},
		}

		for _, cfg := range demoConfigs {
			groupsJSON, _ := json.Marshal(cfg.proxyGroupIDs)
			rulesStruct := mihomoClientRulesV3{
				RuleProviderIDs: cfg.ruleProviderIDs,
				Rules:           cfg.rules,
			}
			rulesJSON, _ := json.Marshal(rulesStruct)
			dnsStruct := mihomoClientDNSV3{Mode: "form", DNS: defaultMihomoClientDNS()}
			dnsJSON, _ := json.Marshal(dnsStruct)

			token, _ := security.RandomToken(32)
			tokenHash := security.TokenHash(token)
			encToken, _ := security.Encrypt(s.store.masterKey, []byte(token))

			_, _ = s.store.db.ExecContext(ctx, `INSERT INTO mihomo_client_configs_v3
				(id, name, groups_json, rule_mode, rules_json, dns_json, subscription_token_hash, subscription_token_encrypted, enabled, created_at, updated_at)
				VALUES (?, ?, ?, 'table', ?, ?, ?, ?, 1, ?, ?)`,
				cfg.id, cfg.name, string(groupsJSON), string(rulesJSON), string(dnsJSON), tokenHash, encToken, now, now)
		}
	}

	// 7. Subscription Access Logs (订阅访问记录)
	var subLogCount int
	_ = s.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM subscription_access_logs`).Scan(&subLogCount)
	if subLogCount == 0 {
		demoLogs := []struct {
			cfgID, cfgName, ip, location, ua string
			offsetSec                        int64
		}{
			{"cfg-general", "全平台通用标准配置 (Clash / Mihomo)", "114.248.15.82", "中国 北京", "ClashVerge/v1.7.7", 300},
			{"cfg-general", "全平台通用标准配置 (Clash / Mihomo)", "117.136.38.109", "中国 广东 广州", "ClashMetaForAndroid/2.10.1", 1200},
			{"cfg-vip-game", "VIP 游戏低延迟加速专线", "183.14.30.22", "中国 广东 深圳", "Mihomo.Party/1.5.8", 2400},
			{"cfg-general", "全平台通用标准配置 (Clash / Mihomo)", "222.186.42.17", "中国 江苏 南京", "Surge/2890", 5400},
			{"cfg-mobile", "移动外勤办公轻量订阅", "116.228.89.201", "中国 上海", "Shadowrocket/2242", 7200},
			{"cfg-general", "全平台通用标准配置 (Clash / Mihomo)", "60.191.45.19", "中国 浙江 杭州", "ClashVerge/v1.7.7", 10800},
			{"cfg-vip-game", "VIP 游戏低延迟加速专线", "125.71.229.6", "中国 四川 成都", "Mihomo.Party/1.5.8", 18000},
			{"cfg-general", "全平台通用标准配置 (Clash / Mihomo)", "113.57.244.31", "中国 湖北 武汉", "sing-box/1.9.3", 25200},
		}
		for _, logItem := range demoLogs {
			logID, _ := newID()
			_, _ = s.store.db.ExecContext(ctx, `INSERT INTO subscription_access_logs (id, config_id, config_name, ip, location, user_agent, accessed_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				logID, logItem.cfgID, logItem.cfgName, logItem.ip, logItem.location, logItem.ua, now-logItem.offsetSec)
		}
	}

	// 8. Route Rules (服务器访问规则)
	var ruleCount int
	_ = s.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM route_rules`).Scan(&ruleCount)
	if ruleCount == 0 {
		demoRouteRules := []struct {
			id, nodeID, action, outboundTag string
			priority                        int
			domains, suffixes, cidrs        []string
			port                            uint16
			network, protocol               string
		}{
			{id: "rr-01", nodeID: "demo-node-tyo-01", priority: 100, action: "direct", outboundTag: "", suffixes: []string{".cn", ".qq.com", ".baidu.com", ".bilibili.com"}},
			{id: "rr-02", nodeID: "demo-node-tyo-01", priority: 110, action: "reject", outboundTag: "", domains: []string{"adservice.google.com", "pagead2.googlesyndication.com"}},
			{id: "rr-03", nodeID: "demo-node-hk-01", priority: 100, action: "direct", outboundTag: "", cidrs: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}},
			{id: "rr-04", nodeID: "demo-node-lax-01", priority: 100, action: "outbound", outboundTag: "ob-cf-warp-v4", suffixes: []string{".openai.com", ".claude.ai", ".anthropic.com"}},
			{id: "rr-05", nodeID: "demo-node-fra-01", priority: 100, action: "direct", outboundTag: "", port: 53, network: "udp"},
		}
		for _, rr := range demoRouteRules {
			domJSON, _ := json.Marshal(rr.domains)
			sufJSON, _ := json.Marshal(rr.suffixes)
			cidrJSON, _ := json.Marshal(rr.cidrs)
			nodeIDsJSON, _ := json.Marshal([]string{rr.nodeID})
			_, _ = s.store.db.ExecContext(ctx, `INSERT INTO route_rules (id, node_id, node_ids, priority, enabled, domains, domain_suffix, cidrs, port, network, protocol, inbound_tag, endpoint_name, action, outbound_tag, created_at, updated_at) VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, '', '', ?, ?, ?, ?)`,
				rr.id, rr.nodeID, string(nodeIDsJSON), rr.priority, string(domJSON), string(sufJSON), string(cidrJSON), rr.port, rr.network, rr.protocol, rr.action, rr.outboundTag, now, now)
		}
	}

	// 9. Historical Connections & Devices (connection_records)
	var connRecCount int
	_ = s.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM connection_records`).Scan(&connRecCount)
	if connRecCount == 0 {
		devicePool := []struct {
			ip, user, loc string
		}{
			{"192.168.1.101", "iPhone 16 Pro", "中国 广东 深圳"},
			{"192.168.1.102", "MacBook Pro M3 Max", "中国 北京 朝阳"},
			{"192.168.1.105", "Windows 11 工作站", "中国 上海 浦东"},
			{"192.168.1.108", "iPad Pro 13", "中国 浙江 杭州"},
			{"192.168.1.120", "Apple TV 4K", "中国 广东 广州"},
			{"192.168.1.200", "Homelab 软路由", "中国 江苏 南京"},
		}

		targetPool := []struct {
			dest, host, net, outbound string
			baseDown, baseUp          int64
		}{
			{"104.18.3.15:443", "api.openai.com", "tcp", "直连", 45000000, 3200000},
			{"140.82.113.4:443", "github.com", "tcp", "直连", 18000000, 1500000},
			{"142.250.72.206:443", "www.youtube.com", "tcp", "直连", 680000000, 12000000},
			{"104.16.249.249:443", "claude.ai", "tcp", "Cloudflare WARP (IPv4 官方出口)", 25000000, 1800000},
			{"17.248.163.66:443", "gateway.icloud.com", "tcp", "直连", 120000000, 65000000},
			{"23.50.60.10:443", "store.steampowered.com", "tcp", "🇭🇰 香港落地备用 Socks5", 850000000, 8000000},
			{"162.159.135.234:443", "discord.com", "tcp", "直连", 35000000, 4200000},
			{"198.38.108.150:443", "netflix.com", "tcp", "Cloudflare WARP (IPv4 官方出口)", 1250000000, 9500000},
			{"142.250.190.46:443", "www.google.com", "tcp", "直连", 15000000, 800000},
			{"1.1.1.1:53", "one.one.one.one", "udp", "直连", 120000, 60000},
			{"8.8.8.8:53", "dns.google", "udp", "直连", 180000, 90000},
		}

		nodesPool := []struct {
			id, name string
		}{
			{"demo-node-tyo-01", "🇯🇵 东京 Softbank 01"},
			{"demo-node-hk-01", "🇭🇰 香港 CN2+BGP 01"},
			{"demo-node-lax-01", "🇺🇸 洛杉矶 CN2 GIA"},
			{"demo-node-fra-01", "🇩🇪 法兰克福 Hetzner AX41"},
			{"demo-node-sgp-01", "🇸🇬 新加坡 GCP Southeast"},
		}

		for i := 0; i < 75; i++ {
			recID, _ := newID()
			dev := devicePool[rand.Intn(len(devicePool))]
			tgt := targetPool[rand.Intn(len(targetPool))]
			n := nodesPool[rand.Intn(len(nodesPool))]

			startedOffset := int64(rand.Intn(7*86400) + 120)
			duration := int64(rand.Intn(3600) + 15)
			startedAt := now - startedOffset
			closedAt := startedAt + duration

			down := int64(float64(tgt.baseDown) * (0.4 + rand.Float64()*1.2))
			up := int64(float64(tgt.baseUp) * (0.4 + rand.Float64()*1.2))
			srcPort := fmt.Sprintf("%d", rand.Intn(40000)+15000)

			_, _ = s.store.db.ExecContext(ctx, `INSERT INTO connection_records
				(id, node_id, node_name, connection_id, source_ip, source_port, source_location,
				 destination, host, network, user, listener_name, outbound_name,
				 upload, download, started_at, closed_at, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				recID, n.id, n.name, recID, dev.ip, srcPort, dev.loc,
				tgt.dest, tgt.host, tgt.net, dev.user, "默认接入服务", tgt.outbound,
				up, down, startedAt, closedAt, startedAt)
		}
	}

	// 10. Audit Events & Tasks (操作记录与系统任务)
	var auditCount int
	_ = s.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events`).Scan(&auditCount)
	if auditCount == 0 {
		demoAudit := []struct {
			action, targetType, targetID, targetName, summary, detail, ip, status string
			offsetSec                                                            int64
		}{
			{"node.created", "node", "demo-node-tyo-01", "东京 Softbank 01", "添加服务器「东京 Softbank 01」并建立安全隧道", `{"os": "Ubuntu 24.04", "arch": "amd64", "ip": "103.152.220.10"}`, "114.88.23.15", "succeeded", 180000},
			{"node.created", "node", "demo-node-hk-01", "香港 CN2+BGP 01", "添加服务器「香港 CN2+BGP 01」并建立安全隧道", `{"os": "Debian 12", "arch": "amd64", "ip": "45.195.12.8"}`, "114.88.23.15", "succeeded", 172000},
			{"ddns.synced", "node", "demo-node-lon-01", "伦敦 OVH VPS", "更新服务器「伦敦 OVH VPS」动态域名解析 lon.polaris-node.net", `{"domain": "lon.polaris-node.net", "target_ip": "146.59.12.88"}`, "114.88.23.15", "succeeded", 120000},
			{"listener.created", "listener", "lis-tyo-vless", "东京 VLESS Reality 高速节点", "创建接入服务「东京 VLESS Reality 高速节点」(协议: VLESS, 端口: 443/tcp)", `{"name": "东京 VLESS Reality 高速节点", "protocol": "vless", "port": 443, "network": "tcp", "domain": "itunes.apple.com"}`, "114.88.23.15", "succeeded", 86000},
			{"endpoint.created", "endpoint", "ep-tyo-mac", "MacBook Pro M3 Max", "为节点「MacBook Pro M3 Max」签发接入凭证与连接密钥", `{"user": "user_macbook_m3", "listener": "lis-tyo-vless", "outbound": "direct"}`, "114.88.23.15", "succeeded", 82000},
			{"rule_provider.synced", "mihomo_rule_provider", "rp-cn-domain", "Geosite-CN 规则供应商", "同步规则供应商「Geosite-CN」成功 (共 4,820 条规则)", `{"format": "yaml", "behavior": "domain", "rules_count": 4820}`, "114.88.23.15", "succeeded", 64000},
			{"proxy_group.created", "mihomo_proxy_group", "pg-select", "节点选择", "创建代理分组「节点选择」(策略: 手动选择)", `{"strategy": "select", "members": ["lis-tyo-vless", "lis-hk-hysteria2"]}`, "114.88.23.15", "succeeded", 48000},
			{"client_config.published", "mihomo_client_config", "cfg-general", "全平台通用标准配置", "编译并发布客户端订阅配置「全平台通用标准配置」", `{"profile": "general", "rules_count": 52}`, "114.88.23.15", "succeeded", 36000},
			{"firewall.rule_add", "firewall_rule", "fw-rule-01", "放行 8443/tcp", "修改服务器防火墙：放行 8443/tcp 接入端口", `{"port": 8443, "protocol": "tcp", "action": "accept"}`, "114.88.23.15", "succeeded", 28000},
			{"fail2ban.unban", "fail2ban", "f2b-01", "198.51.100.42", "手动解封被自动拦截的 IP 地址 198.51.100.42", `{"ip": "198.51.100.42", "jail": "sshd", "reason": "operator_unban"}`, "114.88.23.15", "succeeded", 15000},
			{"outbound.created", "outbound", "ob-cf-warp-v4", "Cloudflare WARP (IPv4)", "配置全局上网出口「Cloudflare WARP (IPv4)」162.159.192.1:2408", `{"type": "wireguard", "server": "162.159.192.1", "port": 2408}`, "114.88.23.15", "succeeded", 7200},
			{"task.completed.succeeded", "task", "task-01", "平滑下发配置", "服务器完成系统任务：更新连接服务配置", `{"node": "demo-node-tyo-01", "status": "succeeded"}`, "127.0.0.1", "succeeded", 3600},
			{"alert.settings_updated", "alert_settings", "alerts", "告警推送设置", "更新告警推送策略与阈值", `{"telegram_enabled": true, "webhook_enabled": true}`, "114.88.23.15", "succeeded", 1200},
		}

		for idx, item := range demoAudit {
			eventID := fmt.Sprintf("ae-%03d", idx+1)
			_, _ = s.store.db.ExecContext(ctx, `INSERT INTO audit_events (id, operator_id, action, target_type, target_id, target_name, summary, detail, ip_address, status, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				eventID, opID, item.action, item.targetType, item.targetID, item.targetName, item.summary, item.detail, item.ip, item.status, now-item.offsetSec)
		}
	}

	// 11. Background Tasks (系统操作任务)
	var taskTableCount int
	_ = s.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks`).Scan(&taskTableCount)
	if taskTableCount == 0 {
		demoTasks := []struct {
			id, nodeID, kind, summary, status string
			durationSec                       int64
			offsetSec                         int64
		}{
			{"task-01", "demo-node-tyo-01", "singbox.apply_config", "配置已生效", "succeeded", 2, 72000},
			{"task-02", "demo-node-hk-01", "singbox.apply_config", "配置已生效", "succeeded", 3, 54000},
			{"task-03", "demo-node-lax-01", "dns.update", "解析已刷新", "succeeded", 1, 36000},
			{"task-04", "demo-node-fra-01", "nginx.apply_config", "端口分配成功", "succeeded", 1, 18000},
			{"task-05", "demo-node-tyo-01", "firewall.mutate", "防火墙已同步", "succeeded", 1, 7200},
			{"task-06", "demo-node-hk-01", "singbox.apply_config", "listen tcp 0.0.0.0:443: bind: address already in use", "failed", 1, 3600},
			{"task-07", "demo-node-tyo-01", "singbox.apply_config", "Job for sing-box.service failed because the control process exited with error code", "rolled_back", 4, 1800},
		}
		for _, t := range demoTasks {
			_, _ = s.store.db.ExecContext(ctx, `INSERT OR REPLACE INTO tasks (id, node_id, created_by, kind, idempotency_key, payload, expected_hash, status, result_summary, created_at, started_at, finished_at) VALUES (?, ?, ?, ?, ?, '{}', 'hash', ?, ?, ?, ?, ?)`,
				t.id, t.nodeID, opID, t.kind, t.id, t.status, t.summary, now-t.offsetSec, now-t.offsetSec+1, now-t.offsetSec+1+t.durationSec)
		}
	}

	log.Printf("[polaris] 全套连接配置、历史连接、热门设备与审计数据初始化就绪！")
	return nil
}

// StartDemoSimulator starts a background simulation ticker that keeps demo nodes online,
// gently varies traffic rates and CPU, and actively streams live connections so the monitoring dashboard
// and active connections view feel alive.
func (s *Server) StartDemoSimulator(ctx context.Context) {
	ticker := time.NewTicker(4 * time.Second)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				now := time.Now().Unix()
				totalConnCount := 0
				var totalRxRate, totalTxRate float64
				popularNodes := make([]popularNode, 0, len(demoNodes))

				for _, d := range demoNodes {
					// 1. Keep node online
					_, _ = s.store.db.ExecContext(ctx, `UPDATE nodes SET last_seen_at = ? WHERE id = ?`, now, d.ID)

					// 2. Fetch stored metrics to update lightly
					reportRaw, _, err := s.store.NodeMetrics(ctx, d.ID)
					if err == nil && len(reportRaw) > 0 {
						var m storedMetrics
						if err := json.Unmarshal(reportRaw, &m); err == nil && m.Host != nil {
							// Slight jitter on CPU
							cpuJitter := (rand.Float64() - 0.5) * 4.0
							curCPU := d.BaseCPU + cpuJitter
							if curCPU < 1.0 {
								curCPU = 1.0
							}
							m.Host.CPUPercent = curCPU

							// Slight jitter on Net Rx / Tx
							rxFactor := 0.85 + rand.Float64()*0.3
							txFactor := 0.85 + rand.Float64()*0.3
							curRx := uint64(float64(d.BaseRxRate) * rxFactor)
							curTx := uint64(float64(d.BaseTxRate) * txFactor)
							m.Host.NetRxRate = curRx
							m.Host.NetTxRate = curTx
							m.Host.TCP = uint32(150 + rand.Intn(120))
							m.Host.UDP = uint32(20 + rand.Intn(40))

							totalRxRate += float64(curRx)
							totalTxRate += float64(curTx)

							conns := int(m.Host.TCP)
							totalConnCount += conns
							popularNodes = append(popularNodes, popularNode{
								Name:  d.Name,
								Count: conns,
							})

							newReport, _ := json.Marshal(m)
							_ = s.store.UpdateNodeMetrics(ctx, d.ID, string(newReport))
						}
					}
				}

				// 3. Publish live fleet totals & active connections to connHub so the console charts live-stream
				if s.connHub != nil {
					totals := fleetTotals{
						At:                   time.Now().UTC().Format(time.RFC3339),
						DownloadRate:         totalRxRate,
						UploadRate:           totalTxRate,
						ConnectionCount:      totalConnCount,
						Nodes:                len(demoNodes),
						Reporting:            len(demoNodes),
						HasRates:             true,
						Protocols:            map[string]int{"TCP": int(float64(totalConnCount) * 0.78), "UDP": int(float64(totalConnCount) * 0.22)},
						PopularNodes:         popularNodes,
						PopularWindowMinutes: 10,
					}
					s.connHub.broadcastTotals(totals)

					// Push simulated active connections for Tokyo and Hong Kong nodes
					liveConnsTyo := []storedConnection{
						{
							ID: "conn-live-01", Network: "tcp", Source: "192.168.1.101:54321", SourceIP: "192.168.1.101",
							SourceLocation: "中国 广东 深圳", Destination: "104.18.3.15:443", Host: "api.openai.com",
							Upload: 18200000, Download: 145000000, StartedAt: time.Now().Add(-18 * time.Minute).UTC().Format(time.RFC3339),
							Outbound: "direct", ListenerName: "东京 VLESS Reality 高速节点", User: "iPhone 16 Pro",
							OutboundName: "直连", HasRates: true, UploadRate: 142000, DownloadRate: 1250000,
						},
						{
							ID: "conn-live-02", Network: "tcp", Source: "192.168.1.105:58920", SourceIP: "192.168.1.105",
							SourceLocation: "中国 上海 浦东", Destination: "142.250.72.206:443", Host: "www.youtube.com",
							Upload: 42000000, Download: 890000000, StartedAt: time.Now().Add(-42 * time.Minute).UTC().Format(time.RFC3339),
							Outbound: "direct", ListenerName: "东京 VLESS Reality 高速节点", User: "Windows 11 工作站",
							OutboundName: "直连", HasRates: true, UploadRate: 85000, DownloadRate: 8400000,
						},
						{
							ID: "conn-live-03", Network: "tcp", Source: "192.168.1.102:60122", SourceIP: "192.168.1.102",
							SourceLocation: "中国 北京 朝阳", Destination: "140.82.113.4:443", Host: "github.com",
							Upload: 12500000, Download: 64000000, StartedAt: time.Now().Add(-8 * time.Minute).UTC().Format(time.RFC3339),
							Outbound: "direct", ListenerName: "东京 VLESS Reality 高速节点", User: "MacBook Pro M3 Max",
							OutboundName: "直连", HasRates: true, UploadRate: 35000, DownloadRate: 420000,
						},
					}
					connsBytesTyo, _ := json.Marshal(liveConnsTyo)
					s.connHub.update(nodeConnectionsSnapshot{
						NodeID: "demo-node-tyo-01", CollectedAt: time.Now().UTC().Format(time.RFC3339),
						Connections: connsBytesTyo, RawConns: liveConnsTyo, ConnectionCount: len(liveConnsTyo),
						HasRates: true, ReceivedRate: 9800000, SentRate: 260000,
						Protocols: map[string]int{"TCP": 3},
					})

					liveConnsHk := []storedConnection{
						{
							ID: "conn-live-04", Network: "tcp", Source: "192.168.1.108:51234", SourceIP: "192.168.1.108",
							SourceLocation: "中国 浙江 杭州", Destination: "104.16.249.249:443", Host: "claude.ai",
							Upload: 8400000, Download: 98000000, StartedAt: time.Now().Add(-12 * time.Minute).UTC().Format(time.RFC3339),
							Outbound: "direct", ListenerName: "香港 Hysteria 2 双程低延迟", User: "iPad Pro 13",
							OutboundName: "直连", HasRates: true, UploadRate: 28000, DownloadRate: 650000,
						},
						{
							ID: "conn-live-05", Network: "tcp", Source: "192.168.1.120:53450", SourceIP: "192.168.1.120",
							SourceLocation: "中国 广东 广州", Destination: "198.38.108.150:443", Host: "netflix.com",
							Upload: 15000000, Download: 1420000000, StartedAt: time.Now().Add(-65 * time.Minute).UTC().Format(time.RFC3339),
							Outbound: "ob-cf-warp-v4", ListenerName: "香港 Hysteria 2 双程低延迟", User: "Apple TV 4K",
							OutboundName: "Cloudflare WARP (IPv4 官方出口)", HasRates: true, UploadRate: 65000, DownloadRate: 14200000,
						},
						{
							ID: "conn-live-06", Network: "udp", Source: "192.168.1.200:53535", SourceIP: "192.168.1.200",
							SourceLocation: "中国 江苏 南京", Destination: "1.1.1.1:53", Host: "one.one.one.one",
							Upload: 450000, Download: 890000, StartedAt: time.Now().Add(-120 * time.Minute).UTC().Format(time.RFC3339),
							Outbound: "direct", ListenerName: "香港 Hysteria 2 双程低延迟", User: "Homelab 软路由",
							OutboundName: "直连", HasRates: true, UploadRate: 1500, DownloadRate: 3200,
						},
					}
					connsBytesHk, _ := json.Marshal(liveConnsHk)
					s.connHub.update(nodeConnectionsSnapshot{
						NodeID: "demo-node-hk-01", CollectedAt: time.Now().UTC().Format(time.RFC3339),
						Connections: connsBytesHk, RawConns: liveConnsHk, ConnectionCount: len(liveConnsHk),
						HasRates: true, ReceivedRate: 14850000, SentRate: 94500,
						Protocols: map[string]int{"TCP": 2, "UDP": 1},
					})
				}
			}
		}
	}()
}
