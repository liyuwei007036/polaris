package control

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/sb-control/sb-control/internal/security"
)

type SubscriptionKind string

const (
	ClientSubscription SubscriptionKind = "client"
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
	Name        string           `json:"name"`
	EndpointIDs []string         `json:"endpoint_ids,omitempty"`
	Enabled     bool             `json:"enabled"`
}

func ValidateSubscription(subscription Subscription) error {
	if subscription.Kind != ClientSubscription {
		return errors.New("only client subscriptions are supported")
	}
	if subscription.Name == "" || len(subscription.Name) > 128 {
		return errors.New("subscription name up to 128 characters is required")
	}
	if len(subscription.EndpointIDs) == 0 {
		return errors.New("client subscription requires at least one endpoint")
	}
	return nil
}

func (s *Store) CreateSubscription(ctx context.Context, input SubscriptionInput) (Subscription, string, error) {
	subscription := Subscription{Kind: input.Kind, Name: input.Name, EndpointIDs: input.EndpointIDs, Enabled: input.Enabled}
	if err := ValidateSubscription(subscription); err != nil {
		return Subscription{}, "", err
	}
	if err := s.validateSubscriptionEndpoints(ctx, input.EndpointIDs); err != nil {
		return Subscription{}, "", err
	}
	identifier, err := newID()
	if err != nil {
		return Subscription{}, "", err
	}
	endpointIDs, _ := json.Marshal(input.EndpointIDs)
	token, err := security.RandomToken(32)
	if err != nil {
		return Subscription{}, "", err
	}
	tokenHash := security.TokenHash(token)
	createdAt := nowUnix()
	_, err = s.db.ExecContext(ctx, `INSERT INTO subscriptions (id, kind, node_id, name, url_encrypted, format, endpoint_ids, token_hash, enabled, last_status, last_error, last_processed_at, generated_version, created_at, updated_at)
		VALUES (?, ?, NULL, ?, NULL, '', ?, ?, ?, 'never', NULL, NULL, 0, ?, ?)`, identifier, input.Kind, input.Name, string(endpointIDs), tokenHash, input.Enabled, createdAt, createdAt)
	if err != nil {
		return Subscription{}, "", fmt.Errorf("create subscription: %w", err)
	}
	subscription.ID = identifier
	subscription.LastStatus = "never"
	return subscription, token, nil
}

func (s *Store) ListSubscriptions(ctx context.Context) ([]Subscription, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, COALESCE(node_id, ''), name, format, endpoint_ids, enabled, last_status, COALESCE(last_error, ''), COALESCE(last_processed_at, 0) FROM subscriptions WHERE kind = 'client' ORDER BY name, id`)
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
	if kind != ClientSubscription {
		return Subscription{}, errors.New("only client subscriptions are supported")
	}
	probe := Subscription{Kind: input.Kind, Name: input.Name, EndpointIDs: input.EndpointIDs, Enabled: input.Enabled}
	if err := ValidateSubscription(probe); err != nil {
		return Subscription{}, err
	}
	if err := s.validateSubscriptionEndpoints(ctx, input.EndpointIDs); err != nil {
		return Subscription{}, err
	}
	endpoints, _ := json.Marshal(input.EndpointIDs)
	_, err := s.db.ExecContext(ctx, `UPDATE subscriptions SET node_id=NULL, name=?, url_encrypted=NULL, format='', endpoint_ids=?, enabled=?, generated_version=generated_version+1, updated_at=? WHERE id=?`, input.Name, string(endpoints), input.Enabled, nowUnix(), subscriptionID)
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
	return s.ListRouteRules(ctx, nodeID)
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
	var clientAddress, nodeName string
	err := s.db.QueryRowContext(ctx, `SELECT e.id, e.listener_id, e.name, e.alias, e.credentials, e.enabled, l.id, l.node_id, l.name, l.connection_domain, l.listen_address, l.port, l.backend_port, l.enabled, l.spec, n.client_address, n.name
		FROM endpoints e JOIN listeners l ON l.id = e.listener_id JOIN nodes n ON n.id = l.node_id
		WHERE e.id = ? AND e.enabled = 1 AND l.enabled = 1 AND n.revoked_at IS NULL`, endpointID).
		Scan(&endpoint.ID, &endpoint.ListenerID, &endpoint.Name, &endpoint.Alias, &encrypted, &endpoint.Enabled, &listener.ID, &listener.NodeID, &listener.Name, &listener.Domain, &listener.ListenAddr, &listener.Port, &listener.BackendPort, &listener.Enabled, &spec, &clientAddress, &nodeName)
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
	host := strings.TrimSpace(listener.Domain)
	if host == "" {
		host = strings.TrimSpace(clientAddress)
	}
	if host == "" {
		return "", fmt.Errorf("node %s has no client connection address", nodeName)
	}
	displayName := strings.TrimSpace(endpoint.Alias)
	if displayName == "" {
		displayName = nodeName + " · " + listener.Name + " · " + endpoint.Name
	}
	address, name := net.JoinHostPort(host, fmt.Sprint(listener.Port)), url.QueryEscape(displayName)
	query := url.Values{}
	if listener.Spec.Protocol == "vless" {
		query.Set("encryption", "none")
		if listener.Spec.TLS.Enabled {
			query.Set("security", "tls")
			if listener.Domain != "" {
				query.Set("sni", listener.Domain)
			}
		}
		if len(listener.Spec.TLS.ALPN) > 0 {
			query.Set("alpn", strings.Join(listener.Spec.TLS.ALPN, ","))
		}
		switch listener.Spec.Transport.Type {
		case "ws":
			query.Set("type", "ws")
			query.Set("path", listener.Spec.Transport.Path)
			if listener.Spec.Transport.Host != "" {
				query.Set("host", listener.Spec.Transport.Host)
			}
		case "grpc":
			query.Set("type", "grpc")
			query.Set("serviceName", listener.Spec.Transport.ServiceName)
		default:
			query.Set("type", "tcp")
		}
	}
	if listener.Spec.Reality.Enabled {
		public, err := s.realityPublicKey(ctx, listener.Spec.Reality.KeyID)
		if err != nil {
			return "", err
		}
		query.Set("security", "reality")
		query.Set("sni", listener.Spec.Reality.HandshakeServer)
		query.Set("pbk", public)
		if len(listener.Spec.Reality.ShortIDs) > 0 {
			query.Set("sid", listener.Spec.Reality.ShortIDs[0])
		}
	}
	if listener.Spec.Protocol == "hysteria2" {
		query.Set("insecure", "1")
		if listener.Domain != "" {
			query.Set("sni", listener.Domain)
		}
	}
	suffix := ""
	if len(query) > 0 {
		suffix = "?" + query.Encode()
	}
	switch listener.Spec.Protocol {
	case "vless":
		return "vless://" + url.QueryEscape(endpoint.Credentials.UUID) + "@" + address + suffix + "#" + name, nil
	case "hysteria2":
		return "hysteria2://" + url.QueryEscape(endpoint.Credentials.Password) + "@" + address + suffix + "#" + name, nil
	}
	return "", fmt.Errorf("unsupported inbound protocol %q", listener.Spec.Protocol)
}

func (s *Store) realityPublicKey(ctx context.Context, keyID string) (string, error) {
	var public string
	err := s.db.QueryRowContext(ctx, `SELECT public_key FROM managed_reality_keys WHERE id = ? AND enabled = 1`, keyID).Scan(&public)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return public, err
}
