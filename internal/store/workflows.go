package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Workflow 流程编排定义：多个任务按顺序执行，可配置 Cron 定时触发。
type Workflow struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	CronExpr    string `json:"cron_expr"` // 空 = 不定时
	Enabled     bool   `json:"enabled"`
	Steps       string `json:"steps"` // JSON: []WorkflowStep
	LastFire    string `json:"last_fire"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// WorkflowStep 流程步骤定义（引用任务管理中的任务）。
type WorkflowStep struct {
	TaskID            int64 `json:"task_id"`
	ContinueOnFailure bool  `json:"continue_on_failure"`
}

// WorkflowStepRun 运行实例中一个步骤的状态快照。
type WorkflowStepRun struct {
	TaskID            int64  `json:"task_id"`
	TaskName          string `json:"task_name"`
	ContinueOnFailure bool   `json:"continue_on_failure"`
	Status            string `json:"status"` // pending | running | success | failed | killed | skipped
	RunID             int64  `json:"run_id"` // 对应任务运行记录 id（0 = 未产生运行）
	StartTime         string `json:"start_time"`
	EndTime           string `json:"end_time"`
	DurationMS        int64  `json:"duration_ms"`
	ExitCode          *int   `json:"exit_code"`
	Error             string `json:"error"`
}

// WorkflowRun 一次流程运行实例。
type WorkflowRun struct {
	ID           int64  `json:"id"`
	WorkflowID   int64  `json:"workflow_id"`
	WorkflowName string `json:"workflow_name"`
	Status       string `json:"status"` // running | success | partial | failed | killed
	Trigger      string `json:"trigger"` // manual | schedule
	StartTime    string `json:"start_time"`
	EndTime      string `json:"end_time"`
	DurationMS   int64  `json:"duration_ms"`
	Steps        string `json:"steps"` // JSON: []WorkflowStepRun
}

const workflowCols = `id, name, description, cron_expr, enabled, steps, last_fire, created_at, updated_at`

func scanWorkflow(row interface{ Scan(...any) error }) (*Workflow, error) {
	var wf Workflow
	var enabled int
	err := row.Scan(&wf.ID, &wf.Name, &wf.Description, &wf.CronExpr, &enabled, &wf.Steps,
		&wf.LastFire, &wf.CreatedAt, &wf.UpdatedAt)
	if err != nil {
		return nil, err
	}
	wf.Enabled = enabled != 0
	return &wf, nil
}

// ListWorkflows 全部流程（按 id 排序）。
func (s *Store) ListWorkflows() ([]*Workflow, error) {
	rows, err := s.db.Query(`SELECT ` + workflowCols + ` FROM workflows ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Workflow
	for rows.Next() {
		wf, err := scanWorkflow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, wf)
	}
	return out, rows.Err()
}

// GetWorkflow 按 id 取流程。
func (s *Store) GetWorkflow(id int64) (*Workflow, error) {
	row := s.db.QueryRow(`SELECT `+workflowCols+` FROM workflows WHERE id=?`, id)
	wf, err := scanWorkflow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return wf, err
}

// CreateWorkflow 新建流程。
func (s *Store) CreateWorkflow(wf *Workflow) (int64, error) {
	now := nowStr()
	res, err := s.db.Exec(`INSERT INTO workflows
		(name, description, cron_expr, enabled, steps, last_fire, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		wf.Name, wf.Description, wf.CronExpr, boolToInt(wf.Enabled), wf.Steps, "", now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateWorkflow 更新流程（不含 last_fire）。
func (s *Store) UpdateWorkflow(wf *Workflow) error {
	_, err := s.db.Exec(`UPDATE workflows SET
		name=?, description=?, cron_expr=?, enabled=?, steps=?, updated_at=?
		WHERE id=?`,
		wf.Name, wf.Description, wf.CronExpr, boolToInt(wf.Enabled), wf.Steps, nowStr(), wf.ID)
	return err
}

// DeleteWorkflow 删除流程（保留运行历史）。
func (s *Store) DeleteWorkflow(id int64) error {
	_, err := s.db.Exec(`DELETE FROM workflows WHERE id=?`, id)
	return err
}

// SetWorkflowEnabled 启用/停用流程定时调度。
func (s *Store) SetWorkflowEnabled(id int64, enabled bool) error {
	_, err := s.db.Exec(`UPDATE workflows SET enabled=?, updated_at=? WHERE id=?`,
		boolToInt(enabled), nowStr(), id)
	return err
}

// SetWorkflowLastFire 记录流程调度触发点（用于重启后 misfire 判断）。
func (s *Store) SetWorkflowLastFire(id int64, fire time.Time) error {
	_, err := s.db.Exec(`UPDATE workflows SET last_fire=? WHERE id=?`,
		fire.Format("2006-01-02 15:04:05"), id)
	return err
}

// ParseWorkflowSteps 解析步骤定义 JSON（空/损坏时返回空切片）。
func ParseWorkflowSteps(raw string) []WorkflowStep {
	out := []WorkflowStep{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

const workflowRunCols = `id, workflow_id, workflow_name, status, trigger, start_time, end_time, duration_ms, steps`

// workflowRunColsQualified 带表别名的列清单（用于 JOIN 查询，避免列名歧义）。
const workflowRunColsQualified = `r.id, r.workflow_id, r.workflow_name, r.status, r.trigger,
	r.start_time, r.end_time, r.duration_ms, r.steps`

func scanWorkflowRun(row interface{ Scan(...any) error }) (*WorkflowRun, error) {
	var r WorkflowRun
	err := row.Scan(&r.ID, &r.WorkflowID, &r.WorkflowName, &r.Status, &r.Trigger,
		&r.StartTime, &r.EndTime, &r.DurationMS, &r.Steps)
	return &r, err
}

// ParseWorkflowStepRuns 解析运行实例的步骤快照 JSON。
func ParseWorkflowStepRuns(raw string) []WorkflowStepRun {
	out := []WorkflowStepRun{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// CreateWorkflowRun 新建 running 状态的流程运行实例，返回实例 id。
func (s *Store) CreateWorkflowRun(r *WorkflowRun) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO workflow_runs
		(workflow_id, workflow_name, status, trigger, start_time, end_time, duration_ms, steps)
		VALUES (?,?,?,?,?, '', 0, ?)`,
		r.WorkflowID, r.WorkflowName, "running", r.Trigger, nowStr(), r.Steps)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateWorkflowRunSteps 更新运行实例的步骤快照（执行进度）。
func (s *Store) UpdateWorkflowRunSteps(id int64, stepsJSON string) error {
	_, err := s.db.Exec(`UPDATE workflow_runs SET steps=? WHERE id=?`, stepsJSON, id)
	return err
}

// FinishWorkflowRun 写入流程运行结果。
func (s *Store) FinishWorkflowRun(id int64, status, endTime string, durationMS int64, stepsJSON string) error {
	_, err := s.db.Exec(`UPDATE workflow_runs SET status=?, end_time=?, duration_ms=?, steps=? WHERE id=?`,
		status, endTime, durationMS, stepsJSON, id)
	return err
}

// GetWorkflowRun 按 id 取流程运行实例。
func (s *Store) GetWorkflowRun(id int64) (*WorkflowRun, error) {
	row := s.db.QueryRow(`SELECT `+workflowRunCols+` FROM workflow_runs WHERE id=?`, id)
	r, err := scanWorkflowRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// WorkflowRunFilter 流程运行记录查询条件。
type WorkflowRunFilter struct {
	WorkflowID int64
	Status     string
	Page       int
	PageSize   int
}

// ListWorkflowRuns 分页查询流程运行实例。
func (s *Store) ListWorkflowRuns(f WorkflowRunFilter) ([]*WorkflowRun, int, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 || f.PageSize > 200 {
		f.PageSize = 20
	}
	var where []string
	var args []any
	if f.WorkflowID > 0 {
		where = append(where, "workflow_id=?")
		args = append(args, f.WorkflowID)
	}
	if f.Status != "" {
		where = append(where, "status=?")
		args = append(args, f.Status)
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM workflow_runs`+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT ` + workflowRunCols + ` FROM workflow_runs` + cond +
		fmt.Sprintf(` ORDER BY id DESC LIMIT %d OFFSET %d`, f.PageSize, (f.Page-1)*f.PageSize)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []*WorkflowRun
	for rows.Next() {
		r, err := scanWorkflowRun(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// LastWorkflowRunPerWorkflow 每个流程最近一次运行实例。
func (s *Store) LastWorkflowRunPerWorkflow() (map[int64]*WorkflowRun, error) {
	rows, err := s.db.Query(`SELECT ` + workflowRunColsQualified + ` FROM workflow_runs r
		JOIN (SELECT workflow_id, MAX(id) AS mid FROM workflow_runs GROUP BY workflow_id) m ON r.id = m.mid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]*WorkflowRun{}
	for rows.Next() {
		r, err := scanWorkflowRun(rows)
		if err != nil {
			return nil, err
		}
		out[r.WorkflowID] = r
	}
	return out, rows.Err()
}

// HasRunningWorkflowRun 流程是否有运行中的实例（同流程同时只允许一个实例）。
func (s *Store) HasRunningWorkflowRun(workflowID int64) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM workflow_runs WHERE workflow_id=? AND status='running'`,
		workflowID).Scan(&n)
	return n > 0, err
}

// DeleteWorkflowRun 删除单条流程运行记录（运行中的不允许）。
func (s *Store) DeleteWorkflowRun(id int64) error {
	var status string
	err := s.db.QueryRow(`SELECT status FROM workflow_runs WHERE id=?`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if status == "running" {
		return errors.New("运行中的流程实例不允许删除")
	}
	_, err = s.db.Exec(`DELETE FROM workflow_runs WHERE id=?`, id)
	return err
}

// FailStaleWorkflowRuns 服务启动时把上次遗留的 running 流程实例标记为失败。
func (s *Store) FailStaleWorkflowRuns() error {
	_, err := s.db.Exec(`UPDATE workflow_runs SET status='failed', end_time=?, duration_ms=0
		WHERE status='running'`, nowStr())
	return err
}
