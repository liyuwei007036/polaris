package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type MasterDDNSSettings struct {
	Enabled        bool     `json:"enabled"`
	RecordIDs      []string `json:"record_ids"`
	Domains        []string `json:"domains"`
	LastObservedIP string   `json:"last_observed_ip"`
	UpdatedAt      string   `json:"updated_at,omitempty"`
}

func (s *Store) GetMasterDDNS(ctx context.Context) (MasterDDNSSettings, error) {
	var (
		enabledInt int
		recordJSON string
		domainJSON string
		lastIP     string
		updatedAt  int64
	)
	row := s.db.QueryRowContext(ctx, `SELECT enabled, record_ids, domains, last_observed_ip, updated_at FROM master_ddns_settings WHERE id = 1`)
	err := row.Scan(&enabledInt, &recordJSON, &domainJSON, &lastIP, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return MasterDDNSSettings{
			Enabled:   false,
			RecordIDs: []string{},
			Domains:   []string{},
		}, nil
	}
	if err != nil {
		return MasterDDNSSettings{}, fmt.Errorf("load master ddns settings: %w", err)
	}

	var recordIDs []string
	if recordJSON != "" {
		_ = json.Unmarshal([]byte(recordJSON), &recordIDs)
	}
	if recordIDs == nil {
		recordIDs = []string{}
	}

	var domains []string
	if domainJSON != "" {
		_ = json.Unmarshal([]byte(domainJSON), &domains)
	}
	if domains == nil {
		domains = []string{}
	}

	var updatedStr string
	if updatedAt > 0 {
		updatedStr = time.Unix(updatedAt, 0).UTC().Format(time.RFC3339)
	}

	return MasterDDNSSettings{
		Enabled:        enabledInt == 1,
		RecordIDs:      recordIDs,
		Domains:        domains,
		LastObservedIP: lastIP,
		UpdatedAt:      updatedStr,
	}, nil
}

func (s *Store) UpdateMasterDDNS(ctx context.Context, in MasterDDNSSettings) (MasterDDNSSettings, error) {
	if in.RecordIDs == nil {
		in.RecordIDs = []string{}
	}
	if in.Domains == nil {
		in.Domains = []string{}
	}
	recordBytes, err := json.Marshal(in.RecordIDs)
	if err != nil {
		return MasterDDNSSettings{}, err
	}
	domainBytes, err := json.Marshal(in.Domains)
	if err != nil {
		return MasterDDNSSettings{}, err
	}

	enabledInt := 0
	if in.Enabled {
		enabledInt = 1
	}

	now := time.Now().Unix()
	_, err = s.db.ExecContext(ctx, `UPDATE master_ddns_settings SET 
		enabled = ?, record_ids = ?, domains = ?, updated_at = ? WHERE id = 1`,
		enabledInt, string(recordBytes), string(domainBytes), now)
	if err != nil {
		return MasterDDNSSettings{}, fmt.Errorf("save master ddns settings: %w", err)
	}
	return s.GetMasterDDNS(ctx)
}

func (s *Store) SetMasterDDNSLastObservedIP(ctx context.Context, ip string) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `UPDATE master_ddns_settings SET last_observed_ip = ?, updated_at = ? WHERE id = 1`, ip, now)
	return err
}
