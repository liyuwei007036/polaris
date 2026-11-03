package control

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// vacuumDatabase implements the web-admin data page's destructive-but-explicit
// cleanup action. Probe history is retained for seven days, then SQLite is
// compacted so the action has a measurable effect instead of being a stub.
func (s *Server) vacuumDatabase(w http.ResponseWriter, r *http.Request) {
	if _, err := s.admin(r); err != nil {
		writeError(w, err)
		return
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour).Unix()
	result, err := s.store.db.ExecContext(r.Context(), `DELETE FROM node_latency_points WHERE ts < ?`, cutoff)
	if err != nil {
		writeError(w, fmt.Errorf("prune latency history: %w", err))
		return
	}
	if _, err := s.store.db.ExecContext(r.Context(), `VACUUM`); err != nil {
		writeError(w, fmt.Errorf("vacuum database: %w", err))
		return
	}
	pruned, _ := result.RowsAffected()
	s.pruneNodeMetricPoints(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"pruned": pruned, "freed": 0})
}

func (s *Server) restoreDatabase(w http.ResponseWriter, r *http.Request) {
	if _, err := s.admin(r); err != nil {
		writeError(w, err)
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeError(w, fmt.Errorf("read backup upload: %w", err))
		return
	}
	file, _, err := r.FormFile("backup")
	if err != nil {
		writeError(w, fmt.Errorf("backup file is required: %w", err))
		return
	}
	defer file.Close()
	tmp, err := os.CreateTemp(s.store.dataDir, "restore-*.db")
	if err != nil {
		writeError(w, err)
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = io.Copy(tmp, file); err != nil {
		tmp.Close()
		writeError(w, err)
		return
	}
	if err = tmp.Close(); err != nil {
		writeError(w, err)
		return
	}
	header := make([]byte, 16)
	f, err := os.Open(tmpName)
	if err != nil {
		writeError(w, err)
		return
	}
	_, readErr := io.ReadFull(f, header)
	_ = f.Close()
	if readErr != nil || string(header) != "SQLite format 3\x00" {
		writeError(w, errors.New("invalid SQLite backup"))
		return
	}
	if err := s.copyDatabaseFrom(r.Context(), tmpName); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"restored": true})
}

func (s *Server) copyDatabaseFrom(ctx context.Context, source string) error {
	if _, err := s.store.db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	if _, err := s.store.db.ExecContext(ctx, `ATTACH DATABASE ? AS restore_db`, source); err != nil {
		return fmt.Errorf("attach backup: %w", err)
	}
	defer s.store.db.ExecContext(context.Background(), `DETACH DATABASE restore_db`)
	rows, err := s.store.db.QueryContext(ctx, `SELECT type, name, sql FROM restore_db.sqlite_master WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' ORDER BY CASE type WHEN 'table' THEN 0 ELSE 1 END, name`)
	if err != nil {
		return err
	}
	type object struct{ kind, name, sql string }
	var objects []object
	for rows.Next() {
		var o object
		if err := rows.Scan(&o.kind, &o.name, &o.sql); err != nil {
			rows.Close()
			return err
		}
		objects = append(objects, o)
	}
	rows.Close()
	for _, o := range objects {
		if o.kind == "table" {
			if _, err := s.store.db.ExecContext(ctx, `DROP TABLE IF EXISTS `+quoteSQLite(o.name)); err != nil {
				return err
			}
		}
	}
	for _, o := range objects {
		if o.kind != "table" {
			continue
		}
		if _, err := s.store.db.ExecContext(ctx, o.sql); err != nil {
			return fmt.Errorf("create restored table %s: %w", o.name, err)
		}
		if _, err := s.store.db.ExecContext(ctx, `INSERT INTO `+quoteSQLite(o.name)+` SELECT * FROM restore_db.`+quoteSQLite(o.name)); err != nil {
			return fmt.Errorf("restore table %s: %w", o.name, err)
		}
	}
	for _, o := range objects {
		if o.kind != "table" {
			if _, err := s.store.db.ExecContext(ctx, o.sql); err != nil {
				return fmt.Errorf("create restored %s %s: %w", o.kind, o.name, err)
			}
		}
	}
	return nil
}

func quoteSQLite(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

func (s *Server) downloadDatabaseBackup(w http.ResponseWriter, r *http.Request) {
	if _, err := s.admin(r); err != nil {
		writeError(w, err)
		return
	}
	backup := filepath.Join(s.store.dataDir, fmt.Sprintf("polaris-backup-%s.db", time.Now().UTC().Format("20060102-150405")))
	if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
		writeError(w, err)
		return
	}
	if _, err := s.store.db.ExecContext(context.Background(), `VACUUM INTO ?`, backup); err != nil {
		writeError(w, fmt.Errorf("create database backup: %w", err))
		return
	}
	defer os.Remove(backup)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="polaris-backup.db"`)
	http.ServeFile(w, r, backup)
}
