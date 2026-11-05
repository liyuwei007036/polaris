package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/liyuwei007036/polaris/internal/security"
)

// CloudflareRecordView is a record as it exists at Cloudflare right now,
// annotated with what this platform serves under that name. Cloudflare is the
// only store, so there is no desired-versus-observed state to reconcile and no
// status to keep in sync.
type CloudflareRecordView struct {
	CloudflareRecord
	// ZoneID and ZoneName say which configured zone the record lives in. A
	// record ID is only meaningful together with its zone.
	ZoneID   string `json:"zone_id"`
	ZoneName string `json:"zone_name"`
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

// CloudflareZoneView is one configured zone and whether its token still
// reaches it. Every zone is verified independently so one revoked token does
// not make the other zones look broken, nor hide the one that is.
type CloudflareZoneView struct {
	ZoneID      string `json:"zone_id"`
	ZoneName    string `json:"zone_name"`
	Connected   bool   `json:"connected"`
	Error       string `json:"error,omitempty"`
	TokenMasked string `json:"token_masked,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

// CloudflareZoneFailure reports a zone that could not be read while the other
// zones were still listed.
type CloudflareZoneFailure struct {
	ZoneID   string `json:"zone_id"`
	ZoneName string `json:"zone_name"`
	Error    string `json:"error"`
}

// CloudflareSettingsView separates "an operator saved a zone and a token" from
// "that token still reaches the zone". Only the latter may be shown as
// connected, so a revoked token or an unreachable network surfaces instead of
// staying hidden behind a stale label.
type CloudflareSettingsView struct {
	Configured  bool                 `json:"configured"`
	Connected   bool                 `json:"connected"`
	Error       string               `json:"error,omitempty"`
	ZoneID      string               `json:"zone_id,omitempty"`
	ZoneName    string               `json:"zone_name,omitempty"`
	TokenMasked string               `json:"token_masked,omitempty"`
	UpdatedAt   string               `json:"updated_at,omitempty"`
	Zones       []CloudflareZoneView `json:"zones"`
}

// CloudflareZone is a configured zone with a ready API client. The token never
// leaves the master process.
type CloudflareZone struct {
	ID     string
	Name   string
	Client *CloudflareClient
}

// cloudflareProbeTimeout keeps the connection check on the settings page short:
// a blocked network must fail fast instead of holding the page for the client's
// full request timeout.
const cloudflareProbeTimeout = 8 * time.Second

// SetCloudflareSettings adds a zone or updates its token. Zones are keyed by
// Cloudflare's zone ID, so re-saving the same zone replaces its credentials
// instead of creating a second entry.
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
	now := nowUnix()
	_, err = s.db.ExecContext(ctx, `INSERT INTO cloudflare_zones (zone_id, zone_name, api_token, created_at, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(zone_id) DO UPDATE SET zone_name=excluded.zone_name, api_token=excluded.api_token, updated_at=excluded.updated_at`,
		zoneID, zoneName, encrypted, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: cloudflare_zones.zone_name") {
			return userErrorf("域名 %s 已绑定到另一个区域，请先删除原区域", zoneName)
		}
		return fmt.Errorf("store Cloudflare settings: %w", err)
	}
	return nil
}

// DeleteCloudflareSettings removes one zone. DNS records that live in that
// zone stay at Cloudflare; this only stops this platform from managing them.
func (s *Store) DeleteCloudflareSettings(ctx context.Context, zoneID string) error {
	zoneID = strings.TrimSpace(zoneID)
	if zoneID == "" {
		return userErrorf("缺少区域编号")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM cloudflare_zones WHERE zone_id = ?`, zoneID)
	if err != nil {
		return fmt.Errorf("delete Cloudflare settings: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete Cloudflare settings: %w", err)
	}
	if affected == 0 {
		return userErrorf("区域不存在或已被删除")
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

// CloudflareSettings verifies every configured zone. The aggregate Connected
// is true only when all of them are reachable; the per-zone statuses let the
// page show which one needs attention.
func (s *Store) CloudflareSettings(ctx context.Context) (CloudflareSettingsView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT zone_id, zone_name, api_token, updated_at FROM cloudflare_zones ORDER BY zone_name, zone_id`)
	if err != nil {
		return CloudflareSettingsView{}, fmt.Errorf("load Cloudflare settings: %w", err)
	}
	defer rows.Close()

	type storedZone struct {
		id        string
		name      string
		encrypted []byte
		updatedAt int64
	}
	var stored []storedZone
	for rows.Next() {
		var zone storedZone
		if err := rows.Scan(&zone.id, &zone.name, &zone.encrypted, &zone.updatedAt); err != nil {
			return CloudflareSettingsView{}, fmt.Errorf("load Cloudflare settings: %w", err)
		}
		stored = append(stored, zone)
	}
	if err := rows.Err(); err != nil {
		return CloudflareSettingsView{}, fmt.Errorf("load Cloudflare settings: %w", err)
	}

	view := CloudflareSettingsView{Zones: make([]CloudflareZoneView, 0, len(stored))}
	tokens := make([]string, len(stored))
	for i, zone := range stored {
		item := CloudflareZoneView{
			ZoneID:    zone.id,
			ZoneName:  zone.name,
			UpdatedAt: time.Unix(zone.updatedAt, 0).UTC().Format(time.RFC3339),
		}
		token, err := security.Decrypt(s.masterKey, zone.encrypted)
		if err != nil {
			item.Error = "无法解密访问令牌"
		} else {
			tokens[i] = string(token)
			item.TokenMasked = maskSecret(string(token))
		}
		view.Zones = append(view.Zones, item)
	}

	// Probe every zone at once: one unreachable zone must not add its timeout
	// to every other zone's check.
	var wg sync.WaitGroup
	for i := range view.Zones {
		if tokens[i] == "" {
			continue
		}
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			client, err := NewCloudflareClient(tokens[index])
			if err != nil {
				view.Zones[index].Error = err.Error()
				return
			}
			probeCtx, cancel := context.WithTimeout(ctx, cloudflareProbeTimeout)
			defer cancel()
			if _, err := client.VerifyZone(probeCtx, view.Zones[index].ZoneID); err != nil {
				// A stored token that no longer works is a state to report, not a
				// request failure: the page still needs to render and offer a fix.
				view.Zones[index].Error = err.Error()
				return
			}
			view.Zones[index].Connected = true
		}(i)
	}
	wg.Wait()

	multiple := len(view.Zones) > 1
	var problems []string
	for _, zone := range view.Zones {
		if zone.Error == "" {
			continue
		}
		if multiple {
			problems = append(problems, zone.ZoneName+"："+zone.Error)
		} else {
			problems = append(problems, zone.Error)
		}
	}

	view.Configured = len(view.Zones) > 0
	view.Connected = view.Configured
	for _, zone := range view.Zones {
		if !zone.Connected {
			view.Connected = false
		}
	}
	if len(problems) > 0 {
		view.Error = strings.Join(problems, "；")
	}
	// Keep the single-zone fields populated for callers and older clients that
	// only know the first zone.
	if len(view.Zones) > 0 {
		first := view.Zones[0]
		view.ZoneID = first.ZoneID
		view.ZoneName = first.ZoneName
		view.TokenMasked = first.TokenMasked
		view.UpdatedAt = first.UpdatedAt
	}
	return view, nil
}

// CloudflareZones returns every configured zone with a ready API client.
func (s *Store) CloudflareZones(ctx context.Context) ([]CloudflareZone, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT zone_id, zone_name, api_token FROM cloudflare_zones ORDER BY zone_name, zone_id`)
	if err != nil {
		return nil, fmt.Errorf("load Cloudflare settings: %w", err)
	}
	defer rows.Close()

	var zones []CloudflareZone
	for rows.Next() {
		var zone CloudflareZone
		var encrypted []byte
		if err := rows.Scan(&zone.ID, &zone.Name, &encrypted); err != nil {
			return nil, fmt.Errorf("load Cloudflare settings: %w", err)
		}
		token, err := security.Decrypt(s.masterKey, encrypted)
		if err != nil {
			return nil, fmt.Errorf("decrypt Cloudflare token for %s: %w", zone.Name, err)
		}
		client, err := NewCloudflareClient(string(token))
		if err != nil {
			return nil, err
		}
		zone.Client = client
		zones = append(zones, zone)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load Cloudflare settings: %w", err)
	}
	return zones, nil
}

// CloudflareZone returns the first configured zone. It exists for callers that
// predate multi-zone support; new code uses CloudflareZones.
func (s *Store) CloudflareZone(ctx context.Context) (string, string, *CloudflareClient, error) {
	zones, err := s.CloudflareZones(ctx)
	if err != nil {
		return "", "", nil, err
	}
	if len(zones) == 0 {
		return "", "", nil, errors.New("Cloudflare is not configured")
	}
	return zones[0].ID, zones[0].Name, zones[0].Client, nil
}

func maskSecret(value string) string {
	if len(value) <= 8 {
		return "****"
	}
	return value[:4] + "…" + value[len(value)-4:]
}

func cloudflareNameInZone(zoneName, name string) bool {
	return name == zoneName || strings.HasSuffix(name, "."+zoneName)
}

func cloudflareZoneNames(zones []CloudflareZone) string {
	names := make([]string, 0, len(zones))
	for _, zone := range zones {
		names = append(names, zone.Name)
	}
	return strings.Join(names, "、")
}

// matchCloudflareZone picks the configured zone a name belongs to. The longest
// matching name wins so a delegated sub-zone (sub.example.com) takes precedence
// over its parent (example.com).
func (s *Store) matchCloudflareZone(ctx context.Context, name string) (CloudflareZone, error) {
	zones, err := s.CloudflareZones(ctx)
	if err != nil {
		return CloudflareZone{}, err
	}
	if len(zones) == 0 {
		return CloudflareZone{}, errors.New("Cloudflare is not configured")
	}
	var matched CloudflareZone
	for _, zone := range zones {
		if !cloudflareNameInZone(zone.Name, name) {
			continue
		}
		if matched.ID == "" || len(zone.Name) > len(matched.Name) {
			matched = zone
		}
	}
	if matched.ID == "" {
		return CloudflareZone{}, userErrorf("域名必须属于已绑定的 Cloudflare 区域（%s）", cloudflareZoneNames(zones))
	}
	return matched, nil
}

// normalizeCloudflareRecordFields checks an operator-supplied record and
// rejects what Cloudflare could not serve. Zone membership is checked
// separately because a create and an edit resolve it differently.
func normalizeCloudflareRecordFields(record *CloudflareRecord) error {
	record.Name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(record.Name)), ".")
	record.Type = strings.ToUpper(strings.TrimSpace(record.Type))
	if record.Type != "A" && record.Type != "AAAA" && record.Type != "CNAME" && record.Type != "TXT" {
		return userErrorf("记录类型只支持 A、AAAA、CNAME 或 TXT")
	}
	if !validSNI(record.Name) {
		return userErrorf("域名格式不正确")
	}
	record.Content = strings.TrimSpace(record.Content)
	if record.Content == "" {
		return userErrorf("请填写指向地址或内容")
	}
	if record.Proxied {
		// Cloudflare forces automatic TTL on proxied records.
		record.TTL = 1
	}
	if record.TTL != 1 && (record.TTL < 60 || record.TTL > 86400) {
		return userErrorf("缓存时间必须为 1（自动）或 60 到 86400 秒之间")
	}
	return nil
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

// ListCloudflareRecords reads every configured zone and annotates each record
// with the access services it reaches. Cloudflare is the source of truth, so a
// record removed in its console simply stops appearing here. A zone that
// cannot be read is reported separately instead of hiding the other zones.
func (s *Store) ListCloudflareRecords(ctx context.Context) ([]CloudflareRecordView, []CloudflareZoneFailure, error) {
	zones, err := s.CloudflareZones(ctx)
	if err != nil {
		return nil, nil, err
	}
	if len(zones) == 0 {
		return nil, nil, errors.New("Cloudflare is not configured")
	}
	views := make([]CloudflareRecordView, 0)
	failures := make([]CloudflareZoneFailure, 0)
	for _, zone := range zones {
		records, err := zone.Client.ListRecords(ctx, zone.ID)
		if err != nil {
			failures = append(failures, CloudflareZoneFailure{ZoneID: zone.ID, ZoneName: zone.Name, Error: err.Error()})
			continue
		}
		annotated, err := s.annotateCloudflareRecords(ctx, records)
		if err != nil {
			return nil, nil, err
		}
		for i := range annotated {
			annotated[i].ZoneID = zone.ID
			annotated[i].ZoneName = zone.Name
		}
		views = append(views, annotated...)
	}
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].ZoneName != views[j].ZoneName {
			return views[i].ZoneName < views[j].ZoneName
		}
		return views[i].Name < views[j].Name
	})
	return views, failures, nil
}

func (s *Store) CreateCloudflareRecord(ctx context.Context, record CloudflareRecord) (CloudflareRecordView, error) {
	if err := normalizeCloudflareRecordFields(&record); err != nil {
		return CloudflareRecordView{}, err
	}
	zone, err := s.matchCloudflareZone(ctx, record.Name)
	if err != nil {
		return CloudflareRecordView{}, err
	}
	if err := s.checkCloudflareProxyFits(ctx, record); err != nil {
		return CloudflareRecordView{}, err
	}
	record.ID = ""
	created, err := zone.Client.CreateRecord(ctx, zone.ID, record)
	if err != nil {
		return CloudflareRecordView{}, err
	}
	return s.annotateCloudflareRecordForZone(ctx, zone, created)
}

func (s *Store) UpdateCloudflareRecord(ctx context.Context, record CloudflareRecord) (CloudflareRecordView, error) {
	if strings.TrimSpace(record.ID) == "" {
		return CloudflareRecordView{}, userErrorf("缺少域名记录编号")
	}
	if err := normalizeCloudflareRecordFields(&record); err != nil {
		return CloudflareRecordView{}, err
	}
	_, zone, err := s.findCloudflareRecord(ctx, record.ID)
	if err != nil {
		return CloudflareRecordView{}, err
	}
	if !cloudflareNameInZone(zone.Name, record.Name) {
		return CloudflareRecordView{}, userErrorf("域名 %s 不属于记录所在的区域 %s；Cloudflare 记录不能跨区域移动", record.Name, zone.Name)
	}
	if err := s.checkCloudflareProxyFits(ctx, record); err != nil {
		return CloudflareRecordView{}, err
	}
	updated, err := zone.Client.UpdateRecord(ctx, zone.ID, record)
	if err != nil {
		return CloudflareRecordView{}, err
	}
	return s.annotateCloudflareRecordForZone(ctx, zone, updated)
}

// DeleteCloudflareRecord removes the record at Cloudflare and returns what was
// removed, so the audit trail names the record rather than only its ID.
func (s *Store) DeleteCloudflareRecord(ctx context.Context, recordID string) (CloudflareRecord, error) {
	record, zone, err := s.findCloudflareRecord(ctx, recordID)
	if err != nil {
		return CloudflareRecord{}, err
	}
	if err := zone.Client.DeleteRecord(ctx, zone.ID, recordID); err != nil {
		return CloudflareRecord{}, err
	}
	return record, nil
}

// findCloudflareRecord looks a record up across every configured zone. Record
// IDs are unique at Cloudflare, so the first zone that owns it is the answer;
// a 404 from another zone just means "keep looking".
func (s *Store) findCloudflareRecord(ctx context.Context, recordID string) (CloudflareRecord, CloudflareZone, error) {
	zones, err := s.CloudflareZones(ctx)
	if err != nil {
		return CloudflareRecord{}, CloudflareZone{}, err
	}
	return findCloudflareRecordInZones(ctx, zones, recordID)
}

func findCloudflareRecordInZones(ctx context.Context, zones []CloudflareZone, recordID string) (CloudflareRecord, CloudflareZone, error) {
	if len(zones) == 0 {
		return CloudflareRecord{}, CloudflareZone{}, errors.New("Cloudflare is not configured")
	}
	for _, zone := range zones {
		record, err := zone.Client.GetRecord(ctx, zone.ID, recordID)
		if err == nil {
			return record, zone, nil
		}
		if errors.Is(err, errCloudflareRecordNotFound) {
			continue
		}
		return CloudflareRecord{}, CloudflareZone{}, err
	}
	return CloudflareRecord{}, CloudflareZone{}, userErrorf("在已绑定的 Cloudflare 区域中找不到该域名记录")
}

func (s *Store) annotateCloudflareRecordForZone(ctx context.Context, zone CloudflareZone, record CloudflareRecord) (CloudflareRecordView, error) {
	views, err := s.annotateCloudflareRecords(ctx, []CloudflareRecord{record})
	if err != nil {
		return CloudflareRecordView{}, err
	}
	views[0].ZoneID = zone.ID
	views[0].ZoneName = zone.Name
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
	ddnsRows, err := s.db.QueryContext(ctx, `SELECT COALESCE(ddns_domains, '[]'), COALESCE(ddns_domain, ''), name FROM nodes
		WHERE ddns_enabled = 1 AND revoked_at IS NULL ORDER BY name, id`)
	if err == nil {
		defer ddnsRows.Close()
		for ddnsRows.Next() {
			var domainsJSON, domain, nodeName string
			if err := ddnsRows.Scan(&domainsJSON, &domain, &nodeName); err == nil {
				var domains []string
				_ = json.Unmarshal([]byte(domainsJSON), &domains)
				if len(domains) == 0 && domain != "" {
					domains = []string{domain}
				}
				for _, d := range domains {
					d = strings.ToLower(strings.TrimSpace(d))
					if d != "" {
						byDomain[d] = append(byDomain[d], CloudflareBinding{NodeName: nodeName + " (DDNS)"})
					}
				}
			}
		}
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
