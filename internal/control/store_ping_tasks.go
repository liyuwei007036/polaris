package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type PingTask struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	Target    string   `json:"target"`
	Interval  int      `json:"interval"`
	AutoJoin  bool     `json:"auto_join"`
	NodeIDs   []string `json:"node_ids"`
	Sort      int      `json:"sort"`
	CreatedAt int64    `json:"created_at"`
}

func (s *Store) initPingTasksTable(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS ping_tasks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  target TEXT NOT NULL,
  interval INTEGER NOT NULL DEFAULT 60,
  auto_join INTEGER NOT NULL DEFAULT 1,
  node_ids TEXT NOT NULL DEFAULT '[]',
  sort INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
`)
	if err != nil {
		return err
	}
	return nil
}

func (s *Store) ListPingTasks(ctx context.Context) ([]PingTask, error) {
	if err := s.initPingTasksTable(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, target, interval, auto_join, node_ids, sort, created_at FROM ping_tasks ORDER BY sort ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []PingTask
	for rows.Next() {
		var t PingTask
		var autoJoinInt int
		var nodeIDsJSON string
		if err := rows.Scan(&t.ID, &t.Name, &t.Target, &t.Interval, &autoJoinInt, &nodeIDsJSON, &t.Sort, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.AutoJoin = autoJoinInt == 1
		_ = json.Unmarshal([]byte(nodeIDsJSON), &t.NodeIDs)
		if t.NodeIDs == nil {
			t.NodeIDs = []string{}
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}

func (s *Store) SavePingTask(ctx context.Context, task *PingTask) error {
	if err := s.initPingTasksTable(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(task.Name) == "" {
		return errors.New("探测名称不能为空")
	}
	if strings.TrimSpace(task.Target) == "" {
		return errors.New("目标地址不能为空")
	}
	if task.Interval < 10 {
		task.Interval = 60
	}
	nodeIDsBytes, _ := json.Marshal(task.NodeIDs)
	autoJoinInt := 0
	if task.AutoJoin {
		autoJoinInt = 1
	}
	now := time.Now().Unix()

	if task.ID <= 0 {
		var maxSort sql.NullInt64
		_ = s.db.QueryRowContext(ctx, `SELECT MAX(sort) FROM ping_tasks`).Scan(&maxSort)
		nextSort := 0
		if maxSort.Valid {
			nextSort = int(maxSort.Int64) + 1
		}
		res, err := s.db.ExecContext(ctx, `INSERT INTO ping_tasks (name, target, interval, auto_join, node_ids, sort, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			task.Name, task.Target, task.Interval, autoJoinInt, string(nodeIDsBytes), nextSort, now)
		if err != nil {
			return err
		}
		task.ID, _ = res.LastInsertId()
		task.Sort = nextSort
		task.CreatedAt = now
	} else {
		_, err := s.db.ExecContext(ctx, `UPDATE ping_tasks SET name = ?, target = ?, interval = ?, auto_join = ?, node_ids = ? WHERE id = ?`,
			task.Name, task.Target, task.Interval, autoJoinInt, string(nodeIDsBytes), task.ID)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) DeletePingTask(ctx context.Context, id int64) error {
	if err := s.initPingTasksTable(ctx); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM ping_tasks WHERE id = ?`, id)
	return err
}

func (s *Store) ReorderPingTasks(ctx context.Context, ids []int64) error {
	if err := s.initPingTasksTable(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE ping_tasks SET sort = ? WHERE id = ?`, i, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
