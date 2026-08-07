package control

import (
	"context"
	"net/http"
	"strings"
)

func (s *Server) cloudflareSettings(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	view, err := s.store.CloudflareSettings(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) setCloudflareSettings(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input struct {
		ZoneID   string `json:"zone_id"`
		ZoneName string `json:"zone_name"`
		APIToken string `json:"api_token"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := s.store.SetCloudflareSettings(r.Context(), input.ZoneID, input.ZoneName, input.APIToken); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "cloudflare.settings_updated", "cloudflare", "settings", "Cloudflare zone and token updated"); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listCloudflareRecords(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	records, err := s.store.ListCloudflareRecords(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": records})
}

func (s *Server) listRemoteCloudflareRecords(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	zoneID, _, client, err := s.store.CloudflareZone(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	remotes, err := client.ListRecords(r.Context(), zoneID)
	if err != nil {
		writeError(w, err)
		return
	}
	managed, err := s.store.ListCloudflareRecords(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	matched := make(map[string]bool, len(managed))
	for _, record := range managed {
		if remote := findRemoteRecord(record, remotes); remote != nil {
			matched[remote.ID] = true
		}
	}
	unmanaged := make([]CloudflareRecord, 0, len(remotes))
	for _, remote := range remotes {
		if !matched[remote.ID] {
			unmanaged = append(unmanaged, remote)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": unmanaged})
}

func (s *Server) createCloudflareRecord(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var record ManagedCloudflareRecord
	if !decodeJSON(w, r, &record) {
		return
	}
	created, err := s.store.CreateCloudflareRecord(r.Context(), record)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "cloudflare_record.created", "cloudflare_record", created.ID, "Cloudflare record desired state created: "+created.Type+" "+created.Name); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateCloudflareRecord(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var record ManagedCloudflareRecord
	if !decodeJSON(w, r, &record) {
		return
	}
	record.ID = r.PathValue("id")
	updated, err := s.store.UpdateCloudflareRecord(r.Context(), record)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "cloudflare_record.updated", "cloudflare_record", updated.ID, "Cloudflare record desired state updated: "+updated.Type+" "+updated.Name); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) listOriginCertificates(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	certificates, err := s.store.ListOriginCertificates(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"certificates": certificates})
}

func (s *Server) createOriginCertificate(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input OriginCertificate
	if !decodeJSON(w, r, &input) {
		return
	}
	created, err := s.store.CreateOriginCertificate(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "origin_certificate.created", "origin_certificate", created.ID, "Origin certificate stored for "+created.Domain); err != nil {
		writeError(w, err)
		return
	}
	tasks, err := s.dispatchOriginCertificateNodes(r.Context(), operator.ID, created.Domain)
	if err != nil {
		writeError(w, err)
		return
	}
	setAutoApplyTaskHeaders(w, tasks)
	writeJSON(w, http.StatusCreated, created)
}

// dispatchOriginCertificateNodes pushes the recompiled configuration to every
// node whose listeners any of the given domain patterns covers. An edit passes
// both the old and the new pattern, so listeners the certificate no longer
// covers fall back to their self-signed certificate right away instead of
// waiting for the next reconcile.
func (s *Server) dispatchOriginCertificateNodes(ctx context.Context, operatorID string, domains ...string) ([]Task, error) {
	seen := map[string]bool{}
	nodeIDs := make([]string, 0, len(domains))
	for _, domain := range domains {
		ids, err := s.store.OriginCertificateNodeIDs(ctx, domain)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				nodeIDs = append(nodeIDs, id)
			}
		}
	}
	return s.dispatchNodeConfigurations(ctx, nodeIDs, operatorID)
}

func (s *Server) updateOriginCertificate(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input OriginCertificate
	if !decodeJSON(w, r, &input) {
		return
	}
	input.ID = r.PathValue("id")
	existing, err := s.store.OriginCertificateByID(r.Context(), input.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	updated, err := s.store.UpdateOriginCertificate(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "origin_certificate.updated", "origin_certificate", updated.ID, "Origin certificate updated for "+updated.Domain); err != nil {
		writeError(w, err)
		return
	}
	tasks, err := s.dispatchOriginCertificateNodes(r.Context(), operator.ID, existing.Domain, updated.Domain)
	if err != nil {
		writeError(w, err)
		return
	}
	setAutoApplyTaskHeaders(w, tasks)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteOriginCertificate(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	certificate, err := s.store.OriginCertificateByID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.DeleteOriginCertificate(r.Context(), certificate.ID); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "origin_certificate.deleted", "origin_certificate", certificate.ID, "Origin certificate deleted for "+certificate.Domain); err != nil {
		writeError(w, err)
		return
	}
	tasks, err := s.dispatchOriginCertificateNodes(r.Context(), operator.ID, certificate.Domain)
	if err != nil {
		writeError(w, err)
		return
	}
	setAutoApplyTaskHeaders(w, tasks)
	w.WriteHeader(http.StatusNoContent)
}

// findRemoteRecord locates the upstream record for a managed record, matching
// by stored remote ID first, then by name and type.
func findRemoteRecord(record ManagedCloudflareRecord, remotes []CloudflareRecord) *CloudflareRecord {
	for i := range remotes {
		if record.RemoteID != "" && remotes[i].ID == record.RemoteID {
			return &remotes[i]
		}
	}
	for i := range remotes {
		if strings.EqualFold(strings.TrimSuffix(remotes[i].Name, "."), record.Name) && strings.EqualFold(remotes[i].Type, record.Type) {
			return &remotes[i]
		}
	}
	return nil
}

func cloudflareDiff(record ManagedCloudflareRecord, remote *CloudflareRecord) map[string]any {
	desired := CloudflareRecord{Type: record.Type, Name: record.Name, Content: record.Content, TTL: record.TTL, Proxied: record.Proxied}
	diff := map[string]any{"desired": desired}
	if remote != nil {
		diff["remote"] = *remote
		diff["equal"] = CloudflareRecordEqual(record, *remote)
	} else {
		diff["remote"] = nil
		diff["equal"] = false
	}
	return diff
}

func (s *Server) publishCloudflareRecord(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input struct {
		Confirm bool `json:"confirm"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	record, err := s.store.CloudflareRecordByID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	zoneID, _, client, err := s.store.CloudflareZone(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	remotes, err := client.ListRecords(r.Context(), zoneID)
	if err != nil {
		_ = s.store.RecordCloudflareObservation(r.Context(), record.ID, record.RemoteID, "error", err.Error(), nil)
		writeError(w, err)
		return
	}
	remote := findRemoteRecord(record, remotes)
	diff := cloudflareDiff(record, remote)
	if !input.Confirm {
		// Change preview: the caller must resubmit with confirm=true.
		writeJSON(w, http.StatusOK, map[string]any{"record": record, "diff": diff, "requires_confirm": true})
		return
	}
	desired := CloudflareRecord{Type: record.Type, Name: record.Name, Content: record.Content, TTL: record.TTL, Proxied: record.Proxied}
	var applied CloudflareRecord
	if remote == nil {
		applied, err = client.CreateRecord(r.Context(), zoneID, desired)
	} else {
		desired.ID = remote.ID
		applied, err = client.UpdateRecord(r.Context(), zoneID, desired)
	}
	if err != nil {
		_ = s.store.RecordCloudflareObservation(r.Context(), record.ID, record.RemoteID, "error", err.Error(), nil)
		writeError(w, err)
		return
	}
	// Re-read to verify what Cloudflare actually stored.
	status := "synced"
	verified := &applied
	if remotes, listErr := client.ListRecords(r.Context(), zoneID); listErr == nil {
		if fresh := findRemoteRecord(ManagedCloudflareRecord{RemoteID: applied.ID, Name: record.Name, Type: record.Type}, remotes); fresh != nil {
			verified = fresh
		}
	}
	if !CloudflareRecordEqual(record, *verified) {
		status = "drift"
	}
	if err := s.store.RecordCloudflareObservation(r.Context(), record.ID, applied.ID, status, "", verified); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "cloudflare_record.published", "cloudflare_record", record.ID, "Cloudflare record written and verified: "+record.Type+" "+record.Name+" status="+status); err != nil {
		writeError(w, err)
		return
	}
	updated, err := s.store.CloudflareRecordByID(r.Context(), record.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"record": updated, "diff": cloudflareDiff(updated, verified)})
}

func (s *Server) deleteCloudflareRecord(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	record, err := s.store.CloudflareRecordByID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if record.RemoteID != "" {
		if r.URL.Query().Get("confirm") != "true" {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":  "record exists at Cloudflare; repeat the request with ?confirm=true to delete it upstream and locally",
				"record": record,
			})
			return
		}
		zoneID, _, client, err := s.store.CloudflareZone(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		if err := client.DeleteRecord(r.Context(), zoneID, record.RemoteID); err != nil {
			_ = s.store.RecordCloudflareObservation(r.Context(), record.ID, record.RemoteID, "error", err.Error(), nil)
			writeError(w, err)
			return
		}
	}
	if err := s.store.DeleteCloudflareRecordLocal(r.Context(), record.ID); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "cloudflare_record.deleted", "cloudflare_record", record.ID, "Cloudflare record deleted: "+record.Type+" "+record.Name); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// syncCloudflareRecords refreshes observed state for every managed record and
// reports drift. It never writes to Cloudflare.
func (s *Server) syncCloudflareRecords(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	zoneID, _, client, err := s.store.CloudflareZone(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	remotes, err := client.ListRecords(r.Context(), zoneID)
	if err != nil {
		writeError(w, err)
		return
	}
	records, err := s.store.ListCloudflareRecords(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	matched := map[string]bool{}
	drifted := 0
	for _, record := range records {
		remote := findRemoteRecord(record, remotes)
		if remote == nil {
			status := "pending"
			if record.RemoteID != "" {
				// Previously published but now absent upstream.
				status = "missing"
				drifted++
			}
			if err := s.store.RecordCloudflareObservation(r.Context(), record.ID, record.RemoteID, status, "", nil); err != nil {
				writeError(w, err)
				return
			}
			continue
		}
		matched[remote.ID] = true
		status := "synced"
		if !CloudflareRecordEqual(record, *remote) {
			status = "drift"
			drifted++
		}
		if err := s.store.RecordCloudflareObservation(r.Context(), record.ID, remote.ID, status, "", remote); err != nil {
			writeError(w, err)
			return
		}
	}
	var unmanaged []CloudflareRecord
	for _, remote := range remotes {
		if !matched[remote.ID] {
			unmanaged = append(unmanaged, remote)
		}
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "cloudflare.synced", "cloudflare", "zone", "Cloudflare zone state read for drift detection"); err != nil {
		writeError(w, err)
		return
	}
	updated, err := s.store.ListCloudflareRecords(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": updated, "unmanaged": unmanaged, "drifted": drifted})
}
