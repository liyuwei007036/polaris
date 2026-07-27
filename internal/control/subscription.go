package control

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sb-control/sb-control/internal/security"
)

const maxSubscriptionBytes = 10 * 1024 * 1024

type SubscriptionKind string

const (
	UpstreamSubscription SubscriptionKind = "upstream"
	ClientSubscription   SubscriptionKind = "client"
)

type Subscription struct {
	ID              string           `json:"id"`
	Kind            SubscriptionKind `json:"kind"`
	NodeID          string           `json:"node_id,omitempty"`
	Name            string           `json:"name"`
	URL             string           `json:"url,omitempty"`
	Format          string           `json:"format,omitempty"`
	EndpointIDs     []string         `json:"endpoint_ids,omitempty"`
	Enabled         bool             `json:"enabled"`
	LastStatus      string           `json:"last_status"`
	LastError       string           `json:"last_error,omitempty"`
	LastProcessedAt string           `json:"last_processed_at,omitempty"`
}

type SubscriptionInput struct {
	Kind        SubscriptionKind `json:"kind"`
	NodeID      string           `json:"node_id,omitempty"`
	Name        string           `json:"name"`
	URL         string           `json:"url,omitempty"`
	Format      string           `json:"format,omitempty"`
	EndpointIDs []string         `json:"endpoint_ids,omitempty"`
	Enabled     bool             `json:"enabled"`
}

func ValidateSubscription(subscription Subscription) error {
	if subscription.Kind != UpstreamSubscription && subscription.Kind != ClientSubscription {
		return errors.New("subscription kind must be upstream or client")
	}
	if subscription.Name == "" || len(subscription.Name) > 128 {
		return errors.New("subscription name up to 128 characters is required")
	}
	if subscription.Kind == ClientSubscription {
		if len(subscription.EndpointIDs) == 0 {
			return errors.New("client subscription requires at least one endpoint")
		}
		return nil
	}
	parsed, err := url.Parse(subscription.URL)
	if err != nil {
		return errors.New("subscription URL is invalid")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return errors.New("upstream subscription URL must use HTTP or HTTPS")
	}
	if parsed.Hostname() == "" || parsed.User != nil {
		return errors.New("upstream subscription URL host is invalid")
	}
	return nil
}

func (s *Store) CreateSubscription(ctx context.Context, input SubscriptionInput) (Subscription, string, error) {
	subscription := Subscription{Kind: input.Kind, NodeID: input.NodeID, Name: input.Name, URL: input.URL, Format: input.Format, EndpointIDs: input.EndpointIDs, Enabled: input.Enabled}
	if err := ValidateSubscription(subscription); err != nil {
		return Subscription{}, "", err
	}
	if input.Kind == UpstreamSubscription {
		if input.NodeID == "" || input.Format != "route_rules_v1" {
			return Subscription{}, "", errors.New("upstream subscription requires a node and route_rules_v1 format")
		}
		var node int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM nodes WHERE id = ? AND revoked_at IS NULL`, input.NodeID).Scan(&node); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return Subscription{}, "", ErrNotFound
			}
			return Subscription{}, "", err
		}
	} else if err := s.validateSubscriptionEndpoints(ctx, input.EndpointIDs); err != nil {
		return Subscription{}, "", err
	}
	identifier, err := newID()
	if err != nil {
		return Subscription{}, "", err
	}
	var encryptedURL []byte
	if input.Kind == UpstreamSubscription {
		encryptedURL, err = security.Encrypt(s.masterKey, []byte(input.URL))
		if err != nil {
			return Subscription{}, "", err
		}
	}
	endpointIDs, _ := json.Marshal(input.EndpointIDs)
	var token string
	var tokenHash []byte
	if input.Kind == ClientSubscription {
		token, err = security.RandomToken(32)
		if err != nil {
			return Subscription{}, "", err
		}
		tokenHash = security.TokenHash(token)
	}
	createdAt := nowUnix()
	_, err = s.db.ExecContext(ctx, `INSERT INTO subscriptions (id, kind, node_id, name, url_encrypted, format, endpoint_ids, token_hash, enabled, last_status, last_error, last_processed_at, generated_version, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'never', NULL, NULL, 0, ?, ?)`, identifier, input.Kind, nullableString(input.NodeID), input.Name, encryptedURL, input.Format, string(endpointIDs), tokenHash, input.Enabled, createdAt, createdAt)
	if err != nil {
		return Subscription{}, "", fmt.Errorf("create subscription: %w", err)
	}
	subscription.ID = identifier
	subscription.LastStatus = "never"
	return subscription, token, nil
}

func (s *Store) ListSubscriptions(ctx context.Context) ([]Subscription, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, COALESCE(node_id, ''), name, format, endpoint_ids, enabled, last_status, COALESCE(last_error, ''), COALESCE(last_processed_at, 0) FROM subscriptions ORDER BY kind, name, id`)
	if err != nil {
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}
	defer rows.Close()
	var subscriptions []Subscription
	for rows.Next() {
		var subscription Subscription
		var endpoints string
		var processed int64
		if err := rows.Scan(&subscription.ID, &subscription.Kind, &subscription.NodeID, &subscription.Name, &subscription.Format, &endpoints, &subscription.Enabled, &subscription.LastStatus, &subscription.LastError, &processed); err != nil {
			return nil, fmt.Errorf("read subscription: %w", err)
		}
		if err := json.Unmarshal([]byte(endpoints), &subscription.EndpointIDs); err != nil {
			return nil, fmt.Errorf("decode subscription endpoints: %w", err)
		}
		if processed != 0 {
			subscription.LastProcessedAt = time.Unix(processed, 0).UTC().Format(time.RFC3339)
		}
		subscriptions = append(subscriptions, subscription)
	}
	return subscriptions, rows.Err()
}

func (s *Store) DeleteSubscription(ctx context.Context, subscriptionID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM subscription_rules WHERE subscription_id = ?`, subscriptionID); err != nil {
		return fmt.Errorf("delete subscription rules: %w", err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM subscriptions WHERE id = ?`, subscriptionID)
	if err != nil {
		return fmt.Errorf("delete subscription: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *Store) SetSubscriptionEnabled(ctx context.Context, subscriptionID string, enabled bool) error {
	result, err := s.db.ExecContext(ctx, `UPDATE subscriptions SET enabled = ?, updated_at = ? WHERE id = ?`, enabled, nowUnix(), subscriptionID)
	if err != nil {
		return fmt.Errorf("set subscription state: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateSubscription(ctx context.Context, subscriptionID string, input SubscriptionInput) (Subscription, error) {
	if subscriptionID == "" {
		return Subscription{}, errors.New("subscription ID is required")
	}
	var kind SubscriptionKind
	if err := s.db.QueryRowContext(ctx, `SELECT kind FROM subscriptions WHERE id = ?`, subscriptionID).Scan(&kind); errors.Is(err, sql.ErrNoRows) {
		return Subscription{}, ErrNotFound
	} else if err != nil {
		return Subscription{}, err
	}
	if kind != input.Kind {
		return Subscription{}, errors.New("subscription kind cannot be changed")
	}
	probe := Subscription{Kind: input.Kind, NodeID: input.NodeID, Name: input.Name, URL: input.URL, Format: input.Format, EndpointIDs: input.EndpointIDs, Enabled: input.Enabled}
	if err := ValidateSubscription(probe); err != nil {
		return Subscription{}, err
	}
	if kind == UpstreamSubscription {
		if input.NodeID == "" || input.Format != "route_rules_v1" {
			return Subscription{}, errors.New("upstream subscription requires a node and route_rules_v1 format")
		}
		var node int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM nodes WHERE id=? AND revoked_at IS NULL`, input.NodeID).Scan(&node); errors.Is(err, sql.ErrNoRows) {
			return Subscription{}, ErrNotFound
		} else if err != nil {
			return Subscription{}, err
		}
	} else if err := s.validateSubscriptionEndpoints(ctx, input.EndpointIDs); err != nil {
		return Subscription{}, err
	}
	var encryptedURL []byte
	var err error
	if kind == UpstreamSubscription {
		encryptedURL, err = security.Encrypt(s.masterKey, []byte(input.URL))
		if err != nil {
			return Subscription{}, err
		}
	}
	endpoints, _ := json.Marshal(input.EndpointIDs)
	_, err = s.db.ExecContext(ctx, `UPDATE subscriptions SET node_id=?, name=?, url_encrypted=?, format=?, endpoint_ids=?, enabled=?, generated_version=generated_version+1, updated_at=? WHERE id=?`, nullableString(input.NodeID), input.Name, encryptedURL, input.Format, string(endpoints), input.Enabled, nowUnix(), subscriptionID)
	if err != nil {
		return Subscription{}, fmt.Errorf("update subscription: %w", err)
	}
	return s.subscriptionByID(ctx, subscriptionID)
}

func (s *Store) RotateClientSubscriptionToken(ctx context.Context, subscriptionID string) (string, error) {
	var kind SubscriptionKind
	var enabled bool
	err := s.db.QueryRowContext(ctx, `SELECT kind, enabled FROM subscriptions WHERE id=?`, subscriptionID).Scan(&kind, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if kind != ClientSubscription || !enabled {
		return "", errors.New("client subscription is disabled or has the wrong kind")
	}
	token, err := security.RandomToken(32)
	if err != nil {
		return "", err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE subscriptions SET token_hash=?, generated_version=generated_version+1, updated_at=? WHERE id=?`, security.TokenHash(token), nowUnix(), subscriptionID)
	if err != nil {
		return "", err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return "", ErrNotFound
	}
	return token, nil
}

func (s *Store) validateSubscriptionEndpoints(ctx context.Context, endpointIDs []string) error {
	seen := map[string]bool{}
	for _, endpointID := range endpointIDs {
		if endpointID == "" || seen[endpointID] {
			return errors.New("client subscription endpoints are invalid")
		}
		seen[endpointID] = true
		var found int
		err := s.db.QueryRowContext(ctx, `SELECT 1 FROM endpoints WHERE id = ?`, endpointID).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) RefreshUpstreamSubscription(ctx context.Context, subscriptionID string) (Subscription, error) {
	var subscription Subscription
	var encryptedURL []byte
	var endpointIDs string
	err := s.db.QueryRowContext(ctx, `SELECT id, kind, COALESCE(node_id, ''), name, url_encrypted, format, endpoint_ids, enabled FROM subscriptions WHERE id = ?`, subscriptionID).
		Scan(&subscription.ID, &subscription.Kind, &subscription.NodeID, &subscription.Name, &encryptedURL, &subscription.Format, &endpointIDs, &subscription.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return Subscription{}, ErrNotFound
	}
	if err != nil {
		return Subscription{}, fmt.Errorf("load upstream subscription: %w", err)
	}
	if subscription.Kind != UpstreamSubscription || !subscription.Enabled {
		return Subscription{}, errors.New("upstream subscription is disabled or has the wrong kind")
	}
	urlValue, err := security.Decrypt(s.masterKey, encryptedURL)
	if err != nil {
		return Subscription{}, fmt.Errorf("decrypt upstream subscription URL: %w", err)
	}
	content, fetchErr := FetchUpstreamSubscription(ctx, string(urlValue))
	if fetchErr != nil {
		_ = s.updateUpstreamSubscriptionStatus(ctx, subscriptionID, "failed", fetchErr.Error())
		return Subscription{}, fetchErr
	}
	var document struct {
		Version int         `json:"version"`
		Rules   []RouteRule `json:"rules"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil || document.Version != 1 {
		_ = s.updateUpstreamSubscriptionStatus(ctx, subscriptionID, "failed", "unsupported route_rules_v1 document")
		return Subscription{}, errors.New("upstream subscription must be a route_rules_v1 document with version 1")
	}
	for index := range document.Rules {
		document.Rules[index].NodeID = subscription.NodeID
		document.Rules[index].ID = "upstream-" + subscriptionID + "-" + fmt.Sprint(index)
		if err := ValidateRouteRule(document.Rules[index]); err != nil {
			_ = s.updateUpstreamSubscriptionStatus(ctx, subscriptionID, "failed", err.Error())
			return Subscription{}, fmt.Errorf("validate upstream rule %d: %w", index, err)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Subscription{}, fmt.Errorf("start upstream refresh: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM subscription_rules WHERE subscription_id = ?`, subscriptionID); err != nil {
		return Subscription{}, fmt.Errorf("clear prior upstream rules: %w", err)
	}
	for _, rule := range document.Rules {
		encoded, _ := json.Marshal(rule)
		identifier, err := newID()
		if err != nil {
			return Subscription{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO subscription_rules (id, subscription_id, rule_json, created_at) VALUES (?, ?, ?, ?)`, identifier, subscriptionID, string(encoded), nowUnix()); err != nil {
			return Subscription{}, fmt.Errorf("store upstream rule: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE subscriptions SET last_status = 'succeeded', last_error = NULL, last_processed_at = ?, generated_version = generated_version + 1, updated_at = ? WHERE id = ?`, nowUnix(), nowUnix(), subscriptionID); err != nil {
		return Subscription{}, fmt.Errorf("record upstream refresh: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Subscription{}, fmt.Errorf("commit upstream refresh: %w", err)
	}
	return s.subscriptionByID(ctx, subscriptionID)
}

func (s *Store) updateUpstreamSubscriptionStatus(ctx context.Context, subscriptionID, status, summary string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE subscriptions SET last_status = ?, last_error = ?, last_processed_at = ?, updated_at = ? WHERE id = ?`, status, summary, nowUnix(), nowUnix(), subscriptionID)
	return err
}

func (s *Store) subscriptionByID(ctx context.Context, subscriptionID string) (Subscription, error) {
	items, err := s.ListSubscriptions(ctx)
	if err != nil {
		return Subscription{}, err
	}
	for _, item := range items {
		if item.ID == subscriptionID {
			return item, nil
		}
	}
	return Subscription{}, ErrNotFound
}

func (s *Store) ListEffectiveRouteRules(ctx context.Context, nodeID string) ([]RouteRule, error) {
	rules, err := s.ListRouteRules(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.rule_json FROM subscription_rules r JOIN subscriptions s ON s.id = r.subscription_id WHERE s.kind = 'upstream' AND s.enabled = 1 AND s.node_id = ?`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("list upstream rules: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var rule RouteRule
		if err := json.Unmarshal([]byte(raw), &rule); err != nil {
			return nil, fmt.Errorf("decode upstream rule: %w", err)
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func (s *Store) GenerateClientSubscription(ctx context.Context, token string) (string, error) {
	var endpointJSON string
	var enabled bool
	err := s.db.QueryRowContext(ctx, `SELECT endpoint_ids, enabled FROM subscriptions WHERE kind = 'client' AND token_hash = ?`, security.TokenHash(token)).Scan(&endpointJSON, &enabled)
	if errors.Is(err, sql.ErrNoRows) || !enabled {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("load client subscription: %w", err)
	}
	var endpointIDs []string
	if err := json.Unmarshal([]byte(endpointJSON), &endpointIDs); err != nil {
		return "", err
	}
	lines := make([]string, 0, len(endpointIDs))
	for _, endpointID := range endpointIDs {
		line, err := s.clientSubscriptionLine(ctx, endpointID)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return "", err
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	// Whole-body base64 is the de facto V2Ray/Xray subscription convention
	// (v2rayN/v2rayNG/Shadowrocket/NekoBox/sing-box clients all expect it);
	// plain-text lines are not universally recognized as a subscription.
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(lines, "\n") + "\n")), nil
}

func (s *Store) clientSubscriptionLine(ctx context.Context, endpointID string) (string, error) {
	var endpoint endpointWithCredentials
	var encrypted []byte
	var listener Listener
	var spec string
	err := s.db.QueryRowContext(ctx, `SELECT e.id, e.listener_id, e.name, e.credentials, e.enabled, l.id, l.node_id, l.name, l.listen_address, l.port, l.backend_port, l.enabled, l.spec
		FROM endpoints e JOIN listeners l ON l.id = e.listener_id WHERE e.id = ? AND e.enabled = 1 AND l.enabled = 1`, endpointID).
		Scan(&endpoint.ID, &endpoint.ListenerID, &endpoint.Name, &encrypted, &endpoint.Enabled, &listener.ID, &listener.NodeID, &listener.Name, &listener.ListenAddr, &listener.Port, &listener.BackendPort, &listener.Enabled, &spec)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("load subscription endpoint: %w", err)
	}
	if err := json.Unmarshal([]byte(spec), &listener.Spec); err != nil {
		return "", err
	}
	plain, err := security.Decrypt(s.masterKey, encrypted)
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(plain, &endpoint.Credentials); err != nil {
		return "", err
	}
	host, port, name := listener.ListenAddr, strconv.Itoa(int(listener.Port)), url.QueryEscape(endpoint.Name)
	query := url.Values{}
	if listener.Spec.TLS.Enabled {
		query.Set("security", "tls")
		if listener.Spec.TLS.ServerName != "" {
			query.Set("sni", listener.Spec.TLS.ServerName)
		}
		if len(listener.Spec.TLS.ALPN) > 0 {
			query.Set("alpn", strings.Join(listener.Spec.TLS.ALPN, ","))
		}
	}
	if listener.Spec.Reality.Enabled {
		public, err := s.realityPublicKey(ctx, listener.Spec.Reality.KeyID)
		if err != nil {
			return "", err
		}
		query.Set("security", "reality")
		query.Set("pbk", public)
		if len(listener.Spec.Reality.ShortIDs) > 0 {
			query.Set("sid", listener.Spec.Reality.ShortIDs[0])
		}
	}
	suffix := ""
	if len(query) > 0 {
		suffix = "?" + query.Encode()
	}
	switch listener.Spec.Protocol {
	case "vless":
		return "vless://" + url.QueryEscape(endpoint.Credentials.UUID) + "@" + host + ":" + port + suffix + "#" + name, nil
	case "trojan":
		return "trojan://" + url.QueryEscape(endpoint.Credentials.Password) + "@" + host + ":" + port + suffix + "#" + name, nil
	case "shadowsocks":
		return "ss://" + base64.RawStdEncoding.EncodeToString([]byte(endpoint.Credentials.Method+":"+endpoint.Credentials.Password)) + "@" + host + ":" + port + "#" + name, nil
	case "hysteria2":
		return "hysteria2://" + url.QueryEscape(endpoint.Credentials.Password) + "@" + host + ":" + port + suffix + "#" + name, nil
	case "socks":
		return "socks://" + url.QueryEscape(endpoint.Credentials.Username) + ":" + url.QueryEscape(endpoint.Credentials.Password) + "@" + host + ":" + port + "#" + name, nil
	case "http":
		return "http://" + url.QueryEscape(endpoint.Credentials.Username) + ":" + url.QueryEscape(endpoint.Credentials.Password) + "@" + host + ":" + port + "#" + name, nil
	}
	return "", nil
}

func (s *Store) realityPublicKey(ctx context.Context, keyID string) (string, error) {
	var public string
	err := s.db.QueryRowContext(ctx, `SELECT public_key FROM managed_reality_keys WHERE id = ? AND enabled = 1`, keyID).Scan(&public)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return public, err
}

// FetchUpstreamSubscription resolves every target before dialing and rejects
// loopback, private, link-local, multicast and unspecified addresses.
func FetchUpstreamSubscription(ctx context.Context, rawURL string) ([]byte, error) {
	subscription := Subscription{Kind: UpstreamSubscription, Name: "fetch", URL: rawURL}
	if err := ValidateSubscription(subscription); err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(addresses) == 0 {
			return nil, errors.New("subscription host has no address")
		}
		for _, candidate := range addresses {
			if !isPublicAddress(candidate.IP) {
				return nil, fmt.Errorf("subscription host resolves to disallowed address %s", candidate.IP)
			}
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].IP.String(), port))
	}}
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("subscription redirect limit exceeded")
		}
		if request.URL.Scheme != "https" && request.URL.Scheme != "http" {
			return errors.New("subscription redirect protocol is not allowed")
		}
		return nil
	}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch upstream subscription: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("upstream subscription returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > maxSubscriptionBytes {
		return nil, errors.New("upstream subscription response is too large")
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, maxSubscriptionBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxSubscriptionBytes {
		return nil, errors.New("upstream subscription response is too large")
	}
	return content, nil
}

func isPublicAddress(address net.IP) bool {
	if address == nil || address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	if address.To4() != nil && strings.HasPrefix(address.String(), "100.64.") {
		return false
	}
	return true
}
