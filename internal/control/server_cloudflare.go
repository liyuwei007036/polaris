package control

import (
	"context"
	"net/http"
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
	if err := VerifyCloudflareCredentials(r.Context(), input.ZoneID, input.ZoneName, input.APIToken); err != nil {
		writeError(w, err)
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

// The DNS pages read and write Cloudflare directly: what the list shows is what
// the zone holds, and a save or a delete lands upstream in the same request.
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

func (s *Server) createCloudflareRecord(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var record CloudflareRecord
	if !decodeJSON(w, r, &record) {
		return
	}
	created, err := s.store.CreateCloudflareRecord(r.Context(), record)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "cloudflare_record.created", "cloudflare_record", created.ID, "Cloudflare record created: "+created.Type+" "+created.Name); err != nil {
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
	var record CloudflareRecord
	if !decodeJSON(w, r, &record) {
		return
	}
	record.ID = r.PathValue("id")
	updated, err := s.store.UpdateCloudflareRecord(r.Context(), record)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "cloudflare_record.updated", "cloudflare_record", updated.ID, "Cloudflare record updated: "+updated.Type+" "+updated.Name); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteCloudflareRecord(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	deleted, err := s.store.DeleteCloudflareRecord(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "cloudflare_record.deleted", "cloudflare_record", deleted.ID, "Cloudflare record deleted: "+deleted.Type+" "+deleted.Name); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
