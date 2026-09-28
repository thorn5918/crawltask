package store

import (
	"database/sql"
	"errors"
	"time"
)

// Task 定时任务。
type Task struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	Command         string `json:"command"`
	Project         string `json:"project"`
	Workdir         string `json:"workdir"`
	EnvID           int64  `json:"env_id"`
	ScheduleType    string `json:"schedule_type"` // manual | interval | date | cron
	IntervalSeconds int64  `json:"interval_seconds"`
	RunDate         string `json:"run_date"`
	CronExpr        string `json:"cron_expr"`
	MaxConcurrent   int    `json:"max_concurrent"`
	Enabled         bool   `json:"enabled"`
	Tags            string `json:"tags"`
	LastFire        string `json:"last_fire"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

// ErrNotFound 未找到记录。
var ErrNotFound = errors.New("记录不存在")

const taskCols = `id, name, command, project, workdir, env_id, schedule_type, interval_seconds,
	run_date, cron_expr, max_concurrent, enabled, tags, last_fire, created_at, updated_at`

func scanTask(row interface{ Scan(...any) error }) (*Task, error) {
	var t Task
	var enabled int
	err := row.Scan(&t.ID, &t.Name, &t.Command, &t.Project, &t.Workdir, &t.EnvID, &t.ScheduleType,
		&t.IntervalSeconds, &t.RunDate, &t.CronExpr, &t.MaxConcurrent, &enabled,
		&t.Tags, &t.LastFire, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	t.Enabled = enabled != 0
	return &t, nil
}

// ListTasks 全部任务。
func (s *Store) ListTasks() ([]*Task, error) {
	rows, err := s.db.Query(`SELECT ` + taskCols + ` FROM tasks ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetTask 按 id 取任务。
func (s *Store) GetTask(id int64) (*Task, error) {
	row := s.db.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id = ?`, id)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// CreateTask 新建任务。
func (s *Store) CreateTask(t *Task) (int64, error) {
	now := nowStr()
	res, err := s.db.Exec(`INSERT INTO tasks
		(name, command, project, workdir, env_id, schedule_type, interval_seconds, run_date, cron_expr,
		 max_concurrent, enabled, tags, last_fire, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.Name, t.Command, t.Project, t.Workdir, t.EnvID, t.ScheduleType, t.IntervalSeconds, t.RunDate, t.CronExpr,
		t.MaxConcurrent, boolToInt(t.Enabled), t.Tags, "", now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateTask 更新任务（不含 enabled/last_fire）。
func (s *Store) UpdateTask(t *Task) error {
	_, err := s.db.Exec(`UPDATE tasks SET
		name=?, command=?, project=?, workdir=?, env_id=?, schedule_type=?, interval_seconds=?,
		run_date=?, cron_expr=?, max_concurrent=?, tags=?, updated_at=?
		WHERE id=?`,
		t.Name, t.Command, t.Project, t.Workdir, t.EnvID, t.ScheduleType, t.IntervalSeconds,
		t.RunDate, t.CronExpr, t.MaxConcurrent, t.Tags, nowStr(), t.ID)
	return err
}

// DeleteTask 删除任务（保留运行历史）。
func (s *Store) DeleteTask(id int64) error {
	_, err := s.db.Exec(`DELETE FROM tasks WHERE id=?`, id)
	return err
}

// SetTaskEnabled 启用/暂停任务。
func (s *Store) SetTaskEnabled(id int64, enabled bool) error {
	_, err := s.db.Exec(`UPDATE tasks SET enabled=?, updated_at=? WHERE id=?`, boolToInt(enabled), nowStr(), id)
	return err
}

// SetTaskLastFire 记录调度触发点（用于重启后 misfire 判断）。
func (s *Store) SetTaskLastFire(id int64, fire time.Time) error {
	_, err := s.db.Exec(`UPDATE tasks SET last_fire=? WHERE id=?`, fire.Format("2006-01-02 15:04:05"), id)
	return err
}
