package control

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
)

func (s *Server) getMasterDDNS(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	settings, err := s.store.GetMasterDDNS(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}

	currentIP, _ := fetchMasterPublicIP(r.Context())

	writeJSON(w, http.StatusOK, map[string]any{
		"settings":   settings,
		"current_ip": currentIP,
	})
}

func (s *Server) updateMasterDDNS(w http.ResponseWriter, r *http.Request) {
	op, err := s.operator(r, false)
	if err != nil {
		writeError(w, err)
		return
	}
	if op.Role != "admin" && op.Role != "operator" {
		writeError(w, ErrForbidden)
		return
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeError(w, userErrorf("读取 Master DDNS 配置失败: %v", err))
		return
	}

	var in struct {
		Enabled   bool     `json:"enabled"`
		RecordIDs []string `json:"record_ids"`
		Domains   []string `json:"domains"`
	}
	if err := json.Unmarshal(bodyBytes, &in); err != nil {
		writeError(w, userErrorf("提交的 Master DDNS 配置格式无效: %v", err))
		return
	}

	updated, err := s.store.UpdateMasterDDNS(r.Context(), MasterDDNSSettings{
		Enabled:   in.Enabled,
		RecordIDs: in.RecordIDs,
		Domains:   in.Domains,
	})
	if err != nil {
		writeError(w, err)
		return
	}

	_ = s.store.AppendAudit(r.Context(), op.ID, "master.ddns_configured", "master", "ddns", "Master DDNS 配置已更新")

	if in.Enabled {
		go func() {
			_, _, _ = s.SyncMasterDDNS(context.Background(), true)
		}()
	}

	writeJSON(w, http.StatusOK, map[string]any{"settings": updated})
}

func (s *Server) syncMasterDDNS(w http.ResponseWriter, r *http.Request) {
	op, err := s.operator(r, false)
	if err != nil {
		writeError(w, err)
		return
	}
	if op.Role != "admin" && op.Role != "operator" {
		writeError(w, ErrForbidden)
		return
	}

	ip, domains, err := s.SyncMasterDDNS(r.Context(), true)
	if err != nil {
		writeError(w, userErrorf("同步 Master DDNS 失败: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ip":      ip,
		"domains": domains,
		"message": "Master DDNS 同步成功",
	})
}
