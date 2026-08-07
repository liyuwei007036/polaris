package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/liyuwei007036/polaris/internal/security"
)

// ManagedCloudflareRecord stores the operator-declared desired state next to
// the last state actually observed at Cloudflare. Observed state is never
// written back automatically; drift only surfaces as a status.
type ManagedCloudflareRecord struct {
	ID         string `json:"id"`
	RemoteID   string `json:"remote_id,omitempty"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Content    string `json:"content"`
	TTL        int    `json:"ttl"`
	Proxied    bool   `json:"proxied"`
	NodeID     string `json:"node_id,omitempty"`
	ListenerID string `json:"listener_id,omitempty"`
	Status     string `json:"status"`
	LastError  string `json:"last_error,omitempty"`
	Observed   string `json:"observed,omitempty"`
	ObservedAt string `json:"observed_at,omitempty"`
	// NodeName and ListenerName resolve the bindings above to what an
	// operator recognizes, so the DNS list shows which server and which
	// access service a record actually points at.
	NodeName     string `json:"node_name,omitempty"`
	ListenerName string `json:"listener_name,omitempty"`
	ListenerPort uint16 `json:"listener_port,omitempty"`
}

// CloudflareSettingsView separates "an operator saved a zone and a token" from
// "that token still reaches the zone". Only the latter may be shown as
// connected, so a revoked token or an unreachable network surfaces instead of
// staying hidden behind a stale label.
type CloudflareSettingsView struct {
	Configured  bool   `json:"configured"`
	Connected   bool   `json:"connected"`
	Error       string `json:"error,omitempty"`
	ZoneID      string `json:"zone_id,omitempty"`
	ZoneName    string `json:"zone_name,omitempty"`
	TokenMasked string `json:"token_masked,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

// cloudflareProbeTimeout keeps the connection check on the settings page short:
// a blocked network must fail fast instead of holding the page for the client's
// full request timeout.
const cloudflareProbeTimeout = 8 * time.Second

func (s *Store) SetCloudflareSettings(ctx context.Context, zoneID, zoneName, token string) error {
	zoneID = strings.TrimSpace(zoneID)
	zoneName = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(zoneName)), ".")
	if zoneID == "" || !validSNI(zoneName) {
		return errors.New("Cloudflare zone ID and zone name are required")
	}
	if strings.TrimSpace(token) == "" {
		return errors.New("Cloudflare API token is required")
	}
	encrypted, err := security.Encrypt(s.masterKey, []byte(token))
	if err != nil {
		return fmt.Errorf("encrypt Cloudflare token: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO cloudflare_settings (id, zone_id, zone_name, api_token, updated_at) VALUES (1, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET zone_id=excluded.zone_id, zone_name=excluded.zone_name, api_token=excluded.api_token, updated_at=excluded.updated_at`,
		zoneID, zoneName, encrypted, nowUnix())
	if err != nil {
		return fmt.Errorf("store Cloudflare settings: %w", err)
	}
	return nil
}

// VerifyCloudflareCredentials proves a zone and token combination works before
// it is stored, so "connected" can never mean "an operator once typed this in".
func VerifyCloudflareCredentials(ctx context.Context, zoneID, zoneName, token string) error {
	client, err := NewCloudflareClient(token)
	if err != nil {
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, cloudflareProbeTimeout)
	defer cancel()
	zone, err := client.VerifyZone(probeCtx, strings.TrimSpace(zoneID))
	if err != nil {
		return err
	}
	wanted := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(zoneName)), ".")
	if remote := strings.TrimSuffix(strings.ToLower(zone.Name), "."); remote != "" && remote != wanted {
		return userErrorf("该区域编号对应的域名是 %s，与填写的 %s 不一致", remote, wanted)
	}
	return nil
}

func (s *Store) CloudflareSettings(ctx context.Context) (CloudflareSettingsView, error) {
	var view CloudflareSettingsView
	var encrypted []byte
	var updatedAt int64
	err := s.db.QueryRowContext(ctx, `SELECT zone_id, zone_name, api_token, updated_at FROM cloudflare_settings WHERE id = 1`).Scan(&view.ZoneID, &view.ZoneName, &encrypted, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return CloudflareSettingsView{}, nil
	}
	if err != nil {
		return CloudflareSettingsView{}, fmt.Errorf("load Cloudflare settings: %w", err)
	}
	token, err := security.Decrypt(s.masterKey, encrypted)
	if err != nil {
		return CloudflareSettingsView{}, fmt.Errorf("decrypt Cloudflare token: %w", err)
	}
	view.Configured = true
	view.TokenMasked = maskSecret(string(token))
	view.UpdatedAt = time.Unix(updatedAt, 0).UTC().Format(time.RFC3339)
	client, err := NewCloudflareClient(string(token))
	if err != nil {
		view.Error = err.Error()
		return view, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, cloudflareProbeTimeout)
	defer cancel()
	if _, err := client.VerifyZone(probeCtx, view.ZoneID); err != nil {
		// A stored token that no longer works is a state to report, not a
		// request failure: the page still needs to render and offer a fix.
		view.Error = err.Error()
		return view, nil
	}
	view.Connected = true
	return view, nil
}

// CloudflareZone returns the configured zone and a ready API client. The token
// never leaves the master process.
func (s *Store) CloudflareZone(ctx context.Context) (string, string, *CloudflareClient, error) {
	var zoneID, zoneName string
	var encrypted []byte
	err := s.db.QueryRowContext(ctx, `SELECT zone_id, zone_name, api_token FROM cloudflare_settings WHERE id = 1`).Scan(&zoneID, &zoneName, &encrypted)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil, errors.New("Cloudflare is not configured")
	}
	if err != nil {
		return "", "", nil, fmt.Errorf("load Cloudflare settings: %w", err)
	}
	token, err := security.Decrypt(s.masterKey, encrypted)
	if err != nil {
		return "", "", nil, fmt.Errorf("decrypt Cloudflare token: %w", err)
	}
	client, err := NewCloudflareClient(string(token))
	if err != nil {
		return "", "", nil, err
	}
	return zoneID, zoneName, client, nil
}

func maskSecret(value string) string {
	if len(value) <= 8 {
		return "****"
	}
	return value[:4] + "…" + value[len(value)-4:]
}

func (s *Store) validateCloudflareRecord(ctx context.Context, record *ManagedCloudflareRecord) error {
	record.Name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(record.Name)), ".")
	record.Type = strings.ToUpper(strings.TrimSpace(record.Type))
	if record.Type != "A" && record.Type != "AAAA" && record.Type != "CNAME" && record.Type != "TXT" {
		return errors.New("record type must be A, AAAA, CNAME or TXT")
	}
	if !validSNI(record.Name) {
		return errors.New("record name must be a valid DNS name")
	}
	_, zoneName, _, err := s.CloudflareZone(ctx)
	if err != nil {
		return err
	}
	if record.Name != zoneName && !strings.HasSuffix(record.Name, "."+zoneName) {
		return fmt.Errorf("record name must be inside zone %s", zoneName)
	}
	if strings.TrimSpace(record.Content) == "" {
		return errors.New("record content is required")
	}
	if record.Proxied {
		// Cloudflare forces automatic TTL on proxied records.
		record.TTL = 1
	}
	if record.TTL != 1 && (record.TTL < 60 || record.TTL > 86400) {
		return errors.New("record TTL must be 1 (auto) or between 60 and 86400 seconds")
	}
	if record.ListenerID != "" {
		listener, err := s.listenerByID(ctx, record.ListenerID)
		if err != nil {
			return err
		}
		if record.NodeID == "" {
			record.NodeID = listener.NodeID
		} else if record.NodeID != listener.NodeID {
			return errors.New("record node binding does not match the listener's node")
		}
		if record.Proxied {
			if listener.Spec.Reality.Enabled {
				return errors.New("Reality listeners must stay grey-cloud (DNS only)")
			}
			if listener.Spec.Network != "tcp" {
				return errors.New("UDP listeners must stay grey-cloud (DNS only)")
			}
			if err := ValidateCloudflareProxy(record.Type, listener.Spec.Protocol, listener.Spec.Transport.Type, listener.Port, listener.Spec.TLS.Enabled, true); err != nil {
				return err
			}
		}
	}
	// An orange-cloud record that is not bound to a listener is left alone:
	// records already living at Cloudflare often serve something other than an
	// access service, and refusing to save them made them uneditable here.
	if record.NodeID != "" {
		var one int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM nodes WHERE id=? AND revoked_at IS NULL`, record.NodeID).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) listenerByID(ctx context.Context, listenerID string) (Listener, error) {
	var listener Listener
	var spec string
	err := s.db.QueryRowContext(ctx, `SELECT id, node_id, name, connection_domain, listen_address, port, backend_port, enabled, spec FROM listeners WHERE id = ?`, listenerID).
		Scan(&listener.ID, &listener.NodeID, &listener.Name, &listener.Domain, &listener.ListenAddr, &listener.Port, &listener.BackendPort, &listener.Enabled, &spec)
	if errors.Is(err, sql.ErrNoRows) {
		return Listener{}, ErrNotFound
	}
	if err != nil {
		return Listener{}, fmt.Errorf("load listener: %w", err)
	}
	if err := json.Unmarshal([]byte(spec), &listener.Spec); err != nil {
		return Listener{}, fmt.Errorf("decode listener spec: %w", err)
	}
	return listener, nil
}

func (s *Store) CreateCloudflareRecord(ctx context.Context, record ManagedCloudflareRecord) (ManagedCloudflareRecord, error) {
	if err := s.validateCloudflareRecord(ctx, &record); err != nil {
		return ManagedCloudflareRecord{}, err
	}
	id, err := newID()
	if err != nil {
		return ManagedCloudflareRecord{}, err
	}
	record.ID = id
	record.Status = "pending"
	_, err = s.db.ExecContext(ctx, `INSERT INTO cloudflare_records (id,remote_id,name,type,content,ttl,proxied,node_id,listener_id,status,created_at,updated_at)
		VALUES (?, '', ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
		record.ID, record.Name, record.Type, record.Content, record.TTL, record.Proxied, nullableString(record.NodeID), nullableString(record.ListenerID), nowUnix(), nowUnix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ManagedCloudflareRecord{}, ErrConflict
		}
		return ManagedCloudflareRecord{}, fmt.Errorf("create Cloudflare record: %w", err)
	}
	return record, nil
}

func (s *Store) UpdateCloudflareRecord(ctx context.Context, record ManagedCloudflareRecord) (ManagedCloudflareRecord, error) {
	if record.ID == "" {
		return ManagedCloudflareRecord{}, errors.New("Cloudflare record ID is required")
	}
	if err := s.validateCloudflareRecord(ctx, &record); err != nil {
		return ManagedCloudflareRecord{}, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE cloudflare_records SET name=?,type=?,content=?,ttl=?,proxied=?,node_id=?,listener_id=?,status='pending',last_error='',updated_at=? WHERE id=?`,
		record.Name, record.Type, record.Content, record.TTL, record.Proxied, nullableString(record.NodeID), nullableString(record.ListenerID), nowUnix(), record.ID)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ManagedCloudflareRecord{}, ErrConflict
		}
		return ManagedCloudflareRecord{}, fmt.Errorf("update Cloudflare record: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ManagedCloudflareRecord{}, ErrNotFound
	}
	return s.CloudflareRecordByID(ctx, record.ID)
}

func (s *Store) ListCloudflareRecords(ctx context.Context) ([]ManagedCloudflareRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id,r.remote_id,r.name,r.type,r.content,r.ttl,r.proxied,COALESCE(r.node_id,''),COALESCE(r.listener_id,''),
		r.status,r.last_error,r.observed,COALESCE(r.observed_at,0),COALESCE(n.name,''),COALESCE(l.name,''),COALESCE(l.port,0)
		FROM cloudflare_records r
		LEFT JOIN nodes n ON n.id = r.node_id
		LEFT JOIN listeners l ON l.id = r.listener_id
		ORDER BY r.name,r.type,r.id`)
	if err != nil {
		return nil, fmt.Errorf("list Cloudflare records: %w", err)
	}
	defer rows.Close()
	var records []ManagedCloudflareRecord
	for rows.Next() {
		record, err := scanCloudflareRecordWithBindings(rows.Scan)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) CloudflareRecordByID(ctx context.Context, recordID string) (ManagedCloudflareRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,remote_id,name,type,content,ttl,proxied,COALESCE(node_id,''),COALESCE(listener_id,''),status,last_error,observed,COALESCE(observed_at,0)
		FROM cloudflare_records WHERE id=?`, recordID)
	record, err := scanCloudflareRecord(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return ManagedCloudflareRecord{}, ErrNotFound
	}
	if err != nil {
		return ManagedCloudflareRecord{}, fmt.Errorf("load Cloudflare record: %w", err)
	}
	return record, nil
}

func scanCloudflareRecordWithBindings(scan func(...any) error) (ManagedCloudflareRecord, error) {
	var record ManagedCloudflareRecord
	var observedAt int64
	if err := scan(&record.ID, &record.RemoteID, &record.Name, &record.Type, &record.Content, &record.TTL, &record.Proxied,
		&record.NodeID, &record.ListenerID, &record.Status, &record.LastError, &record.Observed, &observedAt,
		&record.NodeName, &record.ListenerName, &record.ListenerPort); err != nil {
		return ManagedCloudflareRecord{}, err
	}
	if observedAt != 0 {
		record.ObservedAt = time.Unix(observedAt, 0).UTC().Format(time.RFC3339)
	}
	return record, nil
}

func scanCloudflareRecord(scan func(...any) error) (ManagedCloudflareRecord, error) {
	var record ManagedCloudflareRecord
	var observedAt int64
	if err := scan(&record.ID, &record.RemoteID, &record.Name, &record.Type, &record.Content, &record.TTL, &record.Proxied,
		&record.NodeID, &record.ListenerID, &record.Status, &record.LastError, &record.Observed, &observedAt); err != nil {
		return ManagedCloudflareRecord{}, err
	}
	if observedAt != 0 {
		record.ObservedAt = time.Unix(observedAt, 0).UTC().Format(time.RFC3339)
	}
	return record, nil
}

func (s *Store) DeleteCloudflareRecordLocal(ctx context.Context, recordID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM cloudflare_records WHERE id=?`, recordID)
	if err != nil {
		return fmt.Errorf("delete Cloudflare record: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrNotFound
	}
	return nil
}

// RecordCloudflareObservation persists what was actually read from Cloudflare
// without touching the desired state. An empty remote means the record is
// absent upstream.
func (s *Store) RecordCloudflareObservation(ctx context.Context, recordID, remoteID, status, lastError string, remote *CloudflareRecord) error {
	observed := ""
	if remote != nil {
		encoded, err := json.Marshal(remote)
		if err != nil {
			return fmt.Errorf("encode observed Cloudflare record: %w", err)
		}
		observed = string(encoded)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE cloudflare_records SET remote_id=?,status=?,last_error=?,observed=?,observed_at=?,updated_at=? WHERE id=?`,
		remoteID, status, lastError, observed, nowUnix(), nowUnix(), recordID)
	if err != nil {
		return fmt.Errorf("record Cloudflare observation: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrNotFound
	}
	return nil
}

// CloudflareRecordEqual reports whether the desired record matches an observed
// remote record on the fields polaris manages.
func CloudflareRecordEqual(desired ManagedCloudflareRecord, remote CloudflareRecord) bool {
	if !strings.EqualFold(desired.Name, strings.TrimSuffix(remote.Name, ".")) || !strings.EqualFold(desired.Type, remote.Type) {
		return false
	}
	if desired.Content != remote.Content || desired.Proxied != remote.Proxied {
		return false
	}
	// Proxied records always report TTL 1; only compare TTL when grey-cloud.
	if !desired.Proxied && desired.TTL != remote.TTL {
		return false
	}
	return true
}
