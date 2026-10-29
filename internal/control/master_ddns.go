package control

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// fetchMasterPublicIP queries external endpoints to get the current outbound public IPv4 address of Master.
func fetchMasterPublicIP(ctx context.Context) (string, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	endpoints := []string{
		"https://cloudflare.com/cdn-cgi/trace",
		"https://api.ipify.org",
		"https://icanhazip.com",
	}

	for _, ep := range endpoints {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			continue
		}

		text := string(body)
		var candidate string
		if strings.Contains(ep, "cdn-cgi/trace") {
			for _, line := range strings.Split(text, "\n") {
				if strings.HasPrefix(line, "ip=") {
					candidate = strings.TrimSpace(strings.TrimPrefix(line, "ip="))
					break
				}
			}
		} else {
			candidate = strings.TrimSpace(text)
		}

		if candidate != "" {
			ip := net.ParseIP(candidate)
			if ip != nil && ip.To4() != nil && !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsUnspecified() {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("unable to determine public IP from external endpoints")
}

// SyncMasterDDNS synchronizes all configured Master DDNS domains in Cloudflare to Master's current public IP.
func (s *Server) SyncMasterDDNS(ctx context.Context, force bool) (string, []string, error) {
	settings, err := s.store.GetMasterDDNS(ctx)
	if err != nil {
		return "", nil, err
	}
	if !settings.Enabled && !force {
		return "", nil, nil
	}
	if len(settings.RecordIDs) == 0 && len(settings.Domains) == 0 {
		return "", nil, nil
	}

	currentIP, err := fetchMasterPublicIP(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("detect public IP: %w", err)
	}

	if !force && settings.LastObservedIP == currentIP {
		return currentIP, settings.Domains, nil
	}

	zoneID, _, client, err := s.store.CloudflareZone(ctx)
	if err != nil {
		return currentIP, nil, fmt.Errorf("cloudflare zone not configured: %w", err)
	}

	// Fetch all records once to support fallback lookup by name
	allRecords, listErr := client.ListRecords(ctx, zoneID)
	recordMap := make(map[string]CloudflareRecord)
	nameMap := make(map[string]CloudflareRecord)
	if listErr == nil {
		for _, rec := range allRecords {
			recordMap[rec.ID] = rec
			nameMap[strings.ToLower(strings.TrimSpace(rec.Name))] = rec
		}
	}

	var (
		updatedDomains []string
		mu             sync.Mutex
		wg             sync.WaitGroup
		errs           []string
	)

	// Combine target record IDs and domain names
	targets := make(map[string]string) // recordID -> domain name
	for i, id := range settings.RecordIDs {
		domain := ""
		if i < len(settings.Domains) {
			domain = settings.Domains[i]
		}
		targets[id] = domain
	}

	for id, domain := range targets {
		recID := id
		dName := domain
		wg.Add(1)
		go func() {
			defer wg.Done()
			var targetRecord CloudflareRecord
			var found bool

			if recID != "" {
				if r, ok := recordMap[recID]; ok {
					targetRecord = r
					found = true
				} else {
					r, err := client.GetRecord(ctx, zoneID, recID)
					if err == nil {
						targetRecord = r
						found = true
					}
				}
			}
			if !found && dName != "" {
				if r, ok := nameMap[strings.ToLower(strings.TrimSpace(dName))]; ok {
					targetRecord = r
					found = true
				}
			}

			if !found {
				mu.Lock()
				errs = append(errs, fmt.Sprintf("record not found (id: %s, domain: %s)", recID, dName))
				mu.Unlock()
				return
			}

			if targetRecord.Type != "A" {
				mu.Lock()
				errs = append(errs, fmt.Sprintf("record %s is of type %s, expected A", targetRecord.Name, targetRecord.Type))
				mu.Unlock()
				return
			}

			if targetRecord.Content == currentIP && targetRecord.TTL == 60 {
				mu.Lock()
				updatedDomains = append(updatedDomains, targetRecord.Name)
				mu.Unlock()
				return
			}

			targetRecord.Content = currentIP
			targetRecord.TTL = 60
			updated, err := client.UpdateRecord(ctx, zoneID, targetRecord)
			if err != nil {
				mu.Lock()
				errs = append(errs, fmt.Sprintf("update record %s: %v", targetRecord.Name, err))
				mu.Unlock()
				return
			}

			mu.Lock()
			updatedDomains = append(updatedDomains, updated.Name)
			mu.Unlock()
		}()
	}
	wg.Wait()

	oldIP := settings.LastObservedIP
	_ = s.store.SetMasterDDNSLastObservedIP(ctx, currentIP)

	if len(updatedDomains) > 0 && (oldIP != currentIP || force) {
		operatorID := s.store.SystemOperatorID(ctx)
		summary := fmt.Sprintf("Master 公网 IP 为 %s（原 %s），已自动同步 Cloudflare 解析: %s",
			currentIP, oldIP, strings.Join(updatedDomains, ", "))
		_ = s.store.AppendAudit(ctx, operatorID, "master.ddns_updated", "master", "ddns", summary)

		if s.alertEngine != nil && oldIP != "" && oldIP != currentIP {
			s.alertEngine.NotifyMasterDDNSUpdated(oldIP, currentIP, updatedDomains)
		}
	}

	if s.liveHub != nil {
		s.liveHub.publish(liveEvent{Kind: "cloudflare"})
	}

	if len(errs) > 0 {
		return currentIP, updatedDomains, fmt.Errorf("errors updating some records: %s", strings.Join(errs, "; "))
	}
	return currentIP, updatedDomains, nil
}

// StartMasterDDNS begins the background polling loop for Master public IP changes.
func (s *Server) StartMasterDDNS(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		// Run once after initial startup delay
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
			_, _, _ = s.SyncMasterDDNS(ctx, false)
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _, _ = s.SyncMasterDDNS(ctx, false)
			}
		}
	}()
}
