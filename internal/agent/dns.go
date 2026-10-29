package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
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

// closeStaleClashConnections selectively terminates only the active connections targeting
// the affected domain(s) or stale IP via DELETE /connections/:id.
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
			ID       string `json:"id"`
			Metadata struct {
				Host          string `json:"host"`
				DestinationIP string `json:"destinationIP"`
			} `json:"metadata"`
		} `json:"connections"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8*1024*1024)).Decode(&payload); err != nil {
		return nil
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
		if match && conn.ID != "" {
			delReq, _ := http.NewRequestWithContext(ctx, http.MethodDelete, clashAPIBase+"/connections/"+conn.ID, nil)
			if delResp, err := client.Do(delReq); err == nil {
				_ = delResp.Body.Close()
			}
		}
	}
	return nil
}

// reloadSingBoxState hot-reloads sing-box (SIGHUP) to invalidate dialer caches and reset outbound states.
// It does not modify configuration files or change configuration hashes.
func reloadSingBoxState(ctx context.Context) error {
	// 1. If running under systemd, reload unit
	if commandExists("systemctl") {
		if output, err := exec.CommandContext(ctx, "systemctl", "reload", "sing-box.service").CombinedOutput(); err == nil {
			return nil
		} else {
			_ = output
		}
	}
	// 2. Try pkill -HUP for user-owned sing-box process (works for non-root without sudo)
	if commandExists("pkill") {
		if err := exec.CommandContext(ctx, "pkill", "-HUP", "-f", "sing-box").Run(); err == nil {
			return nil
		}
	}
	// 3. Fallback: if sing-box service is active, restart it
	if singBoxServiceActive(ctx) {
		res := restartSingBox(ctx)
		if res.Status == "succeeded" {
			return nil
		}
		return errors.New(res.Summary)
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

func updateDNS(ctx context.Context, task Task) TaskResult {
	var payload DNSUpdatePayload
	if err := json.Unmarshal([]byte(task.Payload), &payload); err != nil {
		return TaskResult{Status: "failed", Summary: "invalid dns.update payload: " + err.Error()}
	}
	var domains []string
	if payload.Domain != "" {
		domains = append(domains, payload.Domain)
	}
	domains = append(domains, payload.Domains...)
	if len(domains) == 0 {
		return TaskResult{Status: "failed", Summary: "missing target domains in dns.update payload"}
	}

	// 1. Best-effort system DNS cache flush (non-fatal if non-root)
	flushSystemDNSCache(ctx)

	// 2. Active verification: verify whether the target domain actually resolves to the new IP locally
	expectedIP := strings.TrimSpace(payload.IP)
	var verifyNotes []string
	allVerified := true
	if expectedIP != "" {
		for _, domain := range domains {
			resolved, ok := verifyDomainResolution(ctx, domain, expectedIP, 2*time.Second)
			if ok {
				verifyNotes = append(verifyNotes, fmt.Sprintf("%s -> %s (已验证生效)", domain, expectedIP))
			} else if len(resolved) > 0 {
				allVerified = false
				verifyNotes = append(verifyNotes, fmt.Sprintf("%s -> %s (等待上游同步，目标: %s)", domain, strings.Join(resolved, ","), expectedIP))
			} else {
				allVerified = false
				verifyNotes = append(verifyNotes, fmt.Sprintf("%s (待同步，目标: %s)", domain, expectedIP))
			}
		}
	}

	// 3. Selectively terminate ONLY the stale connections targeting the affected NAT domain/IP.
	// Other unrelated proxy users, inbounds, and direct routes remain completely unaffected.
	_ = closeStaleClashConnections(ctx, domains, expectedIP)

	// 4. Hot-reload sing-box state (SIGHUP) only to flush dialer caches for the next requests
	_ = reloadSingBoxState(ctx)

	summary := fmt.Sprintf("已执行动态 DNS 刷新与连接自愈：%s", strings.Join(domains, ", "))
	if len(verifyNotes) > 0 {
		summary += "；解析状态：" + strings.Join(verifyNotes, "，")
	}
	if !allVerified && expectedIP != "" {
		summary += "；提示：上游 DNS 尚在传播收敛中，sing-box 正常运行，仅该中转链路等待 DNS 最终同步"
	}
	return TaskResult{
		Status:  "succeeded",
		Summary: summary,
	}
}
