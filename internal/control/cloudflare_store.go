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

// CloudflareRecordView is a record as it exists at Cloudflare right now,
// annotated with what this platform serves under that name. Cloudflare is the
// only store, so there is no desired-versus-observed state to reconcile and no
// status to keep in sync.
type CloudflareRecordView struct {
	CloudflareRecord
	// Bindings say which server and access service the record actually
	// serves. They are derived on every read from the access services'
	// connection domains and the servers' connection addresses, so an
	// operator never declares them by hand.
	Bindings []CloudflareBinding `json:"bindings"`
}

// CloudflareBinding names one access service (or, when only the address
// matches, one server) that a DNS record reaches.
type CloudflareBinding struct {
	NodeName     string `json:"node_name"`
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

// normalizeCloudflareRecord checks an operator-supplied record and rejects what
// Cloudflare or the access services behind the name could not serve. Everything
// that survives is written upstream immediately.
func (s *Store) normalizeCloudflareRecord(ctx context.Context, record *CloudflareRecord) (*CloudflareClient, string, error) {
	record.Name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(record.Name)), ".")
	record.Type = strings.ToUpper(strings.TrimSpace(record.Type))
	if record.Type != "A" && record.Type != "AAAA" && record.Type != "CNAME" && record.Type != "TXT" {
		return nil, "", userErrorf("记录类型只支持 A、AAAA、CNAME 或 TXT")
	}
	if !validSNI(record.Name) {
		return nil, "", userErrorf("域名格式不正确")
	}
	zoneID, zoneName, client, err := s.CloudflareZone(ctx)
	if err != nil {
		return nil, "", err
	}
	if record.Name != zoneName && !strings.HasSuffix(record.Name, "."+zoneName) {
		return nil, "", userErrorf("域名必须属于区域 %s", zoneName)
	}
	record.Content = strings.TrimSpace(record.Content)
	if record.Content == "" {
		return nil, "", userErrorf("请填写指向地址或内容")
	}
	if record.Proxied {
		// Cloudflare forces automatic TTL on proxied records.
		record.TTL = 1
	}
	if record.TTL != 1 && (record.TTL < 60 || record.TTL > 86400) {
		return nil, "", userErrorf("缓存时间必须为 1（自动）或 60 到 86400 秒之间")
	}
	if err := s.checkCloudflareProxyFits(ctx, *record); err != nil {
		return nil, "", err
	}
	return client, zoneID, nil
}

// checkCloudflareProxyFits refuses an orange cloud that would break the access
// services already published under the same domain. A name this platform does
// not serve is left to the operator: it may well front something else.
func (s *Store) checkCloudflareProxyFits(ctx context.Context, record CloudflareRecord) error {
	if !record.Proxied {
		return nil
	}
	listeners, err := s.listenersByConnectionDomain(ctx, record.Name)
	if err != nil {
		return err
	}
	for _, listener := range listeners {
		switch {
		case listener.Spec.Reality.Enabled:
			return userErrorf("接入服务「%s」使用 Reality，开启加速后将无法连接", listener.Name)
		case listener.Spec.Network != "tcp":
			return userErrorf("接入服务「%s」使用 UDP，开启加速后将无法连接", listener.Name)
		}
		if err := ValidateCloudflareProxy(record.Type, listener.Spec.Protocol, listener.Spec.Transport.Type, listener.Port, listener.Spec.TLS.Enabled, true); err != nil {
			return userErrorf("接入服务「%s」不支持加速：%v", listener.Name, err)
		}
	}
	return nil
}

func (s *Store) listenersByConnectionDomain(ctx context.Context, domain string) ([]Listener, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, node_id, name, connection_domain, listen_address, port, backend_port, enabled, spec
		FROM listeners WHERE LOWER(connection_domain) = ? ORDER BY name, id`, domain)
	if err != nil {
		return nil, fmt.Errorf("list listeners by connection domain: %w", err)
	}
	defer rows.Close()
	var listeners []Listener
	for rows.Next() {
		var listener Listener
		var spec string
		if err := rows.Scan(&listener.ID, &listener.NodeID, &listener.Name, &listener.Domain, &listener.ListenAddr,
			&listener.Port, &listener.BackendPort, &listener.Enabled, &spec); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(spec), &listener.Spec); err != nil {
			return nil, fmt.Errorf("decode listener spec: %w", err)
		}
		listeners = append(listeners, listener)
	}
	return listeners, rows.Err()
}

// ListCloudflareRecords reads the zone from Cloudflare and annotates every
// record with the access services it reaches. Cloudflare is the source of
// truth, so a record removed in its console simply stops appearing here.
func (s *Store) ListCloudflareRecords(ctx context.Context) ([]CloudflareRecordView, error) {
	zoneID, _, client, err := s.CloudflareZone(ctx)
	if err != nil {
		return nil, err
	}
	records, err := client.ListRecords(ctx, zoneID)
	if err != nil {
		return nil, err
	}
	return s.annotateCloudflareRecords(ctx, records)
}

func (s *Store) CreateCloudflareRecord(ctx context.Context, record CloudflareRecord) (CloudflareRecordView, error) {
	client, zoneID, err := s.normalizeCloudflareRecord(ctx, &record)
	if err != nil {
		return CloudflareRecordView{}, err
	}
	record.ID = ""
	created, err := client.CreateRecord(ctx, zoneID, record)
	if err != nil {
		return CloudflareRecordView{}, err
	}
	return s.annotateCloudflareRecord(ctx, created)
}

func (s *Store) UpdateCloudflareRecord(ctx context.Context, record CloudflareRecord) (CloudflareRecordView, error) {
	if strings.TrimSpace(record.ID) == "" {
		return CloudflareRecordView{}, userErrorf("缺少域名记录编号")
	}
	client, zoneID, err := s.normalizeCloudflareRecord(ctx, &record)
	if err != nil {
		return CloudflareRecordView{}, err
	}
	updated, err := client.UpdateRecord(ctx, zoneID, record)
	if err != nil {
		return CloudflareRecordView{}, err
	}
	return s.annotateCloudflareRecord(ctx, updated)
}

// DeleteCloudflareRecord removes the record at Cloudflare and returns what was
// removed, so the audit trail names the record rather than only its ID.
func (s *Store) DeleteCloudflareRecord(ctx context.Context, recordID string) (CloudflareRecord, error) {
	zoneID, _, client, err := s.CloudflareZone(ctx)
	if err != nil {
		return CloudflareRecord{}, err
	}
	record, err := client.GetRecord(ctx, zoneID, recordID)
	if err != nil {
		return CloudflareRecord{}, err
	}
	if err := client.DeleteRecord(ctx, zoneID, recordID); err != nil {
		return CloudflareRecord{}, err
	}
	return record, nil
}

func (s *Store) annotateCloudflareRecord(ctx context.Context, record CloudflareRecord) (CloudflareRecordView, error) {
	views, err := s.annotateCloudflareRecords(ctx, []CloudflareRecord{record})
	if err != nil {
		return CloudflareRecordView{}, err
	}
	return views[0], nil
}

// annotateCloudflareRecords resolves each record to what this platform serves
// under it: an access service whose connection domain is that name, or, failing
// that, the server the record points at.
func (s *Store) annotateCloudflareRecords(ctx context.Context, records []CloudflareRecord) ([]CloudflareRecordView, error) {
	byDomain, byAddress, err := s.cloudflareBindingIndex(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]CloudflareRecordView, 0, len(records))
	for _, record := range records {
		name := strings.TrimSuffix(strings.ToLower(record.Name), ".")
		bindings := byDomain[name]
		if len(bindings) == 0 {
			for _, nodeName := range byAddress[strings.TrimSuffix(strings.ToLower(record.Content), ".")] {
				bindings = append(bindings, CloudflareBinding{NodeName: nodeName})
			}
		}
		if bindings == nil {
			bindings = []CloudflareBinding{}
		}
		views = append(views, CloudflareRecordView{CloudflareRecord: record, Bindings: bindings})
	}
	return views, nil
}

func (s *Store) cloudflareBindingIndex(ctx context.Context) (map[string][]CloudflareBinding, map[string][]string, error) {
	byDomain := map[string][]CloudflareBinding{}
	rows, err := s.db.QueryContext(ctx, `SELECT LOWER(l.connection_domain), n.name, l.name, l.port
		FROM listeners l JOIN nodes n ON n.id = l.node_id
		WHERE l.connection_domain <> '' AND n.revoked_at IS NULL
		ORDER BY n.name, l.name, l.id`)
	if err != nil {
		return nil, nil, fmt.Errorf("index access service domains: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var domain string
		var binding CloudflareBinding
		if err := rows.Scan(&domain, &binding.NodeName, &binding.ListenerName, &binding.ListenerPort); err != nil {
			return nil, nil, err
		}
		byDomain[domain] = append(byDomain[domain], binding)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	byAddress := map[string][]string{}
	addressRows, err := s.db.QueryContext(ctx, `SELECT LOWER(client_address), name FROM nodes
		WHERE client_address <> '' AND revoked_at IS NULL ORDER BY name, id`)
	if err != nil {
		return nil, nil, fmt.Errorf("index server addresses: %w", err)
	}
	defer addressRows.Close()
	for addressRows.Next() {
		var address, nodeName string
		if err := addressRows.Scan(&address, &nodeName); err != nil {
			return nil, nil, err
		}
		byAddress[address] = append(byAddress[address], nodeName)
	}
	return byDomain, byAddress, addressRows.Err()
}
