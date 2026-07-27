package control

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/sb-control/sb-control/internal/security"
	"github.com/sb-control/sb-control/internal/wire"
)

const sessionCookieName = "sb_control_session"

//go:embed web/index.html
var dashboardHTML []byte

type Server struct {
	store         *Store
	noiseKeypair  wire.Keypair
	secureCookies bool
	controlMu     sync.Mutex
	controls      map[string]*controlSession
	connHub       *connectionsHub
}

type controlSession struct {
	done  chan struct{}
	tasks chan Task
}

func NewServer(store *Store, secureCookies bool) (*Server, error) {
	keypair, err := LoadOrCreateNoiseKeypair(store.DataDir(), store.MasterKey())
	if err != nil {
		return nil, err
	}
	return &Server{store: store, noiseKeypair: keypair, secureCookies: secureCookies, controls: make(map[string]*controlSession), connHub: newConnectionsHub()}, nil
}

// NoisePublicKey returns the master's static public key, base64-encoded, for
// operators to pin into agent configuration (see cmd/sb-control "master
// show-pubkey") — the WireGuard-style replacement for distributing a CA cert.
func (s *Server) NoisePublicKey() [wire.KeySize]byte {
	return s.noiseKeypair.Public
}

// BrowserHandler serves the operator-facing web UI and API: plain HTTP is
// fine here (put a reverse proxy in front for public HTTPS if desired), since
// none of these routes depend on seeing a TLS client certificate. Agent
// traffic never touches this handler.
func (s *Server) BrowserHandler() http.Handler {
	mux := http.NewServeMux()
	s.registerBrowserRoutes(mux)
	return securityHeaders(mux)
}

// Handler is an alias for BrowserHandler kept for callers (mainly tests)
// that pre-date the split. Agent traffic is never HTTP — see ServeAgents.
func (s *Server) Handler() http.Handler {
	return s.BrowserHandler()
}

func (s *Server) registerBrowserRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /", s.dashboard)
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("POST /api/v1/auth/mfa", s.finishLogin)
	mux.HandleFunc("GET /api/v1/auth/me", s.me)
	mux.HandleFunc("POST /api/v1/auth/logout", s.logout)
	mux.HandleFunc("GET /api/v1/operators", s.listOperators)
	mux.HandleFunc("POST /api/v1/operators", s.createOperator)
	mux.HandleFunc("PUT /api/v1/operators/{id}", s.updateOperator)
	mux.HandleFunc("POST /api/v1/operators/{id}/password", s.setOperatorPassword)
	mux.HandleFunc("POST /api/v1/operators/{id}/totp/reset", s.resetOperatorTOTP)
	mux.HandleFunc("GET /api/v1/certificates", s.listManagedCertificates)
	mux.HandleFunc("POST /api/v1/certificates", s.createManagedCertificate)
	mux.HandleFunc("PUT /api/v1/certificates/{id}", s.replaceManagedCertificate)
	mux.HandleFunc("DELETE /api/v1/certificates/{id}", s.deleteManagedCertificate)
	mux.HandleFunc("GET /api/v1/reality-keys", s.listRealityKeys)
	mux.HandleFunc("POST /api/v1/reality-keys", s.createRealityKey)
	mux.HandleFunc("POST /api/v1/reality-keys/{id}/enabled", s.setRealityKeyEnabled)
	mux.HandleFunc("POST /api/v1/nodes/registration-tokens", s.createRegistrationToken)
	mux.HandleFunc("GET /api/v1/nodes", s.listNodes)
	mux.HandleFunc("GET /api/v1/nodes/{id}/metrics", s.nodeMetrics)
	mux.HandleFunc("GET /api/v1/nodes/{id}/firewall/rules", s.listFirewallRules)
	mux.HandleFunc("POST /api/v1/nodes/{id}/firewall/rules", s.createFirewallRule)
	mux.HandleFunc("POST /api/v1/nodes/{id}/firewall/publish", s.publishNodeFirewall)
	mux.HandleFunc("POST /api/v1/firewall/rules/{id}/enabled", s.setFirewallRuleEnabled)
	mux.HandleFunc("DELETE /api/v1/firewall/rules/{id}", s.deleteFirewallRule)
	mux.HandleFunc("GET /api/v1/nodes/{id}/fail2ban/jails", s.listFail2BanJails)
	mux.HandleFunc("POST /api/v1/nodes/{id}/fail2ban/jails", s.createFail2BanJail)
	mux.HandleFunc("POST /api/v1/nodes/{id}/fail2ban/publish", s.publishNodeFail2Ban)
	mux.HandleFunc("PUT /api/v1/fail2ban/jails/{id}", s.updateFail2BanJail)
	mux.HandleFunc("POST /api/v1/fail2ban/jails/{id}/enabled", s.setFail2BanJailEnabled)
	mux.HandleFunc("DELETE /api/v1/fail2ban/jails/{id}", s.deleteFail2BanJail)
	mux.HandleFunc("GET /api/v1/nodes/{id}/connections", s.nodeConnections)
	mux.HandleFunc("GET /api/v1/events/connections", s.browserConnectionsStream)
	mux.HandleFunc("GET /api/v1/cloudflare/settings", s.cloudflareSettings)
	mux.HandleFunc("PUT /api/v1/cloudflare/settings", s.setCloudflareSettings)
	mux.HandleFunc("GET /api/v1/cloudflare/records", s.listCloudflareRecords)
	mux.HandleFunc("POST /api/v1/cloudflare/records", s.createCloudflareRecord)
	mux.HandleFunc("PUT /api/v1/cloudflare/records/{id}", s.updateCloudflareRecord)
	mux.HandleFunc("DELETE /api/v1/cloudflare/records/{id}", s.deleteCloudflareRecord)
	mux.HandleFunc("POST /api/v1/cloudflare/records/{id}/publish", s.publishCloudflareRecord)
	mux.HandleFunc("POST /api/v1/cloudflare/sync", s.syncCloudflareRecords)
	mux.HandleFunc("GET /api/v1/tasks", s.listTasks)
	mux.HandleFunc("GET /api/v1/audit-events", s.listAuditEvents)
	mux.HandleFunc("GET /api/v1/subscriptions", s.listSubscriptions)
	mux.HandleFunc("POST /api/v1/subscriptions", s.createSubscription)
	mux.HandleFunc("PUT /api/v1/subscriptions/{id}", s.updateSubscription)
	mux.HandleFunc("POST /api/v1/subscriptions/{id}/refresh", s.refreshUpstreamSubscription)
	mux.HandleFunc("POST /api/v1/subscriptions/{id}/token/rotate", s.rotateClientSubscriptionToken)
	mux.HandleFunc("POST /api/v1/subscriptions/{id}/enabled", s.setSubscriptionEnabled)
	mux.HandleFunc("DELETE /api/v1/subscriptions/{id}", s.deleteSubscription)
	mux.HandleFunc("GET /api/v1/subscriptions/access/{token}", s.clientSubscriptionContent)
	mux.HandleFunc("POST /api/v1/nodes/{id}/configurations/publish", s.publishNodeConfiguration)
	mux.HandleFunc("POST /api/v1/nodes/{id}/nginx/publish", s.publishNodeNginx)
	mux.HandleFunc("GET /api/v1/sing-box/releases", s.listSingBoxReleases)
	mux.HandleFunc("POST /api/v1/sing-box/releases", s.createSingBoxRelease)
	mux.HandleFunc("POST /api/v1/nodes/{id}/sing-box/install", s.installSingBox)
	mux.HandleFunc("GET /api/v1/protocols", s.listProtocols)
	mux.HandleFunc("GET /api/v1/listeners", s.listListeners)
	mux.HandleFunc("POST /api/v1/listeners", s.createListener)
	mux.HandleFunc("PUT /api/v1/listeners/{id}", s.updateListener)
	mux.HandleFunc("POST /api/v1/listeners/{id}/enabled", s.setListenerEnabled)
	mux.HandleFunc("DELETE /api/v1/listeners/{id}", s.deleteListener)
	mux.HandleFunc("GET /api/v1/outbounds", s.listOutbounds)
	mux.HandleFunc("POST /api/v1/outbounds", s.createOutbound)
	mux.HandleFunc("PUT /api/v1/outbounds/{id}", s.updateOutbound)
	mux.HandleFunc("POST /api/v1/outbounds/{id}/enabled", s.setOutboundEnabled)
	mux.HandleFunc("DELETE /api/v1/outbounds/{id}", s.deleteOutbound)
	mux.HandleFunc("GET /api/v1/listeners/{id}/endpoints", s.listEndpoints)
	mux.HandleFunc("POST /api/v1/listeners/{id}/endpoints", s.createEndpoint)
	mux.HandleFunc("PUT /api/v1/endpoints/{id}", s.updateEndpoint)
	mux.HandleFunc("POST /api/v1/endpoints/{id}/enabled", s.setEndpointEnabled)
	mux.HandleFunc("DELETE /api/v1/endpoints/{id}", s.deleteEndpoint)
	mux.HandleFunc("GET /api/v1/ingress-routes", s.listIngressRoutes)
	mux.HandleFunc("POST /api/v1/ingress-routes", s.createIngressRoute)
	mux.HandleFunc("PUT /api/v1/ingress-routes/{id}", s.updateIngressRoute)
	mux.HandleFunc("DELETE /api/v1/ingress-routes/{id}", s.deleteIngressRoute)
	mux.HandleFunc("GET /api/v1/nodes/{id}/rules", s.listRouteRules)
	mux.HandleFunc("POST /api/v1/nodes/{id}/rules", s.createRouteRule)
	mux.HandleFunc("GET /api/v1/nodes/{id}/rules/preview", s.previewRouteRules)
	mux.HandleFunc("PUT /api/v1/rules/{id}", s.updateRouteRule)
	mux.HandleFunc("POST /api/v1/rules/{id}/enabled", s.setRouteRuleEnabled)
	mux.HandleFunc("POST /api/v1/rules/{id}/priority", s.setRouteRulePriority)
	mux.HandleFunc("DELETE /api/v1/rules/{id}", s.deleteRouteRule)
	mux.HandleFunc("GET /api/v1/registrations", s.listPendingRegistrations)
	mux.HandleFunc("POST /api/v1/nodes/{id}/approve", s.approveRegistration)
	mux.HandleFunc("POST /api/v1/nodes/{id}/revoke", s.revokeNode)
}

func (s *Server) dashboard(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
	_, _ = w.Write(dashboardHTML)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	challenge, err := s.store.StartLogin(r.Context(), input.Email, input.Password)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"challenge_id": challenge})
}

func (s *Server) finishLogin(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ChallengeID string `json:"challenge_id"`
		Code        string `json:"code"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	session, err := s.store.FinishLogin(r.Context(), input.ChallengeID, input.Code)
	if err != nil {
		writeError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    session.Token,
		Path:     "/",
		Expires:  session.ExpiresAt,
		HttpOnly: true,
		Secure:   s.secureCookies,
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]string{"csrf_token": session.CSRFToken, "role": session.Role})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	operator, err := s.operator(r, false)
	if err != nil {
		writeError(w, err)
		return
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		writeError(w, ErrUnauthorized)
		return
	}
	csrfToken, err := s.store.ReissueCSRF(r.Context(), cookie.Value)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":         operator.ID,
		"email":      operator.Email,
		"role":       operator.Role,
		"enabled":    operator.Enabled,
		"csrf_token": csrfToken,
	})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	_, err := s.operator(r, true)
	if err != nil {
		writeError(w, err)
		return
	}
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if err := s.store.DeleteSession(r.Context(), cookie.Value); err != nil {
			writeError(w, err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listOperators(w http.ResponseWriter, r *http.Request) {
	if _, err := s.admin(r); err != nil {
		writeError(w, err)
		return
	}
	operators, err := s.store.ListOperators(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"operators": operators})
}

func (s *Server) createOperator(w http.ResponseWriter, r *http.Request) {
	administrator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	operator, secret, err := s.store.CreateOperator(r.Context(), input.Email, input.Password, input.Role)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), administrator.ID, "operator.created", "operator", operator.ID, "operator created; MFA secret omitted"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"operator": operator, "totp_secret": secret})
}

func (s *Server) updateOperator(w http.ResponseWriter, r *http.Request) {
	administrator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if r.PathValue("id") == administrator.ID {
		writeError(w, ErrForbidden)
		return
	}
	var input struct {
		Role    string `json:"role"`
		Enabled bool   `json:"enabled"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	operator, err := s.store.UpdateOperator(r.Context(), Operator{ID: r.PathValue("id"), Role: input.Role, Enabled: input.Enabled})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), administrator.ID, "operator.updated", "operator", operator.ID, "operator role or enabled state updated"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, operator)
}

func (s *Server) setOperatorPassword(w http.ResponseWriter, r *http.Request) {
	administrator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := s.store.SetOperatorPassword(r.Context(), r.PathValue("id"), input.Password); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), administrator.ID, "operator.password_reset", "operator", r.PathValue("id"), "operator password reset"); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) resetOperatorTOTP(w http.ResponseWriter, r *http.Request) {
	administrator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	secret, err := s.store.ResetOperatorTOTP(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), administrator.ID, "operator.mfa_reset", "operator", r.PathValue("id"), "operator MFA reset; secret omitted"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"totp_secret": secret})
}

func (s *Server) listManagedCertificates(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	certificates, err := s.store.ListManagedCertificates(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"certificates": certificates})
}

func (s *Server) createManagedCertificate(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input ManagedCertificateInput
	if !decodeJSON(w, r, &input) {
		return
	}
	certificate, err := s.store.CreateManagedCertificate(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "certificate.created", "certificate", certificate.ID, "managed TLS certificate created; PEM omitted"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, certificate)
}

func (s *Server) replaceManagedCertificate(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input ManagedCertificateInput
	if !decodeJSON(w, r, &input) {
		return
	}
	certificate, err := s.store.ReplaceManagedCertificate(r.Context(), r.PathValue("id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "certificate.replaced", "certificate", certificate.ID, "managed TLS certificate replaced; PEM omitted"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, certificate)
}

func (s *Server) deleteManagedCertificate(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.DeleteManagedCertificate(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "certificate.deleted", "certificate", r.PathValue("id"), "managed TLS certificate deleted"); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listRealityKeys(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	keys, err := s.store.ListRealityKeys(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reality_keys": keys})
}

func (s *Server) createRealityKey(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
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
	key, privateKey, err := s.store.CreateRealityKey(r.Context(), input.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "reality_key.created", "reality_key", key.ID, "managed Reality key created; private key omitted"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"key": key, "private_key": privateKey})
}

func (s *Server) setRealityKeyEnabled(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
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
	if err := s.store.SetRealityKeyEnabled(r.Context(), r.PathValue("id"), input.Enabled); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "reality_key.state_changed", "reality_key", r.PathValue("id"), "managed Reality key state changed"); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) createRegistrationToken(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input struct {
		LifetimeSeconds int `json:"lifetime_seconds"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	ttl := defaultTokenTTL
	if input.LifetimeSeconds != 0 {
		ttl = time.Duration(input.LifetimeSeconds) * time.Second
	}
	token, err := s.store.CreateRegistrationToken(r.Context(), operator.ID, ttl)
	if err != nil {
		writeError(w, err)
		return
	}
	_ = s.store.AppendAudit(r.Context(), operator.ID, "registration_token.created", "registration_token", "", "lifetime recorded; token omitted")
	writeJSON(w, http.StatusCreated, map[string]string{"token": token.Token, "expires_at": token.ExpiresAt.Format(time.RFC3339)})
}

// DispatchTask persists before delivery; an offline agent receives it after
// reconnecting. Task kinds are validated by Store and agent independently.
func (s *Server) DispatchTask(ctx context.Context, task Task) (Task, error) {
	created, err := s.store.CreateTask(ctx, task)
	if err != nil {
		return Task{}, err
	}
	s.controlMu.Lock()
	session := s.controls[created.NodeID]
	s.controlMu.Unlock()
	if session != nil {
		select {
		case session.tasks <- created:
		default:
		}
	}
	return created, nil
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	tasks, err := s.store.ListTasks(r.Context(), r.URL.Query().Get("node_id"), r.URL.Query().Get("status"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (s *Server) listAuditEvents(w http.ResponseWriter, r *http.Request) {
	if _, err := s.admin(r); err != nil {
		writeError(w, err)
		return
	}
	limit, err := security.ParsePositiveInt(r.URL.Query().Get("limit"), 100, 500)
	if err != nil {
		writeError(w, err)
		return
	}
	events, err := s.store.ListAudit(r.Context(), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit_events": events})
}

func (s *Server) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	subscriptions, err := s.store.ListSubscriptions(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscriptions": subscriptions})
}

func (s *Server) createSubscription(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input SubscriptionInput
	if !decodeJSON(w, r, &input) {
		return
	}
	subscription, token, err := s.store.CreateSubscription(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "subscription.created", "subscription", subscription.ID, "subscription created; URL and access token omitted"); err != nil {
		writeError(w, err)
		return
	}
	response := map[string]any{"subscription": subscription}
	if token != "" {
		response["access_token"] = token
	}
	writeJSON(w, http.StatusCreated, response)
}

func (s *Server) refreshUpstreamSubscription(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	subscription, err := s.store.RefreshUpstreamSubscription(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "subscription.refreshed", "subscription", subscription.ID, "upstream subscription refreshed; content omitted"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, subscription)
}

func (s *Server) updateSubscription(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input SubscriptionInput
	if !decodeJSON(w, r, &input) {
		return
	}
	subscription, err := s.store.UpdateSubscription(r.Context(), r.PathValue("id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "subscription.updated", "subscription", subscription.ID, "subscription updated; URL and access token omitted"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, subscription)
}

func (s *Server) rotateClientSubscriptionToken(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	token, err := s.store.RotateClientSubscriptionToken(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "subscription.token_rotated", "subscription", r.PathValue("id"), "client subscription access token rotated; token omitted"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"access_token": token})
}

func (s *Server) setSubscriptionEnabled(w http.ResponseWriter, r *http.Request) {
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
	if err := s.store.SetSubscriptionEnabled(r.Context(), r.PathValue("id"), input.Enabled); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "subscription.state_changed", "subscription", r.PathValue("id"), "subscription enabled state changed"); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteSubscription(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.DeleteSubscription(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "subscription.deleted", "subscription", r.PathValue("id"), "subscription and associated imported rules deleted"); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) clientSubscriptionContent(w http.ResponseWriter, r *http.Request) {
	content, err := s.store.GenerateClientSubscription(r.Context(), r.PathValue("token"))
	if err != nil {
		writeError(w, ErrNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=sb-control-subscription.txt")
	_, _ = w.Write([]byte(content))
}

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	nodes, err := s.store.ListNodes(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
}

func (s *Server) nodeMetrics(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	report, updatedAt, err := s.store.NodeMetrics(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"node_id": r.PathValue("id"), "updated_at": updatedAt, "report": report})
}

func (s *Server) listFirewallRules(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	rules, err := s.store.ListFirewallRules(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules})
}

func (s *Server) createFirewallRule(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var rule FirewallRule
	if !decodeJSON(w, r, &rule) {
		return
	}
	rule.NodeID = r.PathValue("id")
	created, err := s.store.CreateFirewallRule(r.Context(), rule)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "firewall_rule.created", "firewall_rule", created.ID, "sb-control firewall rule created"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) setFirewallRuleEnabled(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
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
	if err := s.store.SetFirewallRuleEnabled(r.Context(), r.PathValue("id"), input.Enabled); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "firewall_rule.state_changed", "firewall_rule", r.PathValue("id"), "sb-control firewall rule state changed"); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteFirewallRule(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.DeleteFirewallRule(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "firewall_rule.deleted", "firewall_rule", r.PathValue("id"), "sb-control firewall rule deleted"); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) publishNodeFirewall(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	nodeID := r.PathValue("id")
	configuration, err := s.store.CompileNodeFirewall(r.Context(), nodeID)
	if err != nil {
		writeError(w, err)
		return
	}
	digest := sha256.Sum256([]byte(configuration))
	hash := hex.EncodeToString(digest[:])
	payload, err := json.Marshal(map[string]string{"configuration": configuration})
	if err != nil {
		writeError(w, err)
		return
	}
	task, err := s.DispatchTask(r.Context(), Task{NodeID: nodeID, OperatorID: operator.ID, Kind: "firewall.apply", IdempotencyKey: "firewall-" + hash, Payload: string(payload), ExpectedHash: hash})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "firewall.publish_requested", "node", nodeID, "sb-control firewall task requested"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, task)
}

func (s *Server) publishNodeConfiguration(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	nodeID := r.PathValue("id")
	configuration, hash, err := s.store.CompileNodeConfig(r.Context(), nodeID)
	if err != nil {
		writeError(w, err)
		return
	}
	payload, err := json.Marshal(map[string]string{"configuration": configuration})
	if err != nil {
		writeError(w, err)
		return
	}
	task, err := s.DispatchTask(r.Context(), Task{NodeID: nodeID, OperatorID: operator.ID, Kind: "singbox.apply_config", IdempotencyKey: "configuration-" + hash, Payload: string(payload), ExpectedHash: hash})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "configuration.publish_requested", "node", nodeID, "compiled configuration task requested"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, task)
}

func (s *Server) publishNodeNginx(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	nodeID := r.PathValue("id")
	configuration, hash, err := s.store.CompileNodeNginx(r.Context(), nodeID)
	if err != nil {
		writeError(w, err)
		return
	}
	payload, err := json.Marshal(map[string]string{"configuration": configuration})
	if err != nil {
		writeError(w, err)
		return
	}
	task, err := s.DispatchTask(r.Context(), Task{NodeID: nodeID, OperatorID: operator.ID, Kind: "nginx.apply_config", IdempotencyKey: "nginx-" + hash, Payload: string(payload), ExpectedHash: hash})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "nginx.publish_requested", "node", nodeID, "compiled Nginx stream task requested"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, task)
}

func (s *Server) listSingBoxReleases(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	releases, err := s.store.ListSingBoxReleases(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"releases": releases})
}

func (s *Server) createSingBoxRelease(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var release SingBoxRelease
	if !decodeJSON(w, r, &release) {
		return
	}
	created, err := s.store.CreateSingBoxRelease(r.Context(), release)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "singbox_release.created", "singbox_release", created.ID, "controlled sing-box release manifest created"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) installSingBox(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input struct {
		Version string `json:"version"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	nodeID := r.PathValue("id")
	node, err := s.store.GetNode(r.Context(), nodeID)
	if err != nil {
		writeError(w, err)
		return
	}
	if node.Architecture == "" {
		writeError(w, errors.New("node architecture is unavailable; wait for a successful agent heartbeat"))
		return
	}
	release, err := s.store.FindSingBoxRelease(r.Context(), input.Version, node.Architecture)
	if err != nil {
		writeError(w, err)
		return
	}
	payload, err := s.store.SignedSingBoxReleasePayload(release)
	if err != nil {
		writeError(w, err)
		return
	}
	kind := "singbox.install"
	if node.SingBox != "" {
		kind = "singbox.upgrade"
	}
	task, err := s.DispatchTask(r.Context(), Task{
		NodeID: nodeID, OperatorID: operator.ID, Kind: kind, IdempotencyKey: "singbox-" + release.Version + "-" + release.SHA256,
		Payload: payload, ExpectedHash: release.SHA256,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "singbox.install_requested", "node", nodeID, "signed sing-box installation task requested for version "+release.Version); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, task)
}

func (s *Server) listProtocols(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"protocols": SupportedProtocols()})
}

func (s *Server) listListeners(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	listeners, err := s.store.ListListeners(r.Context(), r.URL.Query().Get("node_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"listeners": listeners})
}

func (s *Server) createListener(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var listener Listener
	if !decodeJSON(w, r, &listener) {
		return
	}
	created, err := s.store.CreateListener(r.Context(), listener)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "listener.created", "listener", created.ID, "listener definition created"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateListener(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var listener Listener
	if !decodeJSON(w, r, &listener) {
		return
	}
	listener.ID = r.PathValue("id")
	updated, err := s.store.UpdateListener(r.Context(), listener)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "listener.updated", "listener", updated.ID, "listener definition updated"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) setListenerEnabled(w http.ResponseWriter, r *http.Request) {
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
	if err := s.store.SetListenerEnabled(r.Context(), r.PathValue("id"), input.Enabled); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "listener.state_changed", "listener", r.PathValue("id"), "listener enabled state changed"); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteListener(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.DeleteListener(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "listener.deleted", "listener", r.PathValue("id"), "listener and endpoints deleted"); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listOutbounds(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	outbounds, err := s.store.ListOutbounds(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"outbounds": outbounds})
}

func (s *Server) createOutbound(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var outbound Outbound
	if !decodeJSON(w, r, &outbound) {
		return
	}
	created, err := s.store.CreateOutbound(r.Context(), outbound)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "outbound.created", "outbound", created.ID, "outbound definition created"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateOutbound(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var outbound Outbound
	if !decodeJSON(w, r, &outbound) {
		return
	}
	outbound.ID = r.PathValue("id")
	updated, err := s.store.UpdateOutbound(r.Context(), outbound)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "outbound.updated", "outbound", updated.ID, "outbound definition updated"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) setOutboundEnabled(w http.ResponseWriter, r *http.Request) {
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
	if err := s.store.SetOutboundEnabled(r.Context(), r.PathValue("id"), input.Enabled); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "outbound.state_changed", "outbound", r.PathValue("id"), "outbound enabled state changed"); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteOutbound(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.DeleteOutbound(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "outbound.deleted", "outbound", r.PathValue("id"), "outbound deleted"); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listEndpoints(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	endpoints, err := s.store.ListEndpoints(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"endpoints": endpoints})
}

func (s *Server) createEndpoint(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input struct {
		Name        string              `json:"name"`
		Enabled     bool                `json:"enabled"`
		Credentials EndpointCredentials `json:"credentials"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	endpoint, err := s.store.CreateEndpoint(r.Context(), Endpoint{ListenerID: r.PathValue("id"), Name: input.Name, Enabled: input.Enabled}, input.Credentials)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "endpoint.created", "endpoint", endpoint.ID, "endpoint credentials created"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, endpoint)
}

func (s *Server) updateEndpoint(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input struct {
		ListenerID  string               `json:"listener_id"`
		Name        string               `json:"name"`
		Enabled     bool                 `json:"enabled"`
		Credentials *EndpointCredentials `json:"credentials"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	updated, err := s.store.UpdateEndpoint(r.Context(), Endpoint{ID: r.PathValue("id"), ListenerID: input.ListenerID, Name: input.Name, Enabled: input.Enabled}, input.Credentials)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "endpoint.updated", "endpoint", updated.ID, "endpoint definition updated"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) setEndpointEnabled(w http.ResponseWriter, r *http.Request) {
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
	if err := s.store.SetEndpointEnabled(r.Context(), r.PathValue("id"), input.Enabled); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "endpoint.state_changed", "endpoint", r.PathValue("id"), "endpoint enabled state changed"); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteEndpoint(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.DeleteEndpoint(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "endpoint.deleted", "endpoint", r.PathValue("id"), "endpoint credentials deleted"); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listIngressRoutes(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	routes, err := s.store.ListIngressRoutes(r.Context(), r.URL.Query().Get("node_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ingress_routes": routes})
}

func (s *Server) createIngressRoute(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var route IngressRoute
	if !decodeJSON(w, r, &route) {
		return
	}
	created, err := s.store.CreateIngressRoute(r.Context(), route)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "ingress_route.created", "ingress_route", created.ID, "Nginx SNI route created"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateIngressRoute(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var route IngressRoute
	if !decodeJSON(w, r, &route) {
		return
	}
	route.ID = r.PathValue("id")
	updated, err := s.store.UpdateIngressRoute(r.Context(), route)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "ingress_route.updated", "ingress_route", updated.ID, "Nginx SNI route updated"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteIngressRoute(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.DeleteIngressRoute(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "ingress_route.deleted", "ingress_route", r.PathValue("id"), "Nginx SNI route deleted"); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listRouteRules(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	rules, err := s.store.ListRouteRules(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules})
}

func (s *Server) createRouteRule(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var rule RouteRule
	if !decodeJSON(w, r, &rule) {
		return
	}
	rule.NodeID = r.PathValue("id")
	created, err := s.store.CreateRouteRule(r.Context(), rule)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "route_rule.created", "route_rule", created.ID, "structured route rule created"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) previewRouteRules(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	preview, err := s.store.PreviewNodeRules(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (s *Server) updateRouteRule(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var rule RouteRule
	if !decodeJSON(w, r, &rule) {
		return
	}
	rule.ID = r.PathValue("id")
	updated, err := s.store.UpdateRouteRule(r.Context(), rule)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "route_rule.updated", "route_rule", updated.ID, "structured route rule updated"); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) setRouteRuleEnabled(w http.ResponseWriter, r *http.Request) {
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
	if err := s.store.SetRouteRuleEnabled(r.Context(), r.PathValue("id"), input.Enabled); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "route_rule.state_changed", "route_rule", r.PathValue("id"), "route rule enabled state changed"); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setRouteRulePriority(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input struct {
		Priority int `json:"priority"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := s.store.SetRouteRulePriority(r.Context(), r.PathValue("id"), input.Priority); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "route_rule.priority_changed", "route_rule", r.PathValue("id"), "route rule priority changed"); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteRouteRule(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.DeleteRouteRule(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.AppendAudit(r.Context(), operator.ID, "route_rule.deleted", "route_rule", r.PathValue("id"), "route rule deleted"); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listPendingRegistrations(w http.ResponseWriter, r *http.Request) {
	if _, err := s.admin(r); err != nil {
		writeError(w, err)
		return
	}
	registrations, err := s.store.ListPendingRegistrations(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"registrations": registrations})
}

func (s *Server) approveRegistration(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	registration, err := s.store.ApproveRegistration(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	_ = s.store.AppendAudit(r.Context(), operator.ID, "node.registration_approved", "registration", registration.ID, "node public key approved")
	writeJSON(w, http.StatusOK, map[string]string{"node_id": registration.NodeID, "status": registration.Status})
}

func (s *Server) revokeNode(w http.ResponseWriter, r *http.Request) {
	operator, err := s.admin(r)
	if err != nil {
		writeError(w, err)
		return
	}
	nodeID := r.PathValue("id")
	if err := s.store.RevokeNode(r.Context(), nodeID); err != nil {
		writeError(w, err)
		return
	}
	s.disconnectAgent(nodeID)
	_ = s.store.AppendAudit(r.Context(), operator.ID, "node.certificate_revoked", "node", nodeID, "node revoked")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) disconnectAgent(nodeID string) {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	if session := s.controls[nodeID]; session != nil {
		close(session.done)
	}
}

func (s *Server) operator(r *http.Request, requireCSRF bool) (Operator, error) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return Operator{}, ErrUnauthorized
	}
	return s.store.Authenticate(r.Context(), cookie.Value, r.Header.Get("X-CSRF-Token"), requireCSRF)
}

func (s *Server) admin(r *http.Request) (Operator, error) {
	operator, err := s.operator(r, true)
	if err != nil {
		return Operator{}, err
	}
	if operator.Role != "admin" {
		return Operator{}, ErrForbidden
	}
	return operator, nil
}

// writer permits routine configuration work while reserving binary installs,
// credential authority, certificate revocation and destructive Listener actions
// for administrators.
func (s *Server) writer(r *http.Request) (Operator, error) {
	operator, err := s.operator(r, true)
	if err != nil {
		return Operator{}, err
	}
	if operator.Role != "admin" && operator.Role != "operator" {
		return Operator{}, ErrForbidden
	}
	return operator, nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	if r.Body == nil {
		writeError(w, errors.New("JSON body is required"))
		return false
	}
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, fmt.Errorf("invalid JSON: %w", err))
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeError(w, errors.New("JSON body must contain one object"))
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	message := "invalid request"
	switch {
	case errors.Is(err, ErrInvalidCredentials), errors.Is(err, ErrUnauthorized):
		status, message = http.StatusUnauthorized, "authentication failed"
	case errors.Is(err, ErrForbidden):
		status, message = http.StatusForbidden, "permission denied"
	case errors.Is(err, ErrNotFound):
		status, message = http.StatusNotFound, "not found"
	case errors.Is(err, ErrConflict):
		status, message = http.StatusConflict, "conflicting state"
	}
	writeJSON(w, status, map[string]string{"error": message})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
