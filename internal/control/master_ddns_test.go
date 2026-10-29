package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestMasterDDNSLifecycle(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := t.Context()
	settings, err := store.GetMasterDDNS(ctx)
	if err != nil {
		t.Fatalf("GetMasterDDNS failed: %v", err)
	}
	if settings.Enabled {
		t.Fatalf("expected Enabled=false initially, got true")
	}

	updated, err := store.UpdateMasterDDNS(ctx, MasterDDNSSettings{
		Enabled:   true,
		RecordIDs: []string{"rec-1", "rec-2"},
		Domains:   []string{"int.1x1x1.dpdns.org", "master.1x1x1.dpdns.org"},
	})
	if err != nil {
		t.Fatalf("UpdateMasterDDNS failed: %v", err)
	}
	if !updated.Enabled || len(updated.RecordIDs) != 2 || len(updated.Domains) != 2 {
		t.Fatalf("unexpected updated settings: %+v", updated)
	}

	if err := store.SetMasterDDNSLastObservedIP(ctx, "1.2.3.4"); err != nil {
		t.Fatalf("SetMasterDDNSLastObservedIP failed: %v", err)
	}
	settings, err = store.GetMasterDDNS(ctx)
	if err != nil {
		t.Fatalf("GetMasterDDNS failed: %v", err)
	}
	if settings.LastObservedIP != "1.2.3.4" {
		t.Fatalf("expected LastObservedIP=1.2.3.4, got %s", settings.LastObservedIP)
	}
}

func TestMasterDDNSSyncMultipleDomains(t *testing.T) {
	var mu sync.Mutex
	records := map[string]string{
		"rec-1": "161.118.209.69",
		"rec-2": "161.118.209.69",
	}

	mockCF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-123":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result": map[string]any{
					"id":     "zone-123",
					"name":   "1x1x1.dpdns.org",
					"status": "active",
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-123/dns_records":
			var list []map[string]any
			for id, ip := range records {
				name := "int.1x1x1.dpdns.org"
				if id == "rec-2" {
					name = "master.1x1x1.dpdns.org"
				}
				list = append(list, map[string]any{
					"id":      id,
					"type":    "A",
					"name":    name,
					"content": ip,
					"ttl":     300,
					"proxied": false,
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result":  list,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-123/dns_records/rec-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result": map[string]any{
					"id":      "rec-1",
					"type":    "A",
					"name":    "int.1x1x1.dpdns.org",
					"content": records["rec-1"],
					"ttl":     300,
					"proxied": false,
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-123/dns_records/rec-2":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result": map[string]any{
					"id":      "rec-2",
					"type":    "A",
					"name":    "master.1x1x1.dpdns.org",
					"content": records["rec-2"],
					"ttl":     300,
					"proxied": false,
				},
			})
		case r.Method == http.MethodPatch && r.URL.Path == "/zones/zone-123/dns_records/rec-1":
			var update struct {
				Content string `json:"content"`
			}
			_ = json.NewDecoder(r.Body).Decode(&update)
			records["rec-1"] = update.Content
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result": map[string]any{
					"id":      "rec-1",
					"type":    "A",
					"name":    "int.1x1x1.dpdns.org",
					"content": update.Content,
					"ttl":     60,
					"proxied": false,
				},
			})
		case r.Method == http.MethodPatch && r.URL.Path == "/zones/zone-123/dns_records/rec-2":
			var update struct {
				Content string `json:"content"`
			}
			_ = json.NewDecoder(r.Body).Decode(&update)
			records["rec-2"] = update.Content
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result": map[string]any{
					"id":      "rec-2",
					"type":    "A",
					"name":    "master.1x1x1.dpdns.org",
					"content": update.Content,
					"ttl":     60,
					"proxied": false,
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockCF.Close()

	oldAPI := cloudflareAPI
	cloudflareAPI = mockCF.URL
	defer func() { cloudflareAPI = oldAPI }()

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := t.Context()
	if err := store.SetCloudflareSettings(ctx, "zone-123", "1x1x1.dpdns.org", "mock-token"); err != nil {
		t.Fatalf("SetCloudflareSettings failed: %v", err)
	}

	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}

	// Configure Master DDNS with both domains
	_, err = store.UpdateMasterDDNS(ctx, MasterDDNSSettings{
		Enabled:        true,
		RecordIDs:      []string{"rec-1", "rec-2"},
		Domains:        []string{"int.1x1x1.dpdns.org", "master.1x1x1.dpdns.org"},
		LastObservedIP: "161.118.209.69",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Trigger sync (force = true)
	ip, domains, err := server.SyncMasterDDNS(ctx, true)
	if err != nil {
		t.Fatalf("SyncMasterDDNS failed: %v", err)
	}
	if ip == "" {
		t.Fatal("expected non-empty IP")
	}
	if len(domains) != 2 {
		t.Fatalf("expected 2 updated domains, got %v", domains)
	}

	mu.Lock()
	defer mu.Unlock()
	if records["rec-1"] != ip || records["rec-2"] != ip {
		t.Fatalf("records not updated to IP %s: %+v", ip, records)
	}
}
