package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Run 一次执行记录。
type Run struct {
	ID         int64  `json:"id"`
	TaskID     int64  `json:"task_id"`
	TaskName   string `json:"task_name"`
	Status     string `json:"status"` // running | success | failed | killed
	Trigger    string `json:"trigger"` // manual | schedule
	StartTime  string `json:"start_time"`
	EndTime    string `json:"end_time"`
	DurationMS int64  `json:"duration_ms"`
	ExitCode   *int   `json:"exit_code"`
	LogFile    string `json:"log_file"`
}

const runCols = `id, task_id, task_name, status, trigger, start_time, end_time, duration_ms, exit_code, log_file`

func scanRun(row interface{ Scan(...any) error }) (*Run, error) {
	var r Run
	var exitCode sql.NullInt64
	err := row.Scan(&r.ID, &r.TaskID, &r.TaskName, &r.Status, &r.Trigger, &r.StartTime, &r.EndTime,
		&r.DurationMS, &exitCode, &r.LogFile)
	if err != nil {
		return nil, err
	}
	if exitCode.Valid {
		v := int(exitCode.Int64)
		r.ExitCode = &v
	}
	return &r, nil
}

// CreateRun 新建 running 状态的运行记录，返回记录 id。
func (s *Store) CreateRun(r *Run) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO runs
		(task_id, task_name, status, trigger, start_time, end_time, duration_ms, exit_code, log_file)
		VALUES (?,?,?,?,?,?,0,NULL,?)`,
		r.TaskID, r.TaskName, "running", r.Trigger, nowStr(), "", r.LogFile)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinishRun 写入运行结果。
func (s *Store) FinishRun(id int64, status, endTime string, durationMS int64, exitCode *int) error {
	var ec any
	if exitCode != nil {
		ec = *exitCode
	}
	_, err := s.db.Exec(`UPDATE runs SET status=?, end_time=?, duration_ms=?, exit_code=? WHERE id=?`,
		status, endTime, durationMS, ec, id)
	return err
}

// GetRun 按 id 取运行记录。
func (s *Store) GetRun(id int64) (*Run, error) {
	row := s.db.QueryRow(`SELECT `+runCols+` FROM runs WHERE id=?`, id)
	r, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// RunFilter 运行记录查询条件。
type RunFilter struct {
	TaskID   int64
	Status   string
	DateFrom string // YYYY-MM-DD（含）
	DateTo   string // YYYY-MM-DD（含）
	Keyword  string // 匹配任务名
	Page     int
	PageSize int
}

// ListRuns 分页查询运行记录。
func (s *Store) ListRuns(f RunFilter) ([]*Run, int, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 || f.PageSize > 200 {
		f.PageSize = 20
	}
	var where []string
	var args []any
	if f.TaskID > 0 {
		where = append(where, "task_id=?")
		args = append(args, f.TaskID)
	}
	if f.Status != "" {
		where = append(where, "status=?")
		args = append(args, f.Status)
	}
	if f.DateFrom != "" {
		where = append(where, "start_time >= ?")
		args = append(args, f.DateFrom+" 00:00:00")
	}
	if f.DateTo != "" {
		where = append(where, "start_time <= ?")
		args = append(args, f.DateTo+" 23:59:59")
	}
	if f.Keyword != "" {
		where = append(where, "task_name LIKE ?")
		args = append(args, "%"+f.Keyword+"%")
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM runs`+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := `SELECT ` + runCols + ` FROM runs` + cond + fmt.Sprintf(` ORDER BY id DESC LIMIT %d OFFSET %d`,
		f.PageSize, (f.Page-1)*f.PageSize)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []*Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// LastRunPerTask 每个任务最近一次运行（含 running）。
func (s *Store) LastRunPerTask() (map[int64]*Run, error) {
	rows, err := s.db.Query(`SELECT ` + runCols + ` FROM runs r
		JOIN (SELECT task_id, MAX(id) AS mid FROM runs GROUP BY task_id) m ON r.id = m.mid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]*Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out[r.TaskID] = r
	}
	return out, rows.Err()
}

// RunningRuns 全部运行中的记录。
func (s *Store) RunningRuns() ([]*Run, error) {
	rows, err := s.db.Query(`SELECT ` + runCols + ` FROM runs WHERE status='running' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
