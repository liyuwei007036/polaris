package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type DNSUpdatePayload struct {
	IP      string   `json:"ip"`
	Domain  string   `json:"domain,omitempty"`
	Domains []string `json:"domains,omitempty"`
}

// flushSystemDNSCache attempts to flush system resolver caches on a best-effort basis.
// Failure (e.g. non-root user lacking permission) is ignored so non-root executions never fail.
func flushSystemDNSCache(ctx context.Context) {
	if runtime.GOOS == "windows" {
		_ = exec.CommandContext(ctx, "ipconfig", "/flushdns").Run()
		return
	}
	_ = exec.CommandContext(ctx, "resolvectl", "flush-caches").Run()
	_ = exec.CommandContext(ctx, "systemd-resolve", "--flush-caches").Run()
	_ = exec.CommandContext(ctx, "systemctl", "restart", "nscd").Run()
}

// syncHostsFile best-effort updates /etc/hosts with a managed polaris block.
// Returns true if successfully updated, or false if non-root lacking write permission (or error).
func syncHostsFile(domains []string, targetIP string) bool {
	if runtime.GOOS == "windows" {
		return false
	}
	targetIP = strings.TrimSpace(targetIP)
	if targetIP == "" || len(domains) == 0 {
		return false
	}
	hostsPath := "/etc/hosts"
	content, err := os.ReadFile(hostsPath)
	if err != nil {
		return false
	}
	lines := strings.Split(string(content), "\n")
	var newLines []string
	inPolarisBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "# BEGIN POLARIS DDNS" {
			inPolarisBlock = true
			continue
		}
		if trimmed == "# END POLARIS DDNS" {
			inPolarisBlock = false
			continue
		}
		if inPolarisBlock {
			continue
		}
		hasDomain := false
		for _, d := range domains {
			if strings.Contains(line, d) {
				hasDomain = true
				break
			}
		}
		if !hasDomain {
			newLines = append(newLines, line)
		}
	}

	var managedBlock []string
	managedBlock = append(managedBlock, "# BEGIN POLARIS DDNS")
	for _, d := range domains {
		d = strings.TrimSpace(d)
		if d != "" {
			managedBlock = append(managedBlock, fmt.Sprintf("%s %s", targetIP, d))
		}
	}
	managedBlock = append(managedBlock, "# END POLARIS DDNS")

	newContent := strings.TrimRight(strings.Join(newLines, "\n"), "\n") + "\n" + strings.Join(managedBlock, "\n") + "\n"
	if err := os.WriteFile(hostsPath, []byte(newContent), 0o644); err != nil {
		return false
	}
	return true
}

// updateSingBoxDNSOverride updates sing-box's configuration file (managed by agent)
// with a predefined DNS rule mapping domain -> targetIP.
// This works under both root and non-root agent executions without requiring /etc/hosts.
func updateSingBoxDNSOverride(domains []string, targetIP, dataDir string) (bool, error) {
	configPath := managedSingBoxConfig
	if _, err := os.Stat(configPath); err != nil {
		return false, nil
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		return false, err
	}

	var configMap map[string]any
	if err := json.Unmarshal(configBytes, &configMap); err != nil {
		return false, err
	}

	dnsMap, ok := configMap["dns"].(map[string]any)
	if !ok || dnsMap == nil {
		dnsMap = make(map[string]any)
		configMap["dns"] = dnsMap
	}

	rtype := "A"
	if parsed := net.ParseIP(targetIP); parsed != nil && parsed.To4() == nil {
		rtype = "AAAA"
	}

	var rules []any
	if existingRules, ok := dnsMap["rules"].([]any); ok {
		rules = existingRules
	}

	domainSet := make(map[string]bool)
	for _, d := range domains {
		d = strings.TrimSpace(strings.ToLower(strings.TrimSuffix(d, ".")))
		if d != "" {
			domainSet[d] = true
		}
	}

	// Filter out any existing predefined rules for these domains
	var filteredRules []any
	for _, r := range rules {
		rMap, ok := r.(map[string]any)
		if !ok {
			filteredRules = append(filteredRules, r)
			continue
		}
		if rMap["action"] == "predefined" {
			match := false
			if dList, ok := rMap["domain"].([]any); ok {
				for _, dItem := range dList {
					if dStr, ok := dItem.(string); ok && domainSet[strings.ToLower(strings.TrimSuffix(dStr, "."))] {
						match = true
						break
					}
				}
			}
			if match {
				continue
			}
		}
		filteredRules = append(filteredRules, r)
	}

	// Append updated predefined rules
	for d := range domainSet {
		filteredRules = append(filteredRules, map[string]any{
			"domain": []any{d},
			"action": "predefined",
			"answer": []any{fmt.Sprintf("%s. 60 IN %s %s", d, rtype, targetIP)},
		})
	}
	dnsMap["rules"] = filteredRules

	encoded, err := json.MarshalIndent(configMap, "", "  ")
	if err != nil {
		return false, err
	}

	configDir := filepath.Dir(configPath)
	tempFile, err := os.CreateTemp(configDir, ".singbox-ddns-*.json")
	if err != nil {
		return false, err
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)

	if _, err := tempFile.Write(encoded); err != nil {
		tempFile.Close()
		return false, err
	}
	if err := tempFile.Chmod(0o640); err != nil {
		tempFile.Close()
		return false, err
	}
	if err := tempFile.Close(); err != nil {
		return false, err
	}

	if err := os.Rename(tempPath, configPath); err != nil {
		return false, err
	}

	if dataDir != "" {
		_ = updateEffectiveSingBoxConfigurationState(dataDir)
	}
	return true, nil
}

// closeStaleClashConnections selectively terminates only the active connections targeting
// the affected domain(s), stale destination IP, or through the affected chain outbound(s) via DELETE /connections/:id.
// This guarantees that all other unrelated user connections, inbounds, and direct routes
// on sing-box remain completely unaffected and uninterrupted.
func closeStaleClashConnections(ctx context.Context, domains []string, targetIP string) error {
	if len(domains) == 0 {
		return nil
	}
	domainMap := make(map[string]bool, len(domains))
	for _, d := range domains {
		d = strings.TrimSpace(strings.ToLower(strings.TrimSuffix(d, ".")))
		if d != "" {
			domainMap[d] = true
		}
	}

	getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, clashAPIBase+"/connections", nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(getReq)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var payload struct {
		Connections []struct {
			ID       string   `json:"id"`
			Chains   []string `json:"chains"`
			Metadata struct {
				Host          string `json:"host"`
				DestinationIP string `json:"destinationIP"`
			} `json:"metadata"`
		} `json:"connections"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8*1024*1024)).Decode(&payload); err != nil {
		return nil
	}

	// Read config to find outbound tags matching any of the affected domains
	affectedOutboundTags := make(map[string]bool)
	if configBytes, err := os.ReadFile(managedSingBoxConfig); err == nil {
		var cfg struct {
			Outbounds []struct {
				Tag    string `json:"tag"`
				Server string `json:"server"`
				TLS    struct {
					ServerName string `json:"server_name"`
				} `json:"tls"`
			} `json:"outbounds"`
		}
		if json.Unmarshal(configBytes, &cfg) == nil {
			for _, ob := range cfg.Outbounds {
				srv := strings.TrimSpace(strings.ToLower(strings.TrimSuffix(ob.Server, ".")))
				sni := strings.TrimSpace(strings.ToLower(strings.TrimSuffix(ob.TLS.ServerName, ".")))
				if domainMap[srv] || domainMap[sni] {
					if ob.Tag != "" {
						affectedOutboundTags[ob.Tag] = true
					}
				}
			}
		}
	}

	client := &http.Client{Timeout: 2 * time.Second}
	for _, conn := range payload.Connections {
		match := false
		host := strings.TrimSpace(strings.ToLower(strings.TrimSuffix(conn.Metadata.Host, ".")))
		if host != "" && domainMap[host] {
			match = true
		}
		if !match && targetIP != "" && conn.Metadata.DestinationIP != "" && conn.Metadata.DestinationIP != targetIP {
			for d := range domainMap {
				if strings.Contains(host, d) {
					match = true
					break
				}
			}
		}
		if !match {
			for _, ch := range conn.Chains {
				if affectedOutboundTags[ch] {
					match = true
					break
				}
			}
		}
		if match && conn.ID != "" {
			delReq, _ := http.NewRequestWithContext(ctx, http.MethodDelete, clashAPIBase+"/connections/"+conn.ID, nil)
			if delResp, err := client.Do(delReq); err == nil {
				_ = delResp.Body.Close()
			}
		}
	}
	return nil
}

// reloadSingBoxState restarts sing-box to invalidate dialer caches and reset outbound states.
func reloadSingBoxState(ctx context.Context) error {
	// Sing-box does NOT support SIGHUP configuration reload or dialer cache flush.
	// To reliably terminate stale QUIC/TCP session pools and apply the new target IP,
	// sing-box must be restarted. Restarting sing-box takes ~30ms.
	if singBoxServiceActive(ctx) {
		res := restartSingBox(ctx)
		if res.Status == "succeeded" {
			return nil
		}
		return errors.New(res.Summary)
	}
	if commandExists("pkill") {
		_ = exec.CommandContext(ctx, "pkill", "-HUP", "-f", "sing-box").Run()
	}
	return nil
}

// verifyDomainResolution actively probes local DNS resolution to verify whether the domain
// resolves to expectedIP. It polls for up to timeout to allow upstream DNS propagation to arrive.
func verifyDomainResolution(ctx context.Context, domain, expectedIP string, timeout time.Duration) ([]string, bool) {
	expectedIP = strings.TrimSpace(expectedIP)
	deadline := time.Now().Add(timeout)
	for {
		lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		ips, err := net.DefaultResolver.LookupIP(lookupCtx, "ip", domain)
		cancel()
		if err == nil {
			var ipStrs []string
			for _, ip := range ips {
				ipStr := ip.String()
				ipStrs = append(ipStrs, ipStr)
				if ipStr == expectedIP {
					return ipStrs, true
				}
			}
			if len(ipStrs) > 0 && (expectedIP == "" || time.Now().After(deadline)) {
				return ipStrs, expectedIP == ""
			}
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(500 * time.Millisecond):
		}
	}
	return nil, false
}

func updateDNS(ctx context.Context, task Task, dataDirOpts ...string) TaskResult {
	var payload DNSUpdatePayload
	if err := json.Unmarshal([]byte(task.Payload), &payload); err != nil {
		return TaskResult{Status: "failed", Summary: "invalid dns.update payload: " + err.Error()}
	}
	var domains []string
	if payload.Domain != "" {
		domains = append(domains, payload.Domain)
	}
	domains = append(domains, payload.Domains...)
	domains = deduplicateStrings(domains)
	if len(domains) == 0 {
		return TaskResult{Status: "failed", Summary: "missing target domains in dns.update payload"}
	}
	expectedIP := strings.TrimSpace(payload.IP)
	if expectedIP == "" {
		return TaskResult{Status: "failed", Summary: "missing target IP in dns.update payload"}
	}

	var dataDir string
	if len(dataDirOpts) > 0 && dataDirOpts[0] != "" {
		dataDir = dataDirOpts[0]
	}

	var notes []string

	// 1. Best-effort /etc/hosts update (0ms resolution, works when running as root)
	if syncHostsFile(domains, expectedIP) {
		notes = append(notes, "已同步 /etc/hosts")
	}

	// 2. Best-effort system DNS cache flush
	flushSystemDNSCache(ctx)

	// 3. Update sing-box configuration with predefined DNS rules
	// This guarantees 0ms local DNS resolution inside sing-box without requiring root access to /etc/hosts.
	if updated, err := updateSingBoxDNSOverride(domains, expectedIP, dataDir); err == nil && updated {
		notes = append(notes, "已写入 sing-box 静态解析规则")
	}

	// 4. Selectively terminate stale connections targeting the affected NAT domain/IP/chain
	_ = closeStaleClashConnections(ctx, domains, expectedIP)

	// 5. Restart sing-box to flush persistent QUIC/Hysteria2/TCP pools and re-dial immediately
	if singBoxServiceActive(ctx) {
		if res := restartSingBox(ctx); res.Status == "succeeded" {
			notes = append(notes, "已重启 sing-box 重置出站连接池")
		} else {
			notes = append(notes, "sing-box 重启提示: "+res.Summary)
		}
	} else if commandExists("pkill") {
		_ = exec.CommandContext(ctx, "pkill", "-HUP", "-f", "sing-box").Run()
	}

	summary := fmt.Sprintf("%s → %s", strings.Join(domains, ", "), expectedIP)
	if len(notes) > 0 {
		summary += "（" + strings.Join(notes, "，") + "）"
	}
	return TaskResult{
		Status:  "succeeded",
		Summary: summary,
	}
}

func deduplicateStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, v := range values {
		if v != "" && !seen[v] {
			seen[v] = true
			result = append(result, v)
		}
	}
	return result
}

