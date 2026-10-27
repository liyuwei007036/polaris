package control

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

// triggerNodeDDNS is invoked whenever an agent connects or reconnects, or when
// an operator edits DDNS settings. It checks if the observed egress IP has
// changed and actively updates the bound Cloudflare DNS record.
func (s *Server) triggerNodeDDNS(ctx context.Context, nodeID, currentIP string) {
	currentIP = strings.TrimSpace(currentIP)
	if currentIP == "" {
		return
	}
	ip := net.ParseIP(currentIP)
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
		return
	}

	s.ddnsMu.Lock()
	defer s.ddnsMu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	node, err := s.store.GetNode(ctx, nodeID)
	if err != nil || !node.DDNSEnabled || strings.TrimSpace(node.DDNSRecordID) == "" {
		return
	}

	if node.LastObservedIP == currentIP {
		return
	}

	zoneID, _, client, err := s.store.CloudflareZone(ctx)
	if err != nil {
		log.Printf("ddns: node %s (%s) cloudflare zone not configured: %v", node.ID, node.Name, err)
		return
	}

	record, err := client.GetRecord(ctx, zoneID, node.DDNSRecordID)
	if err != nil {
		log.Printf("ddns: node %s (%s) get cloudflare record %s failed: %v", node.ID, node.Name, node.DDNSRecordID, err)
		return
	}

	isIPv4 := ip.To4() != nil
	if isIPv4 && record.Type != "A" {
		log.Printf("ddns: record %s type is %s, expected A for IPv4 %s", record.Name, record.Type, currentIP)
		return
	}
	if !isIPv4 && record.Type != "AAAA" {
		log.Printf("ddns: record %s type is %s, expected AAAA for IPv6 %s", record.Name, record.Type, currentIP)
		return
	}

	oldIP := record.Content
	if record.Content != currentIP {
		record.Content = currentIP
		if record.TTL > 120 {
			record.TTL = 60
		}
		updated, err := s.store.UpdateCloudflareRecord(ctx, record)
		if err != nil {
			log.Printf("ddns: node %s (%s) update cloudflare record failed: %v", node.ID, node.Name, err)
			return
		}

		operatorID := s.store.SystemOperatorID(ctx)
		if operatorID != "" {
			summary := fmt.Sprintf("NAT 节点「%s」公网 IP 变更为 %s（原 %s），已自动更新 Cloudflare 解析 %s", node.Name, currentIP, oldIP, updated.Name)
			_ = s.store.AppendAudit(ctx, operatorID, "cloudflare_record.ddns_updated", "node", node.ID, summary)
		}
	}

	_ = s.store.SetNodeLastObservedIP(ctx, node.ID, currentIP)
	if node.DDNSDomain != "" && (node.ClientAddress == "" || node.ClientAddress == oldIP || net.ParseIP(node.ClientAddress) != nil) {
		_ = s.store.SetNodeClientAddress(ctx, node.ID, node.DDNSDomain)
	}

	s.liveHub.publish(liveEvent{Kind: "node", NodeID: node.ID})
	s.liveHub.publish(liveEvent{Kind: "cloudflare"})
}

// syncNodeDDNSNow forces an immediate Cloudflare DNS record update for the specified node and IP.
func (s *Server) syncNodeDDNSNow(ctx context.Context, node Node, currentIP, operatorID string) error {
	currentIP = strings.TrimSpace(currentIP)
	ip := net.ParseIP(currentIP)
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
		return errors.New("无效的公网 IP 地址")
	}

	s.ddnsMu.Lock()
	defer s.ddnsMu.Unlock()

	zoneID, _, client, err := s.store.CloudflareZone(ctx)
	if err != nil {
		return fmt.Errorf("Cloudflare 未配置或连接不可用: %w", err)
	}

	record, err := client.GetRecord(ctx, zoneID, node.DDNSRecordID)
	if err != nil {
		return fmt.Errorf("获取 Cloudflare 解析记录失败: %w", err)
	}

	isIPv4 := ip.To4() != nil
	if isIPv4 && record.Type != "A" {
		return fmt.Errorf("记录类型为 %s，与当前 IPv4 地址不匹配", record.Type)
	}
	if !isIPv4 && record.Type != "AAAA" {
		return fmt.Errorf("记录类型为 %s，与当前 IPv6 地址不匹配", record.Type)
	}

	oldIP := record.Content
	record.Content = currentIP
	if record.TTL > 120 {
		record.TTL = 60
	}

	updated, err := s.store.UpdateCloudflareRecord(ctx, record)
	if err != nil {
		return fmt.Errorf("更新 Cloudflare 记录失败: %w", err)
	}

	_ = s.store.SetNodeLastObservedIP(ctx, node.ID, currentIP)
	if node.DDNSDomain != "" && (node.ClientAddress == "" || node.ClientAddress == oldIP || net.ParseIP(node.ClientAddress) != nil) {
		_ = s.store.SetNodeClientAddress(ctx, node.ID, node.DDNSDomain)
	}

	if operatorID != "" {
		summary := fmt.Sprintf("手动同步：NAT 节点「%s」公网 IP 更新为 %s，已更新 Cloudflare 解析 %s", node.Name, currentIP, updated.Name)
		_ = s.store.AppendAudit(ctx, operatorID, "cloudflare_record.ddns_synced", "node", node.ID, summary)
	}

	s.liveHub.publish(liveEvent{Kind: "node", NodeID: node.ID})
	s.liveHub.publish(liveEvent{Kind: "cloudflare"})
	return nil
}

// syncNodeDDNS is the HTTP handler for POST /api/v1/nodes/{id}/ddns/sync.
func (s *Server) syncNodeDDNS(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	nodeID := r.PathValue("id")
	node, err := s.store.GetNode(r.Context(), nodeID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !node.DDNSEnabled || strings.TrimSpace(node.DDNSRecordID) == "" {
		writeError(w, userErrorf("该服务器未启用 DDNS 或未关联解析记录"))
		return
	}
	currentIP := s.nodeActiveAddress(node.ID)
	if currentIP == "" {
		currentIP = node.LastObservedIP
	}
	if currentIP == "" {
		writeError(w, userErrorf("当前无法获取该服务器的公网 IP，请确认服务器已上线"))
		return
	}
	if err := s.syncNodeDDNSNow(r.Context(), node, currentIP, operator.ID); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "ip": currentIP})
}
