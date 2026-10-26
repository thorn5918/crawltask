package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/robfig/cron/v3"

	"pyscheduler/internal/config"
	"pyscheduler/internal/store"
	"pyscheduler/internal/workflow"
)

// workflowStepView 流程步骤视图（步骤定义 + 存在性）。
type workflowStepView struct {
	TaskID            int64  `json:"task_id"`
	TaskName          string `json:"task_name"`
	Command           string `json:"command"`
	Project           string `json:"project"`
	Workdir           string `json:"workdir"`
	EnvID             int64  `json:"env_id"`
	EnvName           string `json:"env_name"`
	ContinueOnFailure bool   `json:"continue_on_failure"`
	Missing           bool   `json:"missing"` // 步骤任务不存在（异常情况）
}

// workflowView 流程列表视图。
type workflowView struct {
	store.Workflow
	StepsView    []workflowStepView `json:"steps_view"`
	ScheduleDesc string             `json:"schedule_desc"`
	LastStatus   string             `json:"last_status"`
	LastEndTime  string             `json:"last_end_time"`
	LastDuration int64              `json:"last_duration_ms"`
	Running      bool               `json:"running"`
}

// stepPayload 步骤定义：内联命令/项目/环境，像「新建任务」但不带调度方式。
type stepPayload struct {
	TaskID            int64  `json:"task_id"` // 编辑已有步骤时回传（0 = 新步骤）
	Name              string `json:"name"`
	Command           string `json:"command"`
	Project           string `json:"project"`
	Workdir           string `json:"workdir"`
	EnvID             int64  `json:"env_id"`
	ContinueOnFailure bool   `json:"continue_on_failure"`
}

type workflowPayload struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	CronExpr    string        `json:"cron_expr"`
	Enabled     bool          `json:"enabled"`
	Steps       []stepPayload `json:"steps"`
}

// validateStep 校验并规范化单个步骤定义。
func validateStep(st *stepPayload, idx int) error {
	st.Name = strings.TrimSpace(st.Name)
	st.Command = strings.TrimSpace(st.Command)
	st.Project = strings.TrimSpace(st.Project)
	st.Workdir = strings.TrimSpace(st.Workdir)
	if st.Name == "" {
		st.Name = fmt.Sprintf("步骤%d", idx)
	}
	if st.Command == "" {
		return fmt.Errorf("第 %d 个步骤（%s）的命令不能为空", idx, st.Name)
	}
	if st.Workdir != "" {
		st.Workdir = filepath.ToSlash(filepath.Clean(st.Workdir))
		if st.Workdir == ".." || strings.HasPrefix(st.Workdir, "../") {
			return fmt.Errorf("第 %d 个步骤的工作路径不能超出项目目录", idx)
		}
	}
	return nil
}

func (p *workflowPayload) validate() error {
	p.Name = strings.TrimSpace(p.Name)
	p.Description = strings.TrimSpace(p.Description)
	p.CronExpr = strings.TrimSpace(p.CronExpr)
	if p.Name == "" {
		return errors.New("流程名称不能为空")
	}
	if len(p.Steps) == 0 {
		return errors.New("流程至少需要一个步骤")
	}
	if len(p.Steps) > 100 {
		return errors.New("步骤数不能超过 100")
	}
	for i := range p.Steps {
		if err := validateStep(&p.Steps[i], i+1); err != nil {
			return err
		}
	}
	if p.CronExpr != "" {
		if _, err := cron.ParseStandard(p.CronExpr); err != nil {
			return errors.New("Cron 表达式不合法：" + err.Error())
		}
	}
	if p.Enabled && p.CronExpr == "" {
		return errors.New("启用定时调度需要填写 Cron 表达式")
	}
	return nil
}

// materializeSteps 把步骤定义落成内部任务（manual 调度、workflow_id 标记），
// 返回顺序化的 steps JSON；不在引用列表中的旧步骤任务被删除。
func (s *Server) materializeSteps(wfID int64, steps []stepPayload) (string, error) {
	old := map[int64]*store.Task{}
	if list, err := s.st.ListTasksByWorkflow(wfID); err == nil {
		for _, t := range list {
			old[t.ID] = t
		}
	}

	keep := make([]int64, 0, len(steps))
	out := make([]store.WorkflowStep, 0, len(steps))
	for i := range steps {
		sp := &steps[i]
		// task_id 有效且确实属于本流程 → 更新；否则新建
		if sp.TaskID > 0 && old[sp.TaskID] != nil {
			t := old[sp.TaskID]
			t.Name, t.Command, t.Project, t.Workdir, t.EnvID = sp.Name, sp.Command, sp.Project, sp.Workdir, sp.EnvID
			if err := s.st.UpdateTask(t); err != nil {
				return "", err
			}
		} else {
			id, err := s.st.CreateTask(&store.Task{
				Name: sp.Name, Command: sp.Command, Project: sp.Project, Workdir: sp.Workdir,
				EnvID: sp.EnvID, ScheduleType: "manual", MaxConcurrent: 1,
				Enabled: true, WorkflowID: wfID,
			})
			if err != nil {
				return "", err
			}
			sp.TaskID = id
		}
		keep = append(keep, sp.TaskID)
		out = append(out, store.WorkflowStep{TaskID: sp.TaskID, ContinueOnFailure: sp.ContinueOnFailure})
	}

	// 清理不再引用的步骤任务
	stale, err := s.st.DeleteWorkflowTasksNotIn(wfID, keep)
	if err != nil {
		return "", err
	}
	for _, id := range stale {
		_ = os.RemoveAll(filepath.Join(config.TaskLogsDir, fmt.Sprint(id)))
	}

	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (s *Server) buildWorkflowViews(wfs []*store.Workflow) []*workflowView {
	envNames := map[int64]string{}
	if envs, err := s.st.ListEnvs(); err == nil {
		for _, e := range envs {
			envNames[e.ID] = e.Name
		}
	}
	last := map[int64]*store.WorkflowRun{}
	if m, err := s.st.LastWorkflowRunPerWorkflow(); err == nil {
		last = m
	}
	out := make([]*workflowView, 0, len(wfs))
	for _, wf := range wfs {
		v := &workflowView{Workflow: *wf, StepsView: []workflowStepView{}}
		taskByID := map[int64]*store.Task{}
		if tasks, err := s.st.ListTasksByWorkflow(wf.ID); err == nil {
			for _, t := range tasks {
				taskByID[t.ID] = t
			}
		}
		for _, st := range store.ParseWorkflowSteps(wf.Steps) {
			sv := workflowStepView{TaskID: st.TaskID, ContinueOnFailure: st.ContinueOnFailure}
			if t, ok := taskByID[st.TaskID]; ok {
				sv.TaskName = t.Name
				sv.Command = t.Command
				sv.Project = t.Project
				sv.Workdir = t.Workdir
				sv.EnvID = t.EnvID
				sv.EnvName = envNames[t.EnvID]
			} else {
				sv.Missing = true
			}
			v.StepsView = append(v.StepsView, sv)
		}
		if wf.Enabled && wf.CronExpr != "" {
			v.ScheduleDesc = "Cron · " + wf.CronExpr
		} else {
			v.ScheduleDesc = "未启用定时"
		}
		if lr, ok := last[wf.ID]; ok {
			v.LastStatus = lr.Status
			v.LastEndTime = lr.EndTime
			v.LastDuration = lr.DurationMS
		}
		v.Running, _ = s.st.HasRunningWorkflowRun(wf.ID)
		out = append(out, v)
	}
	return out
}

func (s *Server) listWorkflows(w http.ResponseWriter, r *http.Request) {
	wfs, err := s.st.ListWorkflows()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflows": s.buildWorkflowViews(wfs)})
}

func (s *Server) createWorkflow(w http.ResponseWriter, r *http.Request) {
	var p workflowPayload
	if err := readJSON(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败："+err.Error())
		return
	}
	if err := p.validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	wf := &store.Workflow{Name: p.Name, Description: p.Description, CronExpr: p.CronExpr, Enabled: p.Enabled}
	id, err := s.st.CreateWorkflow(wf)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	wf.ID = id
	stepsJSON, err := s.materializeSteps(id, p.Steps)
	if err != nil {
		_ = s.st.DeleteWorkflow(id)
		writeErr(w, http.StatusInternalServerError, "步骤保存失败："+err.Error())
		return
	}
	wf.Steps = stepsJSON
	if err := s.st.UpdateWorkflow(wf); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.wf.Sync(id)
	writeJSON(w, http.StatusOK, map[string]any{"workflow": s.buildWorkflowViews([]*store.Workflow{wf})[0]})
}

func (s *Server) updateWorkflow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "流程 id 不合法")
		return
	}
	old, err := s.st.GetWorkflow(id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	var p workflowPayload
	if err := readJSON(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败："+err.Error())
		return
	}
	if err := p.validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	stepsJSON, err := s.materializeSteps(id, p.Steps)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "步骤保存失败："+err.Error())
		return
	}
	wf := &store.Workflow{
		ID: old.ID, Name: p.Name, Description: p.Description,
		CronExpr: p.CronExpr, Enabled: p.Enabled, Steps: stepsJSON,
		LastFire: old.LastFire, CreatedAt: old.CreatedAt,
	}
	if err := s.st.UpdateWorkflow(wf); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.wf.Sync(id)
	writeJSON(w, http.StatusOK, map[string]any{"workflow": s.buildWorkflowViews([]*store.Workflow{wf})[0]})
}

func (s *Server) deleteWorkflow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "流程 id 不合法")
		return
	}
	if _, err := s.st.GetWorkflow(id); err != nil {
		writeStoreErr(w, err)
		return
	}
	s.wf.Kill(id) // 终止运行中的实例
	s.wf.Sync(id) // 停止调度
	// 删除流程的步骤任务及其日志目录（运行记录保留，任务名已快照）
	staleIDs, _ := s.st.DeleteTasksByWorkflow(id)
	for _, tid := range staleIDs {
		s.rn.KillTask(tid)
		_ = os.RemoveAll(filepath.Join(config.TaskLogsDir, fmt.Sprint(tid)))
	}
	if err := s.st.DeleteWorkflow(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) runWorkflow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "流程 id 不合法")
		return
	}
	wf, err := s.st.GetWorkflow(id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if err := s.wf.Trigger(wf, "manual"); err != nil {
		if errors.Is(err, workflow.ErrRunning) {
			writeErr(w, http.StatusConflict, "流程已有运行中的实例，请稍后再试或先终止")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) pauseWorkflow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "流程 id 不合法")
		return
	}
	if err := s.st.SetWorkflowEnabled(id, false); err != nil {
		writeStoreErr(w, err)
		return
	}
	s.wf.Sync(id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) resumeWorkflow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "流程 id 不合法")
		return
	}
	wf, err := s.st.GetWorkflow(id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if wf.CronExpr == "" {
		writeErr(w, http.StatusBadRequest, "请先在流程中配置 Cron 表达式")
		return
	}
	if _, err := cron.ParseStandard(wf.CronExpr); err != nil {
		writeErr(w, http.StatusBadRequest, "Cron 表达式不合法："+err.Error())
		return
	}
	if err := s.st.SetWorkflowEnabled(id, true); err != nil {
		writeStoreErr(w, err)
		return
	}
	s.wf.Sync(id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) killWorkflow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "流程 id 不合法")
		return
	}
	killed := s.wf.Kill(id)
	writeJSON(w, http.StatusOK, map[string]any{"killed": killed})
}

func (s *Server) workflowRuns(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "流程 id 不合法")
		return
	}
	items, total, err := s.st.ListWorkflowRuns(store.WorkflowRunFilter{
		WorkflowID: id,
		Page:       queryInt(r, "page", 1),
		PageSize:   queryInt(r, "page_size", 20),
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if items == nil {
		items = []*store.WorkflowRun{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": total, "items": items})
}

func (s *Server) getWorkflowRun(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "流程运行 id 不合法")
		return
	}
	run, err := s.st.GetWorkflowRun(id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run})
}

func (s *Server) deleteWorkflowRun(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "流程运行 id 不合法")
		return
	}
	if err := s.st.DeleteWorkflowRun(id); err != nil {
		if err.Error() == "运行中的流程实例不允许删除" {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
