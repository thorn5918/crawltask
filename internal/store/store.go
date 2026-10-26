// Package store 提供 SQLite 访问层（modernc.org/sqlite，纯 Go 免 CGO）。
package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store SQLite 存储。
type Store struct {
	db *sql.DB
}

// Open 打开数据库并执行建表迁移。
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite 单写者，串行化避免 database is locked
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭数据库。
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS tasks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			command TEXT NOT NULL DEFAULT '',
			project TEXT NOT NULL DEFAULT '',
			workdir TEXT NOT NULL DEFAULT '',
			env_id INTEGER NOT NULL DEFAULT 0,
			schedule_type TEXT NOT NULL DEFAULT 'manual',
			interval_seconds INTEGER NOT NULL DEFAULT 0,
			run_date TEXT NOT NULL DEFAULT '',
			cron_expr TEXT NOT NULL DEFAULT '',
			max_concurrent INTEGER NOT NULL DEFAULT 1,
			enabled INTEGER NOT NULL DEFAULT 1,
			tags TEXT NOT NULL DEFAULT '',
			last_fire TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id INTEGER NOT NULL,
			task_name TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'running',
			trigger TEXT NOT NULL DEFAULT 'manual',
			start_time TEXT NOT NULL DEFAULT '',
			end_time TEXT NOT NULL DEFAULT '',
			duration_ms INTEGER NOT NULL DEFAULT 0,
			exit_code INTEGER,
			log_file TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_runs_task ON runs(task_id, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_runs_start ON runs(start_time)`,
		`CREATE TABLE IF NOT EXISTS envs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			python_version TEXT NOT NULL DEFAULT '',
			interpreter TEXT NOT NULL DEFAULT '',
			scripts_dir TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT 'local',
			status TEXT NOT NULL DEFAULT 'creating',
			created_at TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS projects (
			name TEXT PRIMARY KEY,
			tags TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS workflows (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			cron_expr TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 0,
			steps TEXT NOT NULL DEFAULT '[]',
			last_fire TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS workflow_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			workflow_id INTEGER NOT NULL,
			workflow_name TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'running',
			trigger TEXT NOT NULL DEFAULT 'manual',
			start_time TEXT NOT NULL DEFAULT '',
			end_time TEXT NOT NULL DEFAULT '',
			duration_ms INTEGER NOT NULL DEFAULT 0,
			steps TEXT NOT NULL DEFAULT '[]'
		)`,
		`CREATE INDEX IF NOT EXISTS idx_workflow_runs_wf ON workflow_runs(workflow_id, id DESC)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	// tasks 增加 workflow_id 列（流程编排内部任务标记，>0 表示属于某流程，不在任务管理中显示）
	if _, err := s.db.Exec(`ALTER TABLE tasks ADD COLUMN workflow_id INTEGER NOT NULL DEFAULT 0`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate tasks.workflow_id: %w", err)
		}
	}
	return nil
}

func nowStr() string { return time.Now().Format("2006-01-02 15:04:05") }

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullString(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}
