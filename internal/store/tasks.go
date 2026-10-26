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
	WorkflowID      int64  `json:"workflow_id"` // >0 = 流程编排内部步骤任务
}

// ErrNotFound 未找到记录。
var ErrNotFound = errors.New("记录不存在")

const taskCols = `id, name, command, project, workdir, env_id, schedule_type, interval_seconds,
	run_date, cron_expr, max_concurrent, enabled, tags, last_fire, created_at, updated_at, workflow_id`

func scanTask(row interface{ Scan(...any) error }) (*Task, error) {
	var t Task
	var enabled int
	err := row.Scan(&t.ID, &t.Name, &t.Command, &t.Project, &t.Workdir, &t.EnvID, &t.ScheduleType,
		&t.IntervalSeconds, &t.RunDate, &t.CronExpr, &t.MaxConcurrent, &enabled,
		&t.Tags, &t.LastFire, &t.CreatedAt, &t.UpdatedAt, &t.WorkflowID)
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
		 max_concurrent, enabled, tags, last_fire, created_at, updated_at, workflow_id)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.Name, t.Command, t.Project, t.Workdir, t.EnvID, t.ScheduleType, t.IntervalSeconds, t.RunDate, t.CronExpr,
		t.MaxConcurrent, boolToInt(t.Enabled), t.Tags, "", now, now, t.WorkflowID)
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

// ListUserTasks 全部用户任务（不含流程编排的内部步骤任务）。
func (s *Store) ListUserTasks() ([]*Task, error) {
	return s.listTasksWhere(`workflow_id = 0`)
}

// ListTasksByWorkflow 某流程的全部步骤任务。
func (s *Store) ListTasksByWorkflow(wfID int64) ([]*Task, error) {
	return s.listTasksWhere(`workflow_id = ?`, wfID)
}

func (s *Store) listTasksWhere(cond string, args ...any) ([]*Task, error) {
	rows, err := s.db.Query(`SELECT `+taskCols+` FROM tasks WHERE `+cond+` ORDER BY id`, args...)
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

// DeleteTasksByWorkflow 删除流程全部步骤任务，返回被删任务 id（用于清理日志目录）。
func (s *Store) DeleteTasksByWorkflow(wfID int64) ([]int64, error) {
	return s.deleteWorkflowTasksWhere(`workflow_id = ?`, wfID)
}

// DeleteWorkflowTasksNotIn 删除流程中不再被引用的步骤任务。
func (s *Store) DeleteWorkflowTasksNotIn(wfID int64, keepIDs []int64) ([]int64, error) {
	marks := ""
	args := []any{wfID}
	for _, id := range keepIDs {
		if marks != "" {
			marks += ","
		}
		marks += "?"
		args = append(args, id)
	}
	cond := `workflow_id = ? AND id NOT IN (` + marks + `)`
	return s.deleteWorkflowTasksWhere(cond, args...)
}

func (s *Store) deleteWorkflowTasksWhere(cond string, args ...any) ([]int64, error) {
	rows, err := s.db.Query(`SELECT id FROM tasks WHERE `+cond, args...)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, err := s.db.Exec(`DELETE FROM tasks WHERE id=?`, id); err != nil {
			return ids, err
		}
	}
	return ids, nil
}

// SetTaskLastFire 记录调度触发点（用于重启后 misfire 判断）。
func (s *Store) SetTaskLastFire(id int64, fire time.Time) error {
	_, err := s.db.Exec(`UPDATE tasks SET last_fire=? WHERE id=?`, fire.Format("2006-01-02 15:04:05"), id)
	return err
}
