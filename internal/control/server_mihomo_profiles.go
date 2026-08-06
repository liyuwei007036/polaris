package control

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/sb-control/sb-control/internal/security"
)

func (s *Server) listMihomoProxyGroups(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	groups, err := s.store.ListMihomoProxyGroups(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"proxy_groups": groups})
}

func (s *Server) createMihomoProxyGroup(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input MihomoProxyGroup
	if !decodeJSON(w, r, &input) {
		return
	}
	created, err := s.store.CreateMihomoProxyGroup(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "mihomo.proxy_group.created", "mihomo_proxy_group", created.ID, "Mihomo proxy group created"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateMihomoProxyGroup(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input MihomoProxyGroup
	if !decodeJSON(w, r, &input) {
		return
	}
	input.ID = r.PathValue("id")
	updated, err := s.store.UpdateMihomoProxyGroup(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "mihomo.proxy_group.updated", "mihomo_proxy_group", updated.ID, "Mihomo proxy group updated"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteMihomoProxyGroup(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	id := r.PathValue("id")
	if err := s.store.DeleteMihomoProxyGroup(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "mihomo.proxy_group.deleted", "mihomo_proxy_group", id, "Mihomo proxy group deleted"); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listMihomoRoutingProfiles(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	profiles, err := s.store.ListMihomoRoutingProfiles(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"routing_profiles": profiles})
}

func (s *Server) createMihomoRoutingProfile(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input MihomoRoutingProfile
	if !decodeJSON(w, r, &input) {
		return
	}
	created, err := s.store.CreateMihomoRoutingProfile(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "mihomo.routing_profile.created", "mihomo_routing_profile", created.ID, "Mihomo routing profile created"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateMihomoRoutingProfile(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input MihomoRoutingProfile
	if !decodeJSON(w, r, &input) {
		return
	}
	input.ID = r.PathValue("id")
	updated, err := s.store.UpdateMihomoRoutingProfile(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "mihomo.routing_profile.updated", "mihomo_routing_profile", updated.ID, "Mihomo routing profile updated"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteMihomoRoutingProfile(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	id := r.PathValue("id")
	if err := s.store.DeleteMihomoRoutingProfile(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "mihomo.routing_profile.deleted", "mihomo_routing_profile", id, "Mihomo routing profile deleted"); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listMihomoClientConfigs(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	configs, err := s.store.ListMihomoClientConfigs(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"client_configs": configs})
}

func (s *Server) createMihomoClientConfig(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input MihomoClientConfig
	if !decodeJSON(w, r, &input) {
		return
	}
	created, err := s.store.CreateMihomoClientConfig(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "mihomo.client_config.created", "mihomo_client_config", created.ID, "Mihomo client config created"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// copyMihomoClientConfig duplicates a configuration under a new name. The copy
// gets its own update address: sharing one would mean revoking either config's
// address revoked the other's too.
func (s *Server) copyMihomoClientConfig(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	created, err := s.store.CopyMihomoClientConfig(r.Context(), r.PathValue("id"), input.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "mihomo.client_config.created", "mihomo_client_config", created.ID, "Mihomo client config copied"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) listMihomoRuleProviders(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	providers, err := s.store.ListMihomoRuleProviders(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rule_providers": providers})
}

func (s *Server) createMihomoRuleProvider(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input MihomoRuleProvider
	if !decodeJSON(w, r, &input) {
		return
	}
	created, err := s.store.CreateMihomoRuleProvider(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "mihomo.rule_provider.created", "mihomo_rule_provider", created.ID, "Mihomo rule provider created"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateMihomoRuleProvider(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input MihomoRuleProvider
	if !decodeJSON(w, r, &input) {
		return
	}
	input.ID = r.PathValue("id")
	updated, err := s.store.UpdateMihomoRuleProvider(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "mihomo.rule_provider.updated", "mihomo_rule_provider", updated.ID, "Mihomo rule provider updated"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteMihomoRuleProvider(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	id := r.PathValue("id")
	if err := s.store.DeleteMihomoRuleProvider(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "mihomo.rule_provider.deleted", "mihomo_rule_provider", id, "Mihomo rule provider deleted"); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) updateMihomoClientConfig(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input MihomoClientConfig
	if !decodeJSON(w, r, &input) {
		return
	}
	input.ID = r.PathValue("id")
	updated, err := s.store.UpdateMihomoClientConfig(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "mihomo.client_config.updated", "mihomo_client_config", updated.ID, "Mihomo client config updated"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteMihomoClientConfig(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	id := r.PathValue("id")
	if err := s.store.DeleteMihomoClientConfig(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "mihomo.client_config.deleted", "mihomo_client_config", id, "Mihomo client config deleted"); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setMihomoClientConfigEnabled(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	id := r.PathValue("id")
	if err := s.store.SetMihomoClientConfigEnabled(r.Context(), id, input.Enabled); err != nil {
		writeError(w, err)
		return
	}
	action := "disabled"
	if input.Enabled {
		action = "enabled"
	}
	_ = s.store.AppendAudit(r.Context(), operator.ID, "mihomo.client_config."+action, "mihomo_client_config", id, "Mihomo client config "+action)
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": input.Enabled})
}

func (s *Server) rotateMihomoClientSubscription(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	path, err := s.store.RotateMihomoClientSubscription(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	_ = s.store.AppendAudit(r.Context(), operator.ID, "mihomo.subscription_rotated", "mihomo_client_config", r.PathValue("id"), "Mihomo subscription link regenerated; token omitted")
	writeJSON(w, http.StatusOK, map[string]string{"subscription_path": path})
}

func (s *Server) mihomoClientSubscription(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.PathValue("token"))
	if token == "" || len(token) > 256 {
		writeError(w, ErrNotFound)
		return
	}
	configID, err := s.store.MihomoClientConfigIDByToken(r.Context(), token)
	if err != nil {
		writeError(w, err)
		return
	}
	name, yaml, err := s.store.GenerateStoredMihomoYAML(r.Context(), configID)
	if err != nil {
		writeError(w, err)
		return
	}
	ip := requestIP(r.RemoteAddr, map[string]string{
		"CF-Connecting-IP": r.Header.Get("CF-Connecting-IP"),
		"X-Real-IP":        r.Header.Get("X-Real-IP"),
		"X-Forwarded-For":  r.Header.Get("X-Forwarded-For"),
	})
	if err := s.store.RecordSubscriptionAccess(r.Context(), configID, name, ip, s.ipLocator.Locate(ip), r.UserAgent()); err != nil {
		writeError(w, err)
		return
	}
	filename := strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' {
			return r
		}
		return '-'
	}, name)
	if filename == "" {
		filename = "mihomo"
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	utf8Filename := url.PathEscape(name + ".yaml")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.yaml"; filename*=UTF-8''%s`, filename, utf8Filename))
	w.Header().Set("Profile-Update-Interval", "24")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(yaml))
}

func (s *Server) listMihomoSubscriptionAccess(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	page, err := security.ParsePositiveInt(r.URL.Query().Get("page"), 1, 1_000_000)
	if err != nil {
		writeError(w, err)
		return
	}
	pageSize, err := security.ParsePositiveInt(r.URL.Query().Get("page_size"), 20, 100)
	if err != nil {
		writeError(w, err)
		return
	}
	items, total, err := s.store.ListSubscriptionAccess(r.Context(), SubscriptionAccessFilter{
		ConfigID: r.URL.Query().Get("config_id"), Location: r.URL.Query().Get("location"),
		IP: r.URL.Query().Get("ip"), UserAgent: r.URL.Query().Get("user_agent"),
	}, page, pageSize)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"access_logs": items, "total": total, "page": page, "page_size": pageSize})
}
